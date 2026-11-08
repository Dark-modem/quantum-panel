package store

// billing.go — the billing export data layer (phase 12 TASK 4). One
// rollup-based pass over [from, to] (hourly rows for folded hours +
// the raw tail, chained per stream exactly like RangeUsageDetailed)
// that produces the three report scopes at once:
//
//	per Iran datacenter / per server / per tunnel — billable download
//	(wire bytes RECEIVED on the Iran reporter), upload, payload, the
//	overhead ratio, the datacenter price hint and the SUGGESTED
//	Pasarguard coefficient (price hint × engine overhead ratio).
//
// Every number is REAL bytes — never multiplied by any coefficient
// (ACCOUNTING-SEMANTICS.md). Each row carries DATA-QUALITY flags so the
// operator knows when numbers are incomplete: reporting gaps (hours
// with no sample between the stream's first and last sample), unknown
// counters, and agents offline during the period (a gap IS an agent
// outage). The commercial core is exactness + honesty about holes.

import (
	"sort"
	"strconv"
	"strings"
)

const billingGapHours = 2 // >2h without a sample from a stream = a GAP

type BillingQuality struct {
	GapCount        int   `json:"gaps"`             // number of gap episodes
	LongestGapS     int64 `json:"longest_gap_s"`    // longest gap in seconds
	UnknownCounters bool  `json:"unknown_counters"` // some samples had unknown wire/payload counters
	OfflineAgents   int   `json:"agents_offline"`   // reporting agents that had a gap in the window
}

type BillingTunnelRow struct {
	TunnelID     int64          `json:"tunnel_id"`
	TunnelKey    string         `json:"tunnel_key"`
	Topology     string         `json:"topology"`
	LocalServer  string         `json:"local_server"`
	RemoteServer string         `json:"remote_server"`
	Engine       string         `json:"engine"`
	Usage        Usage          `json:"usage"`
	Billable     Usage          `json:"billable"`
	DatacenterID int64          `json:"datacenter_id,omitempty"`
	Datacenter   string         `json:"datacenter,omitempty"`
	PriceHint    float64        `json:"price_coefficient_hint,omitempty"`
	Suggested    float64        `json:"suggested_coefficient,omitempty"`
	Overhead     float64        `json:"overhead_ratio"`
	Quality      BillingQuality `json:"quality"`
}

type BillingServerRow struct {
	ServerID     int64          `json:"server_id"`
	Name         string         `json:"name"`
	DatacenterID int64          `json:"datacenter_id,omitempty"`
	Datacenter   string         `json:"datacenter,omitempty"`
	Usage        Usage          `json:"usage"`
	Billable     Usage          `json:"billable"`
	Overhead     float64        `json:"overhead_ratio"`
	Quality      BillingQuality `json:"quality"`
}

type BillingDCRow struct {
	DatacenterID int64          `json:"datacenter_id"`
	Name         string         `json:"name"`
	Usage        Usage          `json:"usage"`
	Billable     Usage          `json:"billable"`
	Overhead     float64        `json:"overhead_ratio"`
	PriceHint    float64        `json:"price_coefficient_hint"`
	Suggested    float64        `json:"suggested_coefficient"`
	Quality      BillingQuality `json:"quality"`
}

type billingAcc struct {
	acc  map[string]*streamAcc
	iran map[string]*streamAcc
	// quality per stream: last hour seen + hole tracking
	stHours   map[string]int64 // stream → last hour index with a row
	stHas     map[string]bool
	qGaps     map[string]int
	qLongest  map[string]int64
	qUnknown  map[string]bool
	qServers  map[string]int64 // stream → server id (0 = unlinked)
	firstHour map[string]int64
	lastHour  map[string]int64
}

func newBillingAcc() *billingAcc {
	return &billingAcc{
		acc: map[string]*streamAcc{}, iran: map[string]*streamAcc{},
		stHours: map[string]int64{}, stHas: map[string]bool{},
		qGaps: map[string]int{}, qLongest: map[string]int64{},
		qUnknown: map[string]bool{}, qServers: map[string]int64{},
		firstHour: map[string]int64{}, lastHour: map[string]int64{},
	}
}

func (b *billingAcc) stream(tid int64, agent string) string { return streamKey(tid, agent) }

