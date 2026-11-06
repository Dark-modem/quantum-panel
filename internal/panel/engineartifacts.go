// engineartifacts.go — Phase 5 R5: the panel-side engine artifact store.
// The operator uploads a signed engine binary (multipart, behind
// requireAuth + requireCSRF like every mesh mutation); the panel
// recomputes sha256, VERIFIES the detached ed25519 signature against the
// configured engine-artifact public key (base64 DER SPKI, same format as
// update-pub.b64), enforces a strict download-name pattern and size cap,
// stores the file atomically with 0600 and audits ids only.
//
// Agents read an unauthenticated manifest (same trust model as the
// existing /api/agent/artifact/{name} endpoint: integrity comes from the
// sha256 + ed25519 pair the agent verifies itself — the panel is never
// trusted for integrity) and download the bytes by name.
//
// Route table (registered by registerEngineArtifactRoutes):
//
//	GET     /api/mesh/engines                        (operator) composite tab view
//	GET     /api/mesh/engines/keysnippet             (operator) pinned-key snippet
//	GET     /api/mesh/engines/artifacts              (operator) list rows
//	POST    /api/mesh/engines/artifacts              (operator) upload (+CSRF)
//	DELETE  /api/mesh/engines/artifacts/{id}         (operator) remove (+CSRF)
//	GET     /api/agent/engine-artifacts              (agent)    manifest (unauth)
//	GET     /api/agent/engine-artifacts/file/{name}  (agent)    bytes (unauth)

package panel

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// engineArtifactPubKeyPinned is the engine-artifact signing public key,
// base64 DER SPKI (identical format to the agent's pinned
// engineArtifactPubKeyB64 and to update-pub.b64). Populated at build time
// via -ldflags -X, or at runtime by the file engine-pub.b64 inside the
// engine-artifacts directory (runtime file wins — it lets the operator
// rotate without a rebuild).
var engineArtifactPubKeyPinned = ""

// engineArtifactPubKeyOverride is the test seam (nil in production).
var engineArtifactPubKeyOverride []byte

const (
	// engineArtifactMaxHeadroom is extra multipart overhead allowed on
	// top of the artifact cap when clamping the request body.
	engineArtifactMaxHeadroom = 1 << 20
)

// engineArtifactMaxUpload caps one artifact (frp tar.gz ≈ 13 MB;
// 256 MiB leaves room for any real engine bundle while keeping the
// panel's disk safe). Var (not const) so tests can shrink it.
var engineArtifactMaxUpload = int64(256 << 20)

var (
	// engineArtifactNameRe: strict download name — lowercase, no path
	// separators, no leading dot, so filepath traversal is impossible by
	// construction (Clean==Base is checked again at serve time).
	engineArtifactNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	// engineArtifactEngineRe: engine id as agents report it in hello.
	engineArtifactEngineRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	// engineArtifactVersionRe: operator version tag (0.61.1, v1.2.3-rc1…).
	engineArtifactVersionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)
	engineArtifactArchValid = map[string]bool{"amd64": true, "arm64": true, "arm": true, "386": true}
	engineArtifactOSValid   = map[string]bool{"linux": true}
)

func engineArtifactDir() string { return filepath.Join(configDir, "engine-artifacts") }
func enginePubKeyPath() string  { return filepath.Join(engineArtifactDir(), "engine-pub.b64") }

// engineArtifactNameIsOK is the single name gate used at upload AND at
// serve time: strict pattern, no separators, Clean-stable.
func engineArtifactNameIsOK(name string) bool {
	return engineArtifactNameRe.MatchString(name) &&
		filepath.Clean(name) == name &&
		!strings.Contains(name, "/") && !strings.Contains(name, "\\")
}

// engineArtifactVerifyKey loads and parses the configured public key.
// Precedence: test override → runtime file engine-pub.b64 → build pin.
// Errors name the FIX, not just the failure — the raw-vs-SPKI confusion
// is the known footgun (FRP-ENGINE-INTEGRATION-REVIEW §6).
func engineArtifactVerifyKey() (ed25519.PublicKey, error) {
	if len(engineArtifactPubKeyOverride) > 0 {
		return parseEngineSPKIKey(engineArtifactPubKeyOverride)
	}
	b64 := engineArtifactPubKeyPinned
	if data, err := os.ReadFile(enginePubKeyPath()); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		b64 = string(data)
	} else if b64 == "" {
		return nil, errors.New("no engine-artifact public key configured: write the base64 DER SPKI to " +
			enginePubKeyPath() + " (see docs/ENGINE-ARTIFACTS.md)")
	}
	der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, errors.New("engine-artifact public key is not valid base64; expected base64 of the DER SPKI form")
	}
	return parseEngineSPKIKey(der)
}

