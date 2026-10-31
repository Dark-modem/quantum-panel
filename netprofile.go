package main

// netprofile.go — the three Network Optimization profiles (Conservative,
// Balanced, Performance) and the hardware-aware selector that turns a
// detection report into a concrete, justified change plan.
//
// Rules of the subsystem (from the legacy node-setup inventory, see
// docs/NETOPT-INVENTORY.md):
//   - measure first, then select; never one generic profile for every box;
//   - a parameter is proposed only when the kernel and hardware support it,
//     a baseline exists and the profile's recorded reason justifies it;
//   - SSH, firewall and port rules are never proposed here (Sessions 1/6);
//   - MTU is never changed automatically (the Matrix/tunnel code owns it);
//     MSS clamping stays with the tunnel code;
//   - TCP Fast Open is Performance-only: in-SYN data raises the DPI surface,
//     and DPI resilience outranks a 1-RTT win (MVP priority order);
//   - CAKE is an opt-in jitter tool (`netopt apply --qdisc cake`), not a
//     profile default: it replaces fq on the interface, and fq is what the
//     BBR path wants. Bufferbloat is a measured problem first.

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// netProfileName identifies one of the three profiles.
type netProfileName string

const (
	netProfileConservative netProfileName = "conservative"
	netProfileBalanced     netProfileName = "balanced"
	netProfilePerformance  netProfileName = "performance"
)

// netRole selects which candidates apply. "auto" resolves from the binary's
// mode: the controller runs the web dashboard, an agent does not.
type netRole string

const (
	netRoleAuto       netRole = "auto"
	netRoleController netRole = "controller"
	netRoleAgent      netRole = "agent"
)

// netCandidate is one tunable a profile may propose.
type netCandidate struct {
	Key    string // sysctl key, or a NIC action id ("ring:<if>", "rps:<if>", "channels:<if>")
	Kind   string // "sysctl" | "nic"
	Value  string // proposed value (sysctl value; NIC actions carry their target in the key)
	Roles  string // "both" | "controller" | "agent"
	Needs  string // machine-checkable requirement, "" = always (see netCandidateMet)
	Reason string // recorded in the journal as the why
	Risk   string // what can go wrong, recorded next to the reason
}

// netCandidateMet evaluates a requirement against the detection report.
// Unknown facts fail the requirement (the selector never guesses).
func netCandidateMet(c netCandidate, rep *netHardwareReport) (bool, string) {
	switch c.Needs {
	case "":
		return true, ""
	case "bbr":
		for _, cc := range rep.CCAvailable {
			if cc == "bbr" {
				return true, ""
			}
		}
		return false, "bbr not available in this kernel"
	case "ram>=2g":
		if rep.RAMTotalMB >= 2048 {
			return true, ""
		}
		return false, fmt.Sprintf("RAM %d MB < 2 GB", rep.RAMTotalMB)
	case "ram>=4g":
		if rep.RAMTotalMB >= 4096 {
			return true, ""
		}
		return false, fmt.Sprintf("RAM %d MB < 4 GB", rep.RAMTotalMB)
	case "!mem-pressure":
		if !rep.MemPressure {
			return true, ""
		}
		return false, "memory pressure (available < 15%)"
	case "physical-primary-nic":
		for _, f := range rep.Ifaces {
			if f.Primary && f.Kind == "physical" && f.RingMax > 0 && f.RingMax > f.RingCur {
				return true, ""
			}
		}
		return false, "primary NIC has no headroom in its ring buffers"
	case "rps-possible":
		for _, f := range rep.Ifaces {
			if f.Primary && f.RPSCapable && f.QueuesRX >= 1 && rep.CPUCores >= 2 {
				return true, ""
			}
		}
		return false, "primary NIC exposes no RPS-capable RX queue (or single core)"
	case "nf-conntrack":
		if _, ok := rep.Sysctls["net.netfilter.nf_conntrack_max"]; ok {
			return true, ""
		}
		return false, "nf_conntrack sysctl not present (module not loaded)"
	}
	return false, "unknown requirement " + c.Needs
}

