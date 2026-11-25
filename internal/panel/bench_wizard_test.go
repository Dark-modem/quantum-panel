package panel

import (
	"os/exec"
	"testing"
)

// TestBenchWizardUI runs the VM tests for the redesigned Pair Benchmark
// wizard: the four-step stepper with eligibility gating, the
// different-datacenter rule on real datacenter ids, the core cards with
// their real method counts, and the dual-runner start/poll/stop flow.
func TestBenchWizardUI(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	out, err := exec.Command(node, "bench_wizard.test.cjs").CombinedOutput()
	if err != nil {
		t.Fatalf("bench wizard UI regression failed: %v\n%s", err, out)
	}
}
