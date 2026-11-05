package panel

// e2e_mesh_test.go — end-to-end management-plane harness over a real
// httptest server and N fake v2 WebSocket agents: enroll → link → catalog →
// generate → desired push/ack → StateReportV2 ingestion (usage with counter
// resets) → weight change (minimal revision) → agent drop (state kept) →
// v1 agents untouched. Loopback only, real store seam (in-memory), no
// external network.

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
	"github.com/gorilla/websocket"
)

// meshE2EServer stands up the agent + mesh routes on one httptest server.
func meshE2EServer(t *testing.T) *httptest.Server {
	t.Helper()
	withTempConfig(t)
	withMeshStore(t)
	mux := http.NewServeMux()
	registerMeshRoutes(mux, "")
	mux.HandleFunc("POST /api/agents/tokens", handleAgentTokenPost)
	mux.HandleFunc("POST /api/agents/enroll", handleAgentEnroll)
	mux.HandleFunc("GET /api/agent-ws", handleAgentWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		agentHubDrainAll()
		srv.Close()
	})
	return srv
}

// meshSession forges an authenticated panel session and returns a client
// whose requests carry the session cookie + CSRF header.
func meshSession(t *testing.T) *http.Client {
	t.Helper()
	tok := newSessionToken()
	addSession(tok)
	touchSession(tok)
	return &http.Client{Transport: &meshSessionTransport{tok: tok}}
}

type meshSessionTransport struct{ tok string }

func (m *meshSessionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Del("Cookie")
	r.AddCookie(&http.Cookie{Name: "gre_session", Value: m.tok})
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		r.Header.Set("X-CSRF-Token", GenerateCSRFToken(m.tok))
	}
	return http.DefaultTransport.RoundTrip(r)
}

// meshPut is a small JSON helper for mesh API calls in tests.
func meshPut(t *testing.T, c *http.Client, srv *httptest.Server, method, path string, body any) map[string]any {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: HTTP %d: %v", method, path, resp.StatusCode, out)
	}
	return out
}

// fakeAgentV2 is a fake protocol-v2 agent session.
type fakeAgentV2 struct {
	t   *testing.T
	ws  *websocket.Conn
	id  string
	key string
	// welcomeVersion is the negotiated version the panel's welcome
	// payload declared (integration Step 2: must be 2 for v2 agents).
	welcomeVersion int
}

func dialFakeAgentV2(t *testing.T, srv *httptest.Server, id, key string) *fakeAgentV2 {
	t.Helper()
	hdr := http.Header{}
	hdr.Set(agentIDHeader, id)
	hdr.Set(agentKeyHeader, key)
	ws, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/agent-ws", hdr)
	if err != nil {
		t.Fatalf("fake agent dial: %v (resp %v)", err, resp)
	}
	t.Cleanup(func() { ws.Close() })
	fa := &fakeAgentV2{t: t, ws: ws, id: id, key: key}
	// hello with envelope V=2 → the panel negotiates v2 for this session.
	hello := protocol.Hello{ID: id, Version: "2.0.0", Hostname: "fake-" + id, OS: "linux", Arch: "amd64",
		MaxVersion: protocol.ProtoVersionV2, EngineCatalog: []string{"matrix", "fake"},
		Engines: []protocol.EngineInfo{{ID: "matrix", Version: "2.0.0"}, {ID: "fake", Version: "2.0.0"}}}
	fa.send(protocol.MsgHello, hello)
	typ, data := fa.read()
	if typ != protocol.MsgWelcome {
		t.Fatalf("fake agent: want welcome, got %s", typ)
	}
	var w protocol.Welcome
	if err := json.Unmarshal(data, &w); err != nil {
		t.Fatalf("welcome decode: %v", err)
	}
	fa.welcomeVersion = w.Version
	return fa
}

