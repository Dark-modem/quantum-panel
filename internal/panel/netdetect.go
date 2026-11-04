package panel

// netdetect.go — hardware/network detection for the Network Optimization
// subsystem (`gre-panel netopt`). Everything is measured from the live
// system; nothing is assumed. A value the system will not report is recorded
// as a warning and a zero value, never as a guessed default — the profile
// selector refuses to be aggressive over unreadable facts.
//
// Scope: CPU/RAM/kernel/virtualization reuse agent_resources.go where it
// already reads the same files. This file adds the tuning-relevant layer:
// NIC identity/driver/speed/rings/queues/offloads, MTU, queue discipline,
// congestion-control availability, RPS/XPS capability, current sysctls and
// cumulative traffic/error counters. SSH, firewall and ports are NOT part of
// this subsystem (Sessions 1/6 own them) and are never probed for tuning.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Seams so tests can inject synthetic /proc and /sys content and canned
// command output. Production defaults delegate to the shared runner.
var (
	netReadFile   = os.ReadFile
	netReadDir    = os.ReadDir
	netStat       = os.Stat
	netLstat      = os.Readlink
	netDetectExec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return runner(ctx, name, args...)
	}
)

// netIfaceDetect is one interface as measured right now.
type netIfaceDetect struct {
	Name       string            `json:"name"`
	Kind       string            `json:"kind"` // physical|virtio|tunnel|loopback|bridge|unknown
	Primary    bool              `json:"primary,omitempty"`
	Driver     string            `json:"driver,omitempty"`
	SpeedMBs   int               `json:"speed_mbs,omitempty"` // 0 = unknown/not applicable
	Duplex     string            `json:"duplex,omitempty"`
	MTU        int               `json:"mtu"`
	QueuesRX   int               `json:"queues_rx"`
	QueuesTX   int               `json:"queues_tx"`
	RPSCapable bool              `json:"rps_capable,omitempty"`
	XPSCapable bool              `json:"xps_capable,omitempty"`
	RingCur    int               `json:"ring_current,omitempty"` // 0 = unsupported
	RingMax    int               `json:"ring_max,omitempty"`
	Offloads   map[string]string `json:"offloads,omitempty"` // selected ethtool -k features
	LinkUp     bool              `json:"link_up"`
}

// netIfaceCounters is one interface's cumulative counters from /proc/net/dev.
type netIfaceCounters struct {
	RxBytes   uint64 `json:"rx_bytes"`
	RxPackets uint64 `json:"rx_packets"`
	RxErrors  uint64 `json:"rx_errors"`
	RxDropped uint64 `json:"rx_dropped"`
	TxBytes   uint64 `json:"tx_bytes"`
	TxPackets uint64 `json:"tx_packets"`
	TxErrors  uint64 `json:"tx_errors"`
	TxDropped uint64 `json:"tx_dropped"`
	SampledAt int64  `json:"sampled_at"`
}

// netHardwareReport is the full detection snapshot a profile is chosen from.
type netHardwareReport struct {
	GeneratedAt  int64                       `json:"generated_at"`
	Kernel       string                      `json:"kernel"`
	VirtType     string                      `json:"virt_type"` // "none" = bare metal
	CPUModel     string                      `json:"cpu_model"`
	CPUCores     int                         `json:"cpu_cores"`
	RAMTotalMB   uint64                      `json:"ram_total_mb"`
	RAMUsedMB    uint64                      `json:"ram_used_mb"`
	MemAvailPct  float64                     `json:"mem_available_pct"`
	MemPressure  bool                        `json:"mem_pressure"`
	SwapTotalMB  uint64                      `json:"swap_total_mb"`
	SwapUsedMB   uint64                      `json:"swap_used_mb"`
	PrimaryIface string                      `json:"primary_iface,omitempty"`
	Ifaces       []netIfaceDetect            `json:"ifaces"`
	Sysctls      map[string]string           `json:"sysctls"`
	CC           string                      `json:"cc"` // current congestion control
	CCAvailable  []string                    `json:"cc_available"`
	DefaultQdisc string                      `json:"default_qdisc"`
	Counters     map[string]netIfaceCounters `json:"counters"`
	Warnings     []string                    `json:"warnings"`
}

