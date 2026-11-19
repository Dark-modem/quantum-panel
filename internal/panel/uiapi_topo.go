package panel

// uiapi_topo.go — topology detail, tunnel detail and the per-TUNNEL
// weight endpoint (Phase 11 contract §2.8–2.10) plus the phase-14
// per-member PORT view (§2.11).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// uiTunnelRow is the shared per-tunnel summary row (topology detail and
// agent topologies).
// uiLatencyMap: one locked pass over the runtime for every fresh
// tunnel's latency (the per-row scans were an N+1 hotspot in the
// phase-12 soak).
func uiLatencyMap() map[string]float64 {
	out := map[string]float64{}
	now := time.Now().Unix()
	meshRuntimeMu.Lock()
	for agentID, rts := range meshRuntimeBy {
		if now-meshRuntimeSeen[agentID] > 90 {
			continue
		}
		for _, tr := range rts {
			if tr.LatencyMs > 0 {
				out[tr.TunnelID] = tr.LatencyMs
			}
		}
	}
	meshRuntimeMu.Unlock()
	return out
}

// uiTunnelRow renders one row from PRECOMPUTED shares (pcts) and the
// shared latency map — per-row recomputation was the topology detail's
// N+1 hotspot (phase-12 soak).
func (h uiStoreHandle) uiTunnelRow(s *store.Store, topo store.Topology, t store.Tunnel,
	verdicts map[string]string, now int64, pcts map[int64]float64, lat map[string]float64) map[string]any {
	verdict := "unknown"
	if !t.Enabled {
		verdict = "disabled"
	} else if v, ok := verdicts[t.TunnelKey]; ok {
		verdict = v
	}
	w, _ := h.WeightFor(topo.ID, t.LocalServerID, t.RemoteServerID, t.ID)
	usage, _, err := h.RangeUsageDetailed([]int64{t.ID}, now-now%86400, now)
	if err != nil {
		usage = store.Usage{}
	}
	var latency any
	if l, ok := lat[t.TunnelKey]; ok {
		latency = l
	}
	row := map[string]any{
		"id": t.ID, "tunnel_key": t.TunnelKey, "engine": t.Engine, "enabled": t.Enabled,
		"local_server":  h.uiServerRef(t.LocalServerID),
		"remote_server": h.uiServerRef(t.RemoteServerID),
		"state":         verdict,
		"weight":        w,
		"traffic_today": map[string]any{
			"wire_rx_bytes":    usage.WireRx,
			"wire_tx_bytes":    usage.WireTx,
			"payload_rx_bytes": usage.PayloadRx,
			"payload_tx_bytes": usage.PayloadTx,
		},
		"latency_ms": latency,
	}
	// Only connected tunnels carry weight — a dropped tunnel shows no
	// resolved share and the survivors re-normalize without it.
	if p := pcts[t.ID]; p > 0 {
		row["resolved_percent"] = p
	}
	return row
}

func mustListTunnels(s *store.Store, topoID int64) []store.Tunnel {
	t, _ := s.ListTunnels(topoID)
	return t
}