// parseEngineSPKIKey decodes DER SPKI → ed25519 public key, with the
// raw-key case called out explicitly (a bare 32-byte input is a RAW
// ed25519 key, which this panel refuses — sign/export flow in
// docs/ENGINE-ARTIFACTS.md produces DER SPKI).
func parseEngineSPKIKey(der []byte) (ed25519.PublicKey, error) {
	if len(der) == ed25519.PublicKeySize {
		return nil, errors.New("engine-artifact public key looks like a RAW 32-byte ed25519 key; this panel expects the base64 DER SPKI form — export it with: openssl pkey -pubin -in key.pub -outform DER | base64 -w0")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("engine-artifact public key is not parseable DER SPKI: %v", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("engine-artifact public key is not an ed25519 key")
	}
	return key, nil
}

func registerEngineArtifactRoutes(mux *http.ServeMux, base string) {
	p := base + "/api/mesh/engines"
	mux.HandleFunc("GET "+p, requireAuth(handleEngineTab))
	mux.HandleFunc("GET "+p+"/keysnippet", requireAuth(handleEngineKeySnippet))
	mux.HandleFunc("GET "+p+"/artifacts", requireAuth(handleEngineArtifactList))
	mux.HandleFunc("POST "+p+"/artifacts", requireAuth(requireCSRF(handleEngineArtifactUpload)))
	mux.HandleFunc("DELETE "+p+"/artifacts/{id}", requireAuth(requireCSRF(handleEngineArtifactDelete)))

	ap := base + "/api/agent/engine-artifacts"
	mux.HandleFunc("GET "+ap, handleEngineManifest)                    // unauth by design (see header)
	mux.HandleFunc("GET "+ap+"/file/{name}", handleEngineManifestFile) // unauth by design
}

// handleEngineTab is the composite view the /mesh Engines tab renders:
// uploaded artifacts, per-agent engine catalogs (from hello, R3), the
// enabled-engine counts, and the coverage warnings.
func handleEngineTab(w http.ResponseWriter, r *http.Request) {
	s, err := MeshStore()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "mesh store unavailable: "+err.Error())
		return
	}
	arts, err := s.ListEngineArtifacts()
	if err != nil {
		writeAPIError(w, r, "E-IN-05", "cannot list artifacts: "+err.Error())
		return
	}
	enabled, err := s.EngineEnabledCounts()
	if err != nil {
		writeAPIError(w, r, "E-IN-05", "cannot count enabled tunnels: "+err.Error())
		return
	}
	agents := engineAgentsView(s)
	writeJSON(w, map[string]any{
		"artifacts":      arts,
		"agents":         agents,
		"enabled":        enabled,
		"warnings":       engineCoverageWarnings(arts, enabled, agents),
		"key_configured": engineKeyConfigured(),
	})
}

// engineAgentsView lists every non-revoked registry agent with its arch
// and the engine catalog it reported in hello (agents.engine_catalog).
func engineAgentsView(s *store.Store) []map[string]any {
	agentRegMu.Lock()
	recs, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		log.Printf("[engines] cannot read agent registry: %v", err)
		return []map[string]any{}
	}
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		if rec.Revoked {
			continue
		}
		engines := []map[string]any{}
		if v, err := s.AgentV2(rec.ID); err == nil && strings.TrimSpace(v.EngineCatalog) != "" {
			var entries []struct {
				ID      string `json:"id"`
				Version string `json:"version"`
			}
			if json.Unmarshal([]byte(v.EngineCatalog), &entries) == nil {
				for _, e := range entries {
					if e.ID != "" {
						engines = append(engines, map[string]any{"id": e.ID, "version": e.Version})
					}
				}
			}
		}
		out = append(out, map[string]any{
			"id": rec.ID, "name": rec.Name, "arch": rec.Arch, "os": rec.OS,
			"status": rec.Status, "engines": engines,
		})
	}
	return out
}

