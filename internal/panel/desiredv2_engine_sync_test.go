package panel

// desiredv2_engine_sync_test.go — phase 31 (UI-API-REQUESTS §10.4,
// contract §2.6): the re-sync-on-enable semantics.
//   - the compile stamps artifact engines' tunnel params with the latest
//     artifact version for the agent's arch (the change-detector that
//     makes a new artifact produce a real push and an agent-side restart);
//   - enable of an ALREADY enabled stale IDLE engine cycles disable→enable
//     so the agent reinstalls from the latest artifact;
//   - the cycle is skipped honestly (offline / no tunnels to compile),
//     never leaves the engine disabled, and re-POST enable stays idempotent.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	"github.com/Dark-modem/quantum-protocol"
)

func engineSyncFixture(t *testing.T) *store.Store {
	t.Helper()
	withTempConfig(t)
	s := withMeshStore(t)
	dcIr, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "es-iran"})
	if err != nil {
		t.Fatal(err)
	}
	dcFr, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "es-foreign"})
	if err != nil {
		t.Fatal(err)
	}
	linkedServer, err := s.UpsertServer(store.Server{Role: "iran", Name: "es-ir", DatacenterID: dcIr})
	if err != nil {
		t.Fatal(err)
	}
	peer, err := s.UpsertServer(store.Server{Role: "foreign", Name: "es-fr", DatacenterID: dcFr})
	if err != nil {
		t.Fatal(err)
	}
	topoID, err := s.UpsertTopology(store.Topology{Name: "es-topo", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, linkedServer); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, peer); err != nil {
		t.Fatal(err)
	}
	agentID := "agt_es"
	if err := s.UpsertAgent(store.Agent{ID: agentID, ServerID: linkedServer, Name: "es-ir",
		Role: "iran", Status: "online", Version: "v11", Arch: "amd64", OS: "linux",
		LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	// The agent has negotiated v2 (compileAndPushAgent reads the STORE's
	// negotiated version when no live session exists).
	if err := s.SetAgentV2Session(agentID, protocol.ProtoVersionV2, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "tl_es_1", TopologyID: topoID,
		LocalServerID: linkedServer, RemoteServerID: peer, Engine: "matrix",
		EngineParams: "{}", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	return s
}

func seedArtifact(t *testing.T, s *store.Store, engine, goarch, version string, uploaded int64) {
	t.Helper()
	if _, err := s.UpsertEngineArtifact(store.EngineArtifact{
		Engine: engine, Version: version, Goos: "linux", Goarch: goarch,
		Artifact: engine + "-" + version + "-" + goarch + ".tar.gz",
		SHA256:   strings.Repeat("a", 64), Size: 10, Signature: "c2ln",
		UploadedAt: uploaded,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLatestArtifactVersionRule(t *testing.T) {
	// Highest version STRING wins (the agent's manifest walk is a strict
	// `>` over the version strings — "0.61.2" > "0.61.10" lexicographically,
	// a quirk this rule deliberately mirrors so the stamp always names the
	// version the agent will actually download); equal versions keep the
	// newest upload (the list is uploaded_at DESC).
	arts := []store.EngineArtifact{
		{Engine: "frp", Goarch: "amd64", Version: "0.61.2", UploadedAt: 200},
		{Engine: "frp", Goarch: "amd64", Version: "0.61.10", UploadedAt: 100},
		{Engine: "frp", Goarch: "arm64", Version: "0.61.3", UploadedAt: 300},
		{Engine: "frp", Goarch: "amd64", Version: "", UploadedAt: 400},
	}
	if got := latestArtifactVersionIn(arts, "frp", "amd64"); got != "0.61.2" {
		t.Fatalf("amd64 winner: %q (want 0.61.2 — the agent's strict string compare)", got)
	}
	if got := latestArtifactVersionIn(arts, "frp", "arm64"); got != "0.61.3" {
		t.Fatalf("arm64 winner: %q", got)
	}
	if got := latestArtifactVersionIn(arts, "backhaul", "amd64"); got != "" {
		t.Fatalf("missing engine: %q", got)
	}
}

func TestStampArtifactVersions(t *testing.T) {
	s := engineSyncFixture(t)
	seedArtifact(t, s, "frp", "amd64", "0.61.2", 100)
	specs := []protocol.TunnelSpec{
		{ID: "tl_frp", Engine: "frp", EngineParams: json.RawMessage(`{"port":7000}`)},
		{ID: "tl_mx", Engine: "matrix", EngineParams: json.RawMessage(`{"role":"frpc"}`)},
		{ID: "tl_noparams", Engine: "frp", EngineParams: nil},
		{ID: "tl_bad", Engine: "frp", EngineParams: json.RawMessage(`not-json`)},
		{ID: "tl_array", Engine: "frp", EngineParams: json.RawMessage(`[1,2]`)},
	}
	stampArtifactVersions(s, specs, "amd64")
	var got map[string]any
	if err := json.Unmarshal(specs[0].EngineParams, &got); err != nil {
		t.Fatal(err)
	}
	if got["_artifact_version"] != "0.61.2" || got["port"] != float64(7000) {
		t.Fatalf("frp stamp: %v", specs[0].EngineParams)
	}
	if strings.Contains(string(specs[1].EngineParams), "_artifact_version") {
		t.Fatalf("matrix is kernel tooling — must stay unstamped: %s", specs[1].EngineParams)
	}
	if len(specs[2].EngineParams) != 0 || strings.Contains(string(specs[3].EngineParams), "_artifact_version") ||
		strings.Contains(string(specs[4].EngineParams), "_artifact_version") {
		t.Fatalf("absent/invalid/array params must stay untouched: %v/%s/%s",
			specs[2].EngineParams, specs[3].EngineParams, specs[4].EngineParams)
	}
	// Idempotent: stamping twice keeps the bytes stable (no push churn).
	before := string(specs[0].EngineParams)
	stampArtifactVersions(s, specs, "amd64")
	if string(specs[0].EngineParams) != before {
		t.Fatalf("re-stamp must be byte-stable: %s != %s", specs[0].EngineParams, before)
	}
}

func TestCompileStampsArtifactVersionAndFlipsPush(t *testing.T) {
	s := engineSyncFixture(t)
	ag, err := s.GetAgent("agt_es")
	if err != nil {
		t.Fatal(err)
	}
	// Without an artifact: the compile leaves the params alone.
	desired, _, _, err := compileDesiredV2(s, ag)
	if err != nil || desired == nil {
		t.Fatalf("compile: %v", err)
	}
	var unstamped map[string]any
	if err := json.Unmarshal(desired.Tunnels[0].EngineParams, &unstamped); err != nil {
		t.Fatal(err)
	}
	if _, ok := unstamped["_artifact_version"]; ok {
		t.Fatalf("no artifact → no stamp: %s", desired.Tunnels[0].EngineParams)
	}
	// With an artifact for the agent's arch: the stamp rides the doc and a
	// version bump flips desiredEqual (→ the push reaches the agent).
	seedArtifact(t, s, "frp", "amd64", "0.61.2", 100)
	// The tunnel under compile uses matrix; give the MATRIX-free path a
	// real check instead: swap the tunnel's engine to frp.
	tunnels, _ := s.TunnelsForServer(ag.ServerID)
	if len(tunnels) != 1 {
		t.Fatalf("fixture tunnels: %d", len(tunnels))
	}
	if err := s.UpdateTunnel(store.Tunnel{ID: tunnels[0].ID, TunnelKey: "tl_es_1", TopologyID: tunnels[0].TopologyID,
		LocalServerID: tunnels[0].LocalServerID, RemoteServerID: tunnels[0].RemoteServerID,
		Engine: "frp", EngineParams: `{"port":7000}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	desired, _, _, err = compileDesiredV2(s, ag)
	if err != nil || desired == nil {
		t.Fatalf("compile with artifact: %v", err)
	}
	var stamped map[string]any
	if err := json.Unmarshal(desired.Tunnels[0].EngineParams, &stamped); err != nil {
		t.Fatal(err)
	}
	if stamped["_artifact_version"] != "0.61.2" {
		t.Fatalf("compile stamp: %s", desired.Tunnels[0].EngineParams)
	}
	// Same version again → byte-stable → no push (minimal-revision rule).
	candidate, _, _, err := compileDesiredV2(s, ag)
	if err != nil {
		t.Fatal(err)
	}
	if !desiredEqual(string(mustJSON(desired)), string(mustJSON(candidate))) {
		t.Fatal("same-artifact recompiles must be desiredEqual")
	}
	// A newer artifact changes the stamp → the documents differ → push.
	seedArtifact(t, s, "frp", "amd64", "0.62.0", 200)
	updated, _, _, err := compileDesiredV2(s, ag)
	if err != nil {
		t.Fatal(err)
	}
	if desiredEqual(string(mustJSON(desired)), string(mustJSON(updated))) {
		t.Fatal("a newer artifact must flip desiredEqual (→ real push)")
	}
	var bumped map[string]any
	if err := json.Unmarshal(updated.Tunnels[0].EngineParams, &bumped); err != nil {
		t.Fatal(err)
	}
	if bumped["_artifact_version"] != "0.62.0" {
		t.Fatalf("bumped stamp: %s", updated.Tunnels[0].EngineParams)
	}
}

// engineEnableRequest calls the enable endpoint the way the route would.
func engineEnableRequest(t *testing.T, agentID, engine string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/ui/agents/"+agentID+"/engines/"+engine+"/enable", nil)
	r.SetPathValue("id", agentID)
	r.SetPathValue("engine", engine)
	w := httptest.NewRecorder()
	handleUIEngineEnable(w, r)
	return w
}

func seedStaleIdleEngine(t *testing.T, s *store.Store) {
	t.Helper()
	seedArtifact(t, s, "frp", "amd64", "0.61.2", 100)
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: "agt_es", Engine: "frp",
		Installed: true, Version: "0.61.0", Desired: true, UpdatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	// The engine is IDLE: the fixture's tunnel runs matrix, not frp.
}

func TestEngineEnableCyclesStaleIdleEngine(t *testing.T) {
	s := engineSyncFixture(t)
	seedStaleIdleEngine(t, s)
	// Fake a live v2 session so both pushes actually "deliver".
	oldSession := uiEngineLiveV2Session
	uiEngineLiveV2Session = func(string) int { return protocol.ProtoVersionV2 }
	t.Cleanup(func() { uiEngineLiveV2Session = oldSession })

	w := engineEnableRequest(t, "agt_es", "frp")
	if w.Code != http.StatusOK {
		t.Fatalf("enable: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Status string         `json:"status"`
		Update string         `json:"update"`
		Push   map[string]any `json:"push"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Status != "ok" || resp.Update != "cycled" {
		t.Fatalf("response: %+v", resp)
	}
	if pushed, _ := resp.Push["pushed"].(bool); !pushed {
		t.Fatalf("cycle push: %+v", resp.Push)
	}
	// Two delivered pushes: the disable document, then the enable one.
	entries, err := s.Audit(100)
	if err != nil {
		t.Fatal(err)
	}
	pushes := 0
	for _, e := range entries {
		if e.Action == "mesh.desired.push" {
			pushes++
		}
	}
	if pushes != 2 {
		t.Fatalf("cycle must deliver TWO pushes, saw %d (audit %+v)", pushes, entries)
	}
	// The engine ends desired-enabled (the store must not keep it disabled).
	if desired, _ := s.AgentEngineDesired("agt_es", "frp"); !desired {
		t.Fatal("after the cycle the engine must be desired-enabled")
	}
}

func TestEngineEnableSkipsCycleOffline(t *testing.T) {
	s := engineSyncFixture(t)
	seedStaleIdleEngine(t, s)
	// No live session (the real seam returns 0 for a session-less agent):
	// the cycle cannot deliver its disable step, so it must not run.
	w := engineEnableRequest(t, "agt_es", "frp")
	if w.Code != http.StatusOK {
		t.Fatalf("enable offline: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Update string `json:"update"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Update != "skipped" {
		t.Fatalf("offline cycle must be skipped honestly: %+v", resp)
	}
	// The engine stays desired-enabled and the QUEUED document (delivered
	// at hello) is the ENABLED one — never the cycle's disable document.
	if desired, _ := s.AgentEngineDesired("agt_es", "frp"); !desired {
		t.Fatal("a skipped cycle must not leave the engine disabled")
	}
	pending, ok := meshPendingDesiredV2("agt_es")
	if !ok {
		t.Fatal("the enabled desired document must be queued for hello")
	}
	for _, e := range pending.Engines {
		if e.ID == "frp" && !e.Enabled {
			t.Fatal("the queued document must keep frp ENABLED (disable doc overwritten)")
		}
	}
}

func TestEngineEnableFreshEngineNormalFlow(t *testing.T) {
	s := engineSyncFixture(t)
	seedArtifact(t, s, "frp", "amd64", "0.61.2", 100)
	// No agent_engines row → not stale-installed → the plain enable flow
	// (desired flag flips false→true; no cycle, no update field).
	oldSession := uiEngineLiveV2Session
	uiEngineLiveV2Session = func(string) int { return protocol.ProtoVersionV2 }
	t.Cleanup(func() { uiEngineLiveV2Session = oldSession })
	w := engineEnableRequest(t, "agt_es", "frp")
	if w.Code != http.StatusOK {
		t.Fatalf("enable fresh: status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), `"update"`) {
		t.Fatalf("fresh enable must not report an update field: %s", w.Body.String())
	}
	if desired, _ := s.AgentEngineDesired("agt_es", "frp"); !desired {
		t.Fatal("enable must set the desired flag")
	}
}

func TestEngineEnableWithTunnelsNoCycle(t *testing.T) {
	s := engineSyncFixture(t)
	seedStaleIdleEngine(t, s)
	// Give the agent an ENABLED frp tunnel → the engine is not idle; the
	// upgrade rides the compile-time stamp (no disable→enable cycle).
	topoID, err := s.UpsertTopology(store.Topology{Name: "es-topo-2", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	servers, _ := s.ListServers()
	var local, remote int64
	for _, sv := range servers {
		if sv.Role == "iran" {
			local = sv.ID
		} else {
			remote = sv.ID
		}
	}
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "tl_es_frp", TopologyID: topoID,
		LocalServerID: local, RemoteServerID: remote, Engine: "frp",
		EngineParams: `{"port":7001}`, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	oldSession := uiEngineLiveV2Session
	uiEngineLiveV2Session = func(string) int { return protocol.ProtoVersionV2 }
	t.Cleanup(func() { uiEngineLiveV2Session = oldSession })
	w := engineEnableRequest(t, "agt_es", "frp")
	if w.Code != http.StatusOK {
		t.Fatalf("enable with tunnels: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Update string `json:"update"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Update != "" {
		t.Fatalf("an engine with tunnels must NOT cycle (stamp handles it): %+v", resp)
	}
}

func auditCount(t *testing.T, s *store.Store) int {
	t.Helper()
	entries, err := s.Audit(500)
	if err != nil {
		t.Fatal(err)
	}
	pushes := 0
	for _, e := range entries {
		if e.Action == "mesh.desired.push" {
			pushes++
		}
	}
	return pushes
}
