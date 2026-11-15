package panel

// hermetic_test.go — BACKEND-24: shared helpers so the suite runs green on
// BOTH gates (WSL Linux canonical + Windows go). Two platform realities
// need explicit, documented handling:
//
//  1. The POSIX exec bit: Windows cannot set it via chmod (os.Stat always
//     reports 0666), so engine-binary stubs are plain regular files and the
//     executableFileFn probe is relaxed to "regular file exists" for the
//     test's duration — exactly what the stub means on unix. Unix keeps the
//     REAL probe (no weakening on the canonical gate).
//  2. POSIX permission bits: os.WriteFile(0600) on Windows yields a file
//     os.Stat reports as 0666 — an owner-only assertion is impossible to
//     satisfy there. The assertPerm helpers verify existence + content
//     path on Windows and the full POSIX mode on Linux (the WSL gate).

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// relaxExecProbeOnWindows swaps the exec-bit probe for "regular file
// exists" on Windows only (see the file comment); unix keeps the real
// probe. Restored via t.Cleanup.
func relaxExecProbeOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		return
	}
	old := executableFileFn
	executableFileFn = func(p string) bool {
		fi, err := os.Stat(p)
		return err == nil && fi.Mode().IsRegular()
	}
	t.Cleanup(func() { executableFileFn = old })
}

// stubEngineBins points the engine install-dir seams (engineBinDirs /
// backhaulExtraBins) at a temp dir holding fake binaries, and relaxes the
// exec probe on Windows (see the file comment). Unix keeps the real probe.
// Returns the binary dir.
func stubEngineBins(t *testing.T, names ...string) string {
	t.Helper()
	binDir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(binDir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldDirs, oldExtra := engineBinDirs, backhaulExtraBins
	engineBinDirs, backhaulExtraBins = []string{binDir}, nil
	t.Cleanup(func() { engineBinDirs, backhaulExtraBins = oldDirs, oldExtra })
	relaxExecProbeOnWindows(t)
	return binDir
}

// assertPerm0600 asserts the owner-only file property: full POSIX check on
// Linux; on Windows only existence (the POSIX bits are unrepresentable via
// Go's os.Stat on NTFS — the property is verified by the WSL gate).
func assertPerm0600(t *testing.T, path string) {
	t.Helper()
	assertPermIs(t, path, 0o600)
}

// assertPermIs is assertPerm0600 for an arbitrary wanted mode.
func assertPermIs(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS == "windows" {
		return // POSIX permission bits do not exist on NTFS via Go's os.Stat
	}
	if fi.Mode().Perm() != want {
		t.Fatalf("%s mode = %v, want %v", path, fi.Mode(), want)
	}
}
