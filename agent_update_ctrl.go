package main

// Controller side of agent updates (M2): the operator picks a version and
// artifact; the controller downloads both the artifact and its detached
// ed25519 signature, verifies the signature against the pinned update key
// BEFORE sending anything, then hands the agent the exact {url, sha256,
// signature, version} tuple. The agent re-verifies everything on its side.
// No shell execution anywhere in this flow.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// agentArtifactDir holds approved agent artifacts (uploaded by the operator,
// e.g. scp). Files are served to agents over the panel's HTTPS; integrity
// comes from the mandatory signature, not the transport.
func agentArtifactDir() string { return filepath.Join(configDir, "agent-artifacts") }

var agentArtifactNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,128}$`)

// agentUpdateSignature fetches url+".sig" (base64 ed25519 over the artifact).
func agentUpdateSignature(url string) (string, error) {
	resp, err := http.Get(url + ".sig") //nolint — url is https-validated by the caller
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New("signature file unavailable (HTTP " + itoaFast(resp.StatusCode) + ")")
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return "", err
	}
	sig := strings.TrimSpace(string(b))
	if _, err := base64.StdEncoding.DecodeString(sig); err != nil {
		return "", errors.New("signature file is not base64")
	}
	return sig, nil
}

// agentUpdateFetch downloads the artifact with a size cap.
func agentUpdateFetch(url string) ([]byte, error) {
	resp, err := http.Get(url) //nolint — https enforced by the caller
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("artifact download returned HTTP " + itoaFast(resp.StatusCode))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, agentUpdateMaxArtifact+1))
	if err != nil {
		return nil, err
	}
	if len(data) > agentUpdateMaxArtifact {
		return nil, errors.New("artifact exceeds the size cap")
	}
	return data, nil
}

func itoaFast(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// agentUpdatePrepareFor resolves and verifies the artifact for one agent
// (architecture-aware) and sends the agent.update command. Exported logic
// used by the HTTP handler and by group updates.
func agentUpdatePrepareFor(agentID, version, url string) (agentCommandResult, error) {
	if !agentUpdateVersionRe.MatchString(version) {
		return agentCommandResult{}, errors.New("invalid target version")
	}
	if !strings.HasPrefix(url, "https://") {
		return agentCommandResult{}, errors.New("artifact URL must be https")
	}
	// Architecture gate against the agent's registered arch.
	agentRegMu.Lock()
	agents, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		return agentCommandResult{}, errors.New("cannot read the agent registry")
	}
	var arch string
	found := false
	for _, a := range agents {
		if a.ID == agentID {
			if a.Revoked {
				return agentCommandResult{}, errors.New("agent is revoked")
			}
			arch, found = a.Arch, true
			break
		}
	}
	if !found {
		return agentCommandResult{}, errors.New("agent not found")
	}
	if arch != "" && !strings.Contains(url, "linux-"+arch) {
		return agentCommandResult{}, errors.New("artifact is not built for the agent architecture (" + arch + ")")
	}

	artifact, err := agentUpdateFetch(url)
	if err != nil {
		return agentCommandResult{}, errors.New("artifact download failed: " + err.Error())
	}
	sig, err := agentUpdateSignature(url)
	if err != nil {
		return agentCommandResult{}, errors.New("signature fetch failed: " + err.Error())
	}
	req := agentUpdateRequest{Version: version, URL: url, Signature: sig}
	if err := agentUpdateVerifyArtifact(artifact, req); err != nil {
		// sha256 comes from the artifact itself after signature verification:
		// the signature IS the approval; the digest is computed here so the
		// agent can re-check transport integrity independently.
		return agentCommandResult{}, err
	}
	sum := sha256HexBytes(artifact)
	req.SHA256Hex = sum

	res, err := agentHubCommand(agentID, agentCmdAgentUpdate, map[string]string{
		"version":   req.Version,
		"url":       req.URL,
		"sha256":    req.SHA256Hex,
		"signature": req.Signature,
	}, agentCommandTimeout)
	if err != nil {
		return agentCommandResult{}, errors.New("agent did not accept the update: " + err.Error())
	}
	return res, nil
}

func sha256HexBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// handleAgentUpdatePost initiates a signed agent update:
// POST /api/agents/{id}/update {"version":"v1.28.1","url":"https://…"}
func handleAgentUpdatePost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var q struct {
		Version string `json:"version"`
		URL     string `json:"url"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&q); err != nil {
		agentAPIError(w, r, "E-AGENT-05", http.StatusBadRequest, "body must be {\"version\",\"url\"}")
		return
	}
	if agentUpdatePubKeyPEM == "" {
		agentAPIError(w, r, "E-AGENT-07", http.StatusPreconditionFailed, "no update public key is pinned in this build; agent updates are disabled (fail closed)")
		return
	}
	res, err := agentUpdatePrepareFor(id, strings.TrimSpace(q.Version), strings.TrimSpace(q.URL))
	if err != nil {
		LogSecurityAudit("agent_update_rejected", cfg.Username, ClientIP(r), "agent="+id+" "+err.Error())
		agentAPIError(w, r, "E-AGENT-07", http.StatusBadRequest, err.Error())
		return
	}
	LogSecurityAudit("agent_update_started", cfg.Username, ClientIP(r), "agent="+id)
	writeJSON(w, map[string]any{"status": "started", "command_result": res, "note": "progress is visible in the agent's state; the new version appears after the agent reconnects"})
}

// handleAgentArtifactGet serves an approved artifact from
// <configDir>/agent-artifacts/<name> over HTTPS. Unauthenticated by design:
// agent artifacts are release binaries whose integrity is enforced by the
// mandatory ed25519 signature and digest checks on both controller and agent.
// Path traversal is impossible: the name is strictly validated and joined
// into a dedicated directory.
func handleAgentArtifactGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !agentArtifactNameRe.MatchString(name) {
		http.Error(w, "invalid artifact name", http.StatusBadRequest)
		return
	}
	path := filepath.Join(agentArtifactDir(), name)
	if filepath.Clean(path) != filepath.Join(agentArtifactDir(), filepath.Base(path)) {
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
	w.Header().Set("Content-Length", itoaFast(int(fi.Size())))
	_, _ = io.Copy(w, f)
}
