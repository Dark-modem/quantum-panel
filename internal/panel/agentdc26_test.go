package panel

// agentdc26_test.go — BACKEND-26 (UI-API-REQUESTS §9): the add-agent wizard
// collects a datacenter on token mint; the backend applies it to the
// enrolled agent's server record so the agents rows carry
// a.server.datacenter (+ datacenter_en everywhere dc names are served),
// exactly like seeded data. Legacy agents (no datacenter on the token)
// stay unlinked, pre-26 behavior.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func mint26(t *testing.T, srv *httptest.Server, body string) (int, map[string]any) {
	t.Helper()
	client := meshSession(t)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/agents/tokens", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func enroll26(t *testing.T, srv *httptest.Server, token, name string) int {
	t.Helper()
	body := `{"name":"` + name + `","hostname":"h26","os":"linux","arch":"amd64","version":"v26","caps":[]}`
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/agents/enroll", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(agentEnrollHeader, token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func uiGet26(t *testing.T, srv *httptest.Server, path string) map[string]any {
	t.Helper()
	client := meshSession(t)
	resp, err := client.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestTokenDatacenterMintEnrollAgentsRow — the §9 round trip: mint (with a
// datacenter) → real enroll → GET /api/ui/agents row carries
// a.server.datacenter (the backend find-or-created the datacenters row, the
// server row, and linked the agent) and the detail object carries
// agent.datacenter — exactly like seeded data.
func TestTokenDatacenterMintEnrollAgentsRow(t *testing.T) {
	srv := uiServer(t)
	st := meshStoreMust(t)

	stCode, out := mint26(t, srv, `{"role":"iran","label":"dc26","expires_days":7,"datacenter":"تهران-آزادی"}`)
	if stCode != 200 {
		t.Fatalf("mint: %d %v", stCode, out)
	}
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatalf("mint returned no token: %v", out)
	}
	if code := enroll26(t, srv, token, "tehran-1"); code != 200 {
		t.Fatalf("enroll: %d", code)
	}

	rows := uiGet26(t, srv, "/api/ui/agents")
	ags, _ := rows["agents"].([]any)
	var row map[string]any
	for _, raw := range ags {
		if m, ok := raw.(map[string]any); ok && m["name"] == "tehran-1" {
			row = m
		}
	}
	if row == nil {
		t.Fatalf("enrolled agent missing from the rows: %v", rows)
	}
	if row["linked"] != true {
		t.Fatalf("enrolled agent must be linked to its auto-provisioned server: %v", row)
	}
	srvRef, _ := row["server"].(map[string]any)
	if srvRef == nil {
		t.Fatalf("row has no server ref: %v", row)
	}
	if srvRef["datacenter"] != "تهران-آزادی" {
		t.Fatalf("a.server.datacenter = %v, want the token's datacenter", srvRef["datacenter"])
	}

	detail := uiGet26(t, srv, "/api/ui/agents/"+row["id"].(string))
	ag, _ := detail["agent"].(map[string]any)
	if ag == nil || ag["datacenter"] != "تهران-آزادی" {
		t.Fatalf("detail agent.datacenter missing/wrong: %v", detail["agent"])
	}

	// the datacenters row was find-or-created with the token role's kind
	dcs, err := st.ListDatacenters()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, dc := range dcs {
		if dc.Name == "تهران-آزادی" {
			found = true
			if dc.Kind != "iran" {
				t.Fatalf("auto-created datacenter kind = %q, want iran", dc.Kind)
			}
		}
	}
	if !found {
		t.Fatalf("the token's datacenter was not created: %+v", dcs)
	}
}

// TestTokenDatacenterValidation — trim, empty = absent, >100 characters is
// refused (the documented bound).
func TestTokenDatacenterValidation(t *testing.T) {
	srv := uiServer(t)
	st := meshStoreMust(t)

	// 101 characters: refused
	code, _ := mint26(t, srv, `{"role":"iran","datacenter":"`+strings.Repeat("x", 101)+`"}`)
	if code != 400 {
		t.Fatalf("oversized datacenter must be refused, got %d", code)
	}
	// exactly 100: accepted
	code, out := mint26(t, srv, `{"role":"iran","datacenter":"`+strings.Repeat("x", 100)+`"}`)
	if code != 200 {
		t.Fatalf("100-character datacenter must mint, got %d %v", code, out)
	}
	// missing/empty: mints fine, persists nothing
	code, out = mint26(t, srv, `{"role":"foreign","datacenter":"   "}`)
	if code != 200 {
		t.Fatalf("empty datacenter must mint, got %d", code)
	}
	if code := enroll26(t, srv, out["token"].(string), "no-dc-agent"); code != 200 {
		t.Fatalf("enroll: %d", code)
	}
	rows := uiGet26(t, srv, "/api/ui/agents")
	ags, _ := rows["agents"].([]any)
	for _, raw := range ags {
		if m, ok := raw.(map[string]any); ok && m["name"] == "no-dc-agent" {
			if m["linked"] != false || m["server"] != nil {
				t.Fatalf("a datacenter-less token must leave the agent unlinked (pre-26 behavior): %v", m)
			}
		}
	}
	if dcs, _ := st.ListDatacenters(); len(dcs) != 0 {
		t.Fatalf("no datacenter row may be created for a dc-less token: %+v", dcs)
	}
}

