package panel

import (
	"os/exec"
	"testing"
)

// TestUpdateUI runs the VM tests for the redesigned Panel update page: the
// honest update-state classification (a v1.32-dev build is a channel switch
// over the older stable v1.29.0, never a normal update), the version card
// render per state, the review-first dialog, the GitHub token states and the
// release-notes card.
func TestUpdateUI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	out, err := exec.Command(node, "update_ui.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("update UI regression failed: %v\n%s", err, out)
	}
}
