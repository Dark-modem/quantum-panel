package panel

// uifold_test.go — phase 17: every folded /api/ui read answers like its
// legacy /api/mesh twin (SAME handler → same payload, additive alias),
// and the legacy routes keep working. Phase 18 extends the fold to the
// MUTATIONS (§2.17) with the same equivalence rules.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUIFoldReadAliases(t *testing.T) {
	srv := uiServer(t)
	c := meshSession(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)

	pairs := [][2]string{
		{"/api/ui/topologies", "/api/mesh/topologies"},
		{"/api/ui/mesh/overview", "/api/mesh/overview"},
		{"/api/ui/engines", "/api/mesh/engines"},
		{"/api/ui/engines/artifacts", "/api/mesh/engines/artifacts"},
		{"/api/ui/engines/keysnippet", "/api/mesh/engines/keysnippet"},
		{"/api/ui/engines/templates", "/api/mesh/engines/templates"},
		{"/api/ui/alerts", "/api/mesh/alerts"},
		{"/api/ui/pasarguard", "/api/mesh/pasarguard"},
		{"/api/ui/pasarguard/overhead", "/api/mesh/overhead"},
		{"/api/ui/profiles/pending", "/api/mesh/profiles/pending"},
	}
	for _, p := range pairs {
		folded, legacy := p[0], p[1]
		m := meshGetMap(t, c, srv, folded) // 200 + valid JSON object, else t.Fatal
		if len(m) == 0 {
			t.Fatalf("%s returned an empty object", folded)
		}
		resp, err := c.Get(srv.URL + legacy)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("legacy %s must keep working: %d", legacy, resp.StatusCode)
		}
		var legacyOut map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&legacyOut); err != nil {
			t.Fatalf("legacy %s not json: %v", legacy, err)
		}
	}

	// Payload spot-checks: the folded alerts view carries the phase-16
	// maintenance array; the folded topology list enumerates topologies.
	m := meshGetMap(t, c, srv, "/api/ui/alerts")
	if _, ok := m["maintenance"]; !ok {
		t.Fatalf("folded alerts view missing maintenance: %v", m)
	}
	m = meshGetMap(t, c, srv, "/api/ui/topologies")
	if _, ok := m["topologies"]; !ok {
		t.Fatalf("folded topology list missing topologies: %v", m)
	}

	// Coefficient needs its query param (same validation as the twin).
	respC, err := c.Get(srv.URL + "/api/ui/pasarguard/coefficient")
	if err != nil {
		t.Fatal(err)
	}
	defer respC.Body.Close()
	if respC.StatusCode != http.StatusBadRequest {
		t.Fatalf("folded coefficient without datacenter_id must 400, got %d", respC.StatusCode)
	}
}