// conntrackMaxFor scales the conntrack cap by RAM (the quantum.sh concept,
// clamped tighter than the legacy node-setup fixed 524288 on small boxes).
func conntrackMaxFor(ramMB uint64) string {
	v := ramMB * 128
	if v < 65536 {
		v = 65536
	}
	if v > 524288 {
		v = 524288
	}
	return strconv.FormatUint(v, 10)
}

// netProfileCandidates returns the candidate list for one profile. Lists are
// ordered: sysctl first, NIC actions last (applied after connectivity-critical
// sysctls verified).
func netProfileCandidates(p netProfileName) []netCandidate {
	switch p {
	case netProfileConservative:
		return []netCandidate{
			{Key: "net.core.somaxconn", Kind: "sysctl", Value: "4096", Roles: "both",
				Reason: "modest listen backlog for tunnel/web listeners", Risk: "none known"},
			{Key: "net.ipv4.tcp_max_syn_backlog", Kind: "sysctl", Value: "4096", Roles: "both",
				Reason: "matches somaxconn without deep queues", Risk: "none known"},
			{Key: "net.ipv4.tcp_mtu_probing", Kind: "sysctl", Value: "1", Roles: "both",
				Reason: "tunnel PMTU blackhole recovery", Risk: "negligible"},
			{Key: "vm.swappiness", Kind: "sysctl", Value: "10", Roles: "both",
				Reason: "keep tunnel process pages resident", Risk: "more OOM risk than default on tiny boxes"},
		}
	case netProfileBalanced:
		return []netCandidate{
			{Key: "net.core.default_qdisc", Kind: "sysctl", Value: "fq", Roles: "both",
				Reason: "fq pairs with BBR pacing", Needs: "bbr", Risk: "new connections only"},
			{Key: "net.ipv4.tcp_congestion_control", Kind: "sysctl", Value: "bbr", Roles: "both",
				Reason: "BBR on lossy/high-RTT Iran-foreign paths", Needs: "bbr", Risk: "none known"},
			{Key: "net.core.rmem_max", Kind: "sysctl", Value: "16777216", Roles: "both",
				Reason: "16 MB socket ceiling for BDR/BDP on 100ms paths", Needs: "ram>=2g", Risk: "kernel memory only when sockets ask"},
			{Key: "net.core.wmem_max", Kind: "sysctl", Value: "16777216", Roles: "both",
				Reason: "send-side counterpart of rmem_max", Needs: "ram>=2g", Risk: "same"},
			{Key: "net.ipv4.tcp_rmem", Kind: "sysctl", Value: "4096 131072 16777216", Roles: "both",
				Reason: "auto-tuned receive buffers up to the ceiling", Needs: "ram>=2g", Risk: "same"},
			{Key: "net.ipv4.tcp_wmem", Kind: "sysctl", Value: "4096 131072 16777216", Roles: "both",
				Reason: "auto-tuned send buffers up to the ceiling", Needs: "ram>=2g", Risk: "same"},
			{Key: "net.core.netdev_max_backlog", Kind: "sysctl", Value: "16384", Roles: "both",
				Reason: "burst absorption without extreme queues", Needs: "ram>=2g", Risk: "minor RAM"},
			{Key: "net.core.somaxconn", Kind: "sysctl", Value: "8192", Roles: "both",
				Reason: "headroom for dashboard + tunnel listeners", Risk: "none known"},
			{Key: "net.ipv4.tcp_max_syn_backlog", Kind: "sysctl", Value: "8192", Roles: "both",
				Reason: "matches somaxconn", Risk: "none known"},
			{Key: "net.ipv4.tcp_mtu_probing", Kind: "sysctl", Value: "1", Roles: "both",
				Reason: "tunnel PMTU blackhole recovery", Risk: "negligible"},
			{Key: "net.ipv4.tcp_keepalive_time", Kind: "sysctl", Value: "600", Roles: "both",
				Reason: "calm TCP keepalives: liveness is app-level (agent heartbeat); fewer periodic probes on DPI'd paths", Risk: "slower dead-peer detection for pure-TCP apps relying on keepalive"},
			{Key: "net.ipv4.tcp_keepalive_intvl", Kind: "sysctl", Value: "30", Roles: "both",
				Reason: "matches the calm cadence", Risk: "same"},
			{Key: "net.ipv4.tcp_keepalive_probes", Kind: "sysctl", Value: "5", Roles: "both",
				Reason: "matches the calm cadence", Risk: "same"},
			{Key: "net.ipv4.ip_local_reserved_ports", Kind: "sysctl", Value: "RESOLVED_SERVICE_PORTS", Roles: "both",
				Reason: "keep service ports (panel/matrix/FRP/Backhaul) out of the ephemeral range", Risk: "must be refreshed when service ports change"},
			{Key: "net.netfilter.nf_conntrack_max", Kind: "sysctl", Value: "RAM_SCALED_CONNTRACK", Roles: "both",
				Reason: "RAM-scaled conntrack headroom (fixed caps from the legacy script OOM small boxes)", Needs: "nf-conntrack", Risk: "~300 B/entry worst case"},
			{Key: "vm.swappiness", Kind: "sysctl", Value: "10", Roles: "both",
				Reason: "keep tunnel process pages resident", Risk: "more OOM risk than default on tiny boxes"},
		}
	case netProfilePerformance:
		return append([]netCandidate{
			{Key: "net.core.netdev_max_backlog", Kind: "sysctl", Value: "65535", Roles: "both",
				Reason: "deep burst queue for measured high-BDP links", Needs: "ram>=4g", Risk: "RAM under flood; only on justified hardware"},
			{Key: "net.core.somaxconn", Kind: "sysctl", Value: "16384", Roles: "controller",
				Reason: "controller serves many concurrent sessions", Needs: "ram>=4g", Risk: "none known"},
			{Key: "net.ipv4.tcp_notsent_lowat", Kind: "sysctl", Value: "131072", Roles: "both",
				Reason: "bound unsent send-buffer queueing latency", Needs: "ram>=4g", Risk: "more syscalls at high throughput"},
			{Key: "net.ipv4.tcp_fastopen", Kind: "sysctl", Value: "3", Roles: "both",
				Reason: "1 RTT on repeated connections; Performance-only because in-SYN data raises the DPI surface", Risk: "middleboxes may drop TFO SYN (blackhole timer mitigates)"},
			{Key: "ring:<primary>", Kind: "nic", Value: "max", Roles: "both",
				Reason: "raise RX ring to NIC maximum on the measured primary interface", Needs: "physical-primary-nic", Risk: "slightly more RAM per ring"},
			{Key: "rps:<primary>", Kind: "nic", Value: "on", Roles: "both",
				Reason: "steer single-queue NIC interrupts across cores", Needs: "rps-possible", Risk: "cross-CPU cache traffic if misconfigured"},
		}, conservativeForPerf()...)
	}
	return nil
}

