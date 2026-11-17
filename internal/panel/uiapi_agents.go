package panel

// uiapi_agents.go — agent list/detail/engines endpoints (Phase 11
// contract §2.3–2.7).

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	"github.com/Dark-modem/quantum-protocol"
)

// uiAgentResourceViews builds the cpu/mem/disk maps the UI renders — the
// SAME shape at both serving spots (§2.3 rows and the §2.4 detail object),
// so the agents pages' resource columns carry the detail page's semantics:
// a metric stays nil until the agent's heartbeat reported it (mem/disk need
// their totals, cpu needs cores).
func uiAgentResourceViews(ag store.Agent) (cpu, mem, disk any) {
	mem = nil
	if ag.MemTotalMB > 0 {
		mem = map[string]any{"total_mb": ag.MemTotalMB, "used_mb": ag.MemUsedMB}
	}
	disk = nil
	if ag.DiskTotalGB > 0 {
		disk = map[string]any{"total_gb": ag.DiskTotalGB, "used_gb": ag.DiskUsedGB}
	}
	cpu = nil
	if ag.CPUCores > 0 {
		cpu = map[string]any{"cores": ag.CPUCores, "percent": ag.CPUPct, "load1": ag.Load1}
	}
	return cpu, mem, disk
}

func handleUIAgentsList(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	agents, err := h.ListAgents()
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "list agents"}, err.Error())
		return
	}
	out := []map[string]any{}
	for _, a := range agents {
		out = append(out, map[string]any{
			"id": a.ID, "name": a.Name, "role": a.Role,
			"status":  uiAgentStatus(a),
			"version": a.Version, "arch": a.Arch,
			"last_seen": a.LastSeen,
			"server":    h.uiServerRef(a.ServerID),
			// UI-API-REQUESTS #3 (decided: option b): an UNLINKED agent is
			// invisible in the per-datacenter views by design (bytes need
			// a datacenter to land in); the UI shows the "link this
			// agent" nudge when linked=false.
			"linked": a.ServerID != 0,
		})
	}
	// BACKEND-28: the rows carry the SAME cpu/mem/disk views the §2.4
	// detail serves. Both accepted agents pages render their resource
	// columns from this list; the store rows already hold the heartbeat
	// values, so this enrichment costs no extra queries on the poll.
	for i := range out {
		out[i]["cpu"], out[i]["mem"], out[i]["disk"] = uiAgentResourceViews(agents[i])
	}
	writeJSON(w, map[string]any{"agents": out})
}

func uiAgentStatus(a store.Agent) string {
	if uiAgentOnline(a) {
		return "online"
	}
	return "offline"
}

func (h uiStoreHandle) uiServerRef(serverID int64) any {
	if serverID == 0 {
		return nil
	}
	sv, err := h.GetServer(serverID)
	if err != nil {
		return nil
	}
	dcName, dcEn := "", ""
	if sv.DatacenterID != 0 {
		if dc, err := h.GetDatacenter(sv.DatacenterID); err == nil {
			dcName = dc.Name
			dcEn = dc.DatacenterEN // phase 26 §9: English display name (empty = unset)
		}
	}
	return map[string]any{"id": sv.ID, "name": sv.Name, "role": sv.Role,
		"datacenter": dcName, "datacenter_en": dcEn}
}

func handleUIAgentDetail(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	ag, err := h.GetAgent(id)
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": id}, "")
		return
	}
	cpu, mem, disk := uiAgentResourceViews(ag)
	effective := ag.PublicIP
	if ag.PublicIPOverride != "" {
		effective = ag.PublicIPOverride
	}
	// The agent's datacenter (phase 26 §9): derived from its linked server's
	// datacenter exactly like the agents rows' a.server.datacenter; empty
	// for an unlinked agent.
	dcName, dcEn := "", ""
	if ref := h.uiServerRef(ag.ServerID); ref != nil {
		if m, ok := ref.(map[string]any); ok {
			dcName, _ = m["datacenter"].(string)
			dcEn, _ = m["datacenter_en"].(string)
		}
	}

	// Topologies this agent is a member of, via its server.
	topos := h.uiAgentTopologies(h.Store, ag, false)

	// Recent per-tunnel errors on this agent's tunnels (translated).
	recent := []map[string]any{}
	if ag.ServerID != 0 {
		tunnels, _ := h.TunnelsForServer(ag.ServerID)
		for _, t := range tunnels {
			events, _ := h.TunnelEvents(t.ID, 20)
			for _, ev := range events {
				if ev.Kind != "error" {
					continue
				}
				code := ev.Code
				if c := classifyAgentError(ev.Text); c != "" {
					code = c
				}
				recent = append(recent, map[string]any{"at": ev.At, "code": code, "text": ev.Text})
			}
			if len(recent) >= 10 {
				break
			}
		}
	}

	writeJSON(w, map[string]any{
		"agent": map[string]any{
			"id": ag.ID, "name": ag.Name, "role": ag.Role,
			"linked":  ag.ServerID != 0,
			"status":  uiAgentStatus(ag),
			"version": ag.Version, "arch": ag.Arch, "os": ag.OS,
			"hostname":            ag.Hostname,
			"last_seen":           ag.LastSeen,
			"applied_revision":    ag.AppliedRevision,
			"public_ip":           ag.PublicIP,
			"public_ip_override":  nilIfEmpty(ag.PublicIPOverride),
			"effective_public_ip": effective,
			"cpu":                 cpu, "mem": mem, "disk": disk,
			"uptime_s": ag.UptimeS, "virt": nilIfEmpty(ag.Virt),
			"datacenter": dcName, "datacenter_en": dcEn,
		},
		"topologies":    topos,
		"engines":       h.uiEngineList(ag, h.uiCatalogOf(ag.ID)),
		"recent_errors": recent,
	})
}

