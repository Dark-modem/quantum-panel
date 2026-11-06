// ratelimit_test.go — regression tests for the audit fixes:
//   - H1: the plain-HTTP listener defaults to 127.0.0.1; only an explicit
//     GRE_PANEL_HTTP_BIND / http_bind_host / bind_host widens it.
//   - M1: unauthenticated endpoints rate-limit per IP (429 after the
//     burst) and a blocked IP does not block others.

package panel

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestPanelHTTPBindAddrDefaultsLoopback(t *testing.T) {
	oldCfg := cfg
	t.Cleanup(func() {
		cfg = oldCfg
		_ = os.Unsetenv("GRE_PANEL_HTTP_BIND")
	})
	_ = os.Unsetenv("GRE_PANEL_HTTP_BIND")

	// No config, no env → loopback (audit H1: no cleartext on the wire).
	cfg = panelConfig{}
	if got := panelHTTPBindAddr(8000); got != "127.0.0.1:8000" {
		t.Fatalf("default HTTP bind = %q, want 127.0.0.1:8000", got)
	}

	// bind_host set (existing reverse-proxy configs keep working).
	cfg = panelConfig{BindHost: "0.0.0.0"}
	if got := panelHTTPBindAddr(8000); got != "0.0.0.0:8000" {
		t.Fatalf("bind_host HTTP bind = %q, want 0.0.0.0:8000", got)
	}

	// http_bind_host wins over bind_host (cluster tunnel IP case).
	cfg = panelConfig{BindHost: "0.0.0.0", HTTPBindHost: "10.10.10.1"}
	if got := panelHTTPBindAddr(8000); got != "10.10.10.1:8000" {
		t.Fatalf("http_bind_host = %q, want 10.10.10.1:8000", got)
	}

	// Env overrides everything (container/unit deployments).
	_ = os.Setenv("GRE_PANEL_HTTP_BIND", "0.0.0.0")
	cfg = panelConfig{BindHost: "127.0.0.1", HTTPBindHost: "127.0.0.1"}
	if got := panelHTTPBindAddr(8000); got != "0.0.0.0:8000" {
		t.Fatalf("env override = %q, want 0.0.0.0:8000", got)
	}
}

func TestUnauthRateLimit(t *testing.T) {
	unauthLimiterResetForTest()

	// Burst + a bit is served, then 429 for the SAME ip.
	allowed := 0
	blocked := 0
	now := time.Now()
	for i := 0; i < unauthRateBurst+30; i++ {
		if unauthAllow("10.9.9.9", now.Add(time.Duration(i)*100*time.Millisecond)) {
			allowed++
		} else {
			blocked++
		}
	}
	// Invariants: the whole burst is served, limiting kicks in after it,
	// and the refill across 13s of 100ms steps adds a bounded few more.
	if allowed < unauthRateBurst {
		t.Fatalf("allowed=%d: the burst itself must be served", allowed)
	}
	if allowed > unauthRateBurst+20 || blocked == 0 {
		t.Fatalf("allowed=%d blocked=%d: limiting must engage just past the burst", allowed, blocked)
	}

	// A different IP is unaffected (no cross-tenant punishment).
	if !unauthAllow("10.9.9.8", now.Add(time.Duration(unauthRateBurst+31)*100*time.Millisecond)) {
		t.Fatal("another IP must not be blocked by 10.9.9.9's bucket")
	}

	// Refill over time: 2 minutes at 60/min = 120 tokens back.
	if !unauthAllow("10.9.9.9", now.Add(2*time.Minute)) {
		t.Fatal("after 2 minutes the bucket must have refilled")
	}
}

func TestLimitUnauthMiddleware429(t *testing.T) {
	unauthLimiterResetForTest()
	calls := 0
	h := limitUnauth(func(w http.ResponseWriter, r *http.Request) { calls++ })
	saw429 := false
	for i := 0; i < unauthRateBurst+50; i++ {
		r := httptest.NewRequest("GET", "/api/health", nil)
		r.RemoteAddr = "203.0.113.7:1234"
		w := httptest.NewRecorder()
		h(w, r)
		if w.Code == http.StatusTooManyRequests {
			saw429 = true
			if w.Header().Get("Retry-After") == "" {
				t.Fatal("429 must carry Retry-After")
			}
			break
		}
	}
	if !saw429 {
		t.Fatal("middleware never produced 429")
	}
	if calls >= unauthRateBurst+50 {
		t.Fatal("handler kept running past the burst")
	}
}

func TestLimitUnauthWrapsRealHealthRoute(t *testing.T) {
	unauthLimiterResetForTest()
	mux := http.NewServeMux()
	registerUnauthRoutes(mux, "") // the REAL unauth route wiring
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	saw429 := false
	for i := 0; i < unauthRateBurst+50; i++ {
		resp, err := http.Get(srv.URL + "/api/health")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Fatal("real health route never rate-limited")
	}
	unauthLimiterResetForTest()
}
