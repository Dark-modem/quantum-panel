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
	// modernc/sqlite serializes writes per connection; a small pool is
	// enough and avoids SQLITE_BUSY under concurrent writers.
	db.SetMaxOpenConns(1)
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
ALTER TABLE agents ADD COLUMN last_ack TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN proto_version INTEGER NOT NULL DEFAULT 1;
ALTER TABLE topologies ADD COLUMN health_check TEXT NOT NULL DEFAULT '';
ALTER TABLE topologies ADD COLUMN failover TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_tunnels_topology ON tunnels(topology_id);
CREATE INDEX idx_servers_dc ON servers(datacenter_id);
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
