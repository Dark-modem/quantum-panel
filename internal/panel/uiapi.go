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

// uiKnownEngines is the full panel-known engine set. The agent engines
// grid shows every one of them with its per-agent state — an engine the
// agent never reported renders as «not installed» so the operator sees
// what CAN be installed (install-first lifecycle). bagher is the panel's
// own SSH-pool carrier (the Bagher topology type's engine).
var uiKnownEngines = []string{
	"aestun", "rakhsh", "backpack", "backhaul", "bagher", "chisel", "dagger", "easytier", "frp", "gost",
	"hysteria", "matrix", "midos", "narnia", "paqet", "pengo", "rathole",
	"singbox", "spoof", "tinc", "udp2raw", "waterwall", "wattunnel",
	"wireguard", "wst", "xray",
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
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/rename", requireAuth(requireCSRF(handleUIAgentRename)))
	mux.HandleFunc("DELETE "+base+"/api/ui/agents/{id}", requireAuth(requireCSRF(handleUIAgentDelete)))
	mux.HandleFunc("GET "+base+"/api/ui/topologies/{id}", requireAuth(handleUITopologyDetail))
	// Phase 14: per-member port view (ledger + reservations + what the
	// agents actually observed) — the port-conflict workflow reads this
	// before picking a listen port (UI-API-CONTRACT.md §2.11).
	mux.HandleFunc("GET "+base+"/api/ui/topologies/{id}/ports", requireAuth(handleUITopologyPorts))
	mux.HandleFunc("GET "+base+"/api/ui/tunnels/{key}", requireAuth(handleUITunnelDetail))
	mux.HandleFunc("POST "+base+"/api/ui/topologies/{id}/tunnels/{key}/weight", requireAuth(requireCSRF(handleUITunnelWeight)))
	// Phase 12 TASK 4: the billing export (UI screen requests it via
	// UI-API-REQUESTS.md; the CLI `quantum-panel report` shares the code).
	mux.HandleFunc("GET "+base+"/api/ui/reports/billing", requireAuth(handleUIBillingReport))
	mux.HandleFunc("GET "+base+"/api/ui/traffic-series", requireAuth(handleUITrafficSeries))
	// Phase 14 task 2: the Xray client-config preview for one iran
	// datacenter (UI-API-REQUESTS.md) — the Reality fields come from the
	// sealed reality_keys store (pgdcapi.go).
	mux.HandleFunc("GET "+base+"/api/ui/datacenters/{id}/client-config", requireAuth(handleUIDCClientConfig))
	mux.HandleFunc("GET "+base+"/api/ui/selfcheck", requireAuth(handleUISelfcheck))
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
// schemas carries the agents' hello-catalog params_schema per engine id
// (BACKEND-45: the engines-catalog passthrough — the UI builds the wizard
// fields against the agent's OWN schema; nil for engines the agent did
// not publish a schema for).
func (h uiStoreHandle) uiEngineList(ag store.Agent, v2Catalog []string, schemas map[string]json.RawMessage) []map[string]any {
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
		var schema any
		if s, ok := schemas[engine]; ok && len(s) > 0 {
			schema = json.RawMessage(s)
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
			"params_schema":   schema,
		})
	}
	add("matrix") // desired-by-default first
	for _, c := range v2Catalog {
		add(c)
	}
	for _, row := range rows {
		add(row.Engine)
	}
	for _, eng := range uiKnownEngines {
		add(eng)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["engine"].(string) < out[j]["engine"].(string) })
	return out
}

// uiCatalogOf returns the agent's reported engine catalog ids.
func (h uiStoreHandle) uiCatalogOf(agentID string) []string {
	entries := h.uiCatalogEntries(agentID)
	out := make([]string, 0, len(entries))
	for _, c := range entries {
		out = append(out, c.ID)
	}
	return out
}

// uiCatalogEntry is one hello-catalog entry (recordEngineCatalog's stored
// shape: {id, version?, params_schema?}).
type uiCatalogEntry struct {
	ID           string
	Version      string
	ParamsSchema json.RawMessage
}

// uiCatalogEntries decodes the agent's stored v2 engine catalog — the
// BACKEND-45 passthrough's source: the schemas ride the §2.4/§2.5 engines
// rows so the UI renders the agent's OWN param shapes (hysteria's
// obfs/bandwidth, sing-box's protocol fields, gost's service family,
// frp's vhost family) instead of guessing from the fallback table.
func (h uiStoreHandle) uiCatalogEntries(agentID string) []uiCatalogEntry {
	v2, err := h.AgentV2(agentID)
	if err != nil || strings.TrimSpace(v2.EngineCatalog) == "" {
		return nil
	}
	var catalog []struct {
		ID           string          `json:"id"`
		Version      string          `json:"version,omitempty"`
		ParamsSchema json.RawMessage `json:"params_schema,omitempty"`
	}
	if json.Unmarshal([]byte(v2.EngineCatalog), &catalog) != nil {
		return nil
	}
	out := make([]uiCatalogEntry, 0, len(catalog))
	for _, c := range catalog {
		if c.ID == "" {
			continue
		}
		out = append(out, uiCatalogEntry{ID: c.ID, Version: c.Version, ParamsSchema: c.ParamsSchema})
	}
	return out
}

// uiCatalogSchemas maps engine id → the hello-catalog params_schema (the
// uiEngineList rows' additive field).
func (h uiStoreHandle) uiCatalogSchemas(agentID string) map[string]json.RawMessage {
	entries := h.uiCatalogEntries(agentID)
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(entries))
	for _, c := range entries {
		if len(c.ParamsSchema) > 0 {
			out[c.ID] = c.ParamsSchema
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

// uiVerdictsFn is the verdicts source behind the runtime-aware weights;
// tests stub it to place tunnels in a known up/down state.
var uiVerdictsFn = uiTunnelVerdicts

// uiResolvedPercents computes each tunnel's share of its balancing scope
// (the topology's enabled tunnels per local server): weight / Σweights.
func (h uiStoreHandle) uiResolvedPercents(topoID int64, tunnels []store.Tunnel, verdicts map[string]string) map[int64]float64 {
	out := map[int64]float64{}
	sums := map[int64]int{}      // localServerID -> Σweights (tunnels that are UP right now)
	weights := map[int64]int{}   // tunnelID -> weight
	localOf := map[int64]int64{} // tunnelID -> local server
	// Runtime-aware balancing: only tunnels that are actually connected
	// carry weight. When one drops, its share flows to the surviving
	// tunnels automatically (the sum recomputes without it).
	up := func(t store.Tunnel) bool {
		if !t.Enabled {
			return false
		}
		v := verdicts[t.TunnelKey]
		return v == "healthy" || v == "degraded"
	}
	for _, t := range tunnels {
		if !up(t) {
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
			continue // excluded from balancing right now
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

const uiOverviewCacheTTL = 10 * time.Second // the UI poll cadence

// uiOverviewCacheReset drops the cached traffic block (tests; each test
// server must not inherit the previous test's tunnels).
func uiOverviewCacheReset() {
	uiOverviewMu.Lock()
	uiOverviewTraffic = nil
	uiOverviewAt = 0
	uiOverviewMu.Unlock()
}

// handleUIOverview serves the /ui/api/overview payload. S4: the body moved
// verbatim to overview.go (uiOverviewServe) — uiapi.go is on the ui/live
// merge surface, so only this thin wrapper stays behind.
func handleUIOverview(w http.ResponseWriter, r *http.Request) {
	uiOverviewServe(w, r)
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
