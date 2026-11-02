//go:build linux

package main

import (
	"strconv"
	"strings"
	"testing"
)

// M1: resource + listening-port reporting — parser and inventory tests.
// Collectors are seam-stubbable; these tests pin the pure parsers.

func TestAgentParseMemInfo(t *testing.T) {
	fixture := `MemTotal:        1010568 kB
MemFree:           74904 kB
MemAvailable:     663528 kB
Buffers:           91236 kB
Cached:           459044 kB
SwapTotal:             0 kB`
	total, used := agentParseMemInfo([]byte(fixture))
	if total != 986 { // 1010568/1024
		t.Fatalf("total = %d, want 986", total)
	}
	if used != 338 { // (1010568-663528)/1024 = 338.9 -> 338 (int division)
		t.Fatalf("used = %d, want 338", used)
	}
	if t2, u2 := agentParseMemInfo([]byte("MemTotal: 0 kB")); t2 != 0 || u2 != 0 {
		t.Fatalf("empty meminfo must stay zero: %d/%d", t2, u2)
	}
}

func TestAgentParseCpuStat(t *testing.T) {
	// user nice system idle iowait irq softirq steal ...
	fixture := "cpu  100 0 50 850 0 0 0 0 0 0\ncpu0 100 0 50 850 0 0 0 0 0 0\nintr 1"
	busy, total, ok := agentParseCpuStat([]byte(fixture))
	if !ok {
		t.Fatal("aggregate cpu line must be found")
	}
	if total != 1000 || busy != 150 { // idle(850) excluded from busy
		t.Fatalf("busy/total = %v/%v, want 150/1000", busy, total)
	}
	if _, _, ok := agentParseCpuStat([]byte("intr 1\n")); ok {
		t.Fatal("no cpu line must be not-ok")
	}
}

func TestAgentParseLoadavgAndUptime(t *testing.T) {
	la, err := agentParseLoadavg([]byte("0.52 0.58 0.59 1/476 12345\n"))
	if err != nil || la != 0.52 {
		t.Fatalf("loadavg = %v (%v)", la, err)
	}
	if up := agentParseUptime([]byte("86400.25 123456.78\n")); up != 86400 {
		t.Fatalf("uptime = %d", up)
	}
	if up := agentParseUptime([]byte("garbage")); up != 0 {
		t.Fatalf("garbage uptime = %d", up)
	}
}

func TestAgentParseCPUInfo(t *testing.T) {
	fixture := `processor	: 0
model name	: Intel Xeon E3-12xx v2 (Ivy Bridge, IBRS)
processor	: 1
model name	: Intel Xeon E3-12xx v2 (Ivy Bridge, IBRS)`
	model, cores := agentParseCPUInfo([]byte(fixture))
	if cores != 2 || !strings.Contains(model, "Xeon") {
		t.Fatalf("model/cores = %q/%d", model, cores)
	}
}

func TestAgentParseSS(t *testing.T) {
	fixture := `Netid State  Recv-Q Send-Q  Local Address:Port  Peer Address:Port Process
tcp   LISTEN 0      16384         0.0.0.0:22         0.0.0.0:*    users:(("sshd",pid=480606,fd=3))
tcp   LISTEN 0      65535         *:7777              *:*         users:(("gre-panel",pid=986943,fd=8))
tcp   LISTEN 0      4096      127.0.0.1:443           0.0.0.0:*    users:(("xray",pid=991,fd=6))
udp   UNCONN 0      0             0.0.0.0:2020        0.0.0.0:*    users:(("tincd",pid=77,fd=5))
tcp   ESTAB  0      0       185.1.1.1:22          185.2.2.2:51000  users:(("sshd",pid=480606,fd=4))
udp   UNCONN 0      0           [::1]:323            [::]:*
tcp   LISTEN 0      5         10.99.0.1:45000        0.0.0.0:*`
	ports := agentParseSS(fixture)
	if len(ports) != 6 {
		t.Fatalf("expected 6 listening sockets, got %d: %+v", len(ports), ports)
	}
	byKey := map[string]agentListenPort{}
	for _, p := range ports {
		byKey[p.Proto+"|"+p.Addr+"|"+strconv.Itoa(p.Port)] = p
	}
	ssh := byKey["tcp|0.0.0.0|22"]
	if ssh.Proc != "sshd" {
		t.Fatalf("sshd ownership hint wrong: %q", ssh.Proc)
	}
	panel := byKey["tcp|*|7777"]
	if panel.Proc != "gre-panel" {
		t.Fatalf("panel ownership hint wrong: %q", panel.Proc)
	}
	if x := byKey["tcp|127.0.0.1|443"]; x.Proc != "xray" {
		t.Fatalf("xray ownership hint wrong: %q", x.Proc)
	}
	if u := byKey["udp|0.0.0.0|2020"]; u.Proc != "tincd" {
		t.Fatalf("tinc ownership hint wrong: %q", u.Proc)
	}
	if _, ok := byKey["tcp|185.1.1.1|22"]; ok {
		t.Fatal("ESTAB connections must not be reported as listening")
	}
}

func TestAgentSSProcessesMultiNames(t *testing.T) {
	got := agentSSProcesses(`tcp LISTEN 0 80 *:443 *:* users:(("nginx",pid=1,fd=6),("nginx",pid=2,fd=6)),(("xray",pid=9,fd=7))`)
	if got != "nginx,xray" {
		t.Fatalf("multi-process hint = %q, want nginx,xray", got)
	}
	if got := agentSSProcesses(`tcp LISTEN 0 80 *:443 *:*`); got != "" {
		t.Fatalf("no users clause must be empty, got %q", got)
	}
}

func TestAgentParseSSIPv6AndRange(t *testing.T) {
	fixture := "tcp   LISTEN 0 128 [::]:22 [::]:* users:((\"sshd\",pid=1,fd=4))\nudp   UNCONN 0 0 0.0.0.0:0 0.0.0.0:*"
	ports := agentParseSS(fixture)
	if len(ports) != 1 || ports[0].Addr != "::" || ports[0].Port != 22 {
		t.Fatalf("ipv6 parse wrong: %+v", ports)
	}
}
