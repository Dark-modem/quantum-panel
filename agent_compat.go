package main

// Agent management-plane compatibility: the controller needs these
// definitions to manage agents (enrollment, updates, status checks).
// The agent RUNTIME lives in the separate quantum-agent project.

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// agentIdentityPath returns where the agent stores its identity on the
// managed endpoint. The controller checks this to detect agent installation.
func agentIdentityPath() string {
	return filepath.Join(configDir, "agent-identity.json")
}

// agentStatePath returns where the agent stores its desired state.
func agentStatePath() string {
	return filepath.Join(configDir, "agent-state.json")
}

// agentUpdateMaxArtifact caps agent update artifact size (64 MB).
const agentUpdateMaxArtifact = 64 << 20

// agentUpdateVersionRe matches valid version markers (e.g. v1.29.0).
var agentUpdateVersionRe = regexp.MustCompile(`^v[0-9][0-9A-Za-z._-]{0,63}$`)

// agentUpdateSHARe matches a lowercase-or-uppercase sha256 hex digest.
var agentUpdateSHARe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// agentUpdatePubKeyPEM is the update-signing public key (pinned at build time).
// The controller embeds this to verify update artifacts before dispatching
// them to agents. Agent-side verification uses the same key.
var agentUpdatePubKeyPEM = "" // populated by build or settings

// agentUpdateVerifyKey parses the pinned update public key. Two accepted
// forms: PEM SubjectPublicKeyInfo, or single-line base64 of the DER
// SubjectPublicKeyInfo (ldflags -X cannot carry newlines).
func agentUpdateVerifyKey(pubPEM string) (ed25519.PublicKey, error) {
	pubPEM = strings.TrimSpace(pubPEM)
	if pubPEM == "" {
		return nil, errors.New("no update public key pinned")
	}
	var der []byte
	if block, _ := pem.Decode([]byte(pubPEM)); block != nil && block.Type == "PUBLIC KEY" {
		der = block.Bytes
	} else if b, err := base64.StdEncoding.DecodeString(pubPEM); err == nil {
		der = b
	} else {
		return nil, errors.New("update public key is neither PEM nor base64 DER")
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
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
// before anything is dispatched to agents: version marker, HTTPS URL,
// digest encoding, signature encoding, artifact digest, and the ed25519
// signature against the pinned key. Agents re-verify everything on their
// side; this is the controller-side pre-verification gate (fail closed).
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
