package panel

// Legacy "hashem" → "quantum" rebrand migration.
//
// Servers updated in place still carry systemd units, SSH keys and other
// files named hashem-*. Every subsystem now manages the quantum-* names, so
// without migration those installs would look broken after the upgrade (the
// panel would poll quantum-bagher.service while hashem-bagher.service keeps
// running, the watchdog timer would never be adopted, and so on).
//
// This file holds the ONE shared, best-effort migration helper:
//
//   - legacyUnitMigration(unit): when the quantum-* unit file is missing but
//     the legacy hashem-* twin exists, stop+disable the legacy unit, adopt
//     (copy, brand-rewritten) its unit file under the new name and
//     enable+start the new unit.
//   - legacyFileMigration(path): when the quantum-* file is missing but the
//     legacy hashem-* twin exists, copy it across (preserving mode, plus the
//     .pub sidecar for SSH keys).
//   - legacyUnitRetire(unit): when the panel fully regenerates a unit set
//     (rescue, ssh-fwd) it only needs the legacy twin gone: stop+disable it.
//   - envQuantum(name, legacy): read a renamed QUANTUM_* environment variable
//     with the legacy HASHEM_* spelling as fallback.
//
// Everything here runs at most once per legacy name per process, never
// removes data, and on any failure logs a clear warning and continues: a
// failed migration must never break the panel or the setup flows.

