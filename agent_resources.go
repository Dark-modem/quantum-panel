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
	"runtime"
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

// agentReadLoadAvg returns the 1-minute load average.
func agentReadLoadAvg() (float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	return agentParseLoadavg(data)
}

// agentReadMemInfo returns total and used (total - available) MB.
func agentReadMemInfo() (totalMB, usedMB uint64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0
	}
	return agentParseMemInfo(data)
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

// agentDetectVirt classifies the virtualization environment. Best-effort and
// read-only; empty when undetectable (e.g. non-systemd hosts).
func agentDetectVirt() string {
	out, err := agentHostExec("systemd-detect-virt")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// agentListeningPorts collects the live listening inventory via ss.
func agentListeningPorts() []agentListenPort {
	out, err := agentHostExec("ss", "-tulnp")
	if err != nil {
		return nil // controller shows an empty inventory, never a guess
	}
	return agentParseSS(out)
}
