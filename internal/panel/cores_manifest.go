package panel

// cores_manifest.go — BACKEND-43 (UI-API-CONTRACT §2.22 addendum): the
// cores MANIFEST.json read surface. AGENT-29's cores v2 work publishes the
// ctunnel methods[] block in the core folder's MANIFEST.json (the
// architect-fixed schema:
// methods:[{id,name,title,layer:'L2|L3|L4',transport,ipver,overhead,
// needs_port,userspace,mikrotik,family}]). The panel fetches that manifest
// per discovered core and passes methods[] through VERBATIM on the §2.22
// rows — no reshaping, no validation beyond "it is a JSON array" — and
// wires the method-probe board's manifest join (§2.23.1): every manifest
// method id becomes a board row under its core when BOTH agents' engine
// catalogs agree on the core.
//
// AMENDMENT to §2.22.1's "releases only" line: the MANIFEST.json fetch is
// the ONE git-content read the panel makes (documented in the addendum) —
// raw.githubusercontent.com/…/HEAD/<core>/MANIFEST.json, no user input in
// the URL (the core name comes from the coresTagRe-validated release feed),
// 10-minute success / 1-minute failure cache, single-flight per core. The
// release feed, its caches and every M2 invariant stay UNCHANGED.
//
// The same rows gain `installed_version` (the newest version any enrolled
// agent reported installed for the core — the engine_status rows the
// agents' heartbeats carry, with the hello engine-catalog version as the
// fallback) and `outdated` (installed vs the row's latest_version — the
// same comparison RULES §2.20 uses, numeric over the cores' digits-only
// version shape; null when either side is unknown).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// coresManifestURLBase is the raw-content base of the cores repo. HEAD
// resolves to the default branch (no branch-name guessing); the core name
// is appended after coresTagRe validation — no request input reaches the
// URL unescaped.
var coresManifestURLBase = "https://raw.githubusercontent.com/Dark-modem/quantum-cores/HEAD"

// coresManifestClient bounds every manifest read (raw CDN, generous
// budget — the 15 s timeout and the per-core cache are the politeness
// bounds; the endpoint is never fetched concurrently for one core).
var coresManifestClient = &http.Client{Timeout: 15 * time.Second}

// coresManifestFetcher is the seam the tests swap (an httptest server);
// production performs the real GET.
var coresManifestFetcher = func(core string) ([]byte, error) {
	url := coresManifestURLBase + "/" + core + "/MANIFEST.json"
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "quantum-panel-cores/"+panelVersion)
	resp, err := coresManifestClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cores manifest: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // manifests are small; 1 MiB ceiling
	if err != nil {
		return nil, err
	}
	return body, nil
}

// coresManifestCacheEntry is one core's cached manifest view. Methods is
// the VERBATIM methods[] JSON (nil = the manifest does not publish one,
// has not been fetched yet, or is unreachable — the row renders null,
// never a guessed shape; Err distinguishes the failure). License and
// Upstream are the manifest's own informational lines, passed through
// VERBATIM ("" = the manifest does not publish the field or the manifest
// is unreachable — never guessed; a manifest parse failure also answers
// "" for both: a failed read never half-lands).
type coresManifestCacheEntry struct {
	Methods  json.RawMessage
	License  string
	Upstream string
	At       time.Time
	Err      error
}

var (
	coresManifestMu    sync.Mutex
	coresManifestCache = map[string]*coresManifestCacheEntry{}
	coresManifestBusy  = map[string]bool{} // single-flight markers
)