// netSysctlKeys are the runtime keys the profiles read and may change. Every
// entry maps a sysctl key to its /proc/sys path.
var netSysctlKeys = []string{
	"net.core.rmem_max",
	"net.core.wmem_max",
	"net.core.rmem_default",
	"net.core.wmem_default",
	"net.core.netdev_max_backlog",
	"net.core.somaxconn",
	"net.core.default_qdisc",
	"net.ipv4.tcp_congestion_control",
	"net.ipv4.tcp_rmem",
	"net.ipv4.tcp_wmem",
	"net.ipv4.tcp_mtu_probing",
	"net.ipv4.tcp_fastopen",
	"net.ipv4.tcp_keepalive_time",
	"net.ipv4.tcp_keepalive_intvl",
	"net.ipv4.tcp_keepalive_probes",
	"net.ipv4.tcp_notsent_lowat",
	"net.ipv4.tcp_max_syn_backlog",
	"net.ipv4.ip_local_port_range",
	"net.ipv4.ip_local_reserved_ports",
	"net.ipv4.tcp_slow_start_after_idle",
	"net.netfilter.nf_conntrack_max",
	"vm.swappiness",
}

func netSysctlPath(key string) string {
	return "/proc/sys/" + strings.ReplaceAll(key, ".", "/")
}

// netReadSysctl returns the live value of one key, or "" with a warning.
func netReadSysctl(rep *netHardwareReport, key string) string {
	raw, err := netReadFile(netSysctlPath(key))
	if err != nil {
		rep.Warnings = append(rep.Warnings, "sysctl "+key+" unreadable: "+err.Error())
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// netKernelVersion reads `uname -r` through the shared runner.
func netKernelVersion(rep *netHardwareReport) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := netDetectExec(ctx, "uname", "-r")
	if err != nil {
		rep.Warnings = append(rep.Warnings, "uname -r failed: "+err.Error())
		return
	}
	rep.Kernel = strings.TrimSpace(string(out))
}

// netDetectVirt prefers systemd-detect-virt; falls back to DMI product name.
// "none" means bare metal (the tool exits 1 when not virtualized).
func netDetectVirt(rep *netHardwareReport) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := netDetectExec(ctx, "systemd-detect-virt")
	if err == nil {
		rep.VirtType = strings.TrimSpace(string(out))
		if rep.VirtType == "" {
			rep.VirtType = "none"
		}
		return
	}
	if raw, derr := netReadFile("/sys/class/dmi/id/product_name"); derr == nil {
		name := strings.ToLower(strings.TrimSpace(string(raw)))
		switch {
		case name == "":
			rep.VirtType = "unknown"
		case strings.Contains(name, "virtualbox"), strings.Contains(name, "vmware"),
			strings.Contains(name, "kvm"), strings.Contains(name, "qemu"),
			strings.Contains(name, "xen"), strings.Contains(name, "hyper-v"),
			strings.Contains(name, "cloud"), strings.Contains(name, "droplet"):
			rep.VirtType = name
		default:
			rep.VirtType = "none"
		}
		return
	}
	rep.VirtType = "unknown"
	rep.Warnings = append(rep.Warnings, "virtualization type undetectable")
}

// netIfaceKind classifies an interface from its name and driver.
func netIfaceKind(name, driver string) string {
	switch {
	case name == "lo":
		return "loopback"
	case strings.HasPrefix(name, "tun"), strings.HasPrefix(name, "tap"),
		strings.HasPrefix(name, "qmx"), strings.HasPrefix(name, "gre-"),
		strings.HasPrefix(name, "matrix"):
		return "tunnel"
	case driver == "virtio_net":
		return "virtio"
	case driver != "":
		return "physical"
	case strings.HasPrefix(name, "br"), strings.HasPrefix(name, "docker"),
		strings.HasPrefix(name, "veth"):
		return "bridge"
	}
	return "unknown"
}

// netPrimaryIface returns the interface the default route leaves through.
func netPrimaryIface(rep *netHardwareReport) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := netDetectExec(ctx, "ip", "route", "show", "default")
	if err != nil {
		rep.Warnings = append(rep.Warnings, "ip route failed: "+err.Error())
		return ""
	}
	fields := strings.Fields(string(out))
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

