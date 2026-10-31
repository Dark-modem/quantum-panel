package main

// Hermetic tests for the port registry (portregistry.go). Listener and
// ephemeral-range reads run through seams; the tests run on any OS. The
// real ss/proc behavior is exercised by tests/test_firewall_lab.sh.

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// portRegistryReset points the registry at the current (temp) configDir.
func portRegistryReset(t *testing.T) {
	t.Helper()
	old := portRegPath
	portRegPath = ""
	t.Cleanup(func() { portRegPath = old })
}

// portTestLab is fwTestLab plus a clean registry path.
func portTestLab(t *testing.T) *fwFakeHost {
	t.Helper()
	fake := fwTestLab(t)
	portRegistryReset(t)
	// Deterministic ephemeral range on every host.
	oldRange := portEphemeralRange
	portEphemeralRange = func() (int, int) { return 32768, 60999 }
	t.Cleanup(func() { portEphemeralRange = oldRange })
	return fake
}

const portTestSS = `tcp   LISTEN 0      128          0.0.0.0:8443       0.0.0.0:*    users:(("xray",pid=9,fd=3))
udp   UNCONN 0      0            0.0.0.0:5353       0.0.0.0:*    users:(("systemd-resolved",pid=420,fd=12))
tcp   LISTEN 0      128             [::]:22            [::]:*    users:(("sshd",pid=7,fd=3))
udp   UNCONN 0      0               [::]:51820         [::]:*
garbage line that must be skipped
`

func TestPortRegistryReserveReleaseIdempotent(t *testing.T) {
	portTestLab(t)
	if err := portReserve(8080, "tcp", portOwnerTunnel, "at_x", "ipv4", "first"); err != nil {
		t.Fatal(err)
	}
	// Same owner: idempotent re-reserve.
	if err := portReserve(8080, "tcp", portOwnerTunnel, "at_x", "both", "updated"); err != nil {
		t.Fatalf("same-owner re-reserve must be idempotent: %v", err)
	}
	// Different owner: conflict.
	if err := portReserve(8080, "tcp", portOwnerService, "panel", "", ""); err == nil {
		t.Fatal("a different owner must not be able to reserve a held port")
	}
	// Wrong owner cannot release.
	if err := portRelease(8080, "tcp", portOwnerService, "panel"); err == nil {
		t.Fatal("a non-owner must not be able to release")
	}
	// Right owner releases; releasing again is a no-op.
	if err := portRelease(8080, "tcp", portOwnerTunnel, "at_x"); err != nil {
		t.Fatal(err)
	}
	if err := portRelease(8080, "tcp", portOwnerTunnel, "at_x"); err != nil {
		t.Fatalf("releasing a free port must be a no-op: %v", err)
	}
	if got := portEntries(); len(got) != 0 {
		t.Fatalf("registry must be empty after release, got %+v", got)
	}
}

func TestPortRegistryTCPAndUDPIndependent(t *testing.T) {
	portTestLab(t)
	if err := portReserve(443, "tcp", portOwnerTunnel, "at_x", "both", ""); err != nil {
		t.Fatal(err)
	}
	if err := portReserve(443, "udp", portOwnerTunnel, "at_x", "both", ""); err != nil {
		t.Fatalf("the same port number on UDP must be independently reservable: %v", err)
	}
	if got := portEntries(); len(got) != 2 {
		t.Fatalf("two entries expected, got %+v", got)
	}
}

func TestPortRegistryEphemeralRangeRefused(t *testing.T) {
	portTestLab(t)
	if err := portReserve(35000, "tcp", portOwnerService, "panel", "", ""); err == nil {
		t.Fatal("reserving inside the kernel ephemeral range must be refused")
	}
	if err := portReserve(8080, "tcp", portOwnerService, "panel", "", ""); err != nil {
		t.Fatalf("outside the range must be fine: %v", err)
	}
	a := portAvailabilityCheck(35000, "tcp")
	if a.Available || !strings.Contains(a.Reason, "ephemeral") {
		t.Fatalf("availability must explain the ephemeral conflict, got %+v", a)
	}
}

func TestPortRegistryReserveRefusedWhileForeignListener(t *testing.T) {
	fake := portTestLab(t)
	fake.setResponse("ss -Htulnp", portTestSS)
	if err := portReserve(5353, "udp", portOwnerService, "dns", "", ""); err == nil {
		t.Fatal("reserving a port a foreign process listens on must be refused")
	} else if !strings.Contains(err.Error(), "systemd-resolved") {
		t.Fatalf("the refusal must name the process, got %v", err)
	}
}

func TestPortRegistryAvailabilityChecksBothTransports(t *testing.T) {
	fake := portTestLab(t)
	fake.setResponse("ss -Htulnp", portTestSS)

	tcp := portAvailabilityCheck(8443, "tcp")
	if tcp.Available {
		t.Fatal("tcp 8443 has a live listener and must be unavailable")
	}
	if tcp.Listener == nil || tcp.Listener.Process != "xray" || tcp.Listener.PID != 9 {
		t.Fatalf("process ownership must be reported, got %+v", tcp.Listener)
	}
	udp := portAvailabilityCheck(8443, "udp")
	if !udp.Available {
		t.Fatalf("udp 8443 is free in this namespace, got %+v", udp)
	}
	if !strings.Contains(udp.OtherTransport, "tcp listener") {
		t.Fatalf("the report must surface the sibling transport's usage, got %+v", udp)
	}

	// A registry reservation wins over everything.
	if err := portReserve(9000, "udp", portOwnerTunnel, "at_x", "both", ""); err != nil {
		t.Fatal(err)
	}
	res := portAvailabilityCheck(9000, "udp")
	if res.Available || res.ReservedBy != "tunnel:at_x" {
		t.Fatalf("reserved port must be reported as reserved, got %+v", res)
	}
	if rep := portReport(9000); rep["tcp"] == nil || rep["udp"] == nil {
		t.Fatalf("port report must cover both transports: %+v", rep)
	}
}

