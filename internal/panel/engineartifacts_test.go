// engineartifacts_test.go — Phase 5 R5 tests through the REAL handlers:
// auth+CSRF gates, field validation, sha256 recomputation, ed25519
// signature verification (incl. the raw-key footgun), duplicate slot
// rules, size limits, atomic 0600 storage, the agent manifest + file
// endpoints, and the coverage warnings the Engines tab renders.

package panel

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// engineTestEnv wires a throwaway configDir + in-memory store + fresh
// ed25519 keypair and returns a client with a real session/CSRF.
type engineTestEnv struct {
	t          *testing.T
	srv        *httptest.Server
	client     *http.Client
	store      *store.Store
	dir        string
	priv       ed25519.PrivateKey
	pubDER     []byte
	pubB64     string
	otherPriv  ed25519.PrivateKey // a DIFFERENT key (wrong-signature tests)
	oldCfg     panelConfig
	oldKey     []byte
	oldMaxUpld int64
}

func newEngineTestEnv(t *testing.T) *engineTestEnv {
	t.Helper()
	withTempConfig(t)
	s, err := store.OpenInMemory()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	meshStoreSetForTest(s)
	t.Cleanup(func() { meshStoreCloseForTest() })

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	otherPub, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	_ = otherPub
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("spki: %v", err)
	}

	env := &engineTestEnv{
		t: t, store: s, dir: configDir,
		priv: priv, pubDER: pubDER,
		pubB64:     base64.StdEncoding.EncodeToString(pubDER),
		otherPriv:  otherPriv,
		oldCfg:     cfg,
		oldKey:     engineArtifactPubKeyOverride,
		oldMaxUpld: engineArtifactMaxUpload,
	}
	cfg = panelConfig{Username: "op"}
	engineArtifactPubKeyOverride = pubDER
	t.Cleanup(func() {
		cfg = env.oldCfg
		engineArtifactPubKeyOverride = env.oldKey
		engineArtifactMaxUpload = env.oldMaxUpld
	})

	mux := http.NewServeMux()
	registerEngineArtifactRoutes(mux, "")
	env.srv = httptest.NewServer(mux)
	t.Cleanup(env.srv.Close)
	env.client = meshSession(t)
	return env
}

