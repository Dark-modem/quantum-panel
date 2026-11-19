package store

// uiqueries.go — the Phase-11 UI data layer: everything the new
// /api/ui/* endpoints read and the persistence the v2 report's new
// sections need (agent resources, engine status, per-tunnel process
// metrics, tunnel events) plus RANGE usage over raw samples + hourly
// rollups (the exact-composition query the overview and the detail
// views poll every few seconds).

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// isNoRows reports sql.ErrNoRows without importing the sentinel check
// everywhere.
func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// ---- agent resource mirror (ResourceReport was received and discarded
// before Phase 11; migration 6 gives it columns) ------------------------

// UpdateAgentResources persists one heartbeat's host metrics.
func (s *Store) UpdateAgentResources(agentID string, cpuCores int, cpuPct, load1 float64,
	memTotalMB, memUsedMB int64, diskTotalGB, diskUsedGB float64, uptimeS int64, virt string) error {
	_, err := s.db.Exec(`UPDATE agents SET cpu_cores=?, cpu_pct=?, load1=?, mem_total_mb=?, mem_used_mb=?,
		disk_total_gb=?, disk_used_gb=?, uptime_s=?, virt=? WHERE id=?`,
		cpuCores, cpuPct, load1, memTotalMB, memUsedMB, diskTotalGB, diskUsedGB, uptimeS, virt, agentID)
	return err
}

// UpdateAgentCPUModel persists the CPU model string the agent reports
// from /proc/cpuinfo (the model name usually carries the @ frequency).
func (s *Store) UpdateAgentCPUModel(agentID, cpuModel string) error {
	_, err := s.db.Exec(`UPDATE agents SET cpu_model=? WHERE id=?`, cpuModel, agentID)
	return err
}

// SetAgentArchOS mirrors the hello identity into the store (it lived
// only in the legacy registry before).
func (s *Store) SetAgentArchOS(agentID, arch, osName string) error {
	_, err := s.db.Exec(`UPDATE agents SET arch=?, os=? WHERE id=?`, arch, osName, agentID)
	return err
}

// SetAgentPublicIPOverride sets/clears (empty string) the manual public
// IP override the UI edits.
func (s *Store) SetAgentPublicIPOverride(agentID, ip string) error {
	_, err := s.db.Exec(`UPDATE agents SET public_ip_override=? WHERE id=?`, ip, agentID)
	return err
}

// ---- per-agent engine status (the report's engine_status[]) -----------

type AgentEngine struct {
	AgentID     string `json:"agent_id"`
	Engine      string `json:"engine"`
	Installed   bool   `json:"installed"`
	Version     string `json:"version"`
	BytesOnDisk int64  `json:"bytes_on_disk"`
	Desired     bool   `json:"desired"`
	Instances   int    `json:"instances"`
	LastError   string `json:"last_error"`
	UpdatedAt   int64  `json:"updated_at"`
}

// UpsertAgentEngine persists one engine's reported state (the LATEST
// report wins; there is no history — the UI shows "now").
func (s *Store) UpsertAgentEngine(e AgentEngine) error {
	ii, dd := 0, 0
	if e.Installed {
		ii = 1
	}
	if e.Desired {
		dd = 1
	}
	_, err := s.db.Exec(`INSERT INTO agent_engines (agent_id, engine, installed, version, bytes_on_disk,
		desired, instances, last_error, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, engine) DO UPDATE SET installed=excluded.installed, version=excluded.version,
		  bytes_on_disk=excluded.bytes_on_disk, instances=excluded.instances,
		  last_error=excluded.last_error, updated_at=excluded.updated_at`,
		e.AgentID, e.Engine, ii, e.Version, e.BytesOnDisk, dd, e.Instances, e.LastError, e.UpdatedAt)
	return err
}

// SetAgentEngineDesired writes ONLY the desired flag (the enable/disable
// endpoints) — the reported columns stay as the agent last reported.
func (s *Store) SetAgentEngineDesired(agentID, engine string, desired bool) error {
	d := 0
	if desired {
		d = 1
	}
	_, err := s.db.Exec(`INSERT INTO agent_engines (agent_id, engine, desired, updated_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(agent_id, engine) DO UPDATE SET desired=excluded.desired,
		updated_at=excluded.updated_at`, agentID, engine, d, time.Now().Unix())
	return err
}

