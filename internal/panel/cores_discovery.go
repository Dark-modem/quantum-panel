// cores_discovery.go — BACKEND-38 (UI-API-CONTRACT §2.22): discovery of
// the tunnel-engine cores published in the PUBLIC Dark-modem/quantum-cores
// repository. The repo versions each core through GitHub releases with the
// architect-fixed tag shape `<core>-v<semver>` (e.g. frp-v0.61.0) and
// assets named `<core>-<os>-<arch>.tar.gz` (os/arch use the same enums the
// engine-artifact store already enforces) plus a detached ed25519 signature
// file `<asset>.sig`. The discovery rides the SAME mechanism as the §2.20
// agent-update feed: the GitHub releases API, a 10-minute success cache /
// 1-minute failure cache, and a ?refresh=1 drop throttled to 30 s.
//
// SECURITY INVARIANT M2 UNCHANGED: a repo-sourced artifact is stored only
// after the panel downloads the exact bytes, recomputes sha256 over them
// and verifies the detached ed25519 signature against the configured
// engine-artifact key — BEFORE anything is stored or served to agents
// (finishEngineArtifactStore, shared verbatim with the manual upload
// path). Agents still download + re-verify against their build-pinned key
// on their side. The manual upload path stays working UNCHANGED; repo
// resolution is strictly additive (POST /api/mesh/engines/artifacts with
// source=repo and no file field).

package panel

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// coresReleaseAPI is the releases API of the cores repository (public —
// the mirror/token logic of getJSONAt applies unchanged). A var purely as
// a test seam: the BACKEND-39 pagination test points it at a local
// page-shaped server; production always holds the real URL.
var coresReleaseAPI = "https://api.github.com/repos/Dark-modem/quantum-cores/releases"

// coresTagRe is the ONLY release tag shape the cores feed accepts:
// `<core>-v<semver>` where the version is 2 or 3 numeric segments — the
// 3-segment semver (frp-v0.61.2) plus the CALVER form the repo actually
// publishes for udp2raw (udp2raw-v20230206.0; real-feed check 2026-11-21,
// CORES-1). The core part is lowercase with -, _ allowed (the same
// charset family as engine ids) and the version only digits+dots, so a
// parsed tag can never carry path separators, "..", "?" or spaces into a
// download URL. Non-numeric or suffixed versions (v0.61.0-rc1, vX.Y.Z)
// stay refused.
var coresTagRe = regexp.MustCompile(`^([a-z][a-z0-9_-]*)-v(\d+\.\d+(\.\d+)?)$`)

// coresAssetRe matches a usable release asset: `<core>-<os>-<arch>.tar.gz`
// (the trailing .sig companion files are NOT usable artifacts — they ride
// alongside by name).
var coresAssetRe = regexp.MustCompile(`\.tar\.gz$`)

// coresReleaseAssetURL builds the canonical download URL for one asset of
// a contract-shaped tag. The tag is already coresTagRe-shaped and the
// asset name is derived from validated fields, so no request input
// reaches a URL unescaped.
func coresReleaseAssetURL(tag, asset string) string {
	return "https://github.com/Dark-modem/quantum-cores/releases/download/" + tag + "/" + asset
}

// coresReleasesFetcher is swapped in tests (never touches the network) —
// the cores twin of releasesFetcher. Handlers read through
// fetchCoresReleasesCached, so a mocked seam still exercises the cache.
var coresReleasesFetcher = fetchCoresReleasesUncached

// fetchCoresReleasesUncached walks the cores release list (same paging
// ceiling and keep-what-we-have semantics as the panel-update feed).
func fetchCoresReleasesUncached() ([]ghRelease, error) {
	var all []ghRelease
	// ponytail: 10 pages x 100 = 1000 releases ceiling; raise if the repo ever outgrows it.
	for page := 1; page <= 10; page++ {
		var rels []ghRelease
		if err := getJSONAt(coresReleaseAPI, fmt.Sprintf("?per_page=100&page=%d", page), &rels); err != nil {
			if page == 1 {
				return nil, err
			}
			break // keep what we have
		}
		all = append(all, rels...)
		if len(rels) < 100 {
			break
		}
	}
	return all, nil
}

