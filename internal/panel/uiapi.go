package panel

// uiapi.go — the new UI REST API (Phase 11, contract:
// 00-ADMIN/UI-API-CONTRACT.md). Aggregate + detail views for the rebuilt
// web UI, the structured error format for every failure, and the
// enable-before-use engine endpoints. The panel host's own identity is
// deliberately ABSENT everywhere (requirement 7): the central panel
// carries no tunnels, so there is no panel-host traffic widget either.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// uiKernelEngines never have process metrics or on-disk binaries (the
// agent reports their tooling honestly; the UI renders null).
var uiKernelEngines = map[string]bool{"matrix": true, "wireguard": true}

// uiArtifactEngines are the engines whose binary arrives through the
// signed-artifact manifest flow (R5). Kernel tooling (matrix/wireguard),
// distro packages (tinc) and lab engines have NO artifact row by design
// — the readiness gate must not demand one for them.
var uiArtifactEngines = map[string]bool{
	"frp": true, "backhaul": true, "chisel": true, "wst": true,
	"gost": true, "rathole": true, "xray": true,
}

func registerUIAPIRoutes(mux *http.ServeMux, base string) {
	mux.HandleFunc("GET "+base+"/api/ui/session", requireAuth(handleUISession))
	mux.HandleFunc("GET "+base+"/api/ui/overview", requireAuth(handleUIOverview))
	mux.HandleFunc("GET "+base+"/api/ui/agents", requireAuth(handleUIAgentsList))
	mux.HandleFunc("GET "+base+"/api/ui/agents/{id}", requireAuth(handleUIAgentDetail))
	mux.HandleFunc("GET "+base+"/api/ui/agents/{id}/topologies", requireAuth(handleUIAgentTopologies))
	mux.HandleFunc("GET "+base+"/api/ui/agents/{id}/engines", requireAuth(handleUIAgentEngines))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/engines/{engine}/enable", requireAuth(requireCSRF(handleUIEngineEnable)))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/engines/{engine}/disable", requireAuth(requireCSRF(handleUIEngineDisable)))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/public-ip", requireAuth(requireCSRF(handleUIAgentPublicIP)))
	mux.HandleFunc("GET "+base+"/api/ui/topologies/{id}", requireAuth(handleUITopologyDetail))
	mux.HandleFunc("GET "+base+"/api/ui/tunnels/{key}", requireAuth(handleUITunnelDetail))
	mux.HandleFunc("POST "+base+"/api/ui/topologies/{id}/tunnels/{key}/weight", requireAuth(requireCSRF(handleUITunnelWeight)))
}

// ---- shared helpers ---------------------------------------------------

type uiStoreHandle struct {
	*store.Store
}

// uiOpen opens the mesh store or writes the structured store error.
func uiOpen(w http.ResponseWriter, r *http.Request) (uiStoreHandle, bool) {
	s, err := MeshStore()
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "open"}, err.Error())
		return uiStoreHandle{}, false
	}
	return uiStoreHandle{s}, true
}

// uiAgentOnline: the store flag AND a recent heartbeat agree.
func uiAgentOnline(ag store.Agent) bool {
	return ag.Status == "online" && meshRuntimeFresh(ag.ID, 90*time.Second)
}

// uiEngineList is the union the detail endpoints show: catalog + desired
// rows + matrix default, each with the agent-reported state merged in.
func (h uiStoreHandle) uiEngineList(ag store.Agent, v2Catalog []string) []map[string]any {
	rows, _ := h.AgentEngines(ag.ID)
	byEngine := map[string]store.AgentEngine{}
	for _, row := range rows {
		byEngine[row.Engine] = row
	}
	seen := map[string]bool{}
	out := []map[string]any{}
	add := func(engine string) {
		if engine == "" || seen[engine] {
			return
		}
		seen[engine] = true
		row := byEngine[engine]
		desired := row.Desired
		if _, has := byEngine[engine]; !has {
			if d, err := h.AgentEngineDesired(ag.ID, engine); err == nil {
				desired = d
			}
		}
		health := any(nil)
		if row.Instances > 0 {
			switch {
			case row.Instances > 0 && row.LastError == "":
				health = "healthy"
			default:
				health = "down"
			}
		}
		bytes := any(nil)
		if row.Installed && row.BytesOnDisk > 0 && !uiKernelEngines[engine] {
			bytes = row.BytesOnDisk
		}
		version := any(nil)
		if row.Version != "" {
			version = row.Version
		}
		lastErr := any(nil)
		if row.LastError != "" {
			lastErr = redactErrorText(row.LastError)
		}
		out = append(out, map[string]any{
			"engine":          engine,
			"installed":       row.Installed,
			"version":         version,
			"bytes_on_disk":   bytes,
			"enabled":         desired,
			"desired_enabled": desired,
			"instances":       row.Instances,
			"health":          health,
			"last_error":      lastErr,
			"kernel":          uiKernelEngines[engine],
		})
	}
	add("matrix") // desired-by-default first
	for _, c := range v2Catalog {
		add(c)
	}
	for _, row := range rows {
		add(row.Engine)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["engine"].(string) < out[j]["engine"].(string) })
	return out
}

