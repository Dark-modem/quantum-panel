package main

// Rolling 60-second tunnel health for Matrix instances. The probe loop runs
// one round per instance per 60s tick: three ICMP pings per remote side
// (hub: each paired peer; node: the hub IP) through the matrixExec funnel,
// plus the service and interface state. A few packets per minute — never a
// bandwidth test.

import (
	"context"
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// agentPingExec runs one probe round (3 packets, 2s timeout each) against
// target through iface. It goes through the matrixExec funnel so tests stub
// it like every other host command.
var agentPingExec = func(iface, target string) (string, error) {
	return matrixExec("ping", "-I", iface, "-c", "3", "-W", "2", target)
}

// agentIPLinkShow reads interface state (`ip -j link show`); seam for tests.
var agentIPLinkShow = func(iface string) (string, error) {
	return matrixExec("ip", "-j", "link", "show", iface)
}

var (
	agentPingTimeRe = regexp.MustCompile(`time[=<]\s*([0-9]+(?:\.[0-9]+)?)\s*ms`)
	agentPingLossRe = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)\s*%\s*packet loss`)
)

// agentParsePing extracts latency samples (ms) and the packet-loss percentage
// from Linux iputils ping output. loss is -1 when the output carries no
// summary line (the caller decides what that means, e.g. an exec error).
func agentParsePing(out string) (samples []float64, loss float64) {
	loss = -1
	for _, m := range agentPingTimeRe.FindAllStringSubmatch(out, -1) {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			samples = append(samples, v)
		}
	}
	if m := agentPingLossRe.FindStringSubmatch(out); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			loss = v
		}
	}
	return samples, loss
}

// agentIfaceUp reports whether the named interface exists and is UP. A
// missing interface is down.
func agentIfaceUp(name string) bool {
	out, err := agentIPLinkShow(name)
	if err != nil {
		return false
	}
	var rows []struct {
		OperState string   `json:"operstate"`
		Flags     []string `json:"flags"`
	}
	if json.Unmarshal([]byte(out), &rows) == nil {
		for _, r := range rows {
			if strings.EqualFold(strings.TrimSpace(r.OperState), "UP") {
				return true
			}
			// tun/tap devices (tinc's qmx) keep operstate UNKNOWN while the
			// interface is up: the kernel never sets carrier state for them
			// and the UP flag is the authoritative signal. DOWN still wins.
			for _, f := range r.Flags {
				if strings.EqualFold(f, "UP") && !strings.EqualFold(strings.TrimSpace(r.OperState), "DOWN") {
					return true
				}
			}
		}
	}
	return strings.Contains(out, "state UP") // plain-text fallback
}

// agentHealthInterval is the probe period; a seam for tests.
var agentHealthInterval = 60 * time.Second

// agentHealthLatest keeps the newest health window per matrix instance in
// memory; StateReport.Health carries these.
var (
	agentHealthMu     sync.Mutex
	agentHealthLatest = map[string]agentTunnelHealth{}
)

func agentHealthLoop(ctx context.Context) {
	agentProbeAll()
	t := time.NewTicker(agentHealthInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			agentProbeAll()
		}
	}
}

func agentProbeAll() {
	for _, in := range matrixInstances() {
		agentProbeInstance(in)
	}
}

// agentProbeInstance runs one probe cycle for in and stores the resulting
// 60s window. Instances that are not an enabled owned network report
// service=false → quality "down".
func agentProbeInstance(in matrixInstance) agentTunnelHealth {
	matrixMu.Lock()
	st := loadMatrixInstance(in)
	owned := st.Enabled && st.Network == in.network()
	service := owned && matrixServiceActive(in)
	var targets []string
	if service {
		if st.Role == "hub" {
			for _, p := range st.Peers {
				if p.IP != "" {
					targets = append(targets, p.IP)
				}
			}
		} else {
			targets = append(targets, in.hubIP())
		}
	}
	matrixMu.Unlock()

	h := agentTunnelHealth{
		Instance: in.ID, WindowS: 60, Samples: 3,
		Service: service, IfaceUp: service && agentIfaceUp(in.iface()),
	}
	var samples []float64
	var lossSum float64
	probed := 0
	for _, ip := range targets {
		out, err := agentPingExec(in.iface(), ip)
		s, loss := agentParsePing(out)
		samples = append(samples, s...)
		switch {
		case loss >= 0:
			// summary line present
		case err != nil:
			loss = 100 // the probe itself failed and produced no summary
		default:
			loss = 0
		}
		lossSum += loss
		probed++
	}
	if probed > 0 {
		h.LossPct = lossSum / float64(probed)
	}
	if len(samples) > 0 {
		var sum float64
		for _, v := range samples {
			sum += v
		}
		h.LatencyMs = sum / float64(len(samples))
		h.JitterMs = agentMeanJitter(samples)
	}
	h.Reachable = len(samples) > 0
	h.Quality = agentQualityBucket(h)

	agentHealthMu.Lock()
	agentHealthLatest[in.ID] = h
	agentHealthMu.Unlock()
	return h
}

// agentMeanJitter is the mean absolute difference of successive samples.
func agentMeanJitter(samples []float64) float64 {
	if len(samples) < 2 {
		return 0
	}
	var sum float64
	for i := 1; i < len(samples); i++ {
		sum += math.Abs(samples[i] - samples[i-1])
	}
	return sum / float64(len(samples)-1)
}

// agentQualityBucket maps a health window onto good | fair | poor | down.
func agentQualityBucket(h agentTunnelHealth) string {
	if !h.Service || !h.IfaceUp || h.LossPct >= 100 {
		return "down"
	}
	if h.LossPct >= 10 || !h.Reachable {
		return "poor"
	}
	if h.LossPct >= 2 || h.JitterMs >= 30 {
		return "fair"
	}
	return "good"
}

// agentHealthSnapshot returns the latest window per instance, sorted by
// instance id.
func agentHealthSnapshot() []agentTunnelHealth {
	agentHealthMu.Lock()
	defer agentHealthMu.Unlock()
	out := make([]agentTunnelHealth, 0, len(agentHealthLatest))
	for _, h := range agentHealthLatest {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Instance < out[j].Instance })
	return out
}