func (f *fakeAgentV2) send(typ string, payload any) {
	f.t.Helper()
	msg, err := protocol.EncodeVersioned(protocol.ProtoVersionV2, typ, payload)
	if err != nil {
		f.t.Fatalf("encode %s: %v", typ, err)
	}
	if err := f.ws.WriteMessage(websocket.TextMessage, msg); err != nil {
		f.t.Fatalf("send %s: %v", typ, err)
	}
}

func (f *fakeAgentV2) read() (string, json.RawMessage) {
	f.t.Helper()
	_, raw, err := f.ws.ReadMessage()
	if err != nil {
		f.t.Fatalf("fake agent read: %v", err)
	}
	v, typ, data, err := protocol.DecodeAny(raw)
	if err != nil {
		f.t.Fatalf("fake agent decode (v=%d): %v", v, err)
	}
	return typ, data
}

func (f *fakeAgentV2) readDesired() protocol.DesiredV2 {
	f.t.Helper()
	for {
		typ, data := f.read()
		switch typ {
		case protocol.MsgDesired:
			var d protocol.DesiredV2
			if err := json.Unmarshal(data, &d); err != nil {
				f.t.Fatalf("desired decode: %v", err)
			}
			return d
		default:
			// ignore anything else (heartbeats are one-way; the panel pushes
			// commands only on request)
		}
	}
}

func (f *fakeAgentV2) ack(d protocol.DesiredV2, ok bool, ackErr string) {
	f.t.Helper()
	f.send(protocol.MsgAck, protocol.Ack{Revision: d.Revision, OK: ok, Error: ackErr})
}

func (f *fakeAgentV2) report(rep protocol.StateReportV2) {
	f.t.Helper()
	f.send(protocol.MsgHeartbeat, protocol.HeartbeatV2{TS: time.Now().Unix(), State: &rep})
}

// e2eFixture: 2 Iran DCs × 2 servers + 1 foreign DC × 2 servers, mesh
// session client ready.
type e2eFixture struct {
	srv     *httptest.Server
	client  *http.Client
	s       *store.Store
	iran    []int64 // drak pair (0:2) + chekavak pair (2:4)
	foreign []int64
}

func e2eSetup(t *testing.T) *e2eFixture {
	t.Helper()
	oldGap := meshSampleMinGap
	meshSampleMinGap = 0 // tests send back-to-back heartbeats deliberately
	t.Cleanup(func() { meshSampleMinGap = oldGap })
	f := &e2eFixture{srv: meshE2EServer(t), client: meshSession(t)}
	f.s = meshStoreMust(t)
	drak, _ := f.s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "drak"})
	chk, _ := f.s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "chekavak"})
	hz, _ := f.s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "hetzner"})
	for i, dc := range []int64{drak, drak, chk, chk} {
		id, err := f.s.UpsertServer(store.Server{Name: fmt.Sprintf("ir-%d", i), Role: "iran", DatacenterID: dc,
			Address: fmt.Sprintf("10.0.0.%d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		f.iran = append(f.iran, id)
	}
	for i := 0; i < 2; i++ {
		id, err := f.s.UpsertServer(store.Server{Name: fmt.Sprintf("hz-%d", i), Role: "foreign", DatacenterID: hz,
			Address: fmt.Sprintf("195.201.0.%d", i+1)})
		if err != nil {
			t.Fatal(err)
		}
		f.foreign = append(f.foreign, id)
	}
	return f
}

// enrollLinkCatalog enrolls a fake agent through the real handlers, links
// it to a server and records its engine catalog.
func (f *e2eFixture) enrollLinkCatalog(t *testing.T, name string, serverID int64) (string, string) {
	t.Helper()
	id, key := agentHubEnrollAgent(t, f.srv, []string{agentCapMatrix})
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+id+"/link", map[string]any{"server_id": serverID})
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+id+"/engine-catalog",
		map[string]any{"catalog": []map[string]any{{"id": "matrix"}, {"id": "tinc"}}})
	_ = name
	return id, key
}

