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
	// TargetPort is the desired port on the foreign side; 0 = same as
	// ListenPort (what the compiler assumed before the column existed).
	TargetPort int    `json:"target_port,omitempty"`
	Enabled    bool   `json:"enabled"`
	Note       string `json:"note,omitempty"`
	CreatedAt  int64  `json:"created_at"`
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
	return scanList(s, `SELECT id, kind, name, price_coefficient, COALESCE(datacenter_en, ''), created_at FROM datacenters ORDER BY id`,
		func(d *Datacenter) []any {
			return []any{&d.ID, &d.Kind, &d.Name, &d.PriceCoefficient, &d.DatacenterEN, &d.CreatedAt}
		})
}

func (s *Store) GetDatacenter(id int64) (Datacenter, error) {
	return scanOne(s, `SELECT id, kind, name, price_coefficient, COALESCE(datacenter_en, ''), created_at FROM datacenters WHERE id = ?`,
		func(d *Datacenter) []any {
			return []any{&d.ID, &d.Kind, &d.Name, &d.PriceCoefficient, &d.DatacenterEN, &d.CreatedAt}
		}, id)
}

// SetDatacenterEN sets the optional English display name (phase 26 §9).
// Empty clears it; anything above 100 characters is refused (the same bound
// as the token's datacenter).
func (s *Store) SetDatacenterEN(id int64, en string) error {
	if len(en) > 100 {
		return fmt.Errorf("datacenter_en out of range")
	}
	res, err := s.db.Exec(`UPDATE datacenters SET datacenter_en = ? WHERE id = ?`, en, id)
	if err != nil {
		return err
	}
	return requireRow(res)
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
	return scanList(s, `SELECT id, name, role, COALESCE(datacenter_id, 0), address, created_at FROM servers ORDER BY id`,
		func(sv *Server) []any {
			return []any{&sv.ID, &sv.Name, &sv.Role, &sv.DatacenterID, &sv.Address, &sv.CreatedAt}
		})
}

func (s *Store) GetServer(id int64) (Server, error) {
	return scanOne(s, `SELECT id, name, role, COALESCE(datacenter_id, 0), address, created_at FROM servers WHERE id = ?`,
		func(sv *Server) []any {
			return []any{&sv.ID, &sv.Name, &sv.Role, &sv.DatacenterID, &sv.Address, &sv.CreatedAt}
		}, id)
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
	return scanList(s, `SELECT id, name, enabled, created_at, health_check, failover FROM topologies ORDER BY id`,
		func(t *Topology) []any {
			return []any{&t.ID, &t.Name, &t.Enabled, &t.CreatedAt, &t.HealthCheck, &t.Failover}
		})
}

func (s *Store) GetTopology(id int64) (Topology, error) {
	return scanOne(s, `SELECT id, name, enabled, created_at, health_check, failover FROM topologies WHERE id = ?`,
		func(t *Topology) []any {
			return []any{&t.ID, &t.Name, &t.Enabled, &t.CreatedAt, &t.HealthCheck, &t.Failover}
		}, id)
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
	return scanOne(s, `SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at FROM tunnels WHERE id = ?`,
		func(t *Tunnel) []any {
			return []any{&t.ID, &t.TunnelKey, &t.TopologyID, &t.LocalServerID, &t.RemoteServerID, &t.Engine, &t.EngineParams, &t.Enabled, &t.CreatedAt}
		}, id)
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
	return scanList(s, `SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at
		FROM tunnels WHERE local_server_id = ? AND remote_server_id = ? ORDER BY id`, func(t *Tunnel) []any {
		return []any{&t.ID, &t.TunnelKey, &t.TopologyID, &t.LocalServerID, &t.RemoteServerID, &t.Engine, &t.EngineParams, &t.Enabled, &t.CreatedAt}
	}, localServerID, remoteServerID)
}

