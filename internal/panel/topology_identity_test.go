package panel

// topology_identity_test.go — phase 31 (UI-API-REQUESTS §10, contract
// §2.19): per-side identity for panel-created topologies, per-tunnel
// weight/today_traffic on the legacy payload, and the one-DC-per-side
// invariant on the manual attach path (POST/PATCH tunnel_ids).

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// identityFixture seeds two Iran servers in ONE DC, one foreign server in
// another, and a linked agent whose public IP aliases the foreign server.
func identityFixture(t *testing.T) *store.Store {
	t.Helper()
	withTempConfig(t)
	s := withMeshStore(t)
	dcIr, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "تهران-آزادی", DatacenterEN: "Tehran-Azadi"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Role: "iran", Name: "Tehran-1", DatacenterID: dcIr, Address: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Role: "iran", Name: "Tehran-2", DatacenterID: dcIr, Address: "10.0.0.9"}); err != nil {
		t.Fatal(err)
	}
	dcFx, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "هتزنر", DatacenterEN: "Hetzner"})
	if err != nil {
		t.Fatal(err)
	}
	fx, err := s.UpsertServer(store.Server{Role: "foreign", Name: "Frankfurt-1", DatacenterID: dcFx, Address: "198.51.100.7"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgent(store.Agent{ID: "agt_fx", ServerID: fx, Name: "Frankfurt-1",
		Role: "foreign", Status: "online", PublicIP: "198.51.100.8", LastSeen: 1}); err != nil {
		t.Fatal(err)
	}
	return s
}

// identityInventory points the inventory seam at two tunnels: main (local
// 10.0.0.1 → remote 198.51.100.7) and peer:3 (local 10.0.0.9 → the same
// remote via its two addresses).
func identityInventory(t *testing.T) {
	t.Helper()
	old := topologyInventory
	topologyInventory = func() []topologyTunnel {
		return []topologyTunnel{
			{ID: "main", Name: "Legacy FRP", Engine: "frp", RemoteIP: "198.51.100.7",
				State: "healthy", LatencyMs: 12, Ports: []int{443}, role: "iran", localIP: "10.0.0.1"},
			{ID: "peer:3", Name: "Other server", Engine: "backhaul", RemoteIP: "198.51.100.7", RemoteIPs: []string{"198.51.100.8"},
				State: "down", LatencyMs: -1, Ports: []int{8443}, role: "iran", localIP: "10.0.0.9"},
		}
	}
	t.Cleanup(func() { topologyInventory = old })
}

func TestTopologySideIdentityResolvesPerSide(t *testing.T) {
	identityFixture(t)
	identityInventory(t)
	inventory := topologyInventory()
	topo := Topology{ID: "topo_a", Name: "railway-eu", Role: "iran", TunnelIDs: []string{"main", "peer:3"}, Country: "de", CreatedAt: 1}
	iran, foreign := topologySideIdentity(topo, inventory, loadTopologyIdentityView())
	if iran.ServerName != "Tehran-1" || foreign.ServerName != "Frankfurt-1" {
		t.Fatalf("summary names: iran=%q foreign=%q", iran.ServerName, foreign.ServerName)
	}
	if iran.Datacenter != "تهران-آزادی" || iran.DatacenterEN != "Tehran-Azadi" {
		t.Fatalf("iran dc: %+v", iran)
	}
	if foreign.DatacenterEN != "Hetzner" {
		t.Fatalf("foreign dc: %+v", foreign)
	}
	if iran.CC != "IR" || foreign.CC != "DE" {
		t.Fatalf("cc: iran=%q foreign=%q", iran.CC, foreign.CC)
	}
	// Every matched server of the side lands in servers[] — the summary is
	// the first one (inventory order), not a merge.
	if len(iran.Servers) != 2 {
		t.Fatalf("iran servers: %+v", iran.Servers)
	}
	if iran.Servers[0].Name != "Tehran-1" || iran.Servers[1].Name != "Tehran-2" || iran.Servers[1].IP != "10.0.0.9" {
		t.Fatalf("iran servers order/ip: %+v", iran.Servers)
	}
	// The agent-reported alias resolves to the SAME foreign server (its
	// linked agent's public IP) — one entry, deduplicated by server id.
	if len(foreign.Servers) != 1 || foreign.Servers[0].IP != "198.51.100.7" || foreign.Servers[0].DatacenterEN != "Hetzner" {
		t.Fatalf("foreign servers: %+v", foreign.Servers)
	}
}

func TestTopologySideIdentityHonestAbsence(t *testing.T) {
	// No store rows at all: empty strings + empty servers, cc still carried.
	withTempConfig(t)
	withMeshStore(t)
	topo := Topology{ID: "topo_b", Name: "bare", Role: "iran", TunnelIDs: []string{"main"}, Country: "de", CreatedAt: 1}
	iran, foreign := topologySideIdentity(topo, []topologyTunnel{{ID: "main", RemoteIP: "203.0.113.9", localIP: "10.9.9.9"}}, loadTopologyIdentityView())
	if iran.ServerName != "" || iran.Datacenter != "" || len(iran.Servers) != 0 {
		t.Fatalf("iran should resolve nothing: %+v", iran)
	}
	if foreign.ServerName != "" || len(foreign.Servers) != 0 {
		t.Fatalf("foreign should resolve nothing: %+v", foreign)
	}
	if iran.CC != "IR" || foreign.CC != "DE" {
		t.Fatalf("cc fallback: %q/%q", iran.CC, foreign.CC)
	}
}

func TestTopologyTunnelWeights(t *testing.T) {
	withTempConfig(t)
	cfg := ClusterConfig{Version: 2, Pools: []LBPool{
		{Port: 443, Proto: "tcp", Mode: "balance", Members: []LBMember{
			{Link: 0, Weight: 30}, {Link: 3, Weight: 70},
		}},
		{Port: 8443, Proto: "tcp", Mode: "failover", Members: []LBMember{
			{Link: 3, Weight: 10}, {Link: 4, Weight: 5},
		}},
		{Port: 9443, Proto: "tcp", Mode: "balance", Disabled: true, Members: []LBMember{
			{Link: 3, Weight: 999},
		}},
	}}
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clusterConfigFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	w := topologyTunnelWeights()
	if w["main"] != 30 || w["peer:3"] != 70 || w["peer:4"] != 5 {
		t.Fatalf("weights: main=%d peer:3=%d peer:4=%d (want 30/70/5)", w["main"], w["peer:3"], w["peer:4"])
	}
	// The disabled pool's 999 must not win; engines without an LB concept
	// and links in no pool stay 0 (unset).
	if w["matrix"] != 0 || w["xray:4"] != 0 || w["peer:9"] != 0 {
		t.Fatalf("unset weights must be 0: %+v", w)
	}
}

func TestTopologyTodayTrafficReadsLedger(t *testing.T) {
	withTempConfig(t)
	now := time.Now()
	seed := func(key string, rx, tx uint64, seen int64) {
		trafficUsage.mu.Lock()
		trafficUsage.links[key] = &usageLink{
			Name: key, Points: []usagePoint{{T: now.Unix(), Rx: rx, Tx: tx}},
			Seen: seen, Measured: true,
		}
		trafficUsage.mu.Unlock()
	}
	seed("p3", 1000, 2000, now.Unix())
	seed("xray:4", 5, 6, now.Unix()-3600) // stale beyond the freshness window
	got := topologyTodayTraffic([]string{"peer:3", "xray:4", "main"}, now)
	if u, ok := got["peer:3"]; !ok || u.Rx != 1000 || u.Tx != 2000 {
		t.Fatalf("peer:3 today: %+v", got["peer:3"])
	}
	// Unknown and stale links are absent — the payload serves null, not 0.
	if _, ok := got["xray:4"]; ok {
		t.Fatalf("stale link must be unknown: %+v", got)
	}
	if _, ok := got["main"]; ok {
		t.Fatalf("unmeasured link must be unknown: %+v", got)
	}
}

func TestTopologyPayloadEnrichmentAdditive(t *testing.T) {
	identityFixture(t)
	identityInventory(t)
	now := time.Now().Unix()
	trafficUsage.mu.Lock()
	trafficUsage.links["p3"] = &usageLink{Name: "peer:3", Points: []usagePoint{{T: now, Rx: 7, Tx: 9}}, Seen: now, Measured: true}
	trafficUsage.mu.Unlock()
	cfg := ClusterConfig{Version: 2, Pools: []LBPool{{Port: 8443, Proto: "tcp", Mode: "balance",
		Members: []LBMember{{Link: 3, Weight: 40}}}}}
	b, _ := json.Marshal(cfg)
	if err := os.WriteFile(clusterConfigFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	topo := Topology{ID: "topo_a", Name: "railway-eu", Role: "iran", TunnelIDs: []string{"main", "peer:3"}, Country: "de", CreatedAt: 1}
	if err := saveTopologies([]Topology{topo}); err != nil {
		t.Fatal(err)
	}
	w := topologyRequest(http.MethodGet, "", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET: status=%d body=%s", w.Code, w.Body.String())
	}
	var payload struct {
		Topologies []map[string]any `json:"topologies"`
		Tunnels    []map[string]any `json:"tunnels"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Topologies) != 1 || len(payload.Tunnels) != 2 {
		t.Fatalf("payload: %+v", payload)
	}
	row := payload.Topologies[0]
	for _, field := range []string{"id", "name", "role", "local_ip", "remote_ip", "country", "tunnel_ids", "created_at"} {
		if _, ok := row[field]; !ok {
			t.Fatalf("base field %s missing from topology row (additive rule)", field)
		}
	}
	iran := row["iran"].(map[string]any)
	foreign := row["foreign"].(map[string]any)
	if iran["server_name"] != "Tehran-1" || foreign["server_name"] != "Frankfurt-1" {
		t.Fatalf("identity in payload: iran=%v foreign=%v", iran, foreign)
	}
	if iran["datacenter_en"] != "Tehran-Azadi" || foreign["datacenter_en"] != "Hetzner" {
		t.Fatalf("bilingual identity: %v/%v", iran["datacenter_en"], foreign["datacenter_en"])
	}
	var withWeight map[string]any
	for _, tn := range payload.Tunnels {
		if tn["id"] == "peer:3" {
			withWeight = tn
		}
	}
	if withWeight == nil {
		t.Fatal("peer:3 missing from tunnels")
	}
	if withWeight["weight"] != float64(40) {
		t.Fatalf("weight in payload: %v", withWeight["weight"])
	}
	tt := withWeight["today_traffic"].(map[string]any)
	if tt["wire_rx_bytes"] != float64(7) || tt["wire_tx_bytes"] != float64(9) {
		t.Fatalf("today_traffic: %v", withWeight["today_traffic"])
	}
	// The tunnel with no measurement serves today_traffic:null — honest unknown.
	for _, tn := range payload.Tunnels {
		if tn["id"] == "main" && tn["today_traffic"] != nil {
			t.Fatalf("unmeasured tunnel must serve null, got %v", tn["today_traffic"])
		}
	}
}

// dcinvarFixture seeds TWO Iran DCs (tehran/shiraz) and TWO foreign DCs
// (hetzner/contabo). The remote server pair is one box with two addresses
// (Frankfurt-1=.7, Vienna-1=.8): tunnels can share the remote through both
// IPs, so validateTopologyBindings' same-remote rule passes and the DC
// invariant is what stops the mix.
func dcinvarFixture(t *testing.T) *store.Store {
	t.Helper()
	withTempConfig(t)
	s := withMeshStore(t)
	dcIr1, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "tehran"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Role: "iran", Name: "Tehran-1", DatacenterID: dcIr1, Address: "10.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	dcIr2, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "shiraz"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Role: "iran", Name: "Shiraz-1", DatacenterID: dcIr2, Address: "10.0.0.9"}); err != nil {
		t.Fatal(err)
	}
	dcFx1, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "hetzner"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Role: "foreign", Name: "Frankfurt-1", DatacenterID: dcFx1, Address: "198.51.100.7"}); err != nil {
		t.Fatal(err)
	}
	dcFx2, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "contabo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Role: "foreign", Name: "Vienna-1", DatacenterID: dcFx2, Address: "198.51.100.8"}); err != nil {
		t.Fatal(err)
	}
	return s
}

// dcinvarInventory: two tunnels sharing the remote via its two addresses
// (so the binding rule passes), with local ends in two different Iran DCs.
func dcinvarInventory(t *testing.T) {
	t.Helper()
	old := topologyInventory
	topologyInventory = func() []topologyTunnel {
		return []topologyTunnel{
			{ID: "peer:3", Name: "To Frankfurt", Engine: "frp", RemoteIP: "198.51.100.7",
				State: "healthy", Ports: []int{443}, role: "iran", localIP: "10.0.0.1"},
			{ID: "peer:4", Name: "To Vienna", Engine: "backhaul", RemoteIP: "198.51.100.7", RemoteIPs: []string{"198.51.100.8"},
				State: "down", Ports: []int{8443}, role: "iran", localIP: "10.0.0.9"},
		}
	}
	t.Cleanup(func() { topologyInventory = old })
}

func TestTopologyDCViolationsBothSides(t *testing.T) {
	dcinvarFixture(t)
	dcinvarInventory(t)
	inventory := topologyInventory()
	view := loadTopologyIdentityView()
	topo := Topology{ID: "topo_x", Name: "mix", Role: "iran", TunnelIDs: []string{"peer:3", "peer:4"}, CreatedAt: 1}
	issues := topologyDCViolations(topo, inventory, view)
	if len(issues) != 2 {
		t.Fatalf("want an issue per side, got %+v", issues)
	}
	// Deterministic order: the iran side first.
	if issues[0].Side != "iran" || issues[1].Side != "foreign" {
		t.Fatalf("sides: %+v", issues)
	}
	if len(issues[0].Datacenters) != 2 || len(issues[1].Datacenters) != 2 {
		t.Fatalf("datacenters: %+v", issues)
	}
	// The iran mix spans both local ends; the foreign mix comes from the two
	// remote addresses shared across both tunnels (both carry .7, which
	// resolves to hetzner, and peer:4 adds .8 → contabo).
	if len(issues[0].Tunnels) != 2 || issues[0].Tunnels[0] != "peer:3" || issues[0].Tunnels[1] != "peer:4" {
		t.Fatalf("iran offending tunnels: %+v", issues[0])
	}
	if len(issues[1].Tunnels) != 2 || issues[1].Tunnels[0] != "peer:3" || issues[1].Tunnels[1] != "peer:4" {
		t.Fatalf("foreign offending tunnels: %+v", issues[1])
	}
	// A single-DC tunnel set is fine.
	ok := Topology{ID: "topo_ok", Name: "clean", Role: "iran", TunnelIDs: []string{"peer:3"}, CreatedAt: 2}
	if violations := topologyDCViolations(ok, inventory, view); len(violations) != 0 {
		t.Fatalf("single DC per side must pass: %+v", violations)
	}
	// Unresolvable endpoints cannot be checked — they never invent violations.
	ghost := Topology{ID: "topo_g", Name: "ghost", Role: "iran", TunnelIDs: []string{"xray:9"}, CreatedAt: 3}
	if violations := topologyDCViolations(ghost, inventory, view); len(violations) != 0 {
		t.Fatalf("unresolvable endpoints must pass: %+v", violations)
	}
}

func TestTopologyManualAttachRejectsDCMix(t *testing.T) {
	dcinvarFixture(t)
	dcinvarInventory(t)
	body := `{"name":"mix","role":"iran","tunnel_ids":["peer:3","peer:4"]}`
	w := topologyRequest(http.MethodPost, "", body)
	if w.Code != http.StatusConflict {
		t.Fatalf("POST mixed DCs: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		ErrorCode string `json:"error_code"`
		UI        struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"ui"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != "E-TOPOLOGY-DC-CONFLICT" || resp.UI.Code != "E-TOPOLOGY-DC-CONFLICT" {
		t.Fatalf("structured code missing: %s", w.Body.String())
	}
	side, _ := resp.UI.Details["side"].(string)
	tunnels, _ := resp.UI.Details["tunnels"].([]any)
	if side != "iran" { // iran is reported first (deterministic order)
		t.Fatalf("details.side: %v", resp.UI.Details)
	}
	if len(tunnels) != 2 {
		t.Fatalf("details.tunnels must name the offending tunnels: %v", resp.UI.Details["tunnels"])
	}
	// Nothing was saved.
	all, err := loadTopologies()
	if err != nil || len(all) != 0 {
		t.Fatalf("rejected topology must not persist: %v %v", all, err)
	}
}

// topologyCreateID POSTs a topology and returns the generated id.
func topologyCreateID(t *testing.T, body string) string {
	t.Helper()
	w := topologyRequest(http.MethodPost, "", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST %s: status=%d body=%s", body, w.Code, w.Body.String())
	}
	var resp struct {
		Topology struct {
			ID string `json:"id"`
		} `json:"topology"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Topology.ID
}

func TestTopologyManualAttachAcceptsSingleDC(t *testing.T) {
	dcinvarFixture(t)
	dcinvarInventory(t)
	// One Iran DC (tehran) and one foreign DC (hetzner): valid per the rule.
	id := topologyCreateID(t, `{"name":"clean","role":"iran","tunnel_ids":["peer:3"]}`)
	// Re-PATCHing the same single-DC set must pass as well.
	w := topologyRequest(http.MethodPatch, id, `{"tunnel_ids":["peer:3"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH single-DC set: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestTopologyPatchDCMixRejected(t *testing.T) {
	dcinvarFixture(t)
	dcinvarInventory(t)
	// Start with one Iran DC and one foreign DC, then attach the tunnel that
	// brings in a second DC on BOTH sides → the invariant must fire on PATCH.
	id := topologyCreateID(t, `{"name":"grow","role":"iran","tunnel_ids":["peer:3"]}`)
	w := topologyRequest(http.MethodPatch, id, `{"tunnel_ids":["peer:3","peer:4"]}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("PATCH mixed DCs: status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "E-TOPOLOGY-DC-CONFLICT") {
		t.Fatalf("structured envelope missing: %s", w.Body.String())
	}
}
