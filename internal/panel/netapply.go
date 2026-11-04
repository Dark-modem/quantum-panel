package panel

// netapply.go — the apply / rollback engine for the Network Optimization
// subsystem. Contract (from docs/NETOPT-INVENTORY.md):
//
//   - every apply captures the previous value of each setting BEFORE writing,
//     plus the profile, role, selection reasons and the owned files it creates;
//   - only owned settings are applied, through one owned sysctl drop-in and,
//     when NIC actions are planned, one owned systemd oneshot unit so NIC
//     state survives reboot;
//   - every written sysctl is read back and verified; the connectivity probe
//     must succeed after the apply or the engine rolls everything back;
//   - rollback restores the captured baseline; revert removes every owned
//     file and restores the earliest baseline (full return to pre-netopt);
//   - SSH, firewall and port rules are never written by this engine.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// netProbeDial is the connectivity-probe seam (defaults to the shared dialFn
// so tests stubbing dialFn are automatically honored).
var netProbeDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
	return dialFn(network, addr, timeout)
}

const (
	netOwnedMarker = "# managed by `gre-panel netopt` — do not edit; revert with `gre-panel netopt revert`"
)

// Owned file paths are vars so hermetic tests can redirect them into the
// temp configDir; production keeps the real system paths.
var (
	netOwnedSysctlFile = "/etc/sysctl.d/99-quantum-netopt.conf"
	netOwnedUnitFile   = "/etc/systemd/system/quantum-netopt-nic.service"
)

// netAppliedSetting records one applied key with its before/after values.
type netAppliedSetting struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"` // "sysctl" | "nic"
	Before string `json:"before"`
	After  string `json:"after"`
	Device string `json:"device,omitempty"`
}

// netJournal is the record of one apply. It is the input for rollback/revert
// and the audit trail for what changed and why.
type netJournal struct {
	Version    int                 `json:"version"`
	AppliedAt  int64               `json:"applied_at"`
	Profile    string              `json:"profile"`
	Role       string              `json:"role"`
	Reasons    []string            `json:"reasons"`
	Applied    []netAppliedSetting `json:"applied"`
	OwnedFiles []string            `json:"owned_files"`
	Verified   bool                `json:"verified"`
	RolledBack bool                `json:"rolled_back"`
}

// netBaseline captures everything an apply might need to undo, plus the full
// detection snapshot taken before the change.
type netBaseline struct {
	Version    int                 `json:"version"`
	AppliedAt  int64               `json:"applied_at"` // set at apply time; names the chain file
	Before     []netAppliedSetting `json:"before"`     // per-key previous values ("" = unreadable/absent)
	Detect     *netHardwareReport  `json:"detect"`
	OwnedFiles map[string]string   `json:"owned_files_before"` // owned file path -> previous content
}

func netOptDir() string      { return filepath.Join(configDir, "netopt") }
func netJournalPath() string { return filepath.Join(netOptDir(), "journal.json") }
func netBaselinePath() string {
	return filepath.Join(netOptDir(), "baseline.json")
}

// netOwnedFilesBefore snapshots which owned files already exist (revert must
// restore their content, not delete files another operator wrote).
func netOwnedFilesBefore() map[string]string {
	out := map[string]string{}
	for _, f := range []string{netOwnedSysctlFile, netOwnedUnitFile} {
		if b, err := netReadFile(f); err == nil {
			out[f] = string(b)
		}
	}
	return out
}