var (
	coresRelMu    sync.Mutex
	coresRelCache []ghRelease
	coresRelErr   error
	coresRelAt    time.Time
)

// fetchCoresReleasesCached caches the cores release list for 10 minutes
// (failures for 1 minute) — the same L23 rate-limit protection the
// panel-update feed rides.
//
// GitHub rate-limit math (BACKEND-39 live-path hardening, unauthenticated
// budget = 60 requests/hour per IP on api.github.com): the discovery
// handler and the repo-resolve path share THIS ONE cache, so a polling
// panel costs at most one feed request per 10 minutes = 6/h — a tenth of
// the budget. The authenticated ?refresh=1 drop is throttled to 30 s
// (worst case 2/min by a scripting operator); once GitHub starts
// answering 403, every failure is cached for a full minute, which caps
// the sustained attempt rate at the 60/h ceiling itself — the panel
// degrades into cached errors instead of hammering. Asset downloads ride
// github.com release redirects to the asset CDN (release-assets.githubusercontent.com
// today, objects.githubusercontent.com legacy — both on the download allowlist,
// isOfficialGitHubURL), which are NOT part of the api.github.com budget, and stay
// bounded by engineArtifactMaxUpload + the downloader timeout.
func fetchCoresReleasesCached() ([]ghRelease, error) {
	coresRelMu.Lock()
	defer coresRelMu.Unlock()
	ttl := 10 * time.Minute
	if coresRelErr != nil {
		ttl = time.Minute
	}
	if !coresRelAt.IsZero() && time.Since(coresRelAt) < ttl {
		return append([]ghRelease(nil), coresRelCache...), coresRelErr
	}
	rels, err := coresReleasesFetcher()
	coresRelCache, coresRelErr, coresRelAt = rels, err, time.Now()
	return append([]ghRelease(nil), rels...), err
}

// dropCoresReleaseCache makes the next fetch hit GitHub again (Check Now),
// throttled to 30 s like the panel-update feed.
func dropCoresReleaseCache() {
	coresRelMu.Lock()
	defer coresRelMu.Unlock()
	if time.Since(coresRelAt) > 30*time.Second {
		coresRelAt = time.Time{}
	}
}

// coresReleaseCheckedAt is when the cores release list was last fetched
// (zero: never).
func coresReleaseCheckedAt() time.Time {
	coresRelMu.Lock()
	defer coresRelMu.Unlock()
	return coresRelAt
}

// coreAssetView is the wire shape of one discovered release asset
// (§2.22): the tar.gz artifacts only. os/arch parse out of the name and
// are null when the name does not carry them (honest unknown — the UI
// renders null as "—").
type coreAssetView struct {
	Name string  `json:"name"`
	Os   *string `json:"os"`
	Arch *string `json:"arch"`
	Size int64   `json:"size"`
	URL  string  `json:"url"`
}

// coreRow is the wire shape of one discovered core (§2.22). Error is
// populated when the core is discoverable but its latest release lists no
// usable artifacts (normally empty). BACKEND-43 addendum fields (all
// additive, ALWAYS present): methods is the core MANIFEST.json methods[]
// VERBATIM (null when the manifest does not publish one or is
// unreachable); installed_version is the newest version any enrolled
// agent reported installed ("" = none reported); outdated compares that
// against latest_version (null when either side is unknown — the §2.20
// honest-unknown rule).
type coreRow struct {
	Core             string          `json:"core"`
	LatestTag        string          `json:"latest_tag"`
	LatestVersion    string          `json:"latest_version"`
	PublishedAt      string          `json:"published_at,omitempty"`
	Assets           []coreAssetView `json:"assets"`
	Error            string          `json:"error,omitempty"`
	Methods          json.RawMessage `json:"methods"`
	InstalledVersion string          `json:"installed_version"`
	Outdated         *bool           `json:"outdated"`
}

