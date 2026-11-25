package panel

import (
	"os/exec"
	"testing"
)

// TestCtunnelUI runs the VM tests for the C-Tunnel UI trio (contract §2.22
// additive fields + §2.23): the core details modal (49-method table, version
// strip, outdated badge), the live method board (pair gating, run=1 request,
// live rows, best/deploy card) and the kernel-tuning card normalizer.
func TestCtunnelUI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	out, err := exec.Command(node, "ctunnel_ui.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("ctunnel UI regression failed: %v\n%s", err, out)
	}
}
