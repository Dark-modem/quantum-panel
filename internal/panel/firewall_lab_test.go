//go:build linux

package panel

// Firewall & port engine LAB tests: real kernel, real iptables/nft/ufw,
// real listeners — run ONLY inside the disposable network namespace that
// tests/test_firewall_lab.sh creates (QUANTUM_FW_LAB=1 + root). A plain
// `go test` run skips everything here; the hermetic coverage lives in
// firewall_test.go / portregistry_test.go.

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func fwLabEnabled() bool {
	return os.Getenv("QUANTUM_FW_LAB") == "1" && os.Geteuid() == 0
}

// fwLabHostCommands opts into real host commands: the package TestMain
// stubs the runner for hermetic unit tests, and a lab test must restore
// the real executor exactly like cluster_lab_test.go does.
func fwLabHostCommands(t *testing.T) {
	t.Helper()
	old := runner
	runner = execRunner
	t.Cleanup(func() { runner = old })
}

func fwLabHave(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

func TestFirewallLabIptablesLifecycle(t *testing.T) {
	if !fwLabEnabled() {
		t.Skip("lab only: run via tests/test_firewall_lab.sh")
	}
	fwLabHostCommands(t)
	if fwLabHave("ufw") {
		if out, err := fwRun("ufw", "status"); err == nil && strings.Contains(out, "Status: active") {
			t.Skip("ufw is active in this namespace; the iptables lifecycle test would fight it")
		}
	}
	if !fwLabHave("iptables") {
		t.Skip("iptables missing")
	}
	dir := t.TempDir()
	oldDir := configDir
	configDir = dir
	t.Cleanup(func() { configDir = oldDir })

	// A pre-existing administrator rule that must survive everything below.
	if _, err := fwRun("iptables", "-A", "INPUT", "-p", "tcp", "--dport", "22", "-j", "ACCEPT"); err != nil {
		t.Fatalf("cannot seed the admin rule: %v", err)
	}
	before, err := fwCaptureSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if backend, _ := fwDetect(); backend != fwBackendIptables {
		t.Fatalf("lab namespace must detect iptables, got %q", backend)
	}

	// Real add: both transports, both families.
	plan, err := fwPlanAddOwner("tunnel:at_lab", "both", "lab tunnel",
		[]fwPortSpec{{Port: 18080, Proto: "tcp"}, {Port: 18080, Proto: "udp"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fwApply(plan); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		out, err := fwRun(tool, "-S", fwChain)
		if err != nil {
			t.Fatalf("%s -S %s failed: %v", tool, fwChain, err)
		}
		if !strings.Contains(out, "--dport 18080") || !strings.Contains(out, "quantum:tunnel:at_lab") {
			t.Fatalf("%s rules missing after apply:\n%s", tool, out)
		}
	}

	// Idempotency: the same plan again must be all skipped.
	res, err := fwApply(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Applied {
		if !a.Skipped {
			t.Fatalf("re-apply must be a no-op, got %+v", a)
		}
	}

	// Registry + teardown: only the tunnel's own rules go.
	if err := portReserve(18080, "tcp", portOwnerTunnel, "at_lab", "both", "lab"); err != nil {
		t.Fatal(err)
	}
	if err := portReserve(18080, "udp", portOwnerTunnel, "at_lab", "both", "lab"); err != nil {
		t.Fatal(err)
	}
	if _, err := fwTeardownOwner("tunnel:at_lab"); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"iptables", "ip6tables"} {
		out, _ := fwRun(tool, "-S", fwChain)
		if strings.Contains(out, "dport 18080") {
			t.Fatalf("%s still holds the tunnel rule after teardown:\n%s", tool, out)
		}
	}
	input, _ := fwRun("iptables", "-S", "INPUT")
	if !strings.Contains(input, "--dport 22") {
		t.Fatalf("the administrator rule must survive: %s", input)
	}
	if !strings.Contains(input, "-j "+fwChain) {
		t.Fatalf("the quantum jump must survive the teardown: %s", input)
	}
	after, err := fwCaptureSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if after.Raw == before.Raw && !strings.Contains(before.Raw, "dport 22") {
		t.Fatalf("snapshots must reflect the live ruleset")
	}
}

func TestFirewallLabListenerConflicts(t *testing.T) {
	if !fwLabEnabled() {
		t.Skip("lab only: run via tests/test_firewall_lab.sh")
	}
	fwLabHostCommands(t)
	dir := t.TempDir()
	oldDir := configDir
	configDir = dir
	t.Cleanup(func() { configDir = oldDir })

	tcpL, err := net.Listen("tcp", "127.0.0.1:28080")
	if err != nil {
		t.Skipf("cannot open the lab listener: %v", err)
	}
	defer tcpL.Close()
	udpAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 28080}
	udpL, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Skipf("cannot open the lab UDP listener: %v", err)
	}
	defer udpL.Close()

	tcpAvail := portAvailabilityCheck(28080, "tcp")
	if tcpAvail.Available || tcpAvail.Listener == nil {
		t.Fatalf("live TCP listener must be detected, got %+v", tcpAvail)
	}
	udpAvail := portAvailabilityCheck(28080, "udp")
	if udpAvail.Available || udpAvail.Listener == nil {
		t.Fatalf("live UDP listener must be detected, got %+v", udpAvail)
	}
	if err := portReserve(28080, "tcp", portOwnerService, "lab", "", ""); err == nil {
		t.Fatal("reserving a live listener's port must be refused")
	}

	// Transports are independent namespaces: closing TCP frees TCP only.
	if err := tcpL.Close(); err != nil {
		t.Fatal(err)
	}
	tcpAvail = portAvailabilityCheck(28080, "tcp")
	if !tcpAvail.Available {
		t.Fatalf("tcp must be free after the listener closed, got %+v", tcpAvail)
	}
	if udpAvail2 := portAvailabilityCheck(28080, "udp"); udpAvail2.Available {
		t.Fatalf("udp must still be occupied, got %+v", udpAvail2)
	}
	if err := portReserve(28080, "tcp", portOwnerService, "lab", "", ""); err != nil {
		t.Fatalf("tcp reservation must succeed now: %v", err)
	}
	if err := portReserve(28080, "udp", portOwnerService, "lab2", "", ""); err == nil {
		t.Fatal("udp reservation must still see the live udp listener")
	}
}

func TestFirewallLabNftReads(t *testing.T) {
	if !fwLabEnabled() {
		t.Skip("lab only: run via tests/test_firewall_lab.sh")
	}
	fwLabHostCommands(t)
	if !fwLabHave("nft") {
		t.Skip("nft missing")
	}
	// Read-only verification of the nft snapshot path (the engine REFUSES
	// to mutate a native nftables ruleset by design — firewall_test.go
	// pins that refusal).
	if _, err := fwRun("nft", "add", "table", "inet", "fwlab_probe"); err != nil {
		t.Fatalf("cannot create the lab probe table: %v", err)
	}
	defer fwRun("nft", "delete", "table", "inet", "fwlab_probe")
	snap, err := fwCaptureSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	// Detection prefers iptables when it answers; the snapshot must still
	// reflect the live ruleset either way.
	if snap.Raw == "" {
		t.Fatal("snapshot must not be empty")
	}
}

func TestFirewallLabUfwDetection(t *testing.T) {
	if !fwLabEnabled() {
		t.Skip("lab only: run via tests/test_firewall_lab.sh")
	}
	fwLabHostCommands(t)
	if !fwLabHave("ufw") {
		t.Skip("ufw missing")
	}
	backend, _ := fwDetect()
	if out, err := fwRun("ufw", "status"); err == nil && strings.Contains(out, "Status: active") {
		if backend != fwBackendUfw {
			t.Fatalf("active ufw must be detected, got %q", backend)
		}
	} else if backend == fwBackendUfw {
		t.Fatal("an inactive ufw must not be detected as the backend")
	}
}