func handleUITopologyDetail(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	topoID := atoiDefault(r.PathValue("id"), 0)
	topo, err := h.GetTopology(topoID)
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"topology": r.PathValue("id")}, "")
		return
	}
	now := timeNowUnix()
	verdicts := uiVerdictsFn()
	lat := uiLatencyMap()
	members, _ := h.TopologyMembers(topoID)
	tunnels := mustListTunnels(h.Store, topoID)
	pcts := h.uiResolvedPercents(topoID, tunnels, verdicts) // once, not per row

	iranRows, foreignRows := []map[string]any{}, []map[string]any{}
	allRows := []map[string]any{}
	seenTunnels := map[int64]bool{}
	health := map[string]int{"healthy": 0, "degraded": 0, "down": 0}
	var ids []int64
	for _, t := range tunnels {
		ids = append(ids, t.ID)
	}
	_, billable, _ := h.RangeUsageDetailed(ids, now-now%86400, now)
	for _, sid := range members {
		sv, err := h.GetServer(sid)
		if err != nil {
			continue
		}
		agentRef := any(nil)
		if ag, err := h.AgentByServer(sid); err == nil {
			agentRef = map[string]any{"id": ag.ID, "status": uiAgentStatus(ag)}
		}
		rows := []map[string]any{}
		for _, t := range tunnels {
			if t.LocalServerID != sid && t.RemoteServerID != sid {
				continue
			}
			row := h.uiTunnelRow(h.Store, topo, t, verdicts, now, pcts, lat)
			rows = append(rows, row)
			// The flat `tunnels` list must hold each tunnel ONCE; a tunnel
			// between two members matches both ends above (phase-11 UI
			// probe: 6 tunnels answered as 12). Per-member `rows` keep both
			// perspectives by design.
			if !seenTunnels[t.ID] {
				seenTunnels[t.ID] = true
				allRows = append(allRows, row)
				if t.Enabled {
					switch row["state"] {
					case "healthy":
						health["healthy"]++
					case "degraded":
						health["degraded"]++
					default:
						health["down"]++
					}
				}
			}
		}
		entry := map[string]any{
			"id": sv.ID, "name": sv.Name, "agent": agentRef, "tunnels": rows,
		}
		// phase 26 §9: the member entry carries its datacenter (bilingual) —
		// the same server-ref shape the tunnel rows use.
		if ref := h.uiServerRef(sv.ID); ref != nil {
			if m, ok := ref.(map[string]any); ok {
				entry["datacenter"] = m["datacenter"]
				entry["datacenter_en"] = m["datacenter_en"]
			}
		}
		if sv.Role == "iran" {
			iranRows = append(iranRows, entry)
		} else {
			foreignRows = append(foreignRows, entry)
		}
	}
	// Tunnels-page path column: each end's flag comes from the agent
	// enrolled against that server (the servers table carries no country),
	// and the deep-link needs the agent id. Stamped onto the ref maps so
	// the frontend reads one shape everywhere.
	agCountry := map[int64]string{}
	agBySrv := map[int64]string{}
	if ags, aerr := h.ListAgents(); aerr == nil {
		for _, a := range ags {
			if a.ServerID != 0 {
				if a.Country != "" {
					agCountry[a.ServerID] = a.Country
				}
				agBySrv[a.ServerID] = a.ID
			}
		}
	}
	for _, row := range allRows {
		for _, side := range []string{"local_server", "remote_server"} {
			if m, ok := row[side].(map[string]any); ok {
				if id, ok2 := m["id"].(int64); ok2 {
					m["country"] = agCountry[id]
					if aid := agBySrv[id]; aid != "" {
						m["agent_id"] = aid
					}
				}
			}
		}
	}
	writeJSON(w, map[string]any{
		"topology": map[string]any{"id": topo.ID, "name": topo.Name, "enabled": topo.Enabled},
		"members": map[string]any{
			"iran":    iranRows,
			"foreign": foreignRows,
		},
		"tunnels": allRows,
		"totals": map[string]any{
			"tunnels": map[string]int{
				"total": len(allRows), "up": health["healthy"],
				"degraded": health["degraded"], "down": health["down"],
			},
			"traffic_today": map[string]any{
				"billable_download_bytes": billable.WireRx,
				"upload_bytes":            billable.WireTx,
				"payload_download_bytes":  billable.PayloadRx,
				"payload_upload_bytes":    billable.PayloadTx,
			},
			"health": health,
		},
	})
}