// TestDatacenterEnServedAtAllSpots — the optional English name rides every
// listed serving spot: agents rows (a.server.datacenter_en), the detail
// object (agent.datacenter_en), topology members/remotes
// (tn.remote.datacenter_en), and the billing per-server rows
// (DatacenterEN). Legacy rows without dc names are unaffected.
func TestDatacenterEnServedAtAllSpots(t *testing.T) {
	srv := uiServer(t)
	st := meshStoreMust(t)

	// Seed: a bilingual iran dc + server + agent row (linked via the store's
	// link call, as the operator would), and a foreign dc for the remote.
	dcIr, err := st.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "تهران-آزادی", DatacenterEN: "Tehran-Azadi"})
	if err != nil {
		t.Fatal(err)
	}
	dcFr, err := st.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "هتزنر", DatacenterEN: "Hetzner"})
	if err != nil {
		t.Fatal(err)
	}
	svIr, err := st.UpsertServer(store.Server{Name: "teh-26", Role: "iran", DatacenterID: dcIr})
	if err != nil {
		t.Fatal(err)
	}
	svFr, err := st.UpsertServer(store.Server{Name: "fra-26", Role: "foreign", DatacenterID: dcFr})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAgent(store.Agent{ID: "agt_dc26", Name: "teh-26", Role: "iran",
		Status: "online", Version: "v26", Arch: "amd64", OS: "linux", LastSeen: 1, ServerID: svIr}); err != nil {
		t.Fatal(err)
	}
	if err := st.LinkAgentServer("agt_dc26", svIr); err != nil {
		t.Fatal(err)
	}
	topo, err := st.UpsertTopology(store.Topology{Name: "topo-26", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddTopologyMember(topo, svIr); err != nil {
		t.Fatal(err)
	}
	if err := st.AddTopologyMember(topo, svFr); err != nil {
		t.Fatal(err)
	}
	tun26, err := st.UpsertTunnel(store.Tunnel{TunnelKey: "tun-26", TopologyID: topo,
		LocalServerID: svIr, RemoteServerID: svFr, Engine: "chisel", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// The billing per-server rows build from traffic streams: a server with
	// zero traffic has no row at all.
	if err := st.AddTrafficSample(store.TrafficSample{TunnelID: tun26, AgentID: "agt_dc26",
		At: time.Now().Unix(), WireRx: 1000, WireTx: 500, CountersKnown: true}); err != nil {
		t.Fatal(err)
	}

	// agents rows
	rows := uiGet26(t, srv, "/api/ui/agents")
	ags, _ := rows["agents"].([]any)
	var row map[string]any
	for _, raw := range ags {
		if m, ok := raw.(map[string]any); ok && m["name"] == "teh-26" {
			row = m
		}
	}
	if row == nil {
		t.Fatalf("seeded agent missing from the rows")
	}
	srvRef, _ := row["server"].(map[string]any)
	if srvRef == nil || srvRef["datacenter"] != "تهران-آزادی" || srvRef["datacenter_en"] != "Tehran-Azadi" {
		t.Fatalf("a.server.datacenter(+_en) wrong: %v", srvRef)
	}

	// detail object
	detail := uiGet26(t, srv, "/api/ui/agents/agt_dc26")
	ag, _ := detail["agent"].(map[string]any)
	if ag == nil || ag["datacenter"] != "تهران-آزادی" || ag["datacenter_en"] != "Tehran-Azadi" {
		t.Fatalf("agent.datacenter(+_en) wrong on the detail: %v", detail["agent"])
	}

	// topology members + tunnel remotes
	topoView := uiGet26(t, srv, "/api/ui/topologies/"+itoa26(topo))
	tuns, _ := topoView["tunnels"].([]any)
	var tunRemote map[string]any
	for _, raw := range tuns {
		if m, ok := raw.(map[string]any); ok && m["tunnel_key"] == "tun-26" {
			tunRemote, _ = m["remote_server"].(map[string]any)
		}
	}
	if tunRemote == nil || tunRemote["datacenter_en"] != "Hetzner" {
		t.Fatalf("tn.remote(.datacenter_en) wrong: %v", tunRemote)
	}
	members, _ := topoView["members"].(map[string]any)
	iranRows, _ := members["iran"].([]any)
	var memberDc any
	for _, raw := range iranRows {
		if m, ok := raw.(map[string]any); ok && m["name"] == "teh-26" {
			memberDc = m["datacenter_en"]
		}
	}
	if memberDc != "Tehran-Azadi" {
		t.Fatalf("topology member datacenter_en missing: %v", memberDc)
	}

	// billing per-server rows carry the English name
	dcs, servers, _, err := st.BillingReport(time.Now().Unix()-3600, time.Now().Unix()+3600)
	if err != nil {
		t.Fatal(err)
	}
	_ = dcs
	var bRow *store.BillingServerRow
	for i := range servers {
		if servers[i].Name == "teh-26" {
			bRow = &servers[i]
		}
	}
	if bRow == nil || bRow.DatacenterEN != "Tehran-Azadi" {
		t.Fatalf("billing per-server row lost datacenter_en: %+v", bRow)
	}
}

// TestDatacenterEnSurvivesBackupRestore — migration 8's column rides the
// backup bundle (the whole-db snapshot) and survives a wipe + restore.
func TestDatacenterEnSurvivesBackupRestore(t *testing.T) {
	_, passFile := backupTestEnv(t)
	dbPath := meshDBPath()
	disk, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	dcID, err := disk.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "تهران-آزادی", DatacenterEN: "Tehran-Azadi"})
	if err != nil {
		t.Fatal(err)
	}
	if err := disk.Close(); err != nil {
		t.Fatal(err)
	}

	bak := t.TempDir() + "/p26.qpbak"
	if rc := BackupCmd([]string{"--out", bak, "--passphrase-file", passFile}); rc != 0 {
		t.Fatal("backup failed")
	}
	// wipe the datacenter row + restore it from the bundle
	disk2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := disk2.DeleteDatacenter(dcID); err != nil {
		t.Fatal(err)
	}
	disk2.Close()
	if rc := RestoreCmd([]string{"--in", bak, "--passphrase-file", passFile, "--force"}); rc != 0 {
		t.Fatal("restore failed")
	}
	disk3, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer disk3.Close()
	dc, err := disk3.DatacenterByName("تهران-آزادی")
	if err != nil {
		t.Fatalf("restored db lost the datacenter: %v", err)
	}
	if dc.ID != dcID || dc.DatacenterEN != "Tehran-Azadi" {
		t.Fatalf("restored datacenter wrong: id=%d en=%q", dc.ID, dc.DatacenterEN)
	}
}

// TestUpsertDatacenterENPreserved (store level): an empty incoming
// datacenter_en must NOT clobber a stored English name (the enroll path
// find-or-creates datacenters by name and would otherwise wipe them).
func TestUpsertDatacenterENPreserved(t *testing.T) {
	withTempConfig(t)
	s, err := store.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "تهران-آزادی", DatacenterEN: "Tehran-Azadi"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "تهران-آزادی"}); err != nil {
		t.Fatal(err)
	}
	dc, err := s.DatacenterByName("تهران-آزادی")
	if err != nil || dc.DatacenterEN != "Tehran-Azadi" {
		t.Fatalf("empty upsert must preserve datacenter_en, got %+v err=%v", dc, err)
	}
	// ListDatacenters + GetDatacenter carry it too
	l, err := s.ListDatacenters()
	if err != nil || len(l) != 1 || l[0].DatacenterEN != "Tehran-Azadi" {
		t.Fatalf("ListDatacenters lost datacenter_en: %+v err=%v", l, err)
	}
	if g, err := s.GetDatacenter(dc.ID); err != nil || g.DatacenterEN != "Tehran-Azadi" {
		t.Fatalf("GetDatacenter lost datacenter_en: %+v err=%v", g, err)
	}
	// the English name is settable and clearable through the setter
	if err := s.SetDatacenterEN(dc.ID, ""); err != nil {
		t.Fatal(err)
	}
	if g, err := s.GetDatacenter(dc.ID); err != nil || g.DatacenterEN != "" {
		t.Fatalf("SetDatacenterEN('') must clear: %+v err=%v", g, err)
	}
}

func itoa26(n int64) string {
	return jsonNumber(n)
}

func jsonNumber(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