// uiCatalogOf returns the agent's reported engine catalog ids.
func (h uiStoreHandle) uiCatalogOf(agentID string) []string {
	v2, err := h.AgentV2(agentID)
	if err != nil || strings.TrimSpace(v2.EngineCatalog) == "" {
		return nil
	}
	var catalog []struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(v2.EngineCatalog), &catalog) != nil {
		return nil
	}
	out := make([]string, 0, len(catalog))
	for _, c := range catalog {
		if c.ID != "" {
			out = append(out, c.ID)
		}
	}
	return out
}

// uiTunnelVerdicts maps tunnel_key -> healthy|degraded|down from the
// tunnel-health pass (60 s cache) + the live runtime view.
func uiTunnelVerdicts() map[string]string {
	out := map[string]string{}
	meshRuntimeMu.Lock()
	for agentID, rts := range meshRuntimeBy {
		fresh := time.Now().Unix()-meshRuntimeSeen[agentID] < 90
		if !fresh {
			continue
		}
		for _, tr := range rts {
			if !tr.Up {
				out[tr.TunnelID] = "down"
			} else if _, ok := out[tr.TunnelID]; !ok {
				out[tr.TunnelID] = "healthy"
			}
		}
	}
	meshRuntimeMu.Unlock()
	if res := tunnelHealthCached(); res != nil {
		for _, e := range res.Tunnels {
			switch e.Verdict {
			case "PASS":
				if out[e.Name] != "down" {
					out[e.Name] = "healthy"
				}
			case "FAIL":
				if out[e.Name] == "" || out[e.Name] == "healthy" {
					out[e.Name] = "degraded"
				}
			case "UNREACHABLE":
				out[e.Name] = "down"
			}
		}
	}
	return out
}

// uiResolvedPercents computes each tunnel's share of its balancing scope
// (the topology's enabled tunnels per local server): weight / Σweights.
func (h uiStoreHandle) uiResolvedPercents(topoID int64, tunnels []store.Tunnel) map[int64]float64 {
	out := map[int64]float64{}
	sums := map[int64]int{}      // localServerID -> Σweights (enabled tunnels)
	weights := map[int64]int{}   // tunnelID -> weight
	localOf := map[int64]int64{} // tunnelID -> local server
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}
		w, err := h.WeightFor(topoID, t.LocalServerID, t.RemoteServerID, t.ID)
		if err != nil {
			w = 1
		}
		weights[t.ID] = w
		localOf[t.ID] = t.LocalServerID
		if w > 0 {
			sums[t.LocalServerID] += w
		}
	}
	for id, w := range weights {
		if w <= 0 || sums[localOf[id]] == 0 {
			out[id] = 0 // excluded from balancing
			continue
		}
		out[id] = float64(w) / float64(sums[localOf[id]]) * 100
	}
	return out
}

// uiTrafficTotals builds the today/7d/30d block for a tunnel set.
func (h uiStoreHandle) uiTrafficTotals(tunnelIDs []int64, now int64) map[string]any {
	mk := func(from, to int64) map[string]any {
		all, iran, err := h.RangeUsageDetailed(tunnelIDs, from, to)
		if err != nil {
			return map[string]any{"error": err.Error()}
		}
		return map[string]any{
			"billable_download_bytes": iran.WireRx, // wire rx received on Iran = billed
			"upload_bytes":            iran.WireTx,
			"payload_download_bytes":  iran.PayloadRx,
			"payload_upload_bytes":    iran.PayloadTx,
			"wire_rx_bytes":           all.WireRx,
			"wire_tx_bytes":           all.WireTx,
			"payload_rx_bytes":        all.PayloadRx,
			"payload_tx_bytes":        all.PayloadTx,
		}
	}
	dayStart := now - now%86400
	return map[string]any{
		"today": mk(dayStart, now),
		"7d":    mk(now-7*86400, now),
		"30d":   mk(now-30*86400, now),
	}
}

