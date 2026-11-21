package panel

import (
	"os/exec"
	"testing"
)

// TestCoresArtUI runs the VM tests for the artifact store UI (contract
// §2.22): repo-vs-upload request assembly, the repo-newer indicator,
// null → — rendering and the honest feed states.
func TestCoresArtUI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	out, err := exec.Command(node, "cores_art_ui.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("cores artifact store UI regression failed: %v\n%s", err, out)
	}
}
