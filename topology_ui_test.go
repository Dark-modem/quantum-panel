package main

import (
	"os/exec"
	"testing"
)

func TestTopologyUIBehavior(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	for _, args := range [][]string{{"topology_ui.test.cjs"}, {"--test", "topology_delete_ui.test.cjs"}, {"--test", "topology_wizard_ui.test.cjs"}, {"--test", "topology_create_ui.test.cjs"}} {
		out, err := exec.Command(node, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("topology UI regression failed: %v\n%s", err, out)
		}
	}
}
