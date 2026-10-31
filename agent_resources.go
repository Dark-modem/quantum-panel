//go:build linux

package main

// Agent resource + listening-port reporting (gap analysis G3 / milestone M1):
// a lightweight measured snapshot for the controller dashboards — CPU model
// and utilization, memory, root-disk usage, load, uptime, virtualization and
// the listening TCP/UDP inventory with process-ownership hints. Linux /proc
// based with narrow seams for tests; no bandwidth probes, no snapshot
// collectors.

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// agentHostExec runs a host command for resource discovery (ss,
// systemd-detect-virt). Seam for tests; never used for the Matrix allowlist
// operations (those go through matrixExec).
var agentHostExec = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

var agentCpuSampleMu sync.Mutex // serializes the two-sample CPU measurement

// agentCollectResources gathers the full resource snapshot. Every component
// is best-effort: an unreadable source leaves its field zero/empty rather
// than failing the whole heartbeat.
func agentCollectResources() *agentResourceReport {
	r := &agentResourceReport{}
	r.CPUModel, r.CPUCores = agentReadCPUInfo()
	r.CPUPercent = agentSampleCPUPercent()
	r.LoadAvg1, _ = agentReadLoadAvg()
	r.MemTotalMB, r.MemUsedMB = agentReadMemInfo()
	r.DiskTotalGB, r.DiskUsedGB = agentReadDiskUsage("/")
	r.UptimeS = agentReadUptime()
	r.Virt = agentDetectVirt()
	r.Listening = agentListeningPorts()
	return r
}

// agentReadCPUInfo parses /proc/cpuinfo for the model name and core count.
func agentReadCPUInfo() (model string, cores int) {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return runtime.GOARCH, runtime.NumCPU() // graceful non-Linux fallback
	}
	return agentParseCPUInfo(data)
}

func agentParseCPUInfo(data []byte) (model string, cores int) {
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		switch key {
		case "model name":
			if model == "" {
				model = val
			}
		case "processor":
			cores++
		}
	}
	if cores == 0 {
		cores = runtime.NumCPU()
	}
	return model, cores
}

// agentSampleCPUPercent takes two /proc/stat samples ~200ms apart and
// returns the busy percentage of the interval.
func agentSampleCPUPercent() float64 {
	agentCpuSampleMu.Lock()
	defer agentCpuSampleMu.Unlock()
	busy1, total1, ok := agentCpuTimes()
	if !ok {
		return 0
	}
	time.Sleep(200 * time.Millisecond)
	busy2, total2, ok := agentCpuTimes()
	if !ok {
		return 0
	}
	dTotal := total2 - total1
	if dTotal <= 0 {
		return 0
	}
	dBusy := busy2 - busy1
	if dBusy < 0 {
		dBusy = 0
	}
	return 100 * float64(dBusy) / float64(dTotal)
}

// agentCpuTimes sums /proc/stat's aggregate line: busy = total - idle/iowait.
func agentCpuTimes() (busy, total float64, ok bool) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, false
	}
	return agentParseCpuStat(data)
}

func agentParseCpuStat(data []byte) (busy, total float64, ok bool) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)[1:]
		var sum float64
		var idle float64
		for i, f := range fields {
			v, err := strconv.ParseFloat(f, 64)
			if err != nil {
				continue
			}
			sum += v
			if i == 3 || i == 4 { // idle, iowait
				idle += v
			}
		}
		return sum - idle, sum, true
	}
	return 0, 0, false
}

// agentReadLoadAvg returns the 1-minute load average.
func agentReadLoadAvg() (float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	return agentParseLoadavg(data)
}

func agentParseLoadavg(data []byte) (float64, error) {
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseFloat(f[0], 64)
}

// agentReadMemInfo returns total and used (total - available) MB.
func agentReadMemInfo() (totalMB, usedMB uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	return agentParseMemInfo(data)
}

func agentParseMemInfo(data []byte) (totalMB, usedMB uint64) {
	var total, available uint64
	for _, line := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fields := strings.Fields(val)
		if len(fields) == 0 {
			continue
		}
		kb, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSpace(key) {
		case "MemTotal":
			total = kb
		case "MemAvailable":
			available = kb
		}
	}
	if total == 0 {
		return 0, 0
	}
	if available > total {
		available = total
	}
	return total / 1024, (total - available) / 1024
}

// agentReadDiskUsage returns root-filesystem totals in GB via statfs.
func agentReadDiskUsage(path string) (totalGB, usedGB uint64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	free := st.Bavail * bs
	if free > total {
		free = total
	}
	return total / (1 << 30), (total - free) / (1 << 30)
}

// agentReadUptime returns the host uptime in seconds.
func agentReadUptime() uint64 {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0
	}
	return agentParseUptime(data)
}

func agentParseUptime(data []byte) uint64 {
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(f[0], 64)
	if err != nil || v < 0 {
		return 0
	}
	return uint64(v)
}

// agentDetectVirt classifies the virtualization environment. Best-effort and
// read-only; empty when undetectable (e.g. non-systemd hosts).
func agentDetectVirt() string {
	out, err := agentHostExec("systemd-detect-virt")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// agentParseSS parses `ss -tulnp` output into the listening inventory.
// Only LISTEN (tcp) and UNCONN (udp) states count; wildcard and concrete
// bind addresses are kept verbatim; process names are extracted from the
// users:(("name",pid=…)) clauses (duplicates removed).
func agentParseSS(out string) []agentListenPort {
	ports := []agentListenPort{}
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		proto := f[0]
		if proto != "tcp" && proto != "udp" {
			continue
		}
		state := f[1]
		if proto == "tcp" && state != "LISTEN" {
			continue
		}
		if proto == "udp" && state != "UNCONN" {
			continue
		}
		local := f[4]
		host, portStr, err := splitHostPort(local)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 0 || port > 65535 {
			continue
		}
		proc := agentSSProcesses(line)
		key := proto + "|" + local + "|" + proc
		if seen[key] {
			continue
		}
		seen[key] = true
		ports = append(ports, agentListenPort{Proto: proto, Port: port, Addr: host, Proc: proc})
	}
	return ports
}

// splitHostPort handles IPv6 forms ss prints ([::]:22, 127.0.0.53%lo:53).
func splitHostPort(local string) (string, string, error) {
	if i := strings.LastIndex(local, ":"); i >= 0 {
		return strings.Trim(local[:i], "[]"), local[i+1:], nil
	}
	return "", "", strconv.ErrSyntax
}

// agentSSProcsRe matches the quoted process names inside ss's
// users:(("sshd",pid=…),("systemd",pid=…)) clause.
var agentSSProcsRe = regexp.MustCompile(`"([a-zA-Z0-9_.\-]+)"`)

// agentSSProcesses extracts the process names from a users:((...)) clause.
func agentSSProcesses(line string) string {
	i := strings.Index(line, `users:((`)
	if i < 0 {
		return ""
	}
	var names []string
	seenName := map[string]bool{}
	for _, m := range agentSSProcsRe.FindAllStringSubmatch(line[i:], -1) {
		name := m[1]
		if !seenName[name] {
			seenName[name] = true
			names = append(names, name)
		}
	}
	return strings.Join(names, ",")
}

// agentListeningPorts collects the live listening inventory via ss.
func agentListeningPorts() []agentListenPort {
	out, err := agentHostExec("ss", "-tulnp")
	if err != nil {
		return nil // controller shows an empty inventory, never a guess
	}
	return agentParseSS(out)
}
