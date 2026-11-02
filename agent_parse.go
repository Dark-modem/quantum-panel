package main

// Pure parsers shared by the agent resource reporter (linux) and the
// netopt hardware detection (Session 2). No syscalls — byte-slice in,
// values out; every collector owns its own /proc reading.

import (
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

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

func agentParseLoadavg(data []byte) (float64, error) {
	f := strings.Fields(string(data))
	if len(f) == 0 {
		return 0, strconv.ErrSyntax
	}
	return strconv.ParseFloat(f[0], 64)
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
		if err != nil || port < 1 || port > 65535 {
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

// agentSSProcesses extracts the process names from a users:((...)) clause.
// agentSSProcsRe matches the quoted process names inside ss's
// users:(("sshd",pid=…),("systemd",pid=…)) clause.
var agentSSProcsRe = regexp.MustCompile(`"([a-zA-Z0-9_.\-]+)"`)

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

// splitHostPort handles IPv6 forms ss prints ([::]:22, 127.0.0.53%lo:53).
func splitHostPort(local string) (string, string, error) {
	if i := strings.LastIndex(local, ":"); i >= 0 {
		return strings.Trim(local[:i], "[]"), local[i+1:], nil
	}
	return "", "", strconv.ErrSyntax
}
