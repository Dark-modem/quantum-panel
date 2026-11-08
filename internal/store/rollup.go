package store

// rollup.go — hourly rollups for traffic_samples (phase 10, task 3).
//
// Raw samples live for RETENTION (30 days, pruned by reportv2). Months of
// history must survive that prune: every COMPLETE hour of raw samples is
// folded into traffic_hourly BEFORE the prune deletes the raw rows, so
// old-range usage queries read the rollup instead of the deleted raw
// table.
//
// Hourly row semantics (per tunnel+agent+hour):
//   first_* = the counters of the FIRST KNOWN sample in the hour,
//   last_*  = the counters of the LAST KNOWN sample in the hour,
//   *_known = whether ANY known sample exists for that view in the hour.
// Wire and payload views are independent (matching the raw accum).
//
// Usage over an hour range chains the rows: per (tunnel, agent) stream,
// carry = the previous known row's last counters; the hour contributes
// delta(carry → first) [the boundary traffic, attributed to the newer
// hour] + delta(first → last) [the hour's internal traffic]. For the
// FIRST known row of a stream there is no carry: the internal delta is
// the usage (the pre-first counters are lost — the documented boundary
// tolerance of the rollup path). deltaUint's clamp covers counter resets;
// a wrapped 32-bit counter is indistinguishable from a reset at this
// layer (the raw window never wraps in practice within the retention).

import (
	"fmt"
	"strings"
)

const trafficRollupBatchSeconds = 24 * 3600 // fold at most a day of raw rows per call

