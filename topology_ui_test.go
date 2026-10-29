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
	out, err := exec.Command(node, "topology_ui.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("topology UI regression failed: %v\n%s", err, out)
	}
}
