package main

// Tests for the Network Optimization subsystem (netdetect.go, netprofile.go,
// netapply.go). Fully hermetic: every file read, directory read, stat and
// command goes through the net* seams, so the tests inject synthetic /proc,
// /sys and ethtool output. Nothing touches the host network or sysctls.

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// netTestEnv replaces every seam with in-memory fixtures and restores them on
// cleanup. files maps absolute paths to contents (the engine's sysctl -w stub
// mutates this map so read-back verification sees the write); dirs maps
// directory paths to entry names; exec maps "name args..." to output.
func netTestEnv(t *testing.T, files map[string]string, dirs map[string][]string, exec map[string]string) {
	t.Helper()
	netReadFile = func(path string) ([]byte, error) {
		if b, ok := files[path]; ok {
			return []byte(b), nil
		}
		return nil, os.ErrNotExist
	}
	netReadDir = func(path string) ([]os.DirEntry, error) {
		names, ok := dirs[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		out := make([]os.DirEntry, 0, len(names))
		for _, n := range names {
			out = append(out, fakeDirEntry{name: n})
		}
		return out, nil
	}
	netStat = func(path string) (os.FileInfo, error) {
		for dir, names := range dirs {
			for _, n := range names {
				if filepath.Join(dir, n) == path {
					return fakeFileInfo{}, nil
				}
			}
		}
		if _, ok := files[path]; ok {
			return fakeFileInfo{}, nil
		}
		return nil, os.ErrNotExist
	}
	netLstat = func(path string) (string, error) {
		if v, ok := files[path]; ok {
			return v, nil // synthetic driver symlink target
		}
		return "", os.ErrNotExist
	}
	netDetectExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		key := name
		if len(args) > 0 {
			key = name + " " + strings.Join(args, " ")
		}
		if out, ok := exec[key]; ok {
			return []byte(out), nil
		}
		if out, ok := exec[name]; ok {
			return []byte(out), nil
		}
		return nil, errors.New("net test exec: no canned output for " + key)
	}
	t.Cleanup(func() {
		netReadFile = os.ReadFile
		netReadDir = os.ReadDir
		netStat = os.Stat
		netLstat = os.Readlink
		netDetectExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return runner(ctx, name, args...)
		}
	})
}

type fakeDirEntry struct{ name string }

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return true }
func (f fakeDirEntry) Type() os.FileMode          { return os.ModeDir }
func (f fakeDirEntry) Info() (os.FileInfo, error) { return fakeFileInfo{}, nil }

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return 0 }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

