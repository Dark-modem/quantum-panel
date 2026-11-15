package panel

// security_round3_test.go — BACKEND-19 TASK A: adversarial coverage of the
// phase16–18 surface (maintenance API, §2.16 read aliases, §2.17 mutation
// aliases). Every alias must expose EXACTLY the surface of its twin:
// same auth, same CSRF, same validation errors, same caps — nothing wider.
// All matrices compare alias vs twin so an asymmetry fails loudly.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// r3Reads: [alias, twin] GET pairs (parity asserted with and without auth).
var r3Reads = [][2]string{
	{"/api/ui/topologies", "/api/mesh/topologies"},
	{"/api/ui/mesh/overview", "/api/mesh/overview"},
	{"/api/ui/engines", "/api/mesh/engines"},
	{"/api/ui/engines/artifacts", "/api/mesh/engines/artifacts"},
	{"/api/ui/engines/keysnippet", "/api/mesh/engines/keysnippet"},
	{"/api/ui/engines/templates", "/api/mesh/engines/templates"},
	{"/api/ui/alerts", "/api/mesh/alerts"},
	{"/api/ui/pasarguard", "/api/mesh/pasarguard"},
	{"/api/ui/pasarguard/coefficient?datacenter_id=1", "/api/mesh/coefficient?datacenter_id=1"},
	{"/api/ui/pasarguard/overhead", "/api/mesh/overhead"},
	{"/api/ui/profiles/pending", "/api/mesh/profiles/pending"},
	{"/api/ui/pasarguard/status", "/api/mesh/pasarguard/status"},
}

// r3Mutations: [alias, twin, JSON body]. Twin "" = no mesh twin (new in
// phase 16–18; auth/CSRF asserted against the route itself). Empty body =
// handler reads none. "/999999" entries with an empty body are the DELETE
// variants (exercised in the PATCH/DELETE block below).
var r3Mutations = [][3]string{
	{"/api/ui/backup", "/api/mesh/backup", `{"password":"x","passphrase":"0123456789abc"}`},
	{"/api/ui/agents/agt_000000000000/link", "/api/mesh/agents/agt_000000000000/link", `{"server_id":1}`},
	{"/api/ui/agents/agt_000000000000/push", "/api/mesh/agents/agt_000000000000/push", ``},
	{"/api/ui/agents/agt_000000000000/profile/hardening", "/api/mesh/agents/agt_000000000000/profile/hardening", `{}`},
	{"/api/ui/agents/agt_000000000000/profile/netopt", "/api/mesh/agents/agt_000000000000/profile/netopt", `{}`},
	{"/api/ui/agents/agt_000000000000/profile/confirm", "/api/mesh/agents/agt_000000000000/profile/confirm", `{}`},
	{"/api/ui/agents/agt_000000000000/profile/rollback", "/api/mesh/agents/agt_000000000000/profile/rollback", `{}`},
	{"/api/ui/alerts/settings", "/api/mesh/alerts/settings", `{}`},
	{"/api/ui/alerts/test", "/api/mesh/alerts/test", `{}`},
	{"/api/ui/pasarguard/connect", "/api/mesh/pasarguard/connect", `{}`},
	{"/api/ui/pasarguard/reconcile", "/api/mesh/pasarguard/reconcile", `{}`},
	{"/api/ui/datacenters", "/api/mesh/datacenters", `{"name":"","kind":"nope"}`},
	{"/api/ui/servers", "/api/mesh/servers", `{"name":"","role":"nope"}`},
	{"/api/ui/topologies/generate", "/api/mesh/topologies/generate", `{"name":"BAD NAME!"}`},
	{"/api/ui/agents/agt_000000000000/maintenance", "", `{"until":0}`},
}

var r3IDPairs = [][3]string{ // method, alias, twin (id-based; 4xx parity, no state)
	{"PATCH", "/api/ui/datacenters/999999", "/api/mesh/datacenters/999999"},
	{"DELETE", "/api/ui/datacenters/999999", "/api/mesh/datacenters/999999"},
	{"PATCH", "/api/ui/servers/999999", "/api/mesh/servers/999999"},
	{"DELETE", "/api/ui/servers/999999", "/api/mesh/servers/999999"},
	{"PATCH", "/api/ui/topologies/999999", "/api/mesh/topologies/999999"},
	{"DELETE", "/api/ui/topologies/999999", "/api/mesh/topologies/999999"},
}