func handleUITunnelDetail(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	key := r.PathValue("key")
	t, err := h.GetTunnelByKey(key)
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"tunnel": key}, "")
		return
	}
	topo, err := h.GetTopology(t.TopologyID)
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "topology"}, err.Error())
		return
	}
	now := timeNowUnix()
	verdicts := uiVerdictsFn()

	// NOW: state, proc metrics, rates, latency/loss, uptime.
	state := "unknown"
	if !t.Enabled {
		state = "disabled"
	} else if v, ok := verdicts[key]; ok {
		state = v
	}
	cpu, rss := any(nil), any(nil)
	if tp, err := h.TunnelProcFor([]int64{t.ID}); err == nil {
		if p, ok := tp[t.ID]; ok {
			cpu, rss = p.CPUPct, p.RSSBytes
		}
	}
	latency, loss, uptime := any(nil), any(nil), any(nil)
	var lastErr any
	meshRuntimeMu.Lock()
	for agentID, rts := range meshRuntimeBy {
		if time.Now().Unix()-meshRuntimeSeen[agentID] > 90 {
			continue
		}
		for _, tr := range rts {
			if tr.TunnelID != key {
				continue
			}
			if tr.LatencyMs > 0 {
				latency = tr.LatencyMs
			}
			if tr.LossPct > 0 {
				loss = tr.LossPct
			}
			if tr.UptimeS > 0 {
				uptime = tr.UptimeS
			}
			if tr.LastError != "" {
				lastErr = redactErrorText(tr.LastError)
			}
		}
	}
	meshRuntimeMu.Unlock()
	rates := h.uiTunnelRates(t.ID)

	// Totals + ratios.
	totals := h.uiTrafficTotals([]int64{t.ID}, now)
	topoTunnels := mustListTunnels(h.Store, topo.ID)
	var topoIDs []int64
	for _, tt := range topoTunnels {
		topoIDs = append(topoIDs, tt.ID)
	}
	_, topoBillable := mustRange(h.Store, topoIDs, now-now%86400, now)
	_, myBillable := mustRange(h.Store, []int64{t.ID}, now-now%86400, now)
	topoShare := any(nil)
	if topoBillable.WireRx > 0 {
		topoShare = float64(myBillable.WireRx) / float64(topoBillable.WireRx) * 100
	}
	overhead := any(nil)
	allToday, _ := mustRange(h.Store, []int64{t.ID}, now-now%86400, now)
	if allToday.PayloadRx > 0 && allToday.WireRx > 0 {
		overhead = float64(allToday.WireRx) / float64(allToday.PayloadRx)
	}

	// History: hour buckets for 24h, day buckets for 7d/30d.
	rangeSel := r.URL.Query().Get("range")
	var from, bucket int64
	switch rangeSel {
	case "7d":
		from, bucket = now-7*86400, 86400
	case "30d":
		from, bucket = now-30*86400, 86400
	default:
		rangeSel = "24h"
		from, bucket = now-86400, 3600
	}
	points := []map[string]any{}
	if series, err := h.UsageSeriesDetailed(t.ID, from, now, bucket); err == nil {
		for _, p := range series {
			points = append(points, map[string]any{
				"t":             p.T,
				"wire_rx_bytes": p.WireRx, "wire_tx_bytes": p.WireTx,
				"payload_rx_bytes": p.PayloadRx, "payload_tx_bytes": p.PayloadTx,
			})
		}
	}

	// Events + recent errors.
	events, _ := h.TunnelEvents(t.ID, 50)
	eventRows := []map[string]any{}
	errRows := []map[string]any{}
	for _, ev := range events {
		row := map[string]any{"at": ev.At, "kind": ev.Kind}
		if ev.Kind == "state" {
			row["from"] = ev.FromState
			row["to"] = ev.ToState
		} else {
			code := ev.Code
			if c := classifyAgentError(ev.Text); c != "" {
				code = c
			}
			row["code"] = code
			row["text"] = ev.Text
			errRows = append(errRows, row)
		}
		eventRows = append(eventRows, row)
	}

	// The detail page's endpoint cards need the same real-relation stamps the
	// topology rows carry: the agent country (flag) and agent id (deep link).
	// The header strip also reports the tunnel's weight and resolved share.
	stampRef := func(serverID int64) map[string]any {
		ref, _ := h.uiServerRef(serverID).(map[string]any)
		if ref == nil {
			return nil
		}
		if ag, aerr := h.AgentByServer(serverID); aerr == nil {
			ref["agent_id"] = ag.ID
			ref["country"] = ag.Country
		}
		return ref
	}
	tw, _ := h.WeightFor(topo.ID, t.LocalServerID, t.RemoteServerID, t.ID)
	resolved := any(nil)
	if p := h.uiResolvedPercents(topo.ID, mustListTunnels(h.Store, topo.ID), verdicts)[t.ID]; p > 0 {
		resolved = p
	}
	writeJSON(w, map[string]any{
		"tunnel": map[string]any{
			"id": t.ID, "tunnel_key": t.TunnelKey,
			"topology":      map[string]any{"id": topo.ID, "name": topo.Name},
			"local_server":  stampRef(t.LocalServerID),
			"remote_server": stampRef(t.RemoteServerID),
			"engine":        t.Engine, "enabled": t.Enabled, "created_at": t.CreatedAt,
			"weight": tw, "resolved_percent": resolved,
		},
		"now": map[string]any{
			"state": state, "cpu_percent": cpu, "rss_bytes": rss,
			"latency_ms": latency, "loss_pct": loss, "uptime_s": uptime,
			"rate":       rates,
			"last_error": lastErr,
		},
		"totals": totals,
		"ratios": map[string]any{
			"topology_share_pct": topoShare,
			"overhead_ratio":     overhead,
		},
		"history": map[string]any{
			"range":        rangeSel,
			"granularity":  map[bool]string{true: "hour", false: "day"}[bucket == 3600],
			"points":       points,
			"points_count": len(points),
		},
		"events":        eventRows,
		"errors_recent": errRows,
	})
}

