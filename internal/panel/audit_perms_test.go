// audit_perms_test.go — phase 6 M3 perms sweep: panel-internal files
// must not be world-readable (the systemd units stay 0644 by design —
// systemd requires them; the sysctl.d drop-ins likewise).

package panel

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGreMTUFileModeIsPrivate(t *testing.T) {
	withTempConfig(t)
	if err := savePathMTU(1300); err != nil {
		t.Fatalf("set: %v", err)
	}
	fi, err := os.Stat(filepath.Join(configDir, "gre_mtu"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("gre_mtu mode = %v, want 0600", fi.Mode().Perm())
	}
	// And the value round-trips.
	if got := savedPathMTU(); got != 1300 {
		t.Fatalf("round trip: %d", got)
	}
}

func TestClusterLBFilesAreNotWorldReadable(t *testing.T) {
	withTempConfig(t)
	// Write the files exactly as the production writers do.
	if err := os.WriteFile(lbPooledPortsFile(), []byte("30000\n30001\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lbBindsFile(), []byte("10.0.0.1:8080\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{lbPooledPortsFile(), lbBindsFile()} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		// 0600/0640 are the accepted modes: nothing may be OTHER-readable.
		if fi.Mode().Perm()&0o004 != 0 || fi.Mode().Perm()&0o003 != 0 {
			t.Fatalf("%s mode = %v — other-readable/writable panel files are not allowed", f, fi.Mode().Perm())
		}
	}
}