// upload posts a real multipart upload; returns the response map.
func (e *engineTestEnv) upload(engine, version, goarch, name, sigB64 string, body []byte) (int, map[string]any) {
	e.t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("engine", engine)
	_ = mw.WriteField("version", version)
	_ = mw.WriteField("goarch", goarch)
	_ = mw.WriteField("goos", "linux")
	if name != "" {
		_ = mw.WriteField("name", name)
	}
	_ = mw.WriteField("signature", sigB64)
	if body != nil {
		fw, _ := mw.CreateFormFile("file", "artifact.tar.gz")
		_, _ = fw.Write(body)
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

func (e *engineTestEnv) sign(body []byte, key ed25519.PrivateKey) string {
	sig := ed25519.Sign(key, body)
	return base64.StdEncoding.EncodeToString(sig)
}

func TestEngineArtifactUploadHappyPath(t *testing.T) {
	env := newEngineTestEnv(t)
	artifact := bytes.Repeat([]byte("frp-binary-"), 1000)
	code, out := env.upload("frp", "0.61.1", "amd64", "", env.sign(artifact, env.priv), artifact)
	if code != 200 {
		t.Fatalf("upload: %d %v", code, out)
	}
	name, _ := out["artifact"].(string)
	if name != "frp-0.61.1-linux-amd64.tar.gz" {
		t.Fatalf("derived name: %v", out["artifact"])
	}

	// File stored with 0600 in the dedicated dir.
	p := filepath.Join(env.dir, "engine-artifacts", name)
	assertPerm0600(t, p)
	onDisk, _ := os.ReadFile(p)
	if !bytes.Equal(onDisk, artifact) {
		t.Fatal("stored bytes differ from the upload")
	}

	// Unauthenticated manifest lists it with the right pins (R5 shape).
	resp, err := http.Get(env.srv.URL + "/api/agent/engine-artifacts?engine=frp&goarch=amd64")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var man struct {
		Artifacts []struct {
			Engine    string `json:"engine"`
			Version   string `json:"version"`
			Goos      string `json:"goos"`
			Goarch    string `json:"goarch"`
			Artifact  string `json:"artifact"`
			SHA256    string `json:"sha256"`
			Size      int64  `json:"size"`
			Signature string `json:"signature"`
		} `json:"artifacts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&man); err != nil {
		t.Fatal(err)
	}
	if len(man.Artifacts) != 1 {
		t.Fatalf("manifest: %+v", man.Artifacts)
	}
	a := man.Artifacts[0]
	sum := sha256.Sum256(artifact)
	if a.SHA256 != hex.EncodeToString(sum[:]) || a.Size != int64(len(artifact)) || a.Signature == "" {
		t.Fatalf("manifest pins wrong: %+v", a)
	}

	// Unauthenticated download serves the exact bytes.
	resp2, err := http.Get(env.srv.URL + "/api/agent/engine-artifacts/file/" + a.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	got := new(bytes.Buffer)
	_, _ = got.ReadFrom(resp2.Body)
	if !bytes.Equal(got.Bytes(), artifact) {
		t.Fatal("downloaded bytes differ")
	}

	// Key snippet reflects the configured key.
	req, _ := http.NewRequest("GET", env.srv.URL+"/api/mesh/engines/keysnippet", nil)
	resp3, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	var ks map[string]any
	_ = json.NewDecoder(resp3.Body).Decode(&ks)
	if ks["configured"] != true || !strings.Contains(ks["agent_ldflags"].(string), "engineArtifactPubKeyB64="+env.pubB64) {
		t.Fatalf("key snippet: %v", ks)
	}
}

func TestEngineArtifactUploadValidation(t *testing.T) {
	env := newEngineTestEnv(t)
	artifact := []byte("payload")
	sig := env.sign(artifact, env.priv)
	for _, tc := range []struct {
		name                    string
		engine, version, goarch string
		artifactName, sig       string
		body                    []byte
		wantCode                int
		wantErrContains         string
	}{
		{name: "bad engine id", engine: "FRP!", version: "0.61.1", goarch: "amd64", sig: sig, body: artifact, wantCode: 400, wantErrContains: "engine id"},
		{name: "bad version", engine: "frp", version: "../evil", goarch: "amd64", sig: sig, body: artifact, wantCode: 400, wantErrContains: "version"},
		{name: "bad arch", engine: "frp", version: "0.61.1", goarch: "mips", sig: sig, body: artifact, wantCode: 400, wantErrContains: "goarch"},
		{name: "path trick in name", engine: "frp", version: "0.61.1", goarch: "amd64", artifactName: "../escape.tar.gz", sig: sig, body: artifact, wantCode: 400, wantErrContains: "name"},
		{name: "uppercase name", engine: "frp", version: "0.61.1", goarch: "amd64", artifactName: "Evil.tar.gz", sig: sig, body: artifact, wantCode: 400, wantErrContains: "name"},
		{name: "sig not base64", engine: "frp", version: "0.61.1", goarch: "amd64", sig: "not-base64!!", body: artifact, wantCode: 400, wantErrContains: "signature"},
		{name: "sig wrong length", engine: "frp", version: "0.61.1", goarch: "amd64", sig: base64.StdEncoding.EncodeToString(make([]byte, 32)), body: artifact, wantCode: 400, wantErrContains: "signature"},
		{name: "signature by WRONG key", engine: "frp", version: "0.61.1", goarch: "amd64", sig: env.sign(artifact, env.otherPriv), body: artifact, wantCode: 400, wantErrContains: "verification FAILED"},
		{name: "signature over DIFFERENT bytes", engine: "frp", version: "0.61.1", goarch: "amd64", sig: env.sign([]byte("other"), env.priv), body: artifact, wantCode: 400, wantErrContains: "verification FAILED"},
		{name: "missing file", engine: "frp", version: "0.61.1", goarch: "amd64", sig: sig, body: nil, wantCode: 400, wantErrContains: "file field"},
	} {
		code, out := env.upload(tc.engine, tc.version, tc.goarch, tc.artifactName, tc.sig, tc.body)
		if code != tc.wantCode {
			t.Fatalf("%s: code=%d out=%v (want %d)", tc.name, code, out, tc.wantCode)
		}
		if msg, _ := out["error"].(string); !strings.Contains(msg, tc.wantErrContains) {
			t.Fatalf("%s: error %q does not mention %q", tc.name, msg, tc.wantErrContains)
		}
	}
	// Nothing leaked into the store or the directory.
	arts, _ := env.store.ListEngineArtifacts()
	if len(arts) != 0 {
		t.Fatalf("rejected uploads left rows: %+v", arts)
	}
	if entries, _ := os.ReadDir(filepath.Join(env.dir, "engine-artifacts")); len(entries) != 0 {
		t.Fatalf("rejected uploads left files: %v", entries)
	}
}

func TestEngineArtifactRawKeyRefused(t *testing.T) {
	env := newEngineTestEnv(t)
	// Raw 32-byte key instead of DER SPKI — the known footgun must get a
	// FIX-carrying error, not a generic failure.
	engineArtifactPubKeyOverride = make([]byte, 32)
	_, _ = rand.Read(engineArtifactPubKeyOverride)
	artifact := []byte("payload")
	code, out := env.upload("frp", "0.61.1", "amd64", "", env.sign(artifact, env.priv), artifact)
	if code != 400 {
		t.Fatalf("raw key upload: %d %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "RAW 32-byte") || !strings.Contains(msg, "DER SPKI") {
		t.Fatalf("raw-key error must name the format and the fix: %q", msg)
	}
}

func TestEngineArtifactDuplicateAndLimits(t *testing.T) {
	env := newEngineTestEnv(t)
	v1 := []byte("build-one")
	sig1 := env.sign(v1, env.priv)
	code, _ := env.upload("frp", "0.61.1", "amd64", "dup.tar.gz", sig1, v1)
	if code != 200 {
		t.Fatalf("first upload: %d", code)
	}

	// Same slot, DIFFERENT content → 409 conflict, old artifact intact.
	v2 := []byte("build-two-different")
	sig2 := env.sign(v2, env.priv)
	code, out := env.upload("frp", "0.61.1", "amd64", "", sig2, v2)
	if code != 409 {
		t.Fatalf("hash conflict must be 409, got %d %v", code, out)
	}
	arts, _ := env.store.ListEngineArtifacts()
	if len(arts) != 1 || arts[0].SHA256 != env.shaHex(v1) {
		t.Fatalf("conflicting upload must not touch the row: %+v", arts)
	}

	// Different slot (arm64) with the TAKEN name → rejected.
	code, out = env.upload("frp", "0.61.1", "arm64", "dup.tar.gz", sig2, v2)
	if code != 409 {
		t.Fatalf("name collision must be 409, got %d %v", code, out)
	}

	// Size limit: shrink the cap and re-upload a bigger body.
	engineArtifactMaxUpload = 64
	code, _ = env.upload("frp", "0.62.0", "amd64", "", env.sign(bytes.Repeat([]byte("x"), 65), env.priv),
		bytes.Repeat([]byte("x"), 65))
	if code != 400 {
		t.Fatalf("oversize upload must be 400, got %d", code)
	}
}

func TestEngineManifestFiltersAndTraversal(t *testing.T) {
	env := newEngineTestEnv(t)
	body := []byte("x")
	for _, arch := range []string{"amd64", "arm64"} {
		if code, out := env.upload("frp", "0.61.1", arch, "", env.sign(body, env.priv), body); code != 200 {
			t.Fatalf("upload %s: %d %v", arch, code, out)
		}
	}
	// goarch filter.
	resp, _ := http.Get(env.srv.URL + "/api/agent/engine-artifacts?engine=frp&goarch=arm64")
	var man map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&man)
	resp.Body.Close()
	arts := man["artifacts"].([]any)
	if len(arts) != 1 {
		t.Fatalf("goarch filter: %v", man)
	}
	// Traversal in the file endpoint is impossible (strict name gate).
	resp2, err := http.Get(env.srv.URL + "/api/agent/engine-artifacts/file/..%2Fengine-pub.b64")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 400 && resp2.StatusCode != 404 {
		t.Fatalf("traversal must not serve the key file: %d", resp2.StatusCode)
	}
	resp3, _ := http.Get(env.srv.URL + "/api/agent/engine-artifacts/file/unknown.tar.gz")
	resp3.Body.Close()
	if resp3.StatusCode != 404 {
		t.Fatalf("unknown file must be 404, got %d", resp3.StatusCode)
	}
}

func TestEngineTabCoverageWarnings(t *testing.T) {
	env := newEngineTestEnv(t)
	// Seed the registry file directly: agent "agt_a" (amd64) reports frp in
	// hello; agent "agt_b" (arm64) also reports frp — different archs give
	recs := []agentRecord{
		{ID: "agt_a", Name: "agt_a", Arch: "amd64", Status: "online"},
		{ID: "agt_b", Name: "agt_b", Arch: "arm64", Status: "online"},
	}
	data, _ := json.Marshal(agentRegistryFile{Version: 1, Agents: recs})
	if err := os.WriteFile(filepath.Join(env.dir, "agents.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"agt_a", "agt_b"} {
		if err := env.store.UpsertAgent(store.Agent{ID: id, Name: id, Status: "online"}); err != nil {
			t.Fatalf("agent row %s: %v", id, err)
		}
		if err := env.store.SetAgentV2Session(id, 2, `[{"id":"frp","version":"0.61.1"}]`); err != nil {
			t.Fatalf("v2 seed %s: %v", id, err)
		}
	}
	// Only agt_a reports engines; force the catalog read by ensuring agt_b
	// has an empty catalog (default) — but agt_b IS in the registry, so the
	// agents view shows it with an empty engine list.

	// frp enabled on 2 tunnels; NO artifact yet → per-agent warning.
	dc, _ := env.store.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "d"})
	sv1, _ := env.store.UpsertServer(store.Server{Name: "ir", Role: "iran", DatacenterID: dc})
	sv2, _ := env.store.UpsertServer(store.Server{Name: "fr", Role: "foreign"})
	topo, _ := env.store.UpsertTopology(store.Topology{Name: "t"})
	_, _ = env.store.UpsertTunnel(store.Tunnel{TunnelKey: "k1", TopologyID: topo, LocalServerID: sv1, RemoteServerID: sv2, Engine: "frp", Enabled: true})
	_, _ = env.store.UpsertTunnel(store.Tunnel{TunnelKey: "k2", TopologyID: topo, LocalServerID: sv1, RemoteServerID: sv2, Engine: "frp", Enabled: true})

	req, _ := http.NewRequest("GET", env.srv.URL+"/api/mesh/engines", nil)
	resp, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var tab struct {
		Artifacts []map[string]any    `json:"artifacts"`
		Agents    []map[string]any    `json:"agents"`
		Enabled   map[string]int      `json:"enabled"`
		Warnings  []map[string]string `json:"warnings"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&tab)
	resp.Body.Close()
	if tab.Enabled["frp"] != 2 {
		t.Fatalf("enabled counts: %v", tab.Enabled)
	}
	// agt_a reports frp but has no amd64 artifact → one warning naming it.
	found := false
	for _, w := range tab.Warnings {
		if w["agent"] == "agt_a" && w["goarch"] == "amd64" && strings.Contains(w["text"], "no signed artifact") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a missing-artifact warning for agt_a/amd64, got %v", tab.Warnings)
	}

	// Upload the amd64 artifact → the warning disappears.
	body := []byte("real-build")
	if code, out := env.upload("frp", "0.61.1", "amd64", "", env.sign(body, env.priv), body); code != 200 {
		t.Fatalf("upload: %d %v", code, out)
	}
	req2, _ := http.NewRequest("GET", env.srv.URL+"/api/mesh/engines", nil)
	resp2, _ := env.client.Do(req2)
	var tab2 struct {
		Warnings []map[string]string `json:"warnings"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&tab2)
	resp2.Body.Close()
	for _, w := range tab2.Warnings {
		if w["agent"] == "agt_a" {
			t.Fatalf("warning must clear after the arch artifact lands: %v", tab2.Warnings)
		}
	}
}

func TestEngineRoutesAreGated(t *testing.T) {
	env := newEngineTestEnv(t)
	// No session cookie → the operator endpoints refuse.
	resp, err := http.Get(env.srv.URL + "/api/mesh/engines")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Fatal("unauthenticated /api/mesh/engines must not return 200")
	}
	// POST without CSRF (fresh client WITH a session but no CSRF header)
	// must be refused: forge the session but skip the CSRF header.
	tok := newSessionToken()
	addSession(tok)
	touchSession(tok)
	req, _ := http.NewRequest("POST", env.srv.URL+"/api/mesh/engines/artifacts", strings.NewReader("x"))
	req.AddCookie(&http.Cookie{Name: "gre_session", Value: tok})
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode == 200 {
		t.Fatal("POST without CSRF must not succeed")
	}
	// Agent manifest stays unauthenticated by design (pins are public).
	resp3, err := http.Get(env.srv.URL + "/api/agent/engine-artifacts")
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != 200 {
		t.Fatalf("agent manifest must be reachable without auth: %d", resp3.StatusCode)
	}
}

func (e *engineTestEnv) shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
