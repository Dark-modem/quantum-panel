package panel

// topo_dc_test.go — BACKEND-41 tests for §2.21.2 POST /api/mesh/topologies/dc.
// All hermetic: real in-memory store seam, loopback httptest, no network.
// Legs: happy path (two DCs + subsets → members + tunnels + queued pushes),
// defaults, per-agent validation matrix (offender named), the §10.3 409
// envelope (defense-in-depth, unit-driven — see the comment on
// TestDCWizardDCConflictEnvelope), duplicate-name convergence, honest
// absence on a closed store, and per-agent push-failure reporting.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// dcWizSeed builds two iran DCs + one foreign DC with servers and linked
// agents (the operator flow: agent rows + the store's link call):
//
//	dcA iran "Tehran-A":   svIr1 ← agt_iran1, svIr2 ← agt_iran2
//	dcB iran "Tabriz-B":   svIrB ← agt_iranB (right role, wrong DC)
//	dcF foreign "Frankfurt-F": svFr ← agt_fr1
//	agt_bare: iran, unlinked (ServerID 0)
//
// Agents have LastSeen 0 (never connected) → pushes QUEUE (deterministic,
// no hub): push rows come back status "ok" with the queued note in warnings.
func dcWizSeed(t *testing.T) (*store.Store, *httptest.Server, map[string]int64, map[string]string) {
	t.Helper()
	srv := meshE2EServer(t)
	s := meshStoreMust(t)

	dcA, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "Tehran-A"})
	if err != nil {
		t.Fatal(err)
	}
	dcB, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "Tabriz-B"})
	if err != nil {
		t.Fatal(err)
	}
	dcF, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "Frankfurt-F"})
	if err != nil {
		t.Fatal(err)
	}
	mkServer := func(name, role string, dc int64) int64 {
		id, err := s.UpsertServer(store.Server{Name: name, Role: role, DatacenterID: dc})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	svIr1 := mkServer("teh-1", "iran", dcA)
	svIr2 := mkServer("teh-2", "iran", dcA)
	svIrX := mkServer("teh-x", "iran", dcA)
	svIrB := mkServer("tab-1", "iran", dcB)
	svFr := mkServer("fra-1", "foreign", dcF)
	mkAgent := func(id, name, role string, srvID int64) {
		if err := s.UpsertAgent(store.Agent{ID: id, Name: name, Role: role,
			Status: "online", Version: "v41", Arch: "amd64", OS: "linux", LastSeen: 0, ServerID: srvID}); err != nil {
			t.Fatal(err)
		}
		if srvID != 0 {
			if err := s.LinkAgentServer(id, srvID); err != nil {
				t.Fatal(err)
			}
		}
	}
	mkAgent("agt_a1a1a1a1a1a1", "teh-a1", "iran", svIr1)
	mkAgent("agt_b2b2b2b2b2b2", "teh-b2", "iran", svIr2)
	mkAgent("agt_c3c3c3c3c3c3", "tab-c3", "iran", svIrB)
	mkAgent("agt_d4d4d4d4d4d4", "fra-d4", "foreign", svFr)
	mkAgent("agt_e5e5e5e5e5e5", "bare-e5", "iran", 0)
	// a foreign-ROLE agent linked to an iran server in dcA — the server
	// checks pass, the agent-role check is what must refuse it.
	mkAgent("agt_f6f6f6f6f6f6", "teh-f6", "foreign", svIrX)
	srvByID := map[string]int64{
		"dcA": dcA, "dcB": dcB, "dcF": dcF,
		"svIr1": svIr1, "svIr2": svIr2, "svIrX": svIrX, "svIrB": svIrB, "svFr": svFr,
	}
	agByID := map[string]string{
		"iran1": "agt_a1a1a1a1a1a1", "iran2": "agt_b2b2b2b2b2b2",
		"iranB": "agt_c3c3c3c3c3c3", "fr1": "agt_d4d4d4d4d4d4", "bare": "agt_e5e5e5e5e5e5",
		"roleX": "agt_f6f6f6f6f6f6",
	}
	return s, srv, srvByID, agByID
}