// observe tracks one reading (raw sample or hourly row) for quality.
func (b *billingAcc) observe(stream string, hour int64, wireKnown, payloadKnown bool) {
	if !wireKnown || !payloadKnown {
		b.qUnknown[stream] = true
	}
	if b.stHas[stream] {
		hole := hour - b.stHours[stream] - 1
		if hole > 0 {
			b.qGaps[stream]++
			if g := hole * 3600; g > b.qLongest[stream] {
				b.qLongest[stream] = g
			}
		}
	} else {
		b.firstHour[stream] = hour
	}
	b.stHours[stream] = hour
	b.lastHour[stream] = hour
	b.stHas[stream] = true
}

// BillingReport computes the three billing scopes over [from, to].
func (s *Store) BillingReport(from, to int64) (dcs []BillingDCRow, servers []BillingServerRow, tunnels []BillingTunnelRow, err error) {
	b := newBillingAcc()
	done := s.rollupDoneUntil()
	// EXACT window edges (billing): only hours FULLY inside [from, to]
	// are read from the hourly rollup; the partial edge hours come from
	// the raw samples (exact within the 30-day retention; for older
	// windows the edge partial hours are a documented <=1h tolerance,
	// same as the rollup path's first-row rule).
	hourFrom := (from + 3599) / 3600 // first FULLY-inside hour
	hourTo := to / 3600              // first hour NOT fully inside
	foldLo := hourFrom
	if done > foldLo {
		foldLo = done
	}
	foldHi := hourTo
	if done < foldHi {
		foldHi = done
	}
	accOf := func(m map[string]*streamAcc, k string) *streamAcc {
		a := m[k]
		if a == nil {
			a = &streamAcc{}
			m[k] = a
		}
		return a
	}
	hourOf := func(at int64) int64 { return at / 3600 }

	ingest := func(stream, role string, hour int64, rx, tx, prx, ptx int64, wk, pk bool) {
		b.observe(stream, hour, wk, pk)
		accOf(b.acc, stream).ingest(rx, tx, prx, ptx, wk, pk)
		if role == "iran" {
			accOf(b.iran, stream).ingest(rx, tx, prx, ptx, wk, pk)
		}
	}

	// 1. folded hours (fully inside the window, past the watermark).
	if foldHi > foldLo {
		rows, err := s.db.Query(`SELECT h.tunnel_id, h.agent_id, h.hour, h.last_rx, h.last_tx, h.last_prx, h.last_ptx,
			h.wire_known, h.payload_known, COALESCE(a.role,''), COALESCE(a.server_id,0)
			FROM traffic_hourly h LEFT JOIN agents a ON a.id = h.agent_id
			WHERE h.hour >= ? AND h.hour < ? ORDER BY h.agent_id, h.tunnel_id, h.hour`, foldLo, foldHi)
		if err != nil {
			return nil, nil, nil, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var tid, hour, lrx, ltx, lprx, lptx, serverID int64
				var agent, role string
				var wk, pk bool
				if err := rows.Scan(&tid, &agent, &hour, &lrx, &ltx, &lprx, &lptx, &wk, &pk, &role, &serverID); err != nil {
					return err
				}
				k := b.stream(tid, agent)
				b.qServers[k] = serverID
				ingest(k, role, hour, lrx, ltx, lprx, lptx, wk, pk)
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, nil, nil, err
		}
	}
	// 2. raw edges: everything the folded hours do not cover — the head
	// partial hour(s) and the tail (raw rows exist only within retention;
	// for older windows the edge partial hours are the documented <=1h
	// tolerance). The two ranges below never overlap the folded region.
	rawRanges := [][2]int64{}
	if headEnd := foldLo * 3600; headEnd > from {
		if headEnd > to {
			headEnd = to
		}
		rawRanges = append(rawRanges, [2]int64{from, headEnd - 1})
	}
	if tailStart := foldHi * 3600; tailStart <= to {
		if tailStart < from {
			tailStart = from
		}
		rawRanges = append(rawRanges, [2]int64{tailStart, to})
	}
	for _, rr := range rawRanges {
		if rr[1] < rr[0] {
			continue
		}
		rows, err := s.db.Query(`SELECT ts.tunnel_id, ts.agent_id, ts.at, ts.wire_rx, ts.wire_tx, ts.payload_rx, ts.payload_tx,
			ts.counters_known, ts.payload_known, COALESCE(a.role,''), COALESCE(a.server_id,0)
			FROM traffic_samples ts LEFT JOIN agents a ON a.id = ts.agent_id
			WHERE ts.at >= ? AND ts.at <= ? ORDER BY ts.agent_id, ts.tunnel_id, ts.at`, rr[0], rr[1])
		if err != nil {
			return nil, nil, nil, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var tid, at, rx, tx, prx, ptx, serverID int64
				var agent, role string
				var wk, pk bool
				if err := rows.Scan(&tid, &agent, &at, &rx, &tx, &prx, &ptx, &wk, &pk, &role, &serverID); err != nil {
					return err
				}
				k := b.stream(tid, agent)
				b.qServers[k] = serverID
				ingest(k, role, hourOf(at), rx, tx, prx, ptx, wk, pk)
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, nil, nil, err
		}
	}

	// 3. shape the rows (names/coefficients from the catalog tables).
	type tunnelMeta struct {
		key, topo, local, remote, engine string
	}
	tm := map[int64]*tunnelMeta{}
	trows, err := s.db.Query(`SELECT t.id, t.tunnel_key, COALESCE(tp.name,''), COALESCE(ls.name,''), COALESCE(rs.name,''), t.engine
		FROM tunnels t LEFT JOIN topologies tp ON tp.id = t.topology_id
		LEFT JOIN servers ls ON ls.id = t.local_server_id LEFT JOIN servers rs ON rs.id = t.remote_server_id`)
	if err != nil {
		return nil, nil, nil, err
	}
	for trows.Next() {
		var id int64
		m := &tunnelMeta{}
		if err := trows.Scan(&id, &m.key, &m.topo, &m.local, &m.remote, &m.engine); err != nil {
			trows.Close()
			return nil, nil, nil, err
		}
		tm[id] = m
	}
	trows.Close()

	type serverMeta struct {
		name, dc string
		dcID     int64
	}
	sm := map[int64]*serverMeta{}
	srows, err := s.db.Query(`SELECT s.id, s.name, COALESCE(d.id,0), COALESCE(d.name,''), COALESCE(d.price_coefficient,1.0)
		FROM servers s LEFT JOIN datacenters d ON d.id = s.datacenter_id`)
	if err != nil {
		return nil, nil, nil, err
	}
	for srows.Next() {
		var id int64
		var price float64
		m := &serverMeta{}
		if err := srows.Scan(&id, &m.name, &m.dcID, &m.dc, &price); err != nil {
			srows.Close()
			return nil, nil, nil, err
		}
		sm[id] = m
	}
	srows.Close()

	// Aggregate streams → tunnels / servers / datacenters.
	dcRows := map[int64]*BillingDCRow{}
	svRows := map[int64]*BillingServerRow{}
	tnRows := map[int64]*BillingTunnelRow{}
	for k, a := range b.acc {
		tid, _ := decodeStream(k)
		iran := b.iran[k]
		if iran == nil {
			iran = &streamAcc{}
		}
		usage, billable := a.usage(), iran.usage()
		q := BillingQuality{
			GapCount: b.qGaps[k], LongestGapS: b.qLongest[k],
			UnknownCounters: b.qUnknown[k],
		}
		if q.GapCount > 0 {
			q.OfflineAgents = 1
		}
		// tunnel row
		tr := tnRows[tid]
		if tr == nil {
			meta := tm[tid]
			if meta == nil {
				meta = &tunnelMeta{}
			}
			tr = &BillingTunnelRow{TunnelID: tid, TunnelKey: meta.key, Topology: meta.topo,
				LocalServer: meta.local, RemoteServer: meta.remote, Engine: meta.engine}
			tnRows[tid] = tr
		}
		tr.Usage = tr.Usage.add(usage)
		tr.Billable = tr.Billable.add(billable)
		tr.Quality.GapCount += q.GapCount
		tr.Quality.OfflineAgents += q.OfflineAgents
		if q.LongestGapS > tr.Quality.LongestGapS {
			tr.Quality.LongestGapS = q.LongestGapS
		}
		tr.Quality.UnknownCounters = tr.Quality.UnknownCounters || q.UnknownCounters
		if sid := b.qServers[k]; sid != 0 {
			if smeta := sm[sid]; smeta != nil && smeta.dcID != 0 {
				tr.DatacenterID, tr.Datacenter = smeta.dcID, smeta.dc
			}
		}
		if tr.Usage.PayloadRx > 0 {
			tr.Overhead = float64(tr.Usage.WireRx) / float64(tr.Usage.PayloadRx)
		}
		// server row (the reporting agent's server)
		if sid := b.qServers[k]; sid != 0 {
			smeta := sm[sid]
			if smeta == nil {
				smeta = &serverMeta{}
			}
			sr := svRows[sid]
			if sr == nil {
				sr = &BillingServerRow{ServerID: sid, Name: smeta.name, DatacenterID: smeta.dcID, Datacenter: smeta.dc}
				svRows[sid] = sr
			}
			sr.Usage = sr.Usage.add(usage)
			sr.Billable = sr.Billable.add(billable)
			sr.Quality.GapCount += q.GapCount
			sr.Quality.OfflineAgents += q.OfflineAgents
			if q.LongestGapS > sr.Quality.LongestGapS {
				sr.Quality.LongestGapS = q.LongestGapS
			}
			sr.Quality.UnknownCounters = sr.Quality.UnknownCounters || q.UnknownCounters
			if sr.Usage.PayloadRx > 0 {
				sr.Overhead = float64(sr.Usage.WireRx) / float64(sr.Usage.PayloadRx)
			}
			// datacenter row
			if smeta.dcID != 0 {
				dr := dcRows[smeta.dcID]
				if dr == nil {
					dr = &BillingDCRow{DatacenterID: smeta.dcID, Name: smeta.dc}
					// The price HINT is the configured coefficient alone;
					// the SUGGESTED coefficient folds the engine overhead
					// ratio in (0 when no overhead has been observed).
					var price float64
					if err := s.db.QueryRow(`SELECT price_coefficient FROM datacenters WHERE id = ?`,
						smeta.dcID).Scan(&price); err == nil {
						dr.PriceHint = price
					}
					dr.Suggested, _ = s.SuggestedCoefficient(smeta.dcID, "")
					dcRows[smeta.dcID] = dr
				}
				dr.Usage = dr.Usage.add(usage)
				dr.Billable = dr.Billable.add(billable)
				dr.Quality.GapCount += q.GapCount
				dr.Quality.OfflineAgents += q.OfflineAgents
				if q.LongestGapS > dr.Quality.LongestGapS {
					dr.Quality.LongestGapS = q.LongestGapS
				}
				dr.Quality.UnknownCounters = dr.Quality.UnknownCounters || q.UnknownCounters
			}
		}
	}
	// Final overheads for the dc rows.
	for _, dr := range dcRows {
		if dr.Usage.PayloadRx > 0 {
			dr.Overhead = float64(dr.Usage.WireRx) / float64(dr.Usage.PayloadRx)
		}
	}
	// Deterministic order.
	for _, r := range tnRows {
		tunnels = append(tunnels, *r)
	}
	for _, r := range svRows {
		servers = append(servers, *r)
	}
	for _, r := range dcRows {
		dcs = append(dcs, *r)
	}
	sortBilling(dcs, servers, tunnels)
	return dcs, servers, tunnels, nil
}

func sortBilling(dcs []BillingDCRow, servers []BillingServerRow, tunnels []BillingTunnelRow) {
	sort.Slice(dcs, func(i, j int) bool { return dcs[i].DatacenterID < dcs[j].DatacenterID })
	sort.Slice(servers, func(i, j int) bool { return servers[i].ServerID < servers[j].ServerID })
	sort.Slice(tunnels, func(i, j int) bool { return tunnels[i].TunnelID < tunnels[j].TunnelID })
}

// decodeStream splits "tid\x00agent" (the encoding of streamKey).
func decodeStream(k string) (int64, string) {
	parts := strings.SplitN(k, "\x00", 2)
	tid, _ := strconv.ParseInt(parts[0], 10, 64)
	agent := ""
	if len(parts) > 1 {
		agent = parts[1]
	}
	return tid, agent
}