// engineCoverageWarnings is the honest gap report: for every engine that
// enabled tunnels use, every connected agent that reports running it must
// have a signed artifact for its own arch (R5's operator-facing point).
func engineCoverageWarnings(arts []store.EngineArtifact, enabled map[string]int, agents []map[string]any) []map[string]string {
	have := map[string]bool{} // "engine|goarch"
	for _, a := range arts {
		have[a.Engine+"|"+a.Goarch] = true
	}
	warns := []map[string]string{}
	for engine, n := range enabled {
		anyAgent := false
		for _, ag := range agents {
			engines, _ := ag["engines"].([]map[string]any)
			reports := false
			for _, e := range engines {
				if id, _ := e["id"].(string); id == engine {
					reports = true
					break
				}
			}
			if !reports {
				continue
			}
			anyAgent = true
			arch, _ := ag["arch"].(string)
			if arch == "" {
				arch = "unknown"
			}
			if !have[engine+"|"+arch] {
				warns = append(warns, map[string]string{
					"engine": engine,
					"agent":  fmt.Sprintf("%v", ag["id"]),
					"goarch": arch,
					"text":   fmt.Sprintf("engine %s enabled on %d tunnels but agent %s has no signed artifact for %s", engine, n, ag["id"], arch),
				})
			}
		}
		if !anyAgent {
			warns = append(warns, map[string]string{
				"engine": engine, "agent": "", "goarch": "",
				"text": fmt.Sprintf("engine %s enabled on %d tunnels but no connected agent reports it", engine, n),
			})
		}
	}
	return warns
}

func engineKeyConfigured() bool {
	if len(engineArtifactPubKeyOverride) > 0 || engineArtifactPubKeyPinned != "" {
		return true
	}
	data, err := os.ReadFile(enginePubKeyPath())
	return err == nil && len(strings.TrimSpace(string(data))) > 0
}

// handleEngineKeySnippet renders the exact build snippet the operator
// needs to pin the CURRENT key into agent builds (the agent verifies
// artifacts against its own pinned copy — the two must match).
func handleEngineKeySnippet(w http.ResponseWriter, r *http.Request) {
	if !engineKeyConfigured() {
		writeJSON(w, map[string]any{
			"configured": false,
			"hint": "no engine-artifact public key configured — write the base64 DER SPKI to " +
				enginePubKeyPath() + " (docs/ENGINE-ARTIFACTS.md walks through key generation and signing)",
		})
		return
	}
	if _, err := engineArtifactVerifyKey(); err != nil {
		writeJSON(w, map[string]any{"configured": false, "hint": err.Error()})
		return
	}
	b64 := currentEngineKeyB64()
	writeJSON(w, map[string]any{
		"configured":    true,
		"key_b64":       b64,
		"agent_ldflags": "-ldflags \"-X github.com/Dark-modem/quantum-agent/internal/agent.engineArtifactPubKeyB64=" + b64 + "\"",
		"panel_file":    enginePubKeyPath(),
	})
}

// currentEngineKeyB64 returns the key exactly as configured (file first,
// then pin, then test override) so the snippet matches what the panel
// verifies with.
func currentEngineKeyB64() string {
	if data, err := os.ReadFile(enginePubKeyPath()); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		return strings.TrimSpace(string(data))
	}
	if len(engineArtifactPubKeyOverride) > 0 {
		return base64.StdEncoding.EncodeToString(engineArtifactPubKeyOverride)
	}
	return engineArtifactPubKeyPinned
}

func handleEngineArtifactList(w http.ResponseWriter, r *http.Request) {
	s, err := MeshStore()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "mesh store unavailable: "+err.Error())
		return
	}
	arts, err := s.ListEngineArtifacts()
	if err != nil {
		writeAPIError(w, r, "E-IN-05", "cannot list artifacts: "+err.Error())
		return
	}
	writeJSON(w, map[string]any{"artifacts": arts})
}

