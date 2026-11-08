package store

import (
	"database/sql"
	"errors"
	"time"
)

// ErrNotFound is returned by getters that miss.
var ErrNotFound = errors.New("store: not found")

func wrapNotFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Datacenter is an Iran or foreign datacenter. PriceCoefficient is a
// pricing hint; it is NEVER applied to byte statistics.
type Datacenter struct {
	ID               int64   `json:"id"`
	Kind             string  `json:"kind"` // "iran" | "foreign"
	Name             string  `json:"name"`
	PriceCoefficient float64 `json:"price_coefficient"`
	CreatedAt        int64   `json:"created_at"`
}

// Server is one tunnel endpoint machine.
type Server struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Role         string `json:"role"`          // "iran" | "foreign"
	DatacenterID int64  `json:"datacenter_id"` // 0 = unset
	Address      string `json:"address"`
	CreatedAt    int64  `json:"created_at"`
}

// Topology groups servers that tunnel through each other.
type Topology struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Enabled gates tunnel generation and desired-state compilation.
	Enabled   bool  `json:"enabled"`
	CreatedAt int64 `json:"created_at"`
	// HealthCheck/Failover are JSON blobs (protocol.HealthCheckParams /
	// protocol.FailoverRules shapes) the desired-state compiler copies
	// into BalancingPolicy; the agents enforce them locally.
	HealthCheck string `json:"health_check,omitempty"`
	Failover    string `json:"failover,omitempty"`
}

// Tunnel is one tunnel instance. EngineParams is opaque JSON the named
// engine validates.
type Tunnel struct {
	ID             int64  `json:"id"`
	TunnelKey      string `json:"tunnel_key"`
	TopologyID     int64  `json:"topology_id"`
	LocalServerID  int64  `json:"local_server_id"`
	RemoteServerID int64  `json:"remote_server_id"`
	Engine         string `json:"engine"`
	EngineParams   string `json:"engine_params"` // opaque JSON, engine-validated
	Enabled        bool   `json:"enabled"`
	CreatedAt      int64  `json:"created_at"`
}

// TrafficSample is one per-tunnel counter reading: BOTH byte views,
// always real bytes.
type TrafficSample struct {
	TunnelID      int64  `json:"tunnel_id"`
	At            int64  `json:"at"`
	WireRx        uint64 `json:"wire_rx"`
	WireTx        uint64 `json:"wire_tx"`
	PayloadRx     uint64 `json:"payload_rx"`
	PayloadTx     uint64 `json:"payload_tx"`
	CountersKnown bool   `json:"counters_known"` // legacy: wire counters known
	// AgentID is the reporting agent ("" = legacy rows); payload_known
	// gates the payload byte view independently of the wire view.
	AgentID      string `json:"agent_id,omitempty"`
	PayloadKnown bool   `json:"payload_known"`
}

// PasarguardNode is one Pasarguard node on a foreign server serving one
// Iran datacenter. Coefficient applies to user quota INSIDE Pasarguard
// only.
type PasarguardNode struct {
	ID           int64   `json:"id"`
	ServerID     int64   `json:"server_id"`
	DatacenterID int64   `json:"datacenter_id"`
	Name         string  `json:"name"`
	Coefficient  float64 `json:"coefficient"`
}

// Agent is one enrolled endpoint agent.
type Agent struct {
	ID              string `json:"id"`
	ServerID        int64  `json:"server_id"`
	Name            string `json:"name"`
	Role            string `json:"role"`
	KeyHash         string `json:"-"`
	Status          string `json:"status"`
	Version         string `json:"version"`
	Hostname        string `json:"hostname"`
	PublicIP        string `json:"public_ip"`
	LastSeen        int64  `json:"last_seen"`
	AppliedRevision string `json:"applied_revision"`
	// Phase 11: the v2 report's ResourceReport + hello identity, now
	// persisted (migration 6). PublicIPOverride is the manual override
	// the UI edits; effective IP = override when non-empty, else PublicIP.
	Arch             string  `json:"arch,omitempty"`
	OS               string  `json:"os,omitempty"`
	CPUCores         int     `json:"cpu_cores,omitempty"`
	CPUPct           float64 `json:"cpu_pct,omitempty"`
	Load1            float64 `json:"load1,omitempty"`
	MemTotalMB       int64   `json:"mem_total_mb,omitempty"`
	MemUsedMB        int64   `json:"mem_used_mb,omitempty"`
	DiskTotalGB      float64 `json:"disk_total_gb,omitempty"`
	DiskUsedGB       float64 `json:"disk_used_gb,omitempty"`
	UptimeS          int64   `json:"uptime_s,omitempty"`
	Virt             string  `json:"virt,omitempty"`
	PublicIPOverride string  `json:"public_ip_override,omitempty"`
}

