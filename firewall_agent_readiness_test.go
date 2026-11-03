package main

// Integration-readiness test (Session 1 → Session 3): drives ALL SEVEN
// firewall/port handlers in the exact sequence the controller dispatcher
// will use, asserts the JSON wire shapes round-trip through
// encoding/json (the agent protocol serializes everything), and pins the
// safety semantics one final time: plan never mutates, apply is
// confirmation-gated, teardown removes only the named owner's rules.
// Errors are mapped the way the dispatcher adapter in
// docs/FIREWALL-AGENT-INTEGRATION.md prescribes (OK=!err, Error=err).

import (
	"encoding/json"
	"strings"
	"testing"
)

// fwRoundTrip marshals and unmarshals through JSON — what the wire does.
func fwRoundTrip(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	return data
}

func TestFirewallAgentIntegrationReadiness(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")
	fake.setResponse("ss -Htulnp", portTestSS)

	// 1. fw.detect — no mutation, structured result.
	detect := AgentFirewallDetect()
	if detect.Backend != fwBackendIptables || detect.Detail == "" {
		t.Fatalf("detect: %+v", detect)
	}
	fwRoundTrip(t, detect)

	// 2. fw.snapshot — read-only ruleset capture.
	snap, err := AgentFirewallSnapshot()
	if err != nil || snap.Backend != fwBackendIptables || snap.At == 0 {
		t.Fatalf("snapshot: %+v err %v", snap, err)
	}
	fwRoundTrip(t, snap)

	// 3. fw.plan — dry-run only, planned commands returned, host untouched.
	plan, err := AgentFirewallPlan(fwAgentPlanParams{
		Owner: "tunnel:at_ready", Family: "both", Note: "readiness",
		Add: []fwPortSpec{{Port: 8443, Proto: "tcp"}, {Port: 8443, Proto: "udp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Plan.DryRun || len(plan.PlannedCmds) != 4 { // 2 ports × 2 families
		t.Fatalf("plan: %+v", plan)
	}
	fwRoundTrip(t, plan)
	for _, c := range fake.recorded() {
		if strings.Contains(c, "-A QUANTUM_FW") || strings.Contains(c, "-I INPUT") {
			t.Fatalf("plan mutated the host: %s", c)
		}
	}

	// 4. fw.apply — refused without confirmation, executes with it.
	ops := plan.Plan.Ops
	if _, err := AgentFirewallApply(fwAgentApplyParams{Ops: ops}); err == nil {
		t.Fatal("unconfirmed apply must be refused")
	}
	applyRes, err := AgentFirewallApply(fwAgentApplyParams{Ops: ops, Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	if applyRes.Plan.DryRun || len(applyRes.Applied) == 0 {
		t.Fatalf("confirmed apply must execute: %+v", applyRes)
	}
	fwRoundTrip(t, applyRes)

	// 5. port.check + port.list — read-only reporting.
	check, err := AgentPortCheck(fwAgentPortCheckParams{Port: 8443, Proto: "tcp"})
	if err != nil {
		t.Fatal(err)
	}
	fwRoundTrip(t, check)
	list, err := AgentPortList()
	if err != nil || list.Entries == nil || list.Listeners == nil {
		t.Fatalf("port list: %+v err %v", list, err)
	}
	fwRoundTrip(t, list)

	// 6. fw.teardown — removes only THIS owner's rules and releases only
	// its registry entries; other owners are untouched. (The port is
	// different from the rule port above: the fake ss report holds live
	// listeners that would block a fresh reservation.)
	if err := portReserve(18443, "tcp", portOwnerTunnel, "at_ready", "both", "readiness"); err != nil {
		t.Fatal(err)
	}
	if err := portReserve(9100, "tcp", portOwnerService, "panel", "ipv4", "must survive"); err != nil {
		t.Fatal(err)
	}
	if _, err := AgentFirewallTeardown(fwAgentTeardownParams{Owner: "tunnel:at_ready"}); err == nil {
		t.Fatal("unconfirmed teardown must be refused")
	}
	teardown, err := AgentFirewallTeardown(fwAgentTeardownParams{Owner: "tunnel:at_ready", Confirmed: true})
	if err != nil {
		t.Fatal(err)
	}
	// The apply in step 4 auto-recorded 8443/tcp+udp, the reserve below
	// added 18443 — after teardown the owner must own NOTHING (the remove
	// path drops ownership records whether the rule was present or not).
	if len(portEntriesForOwner("tunnel:at_ready")) != 0 {
		t.Fatalf("owner entries must be gone after teardown, got %+v", portEntriesForOwner("tunnel:at_ready"))
	}
	fwRoundTrip(t, teardown)
	joined := strings.Join(fake.recorded(), "\n")
	if !strings.Contains(joined, "--comment quantum:tunnel:at_ready") {
		t.Fatalf("teardown must target the owner's rules:\n%s", joined)
	}
	for _, e := range portEntries() {
		if e.owner() == "tunnel:at_ready" {
			t.Fatalf("owner entries must be released: %+v", e)
		}
		if e.owner() == "service:panel" && e.Port == 9100 {
			continue // the unrelated entry must survive
		}
	}
	survivor := portEntriesForOwner("service:panel")
	if len(survivor) != 1 || survivor[0].Port != 9100 {
		t.Fatalf("unrelated owner must survive teardown: %+v", survivor)
	}
}

// TestFirewallAgentErrorMapping pins the dispatcher error contract: every
// handler error is a plain error string that the adapter maps to
// agentCommandResult{OK:false, Error:err.Error()}, and refusals are
// recognizable by their text.
func TestFirewallAgentErrorMapping(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")

	_, err1 := AgentFirewallApply(fwAgentApplyParams{Ops: []fwOp{{Kind: "add", Rule: fwPortRule{
		Port: 8443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"}}}})
	if err1 == nil || !strings.Contains(err1.Error(), "confirmed=true") {
		t.Fatalf("apply refusal must be recognizable: %v", err1)
	}
	_, err2 := AgentFirewallTeardown(fwAgentTeardownParams{Owner: "tunnel:at_x"})
	if err2 == nil || !strings.Contains(err2.Error(), "confirmed=true") {
		t.Fatalf("teardown refusal must be recognizable: %v", err2)
	}
	_, err3 := AgentFirewallPlan(fwAgentPlanParams{Owner: "tunnel:at_x"})
	if err3 == nil || !strings.Contains(err3.Error(), "nothing to plan") {
		t.Fatalf("empty plan must be rejected with a clear error: %v", err3)
	}
	_, err4 := AgentPortCheck(fwAgentPortCheckParams{Port: 70000})
	if err4 == nil {
		t.Fatal("bad port must be rejected")
	}
	// Native nftables refusal is machine-recognizable: fwErrNftForeign.
	fake.setResponse("ufw status", "")
	fake.setResponse("iptables -S", "!!!")
	fake.setResponse("nft list tables", "table inet admin {}\n")
	_, err5 := AgentFirewallPlan(fwAgentPlanParams{
		Owner: "tunnel:at_x", Add: []fwPortSpec{{Port: 443, Proto: "tcp"}}})
	if err5 == nil || err5.Error() != fwErrNftForeign.Error() {
		t.Fatalf("nft refusal must surface fwErrNftForeign, got %v", err5)
	}
}

// TestFirewallCLIHelpExitZero protects the installer probe:
// installer-lib.sh fw_engine_available runs `gre-panel firewall --help`
// and expects success.
func TestFirewallCLIHelpExitZero(t *testing.T) {
	fwTestLab(t)
	exit, payload := fwCLIFirewall([]string{"--help"})
	if exit != 0 {
		t.Fatalf("firewall --help must exit 0 (installer probe), got %d payload %#v", exit, payload)
	}
	if exit, _ := fwCLIPorts([]string{"--help"}); exit != 0 {
		t.Fatalf("ports --help must exit 0 too, got %d", exit)
	}
}
