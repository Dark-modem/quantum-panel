// secretbox.go — credentials encrypted at rest. The per-install key is a
// random 32-byte file (mode 0600) at <configDir>/pasarguard.key; values
// are stored as "v1:<nonce_b64>:<ciphertext_b64>" (AES-256-GCM). Losing
// the key file means re-entering the PasarGuard credentials — there is no
// recovery, by design (the file IS the trust anchor).

package pasarguard

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	secretKeySize = 32
	secretVersion = "v1"
)

// ErrNoKey reports "encryption configured but the key file is missing" —
// callers surface it as "re-enter the PasarGuard credentials".
var ErrNoKey = errors.New("pasarguard: secret key file missing (re-enter credentials)")

// SecretKeyPath resolves the per-install key file under dir.
func SecretKeyPath(dir string) string { return filepath.Join(dir, "pasarguard.key") }

// LoadOrCreateKey reads (or creates, mode 0600) the per-install key.
func LoadOrCreateKey(dir string) ([]byte, error) {
	path := SecretKeyPath(dir)
	if b, err := os.ReadFile(path); err == nil && len(b) == secretKeySize {
		return b, nil
	} else if err == nil && len(b) != secretKeySize {
		return nil, fmt.Errorf("pasarguard: %s has wrong size (%d bytes, want %d)", path, len(b), secretKeySize)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, secretKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// Seal encrypts plaintext with the install key. The nonce is random per
// call; the output embeds the version tag.
func Seal(key []byte, plaintext []byte) (string, error) {
	aead, err := aeadOf(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	return secretVersion + ":" +
		base64.RawStdEncoding.EncodeToString(nonce) + ":" +
		base64.RawStdEncoding.EncodeToString(ct), nil
}

// Open decrypts a sealed value. Wrong key or tampering = an error, never
// a guessed plaintext.
func Open(key []byte, sealed string) ([]byte, error) {
	parts := strings.Split(sealed, ":")
	if len(parts) != 3 || parts[0] != secretVersion {
		return nil, fmt.Errorf("pasarguard: not a %s sealed value", secretVersion)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("pasarguard: bad nonce: %v", err)
	}
	ct, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("pasarguard: bad ciphertext: %v", err)
	}
	aead, err := aeadOf(key)
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, errors.New("pasarguard: decrypt failed (wrong key or corrupted value)")
	}
	return pt, nil
}

func aeadOf(key []byte) (cipher.AEAD, error) {
	if len(key) != secretKeySize {
		return nil, fmt.Errorf("pasarguard: key must be %d bytes", secretKeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// constantTimeEqual is a tiny helper for comparing short secret strings
// without a length oracle beyond what Go already exposes.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
