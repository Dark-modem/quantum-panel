package panel

// agentmaintenance_test.go — phase 16: the maintenance-mode API over a
// real httptest server (contract §2.15): set/clear roundtrip, validation,
// restart persistence (sealed settings file), tunnel-scoped silencing
// (a window on ONE end agent silences the tunnel's alerts), and the
// additive maintenance array in the alerts view.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	protocol "github.com/Dark-modem/quantum-protocol"
)

// clearGlobalMaintenance drops the test agent's window on the shared
// engine (the HTTP handlers run on the process-global meshAlertEng).
func clearGlobalMaintenance(t *testing.T, agentIDs ...string) {
	t.Helper()
	t.Cleanup(func() {
		for _, id := range agentIDs {
			meshAlertEng.SetMaintenance(id, time.Now().Add(-time.Hour))
		}
	})
}

func TestUIMaintenanceSetGetClear(t *testing.T) {
	srv := uiServer(t)
	c := meshSession(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)
	clearGlobalMaintenance(t, agIr)

	until := time.Now().Add(2 * time.Hour).Unix()
	resp, body := meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/maintenance",
		map[string]any{"until": until})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set: status %d: %s", resp.StatusCode, body)
	}
	var set map[string]any
	if err := json.Unmarshal([]byte(body), &set); err != nil {
		t.Fatalf("set body not json: %v (%s)", err, body)
	}
	m, _ := set["maintenance"].(map[string]any)
	if m == nil || m["active"] != true || int64(m["until"].(float64)) != until {
		t.Fatalf("set response wrong: %v", set)
	}

	// GET sees the active window.
	m = meshGetMap(t, c, srv, "/api/ui/agents/"+agIr+"/maintenance")
	got, _ := m["maintenance"].(map[string]any)
	if got == nil || got["active"] != true || int64(got["until"].(float64)) != until {
		t.Fatalf("get response wrong: %v", m)
	}

	// Clear with until=0.
	resp, body = meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/maintenance",
		map[string]any{"until": 0})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("clear: status %d: %s", resp.StatusCode, body)
	}
	m = meshGetMap(t, c, srv, "/api/ui/agents/"+agIr+"/maintenance")
	got, _ = m["maintenance"].(map[string]any)
	if got == nil || got["active"] != false || got["until"].(float64) != 0 {
		t.Fatalf("cleared state wrong: %v", m)
	}

	// The action is audited with a summary line.
	entries, err := uiStoreHandle{s}.auditViewList("", "mesh.agent.maintenance", "", 0, 0, 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("maintenance audit rows: n=%d err=%v", len(entries), err)
	}
	if !strings.Contains(entries[0].Summary, "maintenance window for agent "+agIr) {
		t.Fatalf("audit summary missing: %+v", entries[0])
	}
}

func TestUIMaintenanceValidation(t *testing.T) {
	srv := uiServer(t)
	c := meshSession(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)
	clearGlobalMaintenance(t, agIr)

	// Unknown agent → 404.
	resp, _ := meshPutRaw(t, c, srv, "POST", "/api/ui/agents/agt_nope/maintenance",
		map[string]any{"until": time.Now().Add(time.Hour).Unix()})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown agent: status %d, want 404", resp.StatusCode)
	}

	// Past until → 400 (clearing is explicit 0, not a past timestamp).
	resp, _ = meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/maintenance",
		map[string]any{"until": time.Now().Add(-time.Minute).Unix()})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("past until: status %d, want 400", resp.StatusCode)
	}

	// More than 30 days ahead → 400.
	resp, _ = meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/maintenance",
		map[string]any{"until": time.Now().Add(31 * 24 * time.Hour).Unix()})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("too-far until: status %d, want 400", resp.StatusCode)
	}

	// Broken body → 400.
	resp, _ = meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/maintenance",
		map[string]any{"until": "tomorrow"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body: status %d, want 400", resp.StatusCode)
	}
}

func TestMaintenancePersistsAcrossRestart(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)

	e1 := newMeshAlertEngine()
	e1.loadMeshAlertSettings()
	until := time.Now().Add(time.Hour).Round(time.Second)
	if err := e1.maintenanceSet(agIr, until); err != nil {
		t.Fatalf("maintenanceSet: %v", err)
	}

	// A "restarted" engine restores the window from the sealed settings file.
	e2 := newMeshAlertEngine()
	e2.loadMeshAlertSettings()
	got, active := e2.maintenanceUntil(agIr)
	if !active || got != until.Unix() {
		t.Fatalf("window not restored: until=%d active=%v (want %d)", got, active, until.Unix())
	}

	// Clearing persists too.
	if err := e2.maintenanceSet(agIr, time.Time{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	e3 := newMeshAlertEngine()
	e3.loadMeshAlertSettings()
	if _, active := e3.maintenanceUntil(agIr); active {
		t.Fatal("cleared window must stay cleared after reload")
	}
}

func TestMaintenanceSilencesTunnelAlerts(t *testing.T) {
	e, n := newTestEngine13(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)

	// tl-a (iran→foreign, engine chisel) is DOWN per the iran agent's report.
	if _, err := s.GetTunnelByKey("tl-a"); err != nil {
		t.Fatal(err)
	}
	meshRuntimeMu.Lock()
	meshRuntimeBy[agIr] = []protocol.TunnelRuntime{
		{TunnelID: "tl-a", Engine: "chisel", Up: false, WireKnown: false},
	}
	meshRuntimeMu.Unlock()
	t.Cleanup(func() {
		meshRuntimeMu.Lock()
		delete(meshRuntimeBy, agIr)
		meshRuntimeMu.Unlock()
	})

	var down *meshAlertEvent
	for _, ev := range e.evaluate(time.Now()) {
		if ev.Rule == "tunnel_down" && ev.Target == "tl-a" {
			down = &ev
			break
		}
	}
	if down == nil {
		t.Fatal("tunnel_down must fire for the down tunnel")
	}
	carries := false
	for _, tgt := range down.maintTargets {
		if tgt == agIr {
			carries = true
		}
	}
	if !carries {
		t.Fatalf("tunnel event must carry the end agents as maintenance targets: %v", down.maintTargets)
	}

	// No maintenance: delivery happens.
	e.mu.Lock()
	e.dispatchLocked(*down, false)
	e.mu.Unlock()
	if len(n.sent) == 0 {
		t.Fatal("delivery must happen without maintenance")
	}
	n.mu.Lock()
	n.sent = nil
	n.mu.Unlock()

	// Window on the iran END agent: the same event is silenced.
	e.SetMaintenance(agIr, time.Now().Add(time.Hour))
	e.mu.Lock()
	e.dispatchLocked(*down, false)
	e.mu.Unlock()
	if len(n.sent) != 0 {
		t.Fatalf("maintenance on one end's agent must silence the tunnel alert: %v", n.sent)
	}
}

func TestAlertsViewListsMaintenanceWindows(t *testing.T) {
	srv := uiServer(t)
	c := meshSession(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)
	clearGlobalMaintenance(t, agIr)

	until := time.Now().Add(2 * time.Hour).Unix()
	resp, body := meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/maintenance",
		map[string]any{"until": until})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set: status %d: %s", resp.StatusCode, body)
	}

	m := meshGetMap(t, c, srv, "/api/mesh/alerts")
	wins, _ := m["maintenance"].([]any)
	found := false
	for _, w := range wins {
		wm, _ := w.(map[string]any)
		if wm != nil && wm["target"] == agIr && int64(wm["until"].(float64)) == until {
			found = true
		}
	}
	if !found {
		t.Fatalf("alerts view must list the active window: %v", m["maintenance"])
	}
}