// netIfaceFixtures is the standard fixture: kvm VM, 4 cores, 8 GB RAM,
// 10 Gbit/s physical primary eth0 with 2 RX queues and ring headroom, plus a
// Matrix tunnel qmx0. Congestion control is cubic; bbr is NOT available.
func netIfaceFixtures() (map[string]string, map[string][]string, map[string]string) {
	files := map[string]string{
		"/proc/meminfo": "MemTotal:        8388608 kB\nMemAvailable:    6291456 kB\nSwapTotal:       2097152 kB\nSwapFree:        2097152 kB\n",
		"/proc/net/dev": "Inter-|   Receive                                                |  Transmit\n face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
			"  eth0: 1000000 2000 0 0 0 0 0 0  2000000 1500 0 0 0 0 0 0\n" +
			"  qmx0: 500000 900 0 0 0 0 0 0  400000 800 0 0 0 0 0 0\n" +
			"    lo: 100 10 0 0 0 0 0 0  100 10 0 0 0 0 0 0\n",
		"/proc/cpuinfo":                                       "processor : 0\nmodel name : Test CPU Model\nprocessor : 1\nprocessor : 2\nprocessor : 3\n",
		"/proc/sys/net/core/rmem_max":                         "212992\n",
		"/proc/sys/net/core/wmem_max":                         "212992\n",
		"/proc/sys/net/core/rmem_default":                     "212992\n",
		"/proc/sys/net/core/wmem_default":                     "212992\n",
		"/proc/sys/net/core/netdev_max_backlog":               "1000\n",
		"/proc/sys/net/core/somaxconn":                        "4096\n",
		"/proc/sys/net/core/default_qdisc":                    "pfifo_fast\n",
		"/proc/sys/net/ipv4/tcp_congestion_control":           "cubic\n",
		"/proc/sys/net/ipv4/tcp_available_congestion_control": "cubic reno\n",
		"/proc/sys/net/ipv4/tcp_rmem":                         "4096 131072 6291456\n",
		"/proc/sys/net/ipv4/tcp_wmem":                         "4096 16384 4194304\n",
		"/proc/sys/net/ipv4/tcp_mtu_probing":                  "0\n",
		"/proc/sys/net/ipv4/tcp_fastopen":                     "0\n",
		"/proc/sys/net/ipv4/tcp_keepalive_time":               "7200\n",
		"/proc/sys/net/ipv4/tcp_keepalive_intvl":              "75\n",
		"/proc/sys/net/ipv4/tcp_keepalive_probes":             "9\n",
		"/proc/sys/net/ipv4/tcp_max_syn_backlog":              "1024\n",
		"/proc/sys/net/ipv4/ip_local_port_range":              "32768 60999\n",
		"/proc/sys/net/ipv4/ip_local_reserved_ports":          "\n",
		"/proc/sys/net/ipv4/tcp_slow_start_after_idle":        "1\n",
		"/proc/sys/net/netfilter/nf_conntrack_max":            "65536\n",
		"/proc/sys/vm/swappiness":                             "60\n",
		"/sys/class/net/eth0/mtu":                             "1500\n",
		"/sys/class/net/eth0/operstate":                       "up\n",
		"/sys/class/net/eth0/device/driver":                   "/sys/bus/pci/drivers/ixgbe",
		"/sys/class/net/eth0/queues/rx-0/rps_flow_cnt":        "0\n",
		"/sys/class/net/eth0/queues/rx-1/rps_flow_cnt":        "0\n",
		"/sys/class/net/eth0/queues/tx-0/xps_cpus":            "ff\n",
		"/sys/class/net/qmx0/mtu":                             "1380\n",
		"/sys/class/net/qmx0/operstate":                       "unknown\n",
		"/sys/class/net/qmx0/queues/rx-0/rps_flow_cnt":        "0\n",
		"/sys/class/net/lo/mtu":                               "65536\n",
	}
	dirs := map[string][]string{
		"/sys/class/net/eth0/queues": {"rx-0", "rx-1", "tx-0", "tx-1"},
		"/sys/class/net/qmx0/queues": {"rx-0", "tx-0"},
	}
	exec := map[string]string{
		"uname":                 "6.8.0-49-generic",
		"systemd-detect-virt":   "kvm\n",
		"ip route show default": "default via 192.0.2.1 dev eth0 onlink\n",
		"ethtool eth0":          "Settings for eth0:\n\tSpeed: 10000Mb/s\n\tDuplex: Full\n",
		"ethtool qmx0":          "Settings for qmx0:\n\tSpeed: Unknown!\n",
		"ethtool -g eth0":       "Pre-set maximums:\nRX: 8192\nTX: 8192\nCurrent hardware settings:\nRX: 512\nTX: 512\n",
	}
	return files, dirs, exec
}

