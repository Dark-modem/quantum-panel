package panel

// reportv2_limits_test.go — TASK 2 (phase 12): live abuse cases against
// the ingestion path. A hostile agent controls its own binary; these
// tests prove the caps, the ownership rule and the sanity clamps hold.

import (
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	protocol "github.com/Dark-modem/quantum-protocol"
)

func abuseSeed(t *testing.T) *store.Store {
	t.Helper()
	s := withMeshStore(t)
	dc, _ := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "ab-tehran"})
	sv, _ := s.UpsertServer(store.Server{Role: "iran", Name: "ab-ir", DatacenterID: dc})
	if err := s.UpsertAgent(store.Agent{ID: "agt_good", ServerID: sv, Role: "iran", Name: "good"}); err != nil {
		t.Fatal(err)
	}
	topo, _ := s.UpsertTopology(store.Topology{Name: "ab-topo", Enabled: true})
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "ab-t1", TopologyID: topo,
		LocalServerID: sv, RemoteServerID: sv, Engine: "chisel", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIngestRejectsForeignTunnelReports(t *testing.T) {
	s := abuseSeed(t)
	// A SECOND server + tunnel the hostile agent does NOT own.
	dc2, _ := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "ab-tehran2"})
	sv2, _ := s.UpsertServer(store.Server{Role: "iran", Name: "ab-ir2", DatacenterID: dc2})
	topo2, _ := s.UpsertTopology(store.Topology{Name: "ab-topo2", Enabled: true})
	victim, _ := s.UpsertTunnel(store.Tunnel{TunnelKey: "ab-victim", TopologyID: topo2,
		LocalServerID: sv2, RemoteServerID: sv2, Engine: "chisel", Enabled: true})

	meshSampleMinGap = 0 // test: every heartbeat writes
	t.Cleanup(func() { meshSampleMinGap = 5 * time.Second })

	// The hostile agent reports the VICTIM's tunnel key with huge counters.
	rep := &protocol.StateReportV2{
		Tunnels: []protocol.TunnelRuntime{
			{TunnelID: "ab-victim", Engine: "chisel", Up: true,
				WireRx: 1 << 40, WireTx: 1 << 40, CountersKnown: true, WireKnown: true},
		},
	}
	ingestStateReportV2("agt_good", rep)
	time.Sleep(50 * time.Millisecond)
	_, _, tunnels, err := s.BillingReport(0, time.Now().Unix()+3600)
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range tunnels {
		if tr.TunnelID == victim {
			t.Fatalf("OWNERSHIP broken: the hostile agent's bytes reached the victim tunnel: %+v", tr)
		}
	}
	// The agent's OWN tunnel accepts reports (control).
	rep2 := &protocol.StateReportV2{
		Tunnels: []protocol.TunnelRuntime{
			{TunnelID: "ab-t1", Engine: "chisel", Up: true,
				WireRx: 500, CountersKnown: true, WireKnown: true},
		},
	}
	ingestStateReportV2("agt_good", rep2)
	time.Sleep(50 * time.Millisecond)
	_, _, tunnels, _ = s.BillingReport(0, time.Now().Unix()+3600)
	found := false
	for _, tr := range tunnels {
		if tr.TunnelKey == "ab-t1" && tr.Usage.WireRx > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the agent's OWN tunnel report must be accepted: %+v", tunnels)
	}
}

