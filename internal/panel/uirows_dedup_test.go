package panel

// uirows_dedup_test.go — the REFACTOR-S2 safety net. Every payload whose
// builder moves onto the shared uirows.go code is built from BOTH the
// pre-refactor inline path (the frozen verbatim copies below, = the
// uiapi_topo.go / uiapi_agents.go / topology_identity.go state at main
// 5a57bdf9) and the shared builder, then byte-compared: json.Marshal each
// side (map keys sort, so identical content is byte-stable), require the
// bytes equal AND the decoded objects reflect.DeepEqual (the dispatch's
// decode-and-compare). The switch only lands when identical.
//
// Anchors: UI-API-CONTRACT.md §2.3 (agent topologies rows) and §2.19
// (legacy /api/topologies per-side identity), guarded end-to-end by
// uiapi_test.go and topology_dashboard_contract_test.go.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	protocol "github.com/Dark-modem/quantum-protocol"
	"github.com/gorilla/websocket"
)

// s2CompareJSON byte-compares two payload builds: marshal both, require
// equal bytes, then decode both and require DeepEqual objects.
func s2CompareJSON(t *testing.T, what string, oldObj, newObj any) {
	t.Helper()
	oldB, err1 := json.Marshal(oldObj)
	newB, err2 := json.Marshal(newObj)
	if err1 != nil || err2 != nil {
		t.Fatalf("%s: marshal failed %v / %v", what, err1, err2)
	}
	if !bytes.Equal(oldB, newB) {
		t.Fatalf("%s: JSON bytes differ\n old: %s\n new: %s", what, oldB, newB)
	}
	var oldAny, newAny any
	if json.Unmarshal(oldB, &oldAny) != nil || json.Unmarshal(newB, &newAny) != nil {
		t.Fatalf("%s: decode failed", what)
	}
	if !reflect.DeepEqual(oldAny, newAny) {
		t.Fatalf("%s: decoded objects differ\n old: %s\n new: %s", what, oldB, newB)
	}
}

// s2VerdictSeam pins the verdict map so both payload sides see the same
// KNOWN states (healthy / degraded / an unlisted tunnel → "unknown").
func s2VerdictSeam(t *testing.T) {
	t.Helper()
	oldVerdicts := uiVerdictsFn
	uiVerdictsFn = func() map[string]string {
		return map[string]string{"tl-a": "healthy", "tl-b": "degraded"}
	}
	t.Cleanup(func() { uiVerdictsFn = oldVerdicts })
}

// s2LatencySeam REPLACES the whole runtime maps for the test, not just
// adds one entry: earlier tests leak meshRuntimeBy rows (e.g. the
// uiapi_test.go overview fixture never cleans its agent up), and
// uiLatencyMap resolves same-tunnel conflicts by Go MAP ITERATION ORDER —
// a leaked tl-a row would race the byte-compare between the old and new
// builds (observed on Windows full-suite ordering: 41.5 vs a leaked 12.5).
func s2LatencySeam(t *testing.T, agentID string) {
	t.Helper()
	meshRuntimeMu.Lock()
	oldBy, oldSeen := meshRuntimeBy, meshRuntimeSeen
	meshRuntimeBy = map[string][]protocol.TunnelRuntime{
		agentID: {{TunnelID: "tl-a", Engine: "chisel", Up: true, WireKnown: true, LatencyMs: 41.5}},
	}
	meshRuntimeSeen = map[string]int64{agentID: time.Now().Unix()}
	meshRuntimeMu.Unlock()
	t.Cleanup(func() {
		meshRuntimeMu.Lock()
		meshRuntimeBy, meshRuntimeSeen = oldBy, oldSeen
		meshRuntimeMu.Unlock()
	})
}

