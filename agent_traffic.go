package main

// Traffic accounting for the agent heartbeat: per-interface cumulative
// byte counters read from /proc/net/dev and classified into physical
// (billable NIC) vs tunnel (Matrix overlay) interfaces. Raw counters only —
// the controller derives deltas, so a missed heartbeat never loses bytes.

import (
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// agentReadNetDev reads the kernel interface counters; seam for tests.
var agentReadNetDev = func() (string, error) {
	b, err := os.ReadFile("/proc/net/dev")
	return string(b), err
}

type agentDevCounters struct{ Rx, Tx uint64 }

// agentParseNetDev parses /proc/net/dev rows into name → rx/tx bytes.
// Deliberately local to the agent (snapshot.go's parseNetDev serves the
// panel's own snapshot pipeline) so the two stay independently evolvable.
func agentParseNetDev(data string) map[string]agentDevCounters {
	m := map[string]agentDevCounters{}
	for _, line := range strings.Split(data, "\n") {
		name, rest, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) < 9 {
			continue
		}
		rx, e1 := strconv.ParseUint(f[0], 10, 64)
		tx, e2 := strconv.ParseUint(f[8], 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		m[strings.TrimSpace(name)] = agentDevCounters{rx, tx}
	}
	return m
}

// Interface kinds produced by agentClassifyIface.
const (
	agentIfacePhysical = "physical" // billable NIC accounting
	agentIfaceTunnel   = "tunnel"   // Matrix overlay (qmx*)
	agentIfaceOther    = "other"    // virtual plumbing that is neither
	agentIfaceSkip     = ""         // loopback and friends: not reported
)

// agentClassifyIface sorts an interface name into physical / tunnel / other.
// "other" interfaces are virtual plumbing (docker bridges, veth pairs, GRE
// and friends) whose counters would double-count physical traffic or noise
// up the dashboards, so they are not reported.
func agentClassifyIface(name string) string {
	if name == "" || name == "lo" {
		return agentIfaceSkip
	}
	if strings.HasPrefix(name, "qmx") {
		return agentIfaceTunnel
	}
	for _, p := range []string{"gre", "tun", "veth", "br-", "docker", "vti", "sit", "wg"} {
		if strings.HasPrefix(name, p) {
			return agentIfaceOther
		}
	}
	if strings.HasSuffix(name, "-if") {
		return agentIfaceOther
	}
	return agentIfacePhysical
}

// agentCollectTraffic reads the counters once per heartbeat. If /proc/net/dev
// is unreadable the report carries one UNKNOWN entry instead of zeros, so the
// controller shows UNKNOWN rather than inventing a number.
func agentCollectTraffic() *agentTrafficReport {
	rep := &agentTrafficReport{TS: time.Now().Unix()}
	data, err := agentReadNetDev()
	if err != nil {
		rep.Ifaces = []agentIfaceCounters{{Name: "_all", Kind: agentIfacePhysical, Known: false}}
		return rep
	}
	for name, c := range agentParseNetDev(data) {
		kind := agentClassifyIface(name)
		if kind != agentIfacePhysical && kind != agentIfaceTunnel {
			continue
		}
		rep.Ifaces = append(rep.Ifaces, agentIfaceCounters{Name: name, Kind: kind, Rx: c.Rx, Tx: c.Tx, Known: true})
	}
	sort.Slice(rep.Ifaces, func(i, j int) bool { return rep.Ifaces[i].Name < rep.Ifaces[j].Name })
	return rep
}

// errAgentNoNetDev documents the unreadable-counter case for tests.
var errAgentNoNetDev = errors.New("/proc/net/dev unreadable")
