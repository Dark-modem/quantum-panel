package panel

// errorpages_test.go — the branded HTML error pages must appear for browser
// page navigations only: every API path, non-HTML client and non-error status
// passes through byte-for-byte (API errors stay JSON — hard requirement).

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// errorPagesCodes is the full required catalog: every handled code must
// render the branded page with a Persian title, its number and the ref id.
var errorPagesCodes = []int{
	400, 401, 403, 404, 405, 408, 409, 413, 422, 429, 500, 502, 503, 504,
}

func errorPagesRequest(t *testing.T, accept, path string) (*http.Request, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if accept != "" {
		r.Header.Set("Accept", accept)
	}
	return r, httptest.NewRecorder()
}

func TestErrorPagesRenderForBrowserNavigations(t *testing.T) {
	for _, code := range errorPagesCodes {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			raw := "upstream body that must never leak"
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				_, _ = w.Write([]byte(raw))
			})
			r, rec := errorPagesRequest(t, "text/html,application/xhtml+xml", "/some/page")
			errorPagesMiddleware(h).ServeHTTP(rec, r)

			if rec.Code != code {
				t.Fatalf("status %d kept: got %d", code, rec.Code)
			}
			body := rec.Body.String()
			if strings.Contains(body, raw) {
				t.Fatalf("upstream body leaked into the page: %q", body)
			}
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
				t.Fatalf("page must be text/html, got %q", ct)
			}
			ref := rec.Header().Get("X-Request-Id")
			if !regexp.MustCompile(`^QR-[0-9a-f]{8}$`).MatchString(ref) {
				t.Fatalf("X-Request-Id must be QR-XXXXXXXX, got %q", ref)
			}
			if !strings.Contains(body, ref) {
				t.Fatalf("page must show the request ref %q", ref)
			}
			// 401 renders the inline login form (the operator can recover
			// right there); every other code links back to the dashboard.
			// All pages carry the mesh link built from the configured base
			// (other tests mutate cfg.BasePath — compute, don't assume).
			meshHref := "/mesh"
			homeHref := "/"
			if bp := strings.Trim(cfg.BasePath, "/"); bp != "" {
				meshHref = "/" + bp + "/mesh"
				homeHref = "/" + bp + "/"
			}
			wants := []string{`dir="rtl"`, `lang="fa"`, `href="` + meshHref + `"`}
			if code == http.StatusUnauthorized {
				wants = append(wants, `id="lform"`, "/api/login", "ورود",
					// The login fetch must carry a QUOTED url: an unquoted
					// `/base/api/login` parses as a regex literal, the
					// handler never binds and the form does a native GET
					// submit that looks like a page refresh.
					"fetch('"+strings.TrimSuffix(meshHref, "/mesh")+"/api/login'")
			} else {
				wants = append(wants, "بازگشت به داشبورد", `href="`+homeHref+`"`)
			}
			for _, want := range wants {
				if !strings.Contains(body, want) {
					t.Fatalf("page missing %q", want)
				}
			}
			// The page never leaks internals.
			for _, leak := range []string{"/tmp/", "/home/", "goroutine", "panic"} {
				if strings.Contains(body, leak) {
					t.Fatalf("page leaks internal detail %q", leak)
				}
			}
		})
	}
}

func TestErrorPagesRequiredPersianCopy(t *testing.T) {
	page404 := errorPageHTML(404, "QR-00000000")
	if !strings.Contains(page404, "صفحه پیدا نشد") {
		t.Fatal("404 must carry «صفحه پیدا نشد»")
	}
	page403 := errorPageHTML(403, "QR-00000000")
	if !strings.Contains(page403, "دسترسی مجاز نیست") {
		t.Fatal("403 must carry «دسترسی مجاز نیست»")
	}
	page429 := errorPageHTML(429, "QR-00000000")
	if !strings.Contains(page429, "تعداد درخواست‌ها بیش از حد مجاز است") {
		t.Fatal("429 must carry «تعداد درخواست‌ها بیش از حد مجاز است»")
	}
	page500 := errorPageHTML(500, "QR-00000000")
	if !strings.Contains(page500, "خطای داخلی سرور") {
		t.Fatal("500 must carry «خطای داخلی سرور»")
	}
	for _, code := range []int{502, 503, 504} {
		page := errorPageHTML(code, "QR-00000000")
		if !strings.Contains(page, "سرویس در دسترس نیست؛ چند لحظه بعد دوباره تلاش کنید") {
			t.Fatalf("%d must carry the unavailable-service sentence", code)
		}
	}
	// Unknown >= 400 falls back to the generic page that still shows the number.
	page418 := errorPageHTML(418, "QR-00000000")
	if !strings.Contains(page418, "418") || !strings.Contains(page418, "خطا") {
		t.Fatal("unknown code must render the generic page with its number")
	}
	if !strings.Contains(page418, "Vazirmatn") || strings.Contains(page418, "http://") || strings.Contains(page418, "https://") {
		t.Fatal("page must be self-contained: Vazirmatn font stack, no external URLs")
	}
	if !strings.Contains(page418, "@media (max-width") {
		t.Fatal("page must carry the mobile media query")
	}
}

