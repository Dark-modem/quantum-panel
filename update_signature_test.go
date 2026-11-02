package main

// Session 4 gate (SU-1): the controller self-update signature verification
// must FAIL CLOSED on every rejection path — no pinned key, missing .sig,
// malformed signature, wrong-key signature — with the only bypass being the
// explicit, audited QUANTUM_UPDATE_ALLOW_UNSIGNED=1 emergency override.

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func ed25519PEMForTest(t *testing.T) (pubPEM string, priv ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	pubPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	return pubPEM, priv
}

// updateSigServer serves the artifact at "/" and the .sig at "/.sig" with a
// configurable status/body.
func updateSigServer(t *testing.T, sigStatus int, sigBody string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Write([]byte("ARTIFACT-BYTES"))
			return
		}
		w.WriteHeader(sigStatus)
		w.Write([]byte(sigBody))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func withUpdateKey(t *testing.T, pem string) {
	t.Helper()
	old := agentUpdatePubKeyPEM
	agentUpdatePubKeyPEM = pem
	t.Cleanup(func() { agentUpdatePubKeyPEM = old })
}

func withUpdateOverride(t *testing.T, on bool) {
	t.Helper()
	old, had := os.LookupEnv("QUANTUM_UPDATE_ALLOW_UNSIGNED")
	if on {
		os.Setenv("QUANTUM_UPDATE_ALLOW_UNSIGNED", "1")
	} else {
		os.Unsetenv("QUANTUM_UPDATE_ALLOW_UNSIGNED")
	}
	t.Cleanup(func() {
		if had {
			os.Setenv("QUANTUM_UPDATE_ALLOW_UNSIGNED", old)
		} else {
			os.Unsetenv("QUANTUM_UPDATE_ALLOW_UNSIGNED")
		}
	})
}

func TestUpdateSignatureFailClosed(t *testing.T) {
	artifact := []byte("ARTIFACT-BYTES")
	dir := t.TempDir()
	artifactPath := filepath.Join(dir, "asset.bin")
	if err := os.WriteFile(artifactPath, artifact, 0600); err != nil {
		t.Fatalf("artifact: %v", err)
	}

	pubPEM, priv := ed25519PEMForTest(t)
	_, wrongPriv := ed25519PEMForTest(t)
	goodSig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, artifact))
	wrongKeySig := base64.StdEncoding.EncodeToString(ed25519.Sign(wrongPriv, artifact))

	cases := []struct {
		name      string
		pin       string
		override  bool
		sigStatus int
		sigBody   string
		wantErr   bool
	}{
		{"no pin, no override", "", false, 200, goodSig, true},
		{"no pin, emergency override", "", true, 200, goodSig, false},
		{"pin, signature missing (404)", pubPEM, false, 404, "", true},
		{"pin, signature missing, override", pubPEM, true, 404, "", false},
		{"pin, malformed signature", pubPEM, false, 200, "not-base64!!", true},
		{"pin, wrong-key signature", pubPEM, false, 200, wrongKeySig, true},
		{"pin, valid signature", pubPEM, false, 200, goodSig, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withUpdateKey(t, tc.pin)
			withUpdateOverride(t, tc.override)
			srv := updateSigServer(t, tc.sigStatus, tc.sigBody)
			err := verifyAssetSignature(artifactPath, srv.URL+"/", "asset.bin")
			if tc.wantErr && err == nil {
				t.Fatalf("SECURITY FAILURE: update passed without valid signature verification (case %s)", tc.name)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected success, got: %v", err)
			}
		})
	}
}