func TestNetDetectHardwareMeasuresWithoutGuessing(t *testing.T) {
	files, dirs, exec := netIfaceFixtures()
	netTestEnv(t, files, dirs, exec)

	rep := netDetectHardware()
	if rep.Kernel != "6.8.0-49-generic" {
		t.Fatalf("kernel = %q", rep.Kernel)
	}
	if rep.VirtType != "kvm" {
		t.Fatalf("virt = %q", rep.VirtType)
	}
	if rep.CPUModel != "Test CPU Model" || rep.CPUCores != 4 {
		t.Fatalf("cpu = %q/%d", rep.CPUModel, rep.CPUCores)
	}
	if rep.RAMTotalMB != 8192 {
		t.Fatalf("ram total = %d MB, want 8192", rep.RAMTotalMB)
	}
	if rep.MemPressure {
		t.Fatal("75% available must not be memory pressure")
	}
	if rep.PrimaryIface != "eth0" {
		t.Fatalf("primary = %q", rep.PrimaryIface)
	}
	if rep.CC != "cubic" {
		t.Fatalf("cc = %q", rep.CC)
	}
	if got := strings.Join(rep.CCAvailable, ","); got != "cubic,reno" {
		t.Fatalf("cc available = %q", got)
	}
	byName := map[string]netIfaceDetect{}
	for _, f := range rep.Ifaces {
		byName[f.Name] = f
	}
	eth0 := byName["eth0"]
	if eth0.Kind != "physical" || eth0.Driver != "ixgbe" || eth0.SpeedMBs != 10000 ||
		eth0.MTU != 1500 || !eth0.Primary || eth0.QueuesRX != 2 || !eth0.RPSCapable {
		t.Fatalf("eth0 = %+v", eth0)
	}
	if eth0.RingCur != 512 || eth0.RingMax != 8192 {
		t.Fatalf("eth0 rings = %d/%d, want 512/8192", eth0.RingCur, eth0.RingMax)
	}
	qmx := byName["qmx0"]
	if qmx.Kind != "tunnel" || qmx.MTU != 1380 || qmx.Primary {
		t.Fatalf("qmx0 = %+v", qmx)
	}
	c := rep.Counters["eth0"]
	if c.RxBytes != 1000000 || c.TxErrors != 0 || c.SampledAt == 0 {
		t.Fatalf("eth0 counters = %+v", c)
	}
	if rep.Sysctls["vm.swappiness"] != "60" {
		t.Fatalf("sysctl snapshot missing swappiness: %v", rep.Sysctls)
	}
}

func TestNetDetectFlagsMemoryPressure(t *testing.T) {
	files, dirs, exec := netIfaceFixtures()
	files["/proc/meminfo"] = "MemTotal:        2048000 kB\nMemAvailable:     102400 kB\nSwapTotal:       0 kB\nSwapFree:        0 kB\n"
	netTestEnv(t, files, dirs, exec)

	rep := netDetectHardware()
	if !rep.MemPressure {
		t.Fatalf("5%% available must be memory pressure (got %.1f%%)", rep.MemAvailPct)
	}
	profile, why := netSelectProfile(rep, netRoleAuto, "")
	if profile != netProfileConservative {
		t.Fatalf("profile under memory pressure = %s (%v)", profile, why)
	}
}

func TestNetSelectProfileIsHardwareAware(t *testing.T) {
	files, dirs, exec := netIfaceFixtures()
	netTestEnv(t, files, dirs, exec)
	rep := netDetectHardware()

	// kvm VM: the Performance hardware case is not met -> Balanced.
	p, why := netSelectProfile(rep, netRoleAuto, "")
	if p != netProfileBalanced {
		t.Fatalf("kvm VM profile = %s, want balanced (%v)", p, why)
	}
	if len(why) == 0 {
		t.Fatal("selection must record reasons")
	}

	// Bare metal, 16 cores, bbr available: Performance is justified.
	files["/proc/cpuinfo"] = "model name : Test CPU Model\n" + strings.Repeat("processor : x\n", 16)
	exec["systemd-detect-virt"] = "none"
	files["/proc/sys/net/ipv4/tcp_available_congestion_control"] = "cubic reno bbr\n"
	netTestEnv(t, files, dirs, exec)
	rep2 := netDetectHardware()
	p2, why2 := netSelectProfile(rep2, netRoleAuto, "")
	if p2 != netProfilePerformance {
		t.Fatalf("bare metal 16-core profile = %s (%v)", p2, why2)
	}

	// A forced profile always wins and is recorded as the reason.
	if p3, _ := netSelectProfile(rep2, netRoleAuto, "conservative"); p3 != netProfileConservative {
		t.Fatalf("forced profile ignored: %s", p3)
	}
}

