package panel

// cores_manifest_test.go — BACKEND-43 hermetic tests: the manifest
// passthrough never touches the network (coresManifestFetcher seam → a
// fixture string), the §2.22 row fills are checked end-to-end through the
// handler and the method-board join is exercised through the same seams
// BACKEND-43 wires.

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// coresManifestReset clears the manifest cache/seam state (globals).
func coresManifestReset(t *testing.T) {
	t.Helper()
	coresManifestMu.Lock()
	coresManifestCache = map[string]*coresManifestCacheEntry{}
	coresManifestBusy = map[string]bool{}
	coresManifestDropAt = time.Time{}
	coresManifestMu.Unlock()
	old := coresManifestFetcher
	t.Cleanup(func() { coresManifestFetcher = old })
}

// coresManifestCTunnel is a fixture MANIFEST.json carrying the
// architect-fixed methods[] schema (a 2-method stand-in for the real
// 49-row ctunnel block AGENT-29 publishes).
// coresManifestCTunnel is a fixture MANIFEST.json carrying the
// architect-fixed methods[] schema (a 2-method stand-in for the real
// 49-row ctunnel block AGENT-29 publishes). BACKEND-45: the ids mirror
// the REAL landed manifests — JSON numbers (ctunnel 49 rows, sing-box 16,
// hysteria 2, easytier 1, waterwall 18 are all numeric), with the
// driver-consumable spelling in `name` (the ctunnel driver's ctMethods
// keys are names like "6to4"; the numeric id is the manifest's row
// number).
const coresManifestCTunnel = `{"core":"ctunnel","upstream":"https://github.com/CIAUB/C-Tunnel",
"license":"AGPL-3.0","latest":"2.0.0","methods":[
{"id":1,"name":"kernel-l3-1","title":"Kernel L3 tunnel 1","layer":"L3","transport":"udp","ipver":"v4","overhead":42,"needs_port":true,"userspace":false,"mikrotik":false,"family":"kernel"},
{"id":2,"name":"udpcrypt-1","title":"UDP-Crypt 1","layer":"L3","transport":"udp","ipver":"both","overhead":28,"needs_port":true,"userspace":true,"mikrotik":false,"family":"udp-crypt"}]}`

func TestCoresManifestPassthroughVerbatim(t *testing.T) {
	coresManifestReset(t)
	var fetched []string
	coresManifestFetcher = func(core string) ([]byte, error) {
		fetched = append(fetched, core)
		if core == "ctunnel" {
			return []byte(coresManifestCTunnel), nil
		}
		return nil, &coresManifest404{}
	}
	// cold: the not-yet entry answers (background fetch), methods nil
	entry := fetchCoresManifestCached("ctunnel")
	if entry == nil || len(entry.Methods) != 0 {
		t.Fatalf("cold entry must answer immediately: %+v", entry)
	}
	// the fetch lands for the next read
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entry = fetchCoresManifestCached("ctunnel")
		if len(entry.Methods) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(entry.Methods) == 0 {
		t.Fatalf("manifest fetch never landed (fetched=%v)", fetched)
	}
	// VERBATIM: the row carries the manifest's own bytes (no reshaping)
	if !strings.Contains(string(entry.Methods), `"id":1`) ||
		!strings.Contains(string(entry.Methods), `"name":"kernel-l3-1"`) ||
		!strings.Contains(string(entry.Methods), `"family":"udp-crypt"`) {
		t.Fatalf("methods passthrough lost fidelity: %s", entry.Methods)
	}
	var methods []map[string]any
	if err := json.Unmarshal(entry.Methods, &methods); err != nil || len(methods) != 2 {
		t.Fatalf("methods is not the 2-row array: %v %d", err, len(methods))
	}
	if methods[0]["layer"] != "L3" || methods[1]["ipver"] != "both" {
		t.Fatalf("manifest vocabulary altered: %+v", methods)
	}
	// a 404 manifest caches the failure (honest null, Err set)
	entry = fetchCoresManifestCached("frp")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entry.Err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
		entry = fetchCoresManifestCached("frp")
	}
	if entry.Err == nil || len(entry.Methods) != 0 {
		t.Fatalf("404 must cache as failure: %+v", entry)
	}
}

type coresManifest404 struct{}

func (*coresManifest404) Error() string { return "cores manifest: 404 Not Found" }

func TestCoresManifestMethodsNotAnArray(t *testing.T) {
	coresManifestReset(t)
	coresManifestFetcher = func(core string) ([]byte, error) {
		return []byte(`{"core":"x","methods":{"not":"an array"}}`), nil
	}
	entry := fetchCoresManifestCached("x")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if entry.Err != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
		entry = fetchCoresManifestCached("x")
	}
	if entry.Err == nil || !strings.Contains(entry.Err.Error(), "not an array") {
		t.Fatalf("non-array methods must fail honestly: %+v", entry)
	}
}

