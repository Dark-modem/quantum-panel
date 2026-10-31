package main

import "testing"

// The non-Linux build keeps the agent compilable; the resource report then
// carries only platform-independent facts. This test pins the contract on
// whichever platform runs the suite.
func TestAgentCollectResourcesShape(t *testing.T) {
	old := agentHostExec
	agentHostExec = func(name string, args ...string) (string, error) {
		if name == "systemd-detect-virt" {
			return "kvm\n", nil
		}
		if name == "ss" {
			return "tcp   LISTEN 0 128 0.0.0.0:7777 0.0.0.0:* users:((\"gre-panel\",pid=5,fd=8))\n", nil
		}
		return "", nil
	}
	t.Cleanup(func() { agentHostExec = old })

	r := agentCollectResources()
	if r == nil {
		t.Fatal("nil resource report")
	}
	if r.Virt != "" && r.Virt != "kvm" {
		t.Fatalf("virt = %q", r.Virt)
	}
	// Listening inventory is parsed from ss wherever ss output is available;
	// on non-Linux stubs it is legitimately empty — never invented.
	for _, l := range r.Listening {
		if l.Port < 0 || l.Port > 65535 {
			t.Fatalf("bad port in inventory: %+v", l)
		}
	}
}
