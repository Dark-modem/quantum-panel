package panel

// portalloc.go — Phase 14 server port allocator. Before the panel books a
// listen port (or suggests alternatives after a conflict) it unions three
// sources of "taken":
//
//   - WELL-KNOWN: SSH 22 (plus any operator-declared extra SSH ports),
//     80/443, the panel's own HTTP/TLS listeners, and the Pasarguard node
//     ports (62050 node, 62051 API);
//   - OBSERVED: every port the server's linked agent last reported
//     listening (agent_listening — the truth beats the ledger);
//   - BOOKED: port_allocations rows (the panel's own ledger) and the
//     listen ports of existing forward rules on that server.
//
// Allocation is DETERMINISTIC: the lowest free port in the configured
// range wins, so two panels (or a panel replay) over the same store
// converge on the same answer, and a port freed by a delete is reused
// before new ground is consumed.

import (
	"encoding/json"
	"fmt"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// sshPort is the well-known SSH listener; reserving it by default keeps
// a port-forward allocation from colliding with the only door into the
// box. Operators with a moved sshd declare it via the "ssh_ports" setting.
const sshPort = 22

// Pasarguard defaults (pg_nodes.node_port / the upstream API port).
const (
	pasarguardNodePort = 62050
	pasarguardAPIPort  = 62051
)

// Settings keys (same KV table the Pasarguard connection uses).
const (
	settingKeyPortRanges = "port_ranges" // JSON [{"from":20000,"to":29999}]
	settingKeySSHPorts   = "ssh_ports"   // JSON [22, 2222]
)

// defaultPortRanges is the allocation pool when the operator set none:
// 20000-29999 is above the ephemeral range on every supported distro and
// below the registered service crowd, so freshly bound system services
// never collide with the pool.
func defaultPortRanges() []portRange {
	return []portRange{{From: 20000, To: 29999}}
}

// portRange is one inclusive allocation window.
type portRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// portRangesFor reads the operator's "port_ranges" setting (invalid or
// absent falls back to the default — a typo in settings must never leave
// the allocator without a pool).
func portRangesFor(s *store.Store) []portRange {
	raw, err := s.Setting(settingKeyPortRanges)
	if err != nil || raw == "" {
		return defaultPortRanges()
	}
	var ranges []portRange
	if json.Unmarshal([]byte(raw), &ranges) != nil {
		return defaultPortRanges()
	}
	out := make([]portRange, 0, len(ranges))
	for _, r := range ranges {
		if r.From < 1 || r.To > 65535 || r.From > r.To {
			continue // drop nonsense windows instead of inheriting them
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return defaultPortRanges()
	}
	return out
}

// sshPortsFor reads the optional "ssh_ports" setting (JSON array of
// ints); invalid entries are dropped, and the result always merges with
// the well-known 22.
func sshPortsFor(s *store.Store) []int {
	raw, err := s.Setting(settingKeySSHPorts)
	if err != nil || raw == "" {
		return nil
	}
	var ports []int
	if json.Unmarshal([]byte(raw), &ports) != nil {
		return nil
	}
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		if p >= 1 && p <= 65535 {
			out = append(out, p)
		}
	}
	return out
}

// reservedPortsFor builds the full "taken" set for one server. It reads
// the panel's own listeners from the process config (cfg.Port +
// effectiveTLSPort) — a forward on the panel host is refused, but the
// panel port still matters when servers and the panel share a box.
func reservedPortsFor(s *store.Store, serverID int64) (map[int]bool, error) {
	reserved := map[int]bool{}
	for _, p := range []int{
		sshPort, 80, 443,
		cfg.Port, effectiveTLSPort(),
		pasarguardNodePort, pasarguardAPIPort,
	} {
		if p >= 1 && p <= 65535 {
			reserved[p] = true
		}
	}
	for _, p := range sshPortsFor(s) {
		reserved[p] = true
	}
	// Observed listening: what the linked agent last saw on its host.
	listening, err := s.AgentListeningForServer(serverID)
	if err != nil {
		return nil, err
	}
	for _, l := range listening {
		reserved[l.Port] = true
	}
	// Booked: the panel's own ledger.
	allocs, err := s.ListPortAllocations(serverID)
	if err != nil {
		return nil, err
	}
	for _, a := range allocs {
		reserved[a.Port] = true
	}
	// Declared policy: the listen side of every forward rule on this
	// server (covers rules created before the ledger existed).
	rules, err := s.ListForwardRules(0)
	if err != nil {
		return nil, err
	}
	for _, f := range rules {
		if f.LocalServerID == serverID {
			reserved[f.ListenPort] = true
		}
	}
	return reserved, nil
}

// errNoFreePort: every window in the pool is exhausted (or fully
// reserved) for this server.
var errNoFreePort = fmt.Errorf("no free port in the configured range(s)")

// AllocateServerPort books a port for one server. preferred > 0 wins
// when it is free; otherwise the lowest free port across the configured
// ranges (in order) is taken. The reservation lands in port_allocations
// under ownerKey so the owning object's delete can release it.
func AllocateServerPort(s *store.Store, serverID int64, proto, purpose, ownerKey string, preferred int) (int, error) {
	reserved, err := reservedPortsFor(s, serverID)
	if err != nil {
		return 0, err
	}
	if preferred > 0 && preferred <= 65535 && !reserved[preferred] {
		// The reserved set is a snapshot; the INSERT is the arbiter (a
		// concurrent allocation falls through to the scan below).
		if err := s.AllocatePort(serverID, preferred, proto, purpose, ownerKey); err == nil {
			return preferred, nil
		}
	}
	for _, r := range portRangesFor(s) {
		for p := r.From; p <= r.To; p++ {
			if reserved[p] {
				continue
			}
			if err := s.AllocatePort(serverID, p, proto, purpose, ownerKey); err == nil {
				return p, nil
			}
		}
	}
	return 0, errNoFreePort
}

// freePortSuggestions returns up to n FREE (unreserved, unbooked) ports
// from the configured ranges WITHOUT allocating them — the concrete
// alternatives the conflict error offers the operator. Deterministic
// like the allocator, so the same state always suggests the same list.
func freePortSuggestions(s *store.Store, serverID int64, n int) []int {
	if n <= 0 {
		return []int{}
	}
	reserved, err := reservedPortsFor(s, serverID)
	if err != nil {
		return []int{}
	}
	out := []int{}
	for _, r := range portRangesFor(s) {
		for p := r.From; p <= r.To && len(out) < n; p++ {
			if !reserved[p] {
				out = append(out, p)
			}
		}
		if len(out) >= n {
			break
		}
	}
	return out
}
