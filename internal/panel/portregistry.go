package panel

// Quantum port registry (Session 1 — Firewall and Port Management).
// A central, persistent record of which port on which transport belongs to
// whom, plus live listener detection. The registry is the source of truth
// for tunnel→port mapping and the ownership half of the firewall engine
// (fwPlanRemoveOwner reads it), so deleting a tunnel releases exactly the
// ports it held.
//
// Availability rules — a port is NOT free just because no TCP listener
// answers:
//   - TCP and UDP are independent namespaces: each transport is checked
//     against its OWN listeners, and the report always shows the other
//     transport's usage as context.
//   - Ports inside the kernel's ephemeral range are refused (the kernel
//     hands them out for outbound connections; binding a service there is
//     a conflict waiting to happen).
//   - Reserved-by-registry and live-listener checks both apply, so a
//     stopped tunnel keeps its port protected against foreign grabs.
//
// All host reads go through seams (portSSRun, portEphemeralRange) so the
// tests run hermetically on any OS; tests/test_firewall_lab.sh exercises
// the real ss/proc behavior on Linux.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- model ----

const (
	portProtoTCP = "tcp"
	portProtoUDP = "udp"
)

const (
	portOwnerTunnel   = "tunnel"   // a Matrix/agent tunnel (owner id = tunnel id)
	portOwnerService  = "service"  // a Quantum-managed service (owner id = service name)
	portOwnerExternal = "external" // an unrelated application (detection only)
)

// portEntry is one reserved port.
type portEntry struct {
	Port       int    `json:"port"`
	Proto      string `json:"proto"`      // tcp | udp
	OwnerKind  string `json:"owner_kind"` // tunnel | service | external
	OwnerID    string `json:"owner_id"`   // tunnel id / service name / app name
	Family     string `json:"family"`     // ipv4 | ipv6 | both (firewall rule scope)
	Note       string `json:"note,omitempty"`
	ReservedAt int64  `json:"reserved_at"`
}

func (e portEntry) key() string             { return e.Proto + ":" + strconv.Itoa(e.Port) }
func (e portEntry) owner() string           { return e.OwnerKind + ":" + e.OwnerID }
func portKey(proto string, port int) string { return proto + ":" + strconv.Itoa(port) }

type portRegistryFile struct {
	Version int         `json:"version"`
	Entries []portEntry `json:"entries"`
}

var (
	portRegMu   sync.Mutex
	portRegPath string
)

func portRegistryFilepath() string {
	if portRegPath == "" {
		portRegPath = filepath.Join(configDir, "port_registry.json")
	}
	return portRegPath
}

func portRegistryLoad() portRegistryFile {
	var s portRegistryFile
	data, err := os.ReadFile(portRegistryFilepath())
	if err != nil || len(data) == 0 {
		return portRegistryFile{Version: 1}
	}
	if err := json.Unmarshal(data, &s); err != nil || s.Version != 1 {
		_ = os.Rename(portRegistryFilepath(), portRegistryFilepath()+".bad")
		return portRegistryFile{Version: 1}
	}
	return s
}

func portRegistrySave(s portRegistryFile) error {
	s.Version = 1
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	p := portRegistryFilepath()
	// The config directory may not exist yet on a first-run agent; create it
	// rather than failing the whole save (dir perms follow the file's 0600
	// philosophy: private).
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ---- host reads (seams) ----

// portSSRun returns the output of `ss -Htulnp` (all TCP+UDP listeners with
// process info). Tests swap it.
var portSSRun = func() (string, error) {
	return fwRun("ss", "-Htulnp")
}

// portEphemeralRange returns the kernel's local port range; ports inside
// it must never be handed to services. Tests swap it.
var portEphemeralRange = func() (int, int) {
	data, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		return 32768, 60999
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		return 32768, 60999
	}
	lo, e1 := strconv.Atoi(fields[0])
	hi, e2 := strconv.Atoi(fields[1])
	if e1 != nil || e2 != nil || lo <= 0 || hi < lo {
		return 32768, 60999
	}
	return lo, hi
}

// portListener is one live listening socket.
type portListener struct {
	Proto   string `json:"proto"`
	Port    int    `json:"port"`
	Address string `json:"address"`
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

// portListeners parses `ss -Htulnp`.
func portListeners() ([]portListener, error) {
	raw, err := portSSRun()
	if err != nil {
		return nil, err
	}
	var listeners []portListener
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		proto := strings.ToLower(fields[0])
		if proto != portProtoTCP && proto != portProtoUDP {
			continue
		}
		local := fields[4]
		idx := strings.LastIndex(local, ":")
		if idx < 0 {
			continue
		}
		port, err := strconv.Atoi(local[idx+1:])
		if err != nil {
			continue
		}
		l := portListener{Proto: proto, Port: port, Address: local}
		if u := strings.Index(line, `users:((`); u >= 0 {
			l.Process, l.PID = parseSSUser(line[u:])
		}
		listeners = append(listeners, l)
	}
	return listeners, nil
}

