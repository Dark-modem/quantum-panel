package panel

import (
	"os/exec"
	"testing"
)

// TestCoresCheckerUI runs the VM tests for the ui/ux-1 overnight work: the
// per-core drill-down generalization (every core opens the dedicated page;
// per-core hero text; the NOTICE license/upstream strip with its protocol
// guard; the honest catalog-pending state) and the methods-checker view
// (GET /api/overnight/checker documented shape, honest not-live state,
// null = "—", escaped echoed fields).
func TestCoresCheckerUI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	out, err := exec.Command(node, "cores_checker_ui.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("cores checker UI regression failed: %v\n%s", err, out)
	}
}