// uiAgentTopologies lists the topologies the agent's server belongs to.
// withTunnels adds the per-topology tunnel rows (the detail view).
func (h uiStoreHandle) uiAgentTopologies(s *store.Store, ag store.Agent, withTunnels bool) []map[string]any {
	out := []map[string]any{}
	if ag.ServerID == 0 {
		return out
	}
	members, err := h.TopologyMemberships(ag.ServerID)
	if err != nil || len(members) == 0 {
		return out
	}
	verdicts := uiTunnelVerdicts()
	lat := uiLatencyMap()
	now := timeNowUnix()
	for _, topoID := range members {
		topo, err := h.GetTopology(topoID)
		if err != nil {
			continue
		}
		memberServers, _ := h.TopologyMembers(topoID)
		peers := []map[string]any{}
		tunnelCount := 0
		for _, sid := range memberServers {
			if sid == ag.ServerID {
				continue
			}
			if ref := h.uiServerRef(sid); ref != nil {
				peers = append(peers, ref.(map[string]any))
			}
		}
		entry := map[string]any{
			"id": topo.ID, "name": topo.Name, "my_role": ag.Role,
			"my_server": h.uiServerRef(ag.ServerID),
			"peers":     peers, "enabled": topo.Enabled,
		}
		if withTunnels {
			tunnels, _ := h.ListTunnels(topoID)
			rows := []map[string]any{}
			pcts := h.uiResolvedPercents(topoID, tunnels)
			for _, t := range tunnels {
				if t.LocalServerID != ag.ServerID && t.RemoteServerID != ag.ServerID {
					continue
				}
				tunnelCount++
				rows = append(rows, h.uiTunnelRow(s, topo, t, verdicts, now, pcts, lat))
			}
			entry["tunnel_count"] = tunnelCount
			entry["tunnels"] = rows
		} else {
			tunnels, _ := h.ListTunnels(topoID)
			for _, t := range tunnels {
				if t.LocalServerID == ag.ServerID || t.RemoteServerID == ag.ServerID {
					tunnelCount++
				}
			}
			entry["tunnel_count"] = tunnelCount
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["name"].(string) < out[j]["name"].(string) })
	return out
}

func handleUIAgentTopologies(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	ag, err := h.GetAgent(r.PathValue("id"))
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": r.PathValue("id")}, "")
		return
	}
	writeJSON(w, map[string]any{
		"agent_id":   ag.ID,
		"topologies": h.uiAgentTopologies(h.Store, ag, true),
	})
}

func handleUIAgentEngines(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	ag, err := h.GetAgent(r.PathValue("id"))
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": r.PathValue("id")}, "")
		return
	}
	list := h.uiEngineList(ag, h.uiCatalogOf(ag.ID))
	// Enrich with artifact availability + can_enable/block_reason.
	artifacts, _ := h.ListEngineArtifacts()
	arch := ag.Arch
	if arch == "" {
		arch = "amd64" // the panel build default; honest when unreported
	}
	for _, e := range list {
		engine := e["engine"].(string)
		e["artifact"] = nil
		e["can_enable"] = true
		e["block_reason"] = nil
		if !uiArtifactEngines[engine] {
			continue // kernel tooling / distro packages / lab engines need no artifact
		}
		for _, a := range artifacts {
			if a.Engine == engine && a.Goarch == arch {
				e["artifact"] = map[string]any{
					"version": a.Version, "sha256": a.SHA256, "uploaded_at": a.UploadedAt,
				}
				break
			}
		}
		if e["artifact"] == nil && !e["desired_enabled"].(bool) {
			e["can_enable"] = false
			e["block_reason"] = "no signed artifact for arch " + arch
		}
	}
	writeJSON(w, map[string]any{"agent_id": ag.ID, "arch": arch, "engines": list})
}

