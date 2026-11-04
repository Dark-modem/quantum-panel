package panel

// Hermetic tests for the firewall engine (firewall.go). The host is a
// scripted fake behind the fwRun seam: no ufw/iptables/nft binary runs,
// and the safety invariants (never a global flush, only Quantum-owned
// rules touched) are asserted over the RECORDED argv, which is exactly
// what the real backend would execute. Real-backend behavior is covered
// separately by tests/test_firewall_lab.sh on Linux.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fwFakeHost records every command and answers from a script.
type fwFakeHost struct {
	mu        sync.Mutex
	calls     []string
	responses map[string]string
	failOn    []string
}

func (f *fwFakeHost) run(name string, args ...string) (string, error) {
	key := name + " " + strings.Join(args, " ")
	f.mu.Lock()
	f.calls = append(f.calls, key)
	responses := f.responses
	failOn := append([]string(nil), f.failOn...)
	f.mu.Unlock()
	for _, frag := range failOn {
		if strings.Contains(key, frag) {
			return "", fmt.Errorf("injected failure at %q", key)
		}
	}
	if v, ok := responses[key]; ok {
		if v == "!!!" {
			// Scripted failure: the command exists but exits non-zero
			// (missing binary, missing chain, …).
			return "", fmt.Errorf("scripted failure for %q", key)
		}
		return v, nil
	}
	return "", nil
}

// adopt copies another fake's scripted responses into this one (the mutex
// and recorded history stay).
func (f *fwFakeHost) adopt(other *fwFakeHost) {
	other.mu.Lock()
	defer other.mu.Unlock()
	for k, v := range other.responses {
		f.responses[k] = v
	}
}

func (f *fwFakeHost) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fwFakeHost) setResponse(key, out string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responses[key] = out
}

func (f *fwFakeHost) failWhen(frag string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failOn = append(f.failOn, frag)
}

// fwTestLab redirects configDir (audit file) and installs the fake host.
func fwTestLab(t *testing.T) *fwFakeHost {
	t.Helper()
	dir := t.TempDir()
	oldDir := configDir
	configDir = dir
	t.Cleanup(func() { configDir = oldDir })
	fake := &fwFakeHost{responses: map[string]string{}}
	oldRun := fwRun
	fwRun = fake.run
	t.Cleanup(func() { fwRun = oldRun })
	return fake
}

func fwIptablesHost() *fwFakeHost {
	fake := &fwFakeHost{responses: map[string]string{
		"ufw status":             "command not found",
		"iptables -S":            "-P INPUT accept\n-P FORWARD accept\n-P OUTPUT accept\n-A INPUT -m comment --comment admin-web -p tcp --dport 22 -j ACCEPT\n",
		"iptables --version":     "iptables v1.8.7 (nf_tables)",
		"ip6tables -S":           "-P INPUT accept\n",
		"iptables -S INPUT":      "-P INPUT accept\n-A INPUT -m comment --comment admin-web -p tcp --dport 22 -j ACCEPT\n",
		"iptables -S QUANTUM_FW": "!!!", // the chain does not exist yet
	}}
	return fake
}

func TestFirewallDetectUfwWinsOverIptables(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("ufw status", "Status: active")
	fake.setResponse("iptables -S", "-P INPUT accept\n")
	backend, detail := fwDetect()
	if backend != fwBackendUfw {
		t.Fatalf("active ufw must win, got %q (%s)", backend, detail)
	}
}

func TestFirewallDetectIptablesCompatLayer(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("ufw status", "")
	fake.setResponse("iptables -S", "-P INPUT accept\n")
	fake.setResponse("iptables --version", "iptables v1.8.7 (nf_tables)")
	backend, detail := fwDetect()
	if backend != fwBackendIptables {
		t.Fatalf("iptables expected, got %q (%s)", backend, detail)
	}
	if !strings.Contains(detail, "nf_tables") {
		t.Fatalf("the nft compatibility layer must be visible in the detail, got %q", detail)
	}
}

