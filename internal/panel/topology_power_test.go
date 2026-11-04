package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// powerLab wires a topology holding the "main" tunnel with faked systemctl,
// service-active probes and the host view. It returns the created topology's
// id and the recorded systemctl calls.
func powerLab(t *testing.T, greActive bool) (string, *[]string) {
	t.Helper()
	dir := t.TempDir()
	oldDir := configDir
	configDir = dir
	t.Cleanup(func() { configDir = oldDir })
	topologyFixture(t)
	topo := topologyCreated(t, `{"name":"power pair","role":"iran","local_ip":"203.0.113.1","remote_ip":"203.0.113.2","tunnel_ids":["main"]}`)
	oldStatus := topologyLocalStatus
	topologyLocalStatus = func() tunnelStatus {
		return tunnelStatus{Gre: greState{Exists: true, Name: "gre-tunnel"}}
	}
	t.Cleanup(func() { topologyLocalStatus = oldStatus })
	oldActive := engineServiceActive
	engineServiceActive = func(s string) bool { return s == "gre-tunnel" && greActive }
	t.Cleanup(func() { engineServiceActive = oldActive })
	return topo.ID, recordSystemctl(t, "")
}

func powerPost(t *testing.T, id, body string) map[string]any {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/topologies/"+id+"/active", strings.NewReader(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	handleTopologyPowerPost(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("power %s: status=%d body=%s", body, w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("power response: %s (%v)", w.Body.String(), err)
	}
	return out
}

func powerDisabled(t *testing.T, id string) bool {
	t.Helper()
	w := topologyRequest(http.MethodGet, id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get topology: status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Status    string `json:"status"`
		TopoNames []struct {
			Disabled bool `json:"disabled"`
		} `json:"topologies"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode list: %s (%v)", w.Body.String(), err)
	}
	if len(response.TopoNames) != 1 {
		t.Fatalf("expected 1 topology, got %d", len(response.TopoNames))
	}
	return response.TopoNames[0].Disabled
}

func TestTopologyPowerDeactivatesStopsServicesAndPersists(t *testing.T) {
	id, calls := powerLab(t, true)
	out := powerPost(t, id, `{"active":false}`)
	if out["active"] != false {
		t.Fatalf("expected active=false, got %v", out["active"])
	}
	if !strings.Contains(strings.Join(*calls, "\n"), "stop gre-tunnel") {
		t.Fatalf("deactivate must stop the GRE unit, calls: %v", *calls)
	}
	if !powerDisabled(t, id) {
		t.Fatal("the topology must persist disabled=true")
	}
	if _, err := os.Stat(filepath.Join(configDir, "tunnels-disabled")); err != nil {
		t.Fatalf("the tunnels-disabled marker must exist for the watchdog: %v", err)
	}
}

func TestTopologyPowerActivatesStartsServicesAndClearsMarker(t *testing.T) {
	// Start from a disabled topology so activate has something to undo.
	id, calls := powerLab(t, false)
	powerPost(t, id, `{"active":false}`)
	powerPost(t, id, `{"active":true}`)
	if !strings.Contains(strings.Join(*calls, "\n"), "start gre-tunnel") {
		t.Fatalf("activate must start the GRE unit, calls: %v", *calls)
	}
	if powerDisabled(t, id) {
		t.Fatal("the topology must persist disabled=false")
	}
	if _, err := os.Stat(filepath.Join(configDir, "tunnels-disabled")); !os.IsNotExist(err) {
		t.Fatalf("the tunnels-disabled marker must be gone: %v", err)
	}
}

func TestTopologyPowerRejectsBadBody(t *testing.T) {
	powerLab(t, false)
	r := httptest.NewRequest(http.MethodPost, "/api/topologies/x/active", strings.NewReader(`{"active":"yes"}`))
	r.SetPathValue("id", "x")
	w := httptest.NewRecorder()
	handleTopologyPowerPost(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-bool active, got %d", w.Code)
	}
}

func TestTopologyPowerUnknownTopologyIs404(t *testing.T) {
	powerLab(t, false)
	r := httptest.NewRequest(http.MethodPost, "/api/topologies/nope/active", strings.NewReader(`{"active":true}`))
	r.SetPathValue("id", "nope")
	w := httptest.NewRecorder()
	handleTopologyPowerPost(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}