// handleUIEngineEnable / handleUIEngineDisable implement the
// enable-before-use flow (requirement 5): desired flag + immediate
// compile+push; enable validates the artifact for the agent's arch
// (non-kernel engines), disable refuses while enabled tunnels exist.
//
// Phase 31 (contract §2.6, UI-API-REQUESTS §10.4): enable of an ALREADY
// desired-enabled artifact engine is the Update re-POST. An engine with
// tunnels upgrades through the compile-time version stamp (the restart
// re-resolves the manifest); an IDLE installed engine has nothing to
// restart, so a stale one re-syncs through a disable→enable cycle — the
// agent uninstalls the idle engine and reinstalls from the latest
// artifact. The cycle never runs while enabled tunnels exist and is
// skipped honestly when the agent is offline or versions are unknown.
func handleUIEngineEnable(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	agentID, engine := r.PathValue("id"), r.PathValue("engine")
	ag, err := h.GetAgent(agentID)
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": agentID}, "")
		return
	}
	if !knownEngineName(h.Store, agentID, engine) {
		uiErrWrite(w, r, "E-ENGINE-UNKNOWN", map[string]any{"engine": engine, "agent": agentID}, "")
		return
	}
	if uiArtifactEngines[engine] {
		arch := agentArchForArtifacts(ag)
		artifacts, _ := h.ListEngineArtifacts()
		found := false
		for _, a := range artifacts {
			if a.Engine == engine && a.Goarch == arch {
				found = true
				break
			}
		}
		if !found {
			uiErrWrite(w, r, "E-ENGINE-NO-ARTIFACT",
				map[string]any{"engine": engine, "agent": agentID, "arch": arch}, "")
			return
		}
	}
	var push map[string]any
	update := ""
	cycled := false
	if desired, _ := h.AgentEngineDesired(agentID, engine); desired && uiArtifactEngines[engine] {
		latest := latestArtifactVersionFor(h.Store, engine, agentArchForArtifacts(ag))
		if latest != "" && !uiEngineHasEnabledTunnel(h.Store, ag, engine) &&
			uiEngineStaleInstalled(h.Store, agentID, engine, latest) {
			// The cycle needs a LIVE v2 session: both pushes must actually
			// reach the agent (a durable pending is not enough — the disable
			// document would be overwritten by the enable one before hello).
			if uiEngineLiveV2Session(agentID) >= protocol.ProtoVersionV2 {
				if err := h.SetAgentEngineDesired(agentID, engine, false); err != nil {
					uiErrWrite(w, r, "E-STORE", map[string]any{"op": "engine enable"}, err.Error())
					return
				}
				pre := uiPushAgent(w, r, h.Store, agentID)
				if pushed, _ := pre["pushed"].(bool); pushed && pre["error"] == nil {
					if err := h.SetAgentEngineDesired(agentID, engine, true); err != nil {
						// Revert: the operator asked for ENABLED — the store
						// must not keep the engine disabled after a failure.
						_ = h.SetAgentEngineDesired(agentID, engine, true)
						_ = uiPushAgent(w, r, h.Store, agentID)
						uiErrWrite(w, r, "E-STORE", map[string]any{"op": "engine enable"}, err.Error())
						return
					}
					push = uiPushAgent(w, r, h.Store, agentID)
					update = "cycled"
					cycled = true
				}
			}
			if !cycled {
				// Offline, tunnel-less compile skip, or a session that died
				// mid-cycle: restore the desired-enabled flag (the flow below
				// recompiles and queues the ENABLED document — overwriting any
				// half-delivered disable) and say the update did not run.
				_ = h.SetAgentEngineDesired(agentID, engine, true)
				update = "skipped"
			}
		}
	}
	if !cycled {
		if err := h.SetAgentEngineDesired(agentID, engine, true); err != nil {
			uiErrWrite(w, r, "E-STORE", map[string]any{"op": "engine enable"}, err.Error())
			return
		}
		push = uiPushAgent(w, r, h.Store, agentID)
	}
	resp := map[string]any{
		"status": "ok", "engine": engine, "desired": true, "agent": agentID, "push": push,
	}
	if update != "" {
		resp["update"] = update
	}
	writeJSON(w, resp)
}

// uiEngineLiveV2Session reports the live agent session's negotiated
// protocol version (0 = no session). Package var: tests stub it.
var uiEngineLiveV2Session = agentHubSessionVersion