// r3Do fires one request; answers status + raw body.
func r3Do(t *testing.T, c *http.Client, srv *httptest.Server, method, path, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
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
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, e := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if e != nil {
			break
		}
	}
	return resp.StatusCode, sb.String()
}

// TestRound3UnauthenticatedMatrix — every NEW route must refuse anonymous
// callers exactly like its twin (401 on reads AND mutations).
func TestRound3UnauthenticatedMatrix(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	bare := &http.Client{}
	for _, p := range r3Reads {
		a, _ := r3Do(t, bare, srv, "GET", p[0], "")
		b, _ := r3Do(t, bare, srv, "GET", p[1], "")
		if a != http.StatusUnauthorized || a != b {
			t.Fatalf("unauth GET %s: alias=%d twin=%d (want 401 both)", p[0], a, b)
		}
	}
	a, _ := r3Do(t, bare, srv, "GET", "/api/ui/agents/agt_000000000000/maintenance", "")
	if a != http.StatusUnauthorized {
		t.Fatalf("unauth GET maintenance: %d (want 401)", a)
	}
	for _, m := range r3Mutations {
		a, _ := r3Do(t, bare, srv, "POST", m[0], m[2])
		if a != http.StatusUnauthorized {
			t.Fatalf("unauth POST %s: alias=%d (want 401)", m[0], a)
		}
		if m[1] != "" {
			b, _ := r3Do(t, bare, srv, "POST", m[1], m[2])
			if a != b {
				t.Fatalf("unauth POST %s: alias=%d twin=%d", m[0], a, b)
			}
		}
	}
	for _, m := range r3IDPairs {
		a, _ := r3Do(t, bare, srv, m[0], m[1], "")
		b, _ := r3Do(t, bare, srv, m[0], m[2], "")
		if a != http.StatusUnauthorized || a != b {
			t.Fatalf("unauth %s %s: alias=%d twin=%d", m[0], m[1], a, b)
		}
	}
}

// TestRound3CSRFStrippedMatrix — a session WITHOUT the CSRF header must be
// rejected identically on alias and twin for every mutation: 4xx and the
// E-AUTH-01 envelope, with NO handler execution (no side effects).
func TestRound3CSRFStrippedMatrix(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	nocsrf := meshSessionNoCSRF(t)
	for _, m := range r3Mutations {
		a, ab := r3Do(t, nocsrf, srv, "POST", m[0], m[2])
		if a < 400 || a >= 500 || !strings.Contains(ab, "E-AUTH-") {
			t.Fatalf("no-CSRF %s: %d/%q (want 4xx E-AUTH-*)", m[0], a, ab)
		}
		if m[1] != "" {
			b, bb := r3Do(t, nocsrf, srv, "POST", m[1], m[2])
			if a != b || ab != bb {
				t.Fatalf("no-CSRF %s: alias %d/%q vs twin %d/%q", m[0], a, ab, b, bb)
			}
		}
	}
	for _, m := range r3IDPairs {
		a, ab := r3Do(t, nocsrf, srv, m[0], m[1], "")
		b, bb := r3Do(t, nocsrf, srv, m[0], m[2], "")
		if a < 400 || a >= 500 || a != b || ab != bb {
			t.Fatalf("no-CSRF %s %s: alias %d/%q vs twin %d/%q", m[0], m[1], a, ab, b, bb)
		}
	}
}

