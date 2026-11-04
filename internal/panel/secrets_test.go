package panel

// Secrets tests: per-install generation, persistence, and the guard that
// the shared hardcoded credentials never come back.

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFrpSecretsGeneratedOncePerInstall(t *testing.T) {
	dir := t.TempDir()
	old := configDir
	configDir = dir
	t.Cleanup(func() { configDir = old })

	first := loadFrpSecrets()
	if first.AuthToken == "" || first.DashboardPassword == "" {
		t.Fatal("empty credentials generated")
	}
	if first.AuthToken == "quantum-bagher-sec" || first.DashboardPassword == "bagher-dash-local" {
		t.Fatal("generated credentials equal the retired hardcoded defaults")
	}
	second := loadFrpSecrets()
	if second != first {
		t.Fatal("credentials regenerated within one install")
	}
	// Persisted 0600 next to the config.
	data, err := os.ReadFile(filepath.Join(dir, frpSecretsFile))
	if err != nil {
		t.Fatalf("secrets file missing: %v", err)
	}
	if !strings.Contains(string(data), first.AuthToken) {
		t.Fatal("persisted file does not carry the generated token")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(filepath.Join(dir, frpSecretsFile))
		if err != nil || fi.Mode().Perm() != 0600 {
			t.Fatalf("secrets file perms = %v, want 0600", fi.Mode().Perm())
		}
	}
}

func TestFrpSecretsDistinctAcrossInstalls(t *testing.T) {
	old := configDir
	defer func() { configDir = old }()
	frpSecretsMu.Lock()
	frpSecretsCache = nil
	frpSecretsMu.Unlock()
	defer func() {
		frpSecretsMu.Lock()
		frpSecretsCache = nil
		frpSecretsMu.Unlock()
	}()

	resetCache := func() {
		frpSecretsMu.Lock()
		frpSecretsCache = nil
		frpSecretsMu.Unlock()
	}
	resetCache()
	configDir = t.TempDir()
	a := loadFrpSecrets()
	resetCache()
	configDir = t.TempDir()
	b := loadFrpSecrets()
	if a.AuthToken == b.AuthToken || a.DashboardPassword == b.DashboardPassword {
		t.Fatal("two installs generated identical credentials")
	}
}

// TestNoHardcodedFrpCredentials is the regression guard for the Phase-1A
// decision: the shared frp token and dashboard password must never be
// baked into source again.
func TestNoHardcodedFrpCredentials(t *testing.T) {
	for _, bad := range []string{"quantum-bagher-sec", "bagher-dash-local"} {
		err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), bad) {
				t.Errorf("%s contains the retired hardcoded credential %q", path, bad)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