func TestNetPlanChangesFiltersRoleAndNoOps(t *testing.T) {
	files, dirs, exec := netIfaceFixtures()
	// somaxconn already at the balanced value: must be a no-op.
	files["/proc/sys/net/core/somaxconn"] = "8192\n"
	// bbr unavailable: the BBR candidates must be skipped with a reason.
	files["/proc/sys/net/ipv4/tcp_available_congestion_control"] = "cubic\n"
	netTestEnv(t, files, dirs, exec)
	rep := netDetectHardware()

	changes := netPlanChanges(netProfileBalanced, rep, netRoleController)
	seenNoOp, seenSkip := false, false
	for _, c := range changes {
		if c.Key == "net.core.somaxconn" {
			if c.Apply {
				t.Fatal("somaxconn already at proposed value must not apply")
			}
			seenNoOp = true
		}
		if c.Key == "net.ipv4.tcp_congestion_control" {
			if c.Apply {
				t.Fatal("bbr candidate must not apply when unavailable")
			}
			if !strings.Contains(c.SkipReason, "bbr") {
				t.Fatalf("bbr skip reason = %q", c.SkipReason)
			}
			seenSkip = true
		}
	}
	if !seenNoOp || !seenSkip {
		t.Fatalf("plan missing somaxconn no-op or bbr skip: %+v", changes)
	}
}