// s2SeedMatrix extends the seeded world with the two matrix tunnels: tl-c
// (enabled, NO verdict → "unknown", no traffic, no latency) and tl-d
// (disabled → "disabled").
func s2SeedMatrix(t *testing.T, s *store.Store, iranServer, foreignServer int64) {
	t.Helper()
	topos, err := s.ListTopologies()
	if err != nil || len(topos) == 0 {
		t.Fatalf("list topologies: %v (%d rows)", err, len(topos))
	}
	topoID := topos[0].ID
	for _, key := range []string{"tl-c", "tl-d"} {
		if _, err := s.UpsertTunnel(store.Tunnel{
			TunnelKey: key, TopologyID: topoID,
			LocalServerID: iranServer, RemoteServerID: foreignServer,
			Engine: "chisel", EngineParams: "{}", Enabled: key == "tl-c",
		}); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- FROZEN PRE-REFACTOR COPIES (= main 5a57bdf9) -------------------------
// Verbatim bodies of the code the refactor replaces. They are the "old
// inline path" of the byte-compare; if the shared builders ever drift from
// this behavior, the tests below fail. DO NOT "fix" these to match new
// code — a change here means a JSON shape change, which needs a contract
// §-update, not a refactor commit.

// s2OldTunnelRow = uiTunnelRow (uiapi_topo.go) before S2 moved it.
func s2OldTunnelRow(h uiStoreHandle, s *store.Store, topo store.Topology, t store.Tunnel,
	verdicts map[string]string, now int64, pcts map[int64]float64, lat map[string]float64) map[string]any {
	verdict := "unknown"
	if !t.Enabled {
		verdict = "disabled"
	} else if v, ok := verdicts[t.TunnelKey]; ok {
		verdict = v
	}
	w, _ := h.WeightFor(topo.ID, t.LocalServerID, t.RemoteServerID, t.ID)
	usage, _, err := h.RangeUsageDetailed([]int64{t.ID}, now-now%86400, now)
	if err != nil {
		usage = store.Usage{}
	}
	var latency any
	if l, ok := lat[t.TunnelKey]; ok {
		latency = l
	}
	row := map[string]any{
		"id": t.ID, "tunnel_key": t.TunnelKey, "engine": t.Engine, "enabled": t.Enabled,
		"local_server":  h.uiServerRef(t.LocalServerID),
		"remote_server": h.uiServerRef(t.RemoteServerID),
		"state":         verdict,
		"weight":        w,
		"traffic_today": map[string]any{
			"wire_rx_bytes":    usage.WireRx,
			"wire_tx_bytes":    usage.WireTx,
			"payload_rx_bytes": usage.PayloadRx,
			"payload_tx_bytes": usage.PayloadTx,
		},
		"latency_ms": latency,
	}
	// Only connected tunnels carry weight — a dropped tunnel shows no
	// resolved share and the survivors re-normalize without it.
	if p := pcts[t.ID]; p > 0 {
		row["resolved_percent"] = p
	}
	return row
}

// s2OldAgentTopologies = uiAgentTopologies (uiapi_agents.go) before S2:
// inline agentCountry map + the pre-S2 row builder.
func s2OldAgentTopologies(h uiStoreHandle, s *store.Store, ag store.Agent, withTunnels bool) []map[string]any {
	out := []map[string]any{}
	if ag.ServerID == 0 {
		return out
	}
	members, err := h.TopologyMemberships(ag.ServerID)
	if err != nil || len(members) == 0 {
		return out
	}
	verdicts := uiVerdictsFn()
	lat := uiLatencyMap()
	now := timeNowUnix()
	// serverID → agent country (the servers table carries no country; the
	// agent enrolled against it does). Feeds the per-side diagram summary.
	agentCountry := map[int64]string{}
	if ags, aerr := h.ListAgents(); aerr == nil {
		for _, a2 := range ags {
			if a2.ServerID != 0 {
				agentCountry[a2.ServerID] = a2.Country
			}
		}
	}
	for _, topoID := range members {
		topo, err := h.GetTopology(topoID)
		if err != nil {
			continue
		}
		memberServers, _ := h.TopologyMembers(topoID)
		// Per-side summary: distinct servers on the agent's side vs the
		// peers' side, and the country representing each side (the client
		// renders the flag; iran sides are always ir).
		myServers, peerServers := 0, 0
		peerRole, peerCountry := "", ""
		for _, sid := range memberServers {
			sv, serr := h.GetServer(sid)
			if serr != nil {
				continue
			}
			if sv.Role == ag.Role {
				myServers++
				continue
			}
			peerServers++
			if peerRole == "" {
				peerRole = sv.Role
			}
			if peerCountry == "" && agentCountry[sid] != "" {
				peerCountry = agentCountry[sid]
			}
		}
		if peerRole == "" {
			if ag.Role == "iran" {
				peerRole = "foreign"
			} else {
				peerRole = "iran"
			}
		}
		if peerRole == "iran" && peerCountry == "" {
			peerCountry = "ir"
		}
		myCountry := ag.Country
		if ag.Role == "iran" {
			myCountry = "ir"
		}
		peers := []map[string]any{}
		tunnelCount := 0
		for _, sid := range memberServers {
			if sid == ag.ServerID {
				continue
			}
			if ref := h.uiServerRef(sid); ref != nil {
				peers = append(peers, ref.(map[string]any))
			}
		}
		entry := map[string]any{
			"id": topo.ID, "name": topo.Name, "my_role": ag.Role,
			"my_server": h.uiServerRef(ag.ServerID),
			"peers":     peers, "enabled": topo.Enabled,
			"members":   len(memberServers),
			"my_side":   map[string]any{"role": ag.Role, "servers": myServers, "country": myCountry},
			"peer_side": map[string]any{"role": peerRole, "servers": peerServers, "country": peerCountry},
		}
		if withTunnels {
			tunnels, _ := h.ListTunnels(topoID)
			rows := []map[string]any{}
			pcts := h.uiResolvedPercents(topoID, tunnels, verdicts)
			for _, t := range tunnels {
				if t.LocalServerID != ag.ServerID && t.RemoteServerID != ag.ServerID {
					continue
				}
				tunnelCount++
				rows = append(rows, s2OldTunnelRow(h, s, topo, t, verdicts, now, pcts, lat))
			}
			entry["tunnel_count"] = tunnelCount
			entry["tunnels"] = rows
		} else {
			tunnels, _ := h.ListTunnels(topoID)
			for _, t := range tunnels {
				if t.LocalServerID == ag.ServerID || t.RemoteServerID == ag.ServerID {
					tunnelCount++
				}
			}
			entry["tunnel_count"] = tunnelCount
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

// s2OldSideIdentity = topologySideIdentity (topology_identity.go) before
// S2 folded it into uirows.go as the shared §2.19 side-identity resolver.
func s2OldSideIdentity(t Topology, inventory []topologyTunnel, v *topologyIdentityView) (topologySideInfo, topologySideInfo) {
	iran := topologySideInfo{CC: "IR", Servers: []topologySideServer{}}
	foreign := topologySideInfo{CC: strings.ToUpper(strings.TrimSpace(t.Country)), Servers: []topologySideServer{}}
	if v == nil || len(v.servers) == 0 {
		return iran, foreign
	}
	byID := topologyTunnelByID(inventory)
	seen := map[int64]bool{}
	for _, id := range t.TunnelIDs {
		tt, ok := byID[id]
		if !ok {
			continue
		}
		local, remote := topologyTunnelEnds(tt)
		for _, ipStr := range append(append([]string{}, local...), remote...) {
			sid := v.serverForIP(ipStr)
			if sid == 0 || seen[sid] {
				continue
			}
			sv := v.serverByID[sid]
			side := &foreign
			if sv.Role == "iran" {
				side = &iran
			}
			seen[sid] = true
			entry := topologySideServer{Name: sv.Name, IP: ipStr, CC: side.CC}
			if dc, ok := v.dcs[sv.DatacenterID]; ok {
				entry.Datacenter, entry.DatacenterEN = dc.Name, dc.DatacenterEN
			}
			side.Servers = append(side.Servers, entry)
		}
	}
	for _, side := range []*topologySideInfo{&iran, &foreign} {
		if len(side.Servers) > 0 {
			side.ServerName = side.Servers[0].Name
			side.Datacenter = side.Servers[0].Datacenter
			side.DatacenterEN = side.Servers[0].DatacenterEN
		}
	}
	return iran, foreign
}

// ---- TESTS ----------------------------------------------------------------

// TestUIRowsTunnelRowByteCompare: the shared tunnel-row builder (the one
// uiTunnelRow) must stay byte-identical to the frozen pre-S2 body across
// the whole matrix: verdict present/absent, disabled, latency known/nil,
// traffic seeded/zero, resolved_percent present/absent.
func TestUIRowsTunnelRowByteCompare(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	iranServer, foreignServer, agIr, _ := uiSeedWorld(t, s)
	s2SeedMatrix(t, s, iranServer, foreignServer)
	s2VerdictSeam(t)
	s2LatencySeam(t, agIr)
	h := uiStoreHandle{s}

	topos, _ := s.ListTopologies()
	topo := topos[0]
	tunnels, _ := s.ListTunnels(topo.ID)
	if len(tunnels) != 4 {
		t.Fatalf("want the 4-tunnel matrix, got %d", len(tunnels))
	}
	// A known resolved share for tl-a only — the other rows must NOT grow a
	// resolved_percent key.
	tlA, _ := s.GetTunnelByKey("tl-a")
	pcts := map[int64]float64{tlA.ID: 62.5}
	lat := uiLatencyMap()
	now := timeNowUnix()
	verdicts := uiVerdictsFn()
	for _, tun := range tunnels {
		newRow := h.uiTunnelRow(s, topo, tun, verdicts, now, pcts, lat)
		oldRow := s2OldTunnelRow(h, s, topo, tun, verdicts, now, pcts, lat)
		s2CompareJSON(t, "tunnel row "+tun.TunnelKey, oldRow, newRow)
	}
}

// TestUIRowsSideIdentityByteCompare: the §2.19 side-identity resolver must
// stay byte-identical through the uirows.go fold — resolved via server
// addresses AND via linked-agent public IPs, with multi-server sides,
// datacenter names (bilingual), unmatched endpoints and the nil view.
func TestUIRowsSideIdentityByteCompare(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	dcIr, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "Tehran"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDatacenterEN(dcIr, "Tehran-Azadi"); err != nil {
		t.Fatal(err)
	}
	dcFr, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "Frankfurt"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDatacenterEN(dcFr, "Hetzner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Role: "iran", Name: "teh-1", DatacenterID: dcIr, Address: "203.0.113.10"}); err != nil {
		t.Fatal(err)
	}
	teh2, err := s.UpsertServer(store.Server{Role: "iran", Name: "teh-2", Address: "203.0.113.11"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Role: "foreign", Name: "fra-1", DatacenterID: dcFr, Address: "198.51.100.10"}); err != nil {
		t.Fatal(err)
	}
	// The linked agent's public IP makes 203.0.113.77 resolve to teh-2
	// (the agent-IP path of serverForIP).
	if err := s.UpsertAgent(store.Agent{ID: "agt_ip", ServerID: teh2, Name: "teh-2",
		Role: "iran", Status: "online", PublicIP: "203.0.113.77"}); err != nil {
		t.Fatal(err)
	}

	view := loadTopologyIdentityView()
	topo := Topology{ID: "topo_x", Name: "railway-eu", Role: "iran", Country: "de",
		TunnelIDs: []string{"main", "peer:1"}}
	inventory := []topologyTunnel{
		{ID: "main", Name: "Main", Engine: "matrix", RemoteIP: "203.0.113.77", localIP: "203.0.113.10"},
		{ID: "peer:1", Name: "Peer 1", Engine: "frp", RemoteIP: "198.51.100.10",
			RemoteIPs: []string{"198.51.100.20"}, localIP: "203.0.113.10"},
	}
	newIran, newForeign := topologySideIdentity(topo, inventory, view)
	oldIran, oldForeign := s2OldSideIdentity(topo, inventory, view)
	s2CompareJSON(t, "side identity iran", oldIran, newIran)
	s2CompareJSON(t, "side identity foreign", oldForeign, newForeign)

	// No store view at all: honest empty identity, byte-identical.
	nilIranNew, nilForeignNew := topologySideIdentity(topo, inventory, nil)
	nilIranOld, nilForeignOld := s2OldSideIdentity(topo, inventory, nil)
	s2CompareJSON(t, "side identity nil-view iran", nilIranOld, nilIranNew)
	s2CompareJSON(t, "side identity nil-view foreign", nilForeignOld, nilForeignNew)
}

// TestUIRowsAgentTopologiesPayloadByteCompare: the whole §2.3 per-agent
// topologies payload, old inline path vs the shared one, withTunnels BOTH
// ways. The frozen copy pins the agentCountry map, the my_side/peer_side
// summary and the row wiring together.
func TestUIRowsAgentTopologiesPayloadByteCompare(t *testing.T) {
	// The pre-refactor function reads the clock itself; a UTC-midnight flip
	// between the two builds would compare different day windows.
	if sec := time.Now().Unix() % 86400; sec < 2 || sec >= 86398 {
		t.Skip("UTC-midnight boundary — the two builds could land on different day windows")
	}
	withTempConfig(t)
	s := withMeshStore(t)
	_, _, agIr, _ := uiSeedWorld(t, s)
	irID, frID := s2ServerIDsByRole(t, s)
	s2SeedMatrix(t, s, irID, frID)
	s2VerdictSeam(t)
	s2LatencySeam(t, agIr)
	h := uiStoreHandle{s}
	ag, err := s.GetAgent(agIr)
	if err != nil {
		t.Fatal(err)
	}
	for _, withTunnels := range []bool{true, false} {
		newRows := h.uiAgentTopologies(s, ag, withTunnels)
		oldRows := s2OldAgentTopologies(h, s, ag, withTunnels)
		s2CompareJSON(t, "agent topologies payload (withTunnels)", oldRows, newRows)
	}
}

// s2ServerIDsByRole returns (iran, foreign) server ids of the seeded world.
func s2ServerIDsByRole(t *testing.T, s *store.Store) (int64, int64) {
	t.Helper()
	servers, err := s.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	iranID, foreignID := int64(0), int64(0)
	for _, sv := range servers {
		if sv.Role == "iran" && iranID == 0 {
			iranID = sv.ID
		}
		if sv.Role == "foreign" && foreignID == 0 {
			foreignID = sv.ID
		}
	}
	if iranID == 0 || foreignID == 0 {
		t.Fatalf("seeded world lost its iran/foreign servers: %+v", servers)
	}
	return iranID, foreignID
}

// TestUIRowsHelpersByteCompare: the two extracted helpers against their
// frozen inline bodies — uiTunnelVerdict (the exact "unknown/disabled/
// verdict-map" block that served uiTunnelRow, the agent detail rows and
// the tunnel detail) and uiAgentCountries (the exact serverID→country
// loop that served the agent detail flags and the topologies side
// summary: unlinked agents excluded, an empty country still assigned).
func TestUIRowsHelpersByteCompare(t *testing.T) {
	// The verdict block, frozen verbatim (enabled + verdict map in, string
	// out — identical text at all three pre-S2 serving spots).
	s2OldVerdict := func(enabled bool, key string, verdicts map[string]string) string {
		verdict := "unknown"
		if !enabled {
			verdict = "disabled"
		} else if v, ok := verdicts[key]; ok {
			verdict = v
		}
		return verdict
	}
	for _, c := range []struct {
		enabled  bool
		key      string
		verdicts map[string]string
	}{
		{false, "k", map[string]string{"k": "healthy"}}, // disabled beats any verdict
		{true, "k", map[string]string{"k": "healthy"}},  // present verdict wins
		{true, "k", map[string]string{"k": "degraded"}}, // present verdict wins
		{true, "k", map[string]string{}},                // no map entry → unknown
		{true, "k2", map[string]string{"k": "healthy"}}, // other tunnel's verdict ignored
		{true, "k", nil}, // nil map → unknown
	} {
		tun := store.Tunnel{Enabled: c.enabled, TunnelKey: c.key}
		if got := uiTunnelVerdict(tun, c.verdicts); got != s2OldVerdict(c.enabled, c.key, c.verdicts) {
			t.Fatalf("verdict drift for enabled=%v key=%q: %q != %q",
				c.enabled, c.key, got, s2OldVerdict(c.enabled, c.key, c.verdicts))
		}
	}

	withTempConfig(t)
	s := withMeshStore(t)
	_, _, _, _ = uiSeedWorld(t, s)
	irID, frID := s2ServerIDsByRole(t, s)
	// An agent with an EMPTY country on a linked server: the map still
	// assigns key→"" (the old inline block did not filter empties).
	if err := s.UpsertAgent(store.Agent{ID: "agt_nocountry", ServerID: irID, Name: "nc",
		Role: "iran", Status: "online"}); err != nil {
		t.Fatal(err)
	}
	// An UNLINKED agent (ServerID 0 → NULL server_id): must contribute nothing.
	if err := s.UpsertAgent(store.Agent{ID: "agt_free", Name: "free",
		Role: "foreign", Status: "online"}); err != nil {
		t.Fatal(err)
	}
	h := uiStoreHandle{s}
	got := h.uiAgentCountries()
	want := map[int64]string{}
	ags, err := s.ListAgents()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range ags {
		if a.ServerID != 0 {
			want[a.ServerID] = a.Country
		}
	}
	if len(want) != 2 {
		t.Fatalf("fixture drifted: want exactly the two linked servers mapped, got %+v", want)
	}
	s2CompareJSON(t, "agent countries map", want, got)
	_ = frID
}

// TestUIRowsWSUpgraderShared: the ONE shared websocket.Upgrader and its
// per-use derivations keep their exact configs. The per-connection read
// limits are NOT upgrader fields — they stay at the accept sites
// (agenthub.go agentWSReadLimit, terminal.go 64 KiB, wss_carrier.go
// 65535) and are untouched by S2.
func TestUIRowsWSUpgraderShared(t *testing.T) {
	if wsUpgrader.ReadBufferSize != 4096 || wsUpgrader.WriteBufferSize != 4096 {
		t.Fatalf("shared upgrader buffers drifted: %d/%d",
			wsUpgrader.ReadBufferSize, wsUpgrader.WriteBufferSize)
	}
	accept := func(u websocket.Upgrader, origin, host string) bool {
		req := httptest.NewRequest(http.MethodGet, "/ws", nil)
		req.Host = host
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		return u.CheckOrigin(req)
	}
	// Agent channel: the shared non-browser policy verbatim (its pre-S2
	// body = wsOriginNonBrowser; empty Origin passes, a matching host
	// passes, everything else — including a sandboxed "null" origin —
	// fails).
	for _, c := range []struct {
		origin, host string
		want         bool
	}{
		{"", "panel.example.com", true},
		{"https://panel.example.com", "panel.example.com", true},
		{"https://panel.example.com:8443", "panel.example.com:8443", true},
		{"https://evil.example.com", "panel.example.com", false},
		{"null", "panel.example.com", false},
	} {
		if got := accept(agentHubUpgrader, c.origin, c.host); got != c.want {
			t.Fatalf("agent origin policy %q@%q = %v, want %v", c.origin, c.host, got, c.want)
		}
	}
	// Terminal: bigger write buffer + its proxy-aware policy (the full
	// origin matrix incl. X-Forwarded-Host stays covered by
	// TestWebSocketCheckOrigin in domain_tls_test.go).
	if termUpgrader.ReadBufferSize != 4096 || termUpgrader.WriteBufferSize != 32768 {
		t.Fatalf("terminal upgrader buffers drifted: %d/%d",
			termUpgrader.ReadBufferSize, termUpgrader.WriteBufferSize)
	}
	if termUpgrader.CheckOrigin == nil {
		t.Fatal("terminal origin policy lost in the derivation")
	}
	// Carrier: explicit 4096/4096 == the gorilla defaults its previous
	// zero-value resolved to (websocket@v1.5.3 conn.go defaultRead/Write
	// BufferSize = 4096); ANY browser origin rejected, even same-host.
	u := wssUpgrader()
	if u.ReadBufferSize != 4096 || u.WriteBufferSize != 4096 {
		t.Fatalf("carrier upgrader buffers drifted: %d/%d", u.ReadBufferSize, u.WriteBufferSize)
	}
	if !accept(u, "", "carrier.example.com") {
		t.Fatal("carrier must accept non-browser clients")
	}
	if accept(u, "https://carrier.example.com", "carrier.example.com") {
		t.Fatal("carrier must reject ANY browser origin, even a same-host one")
	}
}