// AgentEngineDesired reads the stored desired flag. Back-compat rule:
// a rowless engine is desired iff it is the matrix default or the agent
// already runs tunnels of it (legacy agents never wrote desired rows).
func (s *Store) AgentEngineDesired(agentID, engine string) (bool, error) {
	var d int
	err := s.db.QueryRow(`SELECT desired FROM agent_engines WHERE agent_id=? AND engine=?`,
		agentID, engine).Scan(&d)
	if err == nil {
		return d == 1, nil
	}
	if !isNoRows(err) {
		return false, err
	}
	if engine == "matrix" {
		return true, nil // enabled by default on every agent
	}
	// Legacy: engines with existing tunnels count as enabled.
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM agents a JOIN tunnels t
		ON t.local_server_id = a.server_id OR t.remote_server_id = a.server_id
		WHERE a.id = ? AND t.engine = ?`, agentID, engine).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// AgentEngines lists the persisted per-agent engine rows (empty slice =
// nothing reported yet; the UI unions the catalog).
func (s *Store) AgentEngines(agentID string) ([]AgentEngine, error) {
	rows, err := s.db.Query(`SELECT agent_id, engine, installed, version, bytes_on_disk, desired,
		instances, last_error, updated_at FROM agent_engines WHERE agent_id=? ORDER BY engine`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AgentEngine
	for rows.Next() {
		var e AgentEngine
		var ii, dd int
		if err := rows.Scan(&e.AgentID, &e.Engine, &ii, &e.Version, &e.BytesOnDisk, &dd,
			&e.Instances, &e.LastError, &e.UpdatedAt); err != nil {
			return nil, err
		}
		e.Installed, e.Desired = ii == 1, dd == 1
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- per-tunnel process metrics (latest sample per tunnel+agent) ------

type TunnelProcNow struct {
	TunnelID  int64   `json:"tunnel_id"`
	AgentID   string  `json:"agent_id"`
	CPUPct    float64 `json:"cpu_pct"`
	RSSBytes  int64   `json:"rss_bytes"`
	Processes int     `json:"processes"`
	At        int64   `json:"at"`
}

// UpsertTunnelProc stores the LATEST sample (upsert, no history — the
// UI shows "now"; bounded: one row per running tunnel+agent).
func (s *Store) UpsertTunnelProc(p TunnelProcNow) error {
	_, err := s.db.Exec(`INSERT INTO tunnel_proc (tunnel_id, agent_id, cpu_pct, rss_bytes, processes, at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(tunnel_id, agent_id) DO UPDATE SET
		cpu_pct=excluded.cpu_pct, rss_bytes=excluded.rss_bytes, processes=excluded.processes, at=excluded.at`,
		p.TunnelID, p.AgentID, p.CPUPct, p.RSSBytes, p.Processes, p.At)
	return err
}

// TunnelProcFor reads the latest process metrics per tunnel (agent's
// report wins over a stale other-end sample by `at`).
func (s *Store) TunnelProcFor(tunnelIDs []int64) (map[int64]TunnelProcNow, error) {
	out := map[int64]TunnelProcNow{}
	if len(tunnelIDs) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(tunnelIDs))
	for _, id := range tunnelIDs {
		ids = append(ids, fmt.Sprintf("%d", id))
	}
	rows, err := s.db.Query(fmt.Sprintf(`SELECT tunnel_id, agent_id, cpu_pct, rss_bytes, processes, at
		FROM tunnel_proc WHERE tunnel_id IN (%s) ORDER BY at`, strings.Join(ids, ",")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p TunnelProcNow
		if err := rows.Scan(&p.TunnelID, &p.AgentID, &p.CPUPct, &p.RSSBytes, &p.Processes, &p.At); err != nil {
			return nil, err
		}
		if cur, ok := out[p.TunnelID]; !ok || p.At >= cur.At {
			out[p.TunnelID] = p
		}
	}
	return out, rows.Err()
}

// ---- tunnel events (bounded state-change + error log) -----------------

type TunnelEvent struct {
	ID        int64  `json:"id"`
	TunnelID  int64  `json:"tunnel_id"`
	At        int64  `json:"at"`
	Kind      string `json:"kind"` // "state" | "error"
	FromState string `json:"from_state,omitempty"`
	ToState   string `json:"to_state,omitempty"`
	Code      string `json:"code,omitempty"`
	Text      string `json:"text,omitempty"`
}

func (s *Store) AddTunnelEvent(e TunnelEvent) error {
	_, err := s.db.Exec(`INSERT INTO tunnel_events (tunnel_id, at, kind, from_state, to_state, code, text)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, e.TunnelID, e.At, e.Kind, e.FromState, e.ToState, e.Code, e.Text)
	return err
}

