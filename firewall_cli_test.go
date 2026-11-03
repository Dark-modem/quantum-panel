package main

// Hermetic tests for the firewall/ports CLI (firewall_cli.go). The host
// is the same scripted fake used by firewall_test.go; the internal
// command functions are called directly and their (exit, payload)
// contract is asserted. No binary is executed.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFirewallCLIDetect(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("ufw status", "Status: active")
	exit, payload := fwCLIDetect()
	if exit != 0 {
		t.Fatalf("exit %d", exit)
	}
	m, ok := payload.(map[string]any)
	if !ok || m["backend"] != fwBackendUfw {
		t.Fatalf("detect payload wrong: %#v", payload)
	}
}

func TestFirewallCLIPlanIsDryRunByDefault(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")

	exit, payload := fwCLIPlan([]string{"--owner", "tunnel:at_x", "--add", "8443/tcp", "--note", "cli"}, true)
	if exit != 0 {
		t.Fatalf("plan must succeed, exit %d payload %#v", exit, payload)
	}
	res, ok := payload.(fwApplyResult)
	if !ok {
		t.Fatalf("plan payload must be fwApplyResult, got %T", payload)
	}
	if !res.Plan.DryRun {
		t.Fatal("plan must always be dry-run")
	}
	if len(res.PlannedCmds) == 0 {
		t.Fatalf("planned commands must be returned: %+v", res)
	}
	for _, c := range fake.recorded() {
		if strings.Contains(c, "-A QUANTUM_FW") || strings.HasPrefix(c, "ufw allow") || strings.Contains(c, "-I INPUT") {
			t.Fatalf("plan executed a mutating command: %s", c)
		}
	}
}

func TestFirewallCLIApplyRequiresYes(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")

	exit, payload := fwCLIPlan([]string{"--owner", "tunnel:at_x", "--add", "8443/tcp"}, false)
	if exit != 2 {
		t.Fatalf("apply without --yes must be a usage error, got exit %d", exit)
	}
	if !strings.Contains(fwCLIPayloadError(payload), "--yes") {
		t.Fatalf("refusal must mention --yes, got %#v", payload)
	}
	for _, c := range fake.recorded() {
		if strings.Contains(c, "-A QUANTUM_FW") {
			t.Fatalf("unconfirmed apply mutated the host: %s", c)
		}
	}
}

func TestFirewallCLIApplyWithYesExecutes(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")

	exit, payload := fwCLIPlan([]string{"--owner", "tunnel:at_x", "--add", "8443/tcp", "--yes"}, false)
	if exit != 0 {
		t.Fatalf("confirmed apply must succeed, exit %d payload %#v", exit, payload)
	}
	res := payload.(fwApplyResult)
	if res.Plan.DryRun {
		t.Fatal("apply must not be a dry-run")
	}
	if !strings.Contains(strings.Join(fake.recorded(), "\n"), "-A QUANTUM_FW") {
		t.Fatalf("confirmed apply must execute: %v", fake.recorded())
	}
}

