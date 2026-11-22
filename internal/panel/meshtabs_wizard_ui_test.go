package panel

import (
	"os/exec"
	"testing"
)

// TestMeshtabsWizardUI runs the VM tests for the ui-ux-2 overnight work:
// the mesh tunnel-creation wizard's method catalogs (the §2.22 engine
// sub-lines, the method & parameters step over the verbatim methods[] with
// its layer/transport/ipver labels, the engines-catalog params resolver
// chain, the engine_params payload with the method pin, the honest
// catalog-pending / feed-error states, null = "—", escaped echo).
func TestMeshtabsWizardUI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	out, err := exec.Command(node, "meshtabs_wizard_ui.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("meshtabs wizard UI regression failed: %v\n%s", err, out)
	}
}
