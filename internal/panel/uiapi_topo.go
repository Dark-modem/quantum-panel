package panel

// uiapi_topo.go — topology detail, tunnel detail and the per-TUNNEL
// weight endpoint (Phase 11 contract §2.8–2.10).

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// uiTunnelRow is the shared per-tunnel summary row (topology detail and
// agent topologies).
func (h uiStoreHandle) uiTunnelRow(s *store.Store, topo store.Topology, t store.Tunnel,
	verdicts map[string]string, now int64) map[string]any {
	verdict := "unknown"
	if !t.Enabled {
		verdict = "disabled"
	} else if v, ok := verdicts[t.TunnelKey]; ok {
		verdict = v
	}
	w, _ := h.WeightFor(topo.ID, t.LocalServerID, t.RemoteServerID, t.ID)
	pcts := h.uiResolvedPercents(topo.ID, mustListTunnels(h.Store, topo.ID))
	usage, _, err := h.RangeUsageDetailed([]int64{t.ID}, now-now%86400, now)
	if err != nil {
		usage = store.Usage{}
	}
	latency := any(nil)
	meshRuntimeMu.Lock()
	for _, rts := range meshRuntimeBy {
		for _, tr := range rts {
			if tr.TunnelID == t.TunnelKey && time.Now().Unix()-meshRuntimeSeen[tr.TunnelID] < 90 {
				latency = tr.LatencyMs
			}
		}
	}
	meshRuntimeMu.Unlock()
	return map[string]any{
		"id": t.ID, "tunnel_key": t.TunnelKey, "engine": t.Engine, "enabled": t.Enabled,
		"local_server":     h.uiServerRef(t.LocalServerID),
		"remote_server":    h.uiServerRef(t.RemoteServerID),
		"state":            verdict,
		"weight":           w,
		"resolved_percent": pcts[t.ID],
		"traffic_today": map[string]any{
			"wire_rx_bytes":    usage.WireRx,
			"wire_tx_bytes":    usage.WireTx,
			"payload_rx_bytes": usage.PayloadRx,
			"payload_tx_bytes": usage.PayloadTx,
		},
		"latency_ms": latency,
	}
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
	verdicts := uiTunnelVerdicts()
	members, _ := h.TopologyMembers(topoID)
	tunnels := mustListTunnels(h.Store, topoID)

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
			row := h.uiTunnelRow(h.Store, topo, t, verdicts, now)
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
		if sv.Role == "iran" {
			iranRows = append(iranRows, entry)
		} else {
			foreignRows = append(foreignRows, entry)
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
	verdicts := uiTunnelVerdicts()

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

	writeJSON(w, map[string]any{
		"tunnel": map[string]any{
			"id": t.ID, "tunnel_key": t.TunnelKey,
			"topology":      map[string]any{"id": topo.ID, "name": topo.Name},
			"local_server":  h.uiServerRef(t.LocalServerID),
			"remote_server": h.uiServerRef(t.RemoteServerID),
			"engine":        t.Engine, "enabled": t.Enabled, "created_at": t.CreatedAt,
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
	pcts := h.uiResolvedPercents(topo.ID, mustListTunnels(h.Store, topo.ID))
	writeJSON(w, map[string]any{
		"status": "ok", "tunnel": key, "weight": weight,
		"resolved_percent": pcts[t.ID],
	})
}

// sortTunnelsByID keeps the row order stable for tests.
func sortTunnelsByID(ts []store.Tunnel) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].ID < ts[j].ID })
}

var _ = time.Now
