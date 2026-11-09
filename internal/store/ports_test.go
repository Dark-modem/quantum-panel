// ports_test.go — Phase 14 port inventory store surface: the
// port_allocations ledger (UNIQUE constraint is the single arbiter of
// "taken"), the agent_listening replace-per-heartbeat snapshot, and the
// server join the allocator + topology port view read.

package store

import (
	"errors"
	"testing"
)

func portLedgerSeed(t *testing.T) (*Store, int64) {
	t.Helper()
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	sv, err := s.UpsertServer(Server{Name: "pa-fx1", Role: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	return s, sv
}

func TestPortAllocationLedger(t *testing.T) {
	s, sv := portLedgerSeed(t)

	if err := s.AllocatePort(sv, 21000, "tcp", "forward_rule", "forward:1"); err != nil {
		t.Fatalf("first allocate: %v", err)
	}
	// The ledger is the arbiter: the same slot twice must be a typed
	// conflict, not a generic SQL error leaking to the API.
	err := s.AllocatePort(sv, 21000, "tcp", "forward_rule", "forward:2")
	if !errors.Is(err, ErrPortTaken) {
		t.Fatalf("duplicate allocate must be ErrPortTaken, got %v", err)
	}
	if err := s.AllocatePort(sv, 21001, "udp", "forward_rule", "forward:2"); err != nil {
		t.Fatalf("second allocate: %v", err)
	}
	// Validation: garbage never enters the ledger.
	for _, bad := range []struct {
		port  int
		proto string
	}{{0, "tcp"}, {70000, "tcp"}, {21002, "icmp"}} {
		if err := s.AllocatePort(sv, bad.port, bad.proto, "", ""); err == nil {
			t.Fatalf("allocate port=%d proto=%q must fail", bad.port, bad.proto)
		}
	}

	rows, err := s.ListPortAllocations(sv)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Port != 21000 || rows[1].Port != 21001 {
		t.Fatalf("ledger rows (lowest first): %+v", rows)
	}
	if rows[0].OwnerKey != "forward:1" || rows[0].Purpose != "forward_rule" {
		t.Fatalf("owner/purpose lost: %+v", rows[0])
	}

	// Release the exact slot, then the owner: both paths free rows.
	if err := s.ReleasePort(sv, 21001); err != nil {
		t.Fatalf("release port: %v", err)
	}
	if err := s.ReleasePortOwner(sv, "forward:1"); err != nil {
		t.Fatalf("release owner: %v", err)
	}
	if rows, _ := s.ListPortAllocations(sv); len(rows) != 0 {
		t.Fatalf("ledger must be empty after releases: %+v", rows)
	}
	// Freeing something that was never booked is a no-op, not an error.
	if err := s.ReleasePort(sv, 21000); err != nil {
		t.Fatalf("release of unknown port: %v", err)
	}
}

func TestAgentListeningSnapshotReplaces(t *testing.T) {
	s, sv := portLedgerSeed(t)
	ag := "agt_pa_listening"
	if err := s.UpsertAgent(Agent{ID: ag, ServerID: sv, Name: "pa-fx1", Role: "foreign"}); err != nil {
		t.Fatal(err)
	}

	if err := s.UpsertAgentListening(ag, []ListenRow{
		{Proto: "tcp", Port: 22, Proc: "sshd"},
		{Proto: "tcp", Port: 8080, Proc: "app"},
	}, 1000); err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	got, err := s.AgentListeningForServer(sv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Port != 22 || got[1].Port != 8080 {
		t.Fatalf("snapshot 1: %+v", got)
	}

	// REPLACE semantics: the next heartbeat is the full inventory — a
	// socket that closed must stop reserving its port, so 8080 is gone.
	if err := s.UpsertAgentListening(ag, []ListenRow{
		{Proto: "tcp", Port: 22, Proc: "sshd"},
		{Proto: "udp", Port: 53, Proc: "dnsmasq"},
		{Proto: "icmp", Port: 1},    // nonsense proto dropped
		{Proto: "tcp", Port: 0},     // nonsense port dropped
		{Proto: "tcp", Port: 90000}, // out of range dropped
	}, 2000); err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	got, err = s.AgentListeningForServer(sv)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Proto != "tcp" || got[0].Port != 22 || got[1].Proto != "udp" {
		t.Fatalf("snapshot 2 (replaced, junk dropped): %+v", got)
	}

	// The unlinked-server case: no agent linked → no rows, not an error.
	if got, _ := s.AgentListeningForServer(sv + 99); len(got) != 0 {
		t.Fatalf("unknown server must have no inventory: %+v", got)
	}
}

func TestListAgentListeningServerJoin(t *testing.T) {
	s, sv := portLedgerSeed(t)
	sv2, err := s.UpsertServer(Server{Name: "pa-fx2", Role: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	agA, agB, agC := "agt_pa_a", "agt_pa_b", "agt_pa_unlinked"
	for _, a := range []Agent{
		{ID: agA, ServerID: sv, Name: "a", Role: "foreign"},
		{ID: agB, ServerID: sv2, Name: "b", Role: "foreign"},
		{ID: agC, Name: "c", Role: "foreign"}, // never linked
	} {
		if err := s.UpsertAgent(a); err != nil {
			t.Fatal(err)
		}
	}
	_ = s.UpsertAgentListening(agA, []ListenRow{{Proto: "tcp", Port: 22}}, 10)
	_ = s.UpsertAgentListening(agB, []ListenRow{{Proto: "udp", Port: 53}}, 10)
	_ = s.UpsertAgentListening(agC, []ListenRow{{Proto: "tcp", Port: 9999}}, 10)

	m, err := s.ListAgentListening([]int64{sv, sv2})
	if err != nil {
		t.Fatal(err)
	}
	if len(m[sv]) != 1 || m[sv][0].Port != 22 {
		t.Fatalf("server 1 inventory: %+v", m[sv])
	}
	if len(m[sv2]) != 1 || m[sv2][0].Proto != "udp" {
		t.Fatalf("server 2 inventory: %+v", m[sv2])
	}
	// The unlinked agent's rows are invisible by construction.
	for sid, rows := range m {
		for _, r := range rows {
			if r.Port == 9999 {
				t.Fatalf("unlinked agent leaked into server %d: %+v", sid, rows)
			}
		}
	}
	// Empty/nil server list: empty map, not an error.
	if m, err = s.ListAgentListening(nil); err != nil || len(m) != 0 {
		t.Fatalf("empty server list: %v %v", m, err)
	}
}

func TestForwardRuleTargetPortRoundTrip(t *testing.T) {
	s, sv := portLedgerSeed(t)
	topo, err := s.UpsertTopology(Topology{Name: "pa-topo"})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateForwardRule(ForwardRule{
		TopologyID: &topo, LocalServerID: sv, ListenPort: 25000, Proto: "tcp",
		TargetPort: 12345, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create with target_port: %v", err)
	}
	f, err := s.GetForwardRule(id)
	if err != nil {
		t.Fatal(err)
	}
	if f.TargetPort != 12345 {
		t.Fatalf("target_port lost on read: %+v", f)
	}
	// Update path persists it too (the UPDATE column list must carry it).
	f.TargetPort = 12346
	if err := s.UpdateForwardRule(f); err != nil {
		t.Fatalf("update: %v", err)
	}
	if f, _ = s.GetForwardRule(id); f.TargetPort != 12346 {
		t.Fatalf("target_port lost on update: %+v", f)
	}
	// 0 stays 0 (= same as listen port) and validation still rejects junk.
	id2, err := s.CreateForwardRule(ForwardRule{
		TopologyID: &topo, LocalServerID: sv, ListenPort: 25001, Proto: "tcp", Enabled: true,
	})
	if err != nil {
		t.Fatalf("create with default target_port: %v", err)
	}
	if f, _ = s.GetForwardRule(id2); f.TargetPort != 0 {
		t.Fatalf("target_port must default to 0: %+v", f)
	}
	bad := ForwardRule{TopologyID: &topo, LocalServerID: sv, ListenPort: 25002, Proto: "tcp", TargetPort: 70000}
	if _, err := s.CreateForwardRule(bad); err == nil {
		t.Fatal("target_port 70000 must be refused")
	}
	if err := s.UpdateForwardRule(bad); err == nil {
		t.Fatal("update with target_port 70000 must be refused")
	}
}