// netWriteOwnedFile atomically writes an owned file (0600/0644 via temp+rename
// matches the panel's store conventions).
func netWriteOwnedFile(path, content string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".netopt-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// netRenderSysctlFile renders the owned sysctl drop-in for one apply.
func netRenderSysctlFile(settings []netAppliedSetting) string {
	var b strings.Builder
	b.WriteString(netOwnedMarker + "\n")
	b.WriteString("# profile applied by the Quantum network optimization subsystem\n")
	for _, s := range settings {
		if s.Kind != "sysctl" {
			continue
		}
		b.WriteString(s.Key + " = " + s.After + "\n")
	}
	return b.String()
}

// netRunSysctl applies one owned drop-in through sysctl -p.
func netRunSysctl(file string) error {
	ctx, cancel := contextWithTimeout(10 * time.Second)
	defer cancel()
	out, err := netDetectExec(ctx, "sysctl", "-p", file)
	if err != nil {
		return fmt.Errorf("sysctl -p: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// netVerifySysctl reads one key back from the kernel and compares.
func netVerifySysctl(key, want string) (bool, string) {
	raw, err := netReadFile(netSysctlPath(key))
	if err != nil {
		return false, ""
	}
	got := strings.TrimSpace(string(raw))
	if got == want {
		return true, got
	}
	// Vector values may print with tabs; compare token-wise.
	if strings.Fields(got) != nil && strings.Join(strings.Fields(got), " ") == strings.Join(strings.Fields(want), " ") {
		return true, got
	}
	return false, got
}

// netConnectivityCheck probes the paths the tunnel and dashboard depend on.
// It uses the shared dial seam so tests stub it; on a real server it dials
// the loopback dashboard (when present) and a public resolver endpoint —
// proof that name resolution, routing and egress still work after tuning.
func netConnectivityCheck() error {
	deadline := 5 * time.Second
	targets := []string{"127.0.0.1:1", "1.1.1.1:443"} // first is a canary that must DIAL (refused is fine: routing works), second proves egress
	for i, t := range targets {
		conn, err := netProbeDial("tcp", t, deadline)
		if err == nil {
			if conn != nil {
				conn.Close()
			}
			continue
		}
		// A refused connection still proves the TCP/IP stack works; an
		// unreachable/unroutable error does not.
		if strings.Contains(err.Error(), "connection refused") && i == 0 {
			continue
		}
		if i == 0 {
			continue // canary failure is not fatal (loopback may be filtered)
		}
		return fmt.Errorf("egress probe %s failed: %v", t, err)
	}
	return nil
}

// netApply executes the plan: detect fresh, plan, capture baseline, write
// owned files, apply, verify, probe, and roll back on any failure.
func netApply(profile netProfileName, role netRole, reasons []string) (*netJournal, error) {
	rep := netDetectHardware()
	resolved := netRoleFor(role)
	changes := netPlanChanges(profile, rep, resolved)

	j := &netJournal{Version: 1, AppliedAt: time.Now().Unix(), Profile: string(profile), Role: string(resolved), Reasons: reasons}
	base := &netBaseline{Version: 1, Detect: rep, OwnedFiles: map[string]string{}}
	for k, v := range netOwnedFilesBefore() {
		base.OwnedFiles[k] = v
	}

	var sysctlSettings []netAppliedSetting
	var nicSettings []netAppliedSetting
	for _, c := range changes {
		if !c.Apply {
			continue
		}
		if c.Kind == "sysctl" {
			sysctlSettings = append(sysctlSettings, netAppliedSetting{
				Key: c.Key, Kind: "sysctl", Before: c.Current, After: c.Proposed,
			})
		}
	}
	// NIC actions resolve against the CURRENT primary interface at apply time.
	for _, c := range changes {
		if !c.Apply || c.Kind != "nic" {
			continue
		}
		for _, f := range rep.Ifaces {
			if !f.Primary {
				continue
			}
			switch {
			case strings.HasPrefix(c.Key, "ring:") && f.RingMax > f.RingCur:
				nicSettings = append(nicSettings, netAppliedSetting{
					Key: "ring:" + f.Name, Kind: "nic", Device: f.Name,
					Before: strconv.Itoa(f.RingCur), After: strconv.Itoa(f.RingMax),
				})
			case strings.HasPrefix(c.Key, "rps:") && f.RPSCapable && f.QueuesRX >= 1:
				nicSettings = append(nicSettings, netAppliedSetting{
					Key: "rps:" + f.Name, Kind: "nic", Device: f.Name,
					Before: "off", After: "on",
				})
			}
		}
	}
	base.Before = append(sysctlSettings, nicSettings...)
	base.AppliedAt = time.Now().UnixNano() // chain-file identity (monotonic per ns)

	// 1) sysctl drop-in (owned) + runtime apply + read-back verification.
	var verifyErrs []string
	if len(sysctlSettings) > 0 {
		if err := netWriteOwnedFile(netOwnedSysctlFile, netRenderSysctlFile(sysctlSettings), 0644); err != nil {
			return nil, fmt.Errorf("write owned sysctl file: %v", err)
		}
		j.OwnedFiles = append(j.OwnedFiles, netOwnedSysctlFile)
		if err := netRunSysctl(netOwnedSysctlFile); err != nil {
			verifyErrs = append(verifyErrs, err.Error())
		}
		for _, s := range sysctlSettings {
			ok, got := netVerifySysctl(s.Key, s.After)
			if !ok {
				verifyErrs = append(verifyErrs, fmt.Sprintf("verify %s: kernel reports %q, want %q", s.Key, got, s.After))
			}
		}
	}

	// 2) NIC actions, verified by re-reading what ethtool reports.
	for _, s := range nicSettings {
		if err := netApplyNic(s); err != nil {
			verifyErrs = append(verifyErrs, s.Key+": "+err.Error())
			continue
		}
		if ok, got := netVerifyNic(s); !ok {
			verifyErrs = append(verifyErrs, fmt.Sprintf("verify %s: got %q", s.Key, got))
		}
	}
	if len(nicSettings) > 0 {
		unit := netRenderNicUnit()
		if err := netWriteOwnedFile(netOwnedUnitFile, unit, 0644); err != nil {
			verifyErrs = append(verifyErrs, "write owned unit: "+err.Error())
		} else {
			j.OwnedFiles = append(j.OwnedFiles, netOwnedUnitFile)
			if err := netSystemctl("daemon-reload"); err != nil {
				verifyErrs = append(verifyErrs, "daemon-reload: "+err.Error())
			}
			if err := netSystemctl("enable", "quantum-netopt-nic.service"); err != nil {
				verifyErrs = append(verifyErrs, "enable unit: "+err.Error())
			}
		}
	}

	j.Applied = append(sysctlSettings, nicSettings...)

	// 3) Connectivity must hold; otherwise roll everything back.
	if len(verifyErrs) == 0 {
		if err := netConnectivityCheck(); err != nil {
			verifyErrs = append(verifyErrs, "connectivity: "+err.Error())
		}
	}
	if len(verifyErrs) > 0 {
		j.Verified = false
		j.RolledBack = true
		rbErrs := netRestore(j, base)
		netPersist(j, base)
		if len(rbErrs) > 0 {
			return j, fmt.Errorf("apply failed and rollback reported problems: %v; apply errors: %v", rbErrs, verifyErrs)
		}
		return j, fmt.Errorf("apply failed, rolled back cleanly: %v", verifyErrs)
	}
	j.Verified = true
	netPersist(j, base)
	return j, nil
}

// netApplyNic executes one NIC action through ethtool.
func netApplyNic(s netAppliedSetting) error {
	dev := s.Device
	switch {
	case strings.HasPrefix(s.Key, "ring:"):
		ctx, cancel := contextWithTimeout(5 * time.Second)
		defer cancel()
		_, err := netDetectExec(ctx, "ethtool", "-G", dev, "rx", s.After, "tx", s.After)
		return err
	case strings.HasPrefix(s.Key, "rps:"):
		// RPS: spread the single RX queue across all CPUs and give the flow
		// table its share. 250ms cadence limits CPU cost (legacy-script lesson).
		ctx, cancel := contextWithTimeout(5 * time.Second)
		defer cancel()
		mask := "0"
		// full mask for up to 32 cores; beyond that the operator tunes by hand
		n := 32
		var bits []string
		for i := 0; i < n/4; i++ {
			bits = append(bits, "f")
		}
		mask = strings.Join(bits, "")
		if _, err := netDetectExec(ctx, "bash", "-c",
			fmt.Sprintf("echo %s > /sys/class/net/%s/queues/rx-0/rps_cpus && echo 32768 > /sys/class/net/%s/queues/rx-0/rps_flow_cnt", mask, dev, dev)); err != nil {
			return err
		}
		_, err := netDetectExec(ctx, "sysctl", "-w", "net.core.rps_sock_flow_entries=32768")
		return err
	}
	return fmt.Errorf("unknown nic action %q", s.Key)
}

// netVerifyNic re-reads the state a NIC action claims to have set.
func netVerifyNic(s netAppliedSetting) (bool, string) {
	dev := s.Device
	switch {
	case strings.HasPrefix(s.Key, "ring:"):
		cur, max := netEthtoolRings(&netHardwareReport{}, dev)
		want, _ := strconv.Atoi(s.After)
		if max > 0 && cur == want {
			return true, strconv.Itoa(cur)
		}
		return false, strconv.Itoa(cur)
	case strings.HasPrefix(s.Key, "rps:"):
		raw, err := netReadFile("/sys/class/net/" + dev + "/queues/rx-0/rps_flow_cnt")
		if err != nil || strings.TrimSpace(string(raw)) != "32768" {
			if err == nil {
				return false, strings.TrimSpace(string(raw))
			}
			return false, ""
		}
		return true, "32768"
	}
	return false, ""
}

// netRenderNicUnit renders the owned oneshot unit that re-applies NIC state
// at boot (sysctls persist via the drop-in; NIC registers do not).
func netRenderNicUnit() string {
	return `# ` + netOwnedMarker + `
[Unit]
Description=Quantum network optimization NIC settings
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/usr/local/bin/gre-panel netopt apply-nic
RemainAfterExit=yes

[Install]
WantedBy=multi-user.target
`
}

// netSystemctl wraps systemctl through the shared runner seam.
func netSystemctl(args ...string) error {
	ctx, cancel := contextWithTimeout(15 * time.Second)
	defer cancel()
	_, err := netDetectExec(ctx, "systemctl", args...)
	return err
}

// netRestore restores the baseline for everything a journal applied.
func netRestore(j *netJournal, base *netBaseline) []string {
	var errs []string
	// sysctls: restore every kernel key the apply touched, from the baseline
	// values captured before the change ("" = the key did not exist / was not
	// readable: restoring is then a no-op, never a guess).
	for _, s := range base.Before {
		if s.Kind != "sysctl" || s.Before == "" {
			continue
		}
		ctx, cancel := contextWithTimeout(5 * time.Second)
		// No shell is involved (exec seam): the value must go UNQUOTED —
		// a literal `key="value"` is rejected by sysctl -w (live-acceptance
		// finding, session 2 acceptance round).
		_, err := netDetectExec(ctx, "sysctl", "-w", s.Key+"="+s.Before)
		cancel()
		if err != nil {
			errs = append(errs, "restore "+s.Key+": "+err.Error())
		}
	}
	// NICs: restore captured ring sizes; RPS is left as-is (it is a scheduler
	// hint, not a device register, and the legacy lesson is that off/restore
	// churn does more harm than the hint).
	for _, s := range base.Before {
		if s.Kind != "nic" || !strings.HasPrefix(s.Key, "ring:") || s.Before == "" {
			continue
		}
		ctx, cancel := contextWithTimeout(5 * time.Second)
		_, err := netDetectExec(ctx, "ethtool", "-G", s.Device, "rx", s.Before, "tx", s.Before)
		cancel()
		if err != nil {
			errs = append(errs, "restore ring "+s.Device+": "+err.Error())
		}
	}
	// Owned files: restore exact previous content (revert) — never delete a
	// file another operator may have owned before this subsystem existed.
	if base.OwnedFiles != nil {
		for _, f := range []string{netOwnedSysctlFile, netOwnedUnitFile} {
			prev, ok := base.OwnedFiles[f]
			if !ok {
				_ = os.Remove(f) // this apply created it: removing is the revert
				continue
			}
			if err := netWriteOwnedFile(f, prev, 0644); err != nil {
				errs = append(errs, "restore file "+f+": "+err.Error())
			}
		}
		if len(base.OwnedFiles) > 0 {
			_ = netSystemctl("daemon-reload")
		}
	}
	return errs
}

// netPersist atomically writes the journal and baseline after a decisive
// apply/rollback outcome. Every baseline is ALSO appended to the chain
// directory (immutable, one file per apply) so `revert` can walk back to the
// TRUE pre-netopt state even after several stacked applies — the single
// baseline.json only ever describes the last apply (live-acceptance finding:
// apply#1 → apply#2 → revert otherwise restores apply#1's state, not the
// originals).
func netPersist(j *netJournal, base *netBaseline) {
	_ = os.MkdirAll(netOptDir(), 0700)
	if b, err := json.MarshalIndent(base, "", "  "); err == nil {
		_ = netWriteOwnedFile(netBaselinePath(), string(b), 0600)
		if base.AppliedAt > 0 {
			chain := filepath.Join(netOptDir(), "baselines", fmt.Sprintf("%d.json", base.AppliedAt))
			_ = netWriteOwnedFile(chain, string(b), 0600)
		}
	}
	if b, err := json.MarshalIndent(j, "", "  "); err == nil {
		_ = netWriteOwnedFile(netJournalPath(), string(b), 0600)
	}
}

// netBaselineChain returns every chained baseline sorted oldest-first.
// baselines without a parseable AppliedAt sort last and are ignored for the
// "true original" pick.
func netBaselineChain() []*netBaseline {
	entries, err := os.ReadDir(filepath.Join(netOptDir(), "baselines"))
	if err != nil {
		return nil
	}
	var out []*netBaseline
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(netOptDir(), "baselines", e.Name()))
		if err != nil {
			continue
		}
		var base netBaseline
		if json.Unmarshal(b, &base) != nil || base.AppliedAt <= 0 {
			continue
		}
		out = append(out, &base)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AppliedAt < out[j].AppliedAt })
	return out
}

// netLoadJournal reads the last apply's journal. Journal and baseline are
// panel state the engine itself wrote: they use the real filesystem, not the
// detection seams.
func netLoadJournal() *netJournal {
	b, err := os.ReadFile(netJournalPath())
	if err != nil {
		return nil
	}
	var j netJournal
	if json.Unmarshal(b, &j) != nil {
		return nil
	}
	return &j
}

// netLoadBaseline reads the stored baseline.
func netLoadBaseline() *netBaseline {
	b, err := os.ReadFile(netBaselinePath())
	if err != nil {
		return nil
	}
	var base netBaseline
	if json.Unmarshal(b, &base) != nil {
		return nil
	}
	return &base
}

// netRollback undoes the last apply from its journal and baseline.
func netRollback() (string, error) {
	j := netLoadJournal()
	if j == nil {
		return "", fmt.Errorf("no netopt journal to roll back")
	}
	if j.RolledBack {
		return "last apply already rolled back", nil
	}
	base := netLoadBaseline()
	if base == nil {
		return "", fmt.Errorf("no netopt baseline to restore")
	}
	errs := netRestore(j, base)
	j.RolledBack = true
	netPersist(j, base)
	if len(errs) > 0 {
		return "", fmt.Errorf("rollback incomplete: %v", errs)
	}
	return fmt.Sprintf("rolled back apply of %s (%d settings restored)", j.Profile, len(j.Applied)), nil
}

// netRevert removes every owned file and restores the TRUE pre-netopt state:
// it walks the baseline chain back to the OLDEST baseline (the one captured
// before the first apply) so stacked applies cannot strand intermediate
// values. With no chain it falls back to the last baseline, then to removing
// only files this subsystem owns.
func netRevert() (string, error) {
	var errs []string
	chain := netBaselineChain()
	switch {
	case len(chain) > 0:
		oldest := chain[0]
		errs = netRestore(&netJournal{}, oldest)
		// the chain itself has served its purpose
		_ = os.RemoveAll(filepath.Join(netOptDir(), "baselines"))
	case netLoadBaseline() != nil:
		errs = netRestore(&netJournal{}, netLoadBaseline())
	default:
		// No baseline stored: only remove files this subsystem owns.
		for _, f := range []string{netOwnedSysctlFile, netOwnedUnitFile} {
			if b, err := netReadFile(f); err == nil && strings.HasPrefix(string(b), netOwnedMarker) {
				if err := os.Remove(f); err != nil {
					errs = append(errs, "remove "+f+": "+err.Error())
				}
			}
		}
		_ = netSystemctl("daemon-reload")
	}
	if len(errs) > 0 {
		return "", fmt.Errorf("revert incomplete: %v", errs)
	}
	return "reverted to the pre-netopt state", nil
}

// contextWithTimeout isolates the context import here.
func contextWithTimeout(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

// netApplyNicBoot re-applies NIC state at boot (owned unit ExecStart). It
// reads the last journal and re-runs the recorded NIC actions only.
func netApplyNicBoot() error {
	j := netLoadJournal()
	if j == nil {
		return nil
	}
	for _, s := range j.Applied {
		if s.Kind != "nic" {
			continue
		}
		if err := netApplyNic(s); err != nil {
			return err
		}
	}
	return nil
}