// parseSSUser extracts the first process from `users:(("xray",pid=7,fd=3))`.
func parseSSUser(s string) (string, int) {
	open := strings.Index(s, "(\"")
	if open < 0 {
		return "", 0
	}
	rest := s[open+2:]
	end := strings.Index(rest, "\"")
	if end < 0 {
		return "", 0
	}
	name := rest[:end]
	pid := 0
	if i := strings.Index(rest, "pid="); i >= 0 {
		num := rest[i+4:]
		digits := 0
		for digits < len(num) && num[digits] >= '0' && num[digits] <= '9' {
			digits++
		}
		pid, _ = strconv.Atoi(num[:digits])
	}
	return name, pid
}

// ---- registry operations ----

// portReserve records a reservation. Idempotent for the same owner;
// a conflict error for a different owner; refused inside the ephemeral
// range. A fresh reservation is refused while a foreign process already
// listens on the port — the registry must reflect reality.
func portReserve(port int, proto, ownerKind, ownerID, family, note string) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port %d out of range", port)
	}
	if proto != portProtoTCP && proto != portProtoUDP {
		return fmt.Errorf("proto must be tcp or udp, got %q", proto)
	}
	switch ownerKind {
	case portOwnerTunnel, portOwnerService, portOwnerExternal:
	default:
		return fmt.Errorf("owner kind must be tunnel, service or external, got %q", ownerKind)
	}
	if strings.TrimSpace(ownerID) == "" {
		return fmt.Errorf("owner id is required")
	}
	if family == "" {
		family = "both"
	}
	if family != "ipv4" && family != "ipv6" && family != "both" {
		return fmt.Errorf("family must be ipv4, ipv6 or both, got %q", family)
	}
	lo, hi := portEphemeralRange()
	if port >= lo && port <= hi {
		return fmt.Errorf("port %d sits inside the kernel ephemeral range %d-%d and cannot be reserved", port, lo, hi)
	}

	portRegMu.Lock()
	defer portRegMu.Unlock()
	s := portRegistryLoad()
	for i := range s.Entries {
		e := s.Entries[i]
		if e.Port == port && e.Proto == proto {
			if e.owner() == ownerKind+":"+ownerID {
				// Idempotent re-reserve: refresh note/family.
				s.Entries[i].Note, s.Entries[i].Family = note, family
				return portRegistrySave(s)
			}
			return fmt.Errorf("port %d/%s is already reserved by %s:%s", port, proto, e.OwnerKind, e.OwnerID)
		}
	}
	// A foreign listener on the port blocks a fresh reservation.
	if listeners, err := portListeners(); err == nil {
		for _, l := range listeners {
			if l.Port == port && l.Proto == proto {
				return fmt.Errorf("port %d/%s is already in use by %s (pid %d)", port, proto, l.Process, l.PID)
			}
		}
	}
	s.Entries = append(s.Entries, portEntry{
		Port: port, Proto: proto, OwnerKind: ownerKind, OwnerID: ownerID,
		Family: family, Note: note, ReservedAt: time.Now().Unix(),
	})
	return portRegistrySave(s)
}

// portRelease frees one reservation. Only the owning owner may release;
// releasing a port nobody holds is a no-op.
func portRelease(port int, proto, ownerKind, ownerID string) error {
	portRegMu.Lock()
	defer portRegMu.Unlock()
	s := portRegistryLoad()
	kept := s.Entries[:0]
	released := false
	for _, e := range s.Entries {
		if e.Port == port && e.Proto == proto {
			if e.owner() != ownerKind+":"+ownerID {
				return fmt.Errorf("port %d/%s belongs to %s:%s, not %s:%s", port, proto, e.OwnerKind, e.OwnerID, ownerKind, ownerID)
			}
			released = true
			continue
		}
		kept = append(kept, e)
	}
	if !released {
		return nil // idempotent
	}
	s.Entries = kept
	return portRegistrySave(s)
}

// portReleaseOwner releases every reservation of one owner (tunnel teardown).
func portReleaseOwner(ownerKind, ownerID string) (int, error) {
	portRegMu.Lock()
	defer portRegMu.Unlock()
	s := portRegistryLoad()
	kept := s.Entries[:0]
	n := 0
	for _, e := range s.Entries {
		if e.OwnerKind == ownerKind && e.OwnerID == ownerID {
			n++
			continue
		}
		kept = append(kept, e)
	}
	if n == 0 {
		return 0, nil
	}
	s.Entries = kept
	return n, portRegistrySave(s)
}

// portRegistryRecord records ownership for a port the firewall engine just
// opened (fwApply add path). Unlike portReserve it performs NO listener or
// ephemeral-range checks — the plan already validated the port — and it
// silently upserts, so an installer-only apply (no separate ports reserve
// call) still leaves the port teardown-addressable.
func portRegistryRecord(r fwPortRule) error {
	kind, id := portOwnerExternal, r.Owner
	if k, i, err := fwAgentSplitOwner(r.Owner); err == nil {
		kind, id = k, i
	}
	portRegMu.Lock()
	defer portRegMu.Unlock()
	s := portRegistryLoad()
	for i := range s.Entries {
		if s.Entries[i].Port == r.Port && s.Entries[i].Proto == r.Proto {
			s.Entries[i].OwnerKind, s.Entries[i].OwnerID = kind, id
			s.Entries[i].Family, s.Entries[i].Note = r.Family, r.Note
			return portRegistrySave(s)
		}
	}
	s.Entries = append(s.Entries, portEntry{
		Port: r.Port, Proto: r.Proto, OwnerKind: kind, OwnerID: id,
		Family: r.Family, Note: r.Note, ReservedAt: time.Now().Unix(),
	})
	return portRegistrySave(s)
}

