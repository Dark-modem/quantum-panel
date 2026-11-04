// models2.go — Phase 2B management-plane store surface: forward rules,
// agent v2 session state (engine catalog, negotiated protocol version,
// pending DesiredV2 + last ack), the list/delete half of the CRUD the
// mesh REST API needs, and traffic usage aggregation.
//
// Ground rules carried over from models.go: byte statistics are always
// REAL bytes; unknown overhead stays 0 (never guessed); schema changes go
// through numbered migrations only.

package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ForwardRule is one declared port forward: the panel stores the POLICY;
// delivery to agents waits for DesiredV2.Forwards (PROTOCOL-REQUESTS.md).
type ForwardRule struct {
	ID             int64  `json:"id"`
	TopologyID     *int64 `json:"topology_id,omitempty"`
	LocalServerID  int64  `json:"local_server_id"`
	RemoteServerID *int64 `json:"remote_server_id,omitempty"`
	TunnelID       *int64 `json:"tunnel_id,omitempty"`
	ListenPort     int    `json:"listen_port"`
	Proto          string `json:"proto"`
	Target         string `json:"target,omitempty"`
	Enabled        bool   `json:"enabled"`
	Note           string `json:"note,omitempty"`
	CreatedAt      int64  `json:"created_at"`
}

// AgentV2State is the per-agent v2 session state the compiler and the
// report ingester maintain.
type AgentV2State struct {
	EngineCatalog string `json:"engine_catalog"` // JSON: the catalog the agent reported
	DesiredV2     string `json:"desired_v2"`     // pending DesiredV2 JSON ("" = none pending)
	AppliedV2     string `json:"applied_v2"`     // last ACKED DesiredV2 JSON ("" = none)
	LastAck       string `json:"last_ack"`       // JSON {revision, ok, error, at}
	LastError     string `json:"last_error"`
	ProtoVersion  int    `json:"proto_version"` // negotiated (hello envelope V), default 1
}

// ---- datacenters / servers: list, get, update, delete ------------------