func TestPortRegistryConcurrentAllocation(t *testing.T) {
	portTestLab(t) // ss returns nothing: no live listeners
	const port = 8080
	const workers = 10
	var mu sync.Mutex
	winners, losers := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			err := portReserve(port, "tcp", portOwnerTunnel, fmt.Sprintf("at_%02d", i), "", "")
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				winners++
			} else {
				losers++
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 || losers != workers-1 {
		t.Fatalf("exactly one concurrent reservation may win, got %d winners / %d losers", winners, losers)
	}
	// The other transport is independent.
	if err := portReserve(port, "udp", portOwnerTunnel, "at_udp", "", ""); err != nil {
		t.Fatalf("udp reservation must not fight the tcp one: %v", err)
	}
}

func TestPortRegistryOwnerMappingAndRelease(t *testing.T) {
	portTestLab(t)
	for _, p := range []struct {
		port  int
		proto string
	}{{7001, "tcp"}, {7002, "udp"}, {7003, "tcp"}} {
		if err := portReserve(p.port, p.proto, portOwnerTunnel, "at_x", "both", ""); err != nil {
			t.Fatal(err)
		}
	}
	if got := portEntriesForOwner("tunnel:at_x"); len(got) != 3 {
		t.Fatalf("tunnel mapping must expose all three ports, got %+v", got)
	}
	n, err := portReleaseOwner(portOwnerTunnel, "at_x")
	if err != nil || n != 3 {
		t.Fatalf("owner release must free all three, got %d err %v", n, err)
	}
	if got := portEntriesForOwner("tunnel:at_x"); len(got) != 0 {
		t.Fatalf("entries must be gone, got %+v", got)
	}
	if n, err := portReleaseOwner(portOwnerTunnel, "at_x"); err != nil || n != 0 {
		t.Fatalf("repeated owner release is a no-op, got %d err %v", n, err)
	}
}

func TestPortRegistryListenerParsing(t *testing.T) {
	portTestLab(t)
	fake := fwTestLab(t)
	_ = fake
	old := portSSRun
	portSSRun = func() (string, error) { return portTestSS, nil }
	t.Cleanup(func() { portSSRun = old })

	listeners, err := portListeners()
	if err != nil {
		t.Fatal(err)
	}
	if len(listeners) != 4 {
		t.Fatalf("4 listeners expected, got %d: %+v", len(listeners), listeners)
	}
	byKey := map[string]*portListener{}
	for i := range listeners {
		byKey[listeners[i].Proto+":"+fmt.Sprint(listeners[i].Port)] = &listeners[i]
	}
	x := byKey["tcp:8443"]
	if x == nil || x.Process != "xray" || x.PID != 9 || x.Address != "0.0.0.0:8443" {
		t.Fatalf("tcp listener parse wrong: %+v", x)
	}
	d := byKey["udp:5353"]
	if d == nil || d.Process != "systemd-resolved" || d.PID != 420 {
		t.Fatalf("udp listener parse wrong: %+v", d)
	}
	s := byKey["tcp:22"]
	if s == nil || s.Address != "[::]:22" {
		t.Fatalf("ipv6 listener parse wrong: %+v", s)
	}
	w := byKey["udp:51820"]
	if w == nil || w.Process != "" {
		t.Fatalf("listener without process info must parse, got %+v", w)
	}
}

func TestPortRegistryConflictsDetectForeignProcesses(t *testing.T) {
	portTestLab(t)
	fake := fwTestLab(t)
	_ = fake
	if err := portReserve(8080, "tcp", portOwnerService, "panel", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := portReserve(7001, "tcp", portOwnerTunnel, "at_x", "", ""); err != nil {
		t.Fatal(err)
	}
	old := portSSRun
	portSSRun = func() (string, error) {
		return `tcp   LISTEN 0      128          0.0.0.0:8080       0.0.0.0:*    users:(("nginx",pid=5,fd=6))
tcp   LISTEN 0      128          0.0.0.0:7001       0.0.0.0:*    users:(("xray",pid=9,fd=3))
`, nil
	}
	t.Cleanup(func() { portSSRun = old })

	conflicts := portConflicts()
	if len(conflicts) != 1 {
		t.Fatalf("exactly one foreign conflict expected, got %+v", conflicts)
	}
	if conflicts[0]["owner"] != "service:panel" {
		t.Fatalf("the conflict must name the registry owner, got %+v", conflicts[0])
	}
}

func TestPortRegistryPersistenceRoundTrip(t *testing.T) {
	portTestLab(t)
	if err := portReserve(9443, "tcp", portOwnerService, "panel", "ipv4", "panel https"); err != nil {
		t.Fatal(err)
	}
	// A fresh load (new path resolution, same dir) must see the entry.
	entries := portEntries()
	if len(entries) != 1 || entries[0].Port != 9443 || entries[0].Note != "panel https" {
		t.Fatalf("round trip failed: %+v", entries)
	}
}