// parseCoreAssetOSArch extracts the os/arch segments from
// `<core>-<os>-<arch>.tar.gz` (right-anchored split, so core names with
// dashes keep parsing). nil pointers = the name does not carry them.
func parseCoreAssetOSArch(name string) (*string, *string) {
	base := strings.TrimSuffix(name, ".tar.gz")
	parts := strings.Split(base, "-")
	if len(parts) < 3 {
		return nil, nil
	}
	osName, archName := parts[len(parts)-2], parts[len(parts)-1]
	return &osName, &archName
}

// discoverCores groups a cores release list per core: the newest non-draft
// `<core>-v<semver>` tag wins (the contract has no prerelease concept for
// cores). nil = nothing discoverable (empty repo or no tag in the
// contract shape); rows are sorted by core name and assets by asset name
// so the answer is deterministic.
func discoverCores(rels []ghRelease) []coreRow {
	type latest struct {
		tag, ver string
		rel      *ghRelease
	}
	bests := map[string]*latest{}
	for i := range rels {
		if rels[i].Draft {
			continue
		}
		m := coresTagRe.FindStringSubmatch(rels[i].Tag)
		if m == nil {
			continue
		}
		ver := "v" + m[2]
		cur := bests[m[1]]
		if cur == nil || compareVersions(ver, cur.ver) > 0 {
			bests[m[1]] = &latest{tag: rels[i].Tag, ver: ver, rel: &rels[i]}
		}
	}
	if len(bests) == 0 {
		return nil
	}
	cores := make([]string, 0, len(bests))
	for c := range bests {
		cores = append(cores, c)
	}
	sort.Strings(cores)
	rows := make([]coreRow, 0, len(bests))
	for _, c := range cores {
		b := bests[c]
		row := coreRow{
			Core:          c,
			LatestTag:     b.tag,
			LatestVersion: strings.TrimPrefix(b.ver, "v"),
			PublishedAt:   b.rel.PublishedAt,
			Assets:        []coreAssetView{},
		}
		for _, ga := range b.rel.Assets {
			if !coresAssetRe.MatchString(ga.Name) {
				continue // tar.gz artifacts only; the .sig companion rides by name
			}
			osName, archName := parseCoreAssetOSArch(ga.Name)
			row.Assets = append(row.Assets, coreAssetView{
				Name: ga.Name, Os: osName, Arch: archName, Size: ga.Size, URL: ga.BrowserDownloadURL,
			})
		}
		sort.Slice(row.Assets, func(i, j int) bool { return row.Assets[i].Name < row.Assets[j].Name })
		if len(row.Assets) == 0 {
			row.Error = fmt.Sprintf("latest release %s lists no <core>-<os>-<arch>.tar.gz assets", b.tag)
		}
		rows = append(rows, row)
	}
	return rows
}

// handleCoresAvailable is GET /api/cores/available (§2.22): the cores the
// quantum-cores repository has published, on the §2.20 feed mechanism.
// Honest partial answers: cores is null when nothing is discoverable
// (empty repo / no contract-shaped tags / unreachable — the error field
// distinguishes unreachable), a row carries error when that core's latest
// release is unusable. checked_at = the last SUCCESSFUL fetch.
//
// BACKEND-43 addendum: each row additionally carries the core's
// MANIFEST.json methods[] VERBATIM (null = not published / unreachable)
// and the installed_version/outdated pair from the enrolled agents'
// reported engine versions (null = unknown on either side). Both fills are
// best-effort — a manifest miss or a closed store never fails the row.
func handleCoresAvailable(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("refresh") == "1" {
		dropCoresReleaseCache()
		dropCoresManifestCache()
	}
	rels, err := fetchCoresReleasesCached()
	var rows []coreRow
	if err == nil {
		rows = discoverCores(rels)
	}
	// the additive fills (methods[] + installed/outdated): best-effort,
	// never fatal — the endpoint's §2.22.1 honest-partial contract stands
	s := meshStorePeek()
	for i := range rows {
		if entry := fetchCoresManifestCached(rows[i].Core); entry != nil {
			rows[i].Methods = entry.Methods
		}
		if s != nil {
			rows[i].InstalledVersion, rows[i].Outdated =
				coreRowInstalledOutdated(s, rows[i].Core, rows[i].LatestVersion)
		}
	}
	resp := map[string]any{"cores": rows}
	if err != nil {
		resp["error"] = err.Error()
	} else if t := coresReleaseCheckedAt(); !t.IsZero() {
		resp["checked_at"] = t.Unix()
	}
	writeJSON(w, resp)
}