// conservativeForPerf is the baseline every performance profile also applies:
// Performance is Balanced plus measured-hardware extras, never a replacement
// for the stable core.
func conservativeForPerf() []netCandidate {
	var out []netCandidate
	for _, c := range netProfileCandidates(netProfileBalanced) {
		if c.Key == "net.core.netdev_max_backlog" || c.Key == "net.core.somaxconn" {
			continue // overridden above with deeper values
		}
		out = append(out, c)
	}
	return out
}

// netReservedPortsValue collects the known service ports from the same
// configuration quantum.sh's reserve_service_ports reads (panel.json, FRP
// server configs, Backhaul configs, agent tunnels), so the profile never
// pushes a service port into the ephemeral range. Returns "" when nothing is
// known — the candidate is then skipped, never guessed.
func netReservedPortsValue() string {
	set := map[string]bool{}
	add := func(s string) {
		for _, p := range netNumsIn(s) {
			n, err := strconv.Atoi(p)
			if err == nil && n >= 1024 && n <= 65535 {
				set[p] = true
			}
		}
	}
	if b, err := netReadFile(filepath.Join(configDir, "panel.json")); err == nil {
		s := string(b)
		for _, key := range []string{`"port"`, `"tls_port"`} {
			if i := strings.Index(s, key); i >= 0 {
				add(s[i+len(key):])
			}
		}
	}
	for _, dir := range []string{configDir, filepath.Join(configDir, "backhaul")} {
		entries, err := netReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			isFRP := strings.HasPrefix(name, "frps") && strings.HasSuffix(name, ".toml")
			isBH := dir != configDir && strings.HasSuffix(name, ".toml")
			if !isFRP && !isBH {
				continue
			}
			b, err := netReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.Contains(line, "bindPort") || strings.Contains(line, "quicBindPort") || strings.Contains(line, "bind_addr") {
					add(line)
				}
			}
		}
	}
	if b, err := netReadFile(filepath.Join(configDir, "agent_tunnels.json")); err == nil {
		add(string(b))
	}
	ports := make([]string, 0, len(set))
	for p := range set {
		ports = append(ports, p)
	}
	sort.Strings(ports)
	return strings.Join(ports, ",")
}

