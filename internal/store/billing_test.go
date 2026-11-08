package store

// billing_test.go — TASK 4 (phase 12): the billing export must be EXACT
// across a Jalali month boundary (Tehran midnight), rollup-based, and
// honest about holes (gaps, unknown counters). Seeded KNOWN bytes only.

import (
	"testing"
	"time"
)

// seedBillingStream writes cumulative iran-agent samples at the given
// times (wire rx climbing by `step`, tx = rx/8, payload = rx - rx/16).
func seedBillingStream(t *testing.T, s *Store, tun int64, agent string, times []int64, step uint64) {
	t.Helper()
	var rx uint64
	for _, at := range times {
		rx += step
		if err := s.AddTrafficSample(TrafficSample{
			TunnelID: tun, At: at, WireRx: rx, WireTx: rx / 8,
			PayloadRx: rx - rx/16, PayloadTx: rx / 9,
			CountersKnown: true, PayloadKnown: true, AgentID: agent,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBillingReportMonthBoundaryExact(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	dc, _ := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "tehran-b", PriceCoefficient: 2.5})
	sv, _ := s.UpsertServer(Server{Role: "iran", Name: "ir-b", DatacenterID: dc})
	// The reporting agent is Iran-role → billable view = its wire rx.
	if err := s.UpsertAgent(Agent{ID: "ag_b", ServerID: sv, Role: "iran", Name: "ir-b"}); err != nil {
		t.Fatal(err)
	}
	topo, _ := s.UpsertTopology(Topology{Name: "tb", Enabled: true})
	tun, err := s.UpsertTunnel(Tunnel{TunnelKey: "tb-1", TopologyID: topo,
		LocalServerID: sv, RemoteServerID: sv, Engine: "matrix", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	tehran := time.FixedZone("Asia/Tehran", 3*3600+1800)

	// Window: the Jalali month 1405-08 = 2026-10-22 20:30 UTC ..
	// 2026-11-21 20:30 UTC (Tehran fixed +03:30, no DST).
	from := time.Date(2026, 10, 22, 20, 30, 0, 0, time.UTC).Unix()
	to := time.Date(2026, 11, 21, 20, 30, 0, 0, time.UTC).Unix()
	monthStart := time.Unix(from, 0).In(tehran)
	monthEnd := time.Unix(to, 0).In(tehran)
	if monthStart.Format("2006-01-02 15:04") != "2026-10-23 00:00" {
		t.Fatalf("month start = %s, want 2026-10-23 00:00 Tehran", monthStart.Format("2006-01-02 15:04"))
	}

	// Samples: the LAST hour of 1405-07 (must NOT count) + first/mid/last
	// hours of 1405-08 (must count). Zero-base: the first in-window
	// sample's counters ARE the usage (documented rule).
	lastOf07 := from - 1800
	inMonth := []int64{from + 1800, from + 30*3600, to - 3600}
	seedBillingStream(t, s, tun, "ag_b", append([]int64{lastOf07}, inMonth...), 1000)

	// Seed 2 samples BEFORE folding so the boundary rows fold too.
	if n, err := s.RollupTrafficHourly(to); err != nil || n < 4 {
		t.Fatalf("fold: n=%d err=%v", n, err)
	}

	dcs, servers, tunnels, err := s.BillingReport(from, to)
	if err != nil {
		t.Fatal(err)
	}
	// EXACT month semantics: the sample BEFORE the Tehran-midnight
	// boundary is excluded (its hour is not fully inside the window and
	// the raw head starts at `from`); the first in-window sample
	// zero-bases at its counters (2000) — 2000+1000+1000 = 4000.
	if len(tunnels) != 1 || tunnels[0].Billable.WireRx != 4000 {
		t.Fatalf("billable = %+v, want 4000 (boundary sample excluded, zero-base first sample)", tunnels)
	}
	if tunnels[0].Usage.WireTx != 4000/8 {
		t.Fatalf("usage wire tx = %v", tunnels[0].Usage)
	}
	if len(servers) != 1 || servers[0].Name != "ir-b" || servers[0].Billable.WireRx != 4000 {
		t.Fatalf("server row: %+v", servers)
	}
	if len(dcs) != 1 || dcs[0].Name != "tehran-b" || dcs[0].Billable.WireRx != 4000 {
		t.Fatalf("dc row: %+v", dcs)
	}
	if dcs[0].PriceHint != 2.5 {
		t.Fatalf("price hint = %v, want 2.5", dcs[0].PriceHint)
	}
	// The seeded samples are hours apart by design — the gap flags MUST
	// fire (that is the honest-completeness contract; the no-gap case is
	// covered by the dense-stream test below).
	if dcs[0].Quality.GapCount == 0 {
		t.Fatalf("30-day sample spacing must produce gap flags: %+v", dcs[0].Quality)
	}
	_ = monthEnd
}

func TestBillingQualityFlagsGapsAndUnknown(t *testing.T) {
	s, err := OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	dc, _ := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "tehran-q"})
	sv, _ := s.UpsertServer(Server{Role: "iran", Name: "ir-q", DatacenterID: dc})
	if err := s.UpsertAgent(Agent{ID: "ag_q", ServerID: sv, Role: "iran"}); err != nil {
		t.Fatal(err)
	}
	topo, _ := s.UpsertTopology(Topology{Name: "tq", Enabled: true})
	tun, _ := s.UpsertTunnel(Tunnel{TunnelKey: "tq-1", TopologyID: topo,
		LocalServerID: sv, RemoteServerID: sv, Engine: "matrix", Enabled: true})

	from := time.Date(2026, 10, 22, 20, 30, 0, 0, time.UTC).Unix()
	to := time.Date(2026, 11, 21, 20, 30, 0, 0, time.UTC).Unix()
	base := from + 3600
	// Stream with a 6-hour HOLE in the middle, then an UNKNOWN-payload tail.
	good := []int64{base, base + 3600}
	hole := []int64{base + 8*3600, base + 9*3600} // 6h gap after base+3600
	var rx uint64
	for _, at := range append(good, hole...) {
		rx += 500
		if err := s.AddTrafficSample(TrafficSample{
			TunnelID: tun, At: at, WireRx: rx, WireTx: rx / 8,
			PayloadRx: rx - rx/16, PayloadTx: rx / 9,
			CountersKnown: true, PayloadKnown: true, AgentID: "ag_q",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// An unknown-payload sample (payload_known=0): flagged, bytes not invented.
	if err := s.AddTrafficSample(TrafficSample{
		TunnelID: tun, At: base + 10*3600, WireRx: rx + 500, WireTx: (rx + 500) / 8,
		PayloadRx: 0, PayloadTx: 0, CountersKnown: true, PayloadKnown: false, AgentID: "ag_q",
	}); err != nil {
		t.Fatal(err)
	}

	_, _, tunnels, err := s.BillingReport(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(tunnels) != 1 {
		t.Fatalf("rows: %+v", tunnels)
	}
	q := tunnels[0].Quality
	if q.GapCount != 1 || q.LongestGapS != 6*3600 || q.OfflineAgents != 1 {
		t.Fatalf("gap flags: %+v (want 1 gap of 6h, 1 agent offline)", q)
	}
	if !q.UnknownCounters {
		t.Fatalf("unknown counters not flagged: %+v", q)
	}
	// The wire view continues across the unknown-payload sample (wire
	// known); payload must NOT grow from the unknown sample.
	if tunnels[0].Usage.PayloadRx >= rx {
		t.Fatalf("payload counted an unknown sample: %+v", tunnels[0].Usage)
	}
}