// handleEngineArtifactUpload implements POST /api/mesh/engines/artifacts:
// multipart with fields engine, version, goarch, goos (optional, linux),
// name (optional, derived when empty), signature (base64 ed25519 over
// the exact artifact bytes) and file.
func handleEngineArtifactUpload(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > engineArtifactMaxUpload+engineArtifactMaxHeadroom {
		writeAPIError(w, r, "E-IN-01", "artifact too large (limit "+strconv.FormatInt(engineArtifactMaxUpload>>20, 10)+" MiB)")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, engineArtifactMaxUpload+engineArtifactMaxHeadroom)
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeAPIError(w, r, "E-IN-01", "bad multipart upload: "+err.Error())
		return
	}
	defer func() { _ = r.MultipartForm.RemoveAll() }()

	engine := strings.TrimSpace(r.FormValue("engine"))
	version := strings.TrimSpace(r.FormValue("version"))
	goarch := strings.TrimSpace(r.FormValue("goarch"))
	goos := strings.TrimSpace(r.FormValue("goos"))
	if goos == "" {
		goos = "linux"
	}
	name := strings.TrimSpace(r.FormValue("name"))
	sigB64 := strings.TrimSpace(r.FormValue("signature"))

	if !engineArtifactEngineRe.MatchString(engine) {
		writeAPIError(w, r, "E-IN-04", "invalid engine id (lowercase letters, digits, -, _; starts with a letter)")
		return
	}
	if !engineArtifactVersionRe.MatchString(version) {
		writeAPIError(w, r, "E-IN-04", "invalid version tag")
		return
	}
	if !engineArtifactArchValid[goarch] {
		writeAPIError(w, r, "E-IN-04", "unsupported goarch (amd64, arm64, arm, 386)")
		return
	}
	if !engineArtifactOSValid[goos] {
		writeAPIError(w, r, "E-IN-04", "unsupported goos (linux)")
		return
	}
	if name == "" {
		name = engine + "-" + version + "-" + goos + "-" + goarch + ".tar.gz"
	}
	if !engineArtifactNameIsOK(name) {
		writeAPIError(w, r, "E-IN-04", "invalid artifact name (lowercase letters, digits, ., _, -; no path separators)")
		return
	}
	sig, err := base64.StdEncoding.DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		writeAPIError(w, r, "E-IN-04", "signature must be base64 of the 64-byte ed25519 signature over the artifact bytes")
		return
	}
	key, err := engineArtifactVerifyKey()
	if err != nil {
		writeAPIError(w, r, "E-IN-05", err.Error())
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, r, "E-IN-01", "missing artifact file field")
		return
	}
	defer file.Close()
	if header.Size > engineArtifactMaxUpload {
		writeAPIError(w, r, "E-IN-01", "artifact too large (limit "+strconv.FormatInt(engineArtifactMaxUpload>>20, 10)+" MiB)")
		return
	}

	if err := os.MkdirAll(engineArtifactDir(), 0o700); err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot create engine-artifacts dir: "+err.Error())
		return
	}
	tmp, err := os.CreateTemp(engineArtifactDir(), ".upload-*")
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
	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(file, engineArtifactMaxUpload+1))
	if err != nil {
		writeAPIError(w, r, "E-IN-01", "artifact copy failed: "+err.Error())
		return
	}
	if size > engineArtifactMaxUpload {
		writeAPIError(w, r, "E-IN-01", "artifact too large (limit "+strconv.FormatInt(engineArtifactMaxUpload>>20, 10)+" MiB)")
		return
	}
	if err := tmp.Close(); err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot close temp file: "+err.Error())
		return
	}
	shaHex := hex.EncodeToString(hasher.Sum(nil))

	// Signature over the EXACT bytes: read the temp file back (bounded —
	// we just wrote it and know its size) and verify.
	body, err := os.ReadFile(tmpName)
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot re-read artifact: "+err.Error())
		return
	}
	if !ed25519.Verify(key, body, sig) {
		writeAPIError(w, r, "E-IN-04", "artifact signature verification FAILED (wrong key or wrong signature over the bytes)")
		return
	}

	s, err := MeshStore()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "mesh store unavailable: "+err.Error())
		return
	}
	uploader := cfg.Username
	row := store.EngineArtifact{
		Engine: engine, Version: version, Goos: goos, Goarch: goarch,
		Artifact: name, SHA256: shaHex, Size: size,
		Signature: base64.StdEncoding.EncodeToString(sig),
		Uploader:  uploader, UploadedAt: time.Now().Unix(),
	}
	id, err := s.UpsertEngineArtifact(row)
	if err != nil {
		if errors.Is(err, store.ErrArtifactHashConflict) {
			writeAPIError(w, r, "E-ENG-01", "this engine+version+arch slot already holds a DIFFERENT build (different sha256) — pick a new version or remove the old artifact first")
			return
		}
		if strings.Contains(err.Error(), "already in use") {
			writeAPIError(w, r, "E-ENG-02", "artifact download name is taken by another artifact")
			return
		}
		writeAPIError(w, r, "E-IN-05", "cannot store artifact row: "+err.Error())
		return
	}
	// Row committed: move the file into place atomically (same dir, same
	// filesystem; 0600 already set). If the move fails the row goes too.
	finalPath := filepath.Join(engineArtifactDir(), name)
	if err := os.Rename(tmpName, finalPath); err != nil {
		_ = s.DeleteEngineArtifact(id)
		writeAPIError(w, r, "E-SYS-01", "cannot store artifact file: "+err.Error())
		return
	}
	cleanup = false
	// Audit ids only — never artifact contents.
	LogSecurityAudit("engine_artifact_uploaded", uploader, ClientIP(r),
		fmt.Sprintf("id=%d engine=%s version=%s goos=%s goarch=%s name=%s sha256=%s size=%d",
			id, engine, version, goos, goarch, name, shaHex, size))
	writeJSON(w, map[string]any{"status": "stored", "id": id, "artifact": name, "sha256": shaHex, "size": size})
}