func TestFirewallCLIApplyFromPlanFile(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")

	plan, err := fwNewPlan([]fwOp{{Kind: "add", Rule: fwPortRule{
		Port: 9443, Proto: "tcp", Family: "ipv4", Owner: "service:panel", Note: "panel https"}}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(plan)
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	exit, payload := fwCLIPlan([]string{"--plan-file", path, "--yes"}, false)
	if exit != 0 {
		t.Fatalf("plan-file apply must succeed, exit %d payload %#v", exit, payload)
	}
	res := payload.(fwApplyResult)
	if len(res.Plan.Ops) != 1 || res.Plan.Ops[0].Rule.Port != 9443 {
		t.Fatalf("plan-file ops must be used verbatim, got %+v", res.Plan)
	}
	if !strings.Contains(strings.Join(fake.recorded(), "\n"), "--dport 9443") {
		t.Fatalf("plan-file apply must execute the reviewed ops: %v", fake.recorded())
	}
}

func TestFirewallCLITeardownGuardsAndReleases(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	spec := fwIptablesSpec(fwPortRule{Port: 7001, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"})
	fake.setResponse("iptables -S QUANTUM_FW", "-A QUANTUM_FW "+spec+"\n")
	portRegistryReset(t)
	if err := portReserve(7001, "tcp", portOwnerTunnel, "at_x", "ipv4", ""); err != nil {
		t.Fatal(err)
	}

	// Guards: owner required, --yes required.
	if exit, _ := fwCLITeardown([]string{}); exit != 2 {
		t.Fatalf("missing owner must be a usage error, got %d", exit)
	}
	if exit, _ := fwCLITeardown([]string{"--owner", "tunnel:at_x"}); exit != 2 {
		t.Fatalf("missing --yes must be a usage error, got %d", exit)
	}

	exit, payload := fwCLITeardown([]string{"--owner", "tunnel:at_x", "--yes"})
	if exit != 0 {
		t.Fatalf("confirmed teardown must succeed, exit %d payload %#v", exit, payload)
	}
	m := payload.(map[string]any)
	// The remove path drops the ownership record itself, so released_ports
	// is 0 and the registry ends empty (asserted below) — the rule and the
	// record are both gone.
	if m["released_ports"] != 0 {
		t.Fatalf("release count after the remove-path forget must be 0, got %#v", m)
	}
	if !strings.Contains(strings.Join(fake.recorded(), "\n"), "iptables -D QUANTUM_FW "+spec) {
		t.Fatalf("teardown must remove the owner's rule: %v", fake.recorded())
	}
	if got := portEntries(); len(got) != 0 {
		t.Fatalf("registry must be empty after teardown, got %+v", got)
	}
}

func TestFirewallCLIPortsListAndCheck(t *testing.T) {
	fake := portTestLab(t)
	fake.setResponse("ss -Htulnp", portTestSS)

	exit, payload := fwCLIPortsList()
	if exit != 0 {
		t.Fatalf("list exit %d", exit)
	}
	m := payload.(map[string]any)
	if _, ok := m["entries"]; !ok {
		t.Fatalf("list must expose registry entries: %#v", m)
	}
	if _, ok := m["listeners"]; !ok {
		t.Fatalf("list must expose live listeners: %#v", m)
	}
	if _, ok := m["conflicts"]; !ok {
		t.Fatalf("list must expose conflicts: %#v", m)
	}

	exit, payload = fwCLIPortsCheck([]string{"8443"})
	if exit != 0 {
		t.Fatalf("check exit %d", exit)
	}
	rep := payload.(map[string]any)
	if _, ok := rep["tcp"]; !ok {
		t.Fatalf("check without proto must report both transports: %#v", rep)
	}

	exit, payload = fwCLIPortsCheck([]string{"8443/tcp"})
	if exit != 0 {
		t.Fatalf("check exit %d", exit)
	}
	avail := payload.(map[string]any)["tcp"].(portAvailability)
	if avail.Available || avail.Listener == nil || avail.Listener.Process != "xray" {
		t.Fatalf("tcp 8443 must show the live xray listener, got %+v", avail)
	}

	if exit, _ := fwCLIPortsCheck([]string{"nope"}); exit != 2 {
		t.Fatalf("bad spec must be a usage error, got %d", exit)
	}
}

func TestFirewallCLIPortSpecParsing(t *testing.T) {
	port, proto, err := fwCLIParsePortSpec("443")
	if err != nil || port != 443 || proto != "" {
		t.Fatalf("bare port parse: %d %q %v", port, proto, err)
	}
	port, proto, err = fwCLIParsePortSpec("443/udp")
	if err != nil || port != 443 || proto != "udp" {
		t.Fatalf("port/proto parse: %d %q %v", port, proto, err)
	}
	if _, _, err := fwCLIParsePortSpec("443/gre"); err == nil {
		t.Fatal("unknown proto must be rejected")
	}
	var specs fwSpecFlags
	if err := specs.Set("8443/tcp"); err != nil || len(specs) != 1 || specs[0].Port != 8443 {
		t.Fatalf("spec flag parse: %+v err %v", specs, err)
	}
	if err := specs.Set("8443"); err == nil {
		t.Fatal("add without proto must be rejected (explicit tcp|udp required)")
	}
}

// fwCLIPayloadError extracts the "error" string from a CLI payload.
func fwCLIPayloadError(payload any) string {
	v := reflect.ValueOf(payload)
	if v.Kind() == reflect.Map {
		if e, ok := v.MapIndex(reflect.ValueOf("error")).Interface().(string); ok {
			return e
		}
	}
	return ""
}
