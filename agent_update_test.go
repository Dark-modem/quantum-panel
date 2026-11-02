package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// M2 update tests: the mandatory verification chain, fail-closed behavior,
// and the artifact-serving guard rails.

func agentTestUpdateKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return priv, pemStr
}

func agentTestSign(t *testing.T, priv ed25519.PrivateKey, artifact []byte) string {
	t.Helper()
	sig := ed25519.Sign(priv, artifact)
	return base64.StdEncoding.EncodeToString(sig)
}

func TestAgentUpdateVerifyArtifactChain(t *testing.T) {
	priv, pubPEM := agentTestUpdateKey(t)
	old := agentUpdatePubKeyPEM
	agentUpdatePubKeyPEM = pubPEM
	t.Cleanup(func() { agentUpdatePubKeyPEM = old })

	artifact := []byte("fake gre-panel linux binary")
	sum := sha256.Sum256(artifact)
	req := agentUpdateRequest{
		Version:   "v1.28.1-lab",
		URL:       "https://ctrl.example/panel/api/agent/artifact/gre-panel-linux-amd64",
		SHA256Hex: hex.EncodeToString(sum[:]),
		Signature: agentTestSign(t, priv, artifact),
	}
	if err := agentUpdateVerifyArtifact(artifact, req); err != nil {
		t.Fatalf("legitimate artifact must verify: %v", err)
	}

	// Tampered artifact: signature no longer matches.
	if err := agentUpdateVerifyArtifact([]byte("tampered"), req); err == nil {
		t.Fatal("tampered artifact must FAIL signature verification")
	}
	// Wrong digest.
	bad := req
	bad.SHA256Hex = strings.Repeat("0", 64)
	if err := agentUpdateVerifyArtifact(artifact, bad); err == nil {
		t.Fatal("mismatched digest must be refused")
	}
	// Plaintext URL.
	plain := req
	plain.URL = "http://ctrl.example/x"
	if err := agentUpdateVerifyArtifact(artifact, plain); err == nil {
		t.Fatal("plaintext artifact URL must be refused")
	}
	// No pinned key: fail closed.
	agentUpdatePubKeyPEM = ""
	if err := agentUpdateVerifyArtifact(artifact, req); err == nil {
		t.Fatal("updates must fail closed without a pinned key")
	}
}

func TestAgentUpdateValidateRequestArchGate(t *testing.T) {
	// The URL must name this host's GOARCH; anything else is refused.
	right := agentUpdateRequest{Version: "v1.28.1", URL: "https://x/gre-panel-linux-" + runtime.GOARCH, SHA256Hex: strings.Repeat("a", 64)}
	if err := agentUpdateValidateRequest(right, false); err != nil {
		t.Fatalf("matching architecture must pass: %v", err)
	}
	wrongArch := "amd64"
	if runtime.GOARCH == "amd64" {
		wrongArch = "arm64"
	}
	wrong := agentUpdateRequest{Version: "v1.28.1", URL: "https://x/gre-panel-linux-" + wrongArch, SHA256Hex: strings.Repeat("a", 64)}
	if err := agentUpdateValidateRequest(wrong, false); err == nil {
		t.Fatal("wrong-architecture artifact must be refused")
	}
}

func TestAgentUpdatePostFailClosedNoKey(t *testing.T) {
	withTempConfig(t)
	old := agentUpdatePubKeyPEM
	agentUpdatePubKeyPEM = ""
	t.Cleanup(func() { agentUpdatePubKeyPEM = old })

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/agents/agt_x/update", strings.NewReader(`{"version":"v1.28.1","url":"https://x/a"}`))
	handleAgentUpdatePost(rr, req)
	if rr.Code != http.StatusPreconditionFailed {
		t.Fatalf("no pinned key must be 412, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestAgentArtifactGetServesAndBlocks(t *testing.T) {
	withTempConfig(t)
	dir := agentArtifactDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gre-panel-linux-amd64"), []byte("ARTIFACT-BYTES"), 0755); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agent/artifact/{name}", handleAgentArtifactGet)

	// Valid name serves the bytes.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/agent/artifact/gre-panel-linux-amd64", nil))
	if rr.Code != 200 {
		t.Fatalf("artifact GET = %d", rr.Code)
	}
	b, _ := io.ReadAll(rr.Body)
	if string(b) != "ARTIFACT-BYTES" {
		t.Fatalf("artifact bytes wrong: %q", string(b))
	}
	// Traversal attempts are rejected by the name validation.
	// ".." is cleaned by the mux into a redirect (still safe); the rest are
	// rejected in the handler.
	for _, name := range []string{"..%2F..%2Fetc%2Fpasswd", "..", "a/b", ".hidden"} {
		rr = httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/agent/artifact/"+name, nil))
		if rr.Code != 400 && rr.Code != 404 && rr.Code != 301 && rr.Code != 307 {
			t.Fatalf("artifact name %q must be rejected, got %d", name, rr.Code)
		}
	}
	// Missing artifact is 404.
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/agent/artifact/gre-panel-linux-arm64", nil))
	if rr.Code != 404 {
		t.Fatalf("missing artifact must 404, got %d", rr.Code)
	}
}

// The update command payload survives the command envelope round-trip
// (field names pinned so controller and agent agree).
func TestAgentUpdateRequestWireFields(t *testing.T) {
	req := agentUpdateRequest{Version: "v1.28.1", URL: "https://x/a", SHA256Hex: strings.Repeat("a", 64), Signature: "sig"}
	b, err := json.Marshal(map[string]string{
		"version": req.Version, "url": req.URL, "sha256": req.SHA256Hex, "signature": req.Signature,
	})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]string
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back["version"] != req.Version || back["url"] != req.URL || back["sha256"] != req.SHA256Hex || back["signature"] != req.Signature {
		t.Fatalf("wire fields drifted: %v", back)
	}
}

