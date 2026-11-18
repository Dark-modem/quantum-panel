package panel

// Hermetic tests for the typed Agent execution interface
// (firewall_agent.go): validation, the always-dry-run plan, the
// Confirmed gate on apply/teardown, and the result shapes. These are the
// exact functions Session 3 will register in the agent dispatcher.

import (
	"strings"
	"testing"
)

func TestFirewallAgentSplitOwner(t *testing.T) {
	kind, id, err := fwAgentSplitOwner("tunnel:at_1a2b3c4d")
	if err != nil || kind != "tunnel" || id != "at_1a2b3c4d" {
		t.Fatalf("valid owner parse failed: %q %q %v", kind, id, err)
	}
	// Installer-style kinds (installer-lib.sh fw_allow_port tags rules as
	// quantum:<role>:<name>) are valid firewall owners.
	if k, id2, err := fwAgentSplitOwner("controller:ssh-alt"); err != nil || k != "controller" || id2 != "ssh-alt" {
		t.Fatalf("installer-style owner must parse: %q %q %v", k, id2, err)
	}
	for _, bad := range []string{"at_x", "tunnel:", ":at_x", "weird:at:x", "tunnel:at_\"x\"", "Controller:at_x", "tunnel:at x", "UPPER:at_x"} {
		if _, _, err := fwAgentSplitOwner(bad); err == nil {
			t.Fatalf("owner %q must be rejected", bad)
		}
	}
}

func TestFirewallAgentPlanAlwaysDryRun(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")

	res, err := agentFirewallPlan(fwAgentPlanParams{
		Owner: "tunnel:at_x", Family: "ipv4", Note: "agent plan",
		Add: []fwPortSpec{{Port: 8443, Proto: "tcp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Refusal != "" {
		t.Fatalf("iptables backend must not refuse, got %q", res.Refusal)
	}
	if !res.Plan.DryRun {
		t.Fatal("agent plan must be dry-run")
	}
	if len(res.PlannedCmds) == 0 {
		t.Fatalf("planned commands must be returned: %+v", res)
	}
	for _, c := range fake.recorded() {
		if strings.Contains(c, "-A QUANTUM_FW") || strings.HasPrefix(c, "ufw allow") || strings.Contains(c, "-I INPUT") {
			t.Fatalf("agent plan executed a mutating command: %s", c)
		}
	}
}

func TestFirewallAgentPlanValidation(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	cases := []fwAgentPlanParams{
		{Owner: "at_x", Add: []fwPortSpec{{Port: 1, Proto: "tcp"}}},          // no kind
		{Owner: "tunnel:at_x", Add: []fwPortSpec{{Port: 0, Proto: "tcp"}}},   // bad port
		{Owner: "tunnel:at_x", Add: []fwPortSpec{{Port: 80, Proto: "sctp"}}}, // bad proto
		{Owner: "tunnel:at_x"}, // nothing to plan
	}
	for i, p := range cases {
		if _, err := agentFirewallPlan(p); err == nil {
			t.Fatalf("case %d must be rejected", i)
		}
	}
}

func TestFirewallAgentApplyRequiresConfirmed(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")

	ops := []fwOp{{Kind: "add", Rule: fwPortRule{
		Port: 8443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"}}}

	if _, err := agentFirewallApply(fwAgentApplyParams{Ops: ops}); err == nil {
		t.Fatal("apply without Confirmed must be refused")
	}
	for _, c := range fake.recorded() {
		if strings.Contains(c, "-A QUANTUM_FW") {
			t.Fatalf("unconfirmed apply mutated the host: %s", c)
		}
	}
	res, err := agentFirewallApply(fwAgentApplyParams{Ops: ops, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Plan.DryRun || len(res.Applied) == 0 {
		t.Fatalf("confirmed apply must execute, got %+v", res)
	}
	if !strings.Contains(strings.Join(fake.recorded(), "\n"), "-A QUANTUM_FW") {
		t.Fatalf("confirmed apply must run the rule: %v", fake.recorded())
	}
}

func TestFirewallAgentTeardownGuardsAndReleases(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	spec := fwIptablesSpec(fwPortRule{Port: 7002, Proto: "udp", Family: "ipv4", Owner: "tunnel:at_x"})
	fake.setResponse("iptables -S QUANTUM_FW", "-A QUANTUM_FW "+spec+"\n")
	portRegistryReset(t)
	if err := portReserve(7002, "udp", portOwnerTunnel, "at_x", "ipv4", ""); err != nil {
		t.Fatal(err)
	}

	if _, err := agentFirewallTeardown(fwAgentTeardownParams{Owner: "tunnel:at_x"}); err == nil {
		t.Fatal("teardown without Confirmed must be refused")
	}
	_, err := agentFirewallTeardown(fwAgentTeardownParams{Owner: "tunnel:at_x", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	// The remove path drops the ownership record itself (the rule was
	// present and is now gone), so nothing is left for the release step.
	if len(portEntriesForOwner("tunnel:at_x")) != 0 {
		t.Fatalf("owner entries must be gone, got %+v", portEntriesForOwner("tunnel:at_x"))
	}
	if !strings.Contains(strings.Join(fake.recorded(), "\n"), "iptables -D QUANTUM_FW "+spec) {
		t.Fatalf("teardown must remove the owner's rule: %v", fake.recorded())
	}
	if got := portEntries(); len(got) != 0 {
		t.Fatalf("registry must be empty, got %+v", got)
	}
}

func TestFirewallAgentPortCheckAndList(t *testing.T) {
	fake := portTestLab(t)
	fake.setResponse("ss -Htulnp", portTestSS)

	if _, err := agentPortCheck(fwAgentPortCheckParams{Port: 0}); err == nil {
		t.Fatal("bad port must be rejected")
	}
	if _, err := agentPortCheck(fwAgentPortCheckParams{Port: 443, Proto: "sctp"}); err == nil {
		t.Fatal("bad proto must be rejected")
	}
	rep, err := agentPortCheck(fwAgentPortCheckParams{Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := rep.(map[string]any); !ok || m["tcp"] == nil || m["udp"] == nil {
		t.Fatalf("proto-less check must report both transports: %#v", rep)
	}
	avail, err := agentPortCheck(fwAgentPortCheckParams{Port: 8443, Proto: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	a := avail.(portAvailability)
	if a.Available || a.Listener == nil || a.Listener.Process != "xray" {
		t.Fatalf("tcp 8443 must show the xray listener, got %+v", a)
	}

	list, err := agentPortList()
	if err != nil {
		t.Fatal(err)
	}
	if list.Listeners == nil || list.Entries == nil || list.Conflicts == nil || list.Backend == "" {
		t.Fatalf("port list overview incomplete: %+v", list)
	}
}
