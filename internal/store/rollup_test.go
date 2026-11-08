package store

// Phase-10 TASK 3: the hourly rollup, deterministic. Fixed timestamps in
// two ADJACENT hours; known counter progressions; fold; prune the raw
// rows; the same totals must come back through the rollup-backed query.

import (
	"testing"
)

func TestTrafficHourlyRollup(t *testing.T) {
	s := openTestStore(t)
	topo, _ := s.UpsertTopology(Topology{Name: "roll-topo", Enabled: true})
	iran, _ := s.UpsertServer(Server{Name: "roll-iran", Role: "iran"})
	foreign, _ := s.UpsertServer(Server{Name: "roll-f", Role: "foreign"})
	tun, _ := s.UpsertTunnel(Tunnel{TunnelKey: "roll-t1", TopologyID: topo,
		LocalServerID: iran, RemoteServerID: foreign, Engine: "matrix", Enabled: true})
	if err := s.UpsertAgent(Agent{ID: "roll-iran-agent", Role: "iran"}); err != nil {
		t.Fatal(err)
	}

	// Two adjacent hours (hour indexes 1000 and 1001). The counter climbs
	// 100 → 500 in hour 1000 and 500 → 900 in hour 1001; the total is 800
	// regardless of the hour boundary (the carry chains the hours).
	H1, H2 := int64(1000*3600), int64(1001*3600)
	samples := []TrafficSample{
		{TunnelID: tun, At: H1 + 100, AgentID: "roll-iran-agent", WireRx: 100, WireTx: 10, PayloadRx: 90, PayloadTx: 9, CountersKnown: true, PayloadKnown: true},
		{TunnelID: tun, At: H1 + 1800, AgentID: "roll-iran-agent", WireRx: 300, WireTx: 30, PayloadRx: 270, PayloadTx: 27, CountersKnown: true, PayloadKnown: true},
		{TunnelID: tun, At: H1 + 3500, AgentID: "roll-iran-agent", WireRx: 500, WireTx: 50, PayloadRx: 450, PayloadTx: 45, CountersKnown: true, PayloadKnown: true},
		{TunnelID: tun, At: H2 + 200, AgentID: "roll-iran-agent", WireRx: 700, WireTx: 70, PayloadRx: 630, PayloadTx: 63, CountersKnown: true, PayloadKnown: true},
		{TunnelID: tun, At: H2 + 3500, AgentID: "roll-iran-agent", WireRx: 900, WireTx: 90, PayloadRx: 810, PayloadTx: 81, CountersKnown: true, PayloadKnown: true},
	}
	for _, sm := range samples {
		if err := s.AddTrafficSample(sm); err != nil {
			t.Fatal(err)
		}
	}
	// Sanity: the RAW path sees the same total.
	all, _, err := s.TunnelUsageDetailed(tun, 0, H2+3600)
	if err != nil {
		t.Fatal(err)
	}
	if all.WireRx != 900 {
		t.Fatalf("raw-path wire rx = %d, want 900", all.WireRx)
	}

	// Fold the complete hours (before = the end of hour 1001), prune ALL
	// raw rows, then the rollup-backed query must return the same total.
	if n, err := s.RollupTrafficHourly(H2 + 3600); err != nil {
		t.Fatal(err)
	} else if n != 2 {
		t.Fatalf("folded %d hour rows, want 2", n)
	}
	if pruned, err := s.PruneTrafficSamples(H2 + 3600); err != nil {
		t.Fatal(err)
	} else if pruned != int64(len(samples)) {
		t.Fatalf("pruned %d rows, want %d", pruned, len(samples))
	}
	got, _, err := s.TunnelUsageDetailed(tun, 0, H2+3600)
	if err != nil {
		t.Fatal(err)
	}
	if got.WireRx != 900 || got.WireTx != 90 {
		t.Fatalf("rollup-backed usage = %+v, want wire rx 900 tx 90", got)
	}
	if got.PayloadRx != 810 || got.PayloadTx != 81 {
		t.Fatalf("rollup-backed payload = %+v, want rx 810 tx 81", got)
	}
}