// AuditEntry is one audit-log record.
type AuditEntry struct {
	ID     int64  `json:"id"`
	At     int64  `json:"at"`
	Actor  string `json:"actor"`
	Action string `json:"action"`
	Detail string `json:"detail"`
}

func now() int64 { return time.Now().Unix() }

// --- datacenters ---

// UpsertDatacenter inserts or updates by name.
func (s *Store) UpsertDatacenter(dc Datacenter) (int64, error) {
	if dc.CreatedAt == 0 {
		dc.CreatedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO datacenters (kind, name, price_coefficient, created_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET kind=excluded.kind, price_coefficient=excluded.price_coefficient`,
		dc.Kind, dc.Name, dc.PriceCoefficient, dc.CreatedAt)
	if err != nil {
		return 0, err
	}
	_ = res
	// Lookup by the unique field, never lastInsertID: on the UPDATE path of
	// an upsert, modernc/sqlite reports the PREVIOUS insert's rowid, which
	// silently mis-parents everything created afterwards.
	return s.lookupID(`SELECT id FROM datacenters WHERE name = ?`, dc.Name)
}

// DatacenterByName returns the datacenter with that name.
func (s *Store) DatacenterByName(name string) (Datacenter, error) {
	row := s.db.QueryRow(`SELECT id, kind, name, price_coefficient, created_at FROM datacenters WHERE name = ?`, name)
	var dc Datacenter
	err := row.Scan(&dc.ID, &dc.Kind, &dc.Name, &dc.PriceCoefficient, &dc.CreatedAt)
	return dc, wrapNotFound(err)
}

// --- servers ---

// UpsertServer inserts or updates by name.
func (s *Store) UpsertServer(sv Server) (int64, error) {
	if sv.CreatedAt == 0 {
		sv.CreatedAt = now()
	}
	res, err := s.db.Exec(`INSERT INTO servers (name, role, datacenter_id, address, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET role=excluded.role, datacenter_id=excluded.datacenter_id, address=excluded.address`,
		sv.Name, sv.Role, nullInt(sv.DatacenterID), sv.Address, sv.CreatedAt)
	if err != nil {
		return 0, err
	}
	_ = res // see the lookupID note in UpsertDatacenter
	return s.lookupID(`SELECT id FROM servers WHERE name = ?`, sv.Name)
}

// ServerByName returns the server with that name.
func (s *Store) ServerByName(name string) (Server, error) {
	row := s.db.QueryRow(`SELECT id, name, role, COALESCE(datacenter_id, 0), address, created_at FROM servers WHERE name = ?`, name)
	var sv Server
	err := row.Scan(&sv.ID, &sv.Name, &sv.Role, &sv.DatacenterID, &sv.Address, &sv.CreatedAt)
	return sv, wrapNotFound(err)
}

// --- topologies ---

// UpsertTopology inserts or updates a topology by name and returns its id.
func (s *Store) UpsertTopology(t Topology) (int64, error) {
	if t.CreatedAt == 0 {
		t.CreatedAt = now()
	}
	_, err := s.db.Exec(`INSERT INTO topologies (name, enabled, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET enabled=excluded.enabled`,
		t.Name, boolInt(t.Enabled), t.CreatedAt)
	if err != nil {
		return 0, err
	}
	return s.lookupID(`SELECT id FROM topologies WHERE name = ?`, t.Name)
}

// AddTopologyMember adds a server to a topology (idempotent).
func (s *Store) AddTopologyMember(topologyID, serverID int64) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO topology_members (topology_id, server_id) VALUES (?, ?)`,
		topologyID, serverID)
	return err
}

// TopologyMembers lists the member server ids of a topology.
func (s *Store) TopologyMembers(topologyID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT server_id FROM topology_members WHERE topology_id = ? ORDER BY server_id`, topologyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// --- tunnels ---

// UpsertTunnel inserts or updates a tunnel by tunnel_key.
func (s *Store) UpsertTunnel(t Tunnel) (int64, error) {
	if t.CreatedAt == 0 {
		t.CreatedAt = now()
	}
	if t.EngineParams == "" {
		t.EngineParams = "{}"
	}
	res, err := s.db.Exec(`INSERT INTO tunnels (tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tunnel_key) DO UPDATE SET topology_id=excluded.topology_id,
		  local_server_id=excluded.local_server_id, remote_server_id=excluded.remote_server_id,
		  engine=excluded.engine, engine_params=excluded.engine_params, enabled=excluded.enabled`,
		t.TunnelKey, t.TopologyID, t.LocalServerID, t.RemoteServerID, t.Engine, t.EngineParams, boolInt(t.Enabled), t.CreatedAt)
	if err != nil {
		return 0, err
	}
	_ = res // see the lookupID note in UpsertDatacenter
	return s.lookupID(`SELECT id FROM tunnels WHERE tunnel_key = ?`, t.TunnelKey)
}

// lookupID runs a single-value id query with the shared not-found mapping.
func (s *Store) lookupID(query string, args ...any) (int64, error) {
	var id int64
	err := s.db.QueryRow(query, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// GetTunnelByKey returns the tunnel with that key.
func (s *Store) GetTunnelByKey(key string) (Tunnel, error) {
	row := s.db.QueryRow(`SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at
		FROM tunnels WHERE tunnel_key = ?`, key)
	var t Tunnel
	err := row.Scan(&t.ID, &t.TunnelKey, &t.TopologyID, &t.LocalServerID, &t.RemoteServerID, &t.Engine, &t.EngineParams, &t.Enabled, &t.CreatedAt)
	return t, wrapNotFound(err)
}

// ListTunnels lists the tunnels of one topology.
func (s *Store) ListTunnels(topologyID int64) ([]Tunnel, error) {
	rows, err := s.db.Query(`SELECT id, tunnel_key, topology_id, local_server_id, remote_server_id, engine, engine_params, enabled, created_at
		FROM tunnels WHERE topology_id = ? ORDER BY tunnel_key`, topologyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

// --- weight policies ---

// SetWeightPolicy upserts one weight rule. Any of local/remote/tunnel may
// be 0 (scope-wide). The most specific rule wins at read time; the store
// keeps them all.
func (s *Store) SetWeightPolicy(topologyID, localServerID, remoteServerID, tunnelID int64, weight int) error {
	// SQLite UNIQUE treats NULLs as distinct, so a plain ON CONFLICT upsert
	// piles up duplicate rows for tunnel-scoped policies (NULL pair columns)
	// and WeightFor's smaller-weight tie-break then keeps the OLD value
	// (phase-10 regression: re-running the wizard would never change a
	// tunnel weight upward). Delete the exact scope (IS = NULL-safe) first.
	if _, err := s.db.Exec(`DELETE FROM weight_policies
		WHERE topology_id = ? AND local_server_id IS ? AND remote_server_id IS ? AND tunnel_id IS ?`,
		topologyID, nullInt(localServerID), nullInt(remoteServerID), nullInt(tunnelID)); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO weight_policies (topology_id, local_server_id, remote_server_id, tunnel_id, weight)
		VALUES (?, ?, ?, ?, ?)`,
		topologyID, nullInt(localServerID), nullInt(remoteServerID), nullInt(tunnelID), weight)
	return err
}

// WeightFor resolves the effective weight for a tunnel: the most specific
// matching rule wins (tunnel, then server pair, then topology-wide);
// ties break toward the smaller weight; 1 when no rule matches. Only
// rules whose scope covers the tunnel are considered.
func (s *Store) WeightFor(topologyID, localServerID, remoteServerID, tunnelID int64) (int, error) {
	row := s.db.QueryRow(`SELECT weight FROM weight_policies
		WHERE topology_id = ?
		  AND (tunnel_id IS NULL OR tunnel_id = ?)
		  AND (local_server_id IS NULL OR local_server_id = ?)
		  AND (remote_server_id IS NULL OR remote_server_id = ?)
		ORDER BY CASE
		    WHEN tunnel_id = ? THEN 3
		    WHEN local_server_id = ? AND remote_server_id = ? THEN 2
		    ELSE 1
		  END DESC,
		  weight
		LIMIT 1`,
		topologyID, tunnelID, localServerID, remoteServerID, tunnelID, localServerID, remoteServerID)
	var w int
	err := row.Scan(&w)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	return w, err
}

// --- traffic samples ---

// AddTrafficSample records one counter reading (real bytes only).
// AgentID identifies the reporting agent: both endpoints of a tunnel
// report under the same tunnel key and each reporter is an independent
// counter stream (Phase-4; empty = legacy/unattributed).
func (s *Store) AddTrafficSample(t TrafficSample) error {
	_, err := s.db.Exec(`INSERT INTO traffic_samples (tunnel_id, at, wire_rx, wire_tx, payload_rx, payload_tx, counters_known, agent_id, payload_known)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TunnelID, t.At, int64(t.WireRx), int64(t.WireTx), int64(t.PayloadRx), int64(t.PayloadTx), boolInt(t.CountersKnown), t.AgentID, boolInt(t.PayloadKnown))
	return err
}

// LatestTrafficSample returns the newest sample of a tunnel.
func (s *Store) LatestTrafficSample(tunnelID int64) (TrafficSample, error) {
	row := s.db.QueryRow(`SELECT tunnel_id, at, wire_rx, wire_tx, payload_rx, payload_tx, counters_known
		FROM traffic_samples WHERE tunnel_id = ? ORDER BY at DESC, id DESC LIMIT 1`, tunnelID)
	var t TrafficSample
	var wireRx, wireTx, payloadRx, payloadTx int64
	err := row.Scan(&t.TunnelID, &t.At, &wireRx, &wireTx, &payloadRx, &payloadTx, &t.CountersKnown)
	t.WireRx, t.WireTx = uint64(wireRx), uint64(wireTx)
	t.PayloadRx, t.PayloadTx = uint64(payloadRx), uint64(payloadTx)
	return t, wrapNotFound(err)
}

// --- engine overhead ---

// ObserveEngineOverhead folds one engine's wire/payload pairs into the
// per-engine running totals, per direction. Real bytes in, real bytes
// out; a direction with unknown payload contributes nothing (never
// guessed).
func (s *Store) ObserveEngineOverhead(engine string, wireRx, payloadRx, wireTx, payloadTx uint64) error {
	if payloadRx == 0 && payloadTx == 0 {
		return nil // nothing derivable; never guess
	}
	_, err := s.db.Exec(`INSERT INTO engine_overhead (engine, samples, wire_bytes, payload_bytes, wire_tx_bytes, payload_tx_bytes, updated_at)
		VALUES (?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT(engine) DO UPDATE SET
		  samples = samples + 1,
		  wire_bytes = wire_bytes + excluded.wire_bytes,
		  payload_bytes = payload_bytes + excluded.payload_bytes,
		  wire_tx_bytes = wire_tx_bytes + excluded.wire_tx_bytes,
		  payload_tx_bytes = payload_tx_bytes + excluded.payload_tx_bytes,
		  updated_at = excluded.updated_at`,
		engine, int64(wireRx), int64(payloadRx), int64(wireTx), int64(payloadTx), now())
	return err
}

// EngineOverheadRatio returns wire/payload for an engine; 0 = unknown.
func (s *Store) EngineOverheadRatio(engine string) (float64, error) {
	var wire, payload int64
	err := s.db.QueryRow(`SELECT wire_bytes, payload_bytes FROM engine_overhead WHERE engine = ?`, engine).
		Scan(&wire, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if payload <= 0 {
		return 0, nil
	}
	return float64(wire) / float64(payload), nil
}

// EngineOverheadRatioTx returns wire_tx/payload_tx for an engine
// (upload-direction overhead); 0 = unknown.
func (s *Store) EngineOverheadRatioTx(engine string) (float64, error) {
	var wire, payload int64
	err := s.db.QueryRow(`SELECT wire_tx_bytes, payload_tx_bytes FROM engine_overhead WHERE engine = ?`, engine).
		Scan(&wire, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if payload <= 0 {
		return 0, nil
	}
	return float64(wire) / float64(payload), nil
}

// SuggestedCoefficient returns price_coefficient × measured overhead for
// a datacenter+engine; 0 when the overhead is unknown (the caller shows
// UNKNOWN rather than inventing a number).
func (s *Store) SuggestedCoefficient(datacenterID int64, engine string) (float64, error) {
	var price float64
	err := s.db.QueryRow(`SELECT price_coefficient FROM datacenters WHERE id = ?`, datacenterID).Scan(&price)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	ratio, err := s.EngineOverheadRatio(engine)
	if err != nil || ratio == 0 {
		return 0, err
	}
	return price * ratio, nil
}

// --- pasarguard nodes ---

// UpsertPasarguardNode inserts or updates the node for (server, iran DC).
func (s *Store) UpsertPasarguardNode(n PasarguardNode) (int64, error) {
	_, err := s.db.Exec(`INSERT INTO pasarguard_nodes (server_id, datacenter_id, name, coefficient)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(server_id, datacenter_id) DO UPDATE SET name=excluded.name, coefficient=excluded.coefficient`,
		n.ServerID, n.DatacenterID, n.Name, n.Coefficient)
	if err != nil {
		return 0, err
	}
	return s.lookupID(`SELECT id FROM pasarguard_nodes WHERE server_id = ? AND datacenter_id = ?`,
		n.ServerID, n.DatacenterID)
}

// PasarguardNodesForServer lists the nodes of one foreign server.
func (s *Store) PasarguardNodesForServer(serverID int64) ([]PasarguardNode, error) {
	rows, err := s.db.Query(`SELECT id, server_id, datacenter_id, name, coefficient FROM pasarguard_nodes
		WHERE server_id = ? ORDER BY id`, serverID)
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

// --- agents ---

// UpsertAgent inserts or updates an enrolled agent by id.
func (s *Store) UpsertAgent(a Agent) error {
	_, err := s.db.Exec(`INSERT INTO agents (id, server_id, name, role, key_hash, status, version, hostname, public_ip, last_seen, applied_revision)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET server_id=excluded.server_id, name=excluded.name, role=excluded.role,
		  key_hash=excluded.key_hash, status=excluded.status, version=excluded.version, hostname=excluded.hostname,
		  public_ip=excluded.public_ip, last_seen=excluded.last_seen, applied_revision=excluded.applied_revision`,
		a.ID, nullInt(a.ServerID), a.Name, a.Role, a.KeyHash, a.Status, a.Version, a.Hostname, a.PublicIP, a.LastSeen, a.AppliedRevision)
	return err
}

// GetAgent returns one agent.
func (s *Store) GetAgent(id string) (Agent, error) {
	row := s.db.QueryRow(`SELECT id, COALESCE(server_id, 0), name, role, key_hash, status, version, hostname, public_ip, last_seen, applied_revision,
		arch, os, cpu_cores, cpu_pct, load1, mem_total_mb, mem_used_mb, disk_total_gb, disk_used_gb, uptime_s, virt, public_ip_override
		FROM agents WHERE id = ?`, id)
	var a Agent
	err := row.Scan(&a.ID, &a.ServerID, &a.Name, &a.Role, &a.KeyHash, &a.Status, &a.Version, &a.Hostname, &a.PublicIP, &a.LastSeen, &a.AppliedRevision,
		&a.Arch, &a.OS, &a.CPUCores, &a.CPUPct, &a.Load1, &a.MemTotalMB, &a.MemUsedMB, &a.DiskTotalGB, &a.DiskUsedGB, &a.UptimeS, &a.Virt, &a.PublicIPOverride)
	return a, wrapNotFound(err)
}

// --- audit log ---

// AddAudit appends one audit entry.
func (s *Store) AddAudit(e AuditEntry) error {
	if e.At == 0 {
		e.At = now()
	}
	_, err := s.db.Exec(`INSERT INTO audit_log (at, actor, action, detail) VALUES (?, ?, ?, ?)`,
		e.At, e.Actor, e.Action, e.Detail)
	return err
}

// AuditCount returns the number of audit entries (tests).
func (s *Store) AuditCount() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM audit_log`).Scan(&n)
	return n, err
}

// --- small helpers ---

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func lastInsertID(res sql.Result) (int64, error) {
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}
