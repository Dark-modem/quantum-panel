package panel

// overview.go — the /ui/api/overview payload (S4 extract from uiapi.go:
// that file is on the ui/live merge surface, so the handler body lives
// here and uiapi.go keeps only a thin wrapper). Body verbatim.

import (
	"net/http"
	"time"
)

// uiOverviewServe is handleUIOverview's body (S4 extract, verbatim).
func uiOverviewServe(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	now := time.Now().Unix()

	dcs, _ := h.ListDatacenters()
	servers, _ := h.ListServers()
	agents, _ := h.ListAgents()
	topos, _ := h.ListTopologies()
	tunnels, _ := h.ListAllTunnels()

	dcCounts := map[string]int{"iran": 0, "foreign": 0}
	for _, d := range dcs {
		dcCounts[d.Kind]++
	}
	svCounts := map[string]int{"iran": 0, "foreign": 0}
	for _, s := range servers {
		svCounts[s.Role]++
	}
	agCounts := map[string]int{"online": 0, "offline": 0}
	// The counts map was initialized but never filled (phase-11 UI probe:
	// the dashboard answered online:0/offline:0 while 3 agents were
	// connected) — classify with the same verdict the agents list uses.
	for _, a := range agents {
		if uiAgentOnline(a) {
			agCounts["online"]++
		} else {
			agCounts["offline"]++
		}
	}
	tpCounts := map[string]int{"total": len(topos), "enabled": 0}
	for _, t := range topos {
		if t.Enabled {
			tpCounts["enabled"]++
		}
	}
	verdicts := uiTunnelVerdicts()
	tuCounts := map[string]int{"total": len(tunnels), "up": 0, "degraded": 0, "down": 0, "disabled": 0}
	tunnelIDs := make([]int64, 0, len(tunnels))
	for _, t := range tunnels {
		tunnelIDs = append(tunnelIDs, t.ID)
		if !t.Enabled {
			tuCounts["disabled"]++
			continue
		}
		switch verdicts[t.TunnelKey] {
		case "healthy":
			tuCounts["up"]++
		case "degraded":
			tuCounts["degraded"]++
		case "down":
			tuCounts["down"]++
		default:
			tuCounts["down"]++ // enabled but no report = not up
		}
	}

	// Aggregate agent resources (never the panel host — it has no agent).
	type maxEntry struct {
		agent string
		name  string
		value float64
	}
	var res map[string]any
	{
		var n int
		var cpuSum, cpuMaxV, memSum, memPctSum, memMaxV, diskSum, diskPctSum, diskMaxV float64
		var coresSum, loadSum, memTotSum, diskTotSum, maxUp float64
		memMax, diskMax, cpuMax := maxEntry{}, maxEntry{}, maxEntry{}
		for _, a := range agents {
			if a.MemTotalMB == 0 && a.CPUCores == 0 {
				continue // no report yet
			}
			n++
			cpuSum += a.CPUPct
			coresSum += float64(a.CPUCores)
			loadSum += a.Load1
			memTotSum += float64(a.MemTotalMB)
			diskTotSum += a.DiskTotalGB
			if a.UptimeS > int64(maxUp) {
				maxUp = float64(a.UptimeS)
			}
			if a.CPUPct > cpuMaxV {
				cpuMaxV, cpuMax = a.CPUPct, maxEntry{agent: a.ID, name: a.Name, value: a.CPUPct}
			}
			memSum += float64(a.MemUsedMB)
			if a.MemTotalMB > 0 {
				pct := float64(a.MemUsedMB) / float64(a.MemTotalMB) * 100
				memPctSum += pct
				if pct > memMaxV {
					memMaxV, memMax = pct, maxEntry{agent: a.ID, name: a.Name, value: pct}
				}
			}
			diskSum += a.DiskUsedGB
			if a.DiskTotalGB > 0 {
				pct := a.DiskUsedGB / a.DiskTotalGB * 100
				diskPctSum += pct
				if pct > diskMaxV {
					diskMaxV, diskMax = pct, maxEntry{agent: a.ID, name: a.Name, value: pct}
				}
			}
		}
		agentRef := func(m maxEntry) map[string]any {
			return map[string]any{"agent": m.agent, "name": m.name, "value": m.value}
		}
		avg := func(sum float64) any {
			if n == 0 {
				return nil
			}
			return sum / float64(n)
		}
		res = map[string]any{
			"agent_count": n,
			"cpu_pct":     map[string]any{"sum": cpuSum, "avg": avg(cpuSum), "max": agentRef(cpuMax)},
			"mem":         map[string]any{"sum_used_mb": memSum, "sum_total_mb": memTotSum, "avg_pct": avg(memPctSum), "max": agentRef(memMax)},
			"disk":        map[string]any{"sum_used_gb": diskSum, "sum_total_gb": diskTotSum, "avg_pct": avg(diskPctSum), "max": agentRef(diskMax)},
			"cores_total": coresSum,
			"avg_load1":   avg(loadSum),
			"max_uptime_s": func() any {
				if n == 0 {
					return nil
				}
				return int64(maxUp)
			}(),
		}
	}

	// Traffic: the WHOLE block is cached at the UI poll cadence (10 s) —
	// every poller shares one computation instead of N concurrent range
	// scans (the phase-12 soak's worst hotspot after write batching).
	uiOverviewMu.Lock()
	if uiOverviewTraffic == nil || now-uiOverviewAt >= int64(uiOverviewCacheTTL.Seconds()) {
		uiOverviewTraffic = h.uiTrafficTotals(tunnelIDs, now)
		uiOverviewTraffic["per_datacenter"] = h.uiDatacenterBlock(now)
		uiOverviewAt = now
	}
	cachedBlock := uiOverviewTraffic
	cacheAge := now - uiOverviewAt
	uiOverviewMu.Unlock()

	alertsOpen := 0
	if open, _, _ := meshAlertEng.currentAlertsView(); open != nil {
		alertsOpen = len(open)
	}
	cert := map[string]any{"state": "unknown"}
	if tlsHasCert() {
		if exp, _, err := tlsCertExpiry(); err == nil {
			days := int(time.Until(exp).Hours() / 24)
			state := "valid"
			if days < 0 {
				state = "expired"
			} else if days < 15 {
				state = "expiring_soon"
			}
			cert = map[string]any{
				"expiry":    exp.Format("2006-01-02 15:04:05"),
				"days_left": days,
				"state":     state,
			}
		}
	}

	writeJSON(w, map[string]any{
		"generated_at": now,
		"cached":       cacheAge > 5,
		"counts": map[string]any{
			"datacenters": dcCounts,
			"servers":     svCounts,
			"agents":      agCounts,
			"topologies":  tpCounts,
			"tunnels":     tuCounts,
		},
		"resources": res,
		"host":      hostStats(),
		"traffic": map[string]any{
			"today":          cachedBlock["today"],
			"7d":             cachedBlock["7d"],
			"30d":            cachedBlock["30d"],
			"per_datacenter": cachedBlock["per_datacenter"],
		},
		"alerts":                map[string]any{"open": alertsOpen},
		"pending_confirmations": len(profilePendingBy),
		"certificate":           cert,
	})
}