func mustRange(s *store.Store, ids []int64, from, to int64) (store.Usage, store.Usage) {
	h := uiStoreHandle{s}
	all, iran, err := h.RangeUsageDetailed(ids, from, to)
	if err != nil {
		return store.Usage{}, store.Usage{}
	}
	return all, iran
}

// uiTunnelRates computes the current per-direction rate from the two
// newest samples (any reporter; the newest wins, gaps > 10 min → null).
func (h uiStoreHandle) uiTunnelRates(tunnelID int64) map[string]any {
	zero := map[string]any{"wire_rx_bps": nil, "wire_tx_bps": nil, "payload_rx_bps": nil, "payload_tx_bps": nil}
	samples, err := h.RecentSamples(tunnelID, 8)
	if err != nil || len(samples) < 2 {
		return zero
	}
	// Newest-first; find the newest pair from the SAME reporter within 10 min.
	newest := samples[0]
	for _, older := range samples[1:] {
		if older.AgentID != newest.AgentID {
			continue
		}
		dt := newest.At - older.At
		if dt <= 0 || dt > 600 {
			return zero
		}
		rate := func(cur, prev uint64, known bool) any {
			if !known || cur < prev {
				return nil
			}
			return float64(cur-prev) / float64(dt)
		}
		return map[string]any{
			"wire_rx_bps":    rate(newest.WireRx, older.WireRx, newest.CountersKnown && older.CountersKnown),
			"wire_tx_bps":    rate(newest.WireTx, older.WireTx, newest.CountersKnown && older.CountersKnown),
			"payload_rx_bps": rate(newest.PayloadRx, older.PayloadRx, newest.PayloadKnown && older.PayloadKnown),
			"payload_tx_bps": rate(newest.PayloadTx, older.PayloadTx, newest.PayloadKnown && older.PayloadKnown),
		}
	}
	return zero
}

