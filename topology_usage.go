package main

// Usage groupings are a read-only projection of the recorded tunnel ledger onto
// CURRENT topology assignments. Moving a tunnel moves its retained history;
// this is not historical ownership accounting. Physical traffic is independent:
// encapsulation, mesh hops and unrelated server traffic cannot be reconciled by
// subtracting topology totals from physical totals.
import (
	"sort"
	"strconv"
	"strings"
	"time"
)

type usageAmount struct {
	Rx    uint64 `json:"rx"`
	Tx    uint64 `json:"tx"`
	Total uint64 `json:"total"`
	Known bool   `json:"known"`
}

type topologyUsageRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	usageAmount
	TunnelCount int `json:"tunnel_count"`
}

type serverUsageTotal struct {
	usageAmount
	RxBytes   uint64 `json:"rx_bytes"`
	TxBytes   uint64 `json:"tx_bytes"`
	Available bool   `json:"available"`
	StartedAt int64  `json:"started_at"`
}

type serverUsageWindow struct {
	usageAmount
	Points []usagePoint  `json:"points"`
	Days   []usageDayRow `json:"days"`
}

// The metrics collector calls ONE of these hooks once per snapshot. Never call
// them from an HTTP handler, or feed physical counters into usageOnSnapshot.
// The per-interface hook is preferred: independent resets/new interfaces must
// not turn a smaller aggregate into a reset of every surviving NIC.
func observeServerUsage(at time.Time, rx, tx uint64, known bool) {
	if !known {
		trafficUsage.observe(usageServerKey, "Server physical traffic", at, nil, nil)
		return
	}
	trafficUsage.observe(usageServerKey, "Server physical traffic", at, &rx, &tx)
}

func observeServerInterfaceUsage(at time.Time, counters map[string][2]uint64, known bool) {
	trafficUsage.observeIfsKnown(usageServerKey, "Server physical traffic", at, counters, known)
}

func usageKnown(l *usageLink, now time.Time) bool {
	if l == nil || l.Seen == 0 {
		return false
	}
	if l.Known != nil && !*l.Known {
		return false
	}
	// v1 files did not store Measured; existing points prove measurement.
	if !l.Measured && len(l.Points) == 0 && len(l.Days) == 0 {
		return false
	}
	fresh := 2 * time.Minute
	if 3*snapshotEvery > fresh {
		fresh = 3 * snapshotEvery
	}
	return now.Unix()-l.Seen <= int64(fresh.Seconds())
}

func (s *usageStore) serverTotalsLocked(now time.Time, window time.Duration, step int64) (serverUsageTotal, serverUsageWindow) {
	l := s.links[usageServerKey]
	total := serverUsageTotal{}
	out := serverUsageWindow{Points: []usagePoint{}, Days: []usageDayRow{}}
	if l == nil {
		return total, out
	}
	total.usageAmount = usageAmount{Rx: l.CumulativeRx, Tx: l.CumulativeTx, Total: l.CumulativeRx + l.CumulativeTx, Known: usageKnown(l, now)}
	total.RxBytes, total.TxBytes, total.Available, total.StartedAt = total.Rx, total.Tx, total.Known, l.StartedAt
	sr, _ := s.seriesLocked(usageServerKey, now, window, step)
	out.usageAmount = usageAmount{Rx: sr.Rx, Tx: sr.Tx, Total: sr.Rx + sr.Tx, Known: total.Known}
	out.Points, out.Days = sr.Points, sr.Days
	return total, out
}

// Aliases require shared counter identity, not matching labels or remote IP.
// The common legacy peer record explicitly points at gre-tunnel/frps or the
// base Backhaul service. Canonical main wins over its duplicate ledger history.
func usageSnapshotAliases(snapshot *HubSnapshot) map[string]string {
	aliases := map[string]string{"bagher:1": "bagher"}
	if snapshot == nil {
		return aliases
	}
	for _, p := range snapshot.Peers {
		if p.ID <= 0 {
			continue
		}
		main := p.GreIf == "gre-tunnel"
		if p.NoGre && p.FrpsSvc != "" {
			svc := snapshot.Local.FrpSvc
			if snapshot.Main != nil && snapshot.Main.FrpsSvc != "" {
				svc = snapshot.Main.FrpsSvc
			}
			main = p.FrpsSvc == svc
		}
		if main {
			aliases["peer:"+strconv.Itoa(p.ID)] = "main"
		}
	}
	return aliases
}

