package panel

// overnight_test.go — hermetic tests for GET /api/overnight/checker
// (BACKEND-44, §2.24). Fixture files in t.TempDir; the env override
// GRE_PANEL_OVERNIGHT_DIR is the seam; no host paths, no network.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func overnightTestResetCache() {
	overnightCache = overnightCheckerCache{}
}

func overnightWriteFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const overnightTestChecker = `{
 "generated": "2026-11-22 09:00",
 "ctunnel_check": {
  "upstream_catalog": {"source": "pkg/catalog/catalog.go", "methods": 49},
  "cores_manifest": {"source": "quantum-cores/ctunnel/MANIFEST.json", "methods": 49},
  "agent_driver": {"source": "internal/agent/ctunnel_driver.go ctMethods", "methods": 49},
  "three_way_match": true,
  "mismatches": []
 },
 "projects": [
  {"project": "hysteria", "license": "MIT", "methods_found": 5, "methods_added": 2,
   "missing": []},
  {"project": "sing-box", "license": "GPL-3.0", "methods_found": 18, "methods_added": 8,
   "missing": ["anytls", "shadowtls", "tuic"]}
 ]
}`

const overnightTestStatus = `{
 "generated": "2026-11-22",
 "projects": [
  {"name": "hysteria", "status": "done", "methods_found": 5},
  {"name": "sing-box", "status": "done", "methods_found": 18},
  {"name": "rathole", "status": "queued", "methods_found": null}
 ]
}`

func TestOvernightCheckerEmptyWhenDirMissing(t *testing.T) {
	t.Setenv("GRE_PANEL_OVERNIGHT_DIR", filepath.Join(t.TempDir(), "nope"))
	overnightTestResetCache()
	t.Cleanup(overnightTestResetCache)

	w := httptest.NewRecorder()
	handleOvernightChecker(w, httptest.NewRequest("GET", "/api/overnight/checker", nil))
	if w.Code != 200 {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var p overnightCheckerPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Generated != "" || len(p.Projects) != 0 {
		t.Fatalf("not the honest empty shape: %+v", p)
	}
	if p.CtunnelCheck.ThreeWayMatch {
		t.Fatalf("empty shape must not claim three_way_match")
	}
	if p.CtunnelCheck.Mismatches == nil {
		t.Fatalf("mismatches must emit [] not null")
	}
}

func TestOvernightCheckerJoinAndPassthrough(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GRE_PANEL_OVERNIGHT_DIR", dir)
	overnightWriteFixture(t, dir, "checker.json", overnightTestChecker)
	overnightWriteFixture(t, dir, "status.json", overnightTestStatus)
	overnightTestResetCache()
	t.Cleanup(overnightTestResetCache)

	w := httptest.NewRecorder()
	handleOvernightChecker(w, httptest.NewRequest("GET", "/api/overnight/checker", nil))
	if w.Code != 200 {
		t.Fatalf("status %d body %s", w.Code, w.Body.String())
	}
	var p overnightCheckerPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Generated != "2026-11-22 09:00" {
		t.Fatalf("generated: %q", p.Generated)
	}
	if len(p.Projects) != 3 { // 2 checker rows + the status-only rathole row
		t.Fatalf("rows: %d (%+v)", len(p.Projects), p.Projects)
	}
	byName := map[string]overnightServedRow{}
	for _, r := range p.Projects {
		byName[r.Name] = r
	}
	h := byName["hysteria"]
	if h.Status != "done" || h.MethodsFound == nil || *h.MethodsFound != 5 ||
		h.MethodsAdded == nil || *h.MethodsAdded != 2 || h.Missing == nil || len(h.Missing) != 0 {
		t.Fatalf("hysteria row wrong: %+v", h)
	}
	s := byName["sing-box"]
	if s.Missing == nil || len(s.Missing) != 3 || s.Missing[0] != "anytls" {
		t.Fatalf("sing-box missing wrong: %+v", s.Missing)
	}
	// status-only row: honest unknowns, never fake zeros
	r := byName["rathole"]
	if r.Status != "queued" || r.MethodsFound != nil || r.MethodsAdded != nil || r.Missing != nil {
		t.Fatalf("rathole status-only row must carry null unknowns: %+v", r)
	}
	// ctunnel three-way passthrough verbatim
	if !p.CtunnelCheck.ThreeWayMatch || len(p.CtunnelCheck.Mismatches) != 0 {
		t.Fatalf("ctunnel_check wrong: %+v", p.CtunnelCheck)
	}
	var up struct {
		Source  string `json:"source"`
		Methods int    `json:"methods"`
	}
	if err := json.Unmarshal(p.CtunnelCheck.UpstreamCatalog, &up); err != nil {
		t.Fatalf("upstream_catalog not valid JSON: %v", err)
	}
	if up.Source != "pkg/catalog/catalog.go" || up.Methods != 49 {
		t.Fatalf("upstream_catalog reshaped: %+v", up)
	}
}

func TestOvernightCheckerMissingMissingEmitsEmptyList(t *testing.T) {
	// A checker row without a missing field (all-covered project) must
	// emit [] not null — the non-nil-slice rule.
	dir := t.TempDir()
	t.Setenv("GRE_PANEL_OVERNIGHT_DIR", dir)
	overnightWriteFixture(t, dir, "checker.json",
		`{"generated":"g","projects":[{"project":"paqet","methods_found":3,"methods_added":2}]}`)
	overnightTestResetCache()
	t.Cleanup(overnightTestResetCache)

	w := httptest.NewRecorder()
	handleOvernightChecker(w, httptest.NewRequest("GET", "/api/overnight/checker", nil))
	var p overnightCheckerPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Projects) != 1 || p.Projects[0].Missing == nil {
		t.Fatalf("missing must be [] for inventoried rows: %+v", p.Projects)
	}
}

