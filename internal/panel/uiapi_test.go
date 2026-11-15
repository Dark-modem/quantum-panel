package panel

// uiapi_test.go — the new UI API over a real httptest server (Phase 11):
// session shape (NO panel-host identity), exact overview aggregates from
// KNOWN seeded bytes, agent/topology/tunnel details, the 30/70 per-tunnel
// weights end to end (requirement 8), enable-before-use (requirement 5),
// and the 200-tunnel overview latency budget (requirement 9).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	protocol "github.com/Dark-modem/quantum-protocol"
)

// uiServer serves the mesh + UI API + agent-enrollment/websocket routes
// on one httptest server (the ws-level tests dial real fake agents).
func uiServer(t *testing.T) *httptest.Server {
	t.Helper()
	withTempConfig(t)
	withMeshStore(t)
	mux := http.NewServeMux()
	uiOverviewCacheReset()
	registerMeshRoutes(mux, "")
	registerUIAPIRoutes(mux, "")
	registerAgentMaintenanceRoutes(mux, "")
	registerUIFoldRoutes(mux, "")
	registerGuardRoutes(mux, "")
	registerMeshAlertRoutes(mux, "")
	registerEngineArtifactRoutes(mux, "")
	registerEngineTemplateRoutes(mux, "")
	registerBackupRoutes(mux, "")
	mux.HandleFunc("POST /api/agents/tokens", handleAgentTokenPost)
	mux.HandleFunc("POST /api/agents/enroll", handleAgentEnroll)
	mux.HandleFunc("GET /api/agent-ws", handleAgentWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// uiSeedWorld builds: Tehran(ir)/Frankfurt(fr) datacenters + servers, one
// topology, a pair with TWO tunnels (tl-a, tl-b, engine chisel), both
// agents linked, a signed chisel artifact for amd64, and v2 sessions.
func uiSeedWorld(t *testing.T, s *store.Store) (iranServer, foreignServer int64, agIr, agFr string) {
	t.Helper()
	dcIr, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "Tehran"})
	if err != nil {
		t.Fatal(err)
	}
	dcFr, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "Frankfurt"})
	if err != nil {
		t.Fatal(err)
	}
	iranServer, err = s.UpsertServer(store.Server{Role: "iran", Name: "teh-1", DatacenterID: dcIr})
	if err != nil {
		t.Fatal(err)
	}
	foreignServer, err = s.UpsertServer(store.Server{Role: "foreign", Name: "fra-1", DatacenterID: dcFr})
	if err != nil {
		t.Fatal(err)
	}
	topoID, err := s.UpsertTopology(store.Topology{Name: "main", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, iranServer); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, foreignServer); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"tl-a", "tl-b"} {
		if _, err := s.UpsertTunnel(store.Tunnel{
			TunnelKey: key, TopologyID: topoID,
			LocalServerID: iranServer, RemoteServerID: foreignServer,
			Engine: "chisel", EngineParams: "{}", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	agIr, agFr = "agt_ir_seeded", "agt_fr_seeded"
	if err := s.UpsertAgent(store.Agent{ID: agIr, ServerID: iranServer, Name: "teh-1",
		Role: "iran", Status: "online", Version: "v11", Arch: "amd64", OS: "linux",
		LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgent(store.Agent{ID: agFr, ServerID: foreignServer, Name: "fra-1",
		Role: "foreign", Status: "online", Version: "v11", Arch: "amd64", OS: "linux",
		LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertEngineArtifact(store.EngineArtifact{
		Engine: "chisel", Version: "v1.10.1", Goos: "linux", Goarch: "amd64",
		Artifact: "chisel-linux-amd64", SHA256: "deadbeef", Size: 14893056,
		Signature: "c2ln", UploadedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentV2Session(agIr, 2, `[{"id":"chisel"},{"id":"matrix"},{"id":"rathole"}]`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentV2Session(agFr, 2, `[{"id":"chisel"},{"id":"matrix"}]`); err != nil {
		t.Fatal(err)
	}
	return iranServer, foreignServer, agIr, agFr
}

// uiSeedTraffic seeds KNOWN cumulative counters: the iran agent's stream
// climbs 1000→3000→6000 wire rx (billable download = 5000 in-window),
// the foreign stream is separate and never billable.
func uiSeedTraffic(t *testing.T, s *store.Store, agIr, agFr string) {
	t.Helper()
	tids := map[string]int64{}
	for _, key := range []string{"tl-a", "tl-b"} {
		tun, err := s.GetTunnelByKey(key)
		if err != nil {
			t.Fatal(err)
		}
		tids[key] = tun.ID
	}
	// Seed at 1/4, 1/2 and 3/4 of the ELAPSED part of "today": always in
	// the past and inside [midnight, now] at ANY hour (a fixed-hours seed
	// landed in the future right after UTC midnight — a test-time bomb).
	dayStart := time.Now().Unix() - time.Now().Unix()%86400
	elapsed := time.Now().Unix() - dayStart
	steps := []struct {
		at         int64
		irRx, frRx uint64
	}{
		{dayStart + elapsed/4, 1000, 8000},
		{dayStart + elapsed/2, 3000, 9000},
		{dayStart + elapsed*3/4, 6000, 10000},
	}
	for _, st := range steps {
		for _, key := range []string{"tl-a", "tl-b"} {
			if err := s.AddTrafficSample(store.TrafficSample{
				TunnelID: tids[key], At: st.at,
				WireRx: st.irRx, WireTx: st.irRx / 8, PayloadRx: st.irRx - st.irRx/16, PayloadTx: st.irRx / 9,
				CountersKnown: true, PayloadKnown: true, AgentID: agIr,
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.AddTrafficSample(store.TrafficSample{
				TunnelID: tids[key], At: st.at + 1,
				WireRx: st.frRx, WireTx: st.frRx, PayloadRx: st.frRx, PayloadTx: st.frRx,
				CountersKnown: true, PayloadKnown: true, AgentID: agFr,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestUISessionNoHostIdentity(t *testing.T) {
	srv := uiServer(t)
	c := meshSession(t)
	resp := meshGetMap(t, c, srv, "/api/ui/session")
	for _, field := range []string{"user", "language", "csrf_token", "session_expires_at", "panel_version", "alerts_open"} {
		if _, ok := resp[field]; !ok {
			t.Fatalf("session response missing %s: %v", field, resp)
		}
	}
	text, _ := json.Marshal(resp)
	for _, banned := range []string{"local_pub", "server_ip", "hostname"} {
		if strings.Contains(string(text), banned) {
			t.Fatalf("panel-host identity field %s must not appear: %s", banned, text)
		}
	}
}

func meshGetMap(t *testing.T, c *http.Client, srv *httptest.Server, path string) map[string]any {
	t.Helper()
	resp, err := c.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("%s: status %d: %v", path, resp.StatusCode, m)
	}
	return m
}

func TestUIOverviewExactAggregates(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	_, _, agIr, agFr := uiSeedWorld(t, s)
	uiSeedTraffic(t, s, agIr, agFr)
	// Host metrics for the resource aggregates.
	if err := s.UpdateAgentResources(agIr, 4, 40, 0.5, 8192, 4096, 100, 50, 86400, "kvm"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAgentResources(agFr, 2, 20, 0.1, 4096, 1024, 50, 10, 86400, "kvm"); err != nil {
		t.Fatal(err)
	}
	// The live runtime view (healthy tunnels).
	meshRuntimeMu.Lock()
	meshRuntimeBy[agIr] = []protocol.TunnelRuntime{
		{TunnelID: "tl-a", Engine: "chisel", Up: true},
		{TunnelID: "tl-b", Engine: "chisel", Up: true},
	}
	meshRuntimeSeen[agIr] = time.Now().Unix()
	meshRuntimeMu.Unlock()

	c := meshSession(t)
	ov := meshGetMap(t, c, srv, "/api/ui/overview")
	counts := ov["counts"].(map[string]any)
	tu := counts["tunnels"].(map[string]any)
	if tu["total"].(float64) != 2 || tu["up"].(float64) != 2 {
		t.Fatalf("tunnel counts: %v", tu)
	}
	if counts["datacenters"].(map[string]any)["iran"].(float64) != 1 ||
		counts["servers"].(map[string]any)["foreign"].(float64) != 1 {
		t.Fatalf("dc/server counts: %v", counts)
	}
	// Billable = iran-stream wire rx: the store's zero-base rule counts
	// the FIRST known sample's counters as usage (documented in
	// usageAccum), so 6000 per tunnel → 12000 for the identical pair.
	// Real bytes; never multiplied by a coefficient.
	traffic := ov["traffic"].(map[string]any)
	today := traffic["today"].(map[string]any)
	if got := today["billable_download_bytes"].(float64); got != 12000 {
		t.Fatalf("billable download = %v, want 12000 (2×6000, real bytes, no coefficient)", got)
	}
	// NO panel-host traffic widget / identity anywhere.
	text, _ := json.Marshal(ov)
	for _, banned := range []string{"local_pub", "panel_traffic", "server_ip"} {
		if strings.Contains(string(text), banned) {
			t.Fatalf("banned field %s in overview", banned)
		}
	}
	// Resources aggregate across agents only.
	res := ov["resources"].(map[string]any)
	if res["agent_count"].(float64) != 2 {
		t.Fatalf("agent_count: %v", res)
	}
	cpu := res["cpu_pct"].(map[string]any)
	if cpu["sum"].(float64) != 60 {
		t.Fatalf("cpu sum: %v", cpu)
	}
	if cpu["max"].(map[string]any)["name"] != "teh-1" {
		t.Fatalf("busiest: %v", cpu["max"])
	}
	if ov["pending_confirmations"] == nil || ov["certificate"] == nil || ov["alerts"] == nil {
		t.Fatalf("overview blocks missing: %v", ov)
	}
}

func TestUIAgentDetailAndEngines(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)
	// The agent reports engine status + proc metrics via the ingest path.
	if err := s.SetAgentArchOS(agIr, "amd64", "linux"); err != nil {
		t.Fatal(err)
	}
	rep := &protocol.StateReportV2{
		Resources: &protocol.ResourceReport{CPUCores: 4, CPUPercent: 31.5, MemTotalMB: 8192, MemUsedMB: 2048,
			DiskTotalGB: 100, DiskUsedGB: 25, UptimeS: 7200, Virt: "kvm"},
		EngineStatus: []protocol.EngineStatusReport{
			{Engine: "chisel", Installed: true, Version: "v1.10.1", BytesOnDisk: 14893056, Enabled: true, Instances: 2},
			{Engine: "rathole", Installed: false, Enabled: false, LastError: "artifact missing for arch amd64"},
		},
		Proc: []protocol.TunnelProc{
			{TunnelID: "tl-a", Known: true, CPUPercent: 3.5, RSSBytes: 12 << 20, Processes: 1},
		},
		Tunnels: []protocol.TunnelRuntime{{TunnelID: "tl-a", Engine: "chisel", Up: true}},
	}
	ingestStateReportV2(agIr, rep)
	time.Sleep(50 * time.Millisecond)

	c := meshSession(t)
	d := meshGetMap(t, c, srv, "/api/ui/agents/"+agIr)
	ag := d["agent"].(map[string]any)
	if ag["arch"] != "amd64" || ag["uptime_s"].(float64) != 7200 {
		t.Fatalf("agent identity/metrics: %v", ag)
	}
	if ag["mem"].(map[string]any)["used_mb"].(float64) != 2048 {
		t.Fatalf("mem: %v", ag["mem"])
	}
	engines := d["engines"].([]any)
	var chisel, matrix, rathole map[string]any
	for _, e := range engines {
		m := e.(map[string]any)
		switch m["engine"] {
		case "chisel":
			chisel = m
		case "matrix":
			matrix = m
		case "rathole":
			rathole = m
		}
	}
	if chisel == nil || chisel["installed"] != true || chisel["bytes_on_disk"].(float64) != 14893056 ||
		chisel["instances"].(float64) != 2 {
		t.Fatalf("chisel engine row: %v", chisel)
	}
	if matrix == nil || matrix["desired_enabled"] != true {
		t.Fatalf("matrix must be desired-on by default: %v", matrix)
	}
	if rathole == nil || rathole["last_error"] == nil {
		t.Fatalf("rathole row: %v", rathole)
	}
	topos := d["topologies"].([]any)
	if len(topos) != 1 || topos[0].(map[string]any)["my_role"] != "iran" {
		t.Fatalf("topologies: %v", topos)
	}

	// The proc metric reached the tunnel detail view.
	td := meshGetMap(t, c, srv, "/api/ui/tunnels/tl-a")
	nowBlock := td["now"].(map[string]any)
	if nowBlock["rss_bytes"].(float64) != float64(12<<20) || nowBlock["cpu_percent"].(float64) != 3.5 {
		t.Fatalf("proc metrics: %v", nowBlock)
	}
}

func TestUITopologyDetailWeightsAndTraffic(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	topoIr, _, agIr, _ := uiSeedWorld(t, s)
	uiSeedTraffic(t, s, agIr, "agt_nobody")
	topos, _ := s.ListTopologies()
	topoID := topos[0].ID
	_ = topoIr
	// Per-TUNNEL weights: 30/70 on the same pair (requirement 8).
	if err := s.SetWeightPolicy(topoID, 0, 0, 0, 50); err != nil { // pair default
		t.Fatal(err)
	}
	tA, _ := s.GetTunnelByKey("tl-a")
	tB, _ := s.GetTunnelByKey("tl-b")
	if err := s.SetWeightPolicy(topoID, tA.LocalServerID, tA.RemoteServerID, tA.ID, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeightPolicy(topoID, tB.LocalServerID, tB.RemoteServerID, tB.ID, 70); err != nil {
		t.Fatal(err)
	}

	c := meshSession(t)
	td := meshGetMap(t, c, srv, fmt.Sprintf("/api/ui/topologies/%d", topoID))
	tunnels := td["tunnels"].([]any)
	weights := map[string]float64{}
	percents := map[string]float64{}
	for _, raw := range tunnels {
		row := raw.(map[string]any)
		weights[row["tunnel_key"].(string)] = row["weight"].(float64)
		percents[row["tunnel_key"].(string)] = row["resolved_percent"].(float64)
	}
	if weights["tl-a"] != 30 || weights["tl-b"] != 70 {
		t.Fatalf("per-tunnel weights lost: %v", weights)
	}
	if percents["tl-a"] != 30 || percents["tl-b"] != 70 {
		t.Fatalf("resolved percents: %v (want 30/70 — tunnel rules beat the pair default)", percents)
	}
	members := td["members"].(map[string]any)
	if len(members["iran"].([]any)) != 1 || len(members["foreign"].([]any)) != 1 {
		t.Fatalf("members by role: %v", members)
	}

	// Requirement 8 end to end: the compiled desired state shows 30/70.
	ag, err := s.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range ag {
		if a.Role != "iran" {
			continue
		}
		desired, skip, _, err := compileDesiredV2(s, a)
		if err != nil || skip != "" {
			t.Fatalf("compile: %v skip=%q", err, skip)
		}
		got := map[string]int{}
		for _, w := range desired.Balancing.Weights {
			got[w.TunnelID] = w.Weight
		}
		if got["tl-a"] != 30 || got["tl-b"] != 70 {
			t.Fatalf("compiled weights: %v (want tl-a=30 tl-b=70)", got)
		}
	}
}

func TestUIEnableBeforeUse(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	_, frServer, agIr, _ := uiSeedWorld(t, s)
	c := meshSession(t)

	// 1. Creating a tunnel for an engine that is NOT enabled on the
	// remote agent is refused with the actionable, structured error.
	resp, body := meshPutRaw(t, c, srv, "POST", "/api/mesh/tunnels", map[string]any{
		"topology_id": 1, "local_server_id": 1, "remote_server_id": frServer,
		"tunnel_key": "tl-rathole", "engine": "rathole", "engine_params": map[string]any{},
		"enabled": true,
	})
	if resp.StatusCode != 409 {
		t.Fatalf("create must refuse (409), got %d: %s", resp.StatusCode, body)
	}
	var errResp map[string]any
	if err := json.Unmarshal([]byte(body), &errResp); err != nil {
		t.Fatal(err)
	}
	if errResp["error_code"] != "E-ENGINE-NOT-ENABLED" {
		t.Fatalf("error code: %v", errResp["error_code"])
	}
	ui := errResp["ui"].(map[string]any)
	raw, _ := ui["what_to_do"].(map[string]any)
	if txt, _ := raw["en"].(string); !strings.Contains(txt, "Agents >") || !strings.Contains(txt, "Engines") {
		t.Fatalf("what_to_do must name the UI path: %q", txt)
	}

	// 2. Enabling rathole without an artifact is refused (409) with the
	// artifact message; after uploading the artifact it succeeds.
	resp, body = meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/engines/rathole/enable", nil)
	if resp.StatusCode != 409 || !strings.Contains(body, "E-ENGINE-NO-ARTIFACT") {
		t.Fatalf("enable without artifact: %d %s", resp.StatusCode, body)
	}
	if _, err := s.UpsertEngineArtifact(store.EngineArtifact{
		Engine: "rathole", Version: "v3", Goos: "linux", Goarch: "amd64",
		Artifact: "rathole-linux-amd64", SHA256: "beef", Size: 1, Signature: "c2ln",
		UploadedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
	resp, body = meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/engines/rathole/enable", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("enable with artifact: %d %s", resp.StatusCode, body)
	}
	if desired, _ := s.AgentEngineDesired(agIr, "rathole"); !desired {
		t.Fatal("rathole must be desired after enable")
	}
	// The OTHER end needs it too (the gate checks both agents).
	resp, body = meshPutRaw(t, c, srv, "POST", "/api/ui/agents/agt_fr_seeded/engines/rathole/enable", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("enable on foreign agent: %d %s", resp.StatusCode, body)
	}

	// 3. Now the tunnel create passes the readiness gate on BOTH ends.
	resp, body = meshPutRaw(t, c, srv, "POST", "/api/mesh/tunnels", map[string]any{
		"topology_id": 1, "local_server_id": 1, "remote_server_id": frServer,
		"tunnel_key": "tl-rathole", "engine": "rathole", "engine_params": map[string]any{},
		"enabled": true,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("create after enable: %d %s", resp.StatusCode, body)
	}

	// 4. Disabling an engine with enabled tunnels is refused, listing them.
	resp, body = meshPutRaw(t, c, srv, "POST", "/api/ui/agents/"+agIr+"/engines/chisel/disable", nil)
	if resp.StatusCode != 400 && resp.StatusCode != 409 {
		t.Fatalf("disable with tunnels must refuse: %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "tl-a") {
		t.Fatalf("refusal must list the blocking tunnels: %s", body)
	}
}

func meshPutRaw(t *testing.T, c *http.Client, srv *httptest.Server, method, path string, body any) (*http.Response, string) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 8192)
	for {
		n, rerr := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return resp, sb.String()
}

func TestUIOverview200TunnelsLatency(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	_, _, agIr, agFr := uiSeedWorld(t, s)
	// 200 tunnels on the same pair + samples for every one (today window).
	topos, _ := s.ListTopologies()
	topoID := topos[0].ID
	now := time.Now().Unix()
	dayStart := now - now%86400
	for i := 0; i < 200; i++ {
		key := fmt.Sprintf("tl-load-%03d", i)
		if _, err := s.UpsertTunnel(store.Tunnel{
			TunnelKey: key, TopologyID: topoID,
			LocalServerID: 1, RemoteServerID: 2, Engine: "chisel", EngineParams: "{}", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		tun, _ := s.GetTunnelByKey(key)
		for step := int64(0); step < 3; step++ {
			_ = s.AddTrafficSample(store.TrafficSample{
				TunnelID: tun.ID, At: dayStart + 600 + step*1800,
				WireRx: uint64(1000 + step*2000), WireTx: 100, PayloadRx: uint64(900 + step*1800), PayloadTx: 90,
				CountersKnown: true, PayloadKnown: true, AgentID: agIr,
			})
			_ = s.AddTrafficSample(store.TrafficSample{
				TunnelID: tun.ID, At: dayStart + 601 + step*1800,
				WireRx: uint64(500 + step*100), WireTx: 50, PayloadRx: uint64(450 + step*90), PayloadTx: 45,
				CountersKnown: true, PayloadKnown: true, AgentID: agFr,
			})
		}
	}
	c := meshSession(t)
	// Warm the cache, then measure the poll cadence the UI will use.
	meshGetMap(t, c, srv, "/api/ui/overview")
	var worst time.Duration
	for i := 0; i < 5; i++ {
		start := time.Now()
		meshGetMap(t, c, srv, "/api/ui/overview")
		if d := time.Since(start); d > worst {
			worst = d
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("overview(200 tunnels, 400 samples/tunnel): worst poll %v", worst)
	if worst > 500*time.Millisecond {
		t.Fatalf("overview poll too slow: %v (budget 500ms)", worst)
	}
}