func TestE2EBothShapesDesiredAndReports(t *testing.T) {
	f := e2eSetup(t)

	// Shape A, per the brief: drak topology = drak pair × foreign pair × 3.
	gen := meshPut(t, f.client, f.srv, "POST", "/api/mesh/topologies/generate", TopoGenRequest{
		Name: "drak-hz", IranServers: f.iran[0:2], ForeignServers: f.foreign,
		TunnelsPerPair: 3, Engines: []string{"matrix", "tinc", "matrix"}, Enabled: true,
	})
	if n := len(gen["tunnels"].([]any)); n != 12 {
		t.Fatalf("shape A generate: %d tunnels, want 12", n)
	}
	// Second topology on the chekavak pair sharing the foreign servers.
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/topologies/generate", TopoGenRequest{
		Name: "chekavak-hz", IranServers: f.iran[2:4], ForeignServers: f.foreign,
		TunnelsPerPair: 3, Engines: []string{"matrix", "matrix", "tinc"}, Enabled: true,
	})

	// Enroll + connect one agent per topology (iran[0] and iran[2]).
	ag1, key1 := f.enrollLinkCatalog(t, "drak-1", f.iran[0])
	ag2, key2 := f.enrollLinkCatalog(t, "chek-1", f.iran[2])
	fa1 := dialFakeAgentV2(t, f.srv, ag1, key1)
	fa2 := dialFakeAgentV2(t, f.srv, ag2, key2)

	// Push: each fake agent receives its DesiredV2.
	push1 := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+ag1+"/push", nil)
	if push1["pushed"] != true {
		t.Fatalf("push1: %+v", push1)
	}
	d1 := fa1.readDesired()
	if len(d1.Tunnels) != 6 { // drak pair member ir-0: 2 foreign × 3 slots
		t.Fatalf("agent1 desired tunnels: %d, want 6", len(d1.Tunnels))
	}
	if d1.Balancing == nil || len(d1.Balancing.Weights) == 0 {
		t.Fatalf("agent1 desired must carry a balancing policy with weights: %+v", d1.Balancing)
	}
	fa1.ack(d1, true, "")

	push2 := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+ag2+"/push", nil)
	if push2["pushed"] != true {
		t.Fatalf("push2: %+v", push2)
	}
	d2 := fa2.readDesired()
	if len(d2.Tunnels) != 6 {
		t.Fatalf("agent2 desired tunnels: %d, want 6", len(d2.Tunnels))
	}
	fa2.ack(d2, true, "")

	agentTestWaitFor(t, "applied revision recorded", func() bool {
		a, err := f.s.GetAgent(ag1)
		return err == nil && a.AppliedRevision == d1.Revision
	})

	// Reports: cumulative counters over two heartbeats, then a REBOOT
	// (counter reset), then more traffic. Usage must equal the true bytes.
	tun1 := d1.Tunnels[0].ID
	fa1.report(protocol.StateReportV2{AppliedRevision: d1.Revision,
		Tunnels: []protocol.TunnelRuntime{{TunnelID: tun1, Engine: "matrix", Up: true,
			WireRx: 1000, WireTx: 800, PayloadRx: 900, PayloadTx: 700, CountersKnown: true, PayloadKnown: true}}})
	fa1.report(protocol.StateReportV2{AppliedRevision: d1.Revision,
		Tunnels: []protocol.TunnelRuntime{{TunnelID: tun1, Engine: "matrix", Up: true,
			WireRx: 3000, WireTx: 2400, PayloadRx: 2700, PayloadTx: 2100, CountersKnown: true, PayloadKnown: true}}})
	agentTestWaitFor(t, "traffic samples ingested", func() bool {
		var n int
		if err := f.s.DB().QueryRow(`SELECT COUNT(*) FROM traffic_samples`).Scan(&n); err != nil {
			return false
		}
		return n >= 2
	})
	// Reset: counters restart BELOW the previous values (reboot), then grow.
	fa1.report(protocol.StateReportV2{AppliedRevision: d1.Revision,
		Tunnels: []protocol.TunnelRuntime{{TunnelID: tun1, Engine: "matrix", Up: true,
			WireRx: 120, WireTx: 90, PayloadRx: 100, PayloadTx: 80, CountersKnown: true, PayloadKnown: true}}})
	fa1.report(protocol.StateReportV2{AppliedRevision: d1.Revision,
		Tunnels: []protocol.TunnelRuntime{{TunnelID: tun1, Engine: "matrix", Up: true,
			WireRx: 620, WireTx: 590, PayloadRx: 600, PayloadTx: 580, CountersKnown: true, PayloadKnown: true}}})
	agentTestWaitFor(t, "post-reset samples ingested", func() bool {
		var n int
		_ = f.s.DB().QueryRow(`SELECT COUNT(*) FROM traffic_samples`).Scan(&n)
		return n >= 4
	})

	usage := meshPut(t, f.client, f.srv, "GET",
		fmt.Sprintf("/api/mesh/usage?tunnel_id=%d&from=0&to=%d", usageTunnelID(t, f.s, tun1), time.Now().Unix()+2), nil)
	u := usage["usage"].(map[string]any)
	// Expected wire RX: first sample counts on a zero base (1000), then the
	// delta (2000), the reboot reset on a zero base (120), the post-reset
	// delta (500) → 3620. The reset contributes its restarted value ONCE,
	// never the pre-reboot counters again.
	if got := u["wire_rx"].(float64); got != 3620 {
		t.Fatalf("wire_rx usage after reset = %v, want 3620 (no double counting)", got)
	}
	if got := u["payload_rx"].(float64); got != 3300 { // 900 + 1800 + 100 + 500
		t.Fatalf("payload_rx usage after reset = %v, want 3300", got)
	}

	// Weight change → minimal NEW revision; unchanged push → no revision.
	meshPut(t, f.client, f.srv, "POST",
		fmt.Sprintf("/api/mesh/topologies/%d/weights", topoIDByName(t, f.s, "drak-hz")),
		map[string]any{"tunnel_id": usageTunnelID(t, f.s, tun1), "weight": 5})
	push1b := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+ag1+"/push", nil)
	if push1b["pushed"] != true {
		t.Fatalf("post-weight push must be delivered: %+v", push1b)
	}
	d1b := fa1.readDesired()
	if d1b.Revision == d1.Revision {
		t.Fatal("weight change must produce a NEW revision")
	}
	wFound := false
	for _, wr := range d1b.Balancing.Weights {
		if wr.TunnelID == tun1 && wr.Weight == 5 {
			wFound = true
		}
	}
	if !wFound {
		t.Fatalf("new revision must carry weight=5: %+v", d1b.Balancing.Weights)
	}
	fa1.ack(d1b, true, "")
	agentTestWaitFor(t, "second revision acked", func() bool {
		a, _ := f.s.GetAgent(ag1)
		return a.AppliedRevision == d1b.Revision
	})
	push1c := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+ag1+"/push", nil)
	if push1c["pushed"] != false {
		t.Fatalf("unchanged push must be skipped: %+v", push1c)
	}
}