// ---- repo-source resolution for the artifacts store (§2.22) ---------------

// coresAssetDownloader is swapped in tests (prod = the generic GitHub
// release-asset downloader with its domain allow-list and mirror
// fallbacks — downloadFile).
var coresAssetDownloader = downloadFile

// pickCoreRelease resolves the release for one core: an explicit version
// (0.61.0 or v0.61.0 → tag frp-v0.61.0) must exist verbatim, otherwise
// the core's newest non-draft contract-shaped tag wins.
func pickCoreRelease(core, version string, rels []ghRelease) (string, *ghRelease, error) {
	if version != "" {
		tag := core + "-v" + strings.TrimPrefix(version, "v")
		for i := range rels {
			if rels[i].Draft {
				continue
			}
			if rels[i].Tag == tag {
				return tag, &rels[i], nil
			}
		}
		return "", nil, fmt.Errorf("no release %s in the cores repo feed", tag)
	}
	bestTag, bestVer := "", ""
	var bestRel *ghRelease
	for i := range rels {
		if rels[i].Draft {
			continue
		}
		m := coresTagRe.FindStringSubmatch(rels[i].Tag)
		if m == nil || m[1] != core {
			continue
		}
		ver := "v" + m[2]
		if bestTag == "" || compareVersions(ver, bestVer) > 0 {
			bestTag, bestVer, bestRel = rels[i].Tag, ver, &rels[i]
		}
	}
	if bestTag == "" {
		return "", nil, fmt.Errorf("no release for core %s in the cores repo feed", core)
	}
	return bestTag, bestRel, nil
}

// fetchCoreSig downloads the detached signature `<asset>.sig` of one
// release and returns the 64-byte ed25519 signature. The file may be raw
// bytes or base64 (trimmed); anything else is refused (fail-closed). The
// read is capped — a signature file is tiny by definition.
func fetchCoreSig(tag, asset string) ([]byte, error) {
	tmp, err := os.CreateTemp("", ".cores-sig-*")
	if err != nil {
		return nil, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := coresAssetDownloader(coresReleaseAssetURL(tag, asset+".sig"), tmp); err != nil {
		return nil, fmt.Errorf("cannot fetch the detached signature %s.sig: %w", asset, err)
	}
	tmp.Close()
	f, err := os.Open(tmpName)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return nil, err
	}
	// Raw 64 bytes FIRST — a raw signature may legitimately END in a
	// whitespace-valued byte, so nothing may be trimmed before this check.
	// Then the trailing-newline case (65 raw bytes), then base64.
	if len(data) == 64 {
		return data, nil
	}
	line := strings.TrimSpace(string(data))
	if len(line) == 64 {
		return []byte(line), nil
	}
	sig, err := base64.StdEncoding.DecodeString(line)
	if err != nil || len(sig) != 64 {
		return nil, errors.New("signature file is neither raw 64-byte ed25519 nor base64 of it")
	}
	return sig, nil
}