// RollupTrafficHourly folds every COMPLETE hour of raw samples older than
// `before` into traffic_hourly. Idempotent: a done-until watermark keeps
// the fold from repeating work; the boundary snaps DOWN to the hour so
// only complete hours fold. Returns the number of hour rows written.
// Callers pass `before` well below the retention prune so the raw rows
// still exist when folded.
func (s *Store) RollupTrafficHourly(before int64) (int64, error) {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS traffic_hourly (
		tunnel_id INTEGER NOT NULL,
		agent_id TEXT NOT NULL DEFAULT '',
		hour INTEGER NOT NULL,
		first_rx INTEGER NOT NULL DEFAULT 0, first_tx INTEGER NOT NULL DEFAULT 0,
		first_prx INTEGER NOT NULL DEFAULT 0, first_ptx INTEGER NOT NULL DEFAULT 0,
		last_rx INTEGER NOT NULL DEFAULT 0, last_tx INTEGER NOT NULL DEFAULT 0,
		last_prx INTEGER NOT NULL DEFAULT 0, last_ptx INTEGER NOT NULL DEFAULT 0,
		wire_known INTEGER NOT NULL DEFAULT 0,
		payload_known INTEGER NOT NULL DEFAULT 0,
		UNIQUE (tunnel_id, agent_id, hour)
	)`); err != nil {
		return 0, err
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS traffic_rollup_state (
		id INTEGER PRIMARY KEY, done_until INTEGER NOT NULL
	)`); err != nil {
		return 0, err
	}
	var done int64
	_ = s.db.QueryRow(`SELECT done_until FROM traffic_rollup_state WHERE id = 1`).Scan(&done)
	horizon := before / 3600 // snap down to the hour INDEX: only complete hours fold
	if horizon <= done {
		return 0, nil
	}
	if done > 0 && horizon-done > trafficRollupBatchSeconds {
		// cap the fold SPAN per call — only meaningful once the watermark
		// is real (done=0 would clamp the horizon to timestamp 86400 and
		// select nothing)
		horizon = done + trafficRollupBatchSeconds
	}
	rows, err := s.db.Query(`SELECT tunnel_id, agent_id, at, wire_rx, wire_tx, payload_rx, payload_tx,
		counters_known, payload_known FROM traffic_samples
		WHERE at/3600 >= ? AND at/3600 < ? ORDER BY tunnel_id, agent_id, at`, done, horizon)
	if err != nil {
		return 0, err
	}
	type hourAcc struct {
		firstRX, firstTX, firstPRX, firstPTX int64
		lastRX, lastTX, lastPRX, lastPTX     int64
		wireKnown, payloadKnown              bool
	}
	acc := map[string]*hourAcc{}
	hours := map[string]int64{}
	var order []string
	keyOf := func(tid int64, agent string, hour int64) string {
		return fmt.Sprintf("%d\x00%s\x00%d", tid, agent, hour)
	}
	for rows.Next() {
		var tid int64
		var agent string
		var at, rx, tx, prx, ptx int64
		var wireKnown, payloadKnown bool
		if err := rows.Scan(&tid, &agent, &at, &rx, &tx, &prx, &ptx, &wireKnown, &payloadKnown); err != nil {
			rows.Close()
			return 0, err
		}
		hour := at / 3600 // hour INDEX (query bounds use the same units)
		k := keyOf(tid, agent, hour)
		h := acc[k]
		if h == nil {
			h = &hourAcc{}
			acc[k] = h
			hours[k] = hour
			order = append(order, k)
		}
		if wireKnown {
			if !h.wireKnown {
				h.wireKnown = true
				h.firstRX, h.firstTX = rx, tx
			}
			h.lastRX, h.lastTX = rx, tx
		}
		if payloadKnown {
			if !h.payloadKnown {
				h.payloadKnown = true
				h.firstPRX, h.firstPTX = prx, ptx
			}
			h.lastPRX, h.lastPTX = prx, ptx
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	txw, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer txw.Rollback()
	var folded int64
	for _, k := range order {
		h := acc[k]
		parts := strings.SplitN(k, "\x00", 3)
		var tid int64
		fmt.Sscanf(parts[0], "%d", &tid)
		agent, hour := parts[1], hours[k]
		wk, pk := 0, 0
		if h.wireKnown {
			wk = 1
		}
		if h.payloadKnown {
			pk = 1
		}
		if _, err := txw.Exec(`INSERT INTO traffic_hourly (tunnel_id, agent_id, hour,
			first_rx, first_tx, first_prx, first_ptx,
			last_rx, last_tx, last_prx, last_ptx, wire_known, payload_known)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			tid, agent, hour, h.firstRX, h.firstTX, h.firstPRX, h.firstPTX,
			h.lastRX, h.lastTX, h.lastPRX, h.lastPTX, wk, pk); err != nil {
			return 0, err
		}
		folded++
	}
	if _, err := txw.Exec(`INSERT INTO traffic_rollup_state (id, done_until) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET done_until = excluded.done_until`, horizon); err != nil {
		return 0, err
	}
	return folded, txw.Commit()
}

// hourlyAcc chains one (tunnel, agent) stream of hourly first/last counter
// rows into usage. The carry between hours is the previous row's last
// counters.
type hourlyAcc struct {
	usage    Usage
	haveWire bool
	prevWRX  int64
	prevWTX  int64
	havePay  bool
	prevPRX  int64
	prevPTX  int64
}

func (a *hourlyAcc) ingestHour(firstRX, firstTX, firstPRX, firstPTX,
	lastRX, lastTX, lastPRX, lastPTX int64, wireKnown, payloadKnown bool) {
	// Chain rule: with a carry the hour contributes delta(prevLast →
	// last) (boundary + internal together); without one (the stream's
	// first known hour) it zero-bases on LAST — the counters ARE the
	// usage, the same first-sample rule the raw path applies. Either way
	// the boundary attribution does not change the totals.
	if wireKnown {
		if a.haveWire {
			a.usage.WireRx += deltaUint(a.prevWRX, lastRX)
			a.usage.WireTx += deltaUint(a.prevWTX, lastTX)
		} else {
			a.usage.WireRx += deltaUint(0, lastRX)
			a.usage.WireTx += deltaUint(0, lastTX)
			a.haveWire = true
		}
		a.prevWRX, a.prevWTX = lastRX, lastTX
	} else {
		a.haveWire = false // no known sample that hour: the wire base resets
	}
	if payloadKnown {
		if a.havePay {
			a.usage.PayloadRx += deltaUint(a.prevPRX, lastPRX)
			a.usage.PayloadTx += deltaUint(a.prevPTX, lastPTX)
		} else {
			a.usage.PayloadRx += deltaUint(0, lastPRX)
			a.usage.PayloadTx += deltaUint(0, lastPTX)
			a.havePay = true
		}
		a.prevPRX, a.prevPTX = lastPRX, lastPTX
	} else {
		a.havePay = false
	}
}

// HourlyUsageDetailed sums usage over [from, to] from the traffic_hourly
// rows (the rollup path used when raw samples have been pruned). The
// iran-role streams form the billable view, exactly like the raw path.
func (s *Store) HourlyUsageDetailed(tunnelIDs []int64, from, to int64) (Usage, Usage, error) {
	if len(tunnelIDs) == 0 {
		return Usage{}, Usage{}, nil
	}
	ids := make([]string, 0, len(tunnelIDs))
	for _, id := range tunnelIDs {
		ids = append(ids, fmt.Sprintf("%d", id))
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT h.tunnel_id, h.agent_id,
		h.hour, h.first_rx, h.first_tx, h.first_prx, h.first_ptx,
		h.last_rx, h.last_tx, h.last_prx, h.last_ptx, h.wire_known, h.payload_known,
		COALESCE(a.role,'')
		FROM traffic_hourly h LEFT JOIN agents a ON a.id = h.agent_id
		WHERE h.tunnel_id IN (%s) AND h.hour >= ? AND h.hour <= ?
		ORDER BY h.tunnel_id, h.agent_id, h.hour`, strings.Join(ids, ",")),
		from/3600, to/3600)
	if err != nil {
		return Usage{}, Usage{}, err
	}
	defer rows.Close()
	var all, iran Usage
	var acc, iranAcc hourlyAcc
	curStream := ""
	for rows.Next() {
		var tid, hour int64
		var agent, role string
		var frx, ftx, fprx, fptx, lrx, ltx, lprx, lptx int64
		var wireKnown, payloadKnown bool
		if err := rows.Scan(&tid, &agent, &hour, &frx, &ftx, &fprx, &fptx,
			&lrx, &ltx, &lprx, &lptx, &wireKnown, &payloadKnown, &role); err != nil {
			return Usage{}, Usage{}, err
		}
		stream := fmt.Sprintf("%d\x00%s", tid, agent)
		if stream != curStream {
			all = all.add(acc.usage)
			iran = iran.add(iranAcc.usage)
			acc, iranAcc = hourlyAcc{}, hourlyAcc{}
			curStream = stream
		}
		acc.ingestHour(frx, ftx, fprx, fptx, lrx, ltx, lprx, lptx, wireKnown, payloadKnown)
		if role == "iran" {
			iranAcc.ingestHour(frx, ftx, fprx, fptx, lrx, ltx, lprx, lptx, wireKnown, payloadKnown)
		}
	}
	if err := rows.Err(); err != nil {
		return Usage{}, Usage{}, err
	}
	all = all.add(acc.usage)
	iran = iran.add(iranAcc.usage)
	return all, iran, nil
}