// uiEngineHasEnabledTunnel: does the agent have enabled tunnels of the
// engine? Unknown (store error) counts as YES — the update cycle must
// never risk stopping live tunnels.
func uiEngineHasEnabledTunnel(s *store.Store, ag store.Agent, engine string) bool {
	if ag.ServerID == 0 {
		return false
	}
	tunnels, err := s.TunnelsForServer(ag.ServerID)
	if err != nil {
		return true
	}
	for _, t := range tunnels {
		if t.Enabled && t.Engine == engine {
			return true
		}
	}
	return false
}

// uiEngineStaleInstalled: the agent's last report says the engine is
// installed with a version that differs from the latest artifact for its
// arch. Unknown/empty reported versions never count as stale (no churn
// without evidence).
func uiEngineStaleInstalled(s *store.Store, agentID, engine, latest string) bool {
	rows, err := s.AgentEngines(agentID)
	if err != nil {
		return false
	}
	for _, e := range rows {
		if e.Engine == engine {
			return e.Installed && e.Version != "" && e.Version != latest
		}
	}
	return false
}

func handleUIEngineDisable(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	agentID, engine := r.PathValue("id"), r.PathValue("engine")
	ag, err := h.GetAgent(agentID)
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": agentID}, "")
		return
	}
	_ = ag
	// Refuse while enabled tunnels of this engine exist on this agent.
	if ag.ServerID != 0 {
		tunnels, _ := h.TunnelsForServer(ag.ServerID)
		var blocking []string
		for _, t := range tunnels {
			if t.Enabled && t.Engine == engine {
				blocking = append(blocking, t.TunnelKey)
			}
		}
		if len(blocking) > 0 {
			uiErrWrite(w, r, "E-VALIDATION",
				map[string]any{"agent": agentID, "engine": engine, "tunnels": blocking, "field": "tunnels"},
				"disable the engine's tunnels first")
			return
		}
	}
	if err := h.SetAgentEngineDesired(agentID, engine, false); err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "engine disable"}, err.Error())
		return
	}
	push := uiPushAgent(w, r, h.Store, agentID)
	writeJSON(w, map[string]any{
		"status": "ok", "engine": engine, "desired": false, "agent": agentID, "push": push,
	})
}

// uiPushAgent recompiles + pushes the agent's desired state; the result
// is the compileAndPushAgent map (pushed=false = queued for the next
// connect — enabling while offline is allowed and queued).
func uiPushAgent(w http.ResponseWriter, r *http.Request, st *store.Store, agentID string) map[string]any {
	res, err := compileAndPushAgent(st, agentID)
	if err != nil {
		uiErrWrite(w, r, "E-AGENT-OFFLINE", map[string]any{"agent": agentID}, err.Error())
		return map[string]any{"pushed": false, "error": err.Error()}
	}
	return res
}

// knownEngineName: the engine is known from the agent's catalog, the
// stored rows, or the built-in set.
func knownEngineName(s *store.Store, agentID, engine string) bool {
	switch engine {
	case "matrix", "wireguard", "frp", "backhaul", "xray", "chisel", "wst", "gost", "rathole",
		"wireguard@udp2raw":
		return true
	}
	rows, _ := s.AgentEngines(agentID)
	for _, row := range rows {
		if row.Engine == engine {
			return true
		}
	}
	return false
}

// handleUIAgentPublicIP sets/clears the manual public-IP override.
func handleUIAgentPublicIP(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	agentID := r.PathValue("id")
	ag, err := h.GetAgent(agentID)
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": agentID}, "")
		return
	}
	var body struct {
		PublicIP string `json:"public_ip"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "public_ip"}, err.Error())
		return
	}
	if body.PublicIP != "" && !validIPOrHost(body.PublicIP) {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "public_ip"}, "not an IP or hostname")
		return
	}
	if err := h.SetAgentPublicIPOverride(agentID, body.PublicIP); err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "public-ip"}, err.Error())
		return
	}
	effective := ag.PublicIP
	if body.PublicIP != "" {
		effective = body.PublicIP
	}
	writeJSON(w, map[string]any{"status": "ok", "agent": agentID, "effective_public_ip": effective})
}

func validIPOrHost(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, part := range splitHostParts(s) {
		if part == "" {
			return false
		}
		for _, c := range part {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '.' || c == ':') {
				return false
			}
		}
	}
	return true
}

func splitHostParts(s string) []string {
	out := []string{}
	cur := ""
	for _, c := range s {
		if c == '.' || c == ':' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(c)
	}
	return append(out, cur)
}

func timeNowUnix() int64 { return time.Now().Unix() }

func atoiDefault(s string, d int64) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return d
	}
	return v
}