// portRegistryForget drops the ownership record for one port/transport
// (fwApply remove path). A missing entry is a no-op.
func portRegistryForget(proto string, port int) error {
	portRegMu.Lock()
	defer portRegMu.Unlock()
	s := portRegistryLoad()
	kept := s.Entries[:0]
	dropped := false
	for _, e := range s.Entries {
		if e.Port == port && e.Proto == proto {
			dropped = true
			continue
		}
		kept = append(kept, e)
	}
	if !dropped {
		return nil
	}
	s.Entries = kept
	return portRegistrySave(s)
}

// portEntries returns all reservations sorted by port then proto. The
// result is never nil (empty registries serialize as [] for the UI).
func portEntries() []portEntry {
	portRegMu.Lock()
	defer portRegMu.Unlock()
	s := portRegistryLoad()
	out := make([]portEntry, 0, len(s.Entries))
	out = append(out, s.Entries...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	return out
}

// portEntriesForOwner returns one owner's reservations.
func portEntriesForOwner(owner string) []portEntry {
	var out []portEntry
	for _, e := range portEntries() {
		if e.owner() == owner {
			out = append(out, e)
		}
	}
	return out
}

// ---- availability & conflicts ----

// portAvailability explains whether a port/transport can be handed out.
type portAvailability struct {
	Port           int           `json:"port"`
	Proto          string        `json:"proto"`
	Available      bool          `json:"available"`
	Reason         string        `json:"reason,omitempty"`
	ReservedBy     string        `json:"reserved_by,omitempty"`
	Listener       *portListener `json:"listener,omitempty"`
	OtherTransport string        `json:"other_transport,omitempty"`
}

// portAvailabilityCheck explains one port/transport. The sibling
// transport's usage is surfaced in OtherTransport — a free TCP port with a
// live UDP listener is exactly the trap this report exists to expose.
func portAvailabilityCheck(port int, proto string) portAvailability {
	a := portAvailability{Port: port, Proto: proto}
	if port < 1 || port > 65535 || (proto != portProtoTCP && proto != portProtoUDP) {
		a.Reason = "invalid port or proto"
		return a
	}
	lo, hi := portEphemeralRange()
	if port >= lo && port <= hi {
		a.Reason = fmt.Sprintf("inside the kernel ephemeral range %d-%d", lo, hi)
		return a
	}
	portRegMu.Lock()
	s := portRegistryLoad()
	portRegMu.Unlock()
	for _, e := range s.Entries {
		if e.Port == port && e.Proto == proto {
			a.ReservedBy = e.owner()
			a.Reason = "reserved by " + e.owner()
			return a
		}
	}
	if listeners, err := portListeners(); err == nil {
		for _, l := range listeners {
			if l.Port != port {
				continue
			}
			if l.Proto == proto {
				l := l
				a.Listener = &l
				a.Reason = fmt.Sprintf("%s listener present (%s, pid %d)", proto, l.Process, l.PID)
				return a
			}
			a.OtherTransport = fmt.Sprintf("%s listener present (%s, pid %d)", l.Proto, l.Process, l.PID)
		}
	}
	a.Available = true
	return a
}

// portReport is the full both-transports view of one port.
func portReport(port int) map[string]any {
	return map[string]any{
		"port": port,
		"tcp":  portAvailabilityCheck(port, portProtoTCP),
		"udp":  portAvailabilityCheck(port, portProtoUDP),
	}
}

// portConflicts lists registry entries whose port is simultaneously held
// by a live process the registry does not associate with them — the state
// that means "the registry and reality disagree; investigate". Quantum's
// own daemons (panel, xray, frp, backhaul, tinc) legitimately listen on
// their reserved ports and are not conflicts.
func portConflicts() []map[string]any {
	out := []map[string]any{}
	listeners, err := portListeners()
	if err != nil {
		return out
	}
	byKey := map[string]portListener{}
	for _, l := range listeners {
		byKey[l.Proto+":"+strconv.Itoa(l.Port)] = l
	}
	for _, e := range portEntries() {
		l, ok := byKey[e.key()]
		if !ok || e.OwnerKind == portOwnerExternal {
			continue
		}
		proc := strings.ToLower(l.Process)
		if proc == "" ||
			strings.Contains(proc, "quantum") || strings.Contains(proc, "gre-panel") ||
			strings.Contains(proc, "xray") || strings.Contains(proc, "frp") ||
			strings.Contains(proc, "backhaul") || strings.Contains(proc, "tinc") {
			continue
		}
		out = append(out, map[string]any{
			"port": e.Port, "proto": e.Proto, "owner": e.owner(), "listener": l,
		})
	}
	return out
}