// netNumsIn returns every integer token in s, in order of appearance.
func netNumsIn(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		if s[i] >= '0' && s[i] <= '9' {
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			out = append(out, s[i:j])
			i = j
			continue
		}
		i++
	}
	return out
}

// netSelectProfile chooses the profile from measured hardware. The decision
// path is deliberately explainable: the returned reasons are recorded in the
// journal and shown by `netopt plan`.
func netSelectProfile(rep *netHardwareReport, role netRole, forced string) (netProfileName, []string) {
	var why []string
	if forced != "" {
		why = append(why, "operator forced profile "+forced)
		switch netProfileName(forced) {
		case netProfileConservative, netProfileBalanced, netProfilePerformance:
			return netProfileName(forced), why
		}
		why = append(why, "unknown profile "+forced+"; falling back to measured selection")
	}

	// Anything unreadable or tight on memory drops to Conservative — the
	// profile that is safe on a box we do not fully understand.
	if len(rep.Warnings) > 3 {
		why = append(why, fmt.Sprintf("%d detection warnings; facts incomplete", len(rep.Warnings)))
		return netProfileConservative, why
	}
	if rep.MemPressure {
		why = append(why, fmt.Sprintf("memory pressure: available %.0f%%", rep.MemAvailPct))
		return netProfileConservative, why
	}
	if rep.RAMTotalMB > 0 && rep.RAMTotalMB < 2048 {
		why = append(why, fmt.Sprintf("RAM %d MB < 2 GB", rep.RAMTotalMB))
		return netProfileConservative, why
	}
	if rep.VirtType == "unknown" {
		why = append(why, "virtualization type unknown")
		return netProfileConservative, why
	}

	// Performance needs the full hardware case: BBR present, enough cores and
	// RAM, and a physical NIC with measurable headroom. Virtual machines get
	// Balanced — their NICs are paravirtual and host-shaped.
	perfOK := true
	hasBBR := false
	for _, cc := range rep.CCAvailable {
		if cc == "bbr" {
			hasBBR = true
		}
	}
	if !hasBBR {
		perfOK = false
		why = append(why, "bbr unavailable")
	}
	if rep.CPUCores < 4 {
		perfOK = false
		why = append(why, fmt.Sprintf("%d cores < 4", rep.CPUCores))
	}
	if rep.RAMTotalMB < 8192 {
		perfOK = false
		why = append(why, fmt.Sprintf("RAM %d MB < 8 GB", rep.RAMTotalMB))
	}
	if rep.VirtType != "none" {
		perfOK = false
		why = append(why, "virtualized NIC/host shaping: Performance extras limited")
	}
	physical := false
	for _, f := range rep.Ifaces {
		if f.Kind == "physical" {
			physical = true
		}
	}
	if !physical {
		perfOK = false
		why = append(why, "no physical NIC detected")
	}
	if perfOK {
		why = append(why, "hardware case met: bbr, >=4 cores, >=8 GB RAM, physical NIC, bare metal")
		return netProfilePerformance, why
	}
	why = append(why, "default Balanced profile for a fully-measured server")
	return netProfileBalanced, why
}