func usageTunnelID(t *testing.T, s *store.Store, key string) int64 {
	t.Helper()
	tu, err := s.GetTunnelByKey(key)
	if err != nil {
		t.Fatalf("tunnel %s: %v", key, err)
	}
	return tu.ID
}

func topoIDByName(t *testing.T, s *store.Store, name string) int64 {
	t.Helper()
	topos, err := s.ListTopologies()
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range topos {
		if tp.Name == name {
			return tp.ID
		}
	}
	t.Fatalf("topology %q not found", name)
	return 0
}

func TestE2EAgentDropKeepsState(t *testing.T) {
	f := e2eSetup(t)
	gen := meshPut(t, f.client, f.srv, "POST", "/api/mesh/topologies/generate", TopoGenRequest{
		Name: "drop-topo", IranServers: f.iran[0:2], ForeignServers: f.foreign,
		TunnelsPerPair: 1, Engines: []string{"matrix"}, Enabled: true,
	})
	if n := len(gen["tunnels"].([]any)); n != 4 {
		t.Fatalf("generate: %d tunnels, want 2×2×1=4", n)
	}
	ag, key := f.enrollLinkCatalog(t, "dropper", f.iran[1])
	fa := dialFakeAgentV2(t, f.srv, ag, key)
	push := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+ag+"/push", nil)
	if push["pushed"] != true {
		t.Fatalf("push: %+v", push)
	}
	d := fa.readDesired()
	fa.ack(d, true, "")
	agentTestWaitFor(t, "applied", func() bool {
		a, _ := f.s.GetAgent(ag)
		return a.AppliedRevision == d.Revision
	})

	// The agent DIES: socket closes. The panel must keep the mesh state and
	// (after the staleness sweep) flip only the online flag.
	fa.ws.Close()
	agentTestWaitFor(t, "agent marked offline", func() bool {
		_, _ = f.s.MarkStaleAgents(0)
		a, err := f.s.GetAgent(ag)
		return err == nil && a.Status == "offline"
	})
	a, _ := f.s.GetAgent(ag)
	if a.AppliedRevision != d.Revision {
		t.Fatalf("drop must keep applied_revision: %+v", a)
	}
	v2, _ := f.s.AgentV2(ag)
	if v2.AppliedV2 == "" {
		t.Fatal("drop must keep the applied desired document")
	}
	tuns, _ := f.s.ListTunnels(topoIDByName(t, f.s, "drop-topo"))
	if len(tuns) != 4 {
		t.Fatalf("drop must keep topology/tunnel rows, got %d", len(tuns))
	}
	// The panel never invents state for the dead agent: pushing while it is
	// offline persists the pending desired for the next connect instead.
	push2 := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+ag+"/push", nil)
	if push2["pushed"] != false || push2["skip"] == nil {
		t.Fatalf("offline push must be skipped with a reason: %+v", push2)
	}
}

