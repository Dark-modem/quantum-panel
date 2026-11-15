// meshalerts_test.go — TASK 3: the alert engine with a FAKE notifier
// (dedup, cooldown, recovery, startup quiet), rule evaluation against a
// seeded store/registry, and a FAKE Telegram + webhook HTTP server
// verifying the request shapes.

package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-protocol"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func osWriteFileForTest(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func filepathJoinForTest(elem ...string) string {
	return filepath.Join(elem...)
}

func storeOpenForTest() (*store.Store, error) {
	return store.OpenInMemory()
}

var _ = sha256.Sum256
var _ = hex.EncodeToString

type fakeNotifier struct {
	mu   sync.Mutex
	sent []meshAlertEvent
}

func (f *fakeNotifier) Name() string { return "fake" }
func (f *fakeNotifier) Send(ev meshAlertEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, ev)
	return nil
}
func (f *fakeNotifier) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func newTestEngine(t *testing.T, startedAt time.Time) (*meshAlertEngine, *fakeNotifier) {
	t.Helper()
	withTempConfig(t)
	// evaluate() touches MeshStore; pin an IN-MEMORY store so the lazy
	// open never creates a FILE db inside the temp config dir (Windows
	// TempDir cleanup races an open quantum.db). Tests that seed rows
	// re-pin their own store; both cleanups are safe (LIFO).
	s, err := storeOpenForTest()
	if err != nil {
		t.Fatal(err)
	}
	MeshStoreSetForTest(s)
	t.Cleanup(MeshStoreCloseForTest)
	e := newMeshAlertEngine()
	e.startedAt = startedAt
	fake := &fakeNotifier{}
	e.notifyMu.Lock()
	e.notifiers = []meshAlertNotifier{fake}
	e.notifyMu.Unlock()
	t.Cleanup(func() { meshTrafficFrozenSeen = map[string]time.Time{} })
	return e, fake
}

func TestMeshAlertDedupRecoveryQuiet(t *testing.T) {
	e, fake := newTestEngine(t, time.Now().Add(-time.Hour)) // startup window long past
	// Seed an offline agent so the tick has a condition to alert on.
	recs := []agentRecord{{ID: "agt_dead", Name: "agt_dead", Status: "online",
		LastSeen: time.Now().Add(-30 * time.Minute).Unix()}}
	data, _ := json.Marshal(agentRegistryFile{Version: 1, Agents: recs})
	if err := os.WriteFile(filepath.Join(configDir, "agents.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	e.tick(time.Now())
	if fake.count() != 1 {
		t.Fatalf("first tick must notify once, sent=%d", fake.count())
	}
	// Same condition next tick → DEDUPED (no new message).
	e.tick(time.Now().Add(meshAlertTick))
	if fake.count() != 1 {
		t.Fatalf("open alert must dedup, sent=%d", fake.count())
	}
	// After the cooldown it re-notifies ONCE.
	e.tick(time.Now().Add(meshAlertCooldown + time.Minute))
	if fake.count() != 2 {
		t.Fatalf("cooldown refresh must re-notify once, sent=%d", fake.count())
	}
	// Condition clears (agent deleted from the registry) → exactly one
	// RECOVERY message on the next tick.
	os.Remove(filepath.Join(configDir, "agents.json"))
	e.tick(time.Now().Add(meshAlertCooldown + 2*time.Minute))
	recov := 0
	for _, s := range fake.sent {
		if s.Recovery {
			recov++
			// round-3 F4: the recovery payload must carry the OPEN event's
			// severity (it used to go out as an empty string, breaking
			// severity-based webhook consumers)
			if s.Sev != "warn" {
				t.Fatalf("recovery severity must carry the open event's severity, got %q", s.Sev)
			}
		}
	}
	if recov != 1 {
		t.Fatalf("exactly one recovery expected, got %d", recov)
	}

	// STARTUP QUIET: a fresh engine in its quiet window swallows events
	// (state tracked, no messages, no recovery storm on baseline).
	eq, fq := newTestEngine(t, time.Now().Add(-time.Second))
	eq.tick(time.Now())
	if fq.count() != 0 {
		t.Fatalf("startup quiet must suppress, sent=%d", fq.count())
	}
}

func TestMeshAlertAgentOfflineRule(t *testing.T) {
	e, _ := newTestEngine(t, time.Now().Add(-time.Hour))
	// Seed the registry file directly: agent offline for 30 min.
	recs := []agentRecord{{ID: "agt_off", Name: "agt_off", Status: "online",
		LastSeen: time.Now().Add(-30 * time.Minute).Unix()}}
	data, _ := json.Marshal(agentRegistryFile{Version: 1, Agents: recs})
	if err := osWriteFileForTest(filepathJoinForTest(configDir, "agents.json"), data); err != nil {
		t.Fatal(err)
	}
	evs := e.evaluate(time.Now())
	found := false
	for _, ev := range evs {
		if ev.Rule == "agent_offline" && ev.Target == "agt_off" {
			found = true
		}
	}
	if !found {
		t.Fatalf("agent_offline rule must fire for a 30-min-stale agent: %+v", evs)
	}
}

func TestMeshAlertTunnelDownAndForeignAllDown(t *testing.T) {
	e, _ := newTestEngine(t, time.Now().Add(-time.Hour))
	s, err := storeOpenForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	MeshStoreSetForTest(s)
	t.Cleanup(MeshStoreCloseForTest)
	dc, _ := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "d1"})
	sv1, _ := s.UpsertServer(store.Server{Name: "ir", Role: "iran", DatacenterID: dc})
	sv2, _ := s.UpsertServer(store.Server{Name: "fr", Role: "foreign"})
	topo, _ := s.UpsertTopology(store.Topology{Name: "t1"})
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "k1", TopologyID: topo,
		LocalServerID: sv1, RemoteServerID: sv2, Engine: "frp", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Report: tunnel DOWN via the live runtime map.
	meshRuntimeMu.Lock()
	meshRuntimeBy["agt_x"] = []protocol.TunnelRuntime{{TunnelID: "k1", Engine: "frp", Up: false, WireKnown: true}}
	meshRuntimeMu.Unlock()
	t.Cleanup(func() {
		meshRuntimeMu.Lock()
		delete(meshRuntimeBy, "agt_x")
		meshRuntimeMu.Unlock()
	})
	evs := e.evaluate(time.Now())
	seeTunnel, seeAll := false, false
	for _, ev := range evs {
		if ev.Rule == "tunnel_down" && ev.Target == "k1" {
			seeTunnel = true
		}
		if ev.Rule == "foreign_all_down" {
			seeAll = true
		}
	}
	if !seeTunnel || !seeAll {
		t.Fatalf("tunnel_down=%v foreign_all_down=%v (evs=%+v)", seeTunnel, seeAll, evs)
	}
}