func canonicalUsageID(id string, aliases map[string]string) string {
	if canonical := aliases[id]; canonical != "" {
		return canonical
	}
	return id
}

func topologyUsageKey(id string) string {
	if strings.HasPrefix(id, "peer:") {
		return "p" + strings.TrimPrefix(id, "peer:")
	}
	return id
}

func usageIDFromKey(key string) string {
	switch key {
	case usageMainKey, "matrix", "bagher":
		return key
	case usageHostKey, usageServerKey, usageTotalKey:
		return ""
	}
	if strings.HasPrefix(key, "p") {
		if n, err := strconv.Atoi(strings.TrimPrefix(key, "p")); err == nil && n > 0 {
			return "peer:" + strconv.Itoa(n)
		}
	}
	if topologyTunnelRE.MatchString(key) {
		return key
	}
	return ""
}

// The unassigned bucket is a fallback for tunnels (or their retained history)
// that no current grouping owns. With every tunnel assigned and no detached
// history it is empty, and must not be emitted as a placeholder row.
func unassignedUsageVisible(row topologyUsageRow) bool {
	return row.TunnelCount > 0 || row.Rx > 0 || row.Tx > 0
}

// topologyTotals does not take topology/engine locks. Callers read grouping
// metadata and inventory first, release their locks, then query the usage mutex.
func (s *usageStore) topologyTotals(now time.Time, window time.Duration, step int64, groups []Topology, inventory []topologyTunnel, snapshot *HubSnapshot) ([]topologyUsageRow, topologyUsageRow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.topologyTotalsLocked(now, window, step, groups, inventory, snapshot)
}