func TestE2Ev1AgentUntouched(t *testing.T) {
	f := e2eSetup(t)
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/topologies/generate", TopoGenRequest{
		Name: "v1-topo", IranServers: f.iran[0:1], ForeignServers: f.foreign,
		TunnelsPerPair: 1, Engines: []string{"matrix"}, Enabled: true,
	})
	ag, key := f.enrollLinkCatalog(t, "legacy", f.iran[0])
	// A V1 hello (v1 envelope, like every shipped agent).
	hdr := http.Header{}
	hdr.Set(agentIDHeader, ag)
	hdr.Set(agentKeyHeader, key)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.srv.URL, "http")+"/api/agent-ws", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	hello := agentHello{ID: ag, Version: "1.0.0", Caps: []string{agentCapMatrix}}
	agentTestSendEnv(t, ws, agentMsgHello, hello)
	typ, _ := agentTestReadEnv(t, ws)
	if typ != agentMsgWelcome {
		t.Fatalf("v1 agent: want welcome, got %s", typ)
	}
	// The panel must REFUSE to push DesiredV2 at a v1 session — and say why.
	res := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+ag+"/push", nil)
	if res["pushed"] != false || !strings.Contains(fmt.Sprint(res["skip"]), "v1") {
		t.Fatalf("v1 push must be skipped with a v1 reason: %+v", res)
	}
	// The v1 pending queue is untouched: no desired arrived.
	ws.SetReadDeadline(time.Now().Add(600 * time.Millisecond))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatal("v1 agent must not receive DesiredV2")
	}
}

