package panel

// Regression tests for the agent-update verification gate.
//
// History: the Phase-5B controller extraction replaced these functions
// with stubs that returned (ed25519.PublicKey{}, nil). With a pinned
// update key, update.go then called ed25519.Verify with a zero-length
// key, which PANICS ("ed25519: bad public key length: 0"). These tests
// pin the fail-closed behavior ported back from the monolith
// (agent_update.go @ a1e999c).

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
	"testing"
)

func pinTestKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
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
	old := agentUpdatePubKeyPEM
	agentUpdatePubKeyPEM = pemStr
	t.Cleanup(func() { agentUpdatePubKeyPEM = old })
	return pub, priv
}

func validUpdateRequest(t *testing.T, priv ed25519.PrivateKey, artifact []byte) agentUpdateRequest {
	t.Helper()
	sum := sha256.Sum256(artifact)
	sig := ed25519.Sign(priv, artifact)
	return agentUpdateRequest{
		Version:   "v1.29.1",
		URL:       "https://updates.example.invalid/quantum-agent-1.29.1.tar.gz",
		SHA256Hex: hex.EncodeToString(sum[:]),
		Signature: base64.StdEncoding.EncodeToString(sig),
	}
}

// TestAgentUpdateVerifyKeyRejectsEmpty is THE regression: the stub
// returned (empty key, nil error), which both defeated the caller's
// error gate and made a later ed25519.Verify panic.
func TestAgentUpdateVerifyKeyRejectsEmpty(t *testing.T) {
	key, err := agentUpdateVerifyKey("")
	if err == nil {
		t.Fatal("agentUpdateVerifyKey(\"\") returned nil error, want fail-closed error")
	}
	if key != nil {
		t.Fatalf("key = %v, want nil", key)
	}
	if !strings.Contains(err.Error(), "no update public key pinned") {
		t.Fatalf("error = %v", err)
	}
}

func TestAgentUpdateVerifyKeyRejectsGarbage(t *testing.T) {
	for _, bad := range []string{"not a key", "aGVsbG8gd29ybGQ=" /* base64 of text, not DER */} {
		key, err := agentUpdateVerifyKey(bad)
		if err == nil || key != nil {
			t.Fatalf("agentUpdateVerifyKey(%q) = (%v, %v), want error", bad, key, err)
		}
	}
}

func TestAgentUpdateVerifyKeyAcceptsPEMAndBase64(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	pemKey, err := agentUpdateVerifyKey(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})))
	if err != nil {
		t.Fatalf("PEM form rejected: %v", err)
	}
	b64Key, err := agentUpdateVerifyKey(base64.StdEncoding.EncodeToString(der))
	if err != nil {
		t.Fatalf("base64 DER form rejected: %v", err)
	}
	if string(pemKey) != string(pub) || string(b64Key) != string(pub) {
		t.Fatal("parsed key does not round-trip to the source key")
	}
}

// TestAgentUpdateVerifyArtifactFailClosedWithoutKey pins the no-panic,
// fail-closed path when no key is pinned. The digest must be correct so
// the flow reaches the key check instead of stopping at the sha256 gate.
func TestAgentUpdateVerifyArtifactFailClosedWithoutKey(t *testing.T) {
	old := agentUpdatePubKeyPEM
	agentUpdatePubKeyPEM = ""
	t.Cleanup(func() { agentUpdatePubKeyPEM = old })

	artifact := []byte("artifact")
	sum := sha256.Sum256(artifact)
	req := agentUpdateRequest{
		Version:   "v1.29.1",
		URL:       "https://updates.example.invalid/a.bin",
		SHA256Hex: hex.EncodeToString(sum[:]),
		Signature: base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize)),
	}
	err := agentUpdateVerifyArtifact(artifact, req)
	if err == nil {
		t.Fatal("verification succeeded without a pinned key, want fail-closed error")
	}
	if !strings.Contains(err.Error(), "signature verification unavailable") {
		t.Fatalf("error = %v", err)
	}
}

func TestAgentUpdateVerifyArtifactEndToEnd(t *testing.T) {
	_, priv := pinTestKey(t)
	artifact := []byte("quantum-agent binary bytes")

	t.Run("accepts valid signature", func(t *testing.T) {
		if err := agentUpdateVerifyArtifact(artifact, validUpdateRequest(t, priv, artifact)); err != nil {
			t.Fatalf("valid artifact rejected: %v", err)
		}
	})

	t.Run("rejects tampered artifact", func(t *testing.T) {
		tampered := append([]byte(nil), artifact...)
		tampered[0] ^= 0xff
		err := agentUpdateVerifyArtifact(tampered, validUpdateRequest(t, priv, artifact))
		if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
			t.Fatalf("err = %v, want sha256 mismatch", err)
		}
	})

	t.Run("rejects wrong signature", func(t *testing.T) {
		// Sign with a foreign key that is NOT pinned; the pinned key is
		// still the one from pinTestKey above.
		_, foreign, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		req := validUpdateRequest(t, foreign, artifact)
		err2 := agentUpdateVerifyArtifact(artifact, req)
		if err2 == nil || !strings.Contains(err2.Error(), "signature verification FAILED") {
			t.Fatalf("err = %v, want signature FAILED", err2)
		}
	})

	t.Run("rejects http URL", func(t *testing.T) {
		req := validUpdateRequest(t, priv, artifact)
		req.URL = "http://updates.example.invalid/a.bin"
		if err := agentUpdateVerifyArtifact(artifact, req); err == nil {
			t.Fatal("http URL accepted")
		}
	})

	t.Run("rejects bad version marker", func(t *testing.T) {
		req := validUpdateRequest(t, priv, artifact)
		req.Version = "not-a-version"
		if err := agentUpdateVerifyArtifact(artifact, req); err == nil {
			t.Fatal("bad version accepted")
		}
	})
}
