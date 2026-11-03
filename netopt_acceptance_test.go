package main

// Acceptance tests for the Network Optimization subsystem (Session 2, Linux
// acceptance round). These pin the CONTRACTS the subsystem must keep:
//
//   - the Iran-like low-resource server (1 vCPU, ~956 MB RAM) selects
//     Conservative and the exact changes are enumerated;
//   - asymmetric routes / Tinc-Matrix interfaces are never fought:
//     no rp_filter, no interface MTU changes, no MSS rules;
//   - SSH / firewall / ports are structurally out of reach: no candidate may
//     carry a key that belongs to those domains;
//   - profile selection stays explainable on every fixture.
//
// All fixtures are synthetic (seams); nothing here reads or writes the host.

import (
	"strings"
	"testing"
)

// netIranLikeFixture: 1 vCPU, 956 MB RAM, 61% available (no pressure but
// tiny), KVM VM, 1 Gbit/s virtio NIC — the Iran test server shape. The host
// already carries quantum.sh tuning (somaxconn=65535), so Conservative also
// exercises convergence downward with a captured baseline.
func netIranLikeFixture() (map[string]string, map[string][]string, map[string]string) {
	files, dirs, exec := netIfaceFixtures()
	files["/proc/meminfo"] = "MemTotal:        979000 kB\nMemAvailable:    598000 kB\nSwapTotal:       512000 kB\nSwapFree:        512000 kB\n"
	files["/proc/cpuinfo"] = "processor : 0\nmodel name : Common KVM processor\n"
	exec["systemd-detect-virt"] = "kvm\n"
	files["/proc/sys/net/core/somaxconn"] = "65535\n"
	// virtio primary: no ring headroom, single queue
	files["/sys/class/net/eth0/device/driver"] = "/sys/bus/virtio/drivers/virtio_net"
	dirs["/sys/class/net/eth0/queues"] = []string{"rx-0", "tx-0"}
	files["/proc/sys/net/ipv4/tcp_available_congestion_control"] = "reno cubic bbr\n"
	return files, dirs, exec
}

// TestAcceptIranLikeServerSelectsConservative pins the low-resource outcome.
func TestAcceptIranLikeServerSelectsConservative(t *testing.T) {
	files, dirs, exec := netIranLikeFixture()
	netTestEnv(t, files, dirs, exec)
	rep := netDetectHardware()
	if rep.RAMTotalMB != 956 {
		t.Fatalf("ram = %d MB, want 956", rep.RAMTotalMB)
	}
	if rep.CPUCores != 1 {
		t.Fatalf("cores = %d, want 1", rep.CPUCores)
	}
	profile, why := netSelectProfile(rep, netRoleAgent, "")
	if profile != netProfileConservative {
		t.Fatalf("Iran-like server selected %s, want conservative (%v)", profile, why)
	}
	changes := netPlanChanges(profile, rep, netRoleAgent)
	var applied []string
	for _, c := range changes {
		if c.Apply {
			applied = append(applied, c.Key+"="+c.Proposed)
		}
	}
	want := []string{
		"net.core.somaxconn=4096",
		"net.ipv4.tcp_max_syn_backlog=4096",
		"net.ipv4.tcp_mtu_probing=1",
		"vm.swappiness=10",
	}
	if strings.Join(applied, ",") != strings.Join(want, ",") {
		t.Fatalf("Iran-like apply set = %v, want %v", applied, want)
	}
	// nothing else may be offered to a 956 MB box: no buffers, no conntrack
	// growth, no NIC actions, no reserved-ports guesswork
	for _, c := range changes {
		if c.Apply {
			continue
		}
		switch {
		case strings.Contains(c.Key, "rmem"), strings.Contains(c.Key, "wmem"),
			strings.Contains(c.Key, "conntrack"), strings.HasPrefix(c.Key, "ring:"),
			strings.HasPrefix(c.Key, "rps:"):
			t.Fatalf("low-resource server must not be offered %s (%s)", c.Key, c.SkipReason)
		}
	}
}

// TestAcceptNeverTouchesForbiddenDomains pins the ownership boundary: no
// candidate in any profile may carry an SSH/firewall/port-rule key. (The
// ip_local_port_* sysctls are kernel ephemeral-range tuning, NOT service
// port rules, and are legitimate.)
func TestAcceptNeverTouchesForbiddenDomains(t *testing.T) {
	forbidden := []string{
		"ssh", "sshd", "ufw", "iptables", "nftables", "firewall",
		"authorized", "passwd", "permitroot",
	}
	for _, p := range []netProfileName{netProfileConservative, netProfileBalanced, netProfilePerformance} {
		for _, c := range netProfileCandidates(p) {
			lower := strings.ToLower(c.Key)
			for _, f := range forbidden {
				if strings.Contains(lower, f) {
					t.Fatalf("profile %s candidate %s touches a forbidden domain (%s)", p, c.Key, f)
				}
			}
			if c.Kind == "nic" && strings.HasPrefix(c.Key, "mtu:") {
				t.Fatalf("profile %s proposes an MTU change — MTU belongs to the tunnel subsystem", p)
			}
		}
	}
}