// fetchCoresManifestCached answers one core's manifest view: 10-minute
// success / 1-minute failure cache, single-flight per core. A cold/stale
// entry triggers the fetch in the BACKGROUND and answers immediately (the
// row renders null until the fetch lands — the next read inside the TTL
// sees it): a GET must never serialize behind 9 potentially dead raw
// requests.
func fetchCoresManifestCached(core string) *coresManifestCacheEntry {
	coresManifestMu.Lock()
	entry := coresManifestCache[core]
	if entry != nil {
		ttl := 10 * time.Minute
		if entry.Err != nil {
			ttl = time.Minute
		}
		if time.Since(entry.At) < ttl || coresManifestBusy[core] {
			coresManifestMu.Unlock()
			return entry
		}
	}
	if coresManifestBusy[core] {
		// the background fetch is in flight and no entry is stored yet —
		// answer the not-yet marker, never nil
		coresManifestMu.Unlock()
		return &coresManifestCacheEntry{At: time.Now()}
	}
	coresManifestBusy[core] = true
	coresManifestMu.Unlock()

	go func() {
		body, err := coresManifestFetcher(core)
		newEntry := &coresManifestCacheEntry{At: time.Now(), Err: err}
		if err == nil && len(body) > 0 {
			newEntry.Methods, newEntry.License, newEntry.Upstream, err = parseCoresManifest(core, body)
			newEntry.Err = err
		}
		coresManifestMu.Lock()
		coresManifestCache[core] = newEntry
		coresManifestBusy[core] = false
		coresManifestMu.Unlock()
	}()
	if entry != nil {
		return entry // the stale entry answers; the fresh one lands for the next read
	}
	return &coresManifestCacheEntry{At: time.Now()} // not-yet marker (Methods nil)
}

// dropCoresManifestCache makes the next read re-fetch every manifest
// (throttled to 30 s like the release-feed drop).
func dropCoresManifestCache() {
	coresManifestMu.Lock()
	defer coresManifestMu.Unlock()
	// the throttle rides the release feed's clock via a local marker
	if time.Since(coresManifestDropAt) < 30*time.Second {
		return
	}
	coresManifestDropAt = time.Now()
	coresManifestCache = map[string]*coresManifestCacheEntry{}
}

var coresManifestDropAt time.Time

// parseCoresManifest extracts methods[] plus the informational
// license/upstream lines from a manifest body — verbatim; the only
// validation is "the manifest is JSON" and "methods is a JSON array".
// A parse failure returns "" for license/upstream too: a failed read
// never half-lands.
func parseCoresManifest(core string, body []byte) (methods json.RawMessage, license, upstream string, err error) {
	var manifest struct {
		Methods  json.RawMessage `json:"methods"`
		License  string          `json:"license"`
		Upstream string          `json:"upstream"`
	}
	if err = json.Unmarshal(body, &manifest); err != nil {
		return nil, "", "", fmt.Errorf("cores manifest %s: not valid JSON", core)
	}
	license, upstream = manifest.License, manifest.Upstream
	if len(manifest.Methods) == 0 {
		return nil, license, upstream, nil // published without methods[] — honest null
	}
	var probe []json.RawMessage
	if err = json.Unmarshal(manifest.Methods, &probe); err != nil {
		return nil, "", "", fmt.Errorf("cores manifest %s: methods is not an array", core)
	}
	// JSON null decodes into RawMessage as the literal "null" bytes —
	// normalize it to nil so the cache semantics stay "absent = nil"
	if len(probe) == 0 {
		return nil, license, upstream, nil
	}
	return manifest.Methods, license, upstream, nil
}

// coresEngineAlias maps an engine id to the cores-repo core folder name
// where they differ (verified against the repo layout 2026-11-22: the
// wstunnel core drives the panel's wst engine. BACKEND-45 adds sing-box —
// the core folder/repo name carries the dash, the agents' engine id does
// not (quantum-agent singbox_driver.go singboxID); without the second
// alias the sing-box row never joins its manifest methods[] or its
// installed_version resolution).
var coresEngineAlias = map[string]string{"wst": "wstunnel", "singbox": "sing-box"}

// coresCoreOfEngine resolves the repo core folder for an engine id.
func coresCoreOfEngine(engine string) string {
	if c, ok := coresEngineAlias[engine]; ok {
		return c
	}
	return engine
}

// coresEngineOfCore resolves the ENGINE id the agents report for a core
// folder name (the inverse of the alias map).
func coresEngineOfCore(core string) string {
	for engine, c := range coresEngineAlias {
		if c == core {
			return engine
		}
	}
	return core
}

