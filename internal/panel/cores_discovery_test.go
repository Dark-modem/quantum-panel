// cores_discovery_test.go — BACKEND-38 tests (contract §2.22). Fully
// hermetic: the cores release feed arrives through the swapped
// coresReleasesFetcher seam and the asset bytes through
// coresAssetDownloader — no network, no real GitHub, no real repo. The
// battery covers the discovery shapes (success / partial / empty /
// unreachable), the §2.20 cache mechanics (10-min success / 1-min failure
// / 30-s refresh throttle incl. hostile ?refresh values), and the
// artifacts-store repo source: download → verify sha256+ed25519 → store,
// with the tampered-asset ordering proof (refused BEFORE anything is
// stored) and the M2 invariants (nothing stored/served unverified; the
// agent-facing pins ride the manifest unchanged).

package panel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeCoresReleases is the mocked quantum-cores feed: the 9 contract
// cores (matrix is EXEMPT — no releases by design), an older frp release
// that must lose the latest pick, a draft that must be ignored, a tag not
// in the contract shape, and sig companion assets that must not surface.
func fakeCoresReleases() []ghRelease {
	rel := func(tag string, assets ...ghAsset) ghRelease {
		return ghRelease{Tag: tag, PublishedAt: "2026-11-21T00:00:00Z", Assets: assets}
	}
	asset := func(name string, size int64) ghAsset {
		return ghAsset{
			Name: name, Size: size,
			BrowserDownloadURL: "https://github.com/Dark-modem/quantum-cores/releases/download/x/" + name,
		}
	}
	return []ghRelease{
		rel("frp-v0.61.2",
			asset("frp-linux-amd64.tar.gz", 13000000),
			asset("frp-linux-arm64.tar.gz", 12100000),
			asset("frp-linux-amd64.tar.gz.sig", 64),
			asset("checksums.txt", 512),
		),
		rel("frp-v0.61.1",
			asset("frp-linux-amd64.tar.gz", 12900000),
			asset("frp-linux-arm64.tar.gz", 12000000),
			asset("frp-linux-386.tar.gz", 11000000),
		), // older — must lose
		{Tag: "frp-v0.99.0", Draft: true, Assets: []ghAsset{asset("frp-linux-amd64.tar.gz", 1)}}, // draft — must lose even at the top
		rel("xray-v25.1.30",
			asset("xray-linux-amd64.tar.gz", 24000000),
			asset("xray-linux-arm64.tar.gz", 23000000),
			asset("xray-linux-arm.tar.gz", 22000000),
			asset("xray-linux-386.tar.gz", 21000000),
		),
		rel("backhaul-v0.9.0", asset("backhaul-linux-amd64.tar.gz", 9000000)),
		rel("rathole-v0.5.0", asset("rathole-linux-amd64.tar.gz", 3000000)),
		rel("gost-v3.0.0", asset("gost-linux-amd64.tar.gz", 11000000)),
		rel("chisel-v1.10.1", asset("chisel-linux-amd64.tar.gz", 8000000)),
		rel("wstunnel-v10.1.4", asset("wstunnel-linux-amd64.tar.gz", 15000000)),
		rel("udp2raw-v20230206.0", asset("udp2raw-linux-amd64.tar.gz", 700000)), // REAL repo shape: calver, 2 numeric segments
		rel("ctunnel-v0.4.2", asset("ctunnel-linux-amd64.tar.gz", 2500000)),
		rel("not-a-core-tag"),         // wrong shape → ignored
		{Tag: "draft-x", Draft: true}, // draft → ignored
	}
}

