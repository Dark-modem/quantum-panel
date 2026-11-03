package main

// Regression tests for the root-entry catch-all (audit finding F1): only
// the exact root path may redirect to the secret base path; every other
// unmatched path must answer 404 so probing cannot discover the panel or
// its secret base path.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func rootEntryServe(path string) *httptest.ResponseRecorder {
	oldBase := cfg.BasePath
	cfg.BasePath = "secret123"
	t := httptest.NewRecorder()
	handleRootEntry(t, httptest.NewRequest(http.MethodGet, path, nil))
	cfg.BasePath = oldBase
	return t
}

func TestRootEntryRedirectsExactRootOnly(t *testing.T) {
	resp := rootEntryServe("/")
	if resp.Code != http.StatusFound {
		t.Fatalf("exact / must redirect, got %d", resp.Code)
	}
	if loc := resp.Header().Get("Location"); loc != "/secret123/" {
		t.Fatalf("redirect target wrong: %q", loc)
	}
}

func TestRootEntryUnknownPathIs404(t *testing.T) {
	for _, path := range []string{"/wp-admin", "/.env", "/anything", "//", "/secret123x"} {
		resp := rootEntryServe(path)
		if resp.Code != http.StatusNotFound {
			t.Fatalf("path %q must be 404 (no path disclosure), got %d", path, resp.Code)
		}
		if strings.Contains(resp.Body.String(), "secret123") {
			t.Fatalf("404 body must not leak the base path for %q", path)
		}
	}
}

func TestRootEntryPreservesQuery(t *testing.T) {
	oldBase := cfg.BasePath
	cfg.BasePath = "secret123"
	t.Cleanup(func() { cfg.BasePath = oldBase })
	rr := httptest.NewRecorder()
	handleRootEntry(rr, httptest.NewRequest(http.MethodGet, "/?x=1", nil))
	if loc := rr.Header().Get("Location"); loc != "/secret123/?x=1" {
		t.Fatalf("query must survive the root redirect, got %q", loc)
	}
}