import (
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// legacyBrandReplacer maps the new brand back onto the legacy one.
var legacyBrandReplacer = strings.NewReplacer(
	"quantum", "hashem",
	"Quantum", "Hashem",
	"QUANTUM", "HASHEM",
)

// legacyBrandRewrite rewrites a legacy file's content to the new brand.
var legacyBrandRewrite = strings.NewReplacer(
	"hashem", "quantum",
	"Hashem", "Quantum",
	"HASHEM", "QUANTUM",
)

// legacyFileTokenRe matches file paths embedded in unit files that still
// carry the legacy brand (ExecStart scripts, ssh -i keys, ...). Anything
// without a path separator is not treated as a path.
var legacyFileTokenRe = regexp.MustCompile(`[^"'\s]*?(?:hashem|Hashem|HASHEM)[^"'\s]*`)

var (
	legacyMigrateMu      sync.Mutex
	legacyMigrateAttempt = map[string]bool{}
	// legacyUnitDirOverride is the test seam for the systemd unit directory.
	legacyUnitDirOverride string
)

// legacyUnitDir is the directory systemd unit files live in. QUANTUM_SYSTEMD_DIR
// overrides it (HASHEM_SYSTEMD_DIR kept as legacy fallback); tests can set
// legacyUnitDirOverride.
func legacyUnitDir() string {
	if legacyUnitDirOverride != "" {
		return legacyUnitDirOverride
	}
	if v := os.Getenv("QUANTUM_SYSTEMD_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("HASHEM_SYSTEMD_DIR"); v != "" {
		return v
	}
	return "/etc/systemd/system"
}

// legacyUnitFilePath maps a unit name (with or without .service/.timer) to its
// unit file path.
func legacyUnitFilePath(unit string) string {
	return filepath.Join(legacyUnitDir(), unit)
}

// legacyOnce reports whether this legacy name was already attempted and
// records the attempt.
func legacyOnce(kind, legacy string) bool {
	legacyMigrateMu.Lock()
	defer legacyMigrateMu.Unlock()
	key := kind + ":" + legacy
	if legacyMigrateAttempt[key] {
		return true
	}
	legacyMigrateAttempt[key] = true
	return false
}

// legacyResetMigrationState clears the once-map (tests only).
func legacyResetMigrationState() {
	legacyMigrateMu.Lock()
	defer legacyMigrateMu.Unlock()
	legacyMigrateAttempt = map[string]bool{}
}

// legacyUnitMigration adopts a legacy hashem-* unit under its new quantum-*
// name. Best effort and once per legacy unit: if the new unit file already
// exists (fresh install or already migrated) or no legacy unit exists, it is
// a no-op.
func legacyUnitMigration(unit string) {
	legacy := legacyBrandReplacer.Replace(unit)
	if legacy == unit {
		return
	}
	if legacyOnce("unit", legacy) {
		return
	}
	newPath := legacyUnitFilePath(unit)
	if fileExists(newPath) {
		return
	}
	data, err := os.ReadFile(legacyUnitFilePath(legacy))
	if err != nil {
		return // no legacy unit installed: nothing to migrate
	}
	log.Printf("[QUANTUM-MIGRATE] legacy unit %s detected, adopting as %s", legacy, unit)
	// Stop and disable the legacy unit so the two never run side by side.
	_, _ = runCmd("systemctl", "disable", "--now", legacy)
	original := string(data)
	content := legacyBrandRewrite.Replace(original)
	if err := os.WriteFile(newPath, []byte(content), 0o644); err != nil {
		log.Printf("[QUANTUM-MIGRATE] warning: could not write %s (%v); legacy unit %s is left untouched", newPath, err, legacy)
		return
	}
	// Adopt legacy-branded files the unit references (SSH keys, scripts). The
	// tokens come from the ORIGINAL content (legacy paths); each is handed to
	// legacyFileMigration under its rewritten quantum-* name.
	for _, m := range legacyFileTokenRe.FindAllString(original, -1) {
		if !strings.ContainsAny(m, `/\`) {
			continue // not a path (a bare name like a unit alias)
		}
		legacyFileMigration(legacyBrandRewrite.Replace(m))
	}
	if _, err := runCmd("systemctl", "daemon-reload"); err != nil {
		log.Printf("[QUANTUM-MIGRATE] warning: systemctl daemon-reload failed: %v", err)
	}
	if _, err := runCmd("systemctl", "enable", "--now", unit); err != nil {
		log.Printf("[QUANTUM-MIGRATE] warning: could not enable %s (%v); run 'systemctl enable --now %s' manually", unit, err, unit)
	}
}

// legacyUnitRetire stops and disables the legacy twin of a unit the panel
// regenerates itself (so the legacy unit is not left running). Once per
// legacy unit, best effort.
func legacyUnitRetire(unit string) {
	legacy := legacyBrandReplacer.Replace(unit)
	if legacy == unit {
		return
	}
	if legacyOnce("retire", legacy) {
		return
	}
	if !fileExists(legacyUnitFilePath(legacy)) {
		return
	}
	log.Printf("[QUANTUM-MIGRATE] retiring legacy unit %s (replaced by %s)", legacy, unit)
	_, _ = runCmd("systemctl", "disable", "--now", legacy)
}

// legacyFileMigration copies a legacy hashem-* file to its quantum-* name,
// preserving the mode and the .pub sidecar for SSH keys. Once per legacy
// path, best effort.
func legacyFileMigration(path string) {
	legacy := legacyBrandReplacer.Replace(path)
	if legacy == path {
		return
	}
	if legacyOnce("file", legacy) {
		return
	}
	if fileExists(path) {
		return
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		return // no legacy file: nothing to migrate
	}
	perm := os.FileMode(0o600)
	if info, err := os.Stat(legacy); err == nil {
		perm = info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf("[QUANTUM-MIGRATE] warning: could not create %s: %v", filepath.Dir(path), err)
		return
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		log.Printf("[QUANTUM-MIGRATE] warning: could not copy %s to %s: %v", legacy, path, err)
		return
	}
	log.Printf("[QUANTUM-MIGRATE] migrated %s -> %s", legacy, path)
	// SSH key sidecars travel with the key.
	for _, ext := range []string{".pub"} {
		side, err := os.ReadFile(legacy + ext)
		if err != nil {
			continue
		}
		_ = os.WriteFile(path+ext, side, 0o644)
	}
}

// envQuantum reads the renamed QUANTUM_* variable, falling back to its legacy
// HASHEM_* spelling so environments configured before the rebrand keep
// working mid-session.
func envQuantum(name, legacy string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return os.Getenv(legacy)
}

// legacyMigrateSSHKey is the wrapper used where the panel resolves the
// quantum-fwd SSH key path: adopts the legacy hashem-fwd key pair once.
func legacyMigrateSSHKey(keyPath string) {
	if strings.Contains(keyPath, "quantum") {
		legacyFileMigration(keyPath)
	}
}