// coresManifestMethods is the method-board join's production
// implementation: the verbatim manifest methods[] of one core, decoded
// ONLY into the board's identity columns (the §2.22 passthrough itself
// stays raw).
//
// BACKEND-45 fixes (found by the acceptance pass against the REAL landed
// manifests): every live manifest publishes `id` as a JSON NUMBER (ctunnel
// 49 rows, sing-box 16, hysteria 2, easytier 1, waterwall 18 — all ints),
// which the old `ID string` decode rejected wholesale — the join silently
// yielded zero rows for every core. The decode is now id-shape-agnostic,
// and the board's method id is the manifest's `name` — the DRIVER-
// consumable spelling (the ctunnel driver's ctMethods keys are the names:
// "6to4", "6to4-gre", …; the numeric id is the manifest's own row number
// and no driver accepts it). A manifest without `name` falls back to the
// verbatim id text. The row's Engine is the ENGINE id the agents report
// (coresEngineOfCore: the sing-box core drives engine "singbox"), never
// the repo folder name — a probe tunnel for an unreported engine id can
// never converge.
func coresManifestMethods(core string) []methodManifestEntry {
	entry := fetchCoresManifestCached(core)
	if entry == nil || entry.Err != nil || len(entry.Methods) == 0 {
		return nil
	}
	var methods []struct {
		ID        json.RawMessage `json:"id"`
		Name      string          `json:"name"`
		Layer     string          `json:"layer"`
		Transport string          `json:"transport"`
		IPVer     string          `json:"ipver"`
	}
	if err := json.Unmarshal(entry.Methods, &methods); err != nil {
		return nil
	}
	engine := coresEngineOfCore(core)
	out := make([]methodManifestEntry, 0, len(methods))
	for _, m := range methods {
		id := manifestMethodIDText(m.ID)
		if m.Name != "" {
			id = m.Name
		}
		if id == "" {
			continue
		}
		out = append(out, methodManifestEntry{ID: id, Engine: engine,
			Layer: m.Layer, Transport: m.Transport, IPVer: m.IPVer})
	}
	return out
}

// manifestMethodIDText renders a manifest method id's raw JSON as plain
// text: a JSON string unquotes, a number keeps its literal digits,
// anything else (objects/arrays) yields "" — no invented ids.
func manifestMethodIDText(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if len(trimmed) > 1 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return ""
		}
		return s
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return n.String()
	}
	return ""
}

// coresManifestMethodEntry is the method-board join's reverse lookup
// (method id → manifest entry): scan the manifests of every core the
// release feed knows (the feed cache answers locally; a manifest miss is
// a cached no-op), then the manifest cache itself as the fallback for a
// temporarily unreachable feed.
//
// BACKEND-46: the scan is SORTED-CORE order, not the feed's order. The
// release list arrives in GitHub's order, and the real landed manifests
// publish the SAME method id from several cores ("hysteria2" in hysteria
// AND sing-box; "vless"/"vmess"/"trojan"/"wireguard" in sing-box AND
// waterwall AND ctunnel) — a feed-order scan resolves those ids to
// different engines run to run. The forward join (methodBoardCandidates)
// dedups in sorted-agreed-engine order, whose live alias map sorts the
// same way, so sorted-core order here elects the same winner the
// enumeration kept and the board's identity columns stay deterministic.
func coresManifestMethodEntry(method string) (methodManifestEntry, bool) {
	rels, err := fetchCoresReleasesCached()
	if err == nil {
		cores := make([]string, 0, len(rels))
		seen := map[string]bool{}
		for i := range rels {
			core, _, ok := coresTagParts(rels[i].Tag)
			if !ok || seen[core] {
				continue
			}
			seen[core] = true
			cores = append(cores, core)
		}
		sort.Strings(cores)
		for _, c := range cores {
			for _, e := range coresManifestMethods(c) {
				if e.ID == method {
					return e, true
				}
			}
		}
	}
	coresManifestMu.Lock()
	cacheCores := make([]string, 0, len(coresManifestCache))
	for c := range coresManifestCache {
		cacheCores = append(cacheCores, c)
	}
	coresManifestMu.Unlock()
	sort.Strings(cacheCores)
	for _, c := range cacheCores {
		for _, e := range coresManifestMethods(c) {
			if e.ID == method {
				return e, true
			}
		}
	}
	return methodManifestEntry{}, false
}

// ---- installed_version / outdated (§2.22 addendum) ------------------------