// handleEngineArtifactDelete removes a row + its file (operator housekeeping).
func handleEngineArtifactDelete(w http.ResponseWriter, r *http.Request) {
	s, err := MeshStore()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "mesh store unavailable: "+err.Error())
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeAPIError(w, r, "E-IN-04", "bad artifact id")
		return
	}
	row, err := s.GetEngineArtifact(id)
	if err != nil {
		writeAPIError(w, r, "E-IN-04", "no such artifact")
		return
	}
	if err := s.DeleteEngineArtifact(id); err != nil {
		writeAPIError(w, r, "E-IN-05", "cannot delete: "+err.Error())
		return
	}
	_ = os.Remove(filepath.Join(engineArtifactDir(), row.Artifact))
	LogSecurityAudit("engine_artifact_deleted", cfg.Username, ClientIP(r),
		fmt.Sprintf("id=%d engine=%s version=%s goarch=%s name=%s", id, row.Engine, row.Version, row.Goarch, row.Artifact))
	writeJSON(w, map[string]any{"status": "deleted"})
}

// handleEngineManifest serves the agent-facing manifest (R5): everything
// an agent needs to pick, download and SELF-verify an artifact. No auth:
// only public pins ride this endpoint (engine, version, sha256,
// signature) — integrity stays with the sha256+ed25519 pair the agent
// enforces against its build-pinned key. Same trust model as
// handleAgentArtifactGet.
func handleEngineManifest(w http.ResponseWriter, r *http.Request) {
	s, err := MeshStore()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "store unavailable")
		return
	}
	arts, err := s.ListEngineArtifacts()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot read artifacts")
		return
	}
	engine := strings.TrimSpace(r.URL.Query().Get("engine"))
	goarch := strings.TrimSpace(r.URL.Query().Get("goarch"))
	out := []map[string]any{}
	for _, a := range arts {
		if engine != "" && a.Engine != engine {
			continue
		}
		if goarch != "" && a.Goarch != goarch {
			continue
		}
		out = append(out, map[string]any{
			"engine": a.Engine, "version": a.Version, "goos": a.Goos, "goarch": a.Goarch,
			"artifact": a.Artifact, "sha256": a.SHA256, "size": a.Size, "signature": a.Signature,
		})
	}
	writeJSON(w, map[string]any{"artifacts": out})
}

// handleEngineManifestFile serves the artifact bytes by name — strict
// name validation + Clean==Base, mirroring handleAgentArtifactGet.
func handleEngineManifestFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !engineArtifactNameIsOK(name) {
		http.Error(w, "invalid artifact name", http.StatusBadRequest)
		return
	}
	dir := engineArtifactDir()
	path := filepath.Join(dir, name)
	if filepath.Clean(path) != filepath.Join(dir, filepath.Base(path)) {
		http.Error(w, "invalid artifact name", http.StatusBadRequest)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	_, _ = io.Copy(w, f)
}