// foldDo issues one request and answers status + parsed JSON (any shape).
func foldDo(t *testing.T, c *http.Client, srv *httptest.Server, method, path string, body string) (int, map[string]any) {
	t.Helper()
	var rd *bytes.Reader
	if body == "" {
		rd = bytes.NewReader(nil)
	} else {
		rd = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// foldErrCode digs the E-… code out of a mesh-shaped error envelope
// ({"error": msg, "error_code": "E-…", "hint": …}).
func foldErrCode(m map[string]any) string {
	if m == nil {
		return ""
	}
	if c, _ := m["error_code"].(string); c != "" {
		return c
	}
	if v, ok := m["error"].(map[string]any); ok {
		if c, _ := v["code"].(string); c != "" {
			return c
		}
	}
	if c, _ := m["code"].(string); c != "" {
		return c
	}
	return ""
}

// TestUIFoldMutationAliases — phase 18 (§2.17): the folded MUTATIONS are
// aliases to the SAME handlers, so each must behave like its twin:
// success paths land in the shared store, error paths answer the twin's
// exact envelope, and CSRF enforcement mirrors.
func TestUIFoldMutationAliases(t *testing.T) {
	srv := uiServer(t)
	c := meshSession(t)
	s := meshStoreMust(t)
	iran, foreign, _, _ := uiSeedWorld(t, s)

	// datacenter lifecycle through the ALIAS, observed through the TWIN read
	meshPut(t, c, srv, "POST", "/api/ui/datacenters",
		map[string]any{"name": "P18-DC", "kind": "iran", "price_coefficient": 1.5})
	dcs := meshGetMap(t, c, srv, "/api/mesh/datacenters")
	dcID := int64(0)
	if list, ok := dcs["datacenters"].([]any); ok {
		for _, it := range list {
			d := it.(map[string]any)
			if d["name"] == "P18-DC" {
				dcID = int64(d["id"].(float64))
			}
		}
	}
	if dcID == 0 {
		t.Fatalf("alias-created datacenter missing from twin read: %v", dcs)
	}
	meshPut(t, c, srv, "PATCH", fmt.Sprintf("/api/ui/datacenters/%d", dcID),
		map[string]any{"price_coefficient": 2.5})
	dcs = meshGetMap(t, c, srv, "/api/mesh/datacenters")
	if !bytes.Contains([]byte(fmt.Sprint(dcs)), []byte("2.5")) {
		t.Fatalf("alias PATCH (coefficient) not visible via twin read: %v", dcs)
	}

	// a server created via the ALIAS lands in the same store, and the
	// DELETE alias retires it again (twin read reflects both)
	meshPut(t, c, srv, "POST", "/api/ui/servers",
		map[string]any{"name": "p18-srv", "role": "iran", "datacenter_id": dcID, "address": "10.9.9.9"})
	servers := meshGetMap(t, c, srv, "/api/mesh/servers")
	srvID := int64(0)
	if list, ok := servers["servers"].([]any); ok {
		for _, it := range list {
			sv := it.(map[string]any)
			if sv["name"] == "p18-srv" {
				srvID = int64(sv["id"].(float64))
			}
		}
	}
	if srvID == 0 {
		t.Fatalf("alias-created server missing from twin read: %v", servers)
	}
	meshPut(t, c, srv, "DELETE", fmt.Sprintf("/api/ui/servers/%d", srvID), nil)
	servers = meshGetMap(t, c, srv, "/api/mesh/servers")
	if bytes.Contains([]byte(fmt.Sprint(servers)), []byte("p18-srv")) {
		t.Fatalf("alias DELETE did not retire the server: %v", servers)
	}

	// topology generate + weight through the ALIAS
	gen := meshPut(t, c, srv, "POST", "/api/ui/topologies/generate", map[string]any{
		"name": "p18-mesh", "iran_servers": []int64{iran}, "foreign_servers": []int64{foreign},
		"tunnels_per_pair": 2, "engines": []string{"matrix", "matrix"}, "enabled": true})
	topoID := int64(gen["topology_id"].(float64))
	tunnels := meshGetMap(t, c, srv, fmt.Sprintf("/api/mesh/topologies/%d", topoID))
	tl := tunnels["tunnels"].([]any)
	first := tl[0].(map[string]any)
	// weight-set is NOT part of the fold (per-tunnel weight is contract
	// §2.10 on its own route) — exercised via the mesh twin here
	meshPut(t, c, srv, "POST", fmt.Sprintf("/api/mesh/topologies/%d/weights", topoID),
		map[string]any{"tunnel_id": first["id"].(float64), "weight": 7})

	// link through the ALIAS: the seeded store ids (agt_ir_seeded) do not
	// pass validAgentID's shape check, so enroll a REAL agent through the
	// real handlers and re-link it (a re-link is a plain UPDATE)
	agReal, _ := agentHubEnrollAgent(t, srv, []string{"matrix"})
	meshPut(t, c, srv, "POST", "/api/ui/agents/"+agReal+"/link", map[string]any{"server_id": foreign})
	detail := meshGetMap(t, c, srv, "/api/ui/agents/"+agReal)
	ag := detail["agent"].(map[string]any)
	if ag["linked"] != true {
		t.Fatalf("alias link did not stick: %v", ag)
	}

	// alert settings through the ALIAS, visible on the folded twin read
	meshPut(t, c, srv, "POST", "/api/ui/alerts/settings",
		map[string]any{"enabled": true, "agent_offline_min": 1})
	alerts := meshGetMap(t, c, srv, "/api/mesh/alerts")
	if alerts["enabled"] != true {
		t.Fatalf("alias settings did not stick: enabled=%v", alerts["enabled"])
	}
	if code := meshPut(t, c, srv, "POST", "/api/ui/alerts/test", map[string]any{}); code == nil {
		t.Fatal("alerts test alias answered nothing")
	}

	// pasarguard status answers on the alias (and its legacy twin keeps
	// working); crosscheck has a required query param, so alias≡twin is
	// asserted on the exact validation envelope
	meshGetMap(t, c, srv, "/api/ui/pasarguard/status")
	resp, err := c.Get(srv.URL + "/api/mesh/pasarguard/status")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("legacy twin status must keep working: %v %d", err, resp.StatusCode)
	}
	resp.Body.Close()
	stA, outA := foldDo(t, c, srv, "GET", "/api/ui/pasarguard/crosscheck", "")
	stB, outB := foldDo(t, c, srv, "GET", "/api/mesh/pasarguard/crosscheck", "")
	if stA != stB || foldErrCode(outA) != foldErrCode(outB) {
		t.Fatalf("crosscheck alias≠twin: %d/%v vs %d/%v", stA, outA, stB, outB)
	}
	// write aliases: empty body → the twin's exact envelope
	stA, outA = foldDo(t, c, srv, "POST", "/api/ui/pasarguard/connect", "{}")
	stB, outB = foldDo(t, c, srv, "POST", "/api/mesh/pasarguard/connect", "{}")
	if stA != stB || foldErrCode(outA) != foldErrCode(outB) {
		t.Fatalf("pasarguard connect alias≠twin: %d/%v vs %d/%v", stA, outA, stB, outB)
	}
	stA, outA = foldDo(t, c, srv, "POST", "/api/ui/pasarguard/reconcile", "{}")
	stB, outB = foldDo(t, c, srv, "POST", "/api/mesh/pasarguard/reconcile", "{}")
	if stA != stB || foldErrCode(outA) != foldErrCode(outB) {
		t.Fatalf("pasarguard reconcile alias≠twin: %d/%v vs %d/%v", stA, outA, stB, outB)
	}

	// backup: wrong-password re-auth error must be identical through the alias
	stA, outA = foldDo(t, c, srv, "POST", "/api/ui/backup", `{"password":"wrong","passphrase":"0123456789abc"}`)
	stB, outB = foldDo(t, c, srv, "POST", "/api/mesh/backup", `{"password":"wrong","passphrase":"0123456789abc"}`)
	if stA != stB || foldErrCode(outA) != foldErrCode(outB) || foldErrCode(outA) != "E-AUTH-02" {
		t.Fatalf("backup alias≠twin: %d/%v vs %d/%v", stA, outA, stB, outB)
	}

	// push + profile endpoints: the agent is offline here, so BOTH sides
	// must answer the identical (offline) envelope
	stA, outA = foldDo(t, c, srv, "POST", "/api/ui/agents/"+agReal+"/push", "{}")
	stB, outB = foldDo(t, c, srv, "POST", "/api/mesh/agents/"+agReal+"/push", "{}")
	if stA != stB || foldErrCode(outA) != foldErrCode(outB) {
		t.Fatalf("push alias≠twin: %d/%v vs %d/%v", stA, outA, stB, outB)
	}
	stA, outA = foldDo(t, c, srv, "POST", "/api/ui/agents/"+agReal+"/profile/rollback?kind=hardening", "{}")
	stB, outB = foldDo(t, c, srv, "POST", "/api/mesh/agents/"+agReal+"/profile/rollback?kind=hardening", "{}")
	if stA != stB || foldErrCode(outA) != foldErrCode(outB) {
		t.Fatalf("profile rollback alias≠twin: %d/%v vs %d/%v", stA, outA, stB, outB)
	}

	// CSRF enforcement mirrors: a session cookie WITHOUT the CSRF header
	// is rejected identically on alias and twin
	nocsrf := meshSessionNoCSRF(t)
	stA, _ = foldDo(t, nocsrf, srv, "POST", "/api/ui/datacenters", `{"name":"x","kind":"iran"}`)
	stB, _ = foldDo(t, nocsrf, srv, "POST", "/api/mesh/datacenters", `{"name":"x","kind":"iran"}`)
	if stA != stB || stA < 400 || stA >= 500 {
		t.Fatalf("CSRF must reject equally (4xx) on both: alias %d vs twin %d", stA, stB)
	}
}

// meshSessionNoCSRF is a session client that deliberately omits the
// X-CSRF-Token header (CSRF-negative tests).
func meshSessionNoCSRF(t *testing.T) *http.Client {
	t.Helper()
	tok := newSessionToken()
	addSession(tok)
	touchSession(tok)
	return &http.Client{Transport: noCSRFTransport{tok}}
}

type noCSRFTransport struct{ tok string }

func (m noCSRFTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Del("Cookie")
	r.AddCookie(&http.Cookie{Name: "gre_session", Value: m.tok})
	return http.DefaultTransport.RoundTrip(r)
}