func (s *usageStore) topologyTotalsLocked(now time.Time, window time.Duration, step int64, groups []Topology, inventory []topologyTunnel, snapshot *HubSnapshot) ([]topologyUsageRow, topologyUsageRow) {
	aliases := usageSnapshotAliases(snapshot)
	for key, link := range s.links {
		if link.AliasOf != "" {
			aliases[usageIDFromKey(key)] = usageIDFromKey(link.AliasOf)
		}
	}
	rows := make([]topologyUsageRow, len(groups))
	owners := map[string]int{}
	for i, group := range groups {
		rows[i] = topologyUsageRow{ID: group.ID, Name: group.Name, usageAmount: usageAmount{Known: true}}
		for _, raw := range group.TunnelIDs {
			id := canonicalUsageID(raw, aliases)
			if owner, exists := owners[id]; exists {
				if owner != i {
					rows[i].Known, rows[owner].Known = false, false
				}
				continue // no alias/corrupt duplicate can book the same bytes twice
			}
			owners[id] = i
			rows[i].TunnelCount++
		}
	}
	unassigned := topologyUsageRow{ID: "unassigned", Name: "Unassigned tunnels", usageAmount: usageAmount{Known: true}}
	ids := map[string]bool{}
	present := map[string]bool{}
	for _, tunnel := range inventory {
		id := canonicalUsageID(tunnel.ID, aliases)
		ids[id], present[id] = true, true
	}
	for id := range owners {
		ids[id] = true // temporarily missing assigned resources remain visible/unknown
	}
	// Removed tunnels' retained history is explicitly unassigned, not lost and
	// not promoted to physical traffic. Aggregate host is never attributable.
	for key := range s.links {
		if id := usageIDFromKey(key); id != "" {
			ids[canonicalUsageID(id, aliases)] = true
		}
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	for _, id := range ordered {
		row := &unassigned
		if owner, ok := owners[id]; ok {
			row = &rows[owner]
		} else {
			unassigned.TunnelCount++
		}
		key := topologyUsageKey(id)
		sr, exists := s.seriesLocked(key, now, window, step)
		if !exists || !usageKnown(s.links[key], now) || inventory != nil && !present[id] {
			row.Known = false
		}
		row.Rx += sr.Rx
		row.Tx += sr.Tx
		row.Total = row.Rx + row.Tx
	}
	return rows, unassigned
}

// Book special engines from already published counters; no live status reads,
// carrier refresh, DNS or probes. Matrix is ONE qmx0 ledger, not each mesh peer.
func usageObserveSpecialTunnels(s *HubSnapshot, at time.Time) {
	aliases := usageSnapshotAliases(s)
	trafficUsage.mu.Lock()
	for _, p := range s.Peers {
		if p.ID <= 0 {
			continue
		}
		if old := trafficUsage.links[usageKey(p)]; old != nil {
			alias := ""
			if aliases["peer:"+strconv.Itoa(p.ID)] == "main" {
				alias = usageMainKey
			}
			if old.AliasOf != alias {
				old.AliasOf = alias
				trafficUsage.dirty = true
			}
		}
	}
	trafficUsage.mu.Unlock()
	if mx := s.Local.Matrix; mx != nil && mx.Enabled {
		rx, tx := mx.Rx, mx.Tx
		if rx == nil || tx == nil {
			if c, ok := parseNetDev(s.DevData)[matrixIfaceName]; ok {
				rx, tx = &c.rx, &c.tx
			}
		}
		trafficUsage.observe("matrix", "Matrix", at, rx, tx)
	}
	seen := map[string]bool{}
	for tag, c := range s.CarrierTags {
		var id string
		switch {
		case strings.HasPrefix(tag, "bagher-"):
			n, err := strconv.Atoi(strings.TrimPrefix(tag, "bagher-"))
			if err != nil || n <= 0 {
				continue
			}
			id = "bagher"
			if n != 1 {
				id += ":" + strconv.Itoa(n)
			}
		case strings.HasPrefix(tag, "xray-"):
			n, err := strconv.Atoi(strings.TrimPrefix(tag, "xray-"))
			if err != nil || n <= 0 {
				continue
			}
			id = "xray:" + strconv.Itoa(n)
		default:
			continue
		}
		if !s.CarrierOK {
			continue
		}
		seen[id] = true
		rx, tx := c[0], c[1]
		trafficUsage.observe(id, id, at, &rx, &tx)
	}
	// Previously measurable engines missing from this snapshot are unknown,
	// retaining their baseline and history rather than reporting measured zero.
	trafficUsage.mu.Lock()
	missing := []string{}
	for key := range trafficUsage.links {
		if key == "matrix" && (s.Local.Matrix == nil || !s.Local.Matrix.Enabled) ||
			(key == "bagher" || strings.HasPrefix(key, "bagher:") || strings.HasPrefix(key, "xray:")) && !seen[key] {
			missing = append(missing, key)
		}
	}
	trafficUsage.mu.Unlock()
	for _, key := range missing {
		trafficUsage.observe(key, "", at, nil, nil)
	}
}

func usageResponse(now time.Time, window time.Duration, step int64, rangeName string) map[string]any {
	// Read-only inventory uses snapPtr and cached/configured status, NOT the
	// cold-start currentSnapshot path that can launch collection from a handler.
	inventory := topologyInventory()
	topologyMu.Lock()
	groups, err := loadTopologies()
	topologyMu.Unlock()
	trafficUsage.mu.Lock()
	server, serverWindow := trafficUsage.serverTotalsLocked(now, window, step)
	rows, unassigned := trafficUsage.topologyTotalsLocked(now, window, step, groups, inventory, snapPtr.Load())
	trafficUsage.mu.Unlock()
	out := map[string]any{
		"range": rangeName, "keep_days": int(usageKeep / (24 * time.Hour)), "step_sec": step, "now": now.Unix(),
		"links":        trafficUsage.all(now, window, step),
		"server_total": server, "server_window": serverWindow, "topologies": rows,
		"grouping_basis": "current_assignments", "server_basis": "physical_interfaces", "topology_basis": "recorded_tunnel_counters",
		"topology_known": err == nil,
	}
	// Grouping metadata is unavailable: no unassigned fallback row either.
	if err == nil && unassignedUsageVisible(unassigned) {
		out["unassigned"] = unassigned
	}
	if err != nil {
		out["topology_error"] = "cannot read topology collection"
		out["topologies"] = []topologyUsageRow{}
	}
	return out
}