// handleUITunnelWeight writes a PER-TUNNEL weight (requirement 8): the
// fix for the phase-9 pair-level write; the wizard must send the key.
func handleUITunnelWeight(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	topoID := atoiDefault(r.PathValue("id"), 0)
	key := r.PathValue("key")
	topo, err := h.GetTopology(topoID)
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"topology": r.PathValue("id")}, "")
		return
	}
	t, err := h.GetTunnelByKey(key)
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"tunnel": key}, "")
		return
	}
	if t.TopologyID != topo.ID {
		uiErrWrite(w, r, "E-WEIGHT-CONFLICT",
			map[string]any{"tunnel": key, "topology": topo.Name}, "")
		return
	}
	var body struct {
		Weight *int `json:"weight"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Weight == nil {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "weight"}, "body must be {\"weight\": 0-1000}")
		return
	}
	weight := *body.Weight
	if weight < 0 || weight > 1000 {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "weight"}, "0-1000")
		return
	}
	if err := h.SetWeightPolicy(topo.ID, t.LocalServerID, t.RemoteServerID, t.ID, weight); err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "weight"}, err.Error())
		return
	}
	pcts := h.uiResolvedPercents(topo.ID, mustListTunnels(h.Store, topo.ID), uiVerdictsFn())
	writeJSON(w, map[string]any{
		"status": "ok", "tunnel": key, "weight": weight,
		"resolved_percent": pcts[t.ID],
	})
}

var _ = time.Now

// ---- phase 14: topology port view (contract §2.11) ---------------------

// handleUITopologyPorts is the per-member port view of one topology. For
// every member server it answers three questions the port workflow asks:
//   - allocated: what the panel's ledger holds (who owns each port);
//   - reserved: the full "taken" set the allocator enforces (well-known
//     ports, panel listeners, booked ledger rows, declared forward rules
//     and any extra SSH ports) — the same set forward-create refuses;
//   - observed: what the member's linked agent LAST reported listening
//     (agent_listening; a server whose agent never reported simply shows
//     an empty list, it is not an error).
//
// List output is stable (members by id, ports ascending) so the UI can
// diff consecutive polls.
func handleUITopologyPorts(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	topoID := atoiDefault(r.PathValue("id"), 0)
	if _, err := h.GetTopology(topoID); err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"topology": r.PathValue("id")}, "")
		return
	}
	members, err := h.TopologyMembers(topoID)
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "topology members"}, err.Error())
		return
	}
	// One join query for the whole topology, not per-member (the same N+1
	// rule the phase-12 soak set for the detail views).
	listening, err := h.ListAgentListening(members)
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "agent listening"}, err.Error())
		return
	}
	servers := []map[string]any{}
	for _, sid := range members {
		sv, err := h.GetServer(sid)
		if err != nil {
			continue // stale member row (server deleted): skip, don't fail the view
		}
		allocs, err := h.ListPortAllocations(sid)
		if err != nil {
			uiErrWrite(w, r, "E-STORE", map[string]any{"op": "port allocations", "server": sid}, err.Error())
			return
		}
		allocated := []map[string]any{}
		for _, a := range allocs {
			allocated = append(allocated, map[string]any{
				"port": a.Port, "proto": a.Proto,
				"purpose": a.Purpose, "owner_key": a.OwnerKey,
			})
		}
		reservedSet, err := reservedPortsFor(h.Store, sid)
		if err != nil {
			uiErrWrite(w, r, "E-STORE", map[string]any{"op": "reserved ports", "server": sid}, err.Error())
			return
		}
		reserved := make([]int, 0, len(reservedSet))
		for p := range reservedSet {
			reserved = append(reserved, p)
		}
		sort.Ints(reserved)
		observed := []map[string]any{}
		for _, l := range listening[sid] {
			observed = append(observed, map[string]any{"proto": l.Proto, "port": l.Port, "proc": l.Proc})
		}
		servers = append(servers, map[string]any{
			"server_id": sid, "name": sv.Name,
			"allocated": allocated, "reserved": reserved, "observed": observed,
		})
	}
	writeJSON(w, map[string]any{"topology_id": topoID, "servers": servers})
}

// ---- topology members: add/remove AGENTS (=their servers) on a topology --
// The owner rule: agents ARE the servers; an agent may sit in several
// topologies (many-to-many), while a tunnel belongs to exactly one.

// uiTopoStoreID returns the mesh-store topology id for a panel topology name,
// creating the row on first use so panel topologies gain memberships too.
func uiTopoStoreID(s *store.Store, name string) (int64, error) {
	if topos, err := s.ListTopologies(); err == nil {
		for _, tp := range topos {
			if strings.EqualFold(tp.Name, name) {
				return tp.ID, nil
			}
		}
	}
	return s.UpsertTopology(store.Topology{Name: name, Enabled: true})
}

// uiTopoMemberBody is the add/remove members body (agent-scoped: the agent's
// linked server is what joins a topology).
type uiTopoMemberBody struct {
	AgentID string `json:"agent_id"`
}

func handleUITopoMemberAdd(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if strings.TrimSpace(name) == "" || len(name) > 120 {
		uiErrWrite(w, r, "E-IN-04", map[string]any{"op": "topology member add"}, "invalid topology name")
		return
	}
	var body uiTopoMemberBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		uiErrWrite(w, r, "E-IN-06", map[string]any{"op": "topology member add"}, "body must be a JSON object with agent_id")
		return
	}
	agents, err := h.ListAgents()
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "topology member add"}, err.Error())
		return
	}
	var ag *store.Agent
	for i := range agents {
		if agents[i].ID == body.AgentID {
			ag = &agents[i]
			break
		}
	}
	if ag == nil || ag.ServerID == 0 {
		uiErrWrite(w, r, "E-IN-06", map[string]any{"op": "topology member add", "agent": body.AgentID}, "agent not found or has no linked server")
		return
	}
	topoID, err := uiTopoStoreID(h.Store, name)
	if err != nil {
		uiErrWrite(w, r, "E-SYS-01", map[string]any{"op": "topology member add"}, err.Error())
		return
	}
	if err := h.AddTopologyMember(topoID, ag.ServerID); err != nil {
		uiErrWrite(w, r, "E-SYS-01", map[string]any{"op": "topology member add"}, err.Error())
		return
	}
	meshAudit("panel", "mesh.topology.member.add", fmt.Sprintf(`{"topology":%q,"agent":%q,"server_id":%d}`, name, body.AgentID, ag.ServerID))
	writeJSON(w, map[string]any{"status": "ok"})
}

func handleUITopoMemberRemove(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	if strings.TrimSpace(name) == "" || len(name) > 120 {
		uiErrWrite(w, r, "E-IN-04", map[string]any{"op": "topology member remove"}, "invalid topology name")
		return
	}
	var body uiTopoMemberBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		uiErrWrite(w, r, "E-IN-06", map[string]any{"op": "topology member remove"}, "body must be a JSON object with agent_id")
		return
	}
	agents, err := h.ListAgents()
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "topology member remove"}, err.Error())
		return
	}
	var ag *store.Agent
	for i := range agents {
		if agents[i].ID == body.AgentID {
			ag = &agents[i]
			break
		}
	}
	if ag == nil || ag.ServerID == 0 {
		uiErrWrite(w, r, "E-IN-06", map[string]any{"op": "topology member remove", "agent": body.AgentID}, "agent not found or has no linked server")
		return
	}
	topoID, err := uiTopoStoreID(h.Store, name)
	if err != nil {
		uiErrWrite(w, r, "E-SYS-01", map[string]any{"op": "topology member remove"}, err.Error())
		return
	}
	if err := h.RemoveTopologyMember(topoID, ag.ServerID); err != nil {
		uiErrWrite(w, r, "E-SYS-01", map[string]any{"op": "topology member remove"}, err.Error())
		return
	}
	meshAudit("panel", "mesh.topology.member.remove", fmt.Sprintf(`{"topology":%q,"agent":%q,"server_id":%d}`, name, body.AgentID, ag.ServerID))
	writeJSON(w, map[string]any{"status": "ok"})
}