// netReadIface fills one interface's details. File reads that fail become
// warnings; only /sys/class/net/<dev>/mtu is treated as mandatory.
func netReadIface(rep *netHardwareReport, name string, primary string) netIfaceDetect {
	iface := netIfaceDetect{Name: name, Kind: netIfaceKind(name, "")}
	if raw, err := netReadFile("/sys/class/net/" + name + "/mtu"); err == nil {
		iface.MTU, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
	} else {
		rep.Warnings = append(rep.Warnings, "mtu "+name+" unreadable: "+err.Error())
	}
	if raw, err := netReadFile("/sys/class/net/" + name + "/operstate"); err == nil {
		iface.LinkUp = strings.TrimSpace(string(raw)) == "up" ||
			strings.TrimSpace(string(raw)) == "unknown" // tun/tap report "unknown" while up
	}
	// Driver: the device/driver symlink basename; empty for virtual ifaces.
	if raw, err := netLstat("/sys/class/net/" + name + "/device/driver"); err == nil {
		parts := strings.Split(raw, "/")
		iface.Driver = parts[len(parts)-1]
	} else if raw, err := netReadFile("/sys/class/net/" + name + "/tun_flags"); err == nil && len(raw) > 0 {
		iface.Driver = "tun"
	}
	iface.Kind = netIfaceKind(name, iface.Driver)
	iface.Primary = name == primary

	// Queues and RPS/XPS capability (single place the kernel exposes them).
	if entries, err := netReadDir("/sys/class/net/" + name + "/queues"); err == nil {
		rx, tx := 0, 0
		for _, e := range entries {
			switch {
			case strings.HasPrefix(e.Name(), "rx-"):
				rx++
				if _, err := netStat("/sys/class/net/" + name + "/queues/" + e.Name() + "/rps_flow_cnt"); err == nil {
					iface.RPSCapable = true
				}
			case strings.HasPrefix(e.Name(), "tx-"):
				tx++
				if _, err := netStat("/sys/class/net/" + name + "/queues/" + e.Name() + "/xps_cpus"); err == nil {
					iface.XPSCapable = true
				}
			}
		}
		iface.QueuesRX, iface.QueuesTX = rx, tx
	}

	// ethtool facts (speed/duplex/rings/offloads) — physical-class ifaces
	// only; virtio reports most of them as "unsupported" which we record as
	// zero/absent rather than guessing.
	if iface.Kind == "physical" || iface.Kind == "virtio" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := netDetectExec(ctx, "ethtool", name)
		cancel()
		if err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if v := netFieldAfter(line, "Speed:"); v != "" {
					iface.SpeedMBs = parseEthtoolSpeed(v)
				}
				if v := netFieldAfter(line, "Duplex:"); v != "" {
					iface.Duplex = v
				}
			}
		} else {
			rep.Warnings = append(rep.Warnings, "ethtool "+name+" failed: "+err.Error())
		}
		iface.RingCur, iface.RingMax = netEthtoolRings(rep, name)
		iface.Offloads = netEthtoolOffloads(rep, name)
	}
	return iface
}

// netFieldAfter returns the value after a "Key:" label on one ethtool line.
func netFieldAfter(line, label string) string {
	idx := strings.Index(line, label)
	if idx < 0 {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(line[idx+len(label):], ""))
}

// parseEthtoolSpeed converts ethtool's "1000Mb/s" (or "Unknown!") to Mbit/s.
func parseEthtoolSpeed(v string) int {
	v = strings.TrimSuffix(v, "Mb/s")
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0
	}
	return n
}

// netEthtoolRings reads current and maximum ring buffer sizes (0 when the
// NIC does not support programmable rings — pre-defined NICs and virtio).
func netEthtoolRings(rep *netHardwareReport, name string) (cur, max int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := netDetectExec(ctx, "ethtool", "-g", name)
	if err != nil {
		return 0, 0 // unsupported: not a warning, most virtual NICs refuse -g
	}
	section := ""
	for _, line := range strings.Split(string(out), "\n") {
		switch strings.TrimSpace(line) {
		case "Pre-set maximums:", "Pre-set maximums":
			section = "max"
			continue
		case "Current hardware settings:", "Current hardware settings":
			section = "cur"
			continue
		}
		f := strings.Fields(line)
		if len(f) >= 2 && (f[0] == "RX:" || f[0] == "TX:") {
			n, _ := strconv.Atoi(f[1])
			if section == "max" && f[0] == "RX:" {
				max = n
			}
			if section == "cur" && f[0] == "RX:" {
				cur = n
			}
		}
	}
	return cur, max
}

// netEthtoolOffloads reads the offload features the profiles may touch.
func netEthtoolOffloads(rep *netHardwareReport, name string) map[string]string {
	wanted := map[string]bool{
		"rx-checksumming": true, "tx-checksumming": true,
		"scatter-gather": true, "tcp-segmentation-offload": true,
		"generic-segmentation-offload": true, "generic-receive-offload": true,
	}
	outMap := map[string]string{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := netDetectExec(ctx, "ethtool", "-k", name)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "ethtool -k "+name+" failed: "+err.Error())
		return outMap
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.SplitN(line, ":", 2)
		if len(f) != 2 {
			continue
		}
		feat := strings.TrimSpace(f[0])
		if !wanted[feat] {
			continue
		}
		val := strings.Fields(f[1])
		if len(val) > 0 {
			outMap[feat] = val[0]
		}
	}
	return outMap
}