// coresVersionCompare is the §2.20 comparison RULES over the cores' own
// version shape: the feed publishes digits+dots versions (2 or 3 numeric
// segments — semver AND the CALVER form), and BACKEND-47 added naive's
// four numeric segments + `-<build>` suffix (naive-v154.0.8037.49-5), so
// the compare is segment-wise numeric with zero padding, then the build
// number. A non-parsable version never claims newer: it compares equal
// (outdated stays null/false rather than guessing).
func coresVersionCompare(a, b string) int {
	na, oka := coresVersionSegs(a)
	nb, okb := coresVersionSegs(b)
	if !oka || !okb {
		return 0
	}
	for i := 0; i < 5; i++ {
		if na[i] != nb[i] {
			if na[i] < nb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// coresVersionSegs parses up to four numeric segments plus an optional
// trailing `-<build>` (naive). ok=false = outside the cores contract's
// shape (treated as all-zeros by the comparison).
func coresVersionSegs(v string) ([5]int, bool) {
	var out [5]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.LastIndexByte(v, '-'); i > 0 {
		n, err := strconv.Atoi(v[i+1:])
		if err != nil {
			return out, false
		}
		out[4] = n
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) > 4 {
		return out, false
	}
	for i, seg := range parts {
		n, err := strconv.Atoi(seg)
		if err != nil {
			return [5]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// coresInstalledVersions answers, for one core, the newest version any
// enrolled agent reported installed ("" = no agent reported a version for
// it). Version source preference: the engine_status row the agent's
// heartbeats carry (store.AgentEngines — the "now" view), falling back to
// the hello engine-catalog version (AgentV2.EngineCatalog).
func coresInstalledVersions(s *store.Store, core string) string {
	ags, err := s.ListAgents()
	if err != nil {
		return ""
	}
	best := ""
	compare := func(v string) {
		if v == "" {
			return
		}
		if best == "" || coresVersionCompare(v, best) > 0 {
			best = v
		}
	}
	for _, a := range ags {
		if rows, err := s.AgentEngines(a.ID); err == nil {
			for _, r := range rows {
				if r.Engine == core && r.Installed {
					compare(r.Version)
				}
			}
		}
		for _, e := range agentHelloCatalogVersions(s, a.ID) {
			if e.Engine == core {
				compare(e.Version)
			}
		}
	}
	return best
}

// agentHelloCatalogVersions decodes the agent's stored hello engine
// catalog ([{id, version?, params_schema?}] — the shape recordEngineCatalog
// persists).
func agentHelloCatalogVersions(s *store.Store, agentID string) []struct{ Engine, Version string } {
	v2, err := s.AgentV2(agentID)
	if err != nil || v2.EngineCatalog == "" {
		return nil
	}
	var catalog []struct {
		ID      string `json:"id"`
		Version string `json:"version,omitempty"`
	}
	if json.Unmarshal([]byte(v2.EngineCatalog), &catalog) != nil {
		return nil
	}
	out := make([]struct{ Engine, Version string }, 0, len(catalog))
	for _, c := range catalog {
		if c.ID != "" {
			out = append(out, struct{ Engine, Version string }{Engine: c.ID, Version: c.Version})
		}
	}
	return out
}

// coreRowInstalledOutdated fills the additive installed_version/outdated
// pair for one core row (latestVersion = the row's own latest_version; ""
// = unknown). Agents report ENGINE ids while the rows are CORE names —
// both spellings are checked through the alias map. outdated is null when
// either side is unknown (the §2.20 honest-unknown rule).
func coreRowInstalledOutdated(s *store.Store, core, latestVersion string) (string, *bool) {
	installed := coresInstalledVersions(s, core)
	if installed == "" {
		// agents report ENGINE ids while the rows are CORE names — check
		// the aliased spelling both ways (wstunnel row ↔ wst reports)
		if engine := coresEngineOfCore(core); engine != core {
			installed = coresInstalledVersions(s, engine)
		}
	}
	if installed == "" {
		return "", nil
	}
	if latestVersion == "" {
		return installed, nil
	}
	outdated := coresVersionCompare(installed, latestVersion) < 0
	return installed, &outdated
}

// wireCoresManifestJoin connects the manifest cache to the method-probe
// board's seams (called once at route-registration time — the seams'
// zero defaults keep an unwired build honest).
func wireCoresManifestJoin() {
	mboardManifestMethodsFn = func(core string) []methodManifestEntry {
		return coresManifestMethods(coresCoreOfEngine(core))
	}
	mboardManifestEntryFn = coresManifestMethodEntry
}
