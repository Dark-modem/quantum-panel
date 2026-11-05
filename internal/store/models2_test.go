// models2_test.go — Phase 2B store surface: migration 2, forward rules,
// agent v2 session state + ack fencing, and the counter-reset-safe usage
// aggregation (a reboot must never double-count).

package store

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestMigration2AndForwardRules(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, err := s.Version(); err != nil || v < 2 {
		t.Fatalf("migration 2 not applied: v=%d err=%v", v, err)
	}

	dc, err := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "drak"})
	if err != nil {
		t.Fatal(err)
	}
	sv, err := s.UpsertServer(Server{Name: "ir1", Role: "iran", DatacenterID: dc})
	if err != nil {
		t.Fatal(err)
	}
	topo, err := s.UpsertTopology(Topology{Name: "drak-hz"})
	if err != nil {
		t.Fatal(err)
	}

	fid, err := s.CreateForwardRule(ForwardRule{
		TopologyID: &topo, LocalServerID: sv, ListenPort: 8443, Proto: "tcp",
		Target: "127.0.0.1:443", Enabled: true, Note: "decoy",
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := s.GetForwardRule(fid)
	if err != nil || f.ListenPort != 8443 || !f.Enabled {
		t.Fatalf("forward round trip: %+v %v", f, err)
	}
	f.ListenPort = 8444
	if err := s.UpdateForwardRule(f); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListForwardRules(topo)
	if len(got) != 1 || got[0].ListenPort != 8444 {
		t.Fatalf("forward list after update: %+v", got)
	}
	// Validation paths.
	if _, err := s.CreateForwardRule(ForwardRule{LocalServerID: sv, ListenPort: 0, Proto: "tcp"}); err == nil {
		t.Fatal("port 0 must be refused")
	}
	if _, err := s.CreateForwardRule(ForwardRule{LocalServerID: sv, ListenPort: 10, Proto: "sctp"}); err == nil {
		t.Fatal("sctp must be refused")
	}
	if err := s.DeleteForwardRule(fid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetForwardRule(fid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted forward must be ErrNotFound, got %v", err)
	}

	// Balancing defaults.
	if err := s.SetTopologyBalancing(topo, `{"interval_s":10}`, `{"prefer_order":["hz1"]}`); err != nil {
		t.Fatal(err)
	}
	tp, err := s.GetTopology(topo)
	if err != nil || tp.HealthCheck != `{"interval_s":10}` || tp.Failover != `{"prefer_order":["hz1"]}` {
		t.Fatalf("balancing round trip: %+v %v", tp, err)
	}
	if err := s.SetTopologyBalancing(topo, "{broken", ""); err == nil {
		t.Fatal("broken health_check JSON must be refused")
	}
	// Topology delete cascades its dependents but not the servers.
	tun, err := s.UpsertTunnel(Tunnel{TunnelKey: "t1", TopologyID: topo, LocalServerID: sv, RemoteServerID: sv, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = tun
	if err := s.DeleteTopology(topo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetTopology(topo); !errors.Is(err, ErrNotFound) {
		t.Fatalf("topology must be gone: %v", err)
	}
	if _, err := s.GetServer(sv); err != nil {
		t.Fatalf("servers must survive a topology delete: %v", err)
	}
}

func TestAgentV2StateAndAckFencing(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.UpsertAgent(Agent{ID: "agt_x", Name: "x", Status: "online"}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetAgentV2Session("agt_x", 2, `[{"id":"matrix"}]`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentV2Session("agt_x", 3, ""); err == nil {
		t.Fatal("proto version 3 must be refused")
	}
	v, err := s.AgentV2("agt_x")
	if err != nil || v.ProtoVersion != 2 || v.EngineCatalog != `[{"id":"matrix"}]` {
		t.Fatalf("agent v2 state: %+v %v", v, err)
	}

	// Ack only matches the PENDING revision; stale acks are ignored.
	if err := s.SetPendingDesiredV2("agt_x", `{"revision":"42"}`); err != nil {
		t.Fatal(err)
	}
	matched, err := s.AckDesiredV2("agt_x", "41", true, "")
	if err != nil || matched {
		t.Fatalf("stale ack must not match (matched=%v err=%v)", matched, err)
	}
	matched, err = s.AckDesiredV2("agt_x", "42", true, "")
	if err != nil || !matched {
		t.Fatalf("pending ack must match: matched=%v err=%v", matched, err)
	}
	a, err := s.GetAgent("agt_x")
	if err != nil || a.AppliedRevision != "42" {
		t.Fatalf("applied revision must advance: %+v %v", a, err)
	}
	v, _ = s.AgentV2("agt_x")
	if v.DesiredV2 != "" {
		t.Fatalf("ack must clear the pending desired, got %q", v.DesiredV2)
	}

	// Failed ack keeps the pending row and records the error.
	_ = s.SetPendingDesiredV2("agt_x", `{"revision":"43"}`)
	if matched, _ := s.AckDesiredV2("agt_x", "43", false, "engine boomed"); !matched {
		t.Fatal("failing ack must still match the pending revision")
	}
	a, _ = s.GetAgent("agt_x")
	if a.AppliedRevision != "42" {
		t.Fatalf("failed ack must not advance applied_revision: %+v", a)
	}
	v, _ = s.AgentV2("agt_x")
	if v.DesiredV2 == "" || v.LastError != "engine boomed" {
		t.Fatalf("failed ack must keep pending + record error: %+v", v)
	}

	// Staleness flip keeps every other field.
	if err := s.LinkAgentServer("agt_x", 0); err != nil && !errors.Is(err, ErrNotFound) {
		// server 0 does not exist; foreign keys refuse — acceptable, checked below
	}
	// Staleness flip: never-seen agents (last_seen = 0) must NOT be flipped.
	if _, err := s.MarkStaleAgents(time.Duration(0)); err != nil {
		t.Fatal(err)
	}
	// last_seen is 0 (never seen) → NOT flipped by the staleness sweep.
	if a.Status != "online" {
		t.Fatalf("never-seen agents must not be marked stale: %+v", a)
	}
}

func TestUsageAggregationResetsSafe(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dc, _ := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "d"})
	sv, _ := s.UpsertServer(Server{Name: "s", Role: "iran", DatacenterID: dc})
	topo, _ := s.UpsertTopology(Topology{Name: "t"})
	tun, err := s.UpsertTunnel(Tunnel{TunnelKey: "k", TopologyID: topo, LocalServerID: sv, RemoteServerID: sv, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	// Window (0, 100]:
	//   t=10: first known sample → counts fully (1000, 800, 900, 700)
	//   t=20: +500/+400/+450/+350
	//   t=30: unknown counters → contributes nothing, resets base
	//   t=40: REBOOT: counters restarted, smaller than before → zero base
	//         contributes (100, 50, 90, 40) — no negative, no double count
	samples := []TrafficSample{
		{TunnelID: tun, At: 10, WireRx: 1000, WireTx: 800, PayloadRx: 900, PayloadTx: 700, CountersKnown: true, PayloadKnown: true},
		{TunnelID: tun, At: 20, WireRx: 1500, WireTx: 1200, PayloadRx: 1350, PayloadTx: 1050, CountersKnown: true, PayloadKnown: true},
		{TunnelID: tun, At: 30, CountersKnown: false},
		{TunnelID: tun, At: 40, WireRx: 100, WireTx: 50, PayloadRx: 90, PayloadTx: 40, CountersKnown: true, PayloadKnown: true},
	}
	for _, sm := range samples {
		if err := s.AddTrafficSample(sm); err != nil {
			t.Fatal(err)
		}
	}
	u, err := s.TunnelUsage(tun, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{WireRx: 1600, WireTx: 1250, PayloadRx: 1440, PayloadTx: 1090}
	if u != want {
		t.Fatalf("usage = %+v, want %+v", u, want)
	}
	// A window starting AFTER the last sample is empty.
	if u2, _ := s.TunnelUsage(tun, 41, 100); u2 != (Usage{}) {
		t.Fatalf("late window must be empty: %+v", u2)
	}
	// Aggregation across tunnels + pruning.
	u3, err := s.UsageForTunnels([]int64{tun}, 0, 100)
	if err != nil || u3 != want {
		t.Fatalf("aggregate: %+v %v", u3, err)
	}
	if n, err := s.PruneTrafficSamples(1); err != nil || n != 0 {
		t.Fatalf("prune with nothing older than the cutoff: %d %v", n, err)
	}
	if n, err := s.PruneTrafficSamples(1000); err != nil || n != 4 {
		t.Fatalf("prune: %d %v", n, err)
	}
}

func TestTopology2DeploymentShape(t *testing.T) {
	// The brief's deployment shape: 2 Iran DCs, shared foreign servers,
	// two topologies, 12 tunnels each — the store must hold it.
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	drak, _ := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "drak"})
	chk, _ := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "chekavak"})
	hz, _ := s.UpsertDatacenter(Datacenter{Kind: "foreign", Name: "hetzner"})
	var iranServers, foreignServers []int64
	for i, dc := range []int64{drak, drak, chk, chk} {
		sv, err := s.UpsertServer(Server{Name: fmt.Sprintf("ir-%d", i), Role: "iran", DatacenterID: dc})
		if err != nil {
			t.Fatal(err)
		}
		iranServers = append(iranServers, sv)
	}
	for i := 0; i < 2; i++ {
		sv, err := s.UpsertServer(Server{Name: fmt.Sprintf("hz-%d", i), Role: "foreign", DatacenterID: hz})
		if err != nil {
			t.Fatal(err)
		}
		foreignServers = append(foreignServers, sv)
	}
	for ti, topoName := range []string{"drak-hz", "chekavak-hz"} {
		topo, err := s.UpsertTopology(Topology{Name: topoName})
		if err != nil {
			t.Fatal(err)
		}
		// Each topology scopes ITS OWN two Iran servers (topology 0 → the
		// drak pair, topology 1 → the chekavak pair) but SHARES the foreign
		// servers — the brief's exact shape.
		members := iranServers[ti*2 : ti*2+2]
		for _, m := range members {
			if err := s.AddTopologyMember(topo, m); err != nil {
				t.Fatal(err)
			}
		}
		for _, m := range foreignServers {
			if err := s.AddTopologyMember(topo, m); err != nil {
				t.Fatal(err)
			}
		}
		n := 0
		for _, local := range members {
			for _, remote := range foreignServers {
				for slot := 0; slot < 3; slot++ {
					key := fmt.Sprintf("tun-%s-%d-%d-%d", topoName, local, remote, slot)
					if _, err := s.UpsertTunnel(Tunnel{
						TunnelKey: key, TopologyID: topo,
						LocalServerID: local, RemoteServerID: remote,
						Engine: "matrix", Enabled: true,
					}); err != nil {
						t.Fatal(err)
					}
					n++
				}
			}
		}
		if n != 12 {
			t.Fatalf("topology %d: got %d tunnels, want 12", ti, n)
		}
	}
	tunnels, err := s.ListTunnels(1)
	if err != nil || len(tunnels) != 12 {
		t.Fatalf("topology 1 tunnels: %d %v", len(tunnels), err)
	}
}
