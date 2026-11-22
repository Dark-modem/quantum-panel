// phase46_board_completion_test.go — BACKEND-46: hermetic verification that
// the §2.23 method-board enumerates ALL the landed engines' methods and the
// §2.22 cores/available feed serves the NEW cores (hysteria, sing-box,
// waterwall, easytier) correctly — driven by the REAL landed manifest bytes
// (phase46_real_manifests_test.go, quantum-cores @ 741ba974).
//
// Found + fixed here (the verification's two real gaps):
//   - engine-row hijack: the manifests publish methods literally named
//     "hysteria" (sing-box block) and "wireguard" (ctunnel, sing-box AND
//     waterwall blocks) — panel ENGINE ids. The old probeOne/unprobedRows
//     asked the manifest join FIRST, so the engine rows' probe tunnels were
//     created under the manifest method's engine with a {"method": id} param
//     that engine's driver never consumes. Engine ids now resolve as engine
//     rows FIRST (mboardEngineRowID), matching the candidates join's
//     documented "a manifest method named like an engine never shadows it".
//   - feed-order nondeterminism: the reverse lookup scanned the release
//     feed in GitHub's order while the forward join dedups in sorted order —
//     duplicated ids across cores ("hysteria2" in hysteria AND sing-box;
//     "vless"/"vmess"/"trojan"/"wireguard" in sing-box AND waterwall AND
//     ctunnel) resolved to different engines run to run. The reverse lookup
//     now scans sorted-core order (the same winner the forward join kept).

package panel

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// phase46FeedFixture swaps the release feed for the REAL landed tags and
// returns nothing. order controls the feed's order on purpose: the real
// GitHub feed is NOT sorted, and the reverse lookup must not care.
func phase46FeedFixture(t *testing.T, order string) {
	t.Helper()
	realTags := []ghRelease{
		{Tag: "ctunnel-v2.0.0", PublishedAt: "2026-11-22T00:00:00Z"},
		{Tag: "easytier-v2.7.1", PublishedAt: "2026-11-22T00:00:00Z"},
		{Tag: "hysteria-v2.14.0", PublishedAt: "2026-11-22T00:00:00Z"},
		{Tag: "sing-box-v1.14.4", PublishedAt: "2026-11-22T00:00:00Z"},
		{Tag: "waterwall-v1.6.0", PublishedAt: "2026-11-22T00:00:00Z"},
		{Tag: "xray-v24.12.31", PublishedAt: "2026-11-22T00:00:00Z"},
		{Tag: "xray-v26.3.27", PublishedAt: "2026-11-23T00:00:00Z"},
		{Tag: "udp2raw-v20230206.0", PublishedAt: "2026-11-22T00:00:00Z"},
	}
	if order == "feed" {
		// GitHub's order is its own — deliberate NON-sorted fixture (the
		// waterwall/sing-box/ctunnel relative order differs from the sorted
		// scan, so an order-dependent lookup fails this test)
		realTags = []ghRelease{
			{Tag: "waterwall-v1.6.0", PublishedAt: "2026-11-22T00:00:00Z"},
			{Tag: "xray-v26.3.27", PublishedAt: "2026-11-23T00:00:00Z"},
			{Tag: "sing-box-v1.14.4", PublishedAt: "2026-11-22T00:00:00Z"},
			{Tag: "udp2raw-v20230206.0", PublishedAt: "2026-11-22T00:00:00Z"},
			{Tag: "ctunnel-v2.0.0", PublishedAt: "2026-11-22T00:00:00Z"},
			{Tag: "hysteria-v2.14.0", PublishedAt: "2026-11-22T00:00:00Z"},
			{Tag: "xray-v24.12.31", PublishedAt: "2026-11-22T00:00:00Z"},
			{Tag: "easytier-v2.7.1", PublishedAt: "2026-11-22T00:00:00Z"},
		}
	}
	oldFeed := coresReleasesFetcher
	t.Cleanup(func() { coresReleasesFetcher = oldFeed })
	coresReleasesFetcher = func() ([]ghRelease, error) { return realTags, nil }
	// the release cache must not survive between fixtures/tests
	coresRelMu.Lock()
	coresRelCache, coresRelErr, coresRelAt = nil, nil, time.Time{}
	coresRelMu.Unlock()
	t.Cleanup(func() {
		coresRelMu.Lock()
		coresRelCache, coresRelErr, coresRelAt = nil, nil, time.Time{}
		coresRelMu.Unlock()
	})
}