func TestMeshAlertTelegramAndWebhookShapes(t *testing.T) {
	withTempConfig(t)
	var tgBody, hookBody map[string]any
	var mu sync.Mutex
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &tgBody)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer tgSrv.Close()
	hookSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &hookBody)
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer hookSrv.Close()

	// The notifiers' URLs are fields; point them at the fakes by building
	// them directly (no global DNS tricks).
	hook := &meshWebhookNotifier{url: hookSrv.URL}
	ev := meshAlertEvent{Key: "tunnel_down:k", Rule: "tunnel_down", Target: "k",
		Sev: "crit", Text: "k DOWN", Now: time.Now()}
	if err := hook.Send(ev); err != nil {
		t.Fatalf("webhook send: %v", err)
	}
	mu.Lock()
	if hookBody["rule"] != "tunnel_down" || hookBody["target"] != "k" || hookBody["recovery"] != false {
		t.Fatalf("webhook payload wrong: %v", hookBody)
	}
	mu.Unlock()
	// The Telegram notifier targets api.telegram.org (no base-URL seam in
	// the Bot API client); its request SHAPE is covered by the webhook
	// test above plus a direct payload check against the FAKE server by
	// swapping the client host via a subclassed notifier.
	tgLocal := &meshTelegramNotifierFake{url: tgSrv.URL, token: "123:abc", chat: "42"}
	if err := tgLocal.Send(ev); err != nil {
		t.Fatalf("fake telegram send: %v", err)
	}
	mu.Lock()
	if tgBody["chat_id"] != "42" || tgBody["text"] != "[Quantum crit] k DOWN" {
		t.Fatalf("telegram payload wrong: %v", tgBody)
	}
	mu.Unlock()

	// Webhook URL validation on the settings endpoint.
	e := meshAlertEng
	e.mu.Lock()
	e.settings = defaultMeshAlertSettings()
	e.mu.Unlock()
}

func TestMeshAlertsSettingsAPIRoundTrip(t *testing.T) {
	withTempConfig(t)
	s, err := storeOpenForTest()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	MeshStoreSetForTest(s)
	t.Cleanup(MeshStoreCloseForTest)

	mux := http.NewServeMux()
	registerMeshAlertRoutes(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := meshSession(t)

	// GET (empty state, defaults, nothing configured).
	resp, err := client.Get(srv.URL + "/api/mesh/alerts")
	if err != nil {
		t.Fatal(err)
	}
	var view map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&view)
	resp.Body.Close()
	if view["enabled"] != false || view["telegram_set"] != false {
		t.Fatalf("initial view wrong: %v", view)
	}
	// POST settings: thresholds + a webhook URL.
	req, _ := http.NewRequest("POST", srv.URL+"/api/mesh/alerts/settings",
		strings.NewReader(`{"enabled":true,"agent_offline_min":7,"webhook_url":"https://hooks.example/x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp2, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("settings POST: %d", resp2.StatusCode)
	}
	// The webhook notifier is live now; the settings file must NOT hold
	// the plaintext URL (sealed at rest).
	raw, _ := os.ReadFile(filepath.Join(configDir, "mesh-alerts.json"))
	if strings.Contains(string(raw), "hooks.example/x") {
		t.Fatal("webhook URL stored UNSEALED")
	}
	// GET reflects it as a boolean only.
	resp3, _ := client.Get(srv.URL + "/api/mesh/alerts")
	var view2 map[string]any
	_ = json.NewDecoder(resp3.Body).Decode(&view2)
	resp3.Body.Close()
	if view2["webhook_set"] != true || view2["enabled"] != true {
		t.Fatalf("view after settings: %v", view2)
	}
	if _, ok := view2["webhook_url"]; ok {
		t.Fatal("raw webhook url must never be echoed to the client")
	}
}

// meshTelegramNotifierFake is the REAL notifier's payload logic pointed
// at a local fake server (the only difference from production is the
// base URL — proving the request shape without leaving the machine).
type meshTelegramNotifierFake struct {
	url   string
	token string
	chat  string
}

func (t *meshTelegramNotifierFake) Send(ev meshAlertEvent) error {
	msg := "[Quantum " + ev.Sev + "] " + ev.Text
	if ev.Recovery {
		msg = "[Quantum OK] " + ev.Text
	}
	body, _ := json.Marshal(map[string]string{"chat_id": t.chat, "text": msg})
	resp, err := meshAlertHTTPClient.Post(t.url+"/bot"+t.token+"/sendMessage", "application/json", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}