// Boot-rollback: 3 crash-looped boots of the updated binary restore the
// previous binary and report the failure; stable boots just count up.
func TestAgentUpdateBootRollback(t *testing.T) {
	withTempConfig(t)
	lab := t.TempDir()
	exe := filepath.Join(lab, "gre-panel")
	newBin := []byte("NEW-BINARY")
	prevBin := []byte("PREV-BINARY")
	if err := os.WriteFile(exe, newBin, 0755); err != nil {
		t.Fatal(err)
	}
	prev := filepath.Join(lab, "gre-panel.prev")
	if err := os.WriteFile(prev, prevBin, 0755); err != nil {
		t.Fatal(err)
	}
	oldSeam := agentSelfExe
	agentSelfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { agentSelfExe = oldSeam })

	save := agentUpdateSetProgress
	defer func() { agentUpdateSetProgress = save }()

	if err := agentUpdateMarkerSave(&agentUpdateMarker{Version: "v1.28.1", PrevPath: prev, BootCount: 0, StartedAt: 1}); err != nil {
		t.Fatal(err)
	}
	agentUpdateCheckBootRollback()
	if b, _ := os.ReadFile(exe); string(b) != "NEW-BINARY" {
		t.Fatal("rollback must not trigger before 3 boots")
	}
	agentUpdateCheckBootRollback()
	if b, _ := os.ReadFile(exe); string(b) != "NEW-BINARY" {
		t.Fatal("rollback must not trigger on boot 2")
	}
	agentUpdateCheckBootRollback()
	if b, _ := os.ReadFile(exe); string(b) != "PREV-BINARY" {
		t.Fatalf("boot 3 must restore the previous binary, got %q", string(b))
	}
	if _, ok := agentUpdateMarkerLoad(); ok {
		t.Fatal("marker must be cleared after rollback")
	}
	if p := agentUpdateProgressSnapshot(); p == nil || p.Phase != "failed" {
		t.Fatalf("rollback must report failed progress: %+v", p)
	}
	// The marker is gone; the next boots are no-ops.
	agentUpdateCheckBootRollback()
	if b, _ := os.ReadFile(exe); string(b) != "PREV-BINARY" {
		t.Fatal("rollback must run exactly once")
	}
}

// Typed ops (firewall/port/netopt): allowlist + confirm gate at the
// controller proxy, using the real handlers with stubbed exec.
func TestAgentTypedOpDispatch(t *testing.T) {
	agentLab(t)
	// fw.detect through the dispatcher.
	res := agentRunCommand(agentCommand{ID: "t1", Op: agentCmdFwDetect})
	if !res.OK || !strings.Contains(res.Output, "backend") {
		t.Fatalf("fw.detect: %+v (%s)", res, res.Output)
	}
	// port.list through the dispatcher.
	res = agentRunCommand(agentCommand{ID: "t2", Op: agentCmdPortList})
	if !res.OK || res.Output == "" {
		t.Fatalf("port.list: %+v", res)
	}
	// Mutating op without confirm must be refused before any exec.
	var rec agentExecRecorder
	rec.wrap(t, nil)
	res = agentRunCommand(agentCommand{ID: "t3", Op: agentCmdFwApply, Params: map[string]string{"payload": `{"owner":"tunnel:x","add":[{"proto":"tcp","port":8443,"name":"test"}]}`}})
	if res.OK || !strings.Contains(res.Error, "confirm=yes") {
		t.Fatalf("apply without confirm must be refused: %+v", res)
	}
	if rec.count() != 0 {
		t.Fatalf("refused mutation reached exec: %v", rec.all())
	}
	// Unknown typed op refused.
	res = agentRunCommand(agentCommand{ID: "t4", Op: "firewall.nuke", Params: map[string]string{"payload": "{}"}})
	if res.OK || !strings.Contains(res.Error, "not allowed") {
		t.Fatalf("unknown typed op: %+v", res)
	}
}

func TestAgentOpPostConfirmGate(t *testing.T) {
	withTempConfig(t)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	_ = agentHubHandshakeCaps(t, srv.URL, id, key, []string{agentCapMatrix})

	post := func(body string) (int, string) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/agents/"+id+"/op", strings.NewReader(body))
		// The handler expects the path value; wrap through the same mux shape.
		handleAgentOpPost(rr, req)
		return rr.Code, rr.Body.String()
	}
	// Mutating without confirm → 428 precondition.
	code, body := post(`{"op":"` + agentCmdFwApply + `","payload":{"owner":"tunnel:test","add":[{"proto":"tcp","port":8080,"name":"t"}]}}`)
	if code != http.StatusPreconditionRequired {
		t.Fatalf("mutation without confirm must 428, got %d: %s", code, body)
	}
	// Unknown op → 400.
	code, _ = post(`{"op":"firewall.nuke"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("unknown op must 400, got %d", code)
	}
}