// TestNetReservedPortsValueScansConfigs checks the reserved-ports resolver
// reads panel.json / frps / backhaul / agent tunnels and skips junk ports.
func TestNetReservedPortsValueScansConfigs(t *testing.T) {
	withTempConfig(t)
	old := netReadFile
	t.Cleanup(func() { netReadFile = old })
	netReadFile = os.ReadFile // real fs under the temp configDir
	if err := os.WriteFile(filepath.Join(configDir, "panel.json"), []byte(`{"port": 7777, "tls_port": 7443}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "frps-main.toml"), []byte("bindPort = 7000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(configDir, "backhaul"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "backhaul", "srv.toml"), []byte(`bind_addr = "0.0.0.0:65000"`), 0600); err != nil {
		t.Fatal(err)
	}
	got := netReservedPortsValue()
	want := "65000,7000,7443,7777"
	if got != want {
		t.Fatalf("reserved ports = %q, want %q", got, want)
	}
}

// TestNetApplyAppliesVerifiesPersists runs a full Performance apply against
// stubs: sysctl writes land in the fixture map (so read-back verification is
// real), NIC ring/RPS actions run, the owned files are written and verified,
// and the journal records everything.
func TestNetApplyAppliesVerifiesPersists(t *testing.T) {
	withTempConfig(t)
	files, dirs, exec := netIfaceFixtures()
	// Performance requires bare metal + bbr + cores + RAM + physical NIC.
	files["/proc/cpuinfo"] = "model name : Test CPU Model\n" + strings.Repeat("processor : x\n", 16)
	exec["systemd-detect-virt"] = "none"
	files["/proc/sys/net/ipv4/tcp_available_congestion_control"] = "cubic reno bbr\n"
	// bbr becomes "current" after apply, as the -w stub will record.
	files["/proc/sys/net/ipv4/tcp_notsent_lowat"] = "-1\n"

	// Owned files live in the temp configDir for the test.
	oldSys, oldUnit := netOwnedSysctlFile, netOwnedUnitFile
	netOwnedSysctlFile = filepath.Join(configDir, "sysctl.d", "99-quantum-netopt.conf")
	netOwnedUnitFile = filepath.Join(configDir, "quantum-netopt-nic.service")
	t.Cleanup(func() { netOwnedSysctlFile, netOwnedUnitFile = oldSys, oldUnit })

	exec["systemctl daemon-reload"] = ""
	exec["systemctl enable quantum-netopt-nic.service"] = ""
	netTestEnv(t, files, dirs, exec)
	ringCur := "512"
	realExec := netDetectExec
	netDetectExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "sysctl" && len(args) >= 2 && args[0] == "-w":
			kv := strings.SplitN(args[1], "=", 2)
			if len(kv) == 2 {
				key := strings.TrimSpace(kv[0])
				val := strings.Trim(kv[1], `"`)
				files[netSysctlPath(key)] = val + "\n"
				return []byte(""), nil
			}
		case name == "ethtool" && len(args) >= 4 && args[0] == "-G":
			ringCur = args[3]
			return []byte(""), nil
		case name == "ethtool" && len(args) == 2 && args[0] == "-g":
			return []byte("Pre-set maximums:\nRX: 8192\nTX: 8192\nCurrent hardware settings:\nRX: " + ringCur + "\nTX: " + ringCur + "\n"), nil
		case name == "sysctl" && len(args) >= 2 && args[0] == "-p":
			// apply the drop-in's lines into the fixture like a real kernel
			b, rerr := os.ReadFile(args[1])
			if rerr != nil {
				return nil, rerr
			}
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				kv := strings.SplitN(line, "=", 2)
				if len(kv) == 2 {
					files[netSysctlPath(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1]) + "\n"
				}
			}
			return []byte(""), nil
		}
		return realExec(ctx, name, args...)
	}
	// RPS flow-count writes go through bash -c echo: make them real files.
	netApplyRpsFixture(t, files)
	// Connectivity probe always succeeds.
	netProbeDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		return nil, nil
	}
	t.Cleanup(func() {
		netProbeDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
			return dialFn(network, addr, timeout)
		}
	})

	j, err := netApply(netProfilePerformance, netRoleAuto, []string{"test justification"})
	if err != nil {
		t.Fatalf("apply: %v (journal %+v)", err, j)
	}
	if !j.Verified || j.RolledBack {
		t.Fatalf("apply not verified: %+v", j)
	}
	if j.Profile != "performance" || j.Role != "controller" {
		t.Fatalf("journal profile/role = %s/%s", j.Profile, j.Role)
	}
	// sysctl applied + verified in fixture
	if got := strings.TrimSpace(files[netSysctlPath("net.ipv4.tcp_congestion_control")]); got != "bbr" {
		t.Fatalf("cc after apply = %q", got)
	}
	// owned drop-in carries the managed marker and the keys
	b, err := os.ReadFile(netOwnedSysctlFile)
	if err != nil || !strings.HasPrefix(string(b), netOwnedMarker) {
		t.Fatalf("owned sysctl file missing marker: %v %q", err, string(b))
	}
	if !strings.Contains(string(b), "net.ipv4.tcp_congestion_control = bbr") {
		t.Fatalf("owned sysctl file missing bbr: %q", string(b))
	}
	// ring applied to the max and verified
	if ringCur != "8192" {
		t.Fatalf("ring not raised: %s", ringCur)
	}
	// journal + baseline persisted
	if netLoadJournal() == nil || netLoadBaseline() == nil {
		t.Fatal("journal/baseline not persisted")
	}
	// rollback restores the pre-apply values
	msg, err := netRollback()
	if err != nil {
		t.Fatalf("rollback: %v (%s)", err, msg)
	}
	if got := strings.TrimSpace(files[netSysctlPath("net.ipv4.tcp_congestion_control")]); got != "cubic" {
		t.Fatalf("cc after rollback = %q, want cubic", got)
	}
	if ringCur != "512" {
		t.Fatalf("ring not restored: %s", ringCur)
	}
}