func TestIngestCapsAndSanity(t *testing.T) {
	s := abuseSeed(t)
	meshSampleMinGap = 0
	t.Cleanup(func() { meshSampleMinGap = 5 * time.Second })

	// 5000 tunnel rows, oversized strings, negative proc numbers, a huge
	// engine_status array.
	big := &protocol.StateReportV2{
		Tunnels: make([]protocol.TunnelRuntime, 0, 5000),
		Proc: []protocol.TunnelProc{
			{TunnelID: "ab-t1", Known: true, CPUPercent: -5, RSSBytes: -1 << 40},
			{TunnelID: "ab-t1", Known: true, CPUPercent: 999999, RSSBytes: 1 << 30},
		},
		EngineStatus: make([]protocol.EngineStatusReport, 0, 400),
		Resources:    &protocol.ResourceReport{CPUPercent: -50, MemUsedMB: 1 << 40, Virt: strings.Repeat("A", 9000)},
	}
	for i := 0; i < 5000; i++ {
		big.Tunnels = append(big.Tunnels, protocol.TunnelRuntime{
			TunnelID:  "ab-t1",
			Engine:    strings.Repeat("E", 9000),
			LastError: strings.Repeat("secret", 9000),
			Up:        true, CountersKnown: true, WireKnown: true, WireRx: 10,
		})
	}
	for i := 0; i < 400; i++ {
		big.EngineStatus = append(big.EngineStatus, protocol.EngineStatusReport{
			Engine: strings.Repeat("X", 9000), Installed: true,
		})
	}
	ingestStateReportV2("agt_good", big)
	time.Sleep(80 * time.Millisecond)

	// The hostile LastError must not be stored verbatim anywhere.
	evs, _ := s.TunnelEvents(1, 200)
	for _, ev := range evs {
		if len(ev.Text) > 600 {
			t.Fatalf("oversized error text reached the event log: %d chars", len(ev.Text))
		}
	}
	// The proc block: negatives dropped, nonsense CPU dropped.
	procs, _ := s.TunnelProcFor(nil)
	for _, p := range procs {
		if p.RSSBytes < 0 || p.CPUPct < 0 || p.CPUPct > 6400 {
			t.Fatalf("insane proc numbers reached the store: %+v", p)
		}
	}
	// Resources: the hostile metrics block (negative CPU + absurd mem)
	// must not poison the agents row.
	ag, _ := s.GetAgent("agt_good")
	if ag.CPUPct < 0 {
		t.Fatalf("negative CPU stored: %v", ag.CPUPct)
	}
	// Engine rows: capped at the limit, engine names truncated.
	engs, _ := s.AgentEngines("agt_good")
	if len(engs) > 256 {
		t.Fatalf("engine_status spam not capped: %d rows", len(engs))
	}
	for _, e := range engs {
		if len(e.Engine) > 64 || len(e.LastError) > 512 {
			t.Fatalf("oversized engine strings stored: %d/%d", len(e.Engine), len(e.LastError))
		}
	}
	// The sample flood: only the owned tunnel's stream exists, capped by
	// the tunnel-row cap (5000 rows → 4096), all keyed to ONE tunnel id.
	samples := 0
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM traffic_samples`).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if samples > 4096 {
		t.Fatalf("tunnel-row cap broken: %d samples from one heartbeat", samples)
	}
}

func TestSanitizeReportTunnelsUnit(t *testing.T) {
	rep := &protocol.StateReportV2{Tunnels: []protocol.TunnelRuntime{
		{TunnelID: strings.Repeat("x", 5000), LastError: strings.Repeat("e", 9000)},
	}}
	allowed := map[string]int64{strings.Repeat("x", 128): 7}
	out, dropped := sanitizeReportTunnels(rep, allowed)
	if dropped != 0 || len(out) != 1 {
		t.Fatalf("owned oversized tunnel must be capped not dropped: out=%d dropped=%d", len(out), dropped)
	}
	if len(out[0].TunnelID) != 128 || len(out[0].LastError) != 512 {
		t.Fatalf("string caps wrong: %d/%d", len(out[0].TunnelID), len(out[0].LastError))
	}
	_, dropped = sanitizeReportTunnels(&protocol.StateReportV2{Tunnels: []protocol.TunnelRuntime{
		{TunnelID: "someone-elses"},
	}}, allowed)
	if dropped != 1 {
		t.Fatal("foreign tunnel must be dropped")
	}
}