// phase46WireJoin re-wires the board seams after the fixture swap (the
// closures read the swapped fetchers through the caches).
func phase46WireJoin(t *testing.T) {
	t.Helper()
	wireCoresManifestJoin()
	t.Cleanup(func() {
		mboardManifestMethodsFn = func(core string) []methodManifestEntry { return nil }
		mboardManifestEntryFn = func(method string) (methodManifestEntry, bool) {
			return methodManifestEntry{}, false
		}
	})
}

// phase46WaitEntry polls the reverse lookup until the background manifest
// fill lands for every methods[]-publishing core (the seam is in-memory).
func phase46WaitEntry(t *testing.T, method string) methodManifestEntry {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if e, ok := mboardManifestEntryOf(method); ok {
			return e
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("reverse lookup for %q never landed", method)
	return methodManifestEntry{}
}

// TestPhase46BoardEnumeratesAllLandedManifestMethods drives the REAL five
// manifests through the full join: 26 engine rows + every manifest method
// the agreed cores publish, deduped deterministically.
func TestPhase46BoardEnumeratesAllLandedManifestMethods(t *testing.T) {
	phase46RealManifestFixture(t)
	phase46FeedFixture(t, "feed") // deliberately unsorted
	phase46WireJoin(t)

	// the catalog-agreed set: the four engines whose drivers landed (AGENT-29
	// ctunnel, AGENT-31 hysteria+singbox, OVERNIGHT-A5 waterwall in flight)
	// plus gost (a landed engine whose core publishes NO methods[])
	catalog := []string{"matrix", "ctunnel", "hysteria", "singbox", "waterwall", "gost"}

	// the manifest reads are background-filled — wait for the real fills to
	// land before enumerating (a GET must never serialize behind them, so
	// the join answers empty until the cache warms; the phase45 poll pattern)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if rows := coresManifestMethods("ctunnel"); len(rows) == 49 &&
			len(coresManifestMethods("sing-box")) == 16 &&
			len(coresManifestMethods("waterwall")) == 18 &&
			len(coresManifestMethods("hysteria")) == 2 &&
			len(coresManifestMethods("easytier")) == 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if rows := coresManifestMethods("ctunnel"); len(rows) != 49 {
		t.Fatalf("ctunnel manifest never filled: %d rows", len(rows))
	}

	got := methodBoardCandidates(catalog, catalog)

	// the engine rows come first, uiKnownEngines order, exactly once each
	if len(got) < len(uiKnownEngines) {
		t.Fatalf("candidates %d < engine set %d", len(got), len(uiKnownEngines))
	}
	for i, e := range uiKnownEngines {
		if got[i] != e {
			t.Fatalf("engine row drift at %d: %q vs %q", i, got[i], e)
		}
	}
	manifestRows := got[len(uiKnownEngines):]
	// ctunnel is NOT a uiKnownEngines id (the C-Tunnel engine rides the cores
	// catalog) — it lands as the catalog-agreed extra engine row FIRST, then
	// the manifest methods: ctunnel 49−1 (its own "wireguard" row is shadowed
	// by the kernel-WG ENGINE row — the designed precedence) + hysteria 2 +
	// sing-box 16−3 (hysteria2/hysteria/wireguard seen) + waterwall 18−4
	// (trojan/vless/vmess/wireguard seen)
	if len(manifestRows) == 0 || manifestRows[0] != "ctunnel" {
		t.Fatalf("ctunnel extra engine row missing/first: %v", manifestRows)
	}
	if want := 1 + 48 + 2 + 13 + 14; len(manifestRows) != want {
		t.Fatalf("manifest rows %d, want %d: %v", len(manifestRows), want, manifestRows)
	}
	// every landed ctunnel method enumerates (all 49 — the BACKEND-45 test
	// only drove a 3-row slice)
	ctManifest := coresManifestMethods("ctunnel")
	if len(ctManifest) != 49 {
		t.Fatalf("ctunnel manifest rows %d, want 49", len(ctManifest))
	}
	seen := map[string]bool{}
	for _, m := range got {
		if seen[m] {
			t.Fatalf("duplicate candidate %q", m)
		}
		seen[m] = true
	}
	for _, e := range ctManifest {
		if !seen[e.ID] {
			t.Fatalf("landed ctunnel method %q missing from the board", e.ID)
		}
	}
	for _, want := range []string{"salamander", "vless", "vmess", "trojan", "shadowsocks",
		"tuic", "anytls", "shadowtls", "snell", "socks-http", "openvpn", "tun", "tailscale",
		"quic", "kcp", "enet", "reality", "rtmp", "gns", "bgp4", "http", "socks5",
		"fake-tcp", "obfuscator", "connection-fisher", "connection-stripe", "mux"} {
		if !seen[want] {
			t.Fatalf("landed manifest method %q missing from the board", want)
		}
	}
	// engine ids appear EXACTLY once even though manifests publish methods
	// with those literal names
	for _, engine := range []string{"hysteria", "wireguard", "singbox", "waterwall", "ctunnel"} {
		n := 0
		for _, m := range got {
			if m == engine {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("engine id %q appears %d times, want 1", engine, n)
		}
	}
	// easytier never agreed (no driver yet) — its manifest method stays off
	// the board (honest: a probe tunnel for an unreported engine id can
	// never converge)
	if seen["easytier-mesh"] {
		t.Fatalf("easytier-mesh enumerated without catalog agreement")
	}

	// the reverse lookup answers the SAME winner the forward join kept,
	// regardless of the feed's order
	if e := phase46WaitEntry(t, "hysteria2"); e.Engine != "hysteria" {
		t.Fatalf("hysteria2 must ride the hysteria core (sorted-first), got %q", e.Engine)
	}
	if e := phase46WaitEntry(t, "vless"); e.Engine != "singbox" {
		t.Fatalf("vless must ride singbox, got %q", e.Engine)
	}
	if e := phase46WaitEntry(t, "quic"); e.Engine != "waterwall" {
		t.Fatalf("quic must ride waterwall, got %q", e.Engine)
	}
	if e := phase46WaitEntry(t, "6to4"); e.Engine != "ctunnel" || e.Layer != "L3" ||
		e.Transport != "raw-ip" || e.IPVer != "v4" {
		t.Fatalf("6to4 identity: %+v", e)
	}
}

// TestPhase46EngineRowsSurviveManifestNameCollisions pins the engine-first
// identity rule at the probe level: the "hysteria"/"wireguard" engine rows
// probe AS THEMSELVES with {} params (the manifests publish methods with
// those literal names), while a true manifest method probes under its core's
// engine with the method param.
func TestPhase46EngineRowsSurviveManifestNameCollisions(t *testing.T) {
	phase46RealManifestFixture(t)
	phase46FeedFixture(t, "feed")
	phase46WireJoin(t)

	st, _ := mboardProbeFixture(t)
	st.methods = []string{"hysteria", "wireguard", "vless"}
	// the manifest fill is background — wait for the reverse lookup before
	// probing so the params decision reads filled manifests
	phase46WaitEntry(t, "vless")

	// the actual probe tunnels: engine rows get {} (their driver's default
	// shape), manifest rows get the method param. probeOne's cleanup deletes
	// the tunnel on every exit path — assert MID-PROBE (the tunnel lives for
	// the converge window) while the probe runs in a goroutine.
	assertProbeTunnel := func(method, engine, params string) {
		t.Helper()
		done := make(chan struct{})
		go func() {
			defer close(done)
			st.probeOne(method)
		}()
		var found *store.Tunnel
		deadline := time.Now().Add(1500 * time.Millisecond) // inside the 2s converge window
		for time.Now().Before(deadline) {
			tuns, err := st.store.ListTunnels(st.topoID)
			if err == nil {
				for i := range tuns {
					if strings.HasPrefix(tuns[i].TunnelKey, "mprobe-"+method+"-") {
						found = &tuns[i]
					}
				}
			}
			if found != nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if found == nil {
			t.Fatalf("no %s probe tunnel appeared", method)
		}
		if found.Engine != engine || found.EngineParams != params {
			t.Fatalf("%s probe tunnel: Engine %q params %q, want %q / %q",
				method, found.Engine, found.EngineParams, engine, params)
		}
		<-done
	}
	assertProbeTunnel("hysteria", "hysteria", "{}")
	assertProbeTunnel("wireguard", "wireguard", "{}")
	assertProbeTunnel("vless", "singbox", `{"method":"vless"}`)

	rows := map[string]methodBoardRow{}
	for _, r := range st.rows {
		rows[r.Method] = r
	}
	hys, ok := rows["hysteria"]
	if !ok {
		t.Fatalf("no hysteria row: %+v", st.rows)
	}
	if hys.Engine != "hysteria" || hys.Layer != "l4" || hys.Transport != "udp" || hys.IPVer != "v4+v6" {
		t.Fatalf("hysteria engine row hijacked: %+v", hys)
	}
	wg, ok := rows["wireguard"]
	if !ok {
		t.Fatalf("no wireguard row: %+v", st.rows)
	}
	if wg.Engine != "wireguard" || wg.Layer != "l3" || wg.Transport != "udp" || wg.IPVer != "v4+v6" {
		t.Fatalf("wireguard engine row hijacked: %+v", wg)
	}
	vl, ok := rows["vless"]
	if !ok {
		t.Fatalf("no vless row: %+v", st.rows)
	}
	if vl.Engine != "singbox" || vl.Layer != "l4" || vl.Transport != "tcp" || vl.IPVer != "both" {
		t.Fatalf("vless manifest row identity: %+v", vl)
	}

	// the unprobed render obeys the same precedence
	unprobed := unprobedRows([]string{"hysteria", "wireguard", "vless", "quic"})
	if len(unprobed) != 4 {
		t.Fatalf("unprobed rows %d", len(unprobed))
	}
	want := map[string]methodBoardRow{}
	for _, r := range unprobed {
		want[r.Method] = r
	}
	if r := want["hysteria"]; r.Engine != "hysteria" || r.Layer != "l4" || r.Reachability != "unprobed" {
		t.Fatalf("unprobed hysteria: %+v", r)
	}
	if r := want["wireguard"]; r.Engine != "wireguard" || r.Layer != "l3" {
		t.Fatalf("unprobed wireguard: %+v", r)
	}
	if r := want["vless"]; r.Engine != "singbox" || r.Layer != "l4" || r.Transport != "tcp" {
		t.Fatalf("unprobed vless: %+v", r)
	}
	if r := want["quic"]; r.Engine != "waterwall" || r.Layer != "l4" || r.Transport != "udp" || r.IPVer != "both" {
		t.Fatalf("unprobed quic: %+v", r)
	}
}

// TestPhase46ReverseLookupDeterministicAcrossFeedOrders pins the sorted-core
// reverse lookup: duplicated method ids resolve to the same engine whatever
// order GitHub lists the releases in (pre-fix this flipped with the feed).
func TestPhase46ReverseLookupDeterministicAcrossFeedOrders(t *testing.T) {
	answer := func() map[string]methodManifestEntry {
		out := map[string]methodManifestEntry{}
		for _, m := range []string{"hysteria2", "vless", "vmess", "trojan", "wireguard", "salamander", "quic"} {
			out[m] = phase46WaitEntry(t, m)
		}
		return out
	}
	var first map[string]methodManifestEntry
	for _, order := range []string{"feed", "sorted", "feed"} {
		phase46RealManifestFixture(t)
		phase46FeedFixture(t, order)
		phase46WireJoin(t)
		got := answer()
		if first == nil {
			first = got
			continue
		}
		for m, e := range got {
			if e.Engine != first[m].Engine || e.Layer != first[m].Layer {
				t.Fatalf("order %q: %q resolved to %+v, first resolution %+v", order, m, e, first[m])
			}
		}
	}
	// and the resolution matches the forward join's dedup winner
	if first["hysteria2"].Engine != "hysteria" || first["vless"].Engine != "singbox" ||
		first["trojan"].Engine != "singbox" || first["quic"].Engine != "waterwall" ||
		first["salamander"].Engine != "hysteria" {
		t.Fatalf("duplicated-id resolution drifted: %+v", first)
	}
	// "wireguard" IS an engine id — the board never probes it as a manifest
	// method (mboardEngineRowID), and the raw lookup stays deterministic
	// (sorted cores: ctunnel publishes it first)
	if first["wireguard"].Engine != "ctunnel" {
		t.Fatalf("raw wireguard lookup: %+v", first["wireguard"])
	}
}

// TestPhase46NewCoresFeedEndToEnd is the §2.22 verification: the four NEW
// cores serve methods[]/versions through /api/cores/available with the REAL
// manifests and tags, and the per-core installed_version/outdated resolves
// for them (sing-box through the alias; hysteria/waterwall direct;
// easytier honest-empty — no driver reports it yet).
func TestPhase46NewCoresFeedEndToEnd(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	phase46RealManifestFixture(t)
	phase46FeedFixture(t, "sorted")

	// enrolled agents' reported engine versions (the heartbeat engine_status
	// rows): singbox via the ALIAS spelling, hysteria 2.13.0 (one behind),
	// waterwall 1.6.0 (current), ctunnel 2.0.0 (current); easytier: nothing
	if err := s.UpsertAgent(store.Agent{ID: "agt_1a1a1a1a1a1a", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgent(store.Agent{ID: "agt_2b2b2b2b2b2b", Name: "b"}); err != nil {
		t.Fatal(err)
	}
	seed := []store.AgentEngine{
		{AgentID: "agt_1a1a1a1a1a1a", Engine: "singbox", Installed: true, Version: "1.14.4"},
		{AgentID: "agt_1a1a1a1a1a1a", Engine: "hysteria", Installed: true, Version: "2.13.0"},
		{AgentID: "agt_2b2b2b2b2b2b", Engine: "waterwall", Installed: true, Version: "1.6.0"},
		{AgentID: "agt_2b2b2b2b2b2b", Engine: "ctunnel", Installed: true, Version: "2.0.0"},
	}
	for _, e := range seed {
		e.UpdatedAt = time.Now().Unix()
		if err := s.UpsertAgentEngine(e); err != nil {
			t.Fatal(err)
		}
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
	decode := func() {
		w = httptest.NewRecorder()
		handleCoresAvailable(w, r)
		if w.Code != 200 {
			t.Fatalf("cores available: %d", w.Code)
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	decode()
	// the manifest reads are background-filled — poll until every
	// methods[]-publishing core carries its rows
	deadline := time.Now().Add(3 * time.Second)
	count := func(core string) int {
		for _, c := range resp.Cores {
			if c.Core == core {
				if c.Methods == nil || string(c.Methods) == "null" {
					return -1
				}
				var rows []json.RawMessage
				if json.Unmarshal(c.Methods, &rows) != nil {
					return -1
				}
				return len(rows)
			}
		}
		return -2
	}
	for time.Now().Before(deadline) {
		if count("ctunnel") == 49 && count("sing-box") == 16 && count("hysteria") == 2 &&
			count("waterwall") == 18 && count("easytier") == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
		decode()
	}
	if count("ctunnel") != 49 || count("sing-box") != 16 || count("hysteria") != 2 ||
		count("waterwall") != 18 || count("easytier") != 1 {
		t.Fatalf("methods[] counts: ctunnel=%d sing-box=%d hysteria=%d waterwall=%d easytier=%d",
			count("ctunnel"), count("sing-box"), count("hysteria"), count("waterwall"), count("easytier"))
	}
	row := func(core string) *coreRow {
		for i := range resp.Cores {
			if resp.Cores[i].Core == core {
				return &resp.Cores[i]
			}
		}
		t.Fatalf("no %s row", core)
		return nil
	}
	// the passthrough is VERBATIM: the served methods[] bytes equal the
	// manifest's own methods[] block
	for core, body := range map[string]string{
		"ctunnel": phase46Manifestctunnel, "sing-box": phase46Manifestsingbox,
		"hysteria": phase46Manifesthysteria, "waterwall": phase46Manifestwaterwall,
		"easytier": phase46Manifesteasytier,
	} {
		var m struct {
			Methods json.RawMessage `json:"methods"`
		}
		if err := json.Unmarshal([]byte(body), &m); err != nil {
			t.Fatal(err)
		}
		var a, b []any
		if json.Unmarshal(m.Methods, &a) != nil || json.Unmarshal(row(core).Methods, &b) != nil {
			t.Fatalf("%s methods decode", core)
		}
		aj, _ := json.Marshal(a)
		bj, _ := json.Marshal(b)
		if string(aj) != string(bj) {
			t.Fatalf("%s passthrough altered", core)
		}
	}
	// latest_version from the REAL tags (xray: the newest of its two tags)
	if x := row("xray"); x.LatestVersion != "26.3.27" || x.Methods != nil && string(x.Methods) != "null" {
		t.Fatalf("xray row: latest %q methods %s", x.LatestVersion, x.Methods)
	}
	if u := row("udp2raw"); u.LatestVersion != "20230206.0" {
		t.Fatalf("udp2raw calver latest %q", u.LatestVersion)
	}
	// installed/outdated per core: sing-box via the alias, hysteria one
	// behind, waterwall current, ctunnel current, easytier honest-empty
	if sb := row("sing-box"); sb.InstalledVersion != "1.14.4" || sb.Outdated == nil || *sb.Outdated {
		t.Fatalf("sing-box installed/outdated: %q %v", sb.InstalledVersion, sb.Outdated)
	}
	if h := row("hysteria"); h.InstalledVersion != "2.13.0" || h.Outdated == nil || !*h.Outdated {
		t.Fatalf("hysteria installed/outdated: %q %v", h.InstalledVersion, h.Outdated)
	}
	if ww := row("waterwall"); ww.InstalledVersion != "1.6.0" || ww.Outdated == nil || *ww.Outdated {
		t.Fatalf("waterwall installed/outdated: %q %v", ww.InstalledVersion, ww.Outdated)
	}
	if ct := row("ctunnel"); ct.InstalledVersion != "2.0.0" || ct.Outdated == nil || *ct.Outdated {
		t.Fatalf("ctunnel installed/outdated: %q %v", ct.InstalledVersion, ct.Outdated)
	}
	if et := row("easytier"); et.InstalledVersion != "" || et.Outdated != nil {
		t.Fatalf("easytier must stay honest-empty: %q %v", et.InstalledVersion, et.Outdated)
	}
}