func (s *Store) TunnelEvents(tunnelID int64, limit int) ([]TunnelEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return scanList(s, `SELECT id, tunnel_id, at, kind, from_state, to_state, code, text
		FROM tunnel_events WHERE tunnel_id=? ORDER BY at DESC, id DESC LIMIT ?`, func(e *TunnelEvent) []any {
		return []any{&e.ID, &e.TunnelID, &e.At, &e.Kind, &e.FromState, &e.ToState, &e.Code, &e.Text}
	}, tunnelID, limit)
}

// PruneTunnelEvents drops events older than `before` (called next to the
// traffic prune; keeps the table bounded at ~7 days).
func (s *Store) PruneTunnelEvents(before int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM tunnel_events WHERE at < ?`, before)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- RANGE usage: raw samples + hourly rollups, composed exactly ------
//
// The overview polls traffic for today/7d/30d every few seconds. Raw
// samples live ~30 days; complete hours are FOLDED into traffic_hourly
// (watermark done_until = first unfolded hour INDEX). A range therefore
// reads:
//   [from, foldStart)  → traffic_hourly rows (chained per stream),
//   [foldStart, to]    → raw traffic_samples (chained per stream),
// with the carry handed over at the boundary, and the SAME zero-base
// rule as TunnelUsageDetailed: the first known reading in the window is
// the base (nothing is invented). Counter resets clamp via deltaUint.

// streamAcc chains one counter stream (any grouping key) through
// readings in time order.
type streamAcc struct {
	haveW, haveP   bool
	wR, wT, pR, pT int64
	wireR, wireT   uint64
	payloadR, payT uint64
}

func (a *streamAcc) ingest(rx, tx, prx, ptx int64, wireKnown, payloadKnown bool) {
	if wireKnown {
		if a.haveW {
			a.wireR += deltaUint(a.wR, rx)
			a.wireT += deltaUint(a.wT, tx)
		} else {
			a.wireR += deltaUint(0, rx)
			a.wireT += deltaUint(0, tx)
			a.haveW = true
		}
		a.wR, a.wT = rx, tx
	} else {
		a.haveW = false // unknown counters reset the base
	}
	if payloadKnown {
		if a.haveP {
			a.payloadR += deltaUint(a.pR, prx)
			a.payT += deltaUint(a.pT, ptx)
		} else {
			a.payloadR += deltaUint(0, prx)
			a.payT += deltaUint(0, ptx)
			a.haveP = true
		}
		a.pR, a.pT = prx, ptx
	} else {
		a.haveP = false
	}
}

func (a *streamAcc) usage() Usage {
	return Usage{WireRx: a.wireR, WireTx: a.wireT, PayloadRx: a.payloadR, PayloadTx: a.payT}
}

func streamKey(tid int64, agent string) string { return fmt.Sprintf("%d\x00%s", tid, agent) }

// rollupDoneUntil returns the first unfolded hour INDEX (0 = never folded).
func (s *Store) rollupDoneUntil() int64 {
	var done int64
	_ = s.db.QueryRow(`SELECT done_until FROM traffic_rollup_state WHERE id = 1`).Scan(&done)
	return done
}

func tunnelIDList(tunnelIDs []int64) string {
	ids := make([]string, 0, len(tunnelIDs))
	for _, id := range tunnelIDs {
		ids = append(ids, fmt.Sprintf("%d", id))
	}
	return strings.Join(ids, ",")
}

// RangeUsageDetailed sums usage over [from, to] across the given tunnels
// (empty slice = ALL tunnels): (all view, billable/iran view).
func (s *Store) RangeUsageDetailed(tunnelIDs []int64, from, to int64) (Usage, Usage, error) {
	all := map[string]*streamAcc{}
	iran := map[string]*streamAcc{}
	accOf := func(m map[string]*streamAcc, k string) *streamAcc {
		a := m[k]
		if a == nil {
			a = &streamAcc{}
			m[k] = a
		}
		return a
	}
	ingestRow := func(tid int64, agent, role string, rx, tx, prx, ptx int64, wk, pk bool) {
		k := streamKey(tid, agent)
		accOf(all, k).ingest(rx, tx, prx, ptx, wk, pk)
		if role == "iran" {
			accOf(iran, k).ingest(rx, tx, prx, ptx, wk, pk)
		}
	}

	done := s.rollupDoneUntil()
	foldEnd := done * 3600 // first unfolded timestamp
	if foldEnd > to {
		foldEnd = to
	}

	// 1. Folded hours [from, foldEnd).
	if foldEnd > from {
		q := fmt.Sprintf(`SELECT h.tunnel_id, h.agent_id, h.last_rx, h.last_tx, h.last_prx, h.last_ptx,
			h.wire_known, h.payload_known, COALESCE(a.role,'')
			FROM traffic_hourly h LEFT JOIN agents a ON a.id = h.agent_id
			WHERE h.hour >= ? AND h.hour < ?`)
		args := []any{from / 3600, foldEnd / 3600}
		if len(tunnelIDs) > 0 {
			q += fmt.Sprintf(` AND h.tunnel_id IN (%s)`, tunnelIDList(tunnelIDs))
		}
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return Usage{}, Usage{}, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var tid int64
				var agent, role string
				var lrx, ltx, lprx, lptx int64
				var wk, pk bool
				if err := rows.Scan(&tid, &agent, &lrx, &ltx, &lprx, &lptx, &wk, &pk, &role); err != nil {
					return err
				}
				ingestRow(tid, agent, role, lrx, ltx, lprx, lptx, wk, pk)
			}
			return rows.Err()
		}()
		if err != nil {
			return Usage{}, Usage{}, err
		}
	}

	// 2. Raw tail [max(from, foldEnd), to].
	rawFrom := from
	if foldEnd > rawFrom {
		rawFrom = foldEnd
	}
	if to > rawFrom {
		q := fmt.Sprintf(`SELECT ts.tunnel_id, ts.agent_id, ts.wire_rx, ts.wire_tx, ts.payload_rx, ts.payload_tx,
			ts.counters_known, ts.payload_known, COALESCE(a.role,'')
			FROM traffic_samples ts LEFT JOIN agents a ON a.id = ts.agent_id
			WHERE ts.at >= ? AND ts.at <= ?`)
		args := []any{rawFrom, to}
		if len(tunnelIDs) > 0 {
			q += fmt.Sprintf(` AND ts.tunnel_id IN (%s)`, tunnelIDList(tunnelIDs))
		}
		rows, err := s.db.Query(q, args...)
		if err != nil {
			return Usage{}, Usage{}, err
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				var tid int64
				var agent, role string
				var rx, tx, prx, ptx int64
				var wk, pk bool
				if err := rows.Scan(&tid, &agent, &rx, &tx, &prx, &ptx, &wk, &pk, &role); err != nil {
					return err
				}
				ingestRow(tid, agent, role, rx, tx, prx, ptx, wk, pk)
			}
			return rows.Err()
		}()
		if err != nil {
			return Usage{}, Usage{}, err
		}
	}

	var allU, iranU Usage
	for _, a := range all {
		allU = allU.add(a.usage())
	}
	for _, a := range iran {
		iranU = iranU.add(a.usage())
	}
	return allU, iranU, nil
}

// DatacenterUsage is one datacenter's aggregate over a range (the
// overview's per-Iran-datacenter block; only agents linked to a server
// in that datacenter contribute).
type DatacenterUsage struct {
	DatacenterID int64  `json:"datacenter_id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Usage        Usage  `json:"usage"`
}