// ---- GET /api/ui/session ----------------------------------------------

func handleUISession(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("gre_session"); err == nil {
		_ = c
	}
	alertsOpen := 0
	if open, _, _ := meshAlertEng.currentAlertsView(); open != nil {
		alertsOpen = len(open)
	}
	lang := "fa"
	if c, err := r.Cookie("gre_lang"); err == nil && (c.Value == "fa" || c.Value == "en") {
		lang = c.Value
	}
	resp := map[string]any{
		"user":               cfg.Username,
		"language":           lang,
		"csrf_token":         GenerateCSRFToken(sessionTokenOf(r)),
		"session_expires_at": sessionExpiryOf(r),
		"panel_version":      panelVersion,
		"alerts_open":        alertsOpen,
		"server_time":        time.Now().Unix(),
	}
	writeJSON(w, resp)
}

func sessionTokenOf(r *http.Request) string {
	c, err := r.Cookie("gre_session")
	if err != nil {
		return ""
	}
	return c.Value
}

// sessionExpiryOf reads the session store for the caller's token.
func sessionExpiryOf(r *http.Request) int64 {
	tok := sessionTokenOf(r)
	if tok == "" {
		return 0
	}
	mu.Lock()
	defer mu.Unlock()
	s := loadSessions()
	return s.Tokens[tok]
}

// ---- GET /api/ui/overview ---------------------------------------------

// The 7d/30d traffic blocks are cached (they scan the hourly rollup for
// thousands of rows); the counts/resources/today blocks are computed
// live. cached:true tells the UI how stale the expensive numbers are.
var (
	uiOverviewMu      sync.Mutex
	uiOverviewTraffic map[string]any
	uiOverviewAt      int64
)

const uiOverviewCacheTTL = 60 * time.Second

func handleUIOverview(w http.ResponseWriter, r *http.Request) {
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
	res := map[string]any{}
	{
		var n int
		var cpuSum, cpuMaxV, memSum, memPctSum, memMaxV, diskSum, diskPctSum, diskMaxV float64
		memMax, diskMax, cpuMax := maxEntry{}, maxEntry{}, maxEntry{}
		for _, a := range agents {
			if a.MemTotalMB == 0 && a.CPUCores == 0 {
				continue // no report yet
			}
			n++
			cpuSum += a.CPUPct
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
			"mem":         map[string]any{"sum_used_mb": memSum, "avg_pct": avg(memPctSum), "max": agentRef(memMax)},
			"disk":        map[string]any{"sum_used_gb": diskSum, "avg_pct": avg(diskPctSum), "max": agentRef(diskMax)},
		}
	}

	// Traffic: today recomputed live; 7d/30d from the cache.
	trafficToday := h.uiTrafficTotals(tunnelIDs, now)["today"]
	uiOverviewMu.Lock()
	if uiOverviewTraffic == nil || now-uiOverviewAt >= int64(uiOverviewCacheTTL.Seconds()) {
		uiOverviewTraffic = map[string]any{
			"7d":             h.uiTrafficTotals(tunnelIDs, now)["7d"],
			"30d":            h.uiTrafficTotals(tunnelIDs, now)["30d"],
			"per_datacenter": h.uiDatacenterBlock(now),
		}
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
		"traffic": map[string]any{
			"today":          trafficToday,
			"7d":             cachedBlock["7d"],
			"30d":            cachedBlock["30d"],
			"per_datacenter": cachedBlock["per_datacenter"],
		},
		"alerts":                map[string]any{"open": alertsOpen},
		"pending_confirmations": len(profilePendingBy),
		"certificate":           cert,
	})
}

// uiDatacenterBlock is the per-Iran-datacenter billable view.
func (h uiStoreHandle) uiDatacenterBlock(now int64) []map[string]any {
	rows, err := h.RangeUsageByDatacenter(now-now%86400, now) // today, per DC
	if err != nil {
		return []map[string]any{}
	}
	out := []map[string]any{}
	for _, row := range rows {
		if row.Kind != "iran" {
			continue // the billable view is about Iran datacenters
		}
		out = append(out, map[string]any{
			"datacenter_id":           row.DatacenterID,
			"name":                    row.Name,
			"kind":                    row.Kind,
			"billable_download_bytes": row.Usage.WireRx,
			"upload_bytes":            row.Usage.WireTx,
			"payload_download_bytes":  row.Usage.PayloadRx,
			"payload_upload_bytes":    row.Usage.PayloadTx,
		})
	}
	return out
}