func TestFirewallDetectNftNative(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("iptables -S", "!!!")
	backend, _ := fwDetect()
	if backend != fwBackendNft {
		t.Fatalf("nftables expected when iptables is unavailable, got %q", backend)
	}
}

func TestFirewallDetectNone(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("iptables -S", "!!!")
	fake.setResponse("nft list tables", "!!!")
	backend, _ := fwDetect()
	if backend != fwBackendNone {
		t.Fatalf("none expected, got %q", backend)
	}
}

func TestFirewallUfwPlanDryRunAndApply(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("ufw status", "Status: active")
	fake.setResponse("ufw status verbose", "Status: active\n443/tcp                     ALLOW IN    Anywhere\n")

	plan, err := fwNewPlan([]fwOp{{Kind: "add", Rule: fwPortRule{
		Port: 443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x", Note: "first tunnel"}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Backend != fwBackendUfw {
		t.Fatalf("plan backend must be ufw, got %q", plan.Backend)
	}

	dry := plan
	dry.DryRun = true
	res, err := fwApply(dry)
	if err != nil {
		t.Fatal(err)
	}
	want := "ufw allow 443/tcp comment quantum:tunnel:at_x"
	if len(res.PlannedCmds) != 1 || strings.Join(res.PlannedCmds[0], " ") != want {
		t.Fatalf("dry-run commands wrong: %v", res.PlannedCmds)
	}
	// Dry-run must not mutate: no allow command may have been executed.
	for _, c := range fake.recorded() {
		if strings.HasPrefix(c, "ufw allow") {
			t.Fatalf("dry-run executed a mutating command: %s", c)
		}
	}

	plan.DryRun = false
	res, err = fwApply(plan)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range fake.recorded() {
		if c == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("apply must run %q, recorded: %v", want, fake.recorded())
	}
}

func TestFirewallUfwRemoveGuardedByIdempotency(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("ufw status", "Status: active") // rule NOT present

	plan, err := fwNewPlan([]fwOp{{Kind: "remove", Rule: fwPortRule{
		Port: 443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := fwApply(plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Applied) != 1 || !res.Applied[0].Skipped {
		t.Fatalf("removing an absent rule must be a skipped no-op, got %+v", res.Applied)
	}
	for _, c := range fake.recorded() {
		if strings.HasPrefix(c, "ufw delete") {
			t.Fatalf("no delete may run for an absent rule: %s", c)
		}
	}

	// With the rule present the delete runs.
	fake.setResponse("ufw status", "Status: active\n443/tcp                     ALLOW IN    Anywhere\n")
	res, err = fwApply(plan)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied[0].Skipped {
		t.Fatal("removing a present rule must not be skipped")
	}
	found := false
	for _, c := range fake.recorded() {
		if c == "ufw delete allow 443/tcp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("exact-rule delete missing, recorded: %v", fake.recorded())
	}
}

func TestFirewallIptablesAddRemoveIdempotent(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())

	spec := fwIptablesSpec(fwPortRule{Port: 443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"})
	plan, err := fwNewPlan([]fwOp{{Kind: "add", Rule: fwPortRule{
		Port: 443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Backend != fwBackendIptables {
		t.Fatalf("plan backend must be iptables, got %q", plan.Backend)
	}
	if _, err := fwApply(plan); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.recorded(), "\n")
	for _, want := range []string{
		"iptables -N QUANTUM_FW",
		"iptables -I INPUT 1 -m comment --comment quantum-fw -j QUANTUM_FW",
		"iptables -A QUANTUM_FW " + spec,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}

	// Second add: the rule now exists → skipped, no duplicate -A.
	fake.setResponse("iptables -S QUANTUM_FW", "-A QUANTUM_FW "+spec+"\n")
	before := len(fake.recorded())
	if _, err := fwApply(plan); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.recorded()[before:] {
		if strings.Contains(c, "-A QUANTUM_FW") {
			t.Fatalf("idempotent re-add must not append a duplicate: %s", c)
		}
	}

	// Remove: exact-spec delete, then absent remove is a no-op.
	remove, err := fwNewPlan([]fwOp{{Kind: "remove", Rule: fwPortRule{
		Port: 443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fwApply(remove); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(fake.recorded(), "\n"), "iptables -D QUANTUM_FW "+spec) {
		t.Fatalf("exact-spec delete missing in:\n%s", strings.Join(fake.recorded(), "\n"))
	}
	// The host state moved on: the chain exists, the rule is gone.
	fake.setResponse("iptables -S QUANTUM_FW", "")
	before = len(fake.recorded())
	if _, err := fwApply(remove); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.recorded()[before:] {
		if strings.Contains(c, "-D QUANTUM_FW") {
			t.Fatalf("absent remove must be a no-op: %s", c)
		}
	}
}

func TestFirewallUnrelatedRulesPreserved(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("ufw status", "")
	fake.setResponse("iptables -S", "-P INPUT accept\n-A INPUT -m comment --comment admin-web -p tcp --dport 22 -j ACCEPT\n-A INPUT -p tcp --dport 2222 -j ACCEPT\n-A OUTPUT -j ACCEPT\n")
	fake.setResponse("iptables -S INPUT", "-P INPUT accept\n-A INPUT -m comment --comment admin-web -p tcp --dport 22 -j ACCEPT\n")
	fake.setResponse("iptables -S QUANTUM_FW", "")
	fake.setResponse("ip6tables -S", "-P INPUT accept\n")

	plan, err := fwNewPlan([]fwOp{
		{Kind: "add", Rule: fwPortRule{Port: 8443, Proto: "tcp", Family: "both", Owner: "tunnel:at_a"}},
		{Kind: "remove", Rule: fwPortRule{Port: 8443, Proto: "tcp", Family: "both", Owner: "tunnel:at_a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fwApply(plan); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.recorded() {
		if strings.Contains(c, "dport 22") || strings.Contains(c, "dport 2222") || strings.Contains(c, "OUTPUT") {
			t.Fatalf("a mutating command touched a foreign rule: %s", c)
		}
		if strings.HasPrefix(c, "iptables -D INPUT") || strings.HasPrefix(c, "ip6tables -D INPUT") {
			t.Fatalf("foreign chain mutated: %s", c)
		}
	}
}

func TestFirewallForbiddenCommandsRefused(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"ufw", []string{"reset"}, true},
		{"ufw", []string{"allow", "443/tcp"}, false},
		{"iptables", []string{"-F"}, true},
		{"iptables", []string{"-F", fwChain}, false},
		{"iptables", []string{"-X"}, true},
		{"iptables", []string{"-X", "INPUT"}, true},
		{"iptables", []string{"-X", fwChain}, false},
		{"iptables", []string{"-t", "nat", "-F"}, true},
		{"iptables", []string{"-t", "nat", "-F", "QUANTUM_FW"}, true}, // nat stays off-limits
		{"iptables", []string{"-A", fwChain, "-p", "tcp", "--dport", "1", "-j", "ACCEPT"}, false},
		{"nft", []string{"delete", "table", "inet", "filter"}, true},
		{"nft", []string{"flush", "ruleset"}, true},
		{"nft", []string{"list", "ruleset"}, false},
	}
	for _, tc := range cases {
		if got := fwForbidden(tc.name, tc.args); got != tc.want {
			t.Fatalf("fwForbidden(%s %v) = %v, want %v", tc.name, tc.args, got, tc.want)
		}
	}
}

func TestFirewallRollbackAfterPartialFailure(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")
	// The second rule's append fails.
	fake.failWhen("--dport 20001")

	plan, err := fwNewPlan([]fwOp{
		{Kind: "add", Rule: fwPortRule{Port: 20000, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_r"}},
		{Kind: "add", Rule: fwPortRule{Port: 20001, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_r"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := fwApply(plan)
	if err == nil {
		t.Fatalf("the plan must fail, got %+v", res)
	}
	spec0 := fwIptablesSpec(fwPortRule{Port: 20000, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_r"})
	if !strings.Contains(strings.Join(fake.recorded(), "\n"), "iptables -D QUANTUM_FW "+spec0) {
		t.Fatalf("rollback must undo the applied rule, recorded:\n%s", strings.Join(fake.recorded(), "\n"))
	}
	if res.RolledBack != 1 {
		t.Fatalf("one applied step must be rolled back, got %d", res.RolledBack)
	}
}

func TestFirewallTunnelTeardownRemovesOnlyOwnRules(t *testing.T) {
	fake := fwTestLab(t)
	fake.adopt(fwIptablesHost())
	fake.setResponse("iptables -S QUANTUM_FW", "")
	portRegistryReset(t) // registry on the same temp configDir

	// Two tunnels each hold one port; the registry is the teardown source.
	if err := portReserve(7001, "tcp", portOwnerTunnel, "at_a", "ipv4", ""); err != nil {
		t.Fatal(err)
	}
	if err := portReserve(7002, "tcp", portOwnerTunnel, "at_b", "ipv4", ""); err != nil {
		t.Fatal(err)
	}
	// Both rules already exist on the host.
	specA := fwIptablesSpec(fwPortRule{Port: 7001, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_a"})
	specB := fwIptablesSpec(fwPortRule{Port: 7002, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_b"})
	fake.setResponse("iptables -S QUANTUM_FW", "-A QUANTUM_FW "+specA+"\n-A QUANTUM_FW "+specB+"\n")

	if _, err := fwTeardownOwner("tunnel:at_a"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(fake.recorded(), "\n")
	if !strings.Contains(joined, "iptables -D QUANTUM_FW "+specA) {
		t.Fatalf("at_a's rule must be removed, recorded:\n%s", joined)
	}
	if strings.Contains(joined, "iptables -D QUANTUM_FW "+specB) {
		t.Fatalf("at_b's rule must stay, recorded:\n%s", joined)
	}
	if _, err := portReleaseOwner(portOwnerTunnel, "at_a"); err != nil {
		t.Fatal(err)
	}
	if got := portEntriesForOwner("tunnel:at_a"); len(got) != 0 {
		t.Fatalf("at_a registry entries must be released, got %+v", got)
	}
	if got := portEntriesForOwner("tunnel:at_b"); len(got) != 1 {
		t.Fatalf("at_b registry entries must stay, got %+v", got)
	}
}

func TestFirewallNftNativeRefusesMutation(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("ufw status", "")
	fake.setResponse("iptables -S", "!!!")
	fake.setResponse("nft list tables", "table inet admin_fw {}\n")

	plan, err := fwNewPlan([]fwOp{{Kind: "add", Rule: fwPortRule{
		Port: 443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"}}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := fwApply(plan)
	if err == nil {
		t.Fatal("native nftables mutation must be refused")
	}
	if res.Error != fwErrNftForeign.Error() {
		t.Fatalf("refusal must name the foreign ruleset policy, got %q", res.Error)
	}
	for _, c := range fake.recorded() {
		if strings.HasPrefix(c, "nft add") || strings.HasPrefix(c, "nft insert") || strings.HasPrefix(c, "nft delete") {
			t.Fatalf("nftables mutation executed despite the refusal: %s", c)
		}
	}
}

func TestFirewallAuditTrailRecordsSnapshots(t *testing.T) {
	fake := fwTestLab(t)
	fake.setResponse("ufw status", "Status: active")
	fake.setResponse("ufw status verbose", "Status: active\n443/tcp ALLOW IN Anywhere\n")

	plan, err := fwNewPlan([]fwOp{{Kind: "add", Rule: fwPortRule{
		Port: 443, Proto: "tcp", Family: "ipv4", Owner: "tunnel:at_x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fwApply(plan); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "firewall_audit.jsonl"))
	if err != nil {
		t.Fatalf("audit trail missing: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("one audit entry expected, got %d", len(lines))
	}
	var e fwAuditEntry
	if err := json.Unmarshal([]byte(lines[0]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Backend != fwBackendUfw || e.Applied != 1 || e.Before == "" || e.After == "" {
		t.Fatalf("audit entry incomplete: %+v", e)
	}
	if len(e.Cmds) != 1 || e.Cmds[0][0] != "ufw" {
		t.Fatalf("audit must record the executed commands: %+v", e.Cmds)
	}
}