// netPlannedChange is one candidate resolved against the live system.
type netPlannedChange struct {
	Candidate  netCandidate `json:"-"`
	Key        string       `json:"key"`
	Kind       string       `json:"kind"`
	Current    string       `json:"current"`
	Proposed   string       `json:"proposed"`
	Apply      bool         `json:"apply"` // false = skip, see SkipReason
	SkipReason string       `json:"skip_reason,omitempty"`
	Reason     string       `json:"reason"`
	Risk       string       `json:"risk"`
}

// netResolveValue expands the dynamic placeholders in candidate values
// (RAM-scaled conntrack, config-reserved service ports) against the live
// system and configuration.
func netResolveValue(c netCandidate, rep *netHardwareReport) string {
	switch c.Value {
	case "RAM_SCALED_CONNTRACK":
		return conntrackMaxFor(rep.RAMTotalMB)
	case "RESOLVED_SERVICE_PORTS":
		return netReservedPortsValue()
	}
	return c.Value
}

// netPlanChanges resolves every candidate of the profile against the report:
// role filtering, requirement checks, and no-op skipping. The result is what
// `netopt apply` would record and apply — nothing else.
func netPlanChanges(p netProfileName, rep *netHardwareReport, role netRole) []netPlannedChange {
	var out []netPlannedChange
	for _, c := range netProfileCandidates(p) {
		if role == netRoleController && c.Roles == "agent" {
			continue
		}
		if role == netRoleAgent && c.Roles == "controller" {
			continue
		}
		pc := netPlannedChange{
			Candidate: c, Key: c.Key, Kind: c.Kind,
			Current: rep.Sysctls[c.Key], Reason: c.Reason, Risk: c.Risk,
		}
		pc.Proposed = netResolveValue(c, rep)
		if pc.Proposed == "" {
			pc.Apply, pc.SkipReason = false, "no value resolved (e.g. no known service ports)"
			out = append(out, pc)
			continue
		}
		if ok, why := netCandidateMet(c, rep); !ok {
			pc.Apply, pc.SkipReason = false, why
			out = append(out, pc)
			continue
		}
		if pc.Kind == "sysctl" && pc.Current == pc.Proposed {
			pc.Apply, pc.SkipReason = false, "already at proposed value"
			out = append(out, pc)
			continue
		}
		pc.Apply = true
		out = append(out, pc)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == "sysctl" // sysctls first
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// netProfileSummary is the one-line plan digest for CLI output.
func netProfileSummary(p netProfileName, changes []netPlannedChange) string {
	apply, skip := 0, 0
	for _, c := range changes {
		if c.Apply {
			apply++
		} else {
			skip++
		}
	}
	return fmt.Sprintf("profile=%s apply=%d skip=%d", p, apply, skip)
}

// netRoleFor resolves "auto" from what this installation actually is: the
// presence of the agent's persisted journal (agent-state.json) means the
// binary runs as an endpoint agent; otherwise it is the controller.
func netRoleFor(r netRole) netRole {
	if r != netRoleAuto {
		return r
	}
	if _, err := netStat(agentStatePath()); err == nil {
		return netRoleAgent
	}
	return netRoleController
}