func (s *Store) ListDatacenters() ([]Datacenter, error) {
	rows, err := s.db.Query(`SELECT id, kind, name, price_coefficient, created_at FROM datacenters ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Datacenter
	for rows.Next() {
		var d Datacenter
		if err := rows.Scan(&d.ID, &d.Kind, &d.Name, &d.PriceCoefficient, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDatacenter(id int64) (Datacenter, error) {
	var d Datacenter
	err := s.db.QueryRow(`SELECT id, kind, name, price_coefficient, created_at FROM datacenters WHERE id = ?`, id).
		Scan(&d.ID, &d.Kind, &d.Name, &d.PriceCoefficient, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// SetDatacenterPriceCoefficient updates the pricing HINT (never applied to
// bytes — DATA-MODEL ground rule 1).
func (s *Store) SetDatacenterPriceCoefficient(id int64, coefficient float64) error {
	if math.IsNaN(coefficient) || coefficient < 0 || coefficient > 1e6 {
		return fmt.Errorf("price coefficient out of range")
	}
	res, err := s.db.Exec(`UPDATE datacenters SET price_coefficient = ? WHERE id = ?`, coefficient, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

func (s *Store) DeleteDatacenter(id int64) error {
	res, err := s.db.Exec(`DELETE FROM datacenters WHERE id = ?`, id)
	if err != nil {
		return wrapFK(err)
	}
	return requireRow(res)
}

func (s *Store) ListServers() ([]Server, error) {
	rows, err := s.db.Query(`SELECT id, name, role, datacenter_id, address, created_at FROM servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Server
	for rows.Next() {
		var sv Server
		if err := rows.Scan(&sv.ID, &sv.Name, &sv.Role, &sv.DatacenterID, &sv.Address, &sv.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, sv)
	}
	return out, rows.Err()
}

func (s *Store) GetServer(id int64) (Server, error) {
	var sv Server
	err := s.db.QueryRow(`SELECT id, name, role, datacenter_id, address, created_at FROM servers WHERE id = ?`, id).
		Scan(&sv.ID, &sv.Name, &sv.Role, &sv.DatacenterID, &sv.Address, &sv.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return sv, ErrNotFound
	}
	return sv, err
}

func (s *Store) UpdateServer(sv Server) error {
	if strings.TrimSpace(sv.Name) == "" {
		return fmt.Errorf("server name required")
	}
	res, err := s.db.Exec(`UPDATE servers SET name = ?, role = ?, datacenter_id = ?, address = ? WHERE id = ?`,
		sv.Name, sv.Role, sv.DatacenterID, sv.Address, sv.ID)
	if err != nil {
		return wrapFK(err)
	}
	return requireRow(res)
}

func (s *Store) DeleteServer(id int64) error {
	res, err := s.db.Exec(`DELETE FROM servers WHERE id = ?`, id)
	if err != nil {
		return wrapFK(err)
	}
	return requireRow(res)
}

// ---- topologies: list, get, update, delete ------------------------------

func (s *Store) ListTopologies() ([]Topology, error) {
	rows, err := s.db.Query(`SELECT id, name, enabled, created_at, health_check, failover FROM topologies ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Topology
	for rows.Next() {
		var t Topology
		var hc, fo string
		if err := rows.Scan(&t.ID, &t.Name, &t.Enabled, &t.CreatedAt, &hc, &fo); err != nil {
			return nil, err
		}
		t.HealthCheck, t.Failover = hc, fo
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) GetTopology(id int64) (Topology, error) {
	var t Topology
	var hc, fo string
	err := s.db.QueryRow(`SELECT id, name, enabled, created_at, health_check, failover FROM topologies WHERE id = ?`, id).
		Scan(&t.ID, &t.Name, &t.Enabled, &t.CreatedAt, &hc, &fo)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.HealthCheck, t.Failover = hc, fo
	return t, err
}

// SetTopologyBalancing stores the balancing defaults (health check +
// failover JSON) the compiler copies into DesiredV2.Balancing.
func (s *Store) SetTopologyBalancing(id int64, healthCheckJSON, failoverJSON string) error {
	if err := validJSONOrEmpty(healthCheckJSON); err != nil {
		return fmt.Errorf("health_check: %v", err)
	}
	if err := validJSONOrEmpty(failoverJSON); err != nil {
		return fmt.Errorf("failover: %v", err)
	}
	res, err := s.db.Exec(`UPDATE topologies SET health_check = ?, failover = ? WHERE id = ?`, healthCheckJSON, failoverJSON, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// DeleteTopology removes the topology and everything scoped to it (members,
// tunnels, weights, forwards). Servers survive: they are inventory.
func (s *Store) DeleteTopology(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DELETE FROM weight_policies WHERE topology_id = ?`,
		`DELETE FROM forward_rules WHERE topology_id = ?`,
		`DELETE FROM tunnels WHERE topology_id = ?`,
		`DELETE FROM topology_members WHERE topology_id = ?`,
		`DELETE FROM topologies WHERE id = ?`,
	} {
		if _, err := tx.Exec(stmt, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ---- tunnels: get, update, delete ---------------------------------------

func (s *Store) GetTunnel(id int64) (Tunnel, error) {
	var t Tunnel
	var params string
	err := s.db.QueryRow(`SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at FROM tunnels WHERE id = ?`, id).
		Scan(&t.ID, &t.TunnelKey, &t.TopologyID, &t.LocalServerID, &t.RemoteServerID, &t.Engine, &params, &t.Enabled, &t.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	t.EngineParams = params
	return t, nil
}

func (s *Store) UpdateTunnel(t Tunnel) error {
	if err := validJSONOrEmpty(t.EngineParams); err != nil {
		return fmt.Errorf("engine_params: %v", err)
	}
	res, err := s.db.Exec(`UPDATE tunnels SET engine = ?, engine_params = ?, enabled = ? WHERE id = ?`,
		t.Engine, t.EngineParams, boolInt(t.Enabled), t.ID)
	if err != nil {
		return wrapFK(err)
	}
	return requireRow(res)
}

func (s *Store) DeleteTunnel(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM weight_policies WHERE tunnel_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM forward_rules WHERE tunnel_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM traffic_samples WHERE tunnel_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM tunnels WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// TunnelsBetween lists the tunnels of one (local, remote) server pair
// across every topology.
func (s *Store) TunnelsBetween(localServerID, remoteServerID int64) ([]Tunnel, error) {
	rows, err := s.db.Query(`SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at
		FROM tunnels WHERE local_server_id = ? AND remote_server_id = ? ORDER BY id`,
		localServerID, remoteServerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTunnels(rows)
}

// TunnelsForServer lists tunnels where the server is either end.
func (s *Store) TunnelsForServer(serverID int64) ([]Tunnel, error) {
	rows, err := s.db.Query(`SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at
		FROM tunnels WHERE local_server_id = ? OR remote_server_id = ? ORDER BY id`, serverID, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTunnels(rows)
}

func scanTunnels(rows *sql.Rows) ([]Tunnel, error) {
	var out []Tunnel
	for rows.Next() {
		var t Tunnel
		if err := rows.Scan(&t.ID, &t.TunnelKey, &t.TopologyID, &t.LocalServerID, &t.RemoteServerID, &t.Engine, &t.EngineParams, &t.Enabled, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- weight policies ----------------------------------------------------

// WeightPolicy is one stored weight row (resolved weights live in WeightFor).
type WeightPolicy struct {
	ID             int64  `json:"id"`
	TopologyID     int64  `json:"topology_id"`
	LocalServerID  *int64 `json:"local_server_id,omitempty"`
	RemoteServerID *int64 `json:"remote_server_id,omitempty"`
	TunnelID       *int64 `json:"tunnel_id,omitempty"`
	Weight         int    `json:"weight"`
}

func (s *Store) ListWeights(topologyID int64) ([]WeightPolicy, error) {
	rows, err := s.db.Query(`SELECT id, topology_id, local_server_id, remote_server_id, tunnel_id, weight
		FROM weight_policies WHERE topology_id = ? ORDER BY id`, topologyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WeightPolicy
	for rows.Next() {
		var w WeightPolicy
		if err := rows.Scan(&w.ID, &w.TopologyID, &w.LocalServerID, &w.RemoteServerID, &w.TunnelID, &w.Weight); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWeightPolicy(id int64) error {
	res, err := s.db.Exec(`DELETE FROM weight_policies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// ---- forward rules ------------------------------------------------------

func (s *Store) CreateForwardRule(f ForwardRule) (int64, error) {
	if f.ListenPort < 1 || f.ListenPort > 65535 {
		return 0, fmt.Errorf("listen_port out of range")
	}
	if f.Proto != "tcp" && f.Proto != "udp" {
		return 0, fmt.Errorf("proto must be tcp or udp")
	}
	if len(f.Note) > 500 || len(f.Target) > 255 {
		return 0, fmt.Errorf("note/target too long")
	}
	res, err := s.db.Exec(`INSERT INTO forward_rules
		(topology_id, local_server_id, remote_server_id, tunnel_id, listen_port, proto, target, enabled, note, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		f.TopologyID, f.LocalServerID, f.RemoteServerID, f.TunnelID,
		f.ListenPort, f.Proto, f.Target, boolInt(f.Enabled), f.Note, time.Now().Unix())
	if err != nil {
		return 0, wrapFK(err)
	}
	return lastInsertID(res)
}

func (s *Store) GetForwardRule(id int64) (ForwardRule, error) {
	var f ForwardRule
	err := s.db.QueryRow(`SELECT id, topology_id, local_server_id, remote_server_id, tunnel_id, listen_port, proto, target, enabled, note, created_at
		FROM forward_rules WHERE id = ?`, id).
		Scan(&f.ID, &f.TopologyID, &f.LocalServerID, &f.RemoteServerID, &f.TunnelID, &f.ListenPort, &f.Proto, &f.Target, &f.Enabled, &f.Note, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

func (s *Store) ListForwardRules(topologyID int64) ([]ForwardRule, error) {
	var rows *sql.Rows
	var err error
	if topologyID > 0 {
		rows, err = s.db.Query(`SELECT id, topology_id, local_server_id, remote_server_id, tunnel_id, listen_port, proto, target, enabled, note, created_at
			FROM forward_rules WHERE topology_id = ? ORDER BY id`, topologyID)
	} else {
		rows, err = s.db.Query(`SELECT id, topology_id, local_server_id, remote_server_id, tunnel_id, listen_port, proto, target, enabled, note, created_at
			FROM forward_rules ORDER BY id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ForwardRule
	for rows.Next() {
		var f ForwardRule
		if err := rows.Scan(&f.ID, &f.TopologyID, &f.LocalServerID, &f.RemoteServerID, &f.TunnelID, &f.ListenPort, &f.Proto, &f.Target, &f.Enabled, &f.Note, &f.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) UpdateForwardRule(f ForwardRule) error {
	if f.ListenPort < 1 || f.ListenPort > 65535 {
		return fmt.Errorf("listen_port out of range")
	}
	res, err := s.db.Exec(`UPDATE forward_rules SET topology_id=?, local_server_id=?, remote_server_id=?, tunnel_id=?, listen_port=?, proto=?, target=?, enabled=?, note=?
		WHERE id = ?`,
		f.TopologyID, f.LocalServerID, f.RemoteServerID, f.TunnelID, f.ListenPort, f.Proto, f.Target, boolInt(f.Enabled), f.Note, f.ID)
	if err != nil {
		return wrapFK(err)
	}
	return requireRow(res)
}

func (s *Store) DeleteForwardRule(id int64) error {
	res, err := s.db.Exec(`DELETE FROM forward_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// ---- pasarguard nodes ----------------------------------------------------

func (s *Store) ListPasarguardNodes() ([]PasarguardNode, error) {
	rows, err := s.db.Query(`SELECT id, server_id, datacenter_id, name, coefficient FROM pasarguard_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PasarguardNode
	for rows.Next() {
		var n PasarguardNode
		if err := rows.Scan(&n.ID, &n.ServerID, &n.DatacenterID, &n.Name, &n.Coefficient); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) DeletePasarguardNode(id int64) error {
	res, err := s.db.Exec(`DELETE FROM pasarguard_nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// SetPasarguardCoefficient updates the Pasarguard-side quota coefficient
// (record only in this phase; Quantum byte stats are never multiplied).
func (s *Store) SetPasarguardCoefficient(id int64, coefficient float64) error {
	if math.IsNaN(coefficient) || coefficient <= 0 || coefficient > 1e6 {
		return fmt.Errorf("coefficient out of range")
	}
	res, err := s.db.Exec(`UPDATE pasarguard_nodes SET coefficient = ? WHERE id = ?`, coefficient, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// ---- agents: v2 state + linking ------------------------------------------

func (s *Store) ListAgents() ([]Agent, error) {
	rows, err := s.db.Query(`SELECT id, server_id, name, role, key_hash, status, version, hostname, public_ip, last_seen, applied_revision
		FROM agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.ID, &a.ServerID, &a.Name, &a.Role, &a.KeyHash, &a.Status, &a.Version, &a.Hostname, &a.PublicIP, &a.LastSeen, &a.AppliedRevision); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AgentV2 reads the v2 session columns of one agent.
func (s *Store) AgentV2(id string) (AgentV2State, error) {
	var v AgentV2State
	err := s.db.QueryRow(`SELECT engine_catalog, desired_v2, applied_v2, last_ack, last_error, proto_version FROM agents WHERE id = ?`, id).
		Scan(&v.EngineCatalog, &v.DesiredV2, &v.AppliedV2, &v.LastAck, &v.LastError, &v.ProtoVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// SetAgentV2Session updates the negotiated protocol version and the
// engine catalog the agent reported. catalog is stored verbatim after a
// size check (it is JSON the panel only forwards for display/validation).
func (s *Store) SetAgentV2Session(id string, protoVersion int, engineCatalogJSON string) error {
	if protoVersion != 1 && protoVersion != 2 {
		return fmt.Errorf("unsupported protocol version %d", protoVersion)
	}
	if len(engineCatalogJSON) > 64<<10 {
		return fmt.Errorf("engine catalog too large")
	}
	res, err := s.db.Exec(`UPDATE agents SET proto_version = ?, engine_catalog = ? WHERE id = ?`, protoVersion, engineCatalogJSON, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// SetPendingDesiredV2 stores the pending DesiredV2 JSON (overwrite = the
// last write wins, same as v1 desired) or clears it ("" after ack).
func (s *Store) SetPendingDesiredV2(id, desiredJSON string) error {
	if desiredJSON != "" && len(desiredJSON) > 1<<20 {
		return fmt.Errorf("desired state too large")
	}
	res, err := s.db.Exec(`UPDATE agents SET desired_v2 = ? WHERE id = ?`, desiredJSON, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// AckDesiredV2 records the ack result for a pending v2 desired revision:
// ok clears the pending row and moves applied_revision; failure keeps the
// pending row and records last_error (the compiler re-pushes on change).
func (s *Store) AckDesiredV2(id, revision string, ok bool, ackErr string) (bool, error) {
	var pending string
	err := s.db.QueryRow(`SELECT desired_v2 FROM agents WHERE id = ?`, id).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	matched := pending != "" && strings.Contains(pending, fmt.Sprintf("%q", revision))
	if !matched {
		return false, nil // stale or duplicate ack
	}
	ackJSON := fmt.Sprintf(`{"revision":%q,"ok":%t,"error":%q,"at":%d}`, revision, ok, ackErr, time.Now().Unix())
	if ok {
		// The acked document becomes the applied snapshot (the minimal-
		// revision diff compares against it) and pending clears.
		_, err = s.db.Exec(`UPDATE agents SET applied_v2 = desired_v2, desired_v2 = '', applied_revision = ?, last_ack = ?, last_error = '' WHERE id = ?`,
			revision, ackJSON, id)
	} else {
		_, err = s.db.Exec(`UPDATE agents SET last_ack = ?, last_error = ? WHERE id = ?`, ackJSON, ackErr, id)
	}
	return true, err
}

// LinkAgentServer attaches an enrolled agent to its configured server.
func (s *Store) LinkAgentServer(agentID string, serverID int64) error {
	res, err := s.db.Exec(`UPDATE agents SET server_id = ? WHERE id = ?`, serverID, agentID)
	if err != nil {
		return wrapFK(err)
	}
	return requireRow(res)
}

// MarkStaleAgents flips agents whose last_seen is older than staleAfter to
// offline (panel keeps every other field — an agent drop must never lose
// state). Returns the number of rows flipped.
func (s *Store) MarkStaleAgents(staleAfter time.Duration) (int64, error) {
	res, err := s.db.Exec(`UPDATE agents SET status = 'offline' WHERE status = 'online' AND last_seen > 0 AND last_seen < ?`,
		time.Now().Add(-staleAfter).Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- audit ----------------------------------------------------------------

// Audit is the paged audit reader for the mesh API.
func (s *Store) Audit(limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, at, actor, action, detail FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- traffic usage ---------------------------------------------------------

// Usage is an aggregated byte view over traffic_samples. Values are REAL
// bytes summed from consecutive-sample deltas; a counter reset (current <
// previous) contributes the current value with a zero base, mirroring the
// hub's rate derivation — a reboot can never double-count.
type Usage struct {
	WireRx    uint64 `json:"wire_rx"`
	WireTx    uint64 `json:"wire_tx"`
	PayloadRx uint64 `json:"payload_rx"`
	PayloadTx uint64 `json:"payload_tx"`
}

func (u Usage) add(o Usage) Usage {
	u.WireRx += o.WireRx
	u.WireTx += o.WireTx
	u.PayloadRx += o.PayloadRx
	u.PayloadTx += o.PayloadTx
	return u
}

// TunnelUsage sums the deltas of one tunnel's samples with `at` in
// (fromAt, toAt]. counters_known = 0 samples contribute nothing (unknown
// counters must never invent bytes).
func (s *Store) TunnelUsage(tunnelID int64, fromAt, toAt int64) (Usage, error) {
	rows, err := s.db.Query(`SELECT at, wire_rx, wire_tx, payload_rx, payload_tx, counters_known
		FROM traffic_samples WHERE tunnel_id = ? AND at > ? AND at <= ? ORDER BY at`, tunnelID, fromAt, toAt)
	if err != nil {
		return Usage{}, err
	}
	defer rows.Close()
	return aggregateSampleDeltas(rows)
}

func aggregateSampleDeltas(rows *sql.Rows) (Usage, error) {
	var total Usage
	var prevRX, prevTX, prevPRX, prevPTX int64
	have := false
	for rows.Next() {
		var at, rx, tx, prx, ptx int64
		var known bool
		if err := rows.Scan(&at, &rx, &tx, &prx, &ptx, &known); err != nil {
			return Usage{}, err
		}
		if !known {
			have = false // counters unreadable in this report: reset the base
			continue
		}
		if have {
			total.WireRx += deltaUint(prevRX, rx)
			total.WireTx += deltaUint(prevTX, tx)
			total.PayloadRx += deltaUint(prevPRX, prx)
			total.PayloadTx += deltaUint(prevPTX, ptx)
		} else {
			// First known sample in the window: nothing to subtract, so its
			// counters are the usage (same zero-base rule as a reset).
			total.WireRx += uint64(rx)
			total.WireTx += uint64(tx)
			total.PayloadRx += uint64(prx)
			total.PayloadTx += uint64(ptx)
		}
		prevRX, prevTX, prevPRX, prevPTX = rx, tx, prx, ptx
		have = true
	}
	return total, rows.Err()
}

func deltaUint(prev, cur int64) uint64 {
	if cur >= prev {
		return uint64(cur - prev)
	}
	return uint64(cur) // counter reset (reboot): zero base, never negative
}

// UsageForTunnels aggregates usage across a set of tunnels (server /
// datacenter / topology views).
func (s *Store) UsageForTunnels(tunnelIDs []int64, fromAt, toAt int64) (Usage, error) {
	var total Usage
	for _, id := range tunnelIDs {
		u, err := s.TunnelUsage(id, fromAt, toAt)
		if err != nil {
			return total, err
		}
		total = total.add(u)
	}
	return total, nil
}

// PruneTrafficSamples drops samples older than keepBefore (retention).
func (s *Store) PruneTrafficSamples(keepBefore int64) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM traffic_samples WHERE at < ?`, keepBefore)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- helpers ---------------------------------------------------------------

func requireRow(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// wrapFK turns FOREIGN KEY constraint failures into a user-actionable
// error (the REST layer maps it to 409).
func wrapFK(err error) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key") {
		return fmt.Errorf("referenced by other records (delete or re-link them first): %v", err)
	}
	return err
}

func validJSONOrEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return validJSON(s)
}

// validJSON reports whether s parses as a JSON value.
func validJSON(s string) error {
	var v any
	return json.Unmarshal([]byte(s), &v)
}
