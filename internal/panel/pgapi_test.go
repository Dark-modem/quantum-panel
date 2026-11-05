package panel

// pgapi_test.go — the PasarGuard mesh endpoints against a fake upstream
// panel: connect (credentials sealed at rest), dry-run diff, install
// command (api_key sealed, never logged), apply, crosscheck plumbing.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// pgFakeUpstream is a tiny PasarGuard panel fake (token + node list/create).
type pgFakeUpstream struct {
	srv   *httptest.Server
	nodes map[string]map[string]any
	order []string
}

func newPGFakeUpstream(t *testing.T) *pgFakeUpstream {
	t.Helper()
	f := &pgFakeUpstream{nodes: map[string]map[string]any{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("username") != "qadmin" || r.Form.Get("password") != "qpass" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"tok","token_type":"bearer"}`))
	})
	mux.HandleFunc("GET /api/nodes", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		out := []map[string]any{}
		for _, name := range f.order {
			out = append(out, f.nodes[name])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": out, "total": len(out)})
	})
	mux.HandleFunc("POST /api/node", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var n map[string]any
		_ = json.NewDecoder(r.Body).Decode(&n)
		name, _ := n["name"].(string)
		if _, exists := f.nodes[name]; exists {
			w.WriteHeader(http.StatusConflict)
			return
		}
		n["id"] = len(f.order) + 1
		n["status"] = "connecting"
		f.nodes[name] = n
		f.order = append(f.order, name)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(n)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func TestPGConnectReconcileFlow(t *testing.T) {
	up := newPGFakeUpstream(t)
	f := e2eSetup(t) // mesh server + store + session client + servers/DCs

	// Connect: wrong password fails without storing anything.
	if err := pgPost(f.client, f.srv, "/api/mesh/pasarguard/connect",
		map[string]any{"url": up.srv.URL, "username": "qadmin", "password": "WRONG"}); err == nil {
		t.Fatal("wrong password must fail")
	}
	// Correct connect stores the settings (sealed).
	if err := pgPostOK(f.client, f.srv, "/api/mesh/pasarguard/connect",
		map[string]any{"url": up.srv.URL, "username": "qadmin", "password": "qpass"}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	userEnc, _ := f.s.Setting("pasarguard_user_enc")
	if userEnc == "" || strings.Contains(userEnc, "qadmin") {
		t.Fatal("credentials must be stored sealed, not plaintext")
	}

	// Desired: foreign servers × iran DCs = 2 foreign × 2 iran DCs = 4 pairs.
	res := pgCall(t, f.client, f.srv, "POST", "/api/mesh/pasarguard/reconcile",
		map[string]any{"apply": false})
	plan := res["plan"].(map[string]any)
	if plan["creates"].(float64) != 4 {
		t.Fatalf("dry-run creates = %v, want 4 (2 foreign × 2 iran DCs)", plan["creates"])
	}

	// Without cert/api_key the apply must NOT create upstream records.
	res = pgCall(t, f.client, f.srv, "POST", "/api/mesh/pasarguard/reconcile",
		map[string]any{"apply": true, "confirm": "yes"})
	if len(up.order) != 0 {
		t.Fatalf("apply without cert/key must not create upstream nodes, created %d", len(up.order))
	}

	// Provide cert + api key for one pair via install-command (seals the key).
	ic := pgCall(t, f.client, f.srv, "POST", "/api/mesh/pasarguard/install-command",
		map[string]any{"server_id": f.foreign[0], "datacenter_id": 1})
	apiKey, _ := ic["api_key"].(string)
	if len(apiKey) != 36 {
		t.Fatalf("install command must generate a UUID api_key, got %q", apiKey)
	}
	if err := pgPostOK(f.client, f.srv, "/api/mesh/pasarguard/cert",
		map[string]any{"server_id": f.foreign[0], "datacenter_id": 1,
			"server_ca": "-----BEGIN CERTIFICATE-----\nQ\n-----END CERTIFICATE-----", "api_key": apiKey}); err != nil {
		t.Fatalf("cert store: %v", err)
	}

	// Now exactly one pair is provisionable.
	res = pgCall(t, f.client, f.srv, "POST", "/api/mesh/pasarguard/reconcile",
		map[string]any{"apply": true, "confirm": "yes"})
	if len(up.order) != 1 {
		t.Fatalf("apply should create exactly 1 upstream node, created %d (%v); res=%v", len(up.order), up.order, res)
	}
	if !strings.HasPrefix(up.order[0], "quantum:") {
		t.Fatalf("created node must carry the quantum: prefix, got %q", up.order[0])
	}

	// Idempotence: second apply creates nothing new.
	res = pgCall(t, f.client, f.srv, "POST", "/api/mesh/pasarguard/reconcile",
		map[string]any{"apply": true, "confirm": "yes"})
	if len(up.order) != 1 {
		t.Fatalf("second apply duplicated nodes: %v", up.order)
	}

	// Apply without confirm refused.
	if err := pgPost(f.client, f.srv, "/api/mesh/pasarguard/reconcile",
		map[string]any{"apply": true}); err == nil {
		t.Fatal("apply without confirm=yes must be refused")
	}
	_ = fmt.Sprint(res)
}

// small helpers over the mesh session client
func pgPost(c *http.Client, srv *httptest.Server, path string, body map[string]any) error {
	b, _ := json.Marshal(body)
	resp, err := c.Post(srv.URL+path, "application/json", strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 300 {
		return nil
	}
	var e map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&e)
	return fmt.Errorf("HTTP %d: %v", resp.StatusCode, e)
}

func pgPostOK(c *http.Client, srv *httptest.Server, path string, body map[string]any) error {
	return pgPost(c, srv, path, body)
}

func pgCall(t *testing.T, c *http.Client, srv *httptest.Server, method, path string, body map[string]any) map[string]any {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 {
		t.Fatalf("%s %s: HTTP %d: %v", method, path, resp.StatusCode, out)
	}
	return out
}
