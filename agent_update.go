package main

// Agent self-update (M2): download a Controller-provided artifact, verify it
// cryptographically, swap it in atomically, restart detached, and roll back
// automatically if the new binary crash-loops. No shell execution, no
// arbitrary commands: the artifact is a signed binary and every step is a
// fixed, agent-owned operation.
//
// Trust chain (mandatory, fail-closed):
//   1. sha256 must match the Controller-supplied digest (transport check).
//   2. ed25519 signature over the artifact bytes must verify against the
//      update public key pinned at build time (-X main.agentUpdatePubKeyPEM).
//      No pinned key => updates are refused entirely.
//   3. The downloaded binary must self-report the requested version.
//   4. Architecture: the artifact URL must name this agent's GOARCH.
//
// Enrollment identity and tunnel desired state live in configDir and are
// never touched by a swap.

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Test seams and small file helpers for the update runner.
var (
	execCommand = exec.Command
	timeSleep   = time.Sleep
)

// agentCurrentIdentity is the running agent's enrollment record; exactly one
// identity exists per agent process, set at runtime start.
var agentCurrentIdentity agentIdentity

func agentFileSHA(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustRead(path string) []byte {
	b, _ := os.ReadFile(path)
	return b
}

// agentUpdateSelfReportVersion writes the artifact to a temp file and runs
// its version subcommand. Only ever called after signature verification.
func agentUpdateSelfReportVersion(artifact []byte) (string, error) {
	tmp, err := os.CreateTemp("", "gre-panel-verify-")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err := tmp.Write(artifact); err != nil {
		tmp.Close()
		return "", err
	}
	tmp.Close()
	if err := os.Chmod(path, 0755); err != nil {
		return "", err
	}
	out, err := agentHostExecTimeout(path, "version", 10*time.Second)
	if err != nil {
		return "", fmt.Errorf("artifact does not run or report a version: %w", err)
	}
	v := strings.TrimSpace(out)
	if !agentUpdateVersionRe.MatchString(v) {
		return "", fmt.Errorf("artifact reports invalid version %q", v)
	}
	return v, nil
}

// agentHostExecTimeout runs a local helper command with a hard timeout
// (version self-report). Not a shell: fixed argv only.
func agentHostExecTimeout(name string, arg string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, arg).CombinedOutput()
	return string(out), err
}

func agentUpdateMarkerSave(m *agentUpdateMarker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := agentUpdateMarkerPath() + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, agentUpdateMarkerPath())
}

// agentUpdatePubKeyPEM is the PEM SubjectPublicKeyInfo (ed25519) that signs
// approved agent artifacts. Empty by default: builds without a pinned key
// refuse all updates. Set at build time:
//
//	-ldflags "-X main.agentUpdatePubKeyPEM=$(...cat update-pub.pem)"
var agentUpdatePubKeyPEM = ""