// TunnelsForServer lists tunnels where the server is either end.
func (s *Store) TunnelsForServer(serverID int64) ([]Tunnel, error) {
	return scanList(s, `SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at
		FROM tunnels WHERE local_server_id = ? OR remote_server_id = ? ORDER BY id`, func(t *Tunnel) []any {
		return []any{&t.ID, &t.TunnelKey, &t.TopologyID, &t.LocalServerID, &t.RemoteServerID, &t.Engine, &t.EngineParams, &t.Enabled, &t.CreatedAt}
	}, serverID, serverID)
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
	return scanList(s, `SELECT id, topology_id, local_server_id, remote_server_id, tunnel_id, weight
		FROM weight_policies WHERE topology_id = ? ORDER BY id`, func(w *WeightPolicy) []any {
		return []any{&w.ID, &w.TopologyID, &w.LocalServerID, &w.RemoteServerID, &w.TunnelID, &w.Weight}
	}, topologyID)
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
	if f.TargetPort < 0 || f.TargetPort > 65535 {
		return 0, fmt.Errorf("target_port out of range")
	}
	if f.Proto != "tcp" && f.Proto != "udp" {
		return 0, fmt.Errorf("proto must be tcp or udp")
	}
	if len(f.Note) > 500 || len(f.Target) > 255 {
		return 0, fmt.Errorf("note/target too long")
	}
	res, err := s.db.Exec(`INSERT INTO forward_rules
		(topology_id, local_server_id, remote_server_id, tunnel_id, listen_port, proto, target, target_port, enabled, note, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		f.TopologyID, f.LocalServerID, f.RemoteServerID, f.TunnelID,
		f.ListenPort, f.Proto, f.Target, f.TargetPort, boolInt(f.Enabled), f.Note, time.Now().Unix())
	if err != nil {
		return 0, wrapFK(err)
	}
	return lastInsertID(res)
}

func (s *Store) GetForwardRule(id int64) (ForwardRule, error) {
	return scanOne(s, `SELECT id, topology_id, local_server_id, remote_server_id, tunnel_id, listen_port, proto, target, target_port, enabled, note, created_at
		FROM forward_rules WHERE id = ?`, func(f *ForwardRule) []any {
		return []any{&f.ID, &f.TopologyID, &f.LocalServerID, &f.RemoteServerID, &f.TunnelID, &f.ListenPort, &f.Proto, &f.Target, &f.TargetPort, &f.Enabled, &f.Note, &f.CreatedAt}
	}, id)
}

func (s *Store) ListForwardRules(topologyID int64) ([]ForwardRule, error) {
	q := `SELECT id, topology_id, local_server_id, remote_server_id, tunnel_id, listen_port, proto, target, target_port, enabled, note, created_at
		FROM forward_rules ORDER BY id`
	var args []any
	if topologyID > 0 {
		q = `SELECT id, topology_id, local_server_id, remote_server_id, tunnel_id, listen_port, proto, target, target_port, enabled, note, created_at
			FROM forward_rules WHERE topology_id = ? ORDER BY id`
		args = []any{topologyID}
	}
	return scanList(s, q, func(f *ForwardRule) []any {
		return []any{&f.ID, &f.TopologyID, &f.LocalServerID, &f.RemoteServerID, &f.TunnelID, &f.ListenPort, &f.Proto, &f.Target, &f.TargetPort, &f.Enabled, &f.Note, &f.CreatedAt}
	}, args...)
}

func (s *Store) UpdateForwardRule(f ForwardRule) error {
	if f.ListenPort < 1 || f.ListenPort > 65535 {
		return fmt.Errorf("listen_port out of range")
	}
	if f.TargetPort < 0 || f.TargetPort > 65535 {
		return fmt.Errorf("target_port out of range")
	}
	res, err := s.db.Exec(`UPDATE forward_rules SET topology_id=?, local_server_id=?, remote_server_id=?, tunnel_id=?, listen_port=?, proto=?, target=?, target_port=?, enabled=?, note=?
		WHERE id = ?`,
		f.TopologyID, f.LocalServerID, f.RemoteServerID, f.TunnelID, f.ListenPort, f.Proto, f.Target, f.TargetPort, boolInt(f.Enabled), f.Note, f.ID)
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
	return scanList(s, `SELECT id, COALESCE(server_id, 0), name, role, country, key_hash, status, version, hostname, public_ip, last_seen, applied_revision,
		arch, os, cpu_cores, cpu_pct, load1, mem_total_mb, mem_used_mb, disk_total_gb, disk_used_gb, uptime_s, virt, public_ip_override, COALESCE(cpu_model, '')
		FROM agents ORDER BY id`, func(a *Agent) []any {
		return []any{&a.ID, &a.ServerID, &a.Name, &a.Role, &a.Country, &a.KeyHash, &a.Status, &a.Version, &a.Hostname, &a.PublicIP, &a.LastSeen, &a.AppliedRevision,
			&a.Arch, &a.OS, &a.CPUCores, &a.CPUPct, &a.Load1, &a.MemTotalMB, &a.MemUsedMB, &a.DiskTotalGB, &a.DiskUsedGB, &a.UptimeS, &a.Virt, &a.PublicIPOverride, &a.CPUModel}
	})
}

// AgentV2 reads the v2 session columns of one agent.
func (s *Store) AgentV2(id string) (AgentV2State, error) {
	return scanOne(s, `SELECT engine_catalog, desired_v2, applied_v2, last_ack, last_error, proto_version FROM agents WHERE id = ?`,
		func(v *AgentV2State) []any {
			return []any{&v.EngineCatalog, &v.DesiredV2, &v.AppliedV2, &v.LastAck, &v.LastError, &v.ProtoVersion}
		}, id)
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
	pending, err := scanOne(s, `SELECT desired_v2 FROM agents WHERE id = ?`,
		func(p *string) []any { return []any{p} }, id)
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
	return scanList(s, `SELECT id, at, actor, action, detail FROM audit_log ORDER BY id DESC LIMIT ?`,
		func(e *AuditEntry) []any {
			return []any{&e.ID, &e.At, &e.Actor, &e.Action, &e.Detail}
		}, limit)
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
// (fromAt, toAt]. Every reporter (agent) is an independent counter
// stream; unknown counters must never invent bytes: the wire view is
// gated by counters_known, the payload view by payload_known.
func (s *Store) TunnelUsage(tunnelID int64, fromAt, toAt int64) (Usage, error) {
	all, _, err := s.TunnelUsageDetailed(tunnelID, fromAt, toAt)
	return all, err
}

// TunnelUsageDetailed returns (all, iran) usage for one tunnel. `iran`
// sums only the streams reported by agents whose registry role is 'iran'
// — the Iran DC's billable view (download into Iran = the iran-side
// agent's wire rx). Legacy rows (agent_id ”) stay in the all view only.
func (s *Store) TunnelUsageDetailed(tunnelID int64, fromAt, toAt int64) (Usage, Usage, error) {
	rows, err := s.db.Query(`SELECT ts.at, ts.agent_id, COALESCE(a.role,''),
		ts.wire_rx, ts.wire_tx, ts.payload_rx, ts.payload_tx, ts.counters_known, ts.payload_known
		FROM traffic_samples ts LEFT JOIN agents a ON a.id = ts.agent_id
		WHERE ts.tunnel_id = ? AND ts.at > ? AND ts.at <= ? ORDER BY ts.agent_id, ts.at`, tunnelID, fromAt, toAt)
	if err != nil {
		return Usage{}, Usage{}, err
	}
	defer rows.Close()
	// Retention fallback: the raw samples older than the retention window
	// are pruned after being folded into traffic_hourly — when the window
	// holds NO raw rows, serve the period from the hourly rollups (the
	// prune must never make history read as zero bytes).
	hadRows := false
	var all, iran Usage
	acc := usageAccum{}
	iranAcc := usageAccum{}
	curAgent, curRole := "", ""
	for rows.Next() {
		hadRows = true
		var at, rx, tx, prx, ptx int64
		var agent, role string
		var wireKnown, payloadKnown bool
		if err := rows.Scan(&at, &agent, &role, &rx, &tx, &prx, &ptx, &wireKnown, &payloadKnown); err != nil {
			return Usage{}, Usage{}, err
		}
		if agent != curAgent {
			// New reporter stream: flush the finished one, reset the bases.
			all = all.add(acc.usage)
			iran = iran.add(iranAcc.usage)
			acc, iranAcc = usageAccum{}, usageAccum{}
			curAgent, curRole = agent, role
		}
		acc.ingest(rx, tx, prx, ptx, wireKnown, payloadKnown)
		if curRole == "iran" {
			iranAcc.ingest(rx, tx, prx, ptx, wireKnown, payloadKnown)
		}
	}
	if err := rows.Err(); err != nil {
		return Usage{}, Usage{}, err
	}
	all = all.add(acc.usage)
	iran = iran.add(iranAcc.usage)
	if !hadRows {
		// The retention fallback: old periods read from the hourly
		// rollups (the raw rows were pruned after folding).
		return s.HourlyUsageDetailed([]int64{tunnelID}, fromAt, toAt)
	}
	return all, iran, nil
}

// usageAccum accumulates one reporter's delta stream with independent
// bases for the wire and payload views (a sample whose counters are
// unknown resets that view's base; the other view keeps accumulating).
type usageAccum struct {
	usage                            Usage
	prevRX, prevTX, prevPRX, prevPTX int64
	haveWire, havePayload            bool
}

func (a *usageAccum) ingest(rx, tx, prx, ptx int64, wireKnown, payloadKnown bool) {
	if wireKnown {
		if a.haveWire {
			a.usage.WireRx += deltaUint(a.prevRX, rx)
			a.usage.WireTx += deltaUint(a.prevTX, tx)
		} else {
			// First known sample of the stream/window: nothing to subtract,
			// so its counters are the usage (same zero-base rule as a reset).
			a.usage.WireRx += uint64(rx)
			a.usage.WireTx += uint64(tx)
			a.haveWire = true
		}
		a.prevRX, a.prevTX = rx, tx
	} else {
		a.haveWire = false // wire counters unreadable: reset the wire base
	}
	if payloadKnown {
		if a.havePayload {
			a.usage.PayloadRx += deltaUint(a.prevPRX, prx)
			a.usage.PayloadTx += deltaUint(a.prevPTX, ptx)
		} else {
			a.usage.PayloadRx += uint64(prx)
			a.usage.PayloadTx += uint64(ptx)
			a.havePayload = true
		}
		a.prevPRX, a.prevPTX = prx, ptx
	} else {
		a.havePayload = false
	}
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
	all, _, err := s.UsageForTunnelsDetailed(tunnelIDs, fromAt, toAt)
	return all, err
}

// UsageForTunnelsDetailed is UsageForTunnels with the Iran-side billable
// view summed across the same tunnels.
func (s *Store) UsageForTunnelsDetailed(tunnelIDs []int64, fromAt, toAt int64) (Usage, Usage, error) {
	var all, iran Usage
	for _, id := range tunnelIDs {
		u, ir, err := s.TunnelUsageDetailed(id, fromAt, toAt)
		if err != nil {
			return all, iran, err
		}
		all = all.add(u)
		iran = iran.add(ir)
	}
	return all, iran, nil
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

// SetAgentName renames an agent row (UI rename action).
func (s *Store) SetAgentName(id, name string) error {
	res, err := s.db.Exec(`UPDATE agents SET name = ? WHERE id = ?`, name, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}

// DeleteAgent removes an agent row entirely (UI delete action; the registry
// record and its credential are removed by the caller).
func (s *Store) DeleteAgent(id string) error {
	res, err := s.db.Exec(`DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}