// netDetectHardware measures everything the profile selector needs. It never
// fails wholesale: unreadable facts become warnings and zero values.
func netDetectHardware() *netHardwareReport {
	rep := &netHardwareReport{
		GeneratedAt: time.Now().Unix(),
		Sysctls:     map[string]string{},
		Counters:    map[string]netIfaceCounters{},
	}
	netKernelVersion(rep)
	netDetectVirt(rep)

	if raw, err := netReadFile("/proc/cpuinfo"); err == nil {
		rep.CPUModel, rep.CPUCores = agentParseCPUInfo(raw)
	} else {
		rep.Warnings = append(rep.Warnings, "/proc/cpuinfo unreadable")
	}
	if raw, err := netReadFile("/proc/meminfo"); err == nil {
		// Parsed through the seam (not agentReadMemInfo) so tests inject
		// synthetic meminfo; used = total - available, like M1 reports it.
		total, avail := uint64(0), uint64(0)
		for _, line := range strings.Split(string(raw), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			kb, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				total = kb
			case "MemAvailable:":
				avail = kb
			case "SwapTotal:":
				rep.SwapTotalMB = kb / 1024
			case "SwapFree:":
				rep.SwapUsedMB = (swapTotalOf(raw) - kb) / 1024
			}
		}
		rep.RAMTotalMB = total / 1024
		if avail > 0 && total >= avail {
			rep.RAMUsedMB = (total - avail) / 1024
		}
		if total > 0 && avail > 0 {
			rep.MemAvailPct = float64(avail) / float64(total) * 100
		}
		rep.MemPressure = rep.MemAvailPct > 0 && rep.MemAvailPct < 15
	} else {
		rep.Warnings = append(rep.Warnings, "/proc/meminfo unreadable")
	}
	for _, key := range netSysctlKeys {
		if v := netReadSysctl(rep, key); v != "" {
			rep.Sysctls[key] = v
		}
	}
	rep.CC = rep.Sysctls["net.ipv4.tcp_congestion_control"]
	if raw, err := netReadFile("/proc/sys/net/ipv4/tcp_available_congestion_control"); err == nil {
		rep.CCAvailable = strings.Fields(string(raw))
	} else {
		rep.Warnings = append(rep.Warnings, "available congestion controls unreadable")
	}
	rep.DefaultQdisc = rep.Sysctls["net.core.default_qdisc"]

	rep.PrimaryIface = netPrimaryIface(rep)
	for name := range netParseProcNetDev(rep) {
		rep.Ifaces = append(rep.Ifaces, netReadIface(rep, name, rep.PrimaryIface))
	}
	sort.Slice(rep.Ifaces, func(i, j int) bool { return rep.Ifaces[i].Name < rep.Ifaces[j].Name })
	for _, iface := range rep.Ifaces {
		if c, ok := rep.Counters[iface.Name]; ok {
			iface.LinkUp = iface.LinkUp || c.RxPackets > 0
		}
	}
	return rep
}

// swapTotalOf re-reads SwapTotal from the same meminfo text (single pass
// helper; keeps the main loop free of lookahead).
func swapTotalOf(meminfo []byte) uint64 {
	for _, line := range strings.Split(string(meminfo), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "SwapTotal:" {
			kb, _ := strconv.ParseUint(f[1], 10, 64)
			return kb
		}
	}
	return 0
}

// netParseProcNetDev parses cumulative counters and primes rep.Counters.
func netParseProcNetDev(rep *netHardwareReport) map[string]netIfaceCounters {
	raw, err := netReadFile("/proc/net/dev")
	if err != nil {
		rep.Warnings = append(rep.Warnings, "/proc/net/dev unreadable: "+err.Error())
		return nil
	}
	out := map[string]netIfaceCounters{}
	for _, line := range strings.Split(string(raw), "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		f := strings.Fields(line[idx+1:])
		if len(f) < 16 {
			continue
		}
		u := func(i int) uint64 {
			v, _ := strconv.ParseUint(f[i], 10, 64)
			return v
		}
		c := netIfaceCounters{
			RxBytes: u(0), RxPackets: u(1), RxErrors: u(2), RxDropped: u(3),
			TxBytes: u(8), TxPackets: u(9), TxErrors: u(10), TxDropped: u(11),
			SampledAt: time.Now().Unix(),
		}
		out[name] = c
		rep.Counters[name] = c
	}
	return out
}

// netHardwareSummary is the one-line human digest of a detection report.
func netHardwareSummary(r *netHardwareReport) string {
	return fmt.Sprintf("%s | %s | %d cores | %d MB RAM (avail %.0f%%) | cc=%s qdisc=%s | primary=%s",
		r.Kernel, r.VirtType, r.CPUCores, r.RAMTotalMB, r.MemAvailPct, r.CC, r.DefaultQdisc, r.PrimaryIface)
}