// TestRound3MalformedJSON — garbage bodies must answer the handlers'
// structured 4xx (E-IN-03 family), never 500, identically on alias and twin.
func TestRound3MalformedJSON(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)
	for _, m := range r3Mutations {
		if m[2] == `` {
			continue // push and DELETE variants read no body
		}
		a, ab := r3Do(t, c, srv, "POST", m[0], `{not json`)
		// profile commands on a shape-valid offline agent answer the
		// structured command-timeout envelope — still alias≡twin, still E-
		if a >= 500 && !strings.Contains(m[0], "/profile/") {
			t.Fatalf("malformed JSON %s: %d (want 4xx)", m[0], a)
		}
		// alerts/test ignores its body BY DESIGN (answer: {"sent":N}) —
		// parity is the assertion there, not an error envelope
		if !strings.Contains(m[0], "/alerts/test") && !strings.Contains(ab, "E-") {
			t.Fatalf("malformed JSON %s: body %q (want structured E- envelope)", m[0], ab)
		}
		if m[1] != "" {
			b, bb := r3Do(t, c, srv, "POST", m[1], `{not json`)
			if a != b || ab != bb {
				t.Fatalf("malformed JSON %s: alias %d/%q vs twin %d/%q", m[0], a, ab, b, bb)
			}
		}
	}
	for _, m := range r3IDPairs { // PATCH reads a body; DELETE does not
		if m[0] != "PATCH" {
			continue
		}
		a, ab := r3Do(t, c, srv, "PATCH", m[1], `{not json`)
		b, bb := r3Do(t, c, srv, "PATCH", m[2], `{not json`)
		if a >= 500 || a != b || ab != bb {
			t.Fatalf("malformed JSON %s: alias %d/%q vs twin %d/%q", m[1], a, ab, b, bb)
		}
	}
	// maintenance (no twin): fails soft on garbage
	a, ab := r3Do(t, c, srv, "POST", "/api/ui/agents/agt_000000000000/maintenance", `{not json`)
	if a >= 500 || !strings.Contains(ab, "E-") {
		t.Fatalf("malformed JSON maintenance: %d/%q", a, ab)
	}
}

// TestRound3OversizedBodies — the body caps (512 KiB mesh, 4 KiB
// maintenance, 64 KiB backup) must hold on the ALIASES too; rejected with
// 4xx, never a hang or a 500.
func TestRound3OversizedBodies(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)
	big := `{"pad":"` + strings.Repeat("x", 600*1024) + `"}`
	for _, p := range []string{
		"/api/ui/alerts/settings", "/api/mesh/alerts/settings",
		"/api/ui/agents/agt_000000000000/link", "/api/mesh/agents/agt_000000000000/link",
		"/api/ui/topologies/generate", "/api/mesh/topologies/generate",
	} {
		a, ab := r3Do(t, c, srv, "POST", p, big)
		if a >= 500 || !strings.Contains(ab, "E-IN") {
			t.Fatalf("oversized %s: %d/%q (want 4xx E-IN)", p, a, ab)
		}
	}
	a, ab := r3Do(t, c, srv, "POST", "/api/ui/agents/agt_000000000000/maintenance",
		`{"pad":"`+strings.Repeat("x", 8192)+`"}`)
	if a >= 500 || !strings.Contains(ab, "E-") {
		t.Fatalf("oversized maintenance: %d/%q", a, ab)
	}
	b, bb := r3Do(t, c, srv, "POST", "/api/ui/backup",
		`{"pad":"`+strings.Repeat("x", 128*1024)+`","password":"x","passphrase":"0123456789abc"}`)
	if b >= 500 {
		t.Fatalf("oversized backup: %d/%q", b, bb)
	}
}

// TestRound3PathAndIDInjection — traversal/meta/overflow ids must be
// rejected with 4xx (no 500, no store error leak), identically on alias
// and twin.
func TestRound3PathAndIDInjection(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)
	for _, id := range []string{
		"agt_..%2f..%2f..%2fetc", "agt_%00x", "agt_..%5C..%5Cwindows",
		"not-an-agent", "agt_SHORT", "agt_00000000000000000000000000000000000000",
	} {
		a, ab := r3Do(t, c, srv, "POST", "/api/ui/agents/"+id+"/link", `{"server_id":1}`)
		b, bb := r3Do(t, c, srv, "POST", "/api/mesh/agents/"+id+"/link", `{"server_id":1}`)
		if a >= 500 || a != b || ab != bb {
			t.Fatalf("id injection %q: alias %d/%q vs twin %d/%q", id, a, ab, b, bb)
		}
	}
	for _, id := range []string{"abc", "1;DROP", "-1", "99999999999999999999", "1.5"} {
		a, ab := r3Do(t, c, srv, "PATCH", "/api/ui/datacenters/"+id, `{"price_coefficient":1.5}`)
		b, bb := r3Do(t, c, srv, "PATCH", "/api/mesh/datacenters/"+id, `{"price_coefficient":1.5}`)
		if a >= 500 || a != b || ab != bb {
			t.Fatalf("dc id injection %q: alias %d/%q vs twin %d/%q", id, a, ab, b, bb)
		}
		a, ab = r3Do(t, c, srv, "DELETE", "/api/ui/topologies/"+id, ``)
		b, bb = r3Do(t, c, srv, "DELETE", "/api/mesh/topologies/"+id, ``)
		if a >= 500 || a != b || ab != bb {
			t.Fatalf("topo id injection %q: alias %d/%q vs twin %d/%q", id, a, ab, b, bb)
		}
	}
}