func TestOvernightCheckerMalformedCheckerFileIsEmptyShape(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GRE_PANEL_OVERNIGHT_DIR", dir)
	overnightWriteFixture(t, dir, "checker.json", `{"generated": broken`)
	overnightWriteFixture(t, dir, "status.json", overnightTestStatus)
	overnightTestResetCache()
	t.Cleanup(overnightTestResetCache)

	w := httptest.NewRecorder()
	handleOvernightChecker(w, httptest.NewRequest("GET", "/api/overnight/checker", nil))
	if w.Code != 200 {
		t.Fatalf("coordination data must never 500: %d", w.Code)
	}
	var p overnightCheckerPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Generated != "" || len(p.Projects) != 0 {
		t.Fatalf("malformed checker must degrade to the empty shape: %+v", p)
	}
}

func TestOvernightCheckerRequestTimeRereadAfterRewrite(t *testing.T) {
	// The dispatch's core requirement: the endpoint reads the files at
	// REQUEST time (cached briefly) — a rewritten checker.json must land.
	dir := t.TempDir()
	t.Setenv("GRE_PANEL_OVERNIGHT_DIR", dir)
	overnightWriteFixture(t, dir, "checker.json",
		`{"generated":"v1","projects":[{"project":"hysteria","methods_found":5,"methods_added":2,"missing":[]}]}`)
	overnightTestResetCache()
	t.Cleanup(overnightTestResetCache)

	serve := func() overnightCheckerPayload {
		w := httptest.NewRecorder()
		handleOvernightChecker(w, httptest.NewRequest("GET", "/api/overnight/checker", nil))
		var p overnightCheckerPayload
		if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if got := serve(); got.Generated != "v1" || len(got.Projects) != 1 {
		t.Fatalf("first read: %+v", got)
	}
	// rewrite + bump mtime beyond the cached modSum
	newBody := `{"generated":"v2","projects":[{"project":"hysteria","methods_found":6,"methods_added":3,"missing":["mimic"]},{"project":"paqet","methods_found":3,"methods_added":2,"missing":[]}]}`
	overnightWriteFixture(t, dir, "checker.json", newBody)
	fut := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(filepath.Join(dir, "checker.json"), fut, fut); err != nil {
		t.Fatal(err)
	}
	got := serve()
	if got.Generated != "v2" || len(got.Projects) != 2 {
		t.Fatalf("rewritten checker.json did not land (stale cache?): %+v", got)
	}
}

func TestOvernightCheckerDefaultDirUnderConfigDir(t *testing.T) {
	// Without the env override the coordination dir is <configDir>/overnight.
	withTempConfig(t)
	overnightTestResetCache()
	t.Cleanup(overnightTestResetCache)
	dir := filepath.Join(configDir, "overnight")
	overnightWriteFixture(t, dir, "checker.json",
		`{"generated":"default-dir","projects":[{"project":"paqet","methods_found":3,"methods_added":2,"missing":[]}]}`)

	w := httptest.NewRecorder()
	handleOvernightChecker(w, httptest.NewRequest("GET", "/api/overnight/checker", nil))
	var p overnightCheckerPayload
	if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Generated != "default-dir" || len(p.Projects) != 1 {
		t.Fatalf("default configDir/overnight resolution failed: %+v", p)
	}
}
