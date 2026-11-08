package panel

// uiapi_requests_test.go — regression tests for the session-2 findings
// in 00-ADMIN/UI-API-REQUESTS.md (phase 12, priority 0):
//
//	#1 topology detail's FLAT tunnels array (and health totals) must hold
//	   each tunnel ONCE (the per-member lists keep both perspectives);
//	#2 overview counts.agents must actually count (online vs offline);
//	#3 agents carry an explicit linked flag (unlinked agents are
//	   invisible in per-datacenter views BY DESIGN — the UI nudges).

import (
	"net/http"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	protocol "github.com/Dark-modem/quantum-protocol"
)

func TestUITopologyFlatListCountsEachTunnelOnce(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s) // 1 pair, TWO tunnels (tl-a, tl-b)
	meshRuntimeMu.Lock()
	meshRuntimeBy[agIr] = []protocol.TunnelRuntime{
		{TunnelID: "tl-a", Up: true}, {TunnelID: "tl-b", Up: false},
	}
	meshRuntimeSeen[agIr] = time.Now().Unix()
	meshRuntimeMu.Unlock()

	c := meshSession(t)
	td := meshGetMap(t, c, srv, "/api/ui/topologies/1")
	tunnels := td["tunnels"].([]any)
	if len(tunnels) != 2 {
		t.Fatalf("flat tunnels = %d (UI-API-REQUESTS #1: must be 2, was double-counted)", len(tunnels))
	}
	tot := td["totals"].(map[string]any)
	tu := tot["tunnels"].(map[string]any)
	if tu["total"].(float64) != 2 || tu["up"].(float64) != 1 || tu["down"].(float64) != 1 {
		t.Fatalf("totals must not be doubled: %v", tu)
	}
	// The per-member lists DO show each tunnel from both perspectives.
	members := td["members"].(map[string]any)
	ir0 := members["iran"].([]any)[0].(map[string]any)
	fr0 := members["foreign"].([]any)[0].(map[string]any)
	if len(ir0["tunnels"].([]any)) != 2 || len(fr0["tunnels"].([]any)) != 2 {
		t.Fatalf("per-member tunnel views: iran=%d foreign=%d (want 2/2)",
			len(ir0["tunnels"].([]any)), len(fr0["tunnels"].([]any)))
	}
}

func TestUIOverviewAgentCountsLive(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	_, _, agIr, agFr := uiSeedWorld(t, s)
	// agIr reports (fresh heartbeat → online); agFr never does.
	meshRuntimeMu.Lock()
	meshRuntimeBy[agIr] = []protocol.TunnelRuntime{{TunnelID: "tl-a", Up: true}}
	meshRuntimeSeen[agIr] = time.Now().Unix()
	meshRuntimeMu.Unlock()

	c := meshSession(t)
	ov := meshGetMap(t, c, srv, "/api/ui/overview")
	counts := ov["counts"].(map[string]any)
	ag := counts["agents"].(map[string]any)
	if ag["online"].(float64) != 1 || ag["offline"].(float64) != 1 {
		t.Fatalf("UI-API-REQUESTS #2: agents counts = %v (want online 1, offline 1)", ag)
	}
	_ = agFr
}

func TestUIAgentsCarryLinkedFlag(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	// An enrolled agent with NO server link.
	if err := s.UpsertAgent(store.Agent{ID: "agt_unlinked", Name: "loose", Role: "iran",
		Status: "online", LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	c := meshSession(t)
	resp := meshGetMap(t, c, srv, "/api/ui/agents")
	linked := map[string]bool{}
	for _, raw := range resp["agents"].([]any) {
		m := raw.(map[string]any)
		linked[m["id"].(string)] = m["linked"].(bool)
	}
	if !linked["agt_ir_seeded"] || linked["agt_unlinked"] {
		t.Fatalf("linked flags wrong: %v", linked)
	}
	// Detail view carries it too.
	d := meshGetMap(t, c, srv, "/api/ui/agents/agt_unlinked")
	if d["agent"].(map[string]any)["linked"] != false {
		t.Fatalf("detail linked flag: %v", d["agent"])
	}
	_ = http.StatusOK
}