// TestE2EForwardsWelcomeAndCatalog covers the integration session's Step-2
// wiring: the welcome payload carries the negotiated version, forwards +
// target groups compile into the pushed DesiredV2 (panel ForwardSpec form
// and balanced-group form), and hello's engine catalog is stored.
func TestE2EForwardsWelcomeAndCatalog(t *testing.T) {
	f := e2eSetup(t)

	gen := meshPut(t, f.client, f.srv, "POST", "/api/mesh/topologies/generate", TopoGenRequest{
		Name: "drak-hz", IranServers: f.iran[0:2], ForeignServers: f.foreign,
		TunnelsPerPair: 2, Engines: []string{"matrix", "fake"}, Enabled: true,
	})
	topoID := int64(gen["topology_id"].(float64))

	ag, key := f.enrollLinkCatalog(t, "drak-1", f.iran[0])
	fa := dialFakeAgentV2(t, f.srv, ag, key)

	// Forward rules via REST: one balanced (no explicit target), one
	// explicit-target, one disabled — the third must NOT be delivered.
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/forwards", store.ForwardRule{
		TopologyID: &topoID, LocalServerID: f.iran[0], ListenPort: 8443,
		Proto: "tcp", Enabled: true,
	})
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/forwards", store.ForwardRule{
		TopologyID: &topoID, LocalServerID: f.iran[0], RemoteServerID: &f.foreign[0],
		ListenPort: 2201, Proto: "tcp", Target: "10.189.0.2:22", Enabled: true,
	})
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/forwards", store.ForwardRule{
		TopologyID: &topoID, LocalServerID: f.iran[0], ListenPort: 9999,
		Proto: "tcp", Target: "10.189.0.9", Enabled: false,
	})

	if push := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+ag+"/push", nil); push["pushed"] != true {
		t.Fatalf("push: %+v", push)
	}

	// The FIRST message after push-welcome is the desired (the welcome
	// was already consumed by dialFakeAgentV2; the pending desired is
	// delivered on the next connection — so push over the live session
	// delivers here).
	typ, data := fa.read()
	if typ != protocol.MsgDesired {
		t.Fatalf("want desired, got %s: %s", typ, data)
	}
	var d protocol.DesiredV2
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}

	// Forwards: balanced + explicit delivered; disabled filtered.
	if len(d.Forwards) != 2 {
		t.Fatalf("forwards delivered = %d, want 2 (disabled filtered): %+v", len(d.Forwards), d.Forwards)
	}
	balanced := d.Forwards[0]
	if balanced.TargetGroup == "" || !strings.Contains(balanced.TargetGroup, "drak-hz") ||
		balanced.TargetPort != 8443 || !balanced.IsEnabled() {
		t.Fatalf("balanced forward = %+v", balanced)
	}
	explicit := d.Forwards[1]
	// Explicit-target form: the panel-chosen target (addr:port) rides in
	// Target; TargetPort stays 0 (= same as listen port per contract).
	if explicit.TargetGroup != "" || explicit.Target != "10.189.0.2:22" || explicit.TargetPort != 0 {
		t.Fatalf("explicit forward = %+v", explicit)
	}

	// Target groups: one per referenced group definition.
	if len(d.TargetGroups) == 0 {
		t.Fatalf("target groups missing: %+v", d.TargetGroups)
	}
	for _, g := range d.TargetGroups {
		if g.TopologyID == "" {
			t.Fatalf("group without topology: %+v", g)
		}
	}

	// The welcome the fake agent saw must carry Version=2 (it negotiated
	// v2 via the hello envelope) — assert through a second dial.
	fa2 := dialFakeAgentV2(t, f.srv, ag, key)
	if fa2.welcomeVersion != 2 {
		t.Fatalf("welcome version = %d, want 2 (v2 agents must leave the v1 path)", fa2.welcomeVersion)
	}

	// Catalog storage: the store keeps what hello reported.
	fa2.send(protocol.MsgHeartbeat, protocol.HeartbeatV2{TS: time.Now().Unix()})
	agentTestWaitFor(t, "engine catalog stored", func() bool {
		v2, err := f.s.AgentV2(ag)
		return err == nil && strings.Contains(v2.EngineCatalog, "matrix")
	})
}

var protocolTruePtr = true
