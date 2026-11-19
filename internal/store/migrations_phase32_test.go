package store

// The phase32 ui/live→main merge reconciled the diverged migration chains:
// main shipped datacenter_en as its migration 8 while the demo line had
// numbered it 10 (country/cpu_model took 8/9 there). The merged chain keeps
// main's numbering — append-only — so a database that already ran main's
// chain (schema_migrations MAX(version)=8, datacenters.datacenter_en applied)
// must upgrade by applying only migrations 9-10. Re-running the datacenter_en
// ALTER (what the demo line's numbering would have done at position 10) fails
// with a duplicate-column error and the panel cannot open its store. This
// test pins that upgrade path.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestUpgradeFromMainChainVersion8(t *testing.T) {
	if got := strings.Join(strings.Fields(migrations[7]), " "); !strings.Contains(got, "ADD COLUMN datacenter_en") {
		t.Fatalf("migration 8 is not the datacenter_en migration the phase-26 binaries shipped: %q", got)
	}
	if got := strings.Join(strings.Fields(migrations[8]), " "); !strings.Contains(got, "ADD COLUMN country") {
		t.Fatalf("migration 9 is not the country migration: %q", got)
	}
	if got := strings.Join(strings.Fields(migrations[9]), " "); !strings.Contains(got, "ADD COLUMN cpu_model") {
		t.Fatalf("migration 10 is not the cpu_model migration: %q", got)
	}

	path := t.TempDir() + "/main8.db"
	// Stage the store exactly as a main phase-26..31 binary leaves it:
	// migrations 1-8 applied (identical statements to the merged chain's
	// first eight), schema_migrations stamped 1..8. Raw driver handle — the
	// store's Open would run the full migrator and defeat the setup.
	stage, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// migrate() creates the bookkeeping table before applying anything; the
	// staging path bypasses migrate(), so create it the same way here.
	if _, err := stage.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	for i, stmt := range migrations[:8] {
		tx, err := stage.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("stage migration %d: %v", i+1, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, strftime('%s','now'))`, i+1); err != nil {
			t.Fatalf("stamp migration %d: %v", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	// The staged DB must NOT have the columns migrations 9-10 add.
	var probe int
	if err := stage.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agents') WHERE name IN ('country','cpu_model')`).Scan(&probe); err != nil {
		t.Fatal(err)
	}
	if probe != 0 {
		t.Fatalf("staged main-era DB already carries country/cpu_model (%d columns)", probe)
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}

	// Upgrade through the merged chain: migrate() must apply 9-10 and open.
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open main-era v8 DB on the merged chain: %v", err)
	}
	defer func() { _ = s.Close() }()
	if v, err := s.Version(); err != nil || v != len(migrations) {
		t.Fatalf("version after upgrade = %d, %v; want %d", v, err, len(migrations))
	}
	// Column presence via the schema (the tables are empty — row selects
	// would return no rows even with the columns present).
	var agentsCols, dcCols int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('agents') WHERE name IN ('country','cpu_model')`).Scan(&agentsCols); err != nil {
		t.Fatal(err)
	}
	if agentsCols != 2 {
		t.Fatalf("agents.country/cpu_model missing after upgrade: pragma_table_info names %d of 2", agentsCols)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('datacenters') WHERE name = 'datacenter_en'`).Scan(&dcCols); err != nil {
		t.Fatal(err)
	}
	if dcCols != 1 {
		t.Fatal("datacenters.datacenter_en missing after upgrade")
	}
}