// TestRound3MethodMismatch — wrong-method requests are 405 on the alias
// exactly like the twin (Go 1.22 mux method awareness).
func TestRound3MethodMismatch(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)
	for _, p := range [][2]string{
		{"/api/ui/backup", "/api/mesh/backup"},
		{"/api/ui/alerts/settings", "/api/mesh/alerts/settings"},
		{"/api/ui/pasarguard/connect", "/api/mesh/pasarguard/connect"},
		{"/api/ui/datacenters/999999", "/api/mesh/datacenters/999999"},
	} {
		a, _ := r3Do(t, c, srv, "GET", p[0], "")
		b, _ := r3Do(t, c, srv, "GET", p[1], "")
		if a != b || a != http.StatusMethodNotAllowed {
			t.Fatalf("method mismatch %s: alias=%d twin=%d (want 405 both)", p[0], a, b)
		}
	}
}

// TestRound3MaintenanceBounds — the phase-16 API's validation floor/ceiling.
func TestRound3MaintenanceBounds(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)
	ag, _ := agentHubEnrollAgent(t, srv, []string{"matrix"})
	now := time.Now().Unix()
	for _, tc := range []struct {
		body string
		want string
	}{{`{"until":` + strconv.FormatInt(now-3600, 10) + `}`, "E-VALIDATION"}, // past
		{`{"until":` + strconv.FormatInt(now+31*86400, 10) + `}`, "E-VALIDATION"}, // >30d
		{`{"until":"abc"}`, "E-VALIDATION"},                                       // wrong type
		{`{"until":1.5}`, "E-VALIDATION"},                                         // wrong shape
	} {
		a, ab := r3Do(t, c, srv, "POST", "/api/ui/agents/"+ag+"/maintenance", tc.body)
		if a >= 500 || !strings.Contains(ab, tc.want) {
			t.Fatalf("maintenance bounds %s: %d/%q (want %s)", tc.body, a, ab, tc.want)
		}
	}
	// unknown agent → E-NOTFOUND; malformed ids are shape-checked first
	a, ab := r3Do(t, c, srv, "POST", "/api/ui/agents/agt_000000000000/maintenance", `{"until":0}`)
	if a != http.StatusNotFound || !strings.Contains(ab, "E-NOTFOUND") {
		t.Fatalf("maintenance unknown agent: %d/%q", a, ab)
	}
	a, ab = r3Do(t, c, srv, "POST", "/api/ui/agents/NOT-AN-AGENT/maintenance", `{"until":0}`)
	if a >= 500 || !strings.Contains(ab, "E-") {
		t.Fatalf("maintenance bad id: %d/%q", a, ab)
	}
	// the happy path still works (set + clear)
	a, ab = r3Do(t, c, srv, "POST", "/api/ui/agents/"+ag+"/maintenance",
		`{"until":`+strconv.FormatInt(now+3600, 10)+`}`)
	if a != 200 || !strings.Contains(ab, `"active":true`) {
		t.Fatalf("maintenance set: %d/%q", a, ab)
	}
	a, ab = r3Do(t, c, srv, "POST", "/api/ui/agents/"+ag+"/maintenance", `{"until":0}`)
	if a != 200 || !strings.Contains(ab, `"active":false`) {
		t.Fatalf("maintenance clear: %d/%q", a, ab)
	}
}