func TestErrorPagesPassThroughUntouched(t *testing.T) {
	jsonErr := `{"error":"not found"}`
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(jsonErr))
	})

	// API paths always stay JSON, even with a browser Accept header.
	r, rec := errorPagesRequest(t, "text/html,application/xhtml+xml", "/api/agents")
	errorPagesMiddleware(h).ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound || rec.Body.String() != jsonErr {
		t.Fatalf("API path must stay JSON: code=%d body=%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-Id") != "" {
		t.Fatal("pass-through responses must not gain an error-page ref header")
	}

	// Nested API-looking paths are covered too.
	r, rec = errorPagesRequest(t, "text/html", "/api/agent-tunnels/at_123/power")
	errorPagesMiddleware(h).ServeHTTP(rec, r)
	if rec.Body.String() != jsonErr {
		t.Fatalf("nested API path must stay JSON, got %q", rec.Body.String())
	}

	// Non-HTML clients (agents, curl, health probes) see the raw response.
	r, rec = errorPagesRequest(t, "*", "/login")
	errorPagesMiddleware(h).ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound || rec.Body.String() != jsonErr {
		t.Fatalf("non-HTML accept must pass through: code=%d body=%q", rec.Code, rec.Body.String())
	}
	r, rec = errorPagesRequest(t, "", "/login")
	errorPagesMiddleware(h).ServeHTTP(rec, r)
	if rec.Body.String() != jsonErr {
		t.Fatalf("empty accept must pass through, got %q", rec.Body.String())
	}

	// 2xx/3xx responses are never touched.
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("<html>ok page</html>"))
	})
	r, rec = errorPagesRequest(t, "text/html", "/")
	errorPagesMiddleware(ok).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>ok page</html>" {
		t.Fatalf("200 must pass through: code=%d body=%q", rec.Code, rec.Body.String())
	}
	redir := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "./dash", http.StatusFound)
	})
	r, rec = errorPagesRequest(t, "text/html", "/old")
	errorPagesMiddleware(redir).ServeHTTP(rec, r)
	if rec.Code != http.StatusFound {
		t.Fatalf("302 must pass through, got %d", rec.Code)
	}

	// A body streamed before an explicit status is an implicit 200 in
	// net/http: the middleware must not intercept it.
	stream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("streamed first"))
		w.WriteHeader(http.StatusNotFound)
	})
	r, rec = errorPagesRequest(t, "text/html", "/stream")
	errorPagesMiddleware(stream).ServeHTTP(rec, r)
	if rec.Body.String() != "streamed first" {
		t.Fatalf("body-before-status must stay untouched, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "بازگشت به داشبورد") {
		t.Fatal("body-before-status must never be replaced by the error page")
	}
}

func TestErrorPagesWebSocketUpgradeNeverWrapped(t *testing.T) {
	upgraded := false
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("raw ws rejection"))
		_ = upgraded
	})
	r, rec := errorPagesRequest(t, "text/html", "/api/agent/ws")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Connection", "Upgrade")
	errorPagesMiddleware(h).ServeHTTP(rec, r)
	if rec.Body.String() != "raw ws rejection" {
		t.Fatalf("websocket upgrade request must pass through unwrapped, got %q", rec.Body.String())
	}
}

func TestErrorPagesSuperfluousWriteHeaderIgnored(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.WriteHeader(http.StatusInternalServerError) // must not overwrite
		_, _ = w.Write([]byte("late body"))
	})
	r, rec := errorPagesRequest(t, "text/html", "/page")
	errorPagesMiddleware(h).ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("first status must win, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "صفحه پیدا نشد") {
		t.Fatal("page must replace the handler body after the first error status")
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("late body")) {
		t.Fatal("handler body after the page must be absorbed")
	}
}