// TestAcceptNoRpFilterOnAsymmetricTunnelHosts pins the asymmetric-route
// safety: no profile may propose rp_filter keys, and detection reports them
// read-only for the record.
func TestAcceptNoRpFilterOnAsymmetricTunnelHosts(t *testing.T) {
	for _, p := range []netProfileName{netProfileConservative, netProfileBalanced, netProfilePerformance} {
		for _, c := range netProfileCandidates(p) {
			if strings.Contains(c.Key, "rp_filter") {
				t.Fatalf("profile %s proposes %s — strict reverse-path filtering drops asymmetric tunnel traffic", p, c.Key)
			}
		}
	}
	// A host with a matrix tunnel iface must keep its MTU untouched by the
	// plan: detection reads it, no candidate writes it. (tcp_mtu_probing is
	// a PMTU blackhole-probing knob, not an MTU change — allowed.)
	files, dirs, exec := netIranLikeFixture()
	netTestEnv(t, files, dirs, exec)
	rep := netDetectHardware()
	changes := netPlanChanges(netProfileBalanced, rep, netRoleAgent)
	for _, c := range changes {
		if c.Kind == "nic" && strings.HasPrefix(c.Key, "mtu:") {
			t.Fatalf("plan proposes an MTU NIC action %s", c.Key)
		}
		if c.Kind == "sysctl" && (strings.HasPrefix(c.Key, "net.ipv4.conf.") || strings.HasPrefix(c.Key, "net.ipv6.conf.")) &&
			strings.HasSuffix(c.Key, "mtu") {
			t.Fatalf("plan proposes an interface MTU sysctl %s", c.Key)
		}
		if c.Kind == "sysctl" && strings.Contains(c.Key, "mss") {
			t.Fatalf("plan proposes an MSS rule %s — MSS clamping belongs to the tunnel code", c.Key)
		}
	}
	if rep.Sysctls["net.ipv4.conf.all.rp_filter"] != "" {
		// detection may record it if present on the host; the point is that
		// no profile changes it — covered by the candidate scan above.
		_ = rep.Sysctls
	}
}

// TestAcceptConservativeOnDetectionGaps pins the fail-safe: a host the
// detector cannot fully read must land on Conservative.
func TestAcceptConservativeOnDetectionGaps(t *testing.T) {
	files, dirs, exec := netIfaceFixtures()
	delete(files, "/proc/cpuinfo") // CPU unreadable
	delete(exec, "uname")          // kernel unreadable -> warning
	delete(exec, "systemd-detect-virt")
	// virt falls back to DMI: fixture has no DMI file -> "unknown" -> guard
	netTestEnv(t, files, dirs, exec)
	rep := netDetectHardware()
	profile, why := netSelectProfile(rep, netRoleAuto, "")
	if profile != netProfileConservative {
		t.Fatalf("host with detection gaps selected %s (%v)", profile, why)
	}
	if len(rep.Warnings) == 0 {
		t.Fatal("detection gaps must be recorded as warnings")
	}
}

// netTwoCoreTwoGBFixture: 2 vCPU / 2048 MB RAM — the controller-compatible
// low-resource shape (sits exactly on the ram>=2g and <2048 boundaries).
func netTwoCoreTwoGBFixture() (map[string]string, map[string][]string, map[string]string) {
	files, dirs, exec := netIfaceFixtures()
	files["/proc/meminfo"] = "MemTotal:        2097152 kB\nMemAvailable:    1572864 kB\nSwapTotal:       512000 kB\nSwapFree:        512000 kB\n"
	files["/proc/cpuinfo"] = "processor : 0\nmodel name : Common KVM processor\nprocessor : 1\nmodel name : Common KVM processor\n"
	exec["systemd-detect-virt"] = "kvm\n"
	files["/proc/sys/net/ipv4/tcp_available_congestion_control"] = "reno cubic bbr\n"
	return files, dirs, exec
}

// TestAcceptTwoCoreTwoGBControllerCompatible pins the boundary behavior: a
// 2-core / 2 GB host is NOT Conservative (2048 is not below 2048), selects
// Balanced with the buffers exactly at the ram>=2g requirement, and
// Performance stays out (2 cores < 4). This is the exact plan a
// controller-compatible low-memory host receives.
func TestAcceptTwoCoreTwoGBControllerCompatible(t *testing.T) {
	files, dirs, exec := netTwoCoreTwoGBFixture()
	netTestEnv(t, files, dirs, exec)
	rep := netDetectHardware()
	if rep.RAMTotalMB != 2048 || rep.CPUCores != 2 || rep.MemPressure {
		t.Fatalf("fixture detection = %d MB / %d cores / pressure=%v", rep.RAMTotalMB, rep.CPUCores, rep.MemPressure)
	}
	profile, why := netSelectProfile(rep, netRoleController, "")
	if profile != netProfileBalanced {
		t.Fatalf("2-core/2GB profile = %s (%v)", profile, why)
	}
	changes := netPlanChanges(profile, rep, netRoleController)
	applied := map[string]string{}
	for _, c := range changes {
		if c.Apply {
			applied[c.Key] = c.Proposed
		}
	}
	// the 2 GB gate: buffers ARE proposed (2048 satisfies ram>=2g)
	for _, k := range []string{"net.core.rmem_max", "net.core.wmem_max", "net.ipv4.tcp_rmem", "net.ipv4.tcp_wmem"} {
		if _, ok := applied[k]; !ok {
			t.Fatalf("2GB host: buffer candidate %s must apply", k)
		}
	}
	// conntrack scaled to the small RAM (2048*128 = 262144)
	if applied["net.netfilter.nf_conntrack_max"] != "262144" {
		t.Fatalf("conntrack = %q, want 262144", applied["net.netfilter.nf_conntrack_max"])
	}
	// Performance-only extras must be absent on 2 cores
	for _, k := range []string{"net.ipv4.tcp_fastopen", "net.ipv4.tcp_notsent_lowat"} {
		if _, ok := applied[k]; ok {
			t.Fatalf("2-core host must not receive Performance candidate %s", k)
		}
	}
	if applied["net.core.netdev_max_backlog"] != "16384" {
		t.Fatalf("netdev backlog = %q, want the Balanced 16384", applied["net.core.netdev_max_backlog"])
	}
}