// TestNetApplyRollsBackWhenConnectivityDies checks the health guard: the
// apply succeeds, the probe fails, everything is restored and the journal
// says rolled_back.
func TestNetApplyRollsBackWhenConnectivityDies(t *testing.T) {
	withTempConfig(t)
	files, dirs, exec := netIfaceFixtures()
	oldSys, oldUnit := netOwnedSysctlFile, netOwnedUnitFile
	netOwnedSysctlFile = filepath.Join(configDir, "sysctl.d", "99-quantum-netopt.conf")
	netOwnedUnitFile = filepath.Join(configDir, "quantum-netopt-nic.service")
	t.Cleanup(func() { netOwnedSysctlFile, netOwnedUnitFile = oldSys, oldUnit })

	netTestEnv(t, files, dirs, exec)
	netDetectExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "sysctl" && len(args) >= 2 && args[0] == "-w" {
			kv := strings.SplitN(args[1], "=", 2)
			if len(kv) == 2 {
				files[netSysctlPath(strings.TrimSpace(kv[0]))] = strings.Trim(kv[1], `"`) + "\n"
				return []byte(""), nil
			}
		}
		if name == "sysctl" && len(args) >= 2 && args[0] == "-p" {
			b, err := os.ReadFile(args[1])
			if err != nil {
				return nil, err
			}
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				kv := strings.SplitN(line, "=", 2)
				if len(kv) == 2 {
					files[netSysctlPath(strings.TrimSpace(kv[0]))] = strings.TrimSpace(kv[1]) + "\n"
				}
			}
			return []byte(""), nil
		}
		return nil, errors.New("no canned output for " + name + " " + strings.Join(args, " "))
	}
	netProbeDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
		return nil, errors.New("network is unreachable") // health guard trips
	}
	t.Cleanup(func() {
		netProbeDial = func(network, addr string, timeout time.Duration) (net.Conn, error) {
			return dialFn(network, addr, timeout)
		}
	})

	j, err := netApply(netProfileBalanced, netRoleAuto, nil)
	if err == nil {
		t.Fatal("apply must fail when the connectivity probe fails")
	}
	if j == nil || !j.RolledBack || j.Verified {
		t.Fatalf("journal must record a failed rollback-guarded apply: %+v", j)
	}
	// every applied sysctl must be restored to its captured before value
	for _, s := range j.Applied {
		if s.Kind != "sysctl" {
			continue
		}
		live := strings.TrimSpace(files[netSysctlPath(s.Key)])
		want := s.Before
		if want == "" {
			continue // unreadable before: restore is a no-op, never a guess
		}
		if live != want {
			t.Fatalf("%s live=%q want restored %q", s.Key, live, want)
		}
	}
}

// netApplyRpsFixture makes the RPS bash-echo writes real files under a fake
// sysfs root so netVerifyNic's read-back sees them.
func netApplyRpsFixture(t *testing.T, files map[string]string) {
	t.Helper()
	realExec := netDetectExec
	netDetectExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "bash" && len(args) == 2 && args[0] == "-c" {
			// echo MASK > path && echo 32768 > path2
			parts := strings.Split(args[1], " && ")
			for _, p := range parts {
				val, path, err := parseEchoRedirect(p)
				if err != nil {
					continue
				}
				files[path] = val + "\n"
			}
			return []byte(""), nil
		}
		return realExec(ctx, name, args...)
	}
}

// parseEchoRedirect parses "echo VALUE > /sys/.../file".
func parseEchoRedirect(s string) (val, path string, err error) {
	i := strings.Index(s, ">")
	if i < 0 {
		return "", "", errors.New("no redirect")
	}
	val = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(s[:i]), "echo"))
	path = strings.TrimSpace(s[i+1:])
	if val == "" || path == "" {
		return "", "", errors.New("empty redirect")
	}
	return val, path, nil
}
