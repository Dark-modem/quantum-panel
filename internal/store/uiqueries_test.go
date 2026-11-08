package store

// uiqueries_test.go — the Phase-11 range/series queries: exact numbers
// across the RAW + HOURLY boundary (the fold watermark decides which
// rows come from where; the answer must not depend on where that line
// sits), per-datacenter grouping, and the tunnel history series.

import (
	"testing"
	"time"
)

func seedStream(t *testing.T, s *Store, tun int64, agent string, at int64, rx, tx, prx, ptx uint64) {
	t.Helper()
	if err := s.AddTrafficSample(TrafficSample{
		TunnelID: tun, At: at, WireRx: rx, WireTx: tx, PayloadRx: prx, PayloadTx: ptx,
		CountersKnown: true, PayloadKnown: true, AgentID: agent,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRangeUsageAcrossFoldBoundary(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	dc, err := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "Tehran"})
	if err != nil {
		t.Fatal(err)
	}
	sv, err := s.UpsertServer(Server{Role: "iran", Name: "ir-1", DatacenterID: dc})
	if err != nil {
		t.Fatal(err)
	}
	ag := "agt_range"
	if err := s.UpsertAgent(Agent{ID: ag, ServerID: sv, Role: "iran", Name: "ir-1"}); err != nil {
		t.Fatal(err)
	}
	topo, err := s.UpsertTopology(Topology{Name: "t", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tun, err := s.UpsertTunnel(Tunnel{TunnelKey: "k1", TopologyID: topo,
		LocalServerID: sv, RemoteServerID: sv, Engine: "chisel", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	dayStart := now - now%86400
	hour1 := dayStart + 3600 // inside hour index h1
	hour2 := dayStart + 7200 // inside hour index h2

	// Stream: hour1 counters 100→300, hour2 counters 300→800.
	seedStream(t, s, tun, ag, hour1, 100, 10, 90, 9)
	seedStream(t, s, tun, ag, hour1+1800, 300, 30, 270, 27)
	seedStream(t, s, tun, ag, hour2, 300, 30, 270, 27)
	seedStream(t, s, tun, ag, hour2+1800, 800, 80, 720, 72)

	// NO rollup yet: everything from the raw tail.
	all, iran, err := s.RangeUsageDetailed([]int64{tun}, dayStart, now)
	if err != nil {
		t.Fatal(err)
	}
	// Zero-base rule: the first sample's counters ARE usage →
	// wire rx = 800, wire tx = 80 (per usageAccum semantics).
	if all.WireRx != 800 || all.WireTx != 80 || all.PayloadRx != 720 {
		t.Fatalf("raw-only range: %+v", all)
	}
	if iran.WireRx != 800 {
		t.Fatalf("raw-only billable: %+v", iran)
	}

	// Fold BOTH hours; the same range must produce the SAME numbers
	// from the hourly rows (the carry hands over at the boundary).
	if n, err := s.RollupTrafficHourly(hour2 + 3600); err != nil || n != 2 {
		t.Fatalf("fold: n=%d err=%v", n, err)
	}
	all2, iran2, err := s.RangeUsageDetailed([]int64{tun}, dayStart, now)
	if err != nil {
		t.Fatal(err)
	}
	if all2 != all || iran2 != iran {
		t.Fatalf("folded range differs: %+v vs %+v (billable %+v vs %+v)", all2, all, iran2, iran)
	}

	// Partial fold (fold ONLY hour1): the boundary chains hourly→raw.
	// A fresh store replays the whole fixture (FKs are per-database).
	s2, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	dc2, err := s2.UpsertDatacenter(Datacenter{Kind: "iran", Name: "Tehran"})
	if err != nil {
		t.Fatal(err)
	}
	sv2, err := s2.UpsertServer(Server{Role: "iran", Name: "ir-1", DatacenterID: dc2})
	if err != nil {
		t.Fatal(err)
	}
	if err := s2.UpsertAgent(Agent{ID: ag, ServerID: sv2, Role: "iran", Name: "ir-1"}); err != nil {
		t.Fatal(err)
	}
	topo2, err := s2.UpsertTopology(Topology{Name: "t", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tun2, err := s2.UpsertTunnel(Tunnel{TunnelKey: "k1", TopologyID: topo2,
		LocalServerID: sv2, RemoteServerID: sv2, Engine: "chisel", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	seedStream(t, s2, tun2, ag, hour1, 100, 10, 90, 9)
	seedStream(t, s2, tun2, ag, hour1+1800, 300, 30, 270, 27)
	seedStream(t, s2, tun2, ag, hour2, 300, 30, 270, 27)
	seedStream(t, s2, tun2, ag, hour2+1800, 800, 80, 720, 72)
	if n, err := s2.RollupTrafficHourly(hour2); err != nil || n != 1 {
		t.Fatalf("partial fold: n=%d err=%v", n, err)
	}
	all3, _, err := s2.RangeUsageDetailed([]int64{tun2}, dayStart, now)
	if err != nil {
		t.Fatal(err)
	}
	if all3 != all {
		t.Fatalf("partial-fold range differs: %+v vs %+v", all3, all)
	}
}

func TestUsageSeriesBuckets(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	ag := "agt_series"
	if err := s.UpsertAgent(Agent{ID: ag, Role: "iran", Name: "n"}); err != nil {
		t.Fatal(err)
	}
	sv, err := s.UpsertServer(Server{Role: "iran", Name: "ir"})
	if err != nil {
		t.Fatal(err)
	}
	topo, _ := s.UpsertTopology(Topology{Name: "t", Enabled: true})
	tun, err := s.UpsertTunnel(Tunnel{TunnelKey: "k", TopologyID: topo,
		LocalServerID: sv, RemoteServerID: sv, Engine: "chisel", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	dayStart := now - now%86400
	seedStream(t, s, tun, ag, dayStart+100, 100, 0, 0, 0)
	seedStream(t, s, tun, ag, dayStart+4700, 600, 0, 0, 0)  // +500 in hour 1
	seedStream(t, s, tun, ag, dayStart+8300, 1000, 0, 0, 0) // +400 in hour 2
	pts, err := s.UsageSeriesDetailed(tun, dayStart, dayStart+4*3600, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if len(pts) != 3 {
		t.Fatalf("points: %+v", pts)
	}
	// Zero-base: the first sample's 100 lands in its own bucket.
	if pts[0].WireRx != 100 || pts[1].WireRx != 500 || pts[2].WireRx != 400 {
		t.Fatalf("bucket deltas: %+v", pts)
	}
	if pts[1].T != dayStart+3600 || pts[2].T != dayStart+7200 {
		t.Fatalf("bucket anchors: %+v", pts)
	}
}

func TestRangeUsageByDatacenterGroups(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	dcIr, _ := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "Tehran"})
	dcFr, _ := s.UpsertDatacenter(Datacenter{Kind: "foreign", Name: "Frankfurt"})
	svIr, _ := s.UpsertServer(Server{Role: "iran", Name: "ir", DatacenterID: dcIr})
	svFr, _ := s.UpsertServer(Server{Role: "foreign", Name: "fr", DatacenterID: dcFr})
	_ = s.UpsertAgent(Agent{ID: "ag_ir", ServerID: svIr, Role: "iran"})
	_ = s.UpsertAgent(Agent{ID: "ag_fr", ServerID: svFr, Role: "foreign"})
	topo, _ := s.UpsertTopology(Topology{Name: "t", Enabled: true})
	tun, _ := s.UpsertTunnel(Tunnel{TunnelKey: "k", TopologyID: topo,
		LocalServerID: svIr, RemoteServerID: svFr, Engine: "chisel", Enabled: true})
	now := time.Now().Unix()
	seedStream(t, s, tun, "ag_ir", now-60, 1000, 100, 900, 90)
	seedStream(t, s, tun, "ag_ir", now-30, 2000, 200, 1800, 180)
	seedStream(t, s, tun, "ag_fr", now-60, 5000, 500, 4500, 450)
	seedStream(t, s, tun, "ag_fr", now-30, 7000, 700, 6300, 630)
	rows, err := s.RangeUsageByDatacenter(now-3600, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}
	byName := map[string]DatacenterUsage{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	// Rows are ordered iran-first.
	if rows[0].Kind != "iran" {
		t.Fatalf("order: %+v", rows)
	}
	if byName["Tehran"].Usage.WireRx != 2000 || byName["Frankfurt"].Usage.WireRx != 7000 {
		t.Fatalf("grouped usage: %+v", rows)
	}
}