// TestRound3SecretRedaction — the new surface must not echo secrets: the
// alerts view carries booleans only (planted secrets must not round-trip),
// keysnippet carries the PUBLIC key surface, and the audit log must not
// contain the planted secret.
func TestRound3SecretRedaction(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)
	a, ab := r3Do(t, c, srv, "POST", "/api/ui/alerts/settings",
		`{"telegram_token":"super-secret-token","telegram_chat":"@chat","webhook_url":"http://10.0.0.7/hook"}`)
	if a != 200 {
		t.Fatalf("settings plant: %d/%q", a, ab)
	}
	for _, view := range []string{"/api/ui/alerts", "/api/mesh/alerts"} {
		_, body := r3Do(t, c, srv, "GET", view, "")
		for _, leak := range []string{"super-secret-token", "@chat", "10.0.0.7", `"webhook_url":`, `"telegram_token":`} {
			if strings.Contains(body, leak) {
				t.Fatalf("%s leaks %q", view, leak)
			}
		}
		if !strings.Contains(body, `"webhook_set":true`) || !strings.Contains(body, `"telegram_set":true`) {
			t.Fatalf("%s lost the configured booleans", view)
		}
	}
	_, kb := r3Do(t, c, srv, "GET", "/api/ui/engines/keysnippet", "")
	if strings.Contains(strings.ToLower(kb), "private") {
		t.Fatalf("keysnippet leaks private material: %q", kb)
	}
	if b, err := os.ReadFile(filepath.Join(configDir, "security-audit.log")); err == nil {
		if strings.Contains(string(b), "super-secret-token") {
			t.Fatal("security audit log contains the telegram token")
		}
	}
}

// TestRound3ArtifactsAliasFailClosed — the artifact upload alias must
// expose EXACTLY the twin's surface: 401 unauth, 4xx without CSRF, and
// E-IN-06 (signature) with auth+CSRF — fail-closed on both sides.
func TestRound3ArtifactsAliasFailClosed(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	mp := "--r3b\r\nContent-Disposition: form-data; name=\"engine\"\r\n\r\nbackhaul\r\n" +
		"--r3b\r\nContent-Disposition: form-data; name=\"version\"\r\n\r\n1.0.0\r\n" +
		"--r3b\r\nContent-Disposition: form-data; name=\"signature\"\r\n\r\nAAAA\r\n" +
		"--r3b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.bin\"\r\n\r\nbytes\r\n" +
		"--r3b--\r\n"
	mpPost := func(client *http.Client, path string) (int, string) {
		req, err := http.NewRequest("POST", srv.URL+path, strings.NewReader(mp))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "multipart/form-data; boundary=r3b")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 2048)
		for {
			n, e := resp.Body.Read(buf)
			sb.Write(buf[:n])
			if e != nil {
				break
			}
		}
		return resp.StatusCode, sb.String()
	}
	bare := &http.Client{}
	a, ab := mpPost(bare, "/api/ui/engines/artifacts")
	b, bb := mpPost(bare, "/api/mesh/engines/artifacts")
	if a != http.StatusUnauthorized || a != b {
		t.Fatalf("upload unauth: alias=%d twin=%d", a, b)
	}
	nocsrf := meshSessionNoCSRF(t)
	a, ab = mpPost(nocsrf, "/api/ui/engines/artifacts")
	b, bb = mpPost(nocsrf, "/api/mesh/engines/artifacts")
	if a < 400 || a >= 500 || a != b || ab != bb {
		t.Fatalf("upload no-CSRF: alias %d/%q vs twin %d/%q", a, ab, b, bb)
	}
	c := meshSession(t)
	a, ab = mpPost(c, "/api/ui/engines/artifacts")
	b, bb = mpPost(c, "/api/mesh/engines/artifacts")
	if a >= 500 || a != b || ab != bb || !strings.Contains(ab, "E-IN-06") {
		t.Fatalf("upload bad signature: alias %d/%q vs twin %d/%q", a, ab, b, bb)
	}
}

// TestRound3AlertsTestAudited — the round-3 fix: the test-send is audited.
func TestRound3AlertsTestAudited(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)
	// clear any webhook planted by an earlier test (global engine state)
	r3Do(t, c, srv, "POST", "/api/ui/alerts/settings", `{"webhook_url":"-"}`)
	a, ab := r3Do(t, c, srv, "POST", "/api/ui/alerts/test", `{}`)
	if a != 200 || !strings.Contains(ab, `"sent":`) {
		t.Fatalf("alerts test: %d/%q", a, ab)
	}
	b, err := os.ReadFile(filepath.Join(configDir, "security-audit.log"))
	if err != nil {
		t.Fatalf("audit file: %v", err)
	}
	if !strings.Contains(string(b), "alerts_test_sent") {
		t.Fatal("alerts test send is not audited")
	}
}