// swapCoresSeams swaps the cores feed + downloader seams and zeroes the
// cache so every test sees its own feed. A nil downloader gets a
// fail-closed stub (tests that should never download fail loudly).
func swapCoresSeams(t *testing.T, rels []ghRelease, relErr error, dl func(url string, f *os.File) error) {
	t.Helper()
	of, od := coresReleasesFetcher, coresAssetDownloader
	coresReleasesFetcher = func() ([]ghRelease, error) { return rels, relErr }
	if dl == nil {
		dl = func(url string, f *os.File) error {
			t.Errorf("unexpected asset download: %s", url)
			return errors.New("no downloads expected")
		}
	}
	coresAssetDownloader = dl
	t.Cleanup(func() { coresReleasesFetcher, coresAssetDownloader = of, od })

	coresRelMu.Lock()
	coresRelCache, coresRelErr, coresRelAt = nil, nil, time.Time{}
	coresRelMu.Unlock()
	t.Cleanup(func() {
		coresRelMu.Lock()
		coresRelCache, coresRelErr, coresRelAt = nil, nil, time.Time{}
		coresRelMu.Unlock()
	})
}

// coresAvailable GETs the discovery handler and decodes the response.
func coresAvailable(t *testing.T, query string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/cores/available"+query, nil)
	w := httptest.NewRecorder()
	handleCoresAvailable(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// TestCoresAvailableDiscovery: the 9 contract cores (matrix exempt —
// absent), newest tag wins, sig companions and non-artifacts excluded,
// os/arch parsed from the asset names, checked_at present.
func TestCoresAvailableDiscovery(t *testing.T) {
	swapCoresSeams(t, fakeCoresReleases(), nil, nil)
	code, out := coresAvailable(t, "")
	if code != 200 {
		t.Fatalf("status %d: %v", code, out)
	}
	if _, has := out["error"]; has {
		t.Fatalf("unexpected error field: %v", out["error"])
	}
	if _, has := out["checked_at"]; !has {
		t.Fatalf("checked_at missing: %v", out)
	}
	cores, ok := out["cores"].([]any)
	if !ok || len(cores) != 9 {
		t.Fatalf("cores = %v (want 9 rows, matrix exempt)", out["cores"])
	}
	// Rows are sorted by core name: backhaul first, xray last.
	first, last := cores[0].(map[string]any), cores[len(cores)-1].(map[string]any)
	if first["core"] != "backhaul" || last["core"] != "xray" {
		t.Fatalf("row order wrong: first=%v last=%v", first["core"], last["core"])
	}
	for _, c := range cores {
		if c.(map[string]any)["core"] == "matrix" {
			t.Fatal("matrix must be absent (in-process, no binary)")
		}
	}
	// frp row: newest tag wins, latest_version carries no leading "v".
	var frp map[string]any
	for _, c := range cores {
		if m := c.(map[string]any); m["core"] == "frp" {
			frp = m
		}
	}
	if frp["latest_tag"] != "frp-v0.61.2" || frp["latest_version"] != "0.61.2" {
		t.Fatalf("frp pick wrong: %v", frp)
	}
	if frp["published_at"] != "2026-11-21T00:00:00Z" {
		t.Fatalf("published_at wrong: %v", frp)
	}
	assets := frp["assets"].([]any)
	if len(assets) != 2 {
		t.Fatalf("frp assets = %v (want the 2 tar.gz only: no .sig, no checksums)", assets)
	}
	a0 := assets[0].(map[string]any)
	if a0["name"] != "frp-linux-amd64.tar.gz" || a0["os"] != "linux" || a0["arch"] != "amd64" || a0["size"].(float64) != 13000000 {
		t.Fatalf("frp asset row wrong: %v", a0)
	}
	if !strings.HasPrefix(a0["url"].(string), "https://github.com/Dark-modem/quantum-cores/releases/download/") {
		t.Fatalf("asset url wrong: %v", a0["url"])
	}
	// The REAL udp2raw calver shape surfaces with its version intact.
	for _, c := range cores {
		m := c.(map[string]any)
		if m["core"] == "udp2raw" && m["latest_tag"] != "udp2raw-v20230206.0" {
			t.Fatalf("udp2raw calver tag lost: %v", m)
		}
	}
	// xray covers all four store arch enums.
	for _, c := range cores {
		m := c.(map[string]any)
		if m["core"] != "xray" {
			continue
		}
		if n := len(m["assets"].([]any)); n != 4 {
			t.Fatalf("xray assets = %d (want amd64/arm64/arm/386)", n)
		}
	}
}

// TestCoresAvailableUnreachable: the feed fails → cores null + the error
// field, and checked_at is NOT dated to the failure.
func TestCoresAvailableUnreachable(t *testing.T) {
	swapCoresSeams(t, nil, errors.New("github api: 403 — the API rate limit was hit"), nil)
	code, out := coresAvailable(t, "")
	if code != 200 {
		t.Fatalf("status %d (honest partial must still be 200)", code)
	}
	if v, has := out["cores"]; !has || v != nil {
		t.Fatalf("cores must be null when unreachable: %v", v)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "403") {
		t.Fatalf("error field wrong: %v", out["error"])
	}
	if _, has := out["checked_at"]; has {
		t.Fatalf("checked_at must be absent after a failed fetch: %v", out)
	}
}

// TestCoresAvailableEmptyRepo: an empty feed (and a feed with no
// contract-shaped tags) answers cores null with NO error — fetch
// succeeded, there is simply nothing there.
func TestCoresAvailableEmptyRepo(t *testing.T) {
	swapCoresSeams(t, []ghRelease{}, nil, nil)
	_, out := coresAvailable(t, "")
	if v, has := out["cores"]; !has || v != nil {
		t.Fatalf("cores must be null for an empty repo: %v", v)
	}
	if _, has := out["error"]; has {
		t.Fatalf("empty repo is not an error: %v", out["error"])
	}

	swapCoresSeams(t, []ghRelease{{Tag: "not-a-core-tag"}, {Tag: "frp-0.61.0"}}, nil, nil)
	_, out = coresAvailable(t, "")
	if v, _ := out["cores"]; v != nil {
		t.Fatalf("no contract-shaped tags must answer null: %v", v)
	}
}

// TestCoresAvailablePartial: a core whose latest release lists no usable
// artifacts gets an honest per-row error and empty assets; the other rows
// stay complete; unparseable asset names carry null os/arch.
func TestCoresAvailablePartial(t *testing.T) {
	rels := []ghRelease{
		{Tag: "frp-v0.61.2", PublishedAt: "2026-11-21T00:00:00Z"}, // no assets at all
		{Tag: "gost-v3.0.0", Assets: []ghAsset{
			{Name: "weird.tar.gz", Size: 5}, // no <core>-<os>-<arch> segments
			{Name: "gost-linux-amd64.tar.gz", Size: 6, BrowserDownloadURL: "https://github.com/Dark-modem/quantum-cores/releases/download/gost-v3.0.0/gost-linux-amd64.tar.gz"},
		}},
	}
	swapCoresSeams(t, rels, nil, nil)
	_, out := coresAvailable(t, "")
	cores := out["cores"].([]any)
	if len(cores) != 2 {
		t.Fatalf("cores = %v", cores)
	}
	frp := cores[0].(map[string]any)
	if frp["core"] != "frp" || frp["assets"].([]any) == nil || len(frp["assets"].([]any)) != 0 {
		t.Fatalf("frp row wrong: %v", frp)
	}
	if msg, _ := frp["error"].(string); !strings.Contains(msg, "frp-v0.61.2") || !strings.Contains(msg, "no") {
		t.Fatalf("frp row must name its error: %v", frp["error"])
	}
	gost := cores[1].(map[string]any)
	if _, has := gost["error"]; has {
		t.Fatalf("healthy core must carry no error: %v", gost)
	}
	assets := gost["assets"].([]any)
	if len(assets) != 2 {
		t.Fatalf("gost assets = %v", assets)
	}
	// Assets are sorted by name: the parseable one first, "weird.tar.gz"
	// second with null os/arch.
	if assets[0].(map[string]any)["name"] != "gost-linux-amd64.tar.gz" {
		t.Fatalf("asset order wrong: %v", assets)
	}
	weird := assets[1].(map[string]any)
	if weird["name"] != "weird.tar.gz" || weird["os"] != nil || weird["arch"] != nil {
		t.Fatalf("unparseable asset must carry null os/arch: %v", weird)
	}
}

// TestCoresAvailableCache: the §2.20 cache mechanics — 10-min success
// cache, 1-min failure cache, ?refresh=1 throttled to 30 s.
func TestCoresAvailableCache(t *testing.T) {
	calls := 0
	swapCoresSeams(t, fakeCoresReleases(), nil, nil)
	coresReleasesFetcher = func() ([]ghRelease, error) { calls++; return fakeCoresReleases(), nil }

	coresAvailable(t, "")
	coresAvailable(t, "")
	if calls != 1 {
		t.Fatalf("fetches = %d, want 1 (10-min success cache)", calls)
	}
	// refresh=1 inside the 30 s throttle must NOT refetch.
	coresAvailable(t, "?refresh=1")
	if calls != 1 {
		t.Fatalf("refresh inside the 30 s throttle refetched (%d)", calls)
	}
	// Age the cache past the throttle → exactly one refetch.
	coresRelMu.Lock()
	coresRelAt = time.Now().Add(-31 * time.Second)
	coresRelMu.Unlock()
	coresAvailable(t, "?refresh=1")
	if calls != 2 {
		t.Fatalf("refresh=1 after the throttle must refetch once (calls=%d)", calls)
	}
	// A plain GET inside the 10-min success TTL must not.
	coresAvailable(t, "")
	if calls != 2 {
		t.Fatalf("plain GET must ride the success cache (calls=%d)", calls)
	}

	// Failure cache: 1-minute TTL.
	fails := 0
	swapCoresSeams(t, nil, errors.New("boom"), nil)
	coresReleasesFetcher = func() ([]ghRelease, error) { fails++; return nil, errors.New("boom") }
	coresAvailable(t, "")
	coresAvailable(t, "")
	if fails != 1 {
		t.Fatalf("failure fetches = %d, want 1 (1-min failure cache)", fails)
	}
	coresRelMu.Lock()
	coresRelAt = time.Now().Add(-2 * time.Minute) // past the failure TTL
	coresRelMu.Unlock()
	coresAvailable(t, "")
	if fails != 2 {
		t.Fatalf("failure cache must expire after a minute (fails=%d)", fails)
	}
}

// TestCoresTagShape: the ONLY tags the feed accepts — 3-segment semver
// AND the calver form the real repo publishes for udp2raw; suffixed,
// non-numeric and no-"v" tags stay refused.
func TestCoresTagShape(t *testing.T) {
	for tag, ok := range map[string]bool{
		"frp-v0.61.2":         true,
		"udp2raw-v20230206.0": true, // real calver release (2026-11-21 feed check)
		"x-v-v1.2.3":          true, // dashed core name, greedy back to the last -v
		"wstunnel-v10.1.9":    true,
		"frp-v0.61":           true,  // 2 numeric segments are accepted (calver family)
		"frp-0.61.2":          false, // no leading v
		"frp-v0.61.0-beta1":   false, // suffixed
		"frp-vX.Y.Z":          false, // non-numeric
		"frp-v..":             false, // empty segments
		"frp-v1.2.3.4":        false, // 4 segments
		"../evil-v1.2.3":      false, // path traversal in the core part
		"frp/../v1.2.3":       false, // path traversal inside
		"not-a-core-tag":      false,
		"":                    false,
	} {
		if got := coresTagRe.MatchString(tag); got != ok {
			t.Fatalf("coresTagRe(%q) = %v, want %v", tag, got, ok)
		}
	}
}

// TestCoresAvailableRefreshHostileInput: the new query param accepts only
// the literal "1"; every other value is inert (no refetch, still 200).
func TestCoresAvailableRefreshHostileInput(t *testing.T) {
	calls := 0
	swapCoresSeams(t, fakeCoresReleases(), nil, nil)
	coresReleasesFetcher = func() ([]ghRelease, error) { calls++; return fakeCoresReleases(), nil }
	coresAvailable(t, "") // prime the cache
	primed := calls
	for _, q := range []string{
		"?refresh=", "?refresh=0", "?refresh=true", "?refresh=01", "?refresh=1;",
		"?refresh=1%20OR%201=1", "?refresh=<script>", "?refresh=1&refresh=1",
		"?refresh=ON", "?refresh=2",
	} {
		code, _ := coresAvailable(t, q)
		if code != 200 {
			t.Fatalf("GET %s: status %d", q, code)
		}
		if calls != primed {
			t.Fatalf("GET %s refetched (%d→%d) — only refresh=1 may force", q, primed, calls)
		}
	}
	// The literal forces exactly once per 30 s (throttled, not per-request).
	code, _ := coresAvailable(t, "?refresh=1")
	if code != 200 || calls != primed {
		t.Fatalf("refresh=1 inside the throttle must stay cached (calls=%d primed=%d)", calls, primed)
	}
}

// ---- repo-source resolution (artifacts store, source=repo) ----------------

// repoTestEnv extends newEngineTestEnv with the cores seams wired to a
// tiny "repo": one release, one asset, real bytes, a real signature.
type repoTestEnv struct {
	*engineTestEnv
	artifact []byte // the exact release-asset bytes
	priv     ed25519.PrivateKey
}

// newRepoTestEnv wires the feed + downloader seams around a real
// engineTestEnv (real handlers, auth+CSRF, temp dir, in-memory store).
func newRepoTestEnv(t *testing.T, releases func() []ghRelease, dl func(url string, f *os.File) error) *repoTestEnv {
	t.Helper()
	base := newEngineTestEnv(t)
	env := &repoTestEnv{engineTestEnv: base, artifact: []byte("frp-binary-v0.61.2-bytes"), priv: base.priv}
	swapCoresSeams(t, releases(), nil, dl)
	return env
}

// overrideDL swaps the downloader mid-test (nil = the default repo twin).
func (e *repoTestEnv) overrideDL(dl func(url string, f *os.File) error) {
	if dl == nil {
		dl = e.defaultDL
	}
	coresAssetDownloader = dl
}

func (e *repoTestEnv) sigBytes() []byte { return ed25519.Sign(e.priv, e.artifact) }

// defaultDL serves the release asset + its detached raw signature.
func (e *repoTestEnv) defaultDL(url string, f *os.File) error {
	if strings.HasSuffix(url, ".tar.gz.sig") {
		_, err := f.Write(e.sigBytes())
		return err
	}
	_, err := f.Write(e.artifact)
	return err
}

// repoPost posts a source=repo multipart (fields only, no file).
func (e *repoTestEnv) repoPost(fields map[string]string) (int, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	order := []string{"engine", "version", "goarch", "goos", "name", "signature", "source"}
	for _, k := range order {
		if v, ok := fields[k]; ok {
			_ = mw.WriteField(k, v)
		}
	}
	for k, v := range fields {
		switch k {
		case "engine", "version", "goarch", "goos", "name", "signature", "source":
		default:
			_ = mw.WriteField(k, v)
		}
	}
	_ = mw.Close()
	req, err := http.NewRequest("POST", e.srv.URL+"/api/mesh/engines/artifacts", &buf)
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := e.client.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// assertNothingStored proves the M2 ordering: zero rows, zero files
// (including temp droppings) after a refused resolve.
func assertNothingStored(e *repoTestEnv) {
	t := e.t
	t.Helper()
	arts, _ := e.store.ListEngineArtifacts()
	if len(arts) != 0 {
		t.Fatalf("refused resolve left store rows: %+v", arts)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.dir, "engine-artifacts")); len(entries) != 0 {
		t.Fatalf("refused resolve left files: %v", entries)
	}
}

// TestEngineArtifactFromRepoHappyPath: latest pick → download → verify →
// store — the row carries source=repo and the computed pins, the file on
// disk byte-equals the verified bytes, and the agent-facing manifest
// serves the same pins (M2: serving happens only after verification).
func TestEngineArtifactFromRepoHappyPath(t *testing.T) {
	env := newRepoTestEnv(t, fakeCoresReleases, nil)
	env.overrideDL(nil)
	code, out := env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo", "goos": "linux"})
	if code != 200 {
		t.Fatalf("repo resolve: %d %v", code, out)
	}
	name, _ := out["artifact"].(string)
	if name != "frp-0.61.2-linux-amd64.tar.gz" {
		t.Fatalf("derived name: %v", out["artifact"])
	}
	if out["source"] != "repo" || out["tag"] != "frp-v0.61.2" || out["status"] != "stored" {
		t.Fatalf("additive fields wrong: %v", out)
	}
	sum := sha256.Sum256(env.artifact)
	if out["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("sha256 wrong: %v", out["sha256"])
	}

	// Row: provenance + pins.
	arts, _ := env.store.ListEngineArtifacts()
	if len(arts) != 1 || arts[0].Source != "repo" || arts[0].Engine != "frp" || arts[0].Version != "0.61.2" {
		t.Fatalf("store rows: %+v", arts)
	}
	if arts[0].SHA256 != hex.EncodeToString(sum[:]) || arts[0].Size != int64(len(env.artifact)) {
		t.Fatalf("row pins wrong: %+v", arts[0])
	}

	// File: byte-equal, 0600, inside the artifacts dir.
	p := filepath.Join(env.dir, "engine-artifacts", name)
	assertPerm0600(t, p)
	onDisk, _ := os.ReadFile(p)
	if !bytes.Equal(onDisk, env.artifact) {
		t.Fatal("stored bytes differ from the release asset")
	}

	// M2 serving: the agent manifest lists the same pins (the agent
	// re-verifies sha256 + signature on its side).
	resp, err := http.Get(env.srv.URL + "/api/agent/engine-artifacts?engine=frp&goarch=amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var man struct {
		Artifacts []struct {
			Artifact  string `json:"artifact"`
			SHA256    string `json:"sha256"`
			Signature string `json:"signature"`
			Version   string `json:"version"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&man); err != nil {
		t.Fatal(err)
	}
	if len(man.Artifacts) != 1 || man.Artifacts[0].SHA256 != hex.EncodeToString(sum[:]) || man.Artifacts[0].Signature == "" {
		t.Fatalf("agent manifest pins wrong: %+v", man.Artifacts)
	}

	// Explicit version resolves THAT release: 0.61.1 (the older one).
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "arm64", "source": "repo", "version": "0.61.1"})
	if code != 200 {
		t.Fatalf("explicit version resolve: %d %v", code, out)
	}
	if out["tag"] != "frp-v0.61.1" || out["artifact"] != "frp-0.61.1-linux-arm64.tar.gz" {
		t.Fatalf("explicit version pick wrong: %v", out)
	}
	// ...and the "v"-prefixed form normalizes to the same tag.
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "386", "source": "repo", "version": "v0.61.1"})
	if code != 200 || out["tag"] != "frp-v0.61.1" {
		t.Fatalf("v-prefixed version: %d %v", code, out)
	}
}

// TestEngineArtifactFromRepoBase64Sig: the detached signature may arrive
// base64-encoded (trimmed) — same verification.
func TestEngineArtifactFromRepoBase64Sig(t *testing.T) {
	env := newRepoTestEnv(t, fakeCoresReleases, nil)
	b64 := base64.StdEncoding.EncodeToString(env.sigBytes())
	env.overrideDL(func(url string, f *os.File) error {
		if strings.HasSuffix(url, ".tar.gz.sig") {
			_, err := f.Write([]byte(b64 + "\n"))
			return err
		}
		_, err := f.Write(env.artifact)
		return err
	})
	code, out := env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	if code != 200 {
		t.Fatalf("base64 sig resolve: %d %v", code, out)
	}
}

// TestEngineArtifactFromRepoTamperRefused is the verify-then-store
// ORDERING proof: a tampered asset (valid-looking signature over
// DIFFERENT bytes) must be REFUSED before storage — no row, no file, no
// temp leftover. A wrong-key signature is refused the same way.
func TestEngineArtifactFromRepoTamperRefused(t *testing.T) {
	env := newRepoTestEnv(t, fakeCoresReleases, nil)

	// Signature over different bytes than the served asset.
	tampered := append([]byte(nil), env.artifact...)
	tampered[0] ^= 0xFF
	sigOfTampered := ed25519.Sign(env.priv, tampered)
	env.overrideDL(func(url string, f *os.File) error {
		if strings.HasSuffix(url, ".tar.gz.sig") {
			_, err := f.Write(sigOfTampered)
			return err
		}
		_, err := f.Write(env.artifact) // ORIGINAL bytes, sig of TAMPERED bytes
		return err
	})
	code, out := env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	if code != 400 {
		t.Fatalf("tampered asset must be 400, got %d %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "verification FAILED") {
		t.Fatalf("tamper error wrong: %v", out["error"])
	}
	assertNothingStored(env)

	// Signature by a DIFFERENT key over the served bytes.
	env.overrideDL(func(url string, f *os.File) error {
		if strings.HasSuffix(url, ".tar.gz.sig") {
			_, err := f.Write(ed25519.Sign(env.otherPriv, env.artifact))
			return err
		}
		_, err := f.Write(env.artifact)
		return err
	})
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	if code != 400 || !strings.Contains(out["error"].(string), "verification FAILED") {
		t.Fatalf("wrong-key signature must be refused: %d %v", code, out)
	}
	assertNothingStored(env)
}

// TestEngineArtifactFromRepoFeedAndAssetFailures: honest refusals for
// every upstream shape — unreachable feed, empty repo, unknown core,
// unknown version, missing arch asset, missing signature, download
// failure, oversize. NOTHING is stored in any of these.
func TestEngineArtifactFromRepoFeedAndAssetFailures(t *testing.T) {
	env := newRepoTestEnv(t, fakeCoresReleases, nil)

	// Unreachable feed.
	swapCoresSeams(t, nil, errors.New("github api: 403 — rate limit"), nil)
	code, out := env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	if code != 502 || !strings.Contains(out["error"].(string), "cores release feed") {
		t.Fatalf("unreachable feed: %d %v", code, out)
	}
	assertNothingStored(env)

	// Empty repo.
	swapCoresSeams(t, []ghRelease{}, nil, nil)
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	if code != 404 || !strings.Contains(out["error"].(string), "no release for core frp") {
		t.Fatalf("empty repo: %d %v", code, out)
	}
	assertNothingStored(env)

	// Unknown version.
	swapCoresSeams(t, fakeCoresReleases(), nil, nil)
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo", "version": "9.9.9"})
	if code != 404 || !strings.Contains(out["error"].(string), "no release frp-v9.9.9") {
		t.Fatalf("unknown version: %d %v", code, out)
	}
	// Unknown core (matrix is exempt — the repo has no releases for it).
	code, out = env.repoPost(map[string]string{"engine": "matrix", "goarch": "amd64", "source": "repo"})
	if code != 404 || !strings.Contains(out["error"].(string), "no release for core matrix") {
		t.Fatalf("matrix resolve: %d %v", code, out)
	}
	// Missing arch asset.
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "arm", "source": "repo"})
	if code != 404 || !strings.Contains(out["error"].(string), "has no frp-linux-arm.tar.gz asset") {
		t.Fatalf("missing arch asset: %d %v", code, out)
	}

	// Missing signature asset.
	env.overrideDL(func(url string, f *os.File) error {
		if strings.HasSuffix(url, ".tar.gz.sig") {
			return errors.New("http 404 Not Found from https://github.com/Dark-modem/quantum-cores")
		}
		_, err := f.Write(env.artifact)
		return err
	})
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	if code != 502 || !strings.Contains(out["error"].(string), "detached signature") {
		t.Fatalf("missing sig: %d %v", code, out)
	}
	assertNothingStored(env)

	// Malformed signature file.
	env.overrideDL(func(url string, f *os.File) error {
		if strings.HasSuffix(url, ".tar.gz.sig") {
			_, err := f.Write([]byte("hello-world-not-a-sig"))
			return err
		}
		_, err := f.Write(env.artifact)
		return err
	})
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	if code != 400 || !strings.Contains(out["error"].(string), "neither raw 64-byte ed25519") {
		t.Fatalf("malformed sig: %d %v", code, out)
	}
	assertNothingStored(env)

	// Download failure.
	env.overrideDL(func(url string, f *os.File) error {
		return errors.New("download failed across all mirrors")
	})
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	if code != 502 || !strings.Contains(out["error"].(string), "cores asset download") {
		t.Fatalf("download failure: %d %v", code, out)
	}
	assertNothingStored(env)

	// Oversize asset (the cap is shrunk around the POST, not inside the
	// downloader — the handler Stat check runs after the download).
	env.overrideDL(func(url string, f *os.File) error {
		if strings.HasSuffix(url, ".tar.gz.sig") {
			_, err := f.Write(env.sigBytes())
			return err
		}
		_, err := f.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
		return err
	})
	oldCap := engineArtifactMaxUpload
	engineArtifactMaxUpload = 1 << 20
	code, out = env.repoPost(map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo"})
	engineArtifactMaxUpload = oldCap
	if code != 400 || !strings.Contains(out["error"].(string), "too large") {
		t.Fatalf("oversize asset: %d %v", code, out)
	}
	assertNothingStored(env)
}

// TestEngineArtifactFromRepoHostileInput: the strict-form matrix — the
// repo path takes the core + optional version/arch ONLY; names,
// signatures, files and unknown sources are refused before any fetch,
// and nothing is ever stored.
func TestEngineArtifactFromRepoHostileInput(t *testing.T) {
	env := newRepoTestEnv(t, fakeCoresReleases, nil)
	for _, tc := range []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{name: "uppercase source", fields: map[string]string{"engine": "frp", "goarch": "amd64", "source": "REPO"}, want: "invalid source"},
		{name: "unknown source", fields: map[string]string{"engine": "frp", "goarch": "amd64", "source": "upstream"}, want: "invalid source"},
		{name: "path trick in source", fields: map[string]string{"engine": "frp", "goarch": "amd64", "source": "../../repo"}, want: "invalid source"},
		{name: "custom name", fields: map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo", "name": "evil.tar.gz"}, want: "derived automatically"},
		{name: "client signature", fields: map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo", "signature": "AAAA"}, want: "do not send one"},
		{name: "bad version", fields: map[string]string{"engine": "frp", "goarch": "amd64", "source": "repo", "version": "../evil"}, want: "invalid version tag"},
		{name: "bad engine", fields: map[string]string{"engine": "FRP!", "goarch": "amd64", "source": "repo"}, want: "invalid engine id"},
		{name: "engine path trick", fields: map[string]string{"engine": "../frp", "goarch": "amd64", "source": "repo"}, want: "invalid engine id"},
		{name: "bad arch", fields: map[string]string{"engine": "frp", "goarch": "mips", "source": "repo"}, want: "unsupported goarch"},
		{name: "bad goos", fields: map[string]string{"engine": "frp", "goarch": "amd64", "goos": "darwin", "source": "repo"}, want: "unsupported goos"},
	} {
		code, out := env.repoPost(tc.fields)
		if code != 400 {
			t.Fatalf("%s: code=%d out=%v (want 400)", tc.name, code, out)
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, tc.want) {
			t.Fatalf("%s: error %q does not mention %q", tc.name, msg, tc.want)
		}
	}
	assertNothingStored(env)

	// A file field on the repo path is refused (multipart with a file).
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("engine", "frp")
	_ = mw.WriteField("goarch", "amd64")
	_ = mw.WriteField("source", "repo")
	fw, _ := mw.CreateFormFile("file", "artifact.tar.gz")
	_, _ = fw.Write([]byte("smuggled"))
	_ = mw.Close()
	req, _ := http.NewRequest("POST", env.srv.URL+"/api/mesh/engines/artifacts", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if resp.StatusCode != 400 || !strings.Contains(out["error"].(string), "no file field") {
		t.Fatalf("repo path with a file field: %d %v", resp.StatusCode, out)
	}
	assertNothingStored(env)

	// The manual path is UNCHANGED: an explicit source=upload upload still
	// works and its response carries no source key.
	artifact := []byte("manual-upload-bytes")
	code, out := env.upload("frp", "0.61.0", "amd64", "", env.sign(artifact, env.priv), artifact)
	if code != 200 {
		t.Fatalf("manual upload with explicit source=upload: %d %v", code, out)
	}
	if _, has := out["source"]; has {
		t.Fatalf("manual upload response shape must stay unchanged: %v", out)
	}
	arts, _ := env.store.ListEngineArtifacts()
	if len(arts) != 1 || arts[0].Source != "upload" {
		t.Fatalf("manual upload provenance: %+v", arts)
	}
}