var (
	agentUpdateVersionRe = regexp.MustCompile(`^v[0-9][0-9A-Za-z._-]{0,63}$`)
	agentUpdateSHARe     = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// agentUpdateVerifyKey parses the pinned public key once per call site need;
// small and stateless.
func agentUpdateVerifyKey(pubPEM string) (ed25519.PublicKey, error) {
	block, _ := pem.Decode([]byte(pubPEM))
	if block == nil || block.Type != "PUBLIC KEY" {
		return nil, errors.New("update public key PEM missing or malformed")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("update public key not parseable: %w", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("update public key is not ed25519")
	}
	return key, nil
}

// agentUpdateVerifyArtifact enforces the mandatory artifact checks that run
// before anything touches the binary on disk: digest, signature, and (for
// the controller-side pre-verification path) size sanity.
func agentUpdateVerifyArtifact(artifact []byte, req agentUpdateRequest) error {
	if !agentUpdateVersionRe.MatchString(req.Version) {
		return errors.New("invalid target version")
	}
	if !strings.HasPrefix(req.URL, "https://") {
		return errors.New("artifact URL must be https")
	}
	if !agentUpdateSHARe.MatchString(strings.ToLower(req.SHA256Hex)) {
		return errors.New("invalid sha256 digest")
	}
	sig, err := base64.StdEncoding.DecodeString(req.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("invalid signature encoding")
	}
	sum := sha256.Sum256(artifact)
	if !strings.EqualFold(hex.EncodeToString(sum[:]), req.SHA256Hex) {
		return errors.New("artifact sha256 mismatch")
	}
	key, err := agentUpdateVerifyKey(agentUpdatePubKeyPEM)
	if err != nil {
		return fmt.Errorf("signature verification unavailable: %w", err)
	}
	if !ed25519.Verify(key, artifact, sig) {
		return errors.New("artifact signature verification FAILED")
	}
	return nil
}

// agentUpdateValidateRequest is the shared pre-flight for both the agent
// runner and the controller prepare step.
func agentUpdateValidateRequest(req agentUpdateRequest, fromController bool) error {
	if !strings.HasPrefix(req.URL, "https://") {
		return errors.New("artifact URL must be https")
	}
	if !agentUpdateVersionRe.MatchString(req.Version) || !agentUpdateSHARe.MatchString(strings.ToLower(req.SHA256Hex)) {
		return errors.New("invalid version or sha256")
	}
	// Architecture gate: the artifact must be built for this host's GOARCH.
	// The controller resolves release artifacts per agent architecture; the
	// naming convention is part of the approval.
	if !strings.Contains(req.URL, "linux-"+runtime.GOARCH) {
		return fmt.Errorf("artifact is not for this architecture (%s)", runtime.GOARCH)
	}
	if fromController {
		// The controller additionally verifies the signature itself before
		// sending; the agent re-verifies against the artifact bytes.
		if _, err := agentUpdateVerifyKey(agentUpdatePubKeyPEM); err != nil {
			return fmt.Errorf("controller cannot verify artifacts: %w", err)
		}
	}
	return nil
}

// agentUpdateDownload fetches the artifact with the agent's credentials and
// a hard size cap. url must already be https (validated).
func agentUpdateDownload(url string, id, key string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set(agentIDHeader, id)
	req.Header.Set(agentKeyHeader, key)
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("artifact download returned HTTP %d", resp.StatusCode)
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

const agentUpdateMaxArtifact = 96 << 20 // 96 MB cap

// agentUpdateSetProgress records the live update phase for the next
// heartbeat (in-memory; the controller reads it from the state report).
var (
	agentUpdateProgressMu sync.Mutex
	agentUpdateProgressSt *agentUpdateProgress
)

func agentUpdateSetProgress(phase, version, errMsg string) {
	agentUpdateProgressMu.Lock()
	agentUpdateProgressSt = &agentUpdateProgress{Phase: phase, Version: version, Error: errMsg, At: time.Now().Unix()}
	agentUpdateProgressMu.Unlock()
}

func agentUpdateProgressSnapshot() *agentUpdateProgress {
	agentUpdateProgressMu.Lock()
	defer agentUpdateProgressMu.Unlock()
	if agentUpdateProgressSt == nil {
		return nil
	}
	cp := *agentUpdateProgressSt
	return &cp
}

// agentUpdateMarker is the crash-loop rollback journal, persisted next to
// the agent state before the swap and cleared after stable uptime.
type agentUpdateMarker struct {
	Version   string `json:"version"`
	FromSHA   string `json:"from_sha256"`
	PrevPath  string `json:"prev_path"`
	BootCount int    `json:"boot_count"`
	StartedAt int64  `json:"started_at"`
}

func agentUpdateMarkerPath() string { return filepath.Join(configDir, "agent-update.json") }

func agentUpdateMarkerLoad() (*agentUpdateMarker, bool) {
	b, err := os.ReadFile(agentUpdateMarkerPath())
	if err != nil {
		return nil, false
	}
	var m agentUpdateMarker
	if json.Unmarshal(b, &m) != nil {
		return nil, false
	}
	return &m, true
}

// agentUpdateCheckBootRollback runs at agent startup: if the update marker
// exists and the new binary has crash-looped (3 boots without 5 minutes of
// stable uptime), the previous binary is restored and the failure reported.
func agentUpdateCheckBootRollback() {
	m, ok := agentUpdateMarkerLoad()
	if !ok {
		return
	}
	m.BootCount++
	if m.BootCount < 3 {
		_ = agentUpdateMarkerSave(m)
		return
	}
	// Roll back.
	prev, err := os.ReadFile(m.PrevPath)
	if err == nil {
		exe, e := os.Executable()
		if e == nil {
			tmp := exe + ".rollback"
			if os.WriteFile(tmp, prev, 0755) == nil && os.Rename(tmp, exe) == nil {
				log.Printf("[agent] update rollback: restored previous binary after %d failed boots of %s", m.BootCount, m.Version)
			}
		}
	}
	_ = os.Remove(agentUpdateMarkerPath())
	agentUpdateSetProgress("failed", m.Version, "rolled back after repeated boot failures")
}

// agentUpdateClearMarkerIfStable clears the rollback journal once the
// updated binary has been up long enough to count as healthy.
func agentUpdateClearMarkerIfStable(started time.Time) {
	if time.Since(started) < 5*time.Minute {
		return
	}
	if _, ok := agentUpdateMarkerLoad(); ok {
		_ = os.Remove(agentUpdateMarkerPath())
		agentUpdateSetProgress("completed", "", "")
	}
}
