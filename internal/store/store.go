// Package store is the Quantum panel's SQLite persistence layer
// (modernc.org/sqlite, pure Go, WAL). See 00-ADMIN/DATA-MODEL.md for the
// schema spec and the legacy-JSON import contract.
//
// Ground rules encoded here: byte statistics are always REAL bytes (the
// Pasarguard coefficient and the price hint are never applied to them);
// tunnels.engine_params is opaque JSON that engines validate; schema
// changes are numbered migrations, never edits to applied ones.
package store

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Store wraps one SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the database at path, applies pragmas
// (WAL, foreign keys, busy timeout) and pending migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	// modernc/sqlite in WAL mode supports CONCURRENT READERS with a
	// single writer; busy_timeout absorbs write contention. A pool of 6
	// lets the UI read paths (12+ pollers) stop starving behind the
	// heartbeat writes (phase-12 soak: the 1-conn pool was the last
	// serialization point). :memory: must stay at 1 connection (each
	// extra connection would see its own empty database).
	if path == ":memory:" {
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(6)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// OpenInMemory opens a private in-memory database (tests).
func OpenInMemory() (*Store, error) { return Open(":memory:") }

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for read-only queries in tests.
func (s *Store) DB() *sql.DB { return s.db }

// migrations are applied in order; the slice index+1 is the version.
var migrations = []string{
	// 1: initial schema (2026-11, Phase 1A). See DATA-MODEL.md.
	`
CREATE TABLE datacenters (
  id INTEGER PRIMARY KEY,
  kind TEXT NOT NULL CHECK (kind IN ('iran','foreign')),
  name TEXT NOT NULL UNIQUE,
  price_coefficient REAL NOT NULL DEFAULT 1.0,
  created_at INTEGER NOT NULL
);
CREATE TABLE servers (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  role TEXT NOT NULL CHECK (role IN ('iran','foreign')),
  datacenter_id INTEGER REFERENCES datacenters(id),
  address TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE TABLE topologies (
  id INTEGER PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE TABLE topology_members (
  topology_id INTEGER NOT NULL REFERENCES topologies(id),
  server_id INTEGER NOT NULL REFERENCES servers(id),
  PRIMARY KEY (topology_id, server_id)
);
CREATE TABLE tunnels (
  id INTEGER PRIMARY KEY,
  tunnel_key TEXT NOT NULL UNIQUE,
  topology_id INTEGER NOT NULL REFERENCES topologies(id),
  local_server_id INTEGER NOT NULL REFERENCES servers(id),
  remote_server_id INTEGER NOT NULL REFERENCES servers(id),
  engine TEXT NOT NULL DEFAULT '',
  engine_params TEXT NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL
);
CREATE TABLE weight_policies (
  id INTEGER PRIMARY KEY,
  topology_id INTEGER NOT NULL REFERENCES topologies(id),
  local_server_id  INTEGER REFERENCES servers(id),
  remote_server_id INTEGER REFERENCES servers(id),
  tunnel_id        INTEGER REFERENCES tunnels(id),
  weight INTEGER NOT NULL DEFAULT 1,
  UNIQUE (topology_id, local_server_id, remote_server_id, tunnel_id)
);
CREATE TABLE traffic_samples (
  id INTEGER PRIMARY KEY,
  tunnel_id INTEGER NOT NULL REFERENCES tunnels(id),
  at INTEGER NOT NULL,
  wire_rx INTEGER NOT NULL, wire_tx INTEGER NOT NULL,
  payload_rx INTEGER NOT NULL, payload_tx INTEGER NOT NULL,
  counters_known INTEGER NOT NULL DEFAULT 1
);
CREATE INDEX idx_traffic_samples_tunnel_at ON traffic_samples(tunnel_id, at);
CREATE TABLE engine_overhead (
  engine TEXT PRIMARY KEY,
  samples INTEGER NOT NULL DEFAULT 0,
  wire_bytes INTEGER NOT NULL DEFAULT 0,
  payload_bytes INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL
);
CREATE TABLE pasarguard_nodes (
  id INTEGER PRIMARY KEY,
  server_id INTEGER NOT NULL REFERENCES servers(id),
  datacenter_id INTEGER NOT NULL REFERENCES datacenters(id),
  name TEXT NOT NULL,
  coefficient REAL NOT NULL DEFAULT 1.0,
  UNIQUE (server_id, datacenter_id)
);
CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  server_id INTEGER REFERENCES servers(id),
  name TEXT NOT NULL DEFAULT '',
  role TEXT NOT NULL DEFAULT '',
  key_hash TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'offline',
  version TEXT NOT NULL DEFAULT '',
  hostname TEXT NOT NULL DEFAULT '',
  public_ip TEXT NOT NULL DEFAULT '',
  last_seen INTEGER NOT NULL DEFAULT 0,
  applied_revision TEXT NOT NULL DEFAULT ''
);
CREATE TABLE audit_log (
  id INTEGER PRIMARY KEY,
  at INTEGER NOT NULL,
  actor TEXT NOT NULL,
  action TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX idx_audit_log_at ON audit_log(at);
`,
	// 2: Phase 2B management-plane additions. Forward rules are panel-side
	// policy until the protocol grows DesiredV2.Forwards (see
	// 00-ADMIN/PROTOCOL-REQUESTS.md); agents gain the v2 session state the
	// compiler and report ingester need; topologies gain the balancing
	// defaults the agents enforce locally.
	`
CREATE TABLE forward_rules (
  id INTEGER PRIMARY KEY,
  topology_id INTEGER REFERENCES topologies(id),
  local_server_id INTEGER NOT NULL REFERENCES servers(id),
  remote_server_id INTEGER REFERENCES servers(id),
  tunnel_id INTEGER REFERENCES tunnels(id),
  listen_port INTEGER NOT NULL,
  proto TEXT NOT NULL CHECK (proto IN ('tcp','udp')),
  target TEXT NOT NULL DEFAULT '',
  enabled INTEGER NOT NULL DEFAULT 1,
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE (topology_id, local_server_id, listen_port, proto)
);
CREATE INDEX idx_forward_rules_server ON forward_rules(local_server_id);
ALTER TABLE agents ADD COLUMN engine_catalog TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN desired_v2 TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN applied_v2 TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN last_ack TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN proto_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE topologies ADD COLUMN health_check TEXT NOT NULL DEFAULT '';
ALTER TABLE topologies ADD COLUMN failover TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_tunnels_topology ON tunnels(topology_id);
CREATE INDEX idx_servers_dc ON servers(datacenter_id);
`,
	// 3: Phase 3 PasarGuard integration. Settings hold the panel connection
	// (url plain; username/password AES-GCM sealed with the per-install
	// key file). pg_nodes maps (foreign server, iran datacenter) to the
	// PasarGuard node record Quantum manages; api_key_enc is sealed the
	// same way. drift_threshold_pct drives the usage cross-check flag.
	`
CREATE TABLE pasarguard_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL
);
CREATE TABLE pg_nodes (
  id INTEGER PRIMARY KEY,
  server_id INTEGER NOT NULL REFERENCES servers(id),
  datacenter_id INTEGER NOT NULL REFERENCES datacenters(id),
  pg_node_id INTEGER NOT NULL DEFAULT 0,
  api_key_enc TEXT NOT NULL DEFAULT '',
  server_ca TEXT NOT NULL DEFAULT '',
  node_port INTEGER NOT NULL DEFAULT 62050,
  note TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  UNIQUE (server_id, datacenter_id)
);
CREATE INDEX idx_pg_nodes_dc ON pg_nodes(datacenter_id);
INSERT INTO pasarguard_settings (key, value, updated_at) VALUES ('drift_threshold_pct', '20', strftime('%s','now'));
`,
	// 4: Phase 4 directional accounting. Both endpoints of a tunnel report
	// StateReportV2 under the same tunnel key, so traffic_samples gains
	// agent_id: each reporter is an independent counter stream and the
	// usage aggregation resets its base per stream (mixing two machines'
	// cumulative counters produced garbage deltas). payload_known makes
	// the payload byte view honest per sample (the legacy counters_known
	// only ever meant wire-known); legacy rows keep payload_known=1 which
	// matches the old behavior exactly (unknown engines reported zeros,
	// so nothing was invented). engine_overhead gains the tx direction.
	`
ALTER TABLE traffic_samples ADD COLUMN agent_id TEXT NOT NULL DEFAULT '';
ALTER TABLE traffic_samples ADD COLUMN payload_known INTEGER NOT NULL DEFAULT 1;
ALTER TABLE engine_overhead ADD COLUMN wire_tx_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE engine_overhead ADD COLUMN payload_tx_bytes INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_traffic_samples_tunnel_agent_at ON traffic_samples(tunnel_id, agent_id, at);
`,
	// 5: Phase 5 R5 engine artifact store. One row per signed engine
	// binary artifact the operator uploaded; the file itself lives in
	// <configDir>/engine-artifacts/ (0600), the row carries the pins the
	// agent enforces again on its side (sha256 + ed25519 signature — the
	// panel is NEVER trusted for integrity, R5). UNIQUE(engine, version,
	// goos, goarch) makes the duplicate rule a constraint: re-uploading
	// the same build is an update, a different hash for the same slot is
	// rejected by the handler (hash conflict).
	`
CREATE TABLE engine_artifacts (
  id INTEGER PRIMARY KEY,
  engine TEXT NOT NULL,
  version TEXT NOT NULL,
  goos TEXT NOT NULL DEFAULT 'linux',
  goarch TEXT NOT NULL,
  artifact TEXT NOT NULL UNIQUE,
  sha256 TEXT NOT NULL,
  size INTEGER NOT NULL,
  signature TEXT NOT NULL,
  uploader TEXT NOT NULL DEFAULT '',
  uploaded_at INTEGER NOT NULL,
  UNIQUE (engine, version, goos, goarch)
);
CREATE INDEX idx_engine_artifacts_engine ON engine_artifacts(engine, goarch);
`,
	// 6: Phase 11 UI data. The dashboard/agent/topology/tunnel views need
	// DATA that did not exist in the store before:
	//   - agents gain the ResourceReport the v2 heartbeat already carried
	//     (it was received and DISCARDED) plus arch/os (hello) and the
	//     manual public-IP override the UI edits;
	//   - agent_engines persists the per-agent engine installation state
	//     (installed/version/bytes/desired/instances/last error) the agent
	//     now reports in engine_status[];
	//   - tunnel_proc is the LATEST per-(tunnel,agent) process sample
	//     (upsert — not a history: the UI shows "now");
	//   - tunnel_events is a bounded per-tunnel state-change + error log
	//     (pruned to 7 days);
	//   - idx_traffic_samples_at speeds the "current partial hour" raw
	//     tail scan the overview does on every poll.
	`
ALTER TABLE agents ADD COLUMN arch TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN os TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN cpu_cores INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN cpu_pct REAL NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN load1 REAL NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN mem_total_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN mem_used_mb INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN disk_total_gb REAL NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN disk_used_gb REAL NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN uptime_s INTEGER NOT NULL DEFAULT 0;
ALTER TABLE agents ADD COLUMN virt TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN public_ip_override TEXT NOT NULL DEFAULT '';
CREATE TABLE agent_engines (
  agent_id TEXT NOT NULL,
  engine TEXT NOT NULL,
  installed INTEGER NOT NULL DEFAULT 0,
  version TEXT NOT NULL DEFAULT '',
  bytes_on_disk INTEGER NOT NULL DEFAULT 0,
  desired INTEGER NOT NULL DEFAULT 0,
  instances INTEGER NOT NULL DEFAULT 0,
  last_error TEXT NOT NULL DEFAULT '',
  updated_at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (agent_id, engine)
);
CREATE TABLE tunnel_proc (
  tunnel_id INTEGER NOT NULL,
  agent_id TEXT NOT NULL,
  cpu_pct REAL NOT NULL DEFAULT 0,
  rss_bytes INTEGER NOT NULL DEFAULT 0,
  processes INTEGER NOT NULL DEFAULT 0,
  at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (tunnel_id, agent_id)
);
CREATE TABLE tunnel_events (
  id INTEGER PRIMARY KEY,
  tunnel_id INTEGER NOT NULL,
  at INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'state',
  from_state TEXT NOT NULL DEFAULT '',
  to_state TEXT NOT NULL DEFAULT '',
  code TEXT NOT NULL DEFAULT '',
  text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_tunnel_events_tunnel ON tunnel_events(tunnel_id, at);
CREATE INDEX idx_traffic_samples_at ON traffic_samples(at);
`,
	// 7: Phase 14 port inventory. Three additive pieces:
	//   - forward_rules.target_port: the desired foreign-side port of a
	//     forward rule (the wire protocol already carried it; 0 = same as
	//     listen_port, which is what the compiler used to hardcode);
	//   - port_allocations: the panel's own port ledger per server — what
	//     the allocator reserved, for whom (owner_key), so create/update
	//     flows cannot double-book a listen port;
	//   - agent_listening: the LATEST listening-port inventory each agent
	//     reported in its v2 heartbeat (ResourceReport.Listening was
	//     received and discarded before). It feeds both the allocator's
	//     reservation set and the topology port view. One row per
	//     (agent, proto, port) — a replace-per-heartbeat snapshot, not a
	//     history.
	`
ALTER TABLE forward_rules ADD COLUMN target_port INTEGER NOT NULL DEFAULT 0;
CREATE TABLE port_allocations (
  id INTEGER PRIMARY KEY,
  server_id INTEGER NOT NULL REFERENCES servers(id),
  port INTEGER NOT NULL,
  proto TEXT NOT NULL DEFAULT 'tcp',
  purpose TEXT NOT NULL DEFAULT '',
  owner_key TEXT NOT NULL DEFAULT '',
  allocated_at INTEGER NOT NULL,
  UNIQUE(server_id, port)
);
CREATE INDEX idx_port_allocations_server ON port_allocations(server_id);
CREATE TABLE agent_listening (
  agent_id TEXT NOT NULL,
  proto TEXT NOT NULL,
  port INTEGER NOT NULL,
  proc TEXT NOT NULL DEFAULT '',
  seen_at INTEGER NOT NULL,
  PRIMARY KEY(agent_id, proto, port)
);
`,
	// 8: Agent hosting country (UI review round). The operator picks the
	// country of a FOREIGN agent when minting its enrollment token; the
	// agents table shows it next to the role. Additive column; empty =
	// unknown or a pre-country agent.
	`
ALTER TABLE agents ADD COLUMN country TEXT NOT NULL DEFAULT '';
`,
	// 9: Agent CPU model (UI review round). The agent's resource report
	// carries the CPU model string read from /proc/cpuinfo (its model
	// name usually includes the "@ X.XXGHz" frequency); the agents
	// detail page extracts the GHz from it. Additive column; empty =
	// a pre-model report. New batch so it also applies to existing DBs.
	`
ALTER TABLE agents ADD COLUMN cpu_model TEXT NOT NULL DEFAULT '';
`,
	// 10: Phase 26 bilingual datacenter names (UI-API-REQUESTS §9). Additive:
	// old binaries ignore the column; DEFAULT '' keeps NOT NULL satisfied on
	// reopen and makes the change downgrade-safe (pre-26 binaries read the
	// table without the column and never write it). Renumbered from main's
	// migration 8 in the merge — this line's country/cpu_model migrations
	// (8, 9) shipped first on the demo line.
	`
ALTER TABLE datacenters ADD COLUMN datacenter_en TEXT NOT NULL DEFAULT '';
`,
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("migrations table: %w", err)
	}
	var current int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	for i, stmt := range migrations {
		version := i + 1
		if version <= current {
			continue
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(stmt); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%s','now'))`, version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d bookkeeping: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Version returns the applied schema version.
func (s *Store) Version() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&v)
	return v, err
}

// JournalMode reports the current journal mode ("wal" expected).
func (s *Store) JournalMode() (string, error) {
	var mode string
	err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	return mode, err
}