// RangeUsageByDatacenter groups the range usage by the REPORTING agent's
// server's datacenter (real bytes; the caller filters to kind=iran for
// the billable view).
func (s *Store) RangeUsageByDatacenter(from, to int64) ([]DatacenterUsage, error) {
	type dcAcc struct {
		meta DatacenterUsage
		acc  map[string]*streamAcc
	}
	dcs := map[int64]*dcAcc{}
	get := func(id int64, name, kind string) *dcAcc {
		d := dcs[id]
		if d == nil {
			d = &dcAcc{meta: DatacenterUsage{DatacenterID: id, Name: name, Kind: kind}, acc: map[string]*streamAcc{}}
			dcs[id] = d
		}
		return d
	}
	done := s.rollupDoneUntil()
	foldEnd := done * 3600
	if foldEnd > to {
		foldEnd = to
	}
	ingest := func(dc *dcAcc, k string, rx, tx, prx, ptx int64, wk, pk bool) {
		a := dc.acc[k]
		if a == nil {
			a = &streamAcc{}
			dc.acc[k] = a
		}
		a.ingest(rx, tx, prx, ptx, wk, pk)
	}
	if foldEnd > from {
		rows, err := s.db.Query(`SELECT COALESCE(d.id,0), COALESCE(d.name,''), COALESCE(d.kind,''),
			h.tunnel_id, h.agent_id, h.last_rx, h.last_tx, h.last_prx, h.last_ptx, h.wire_known, h.payload_known
			FROM traffic_hourly h
			JOIN agents ag ON ag.id = h.agent_id
			JOIN servers s ON s.id = ag.server_id
			JOIN datacenters d ON d.id = s.datacenter_id
			WHERE h.hour >= ? AND h.hour < ?`, from/3600, foldEnd/3600)
		if err != nil {
			return nil, err
		}
		if err := func() error {
			defer rows.Close()
			for rows.Next() {
				var id int64
				var name, kind, agent string
				var tid int64
				var lrx, ltx, lprx, lptx int64
				var wk, pk bool
				if err := rows.Scan(&id, &name, &kind, &tid, &agent, &lrx, &ltx, &lprx, &lptx, &wk, &pk); err != nil {
					return err
				}
				ingest(get(id, name, kind), streamKey(tid, agent), lrx, ltx, lprx, lptx, wk, pk)
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	rawFrom := from
	if foldEnd > rawFrom {
		rawFrom = foldEnd
	}
	if to > rawFrom {
		rows, err := s.db.Query(`SELECT COALESCE(d.id,0), COALESCE(d.name,''), COALESCE(d.kind,''),
			ts.tunnel_id, ts.agent_id, ts.wire_rx, ts.wire_tx, ts.payload_rx, ts.payload_tx,
			ts.counters_known, ts.payload_known
			FROM traffic_samples ts
			JOIN agents ag ON ag.id = ts.agent_id
			JOIN servers s ON s.id = ag.server_id
			JOIN datacenters d ON d.id = s.datacenter_id
			WHERE ts.at >= ? AND ts.at <= ?`, rawFrom, to)
		if err != nil {
			return nil, err
		}
		if err := func() error {
			defer rows.Close()
			for rows.Next() {
				var id int64
				var name, kind, agent string
				var tid int64
				var rx, tx, prx, ptx int64
				var wk, pk bool
				if err := rows.Scan(&id, &name, &kind, &tid, &agent, &rx, &tx, &prx, &ptx, &wk, &pk); err != nil {
					return err
				}
				ingest(get(id, name, kind), streamKey(tid, agent), rx, tx, prx, ptx, wk, pk)
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	out := make([]DatacenterUsage, 0, len(dcs))
	for _, d := range dcs {
		for _, a := range d.acc {
			d.meta.Usage = d.meta.Usage.add(a.usage())
		}
		out = append(out, d.meta)
	}
	// Deterministic order: iran first, then by id.
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if (a.Kind == "iran") != (b.Kind == "iran") {
			return a.Kind == "iran"
		}
		return a.DatacenterID < b.DatacenterID
	})
	return out, nil
}

// ---- extra reads for the UI endpoints ---------------------------------

// ListAllTunnels returns every tunnel regardless of topology.
func (s *Store) ListAllTunnels() ([]Tunnel, error) {
	return scanList(s, `SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id,
		engine, engine_params, enabled, created_at FROM tunnels ORDER BY id`, func(t *Tunnel) []any {
		return []any{&t.ID, &t.TunnelKey, &t.TopologyID, &t.LocalServerID, &t.RemoteServerID,
			&t.Engine, &t.EngineParams, &t.Enabled, &t.CreatedAt}
	})
}

// RecentSamples returns the tunnel's last n raw readings, newest first
// (the "now" rate computation).
func (s *Store) RecentSamples(tunnelID int64, n int) ([]TrafficSample, error) {
	if n <= 0 || n > 16 {
		n = 2
	}
	return scanList(s, `SELECT tunnel_id, at, wire_rx, wire_tx, payload_rx, payload_tx,
		counters_known, agent_id, payload_known FROM traffic_samples
		WHERE tunnel_id = ? ORDER BY at DESC LIMIT ?`, func(t *TrafficSample) []any {
		return []any{&t.TunnelID, &t.At, &t.WireRx, &t.WireTx, &t.PayloadRx, &t.PayloadTx,
			&t.CountersKnown, &t.AgentID, &t.PayloadKnown}
	}, tunnelID, n)
}

// UsagePoint is one history bucket (deltas attributed to the bucket of
// the newer reading; counters chain across buckets per stream).
type UsagePoint struct {
	T int64 `json:"t"`
	Usage
}

// UsageSeriesDetailed returns the per-bucket usage series of ONE tunnel
// over [from, to] (bucketSec divides the window; >48 buckets coarsen the
// bucket so the answer stays bounded). Streams chain across buckets
// exactly like the range query; each delta is attributed to the bucket
// of the NEWER reading.
func (s *Store) UsageSeriesDetailed(tunnelID int64, from, to, bucketSec int64) ([]UsagePoint, error) {
	if bucketSec < 60 {
		bucketSec = 3600
	}
	if n := (to - from) / bucketSec; n > 48 {
		bucketSec = ((to-from)/48/bucketSec + 1) * bucketSec
	}
	bucketOf := func(at int64) int64 { return at / bucketSec * bucketSec }
	buckets := map[int64]Usage{}
	perStream := map[string][4]uint64{} // last wire rx/tx, payload rx/tx counters
	perKnown := map[string][2]bool{}    // wire/payload base valid
	consume := func(agent string, at int64, rx, tx, prx, ptx int64, wk, pk bool) {
		k := streamKey(tunnelID, agent)
		var prev [4]uint64
		var known [2]bool
		if v, ok := perStream[k]; ok {
			prev, known = v, perKnown[k]
		}
		var dWrx, dWtx, dPrx, dPtx uint64
		if wk {
			if known[0] {
				dWrx = deltaUint(int64(prev[0]), rx)
				dWtx = deltaUint(int64(prev[1]), tx)
			} else {
				dWrx = deltaUint(0, rx)
				dWtx = deltaUint(0, tx)
			}
			perStream[k] = [4]uint64{uint64(rx), uint64(tx), prev[2], prev[3]}
			perKnown[k] = [2]bool{true, known[1]}
		} else {
			perKnown[k] = [2]bool{false, known[1]}
		}
		if pk {
			if known[1] {
				dPrx = deltaUint(int64(prev[2]), prx)
				dPtx = deltaUint(int64(prev[3]), ptx)
			} else {
				dPrx = deltaUint(0, prx)
				dPtx = deltaUint(0, ptx)
			}
			perStream[k] = [4]uint64{perStream[k][0], perStream[k][1], uint64(prx), uint64(ptx)}
			perKnown[k] = [2]bool{perKnown[k][0], true}
		} else {
			perKnown[k] = [2]bool{perKnown[k][0], false}
		}
		if dWrx|dWtx|dPrx|dPtx != 0 {
			b := bucketOf(at)
			cur := buckets[b]
			cur.WireRx += dWrx
			cur.WireTx += dWtx
			cur.PayloadRx += dPrx
			cur.PayloadTx += dPtx
			buckets[b] = cur
		}
	}

	done := s.rollupDoneUntil()
	foldEnd := done * 3600
	if foldEnd > to {
		foldEnd = to
	}
	if foldEnd > from {
		rows, err := s.db.Query(`SELECT agent_id, hour, last_rx, last_tx, last_prx, last_ptx,
			wire_known, payload_known FROM traffic_hourly
			WHERE tunnel_id = ? AND hour >= ? AND hour < ? ORDER BY agent_id, hour`,
			tunnelID, from/3600, foldEnd/3600)
		if err != nil {
			return nil, err
		}
		if err := func() error {
			defer rows.Close()
			for rows.Next() {
				var agent string
				var hour, lrx, ltx, lprx, lptx int64
				var wk, pk bool
				if err := rows.Scan(&agent, &hour, &lrx, &ltx, &lprx, &lptx, &wk, &pk); err != nil {
					return err
				}
				consume(agent, hour*3600, lrx, ltx, lprx, lptx, wk, pk)
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	rawFrom := from
	if foldEnd > rawFrom {
		rawFrom = foldEnd
	}
	if to > rawFrom {
		rows, err := s.db.Query(`SELECT agent_id, at, wire_rx, wire_tx, payload_rx, payload_tx,
			counters_known, payload_known FROM traffic_samples
			WHERE tunnel_id = ? AND at >= ? AND at <= ? ORDER BY agent_id, at`,
			tunnelID, rawFrom, to)
		if err != nil {
			return nil, err
		}
		if err := func() error {
			defer rows.Close()
			for rows.Next() {
				var agent string
				var at, rx, tx, prx, ptx int64
				var wk, pk bool
				if err := rows.Scan(&agent, &at, &rx, &tx, &prx, &ptx, &wk, &pk); err != nil {
					return err
				}
				consume(agent, at, rx, tx, prx, ptx, wk, pk)
			}
			return rows.Err()
		}(); err != nil {
			return nil, err
		}
	}
	out := make([]UsagePoint, 0, len(buckets))
	for t, u := range buckets {
		out = append(out, UsagePoint{T: t, Usage: u})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

// TopologyMemberships returns the topologies whose membership includes
// the server (the agent-detail "my topologies" list).
func (s *Store) TopologyMemberships(serverID int64) ([]int64, error) {
	return scanList(s, `SELECT topology_id FROM topology_members WHERE server_id = ? ORDER BY topology_id`,
		func(id *int64) []any { return []any{id} }, serverID)
}

// AgentByServer returns the agent linked to a server ("" when none).
func (s *Store) AgentByServer(serverID int64) (Agent, error) {
	return scanOne(s, `SELECT id, COALESCE(server_id, 0), name, role, key_hash, status, version, hostname, public_ip, last_seen, applied_revision,
		arch, os, cpu_cores, cpu_pct, load1, mem_total_mb, mem_used_mb, disk_total_gb, disk_used_gb, uptime_s, virt, public_ip_override
		FROM agents WHERE server_id = ? ORDER BY id LIMIT 1`, func(a *Agent) []any {
		return []any{&a.ID, &a.ServerID, &a.Name, &a.Role, &a.KeyHash, &a.Status, &a.Version, &a.Hostname, &a.PublicIP, &a.LastSeen, &a.AppliedRevision,
			&a.Arch, &a.OS, &a.CPUCores, &a.CPUPct, &a.Load1, &a.MemTotalMB, &a.MemUsedMB, &a.DiskTotalGB, &a.DiskUsedGB, &a.UptimeS, &a.Virt, &a.PublicIPOverride}
	}, serverID)
}

// ---- batched writes (phase 12 TASK 3) ---------------------------------
//
// One heartbeat carries ~6 samples + ~6 proc upserts + ~2 engine rows;
// issued as individual Execs each held the single write connection in
// its own transaction (measured: UI p95 in the seconds under load).
// These batch variants wrap one heartbeat's writes in ONE transaction.

func (s *Store) AddTrafficSamplesBatch(samples []TrafficSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO traffic_samples (tunnel_id, at, wire_rx, wire_tx, payload_rx, payload_tx,
		counters_known, agent_id, payload_known) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, t := range samples {
		if _, err := stmt.Exec(t.TunnelID, t.At, t.WireRx, t.WireTx, t.PayloadRx, t.PayloadTx,
			t.CountersKnown, t.AgentID, t.PayloadKnown); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertTunnelProcBatch(procs []TunnelProcNow) error {
	if len(procs) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO tunnel_proc (tunnel_id, agent_id, cpu_pct, rss_bytes, processes, at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(tunnel_id, agent_id) DO UPDATE SET
		cpu_pct=excluded.cpu_pct, rss_bytes=excluded.rss_bytes, processes=excluded.processes, at=excluded.at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, p := range procs {
		if _, err := stmt.Exec(p.TunnelID, p.AgentID, p.CPUPct, p.RSSBytes, p.Processes, p.At); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertAgentEnginesBatch(engines []AgentEngine) error {
	if len(engines) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO agent_engines (agent_id, engine, installed, version, bytes_on_disk,
		desired, instances, last_error, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(agent_id, engine) DO UPDATE SET installed=excluded.installed, version=excluded.version,
		  bytes_on_disk=excluded.bytes_on_disk, instances=excluded.instances,
		  last_error=excluded.last_error, updated_at=excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range engines {
		ii, dd := 0, 0
		if e.Installed {
			ii = 1
		}
		if e.Desired {
			dd = 1
		}
		if _, err := stmt.Exec(e.AgentID, e.Engine, ii, e.Version, e.BytesOnDisk, dd, e.Instances, e.LastError, e.UpdatedAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}