// dcPost posts one §2.21.2 body (raw JSON) and answers status + decoded body.
func dcPost(t *testing.T, srv *httptest.Server, c *http.Client, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest("POST", srv.URL+"/api/mesh/topologies/dc", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST /api/mesh/topologies/dc: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// dcAssertUIError asserts the §1 structured envelope: legacy fields + the
// full ui block with both languages and the expected details.field.
func dcAssertUIError(t *testing.T, out map[string]any, wantStatus int, wantCode, wantField string) {
	t.Helper()
	if out["error_code"] != wantCode {
		t.Fatalf("error_code = %v, want %s (body %v)", out["error_code"], wantCode, out)
	}
	if int(out["http_status"].(float64)) != wantStatus {
		t.Fatalf("http_status = %v, want %d", out["http_status"], wantStatus)
	}
	ui, _ := out["ui"].(map[string]any)
	if ui == nil || ui["code"] != wantCode {
		t.Fatalf("ui.code missing/wrong: %v", out)
	}
	title, _ := ui["title"].(map[string]any)
	if title == nil || title["en"] == "" || title["fa"] == "" {
		t.Fatalf("ui.title must carry BOTH languages: %v", ui["title"])
	}
	if wantField != "" {
		details, _ := ui["details"].(map[string]any)
		if details == nil || details["field"] != wantField {
			t.Fatalf("ui.details.field = %v, want %q", ui["details"], wantField)
		}
	}
}

func TestDCWizardCreateHappyPath(t *testing.T) {
	_, srv, dc, ag := dcWizSeed(t)
	c := meshSession(t)
	body := `{"name":"dc-wiz-1","iran":{"datacenter_id":` + itoa26(dc["dcA"]) +
		`,"agent_ids":["` + ag["iran1"] + `","` + ag["iran2"] + `"]},` +
		`"foreign":{"datacenter_id":` + itoa26(dc["dcF"]) + `,"agent_ids":["` + ag["fr1"] + `"]}}`
	code, out := dcPost(t, srv, c, body)
	if code != 200 {
		t.Fatalf("happy path: HTTP %d: %v", code, out)
	}
	if out["status"] != "ok" {
		t.Fatalf("status = %v, want ok", out["status"])
	}
	topoID := int64(out["topology_id"].(float64))
	if topoID <= 0 {
		t.Fatalf("topology_id = %v", out["topology_id"])
	}
	// pairs = 2 iran servers × 1 foreign server
	if int(out["pairs"].(float64)) != 2 {
		t.Fatalf("pairs = %v, want 2", out["pairs"])
	}
	// tunnels = pairs × 1 (default tunnels_per_pair)
	tuns, _ := out["tunnels"].([]any)
	if len(tuns) != 2 {
		t.Fatalf("tunnels = %d, want 2 (default matrix, one per pair)", len(tuns))
	}
	for _, raw := range tuns {
		tun := raw.(map[string]any)
		if tun["engine"] != "matrix" {
			t.Fatalf("default engine = %v, want matrix", tun["engine"])
		}
		if tun["engine_params"] != "{}" {
			t.Fatalf("engine_params = %v, want the {} default", tun["engine_params"])
		}
		if tun["enabled"] != true {
			t.Fatalf("created tunnels must be enabled (the addendum): %v", tun)
		}
		if !strings.HasPrefix(tun["tunnel_key"].(string), "tun-dc-wiz-1-") {
			t.Fatalf("tunnel key = %v, want the topoSlug keys", tun["tunnel_key"])
		}
	}
	// pushes: every selected agent gets a row; never-connected agents QUEUE
	// (designed outcome) → status ok + the queued note rides warnings.
	pushes, _ := out["pushes"].([]any)
	if len(pushes) != 3 {
		t.Fatalf("pushes = %d rows, want 3 (both sides' selected agents)", len(pushes))
	}
	for _, raw := range pushes {
		row := raw.(map[string]any)
		if row["status"] != "ok" {
			t.Fatalf("push row = %v, want ok (queued is ok by design)", row)
		}
	}
	warns, _ := out["warnings"].([]any)
	queued := 0
	for _, w := range warns {
		if strings.Contains(w.(string), "queued") {
			queued++
		}
	}
	if queued != 3 {
		t.Fatalf("queued warnings = %d, want 3 (never-connected agents queue): %v", queued, warns)
	}
	// store side: the topology exists enabled with exactly the 3 servers
	s := meshStoreMust(t)
	tp, err := s.GetTopology(topoID)
	if err != nil {
		t.Fatalf("topology not stored: %v", err)
	}
	if !tp.Enabled {
		t.Fatalf("created topology must be enabled (the addendum)")
	}
	members, _ := s.TopologyMembers(topoID)
	if len(members) != 3 {
		t.Fatalf("members = %d, want 3 (2 iran + 1 foreign servers)", len(members))
	}
}

func TestDCWizardDefaultsAndExplicitFields(t *testing.T) {
	_, srv, dc, ag := dcWizSeed(t)
	c := meshSession(t)
	// explicit engine/tunnels_per_pair, push=false → no push rows at all
	body := `{"name":"dc-wiz-2","iran":{"datacenter_id":` + itoa26(dc["dcA"]) +
		`,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + itoa26(dc["dcF"]) +
		`,"agent_ids":["` + ag["fr1"] + `"]},"engine":"tinc","tunnels_per_pair":2,"push":false}`
	code, out := dcPost(t, srv, c, body)
	if code != 200 {
		t.Fatalf("explicit fields: HTTP %d: %v", code, out)
	}
	tuns, _ := out["tunnels"].([]any)
	if len(tuns) != 2 {
		t.Fatalf("tunnels_per_pair=2 → 2 tunnels, got %d", len(tuns))
	}
	for _, raw := range tuns {
		if raw.(map[string]any)["engine"] != "tinc" {
			t.Fatalf("engine = %v, want tinc", raw)
		}
	}
	pushes, _ := out["pushes"].([]any)
	if len(pushes) != 0 {
		t.Fatalf("push=false must answer empty pushes, got %v", pushes)
	}
	if warns, _ := out["warnings"].([]any); len(warns) != 0 {
		t.Fatalf("tinc is builtin: no warnings expected, got %v", warns)
	}
}

func TestDCWizardValidationMatrix(t *testing.T) {
	_, srv, dc, ag := dcWizSeed(t)
	c := meshSession(t)
	irDC := itoa26(dc["dcA"])
	frDC := itoa26(dc["dcF"])

	cases := []struct {
		name, body, code, field string
		wantDetailNeedle        string // optional substring in ui.details.reason
	}{
		{"name empty", `{"name":"","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "name", ""},
		{"name uppercase", `{"name":"Bad_Name","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "name", ""},
		{"name traversal", `{"name":"a/b","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "name", ""},
		{"name 65 chars", `{"name":"` + strings.Repeat("a", 65) + `","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "name", ""},
		{"iran dc unknown", `{"name":"dc-v","iran":{"datacenter_id":999999,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.datacenter_id", ""},
		{"iran dc wrong kind", `{"name":"dc-v","iran":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.datacenter_id", ""},
		{"foreign dc unknown", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":999999,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "foreign.datacenter_id", ""},
		{"iran side empty", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":[]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.agent_ids", ""},
		{"foreign side empty", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":[]}}`, "E-VALIDATION", "foreign.agent_ids", ""},
		{"unknown agent", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["agt_999999999999"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.agent_ids", "unknown agent"},
		{"invalid agent id shape", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["bogus"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.agent_ids", "invalid agent id"},
		{"unlinked agent", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["bare"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.agent_ids", "no linked server"},
		{"agent server in another DC", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iranB"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.agent_ids", "sits in datacenter"},
		{"agent on both sides", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["fr1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.agent_ids", "sits in datacenter"},
		{"agent role mismatch", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["roleX"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]}}`, "E-VALIDATION", "iran.agent_ids", "agent role"},
		{"invalid engine", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]},"engine":"Bad Engine"}`, "E-VALIDATION", "engine", ""},
		{"tunnels_per_pair 27", `{"name":"dc-v","iran":{"datacenter_id":` + irDC + `,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + frDC + `,"agent_ids":["` + ag["fr1"] + `"]},"tunnels_per_pair":27}`, "E-VALIDATION", "tunnels_per_pair", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := dcPost(t, srv, c, tc.body)
			if code != 400 {
				t.Fatalf("HTTP %d, want 400: %v", code, out)
			}
			dcAssertUIError(t, out, 400, tc.code, tc.field)
			if tc.wantDetailNeedle != "" {
				ui := out["ui"].(map[string]any)
				details := ui["details"].(map[string]any)
				reason, _ := details["reason"].(string)
				if !strings.Contains(reason, tc.wantDetailNeedle) {
					t.Fatalf("details.reason = %q, want it to contain %q", reason, tc.wantDetailNeedle)
				}
			}
		})
	}

	// The offender must be NAMED: the unlinked-agent case carries the agent
	// id + name in details.
	_, out := dcPost(t, srv, c, `{"name":"dc-v","iran":{"datacenter_id":`+irDC+`,"agent_ids":["`+ag["bare"]+`"]},"foreign":{"datacenter_id":`+frDC+`,"agent_ids":["`+ag["fr1"]+`"]}}`)
	details := out["ui"].(map[string]any)["details"].(map[string]any)
	if details["agent"] != ag["bare"] || details["agent_name"] != "bare-e5" {
		t.Fatalf("offender not named: %v", details)
	}

	// Transport-level hostile bodies: empty, invalid JSON, oversized.
	code, out := dcPost(t, srv, c, "")
	if code != 400 {
		t.Fatalf("empty body: HTTP %d: %v", code, out)
	}
	dcAssertUIError(t, out, 400, "E-VALIDATION", "body")
	code, out = dcPost(t, srv, c, "{not json")
	if code != 400 {
		t.Fatalf("invalid JSON: HTTP %d: %v", code, out)
	}
	dcAssertUIError(t, out, 400, "E-VALIDATION", "body")
	code, out = dcPost(t, srv, c, `{"name":"dc-v","pad":"`+strings.Repeat("x", 520*1024)+`"}`)
	if code != 400 {
		t.Fatalf("oversized body: HTTP %d: %v", code, out)
	}
	dcAssertUIError(t, out, 400, "E-VALIDATION", "body")

	// Wrong types everywhere (JSON with the right keys, wrong shapes).
	code, out = dcPost(t, srv, c, `{"name":123,"iran":"x","foreign":{"datacenter_id":"a","agent_ids":"b"}}`)
	if code != 400 {
		t.Fatalf("wrong types: HTTP %d: %v", code, out)
	}

	// Duplicates collapse: the same agent twice still creates one pair.
	code, out = dcPost(t, srv, c, `{"name":"dc-dup","iran":{"datacenter_id":`+irDC+`,"agent_ids":["`+ag["iran1"]+`","`+ag["iran1"]+`"]},"foreign":{"datacenter_id":`+frDC+`,"agent_ids":["`+ag["fr1"]+`"]},"push":false}`)
	if code != 200 {
		t.Fatalf("duplicates must collapse: HTTP %d: %v", code, out)
	}
	if int(out["pairs"].(float64)) != 1 {
		t.Fatalf("pairs after collapse = %v, want 1", out["pairs"])
	}
}

func TestDCWizardDuplicateNameConverges(t *testing.T) {
	_, srv, dc, ag := dcWizSeed(t)
	c := meshSession(t)
	body := `{"name":"dc-idem","iran":{"datacenter_id":` + itoa26(dc["dcA"]) +
		`,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + itoa26(dc["dcF"]) +
		`,"agent_ids":["` + ag["fr1"] + `"]},"push":false}`
	code1, out1 := dcPost(t, srv, c, body)
	code2, out2 := dcPost(t, srv, c, body)
	if code1 != 200 || code2 != 200 {
		t.Fatalf("idempotent re-run: HTTP %d / %d", code1, code2)
	}
	id1 := int64(out1["topology_id"].(float64))
	id2 := int64(out2["topology_id"].(float64))
	if id1 != id2 {
		t.Fatalf("re-run created a second topology (%d vs %d) — the unique-name upsert must converge", id1, id2)
	}
	if n1, n2 := len(out1["tunnels"].([]any)), len(out2["tunnels"].([]any)); n1 != n2 {
		t.Fatalf("re-run doubled the tunnels (%d vs %d) — unique keys must converge", n1, n2)
	}
}

// TestDCWizardDCConflictEnvelope drives the §10.3 invariant function
// DIRECTLY: through §2.21.2 the per-agent 400 validation already pins every
// server to the chosen DC, so the 409 is defense-in-depth (it fires only if
// the two checks ever drift). The unit call constructs exactly that
// unreachable state (two DCs on one side) to pin the envelope: details.side
// / details.datacenters / details.agents — the §10.3 {side, datacenters,
// tunnels} shape with agents in tunnels' place (the contract addendum).
func TestDCWizardDCConflictEnvelope(t *testing.T) {
	s, _, dc, ag := dcWizSeed(t)
	// Build a side whose resolved servers span dcA + dcB (the unreachable
	// state) with their agents attached.
	svA := dc["svIr1"]
	svB := dc["svIrB"]
	agents := []store.Agent{
		{ID: ag["iran1"], Name: "teh-a1", ServerID: svA},
		{ID: ag["iranB"], Name: "tab-c3", ServerID: svB},
	}
	// Resolve the agents' servers the way the endpoint does (both pass their
	// own side validation in the unreachable world).
	sv1, _ := s.GetServer(svA)
	sv2, _ := s.GetServer(svB)
	chosen, _ := s.GetDatacenter(dc["dcA"])

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/mesh/topologies/dc", nil)
	if ok := dcTopoEnforceOneDC(w, req, s, "dc-v", "iran", chosen, agents, []store.Server{sv1, sv2}); ok {
		t.Fatal("a two-DC side must violate the invariant")
	}
	if w.Code != 409 {
		t.Fatalf("conflict status = %d, want 409", w.Code)
	}
	var out map[string]any
	if err := json.NewDecoder(w.Body).Decode(&out); err != nil {
		t.Fatalf("envelope not JSON: %v", err)
	}
	if out["error_code"] != "E-TOPOLOGY-DC-CONFLICT" {
		t.Fatalf("error_code = %v, want E-TOPOLOGY-DC-CONFLICT", out["error_code"])
	}
	ui := out["ui"].(map[string]any)
	details := ui["details"].(map[string]any)
	if details["side"] != "iran" {
		t.Fatalf("details.side = %v, want iran", details["side"])
	}
	dcs, _ := details["datacenters"].([]any)
	if len(dcs) != 2 {
		t.Fatalf("details.datacenters = %v, want the two DC names", details["datacenters"])
	}
	ids, _ := details["agents"].([]any)
	if len(ids) != 2 || ids[0] != ag["iran1"] || ids[1] != ag["iranB"] {
		t.Fatalf("details.agents = %v, want both offenders sorted", details["agents"])
	}
}

func TestDCWizardPushFailureReportedPerAgent(t *testing.T) {
	s, srv, dc, ag := dcWizSeed(t)
	c := meshSession(t)
	// The foreign agent's server carries a pre-existing ENABLED chisel
	// tunnel (endpointed on svIr2 — NOT on this topology's iran server) and
	// chisel is desired=FALSE on that agent → the enable-before-use gate
	// (the same one the push endpoint applies) fails the push row; the
	// topology itself must still be created (no rollback).
	seedTopo, err := s.UpsertTopology(store.Topology{Name: "seed-topo", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(seedTopo, dc["svFr"]); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(seedTopo, dc["svIr2"]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "tun-seed-chisel", TopologyID: seedTopo,
		LocalServerID: dc["svFr"], RemoteServerID: dc["svIr2"], Engine: "chisel", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: ag["fr1"], Engine: "chisel", Desired: false}); err != nil {
		t.Fatal(err)
	}

	body := `{"name":"dc-pushfail","iran":{"datacenter_id":` + itoa26(dc["dcA"]) +
		`,"agent_ids":["` + ag["iran1"] + `"]},"foreign":{"datacenter_id":` + itoa26(dc["dcF"]) +
		`,"agent_ids":["` + ag["fr1"] + `"]}}`
	code, out := dcPost(t, srv, c, body)
	if code != 200 {
		t.Fatalf("a push failure must not fail the creation: HTTP %d: %v", code, out)
	}
	if out["topology_id"] == nil {
		t.Fatalf("topology must exist despite the push failure: %v", out)
	}
	pushes, _ := out["pushes"].([]any)
	var frRow, irRow map[string]any
	for _, raw := range pushes {
		row := raw.(map[string]any)
		switch row["agent_id"] {
		case ag["fr1"]:
			frRow = row
		case ag["iran1"]:
			irRow = row
		}
	}
	if frRow == nil || frRow["status"] != "failed" {
		t.Fatalf("the readiness-gated agent must report failed: %v", frRow)
	}
	if e, _ := frRow["error"].(string); !strings.Contains(e, "Engine chisel is not enabled") {
		t.Fatalf("failed row error = %q, want the readiness message", e)
	}
	if irRow == nil || irRow["status"] != "ok" {
		t.Fatalf("the uninvolved agent still pushes: %v", irRow)
	}
	// the topology really is in the store with its tunnels
	tuns, err := s.ListTunnels(int64(out["topology_id"].(float64)))
	if err != nil || len(tuns) != 1 {
		t.Fatalf("created topology tunnels: %d (%v), want 1", len(tuns), err)
	}
}

func TestDCWizardStoreClosedHonestAbsence(t *testing.T) {
	// A REAL open failure: nothing is created and the client gets the
	// structured E-STORE envelope (500) — no flat fallback, no partial
	// resolution, no store-creation side effect (the path stays a file-less
	// directory). The mux is built WITHOUT a store fixture (the store must
	// actually attempt and fail its open).
	meshStoreSetForTest(nil)
	t.Cleanup(func() {
		meshStoreMu.Lock()
		meshStoreErr = nil
		meshStoreMu.Unlock()
		meshStoreCloseForTest()
	})
	tmp := t.TempDir()
	badPath := filepath.Join(tmp, "not-a-database")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatalf("cannot stage the forced-failure path: %v", err)
	}
	oldPath := meshDBPath
	meshDBPath = func() string { return badPath }
	t.Cleanup(func() { meshDBPath = oldPath })

	withTempConfig(t)
	mux := http.NewServeMux()
	registerMeshRoutes(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := meshSession(t)
	code, out := dcPost(t, srv, c, `{"name":"dc-closed","iran":{"datacenter_id":1,"agent_ids":["agt_a1a1a1a1a1a1"]},"foreign":{"datacenter_id":2,"agent_ids":["agt_d4d4d4d4d4d4"]}}`)
	if code != 500 {
		t.Fatalf("closed store: HTTP %d, want 500: %v", code, out)
	}
	if out["error_code"] != "E-STORE" {
		t.Fatalf("error_code = %v, want E-STORE", out["error_code"])
	}
	ui, _ := out["ui"].(map[string]any)
	if ui == nil || ui["code"] != "E-STORE" {
		t.Fatalf("structured envelope missing: %v", out)
	}
	// The raw open error (paths, driver text) must stay OFF the wire.
	if raw, _ := out["error"].(string); strings.Contains(raw, badPath) || strings.Contains(raw, "sqlite") {
		t.Fatalf("store-open internals leaked: %q", raw)
	}
	// Honest absence: the failed open created nothing — the path is still
	// the empty DIRECTORY it was (no db, no -journal/-wal sidecars), i.e.
	// the endpoint opened no store as a side effect.
	if fi, err := os.Stat(badPath); err != nil || !fi.IsDir() {
		t.Fatalf("the staged directory changed on a failed open (err %v)", err)
	}
	if entries, err := os.ReadDir(badPath); err != nil || len(entries) != 0 {
		t.Fatalf("failed open must create nothing: entries=%v err=%v", entries, err)
	}
	for _, side := range []string{badPath + "-journal", badPath + "-wal", badPath + "-shm"} {
		if _, err := os.Stat(side); err == nil {
			t.Fatalf("sqlite sidecar %s appeared on a failed open", side)
		}
	}
}