// resolveCoreArtifactFromRepo implements the source=repo arm of
// POST /api/mesh/engines/artifacts (§2.22): resolve the release + the
// arch-resolved asset from the cores feed, download the asset and its
// detached signature, verify sha256 + ed25519 over the exact bytes
// (finishEngineArtifactStore — the SAME M2 tail the manual upload runs),
// then store exactly like an upload. The download name is derived; the
// signature and the bytes always come from the release.
func resolveCoreArtifactFromRepo(w http.ResponseWriter, r *http.Request, engine, version, goos, goarch, name, sigB64 string) {
	// Strict form: on this path the operator names the core (+optional
	// version/arch) — the bytes, the signature and the download name come
	// from the release. Anything else is refused before any fetch.
	switch {
	case !engineArtifactEngineRe.MatchString(engine):
		writeAPIError(w, r, "E-IN-06", "invalid engine id (lowercase letters, digits, -, _; starts with a letter)")
		return
	case !engineArtifactArchValid[goarch]:
		writeAPIError(w, r, "E-IN-06", "unsupported goarch (amd64, arm64, arm, 386)")
		return
	case !engineArtifactOSValid[goos]:
		writeAPIError(w, r, "E-IN-06", "unsupported goos (linux)")
		return
	case name != "":
		writeAPIError(w, r, "E-IN-06", "download name is derived automatically for repo-sourced artifacts")
		return
	case sigB64 != "":
		writeAPIError(w, r, "E-IN-06", "the signature comes from the release's detached .sig asset — do not send one")
		return
	case version != "" && !engineArtifactVersionRe.MatchString(version):
		writeAPIError(w, r, "E-IN-06", "invalid version tag")
		return
	}
	if r.MultipartForm != nil && len(r.MultipartForm.File["file"]) > 0 {
		writeAPIError(w, r, "E-IN-06", "source=repo takes no file field — the artifact is downloaded from the release")
		return
	}
	// The M2 key gate comes BEFORE any download work: a misconfigured
	// engine-artifact key fails fast with the fix-carrying message.
	key, err := engineArtifactVerifyKey()
	if err != nil {
		writeAPIError(w, r, "E-ENG-03", err.Error())
		return
	}

	rels, err := fetchCoresReleasesCached()
	if err != nil {
		writeAPIError(w, r, "E-UPDATE-01", "cores release feed: "+err.Error())
		return
	}
	tag, rel, perr := pickCoreRelease(engine, version, rels)
	if perr != nil {
		writeAPIError(w, r, "E-IN-04", perr.Error())
		return
	}
	asset := engine + "-" + goos + "-" + goarch + ".tar.gz"
	url := ""
	listed := false
	for _, ga := range rel.Assets {
		if ga.Name == asset {
			listed = true
			url = ga.BrowserDownloadURL
			break
		}
	}
	if !listed {
		writeAPIError(w, r, "E-IN-04", fmt.Sprintf("release %s has no %s asset", tag, asset))
		return
	}
	if url == "" {
		url = coresReleaseAssetURL(tag, asset) // listed without a URL: canonical form
	}

	if err := os.MkdirAll(engineArtifactDir(), 0o700); err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot create engine-artifacts dir: "+err.Error())
		return
	}
	tmp, err := os.CreateTemp(engineArtifactDir(), ".repo-*")
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot create temp file: "+err.Error())
		return
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot chmod temp file: "+err.Error())
		return
	}
	if err := coresAssetDownloader(url, tmp); err != nil {
		tmp.Close()
		writeAPIError(w, r, "E-UPDATE-02", "cores asset download: "+err.Error())
		return
	}
	_ = tmp.Close() // downloadFile closes on success too; double close is a no-op error
	fi, err := os.Stat(tmpName)
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot stat downloaded artifact: "+err.Error())
		return
	}
	if fi.Size() == 0 {
		writeAPIError(w, r, "E-IN-01", "downloaded artifact is empty")
		return
	}
	if fi.Size() > engineArtifactMaxUpload {
		writeAPIError(w, r, "E-IN-01", "artifact too large (limit "+strconv.FormatInt(engineArtifactMaxUpload>>20, 10)+" MiB)")
		return
	}
	sig, serr := fetchCoreSig(tag, asset)
	if serr != nil {
		if strings.HasPrefix(serr.Error(), "cannot fetch") {
			writeAPIError(w, r, "E-UPDATE-02", serr.Error())
			return
		}
		writeAPIError(w, r, "E-IN-06", serr.Error())
		return
	}

	// The tag is authoritative for the stored version (an explicit version
	// was already normalized into it by pickCoreRelease).
	version = strings.TrimPrefix(tag, engine+"-v")
	storeName := engine + "-" + version + "-" + goos + "-" + goarch + ".tar.gz"
	if finishEngineArtifactStore(w, r, tmpName, engine, version, goos, goarch, storeName, key, sig,
		map[string]any{"source": "repo", "tag": tag}) {
		cleanup = false
	}
}