func TestCoresVersionCompare(t *testing.T) {
	// semver AND the CALVER form the cores feed actually publishes
	if coresVersionCompare("1.1.1", "2.0.0") >= 0 {
		t.Fatalf("1.1.1 < 2.0.0")
	}
	if coresVersionCompare("2.0.0", "1.1.1") <= 0 {
		t.Fatalf("2.0.0 > 1.1.1")
	}
	if coresVersionCompare("20230206.0", "20240101.0") >= 0 {
		t.Fatalf("CALVER compare broken")
	}
	if coresVersionCompare("1.2", "1.2.0") != 0 {
		t.Fatalf("2-seg pads to 3: 1.2 == 1.2.0")
	}
	if coresVersionCompare("v1.1.1", "1.1.1") != 0 {
		t.Fatalf("v prefix is normalized")
	}
	if coresVersionCompare("abc", "1.0.0") > 0 {
		t.Fatalf("a non-parsable version never claims newer")
	}
}

func TestCoresInstalledOutdated(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	// two agents: one reports ctunnel v1.1.1 installed (engine_status), one
	// carries the version only in the hello catalog (v2.0.0)
	if err := s.UpsertAgent(store.Agent{ID: "agt_0a0a0a0a0a0a", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgent(store.Agent{ID: "agt_0b0b0b0b0b0b", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: "agt_0a0a0a0a0a0a",
		Engine: "ctunnel", Installed: true, Version: "1.1.1", UpdatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentV2Session("agt_0b0b0b0b0b0b", 2,
		`[{"id":"ctunnel","version":"2.0.0"}]`); err != nil {
		t.Fatal(err)
	}
	// newest installed across agents = 2.0.0; vs latest 2.0.1 → outdated
	installed, outdated := coreRowInstalledOutdated(s, "ctunnel", "2.0.1")
	if installed != "2.0.0" || outdated == nil || !*outdated {
		t.Fatalf("installed/outdated: %q %v", installed, outdated)
	}
	// equal versions → false
	_, outdated = coreRowInstalledOutdated(s, "ctunnel", "2.0.0")
	if outdated == nil || *outdated {
		t.Fatalf("2.0.0 vs 2.0.0 must be false: %v", outdated)
	}
	// unknown latest → outdated null (honest unknown), installed kept
	installed, outdated = coreRowInstalledOutdated(s, "ctunnel", "")
	if installed != "2.0.0" || outdated != nil {
		t.Fatalf("unknown latest: %q %v", installed, outdated)
	}
	// nothing installed → both empty/null
	installed, outdated = coreRowInstalledOutdated(s, "frp", "0.61.2")
	if installed != "" || outdated != nil {
		t.Fatalf("no install reports: %q %v", installed, outdated)
	}
	// the engine-id alias: agents report wst, the row is wstunnel
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: "agt_0a0a0a0a0a0a",
		Engine: "wst", Installed: true, Version: "9.9.9", UpdatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	installed, outdated = coreRowInstalledOutdated(s, "wstunnel", "9.9.8")
	if installed != "9.9.9" || outdated == nil || *outdated {
		t.Fatalf("alias resolution: %q %v", installed, outdated)
	}
}

func TestCoresAvailableHandlerAdditiveFields(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	coresManifestReset(t)
	coresManifestFetcher = func(core string) ([]byte, error) {
		if core == "ctunnel" {
			return []byte(coresManifestCTunnel), nil
		}
		return nil, &coresManifest404{}
	}
	// the release feed seam: a 2-core feed
	oldFeed := coresReleasesFetcher
	coresReleasesFetcher = func() ([]ghRelease, error) {
		return []ghRelease{
			{Tag: "ctunnel-v2.0.1", PublishedAt: "2026-11-22T00:00:00Z"},
			{Tag: "frp-v0.61.2", PublishedAt: "2026-11-21T00:00:00Z"},
		}, nil
	}
	t.Cleanup(func() { coresReleasesFetcher = oldFeed })
	if err := s.UpsertAgent(store.Agent{ID: "agt_0c0c0c0c0c0c", Name: "c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: "agt_0c0c0c0c0c0c",
		Engine: "ctunnel", Installed: true, Version: "1.1.1", UpdatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest("GET", "/api/cores/available", nil)
	w := httptest.NewRecorder()
	handleCoresAvailable(w, r)
	if w.Code != 200 {
		t.Fatalf("cores available: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Cores []coreRow `json:"cores"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Cores) != 2 {
		t.Fatalf("cores rows: %d", len(resp.Cores))
	}
	var ct *coreRow
	for i := range resp.Cores {
		if resp.Cores[i].Core == "ctunnel" {
			ct = &resp.Cores[i]
		}
	}
	if ct == nil {
		t.Fatalf("no ctunnel row: %+v", resp.Cores)
	}
	// methods[] waits for the background fetch → poll the handler until it
	// lands (the seam is local, so this is fast)
	// JSON null round-trips through json.RawMessage as the literal "null"
	// bytes — treat that as "not landed yet"
	methodsEmpty := func() bool { return len(ct.Methods) == 0 || string(ct.Methods) == "null" }
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && methodsEmpty() {
		time.Sleep(20 * time.Millisecond)
		w = httptest.NewRecorder()
		handleCoresAvailable(w, r)
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for i := range resp.Cores {
			if resp.Cores[i].Core == "ctunnel" {
				ct = &resp.Cores[i]
			}
		}
	}
	if methodsEmpty() {
		t.Fatalf("ctunnel methods[] never landed")
	}
	if !strings.Contains(string(ct.Methods), `"id":2`) ||
		!strings.Contains(string(ct.Methods), `"name":"udpcrypt-1"`) {
		t.Fatalf("methods passthrough altered: %s", ct.Methods)
	}
	if ct.InstalledVersion != "1.1.1" || ct.Outdated == nil || !*ct.Outdated {
		t.Fatalf("ctunnel installed/outdated: %q %v (latest %s)", ct.InstalledVersion, ct.Outdated, ct.LatestVersion)
	}
	// frp: manifest 404 → methods null; nothing installed → ""/null
	var frp *coreRow
	for i := range resp.Cores {
		if resp.Cores[i].Core == "frp" {
			frp = &resp.Cores[i]
		}
	}
	// methods "absent" decodes as the literal "null" bytes through
	// json.RawMessage — the honest null either way
	frpMethodsAbsent := frp != nil && (frp.Methods == nil || string(frp.Methods) == "null")
	if frp == nil || !frpMethodsAbsent || frp.InstalledVersion != "" || frp.Outdated != nil {
		t.Fatalf("frp row: %+v", frp)
	}
}

func TestWireCoresManifestJoinBoard(t *testing.T) {
	withTempConfig(t)
	coresManifestReset(t)
	wireCoresManifestJoin()
	t.Cleanup(func() {
		mboardManifestMethodsFn = func(string) []methodManifestEntry { return nil }
		mboardManifestEntryFn = func(string) (methodManifestEntry, bool) { return methodManifestEntry{}, false }
	})
	coresManifestFetcher = func(core string) ([]byte, error) {
		if core == "ctunnel" {
			return []byte(coresManifestCTunnel), nil
		}
		return nil, &coresManifest404{}
	}
	// the release feed seam: the ctunnel core only
	oldFeed := coresReleasesFetcher
	coresReleasesFetcher = func() ([]ghRelease, error) {
		return []ghRelease{{Tag: "ctunnel-v2.0.0"}}, nil
	}
	t.Cleanup(func() { coresReleasesFetcher = oldFeed })

	// the board candidates: uiKnownEngines + the catalog-agreed ctunnel
	// ENGINE row (ctunnel is not in uiKnownEngines — it carries the
	// methodCatalog metadata) + the manifest methods of the agreed core.
	// The manifest fetch is background — poll until it lands (the seam is
	// local, so this is fast).
	var got []string
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got = methodBoardCandidates([]string{"ctunnel"}, []string{"ctunnel"})
		if len(got) == len(uiKnownEngines)+1+2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(got) != len(uiKnownEngines)+1+2 {
		t.Fatalf("candidates %d, want %d + 1 engine row + 2 manifest methods", len(got), len(uiKnownEngines))
	}
	if got[len(uiKnownEngines)] != "ctunnel" {
		t.Fatalf("the agreed engine row must precede its manifest methods: %v", got)
	}
	// BACKEND-45: the manifest method ids are the NAME column (the
	// driver-consumable spelling) — the numeric id is the manifest's row
	// number
	m1, m2 := got[len(got)-2], got[len(got)-1]
	if m1 != "kernel-l3-1" || m2 != "udpcrypt-1" {
		t.Fatalf("manifest method ids: %q %q", m1, m2)
	}
	// the reverse lookup resolves the manifest method's identity columns
	entry, ok := mboardManifestEntryOf("udpcrypt-1")
	if !ok || entry.Engine != "ctunnel" || entry.Layer != "L3" || entry.IPVer != "both" {
		t.Fatalf("reverse lookup: %v %v", entry, ok)
	}
	// and the row metadata mapping lowercases layer, keeps the rest verbatim
	row := unprobedRows([]string{"udpcrypt-1"})
	if len(row) != 1 || row[0].Engine != "ctunnel" || row[0].Layer != "l3" || row[0].Transport != "udp" || row[0].IPVer != "both" {
		t.Fatalf("manifest row metadata: %+v", row[0])
	}
}
