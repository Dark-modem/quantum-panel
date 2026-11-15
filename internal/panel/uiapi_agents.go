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
)

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
	// agent -> topology names (via the agent's server membership)
	topoName := map[int64]string{}
	if topos, err := h.ListTopologies(); err == nil {
		for _, tp := range topos {
			topoName[tp.ID] = tp.Name
		}
	}
	serverTopos := map[int64][]int64{}
	seenSrv := map[int64]bool{}
	for _, a := range agents {
		if a.ServerID == 0 || seenSrv[a.ServerID] {
			continue
		}
		seenSrv[a.ServerID] = true
		if ids, err := h.TopologyMemberships(a.ServerID); err == nil {
			serverTopos[a.ServerID] = ids
		}
	}
	out := []map[string]any{}
	for _, a := range agents {
		var agentTopos []string
		for _, id := range serverTopos[a.ServerID] {
			if nm, ok := topoName[id]; ok {
				agentTopos = append(agentTopos, nm)
			}
		}
		sort.Strings(agentTopos)
		out = append(out, map[string]any{
			"id": a.ID, "name": a.Name, "role": a.Role, "country": a.Country,
			"topologies": agentTopos,
			"status":     uiAgentStatus(a),
			"version":    a.Version, "arch": a.Arch,
			"last_seen": a.LastSeen,
			"server":    h.uiServerRef(a.ServerID),
			// UI-API-REQUESTS #3 (decided: option b): an UNLINKED agent is
			// invisible in the per-datacenter views by design (bytes need
			// a datacenter to land in); the UI shows the "link this
			// agent" nudge when linked=false.
			"linked": a.ServerID != 0,
		})
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
	dcName := ""
	if sv.DatacenterID != 0 {
		if dc, err := h.GetDatacenter(sv.DatacenterID); err == nil {
			dcName = dc.Name
		}
	}
	return map[string]any{"id": sv.ID, "name": sv.Name, "role": sv.Role, "datacenter": dcName}
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
	mem := any(nil)
	if ag.MemTotalMB > 0 {
		mem = map[string]any{"total_mb": ag.MemTotalMB, "used_mb": ag.MemUsedMB}
	}
	disk := any(nil)
	if ag.DiskTotalGB > 0 {
		disk = map[string]any{"total_gb": ag.DiskTotalGB, "used_gb": ag.DiskUsedGB}
	}
	cpu := any(nil)
	if ag.CPUCores > 0 {
		cpu = map[string]any{"cores": ag.CPUCores, "percent": ag.CPUPct, "load1": ag.Load1}
	}
	effective := ag.PublicIP
	if ag.PublicIPOverride != "" {
		effective = ag.PublicIPOverride
	}
	dcName := ""
	if ag.ServerID != 0 {
		if sv, serr := h.GetServer(ag.ServerID); serr == nil && sv.DatacenterID != 0 {
			if dc, derr := h.GetDatacenter(sv.DatacenterID); derr == nil {
				dcName = dc.Name
			}
		}
	}

	// Topologies this agent is a member of, via its server.
	topos := h.uiAgentTopologies(h.Store, ag, false)

	// Tunnels terminating on this agent's server (compact rows, live state).
	tunRows := []map[string]any{}
	// serverID -> agent country (the servers table carries no country; the
	// agent enrolled against it does) — feeds the destination flag cell.
	tnCountry := map[int64]string{}
	if ags, aerr := h.ListAgents(); aerr == nil {
		for _, a2 := range ags {
			if a2.ServerID != 0 {
				tnCountry[a2.ServerID] = a2.Country
			}
		}
	}
	if ag.ServerID != 0 {
		if allTopos, err := h.ListTopologies(); err == nil {
			verdicts := uiVerdictsFn()
			for _, tp := range allTopos {
				tunnels, terr := h.ListTunnels(tp.ID)
				if terr != nil {
					continue
				}
				pcts := h.uiResolvedPercents(tp.ID, tunnels, verdicts)
				lat := uiLatencyMap()
				now := timeNowUnix()
				for _, tn := range tunnels {
					if tn.LocalServerID != ag.ServerID && tn.RemoteServerID != ag.ServerID {
						continue
					}
					state := "unknown"
					if !tn.Enabled {
						state = "disabled"
					} else if v, ok := verdicts[tn.TunnelKey]; ok {
						state = v
					}
					usage, _, uerr := h.RangeUsageDetailed([]int64{tn.ID}, now-now%86400, now)
					var todayBytes uint64
					if uerr == nil {
						todayBytes = usage.WireRx + usage.WireTx
					}
					row := map[string]any{
						"tunnel_key": tn.TunnelKey, "engine": tn.Engine, "topology": tp.Name,
						"local": h.uiServerRef(tn.LocalServerID), "remote": h.uiServerRef(tn.RemoteServerID),
						"country": tnCountry[tn.RemoteServerID],
						"state":   state, "latency_ms": lat[tn.TunnelKey],
						"today_bytes": todayBytes,
					}
					// Only connected tunnels carry weight — a dropped tunnel
					// shows «—» and its share flows to the surviving ones.
					if p := pcts[tn.ID]; p > 0 {
						row["weight_pct"] = p
					}
					tunRows = append(tunRows, row)
				}
			}
		}
	}

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
			"id": ag.ID, "name": ag.Name, "role": ag.Role, "country": ag.Country,
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
			"datacenter": dcName, "cpu_model": nilIfEmpty(ag.CPUModel),
		},
		"topologies":    topos,
		"tunnels":       tunRows,
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
	verdicts := uiVerdictsFn()
	lat := uiLatencyMap()
	now := timeNowUnix()
	// serverID → agent country (the servers table carries no country; the
	// agent enrolled against it does). Feeds the per-side diagram summary.
	agentCountry := map[int64]string{}
	if ags, aerr := h.ListAgents(); aerr == nil {
		for _, a2 := range ags {
			if a2.ServerID != 0 {
				agentCountry[a2.ServerID] = a2.Country
			}
		}
	}
	for _, topoID := range members {
		topo, err := h.GetTopology(topoID)
		if err != nil {
			continue
		}
		memberServers, _ := h.TopologyMembers(topoID)
		// Per-side summary: distinct servers on the agent's side vs the
		// peers' side, and the country representing each side (the client
		// renders the flag; iran sides are always ir).
		myServers, peerServers := 0, 0
		peerRole, peerCountry := "", ""
		for _, sid := range memberServers {
			sv, serr := h.GetServer(sid)
			if serr != nil {
				continue
			}
			if sv.Role == ag.Role {
				myServers++
				continue
			}
			peerServers++
			if peerRole == "" {
				peerRole = sv.Role
			}
			if peerCountry == "" && agentCountry[sid] != "" {
				peerCountry = agentCountry[sid]
			}
		}
		if peerRole == "" {
			if ag.Role == "iran" {
				peerRole = "foreign"
			} else {
				peerRole = "iran"
			}
		}
		if peerRole == "iran" && peerCountry == "" {
			peerCountry = "ir"
		}
		myCountry := ag.Country
		if ag.Role == "iran" {
			myCountry = "ir"
		}
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
			"members":   len(memberServers),
			"my_side":   map[string]any{"role": ag.Role, "servers": myServers, "country": myCountry},
			"peer_side": map[string]any{"role": peerRole, "servers": peerServers, "country": peerCountry},
		}
		if withTunnels {
			tunnels, _ := h.ListTunnels(topoID)
			rows := []map[string]any{}
			pcts := h.uiResolvedPercents(topoID, tunnels, verdicts)
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
		arch := ag.Arch
		if arch == "" {
			arch = "amd64"
		}
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
	if err := h.SetAgentEngineDesired(agentID, engine, true); err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "engine enable"}, err.Error())
		return
	}
	push := uiPushAgent(w, r, h.Store, agentID)
	writeJSON(w, map[string]any{
		"status": "ok", "engine": engine, "desired": true, "agent": agentID, "push": push,
	})
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

// handleUIAgentRename renames an agent (registry + store mirror).
func handleUIAgentRename(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		uiErrWrite(w, r, "E-IN-04", map[string]any{"agent": id}, "bad body")
		return
	}
	name := agentValidText(body.Name, 120)
	if name == "" {
		uiErrWrite(w, r, "E-IN-04", map[string]any{"agent": id}, "name required")
		return
	}
	if err := agentRegistryRename(id, name); err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": id}, err.Error())
		return
	}
	if err := h.SetAgentName(id, name); err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"agent": id}, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true, "name": name})
}

// handleUIAgentDelete removes the agent entirely: registry record (its
// credential) + the store row. A live hub session for a now-unknown agent is
// dropped by the hub on its next message.
func handleUIAgentDelete(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if err := agentRegistryDelete(id); err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": id}, err.Error())
		return
	}
	if err := h.DeleteAgent(id); err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"agent": id}, err.Error())
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
