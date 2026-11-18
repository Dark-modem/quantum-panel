// audit_fixes_test.go — phase 6 regression tests for the audit items:
// M3 (legacy host-operator surface default-off), L1 (no raw store error
// text in mesh API responses), L2 (opt-in pasarguard private-target
// guard). M2's fix is regression-tested by TestUsageDatacenterError.

package panel

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestLegacyGateDisabledByDefault(t *testing.T) {
	withTempConfig(t)
	cfg = panelConfig{Username: "op"} // legacy_enabled absent → OFF
	_ = os.Unsetenv("GRE_PANEL_LEGACY")

	calls := 0
	h := requireLegacy(func(w http.ResponseWriter, r *http.Request) { calls++ })
	r := httptest.NewRequest("POST", "/api/matrix/setup", nil)
	w := httptest.NewRecorder()
	h(w, r)
	if calls != 0 {
		t.Fatal("handler must not run while the legacy surface is off")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("disabled legacy mutation = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "legacy_enabled") {
		t.Fatal("the 403 must name the fix (legacy_enabled)")
	}
}

func TestLegacyGateEnvAndFlagEnable(t *testing.T) {
	withTempConfig(t)
	// Env override wins even with the flag off.
	cfg = panelConfig{}
	t.Setenv("GRE_PANEL_LEGACY", "1")
	if !legacyOpsEnabled() {
		t.Fatal("GRE_PANEL_LEGACY=1 must enable the surface")
	}
	// Config flag alone (no env).
	t.Setenv("GRE_PANEL_LEGACY", "")
	cfg = panelConfig{LegacyEnabled: true}
	if !legacyOpsEnabled() {
		t.Fatal("legacy_enabled=true must enable the surface")
	}
	calls := 0
	h := requireLegacy(func(w http.ResponseWriter, r *http.Request) { calls++ })
	h(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/xray", nil))
	if calls != 1 {
		t.Fatal("enabled surface must pass through")
	}
}

func TestLegacyGatedRouteWiring(t *testing.T) {
	withTempConfig(t)
	cfg = panelConfig{Username: "op"}
	// The REAL wiring: an authed+CSRF client hitting a gated legacy route
	// gets 403 E-LEGACY-01 while the flag is off.
	tok := newSessionToken()
	addSession(tok)
	touchSession(tok)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/xray", requireAuth(requireLegacy(requireCSRF(handleXrayPost))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	req, _ := http.NewRequest("POST", srv.URL+"/api/xray", strings.NewReader("{}"))
	req.AddCookie(&http.Cookie{Name: "gre_session", Value: tok})
	req.Header.Set("X-CSRF-Token", GenerateCSRFToken(tok))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("gated legacy route = %d, want 403", resp.StatusCode)
	}
}

func TestMeshSafeDetailHidesSQLiteInternals(t *testing.T) {
	for _, raw := range []string{
		"SQLITE_CONSTRAINT: UNIQUE constraint failed: tunnels.tunnel_key",
		"no such table: traffic_samples",
		"database is locked (5) (SQLITE_BUSY)",
		"modernc.org/sqlite driver: malformed db",
	} {
		if got := meshSafeDetail(errors.New(raw)); got != "store operation failed (details in the panel error journal)" {
			t.Fatalf("raw store text leaked: %q → %q", raw, got)
		}
	}
	// Operator-actionable text passes through untouched.
	if got := meshSafeDetail(errors.New("referenced by other records (delete or re-link them first)")); !strings.Contains(got, "referenced") {
		t.Fatalf("actionable detail mangled: %q", got)
	}
	if got := meshSafeDetail(nil); got == "" {
		t.Fatal("nil error must yield a generic message, not empty")
	}
}

func TestPGGuardPrivateURLOptIn(t *testing.T) {
	withTempConfig(t)
	s, err := storeOpenForTest()
	if err != nil {
		t.Fatal(err)
	}
	meshStoreSetForTest(s)
	t.Cleanup(meshStoreCloseForTest)
	_ = s.SetSetting("pasarguard_block_private", "true")

	for _, bad := range []string{
		"http://127.0.0.1:8000",
		"http://10.0.0.5:8000",
		"http://192.168.1.10:8000",
		"http://172.16.3.4:8000",
		"http://localhost:8000",
		"http://[::1]:8000",
		"http://169.254.1.2:8000",
	} {
		if err := pgGuardPrivateURL(bad); err == nil {
			t.Fatalf("private target %q must be refused while the guard is on", bad)
		}
	}
	// Public targets pass.
	if err := pgGuardPrivateURL("https://pg.example.com:8000"); err != nil {
		t.Fatalf("public target must pass: %v", err)
	}
	// Guard OFF (default): everything passes — zero behavior change.
	_ = s.SetSetting("pasarguard_block_private", "false")
	if err := pgGuardPrivateURL("http://127.0.0.1:8000"); err != nil {
		t.Fatalf("guard must be opt-in (off = pass): %v", err)
	}
}

func TestEngineArtifactUploadExemptFromGlobalCap(t *testing.T) {
	// The global security middleware caps mutating bodies at 1 MiB; the
	// R5 artifact upload (up to 256 MiB, own MaxBytesReader) must pass.
	if !isEngineArtifactUpload(mustReqPOST("/ui/api/mesh/engines/artifacts")) {
		t.Fatal("artifact upload path not matched")
	}
	if isEngineArtifactUpload(mustReqPOST("/ui/api/mesh/backup")) {
		t.Fatal("other routes must stay capped")
	}
	// And through the REAL middleware chain: a 2 MiB body survives.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/mesh/engines/artifacts", requireAuth(requireCSRF(func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		writeJSON(w, map[string]any{"received": n})
	})))
	h := securityMiddleware(mux)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	tok := newSessionToken()
	addSession(tok)
	touchSession(tok)
	body := strings.Repeat("x", 2<<20) // 2 MiB > the 1 MiB global cap
	req, _ := http.NewRequest("POST", srv.URL+"/api/mesh/engines/artifacts", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "gre_session", Value: tok})
	req.Header.Set("X-CSRF-Token", GenerateCSRFToken(tok))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if n, _ := got["received"].(float64); n != float64(len(body)) {
		t.Fatalf("2 MiB body truncated by the global cap: got %v bytes", got["received"])
	}
}

func mustReqPOST(path string) *http.Request {
	return httptest.NewRequest("POST", path, nil)
}
