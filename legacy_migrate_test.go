package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// legacyMigrateTestEnv points the migration helper at a temp unit dir and
// stubs runCmd, recording every systemctl call.
func legacyMigrateTestEnv(t *testing.T) *[]string {
	t.Helper()
	dir := t.TempDir()
	oldDir := legacyUnitDirOverride
	legacyUnitDirOverride = dir
	oldRun := runCmd
	var calls []string
	runCmd = func(name string, args ...string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", nil
	}
	legacyResetMigrationState()
	t.Cleanup(func() {
		legacyUnitDirOverride = oldDir
		runCmd = oldRun
		legacyResetMigrationState()
	})
	return &calls
}

func TestLegacyUnitMigrationAdoptsHashemUnit(t *testing.T) {
	calls := legacyMigrateTestEnv(t)

	// Not t.TempDir(): the dir name would contain the test's own "Hashem"
	// substring and break the brand-mapping assertions.
	base, err := os.MkdirTemp("", "legmig-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	legacyKey := filepath.Join(base, "hashem-fwd")
	if err := os.WriteFile(legacyKey, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(legacyKey+".pub", []byte("ssh-ed25519 AAA"), 0o644)

	unitDir := legacyUnitDir()
	legacyUnit := filepath.Join(unitDir, "hashem-bagher.service")
	content := "[Unit]\nAfter=hashem-bagher-frps.service\n\n[Service]\nExecStart=/usr/local/bin/gre-panel -i " + legacyKey + "\n"
	if err := os.WriteFile(legacyUnit, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	legacyUnitMigration("quantum-bagher.service")

	// New unit file adopted with rewritten brand.
	newUnit := filepath.Join(unitDir, "quantum-bagher.service")
	data, err := os.ReadFile(newUnit)
	if err != nil {
		t.Fatalf("new unit file not written: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "hashem") || strings.Contains(got, "Hashem") {
		t.Fatalf("adopted unit still references the legacy brand:\n%s", got)
	}
	if !strings.Contains(got, "quantum-bagher-frps.service") {
		t.Fatalf("adopted unit lost the After= dependency:\n%s", got)
	}
	// Referenced legacy SSH key adopted under the new name, mode preserved.
	if _, err := os.Stat(filepath.Join(filepath.Dir(legacyKey), "quantum-fwd")); err != nil {
		t.Fatalf("legacy key not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(legacyKey), "quantum-fwd.pub")); err != nil {
		t.Fatalf("legacy key sidecar not migrated: %v", err)
	}
	// systemctl: legacy disabled, new enabled.
	joined := strings.Join(*calls, "\n")
	for _, want := range []string{
		"systemctl disable --now hashem-bagher.service",
		"systemctl daemon-reload",
		"systemctl enable --now quantum-bagher.service",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing systemctl call %q; got:\n%s", want, joined)
		}
	}
}

func TestLegacyUnitMigrationRunsOnce(t *testing.T) {
	calls := legacyMigrateTestEnv(t)

	unitDir := legacyUnitDir()
	if err := os.WriteFile(filepath.Join(unitDir, "hashem-lb-net.service"), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	legacyUnitMigration("quantum-lb-net.service")
	if _, err := os.Stat(filepath.Join(unitDir, "quantum-lb-net.service")); err != nil {
		t.Fatalf("new unit not written: %v", err)
	}
	before := len(*calls)

	// Second attempt is memoized even though the legacy unit still exists.
	legacyUnitMigration("quantum-lb-net.service")
	if len(*calls) != before {
		t.Fatalf("migration ran twice: %v", *calls)
	}
}

func TestLegacyUnitMigrationNoopWithoutLegacyUnit(t *testing.T) {
	calls := legacyMigrateTestEnv(t)
	legacyUnitMigration("quantum-bagher.service") // no hashem-bagher.service anywhere
	if len(*calls) != 0 {
		t.Fatalf("expected no systemctl calls, got %v", *calls)
	}
}

func TestLegacyUnitRetireStopsLegacyUnit(t *testing.T) {
	calls := legacyMigrateTestEnv(t)

	unitDir := legacyUnitDir()
	if err := os.WriteFile(filepath.Join(unitDir, "hashem-rescue-frpc.service"), []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	legacyUnitRetire("quantum-rescue-frpc.service")
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "systemctl disable --now hashem-rescue-frpc.service") {
		t.Fatalf("legacy unit not retired; got:\n%s", joined)
	}
	// Once per legacy unit.
	legacyUnitRetire("quantum-rescue-frpc.service")
	if strings.Count(joined, "hashem-rescue-frpc") != strings.Count(strings.Join(*calls, "\n"), "hashem-rescue-frpc") {
		t.Fatalf("retire ran twice: %v", *calls)
	}
}

func TestLegacyFileMigrationCopiesFileAndSidecar(t *testing.T) {
	_ = legacyMigrateTestEnv(t)

	dir := t.TempDir()
	legacy := filepath.Join(dir, "hashem-revpath.conf")
	if err := os.WriteFile(legacy, []byte("net.ipv4.conf.all.rp_filter=2"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(legacy+".pub", []byte("side"), 0o644)

	legacyFileMigration(filepath.Join(dir, "quantum-revpath.conf"))

	data, err := os.ReadFile(filepath.Join(dir, "quantum-revpath.conf"))
	if err != nil {
		t.Fatalf("file not migrated: %v", err)
	}
	if string(data) != "net.ipv4.conf.all.rp_filter=2" {
		t.Fatalf("wrong content: %q", data)
	}
	info, err := os.Stat(filepath.Join(dir, "quantum-revpath.conf"))
	if err != nil {
		t.Fatal(err)
	}
	// On POSIX the legacy mode (0600) is preserved; on Windows the perm bits
	// are synthetic, so only assert what is portable.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("mode not preserved: %v", info.Mode())
	}
	if _, err := os.ReadFile(filepath.Join(dir, "quantum-revpath.conf.pub")); err != nil {
		t.Fatalf("sidecar not migrated: %v", err)
	}
}

func TestEnvQuantumFallsBackToLegacyName(t *testing.T) {
	t.Setenv("QUANTUM_MIG_TEST", "new")
	t.Setenv("HASHEM_MIG_TEST", "old")
	if got := envQuantum("QUANTUM_MIG_TEST", "HASHEM_MIG_TEST"); got != "new" {
		t.Fatalf("QUANTUM_ name should win, got %q", got)
	}
	os.Unsetenv("QUANTUM_MIG_TEST")
	t.Setenv("QUANTUM_MIG_TEST", "")
	if got := envQuantum("QUANTUM_MIG_TEST", "HASHEM_MIG_TEST"); got != "old" {
		t.Fatalf("legacy HASHEM_ name should be used as fallback, got %q", got)
	}
}
