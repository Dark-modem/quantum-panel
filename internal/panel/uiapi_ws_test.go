package panel

// uiapi_ws_test.go — the WS-LEVEL integration (Phase 11): a fake agent
// connects through the REAL agent websocket, reports per-tunnel process
// metrics + engine status + resources in a StateReportV2 heartbeat, and
// the numbers must surface in the UI API (agent → panel → API).

import (
	"encoding/json"
	"testing"
	"time"

	protocol "github.com/Dark-modem/quantum-protocol"
)

func TestUIProcAndEngineStatusOverWebSocket(t *testing.T) {
	srv := uiServer(t) // registers mesh + UI + agent-ws routes
	c := meshSession(t)
	s := meshStoreMust(t)
	_, frServer, _, _ := uiSeedWorld(t, s)

	// A REAL v2 websocket session (hello/welcome over the agent hub).
	// The enroll helper creates FOREIGN-role agents: link to the foreign
	// server (the seeded world's second server).
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	meshPut(t, c, srv, "POST", "/api/mesh/agents/"+id+"/link", map[string]any{"server_id": frServer})
	fa := dialFakeAgentV2(t, srv, id, key)
	if fa.welcomeVersion != protocol.ProtoVersionV2 {
		t.Fatalf("session version = %d, want v2", fa.welcomeVersion)
	}

	// The heartbeat the REAL agent now builds (tunnel_proc.go +
	// engine_procinfo.go produce exactly these sections).
	fa.report(protocol.StateReportV2{
		AppliedRevision: "rev-ws-1",
		Tunnels: []protocol.TunnelRuntime{
			{TunnelID: "tl-a", Engine: "chisel", Up: true, LatencyMs: 12.5,
				WireRx: 1000, WireTx: 100, CountersKnown: true, WireKnown: true,
				UptimeS: 3600},
		},
		Proc: []protocol.TunnelProc{
			{TunnelID: "tl-a", Known: true, CPUPercent: 7.25, RSSBytes: 9 << 20, Processes: 1},
			{TunnelID: "tl-ghost", Known: false}, // unknown tunnel: skipped
		},
		EngineStatus: []protocol.EngineStatusReport{
			{Engine: "chisel", Installed: true, Version: "v1.10.1", BytesOnDisk: 14893056,
				Enabled: true, Instances: 1},
		},
		Resources: &protocol.ResourceReport{CPUCores: 2, CPUPercent: 12.5,
			MemTotalMB: 2048, MemUsedMB: 512, DiskTotalGB: 40, DiskUsedGB: 10,
			UptimeS: 1800, Virt: "kvm"},
	})
	time.Sleep(300 * time.Millisecond) // the hub ingests asynchronously

	// The API must show it all.
	d := meshGetMap(t, c, srv, "/api/ui/agents/"+id)
	ag := d["agent"].(map[string]any)
	if ag["uptime_s"].(float64) != 1800 || ag["virt"] != "kvm" {
		t.Fatalf("resources did not travel: %v", ag)
	}
	var chisel map[string]any
	for _, e := range d["engines"].([]any) {
		if m := e.(map[string]any); m["engine"] == "chisel" {
			chisel = m
		}
	}
	if chisel == nil || chisel["installed"] != true || chisel["version"] != "v1.10.1" ||
		chisel["instances"].(float64) != 1 {
		t.Fatalf("engine_status did not travel: %v", chisel)
	}
	td := meshGetMap(t, c, srv, "/api/ui/tunnels/tl-a")
	nowBlock := td["now"].(map[string]any)
	if nowBlock["rss_bytes"].(float64) != float64(9<<20) || nowBlock["cpu_percent"].(float64) != 7.25 {
		t.Fatalf("proc did not travel: %v", nowBlock)
	}
	if nowBlock["uptime_s"].(float64) != 3600 {
		t.Fatalf("tunnel uptime did not travel: %v", nowBlock)
	}
	// The reporting agent's counters landed as a sample stream. The
	// enrolled agent is FOREIGN-role, so its stream counts in the all
	// view but NOT in the billable (iran) view — direction correctness.
	all, iran, err := s.RangeUsageDetailed(nil, time.Now().Unix()-3600, time.Now().Unix())
	if err != nil {
		t.Fatal(err)
	}
	if all.WireRx != 1000 {
		t.Fatalf("counters: all=%+v", all)
	}
	if iran.WireRx != 0 {
		t.Fatalf("a foreign agent's stream must not be billable: %+v", iran)
	}
	// arch/os were mirrored from the hello (agenthub → store).
	agRow, err := s.GetAgent(id)
	if err != nil || agRow.Arch != "amd64" {
		t.Fatalf("hello arch not mirrored: %+v (%v)", agRow, err)
	}
	_ = json.Marshal // keep json imported if assertions change
}
