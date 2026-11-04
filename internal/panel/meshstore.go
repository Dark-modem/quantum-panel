package panel

// meshstore.go — lifecycle of the panel's SQLite store (internal/store),
// which is the AUTHORITY for the new mesh model (datacenters, servers,
// topologies, tunnels, weights, forwards, pasarguard nodes, traffic,
// overhead, audit). The legacy JSON files (agents.json, topologies.json)
// stay authoritative for the FROZEN legacy host-operator features and are
// imported into the store once, on first start, when the store is empty.
//
// Failure policy: if the store cannot open, the mesh API answers 503 and
// every legacy feature keeps working (they never read the store). The
// error is logged once; the store retries on the next mesh request.

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

var (
	meshStoreMu     sync.Mutex
	meshStore       *store.Store
	meshStoreErr    error
	meshImported    bool
	meshStaleSweep  = 90 * time.Second // last_seen older than this → offline
	meshStalePeriod = 30 * time.Second
)

// meshDBPath is the store location under the config dir. A var so tests
// can redirect it (they normally inject an in-memory store instead).
var meshDBPath = func() string { return filepath.Join(configDir, "quantum.db") }

// MeshStore returns the open store, opening + importing on first use.
func MeshStore() (*store.Store, error) {
	meshStoreMu.Lock()
	defer meshStoreMu.Unlock()
	if meshStore != nil {
		return meshStore, nil
	}
	if meshStoreErr != nil && time.Since(meshStoreRetryAt()) < 10*time.Second {
		return nil, meshStoreErr
	}
	s, err := store.Open(meshDBPath())
	if err != nil {
		meshStoreErr = fmt.Errorf("open store: %v", err)
		return nil, meshStoreErr
	}
	meshStore = s
	meshStoreErr = nil
	if err := meshImportOnce(s); err != nil {
		// The store stays open: a failed import is surfaced in the API and
		// retried on the next start, never a crash.
		log.Printf("[mesh] legacy import failed (will retry next start): %v", err)
	}
	startMeshStaleSweep()
	return s, nil
}

// meshStoreRetryAt gates retry logging so a broken DB path does not spam.
func meshStoreRetryAt() time.Time {
	meshRetryMu.Lock()
	defer meshRetryMu.Unlock()
	return meshRetryAt
}

var (
	meshRetryMu sync.Mutex
	meshRetryAt time.Time
)

func noteStoreRetry() {
	meshRetryMu.Lock()
	meshRetryAt = time.Now()
	meshRetryMu.Unlock()
}

// MeshStoreSetForTest replaces the process-wide store (tests only).
func MeshStoreSetForTest(s *store.Store) {
	meshStoreMu.Lock()
	defer meshStoreMu.Unlock()
	if meshStore != nil {
		_ = meshStore.Close()
	}
	meshStore = s
	meshStoreErr = nil
	meshImported = true // tests manage their own fixtures
}

// MeshStoreCloseForTest closes and forgets the store (test teardown).
func MeshStoreCloseForTest() {
	meshStoreMu.Lock()
	defer meshStoreMu.Unlock()
	if meshStore != nil {
		_ = meshStore.Close()
	}
	meshStore = nil
	meshImported = false
	meshStaleStop()
}

// meshImportOnce runs the legacy JSON import exactly once per store, and
// only when the store is EMPTY (first start): an operator-curated store is
// never overwritten by re-imports. Idempotent upserts make a re-run after
// a partially failed import safe.
func meshImportOnce(s *store.Store) error {
	if meshImported {
		return nil
	}
	empty, err := storeIsEmpty(s)
	if err != nil {
		return err
	}
	if !empty {
		meshImported = true
		return nil
	}
	var stats store.ImportStats
	if raw, err := os.ReadFile(filepath.Join(configDir, "agents.json")); err == nil {
		st, err := s.ImportAgentsJSON(raw)
		if err != nil {
			return fmt.Errorf("import agents.json: %v", err)
		}
		stats.AgentsUpserted = st.AgentsUpserted
	}
	if raw, err := os.ReadFile(filepath.Join(configDir, "topologies.json")); err == nil {
		st, err := s.ImportTopologiesJSON(raw)
		if err != nil {
			return fmt.Errorf("import topologies.json: %v", err)
		}
		stats.Topologies, stats.Servers, stats.Tunnels = st.Topologies, st.Servers, st.Tunnels
	}
	meshImported = true
	log.Printf("[mesh] legacy import complete: agents=%d topologies=%d servers=%d tunnels=%d",
		stats.AgentsUpserted, stats.Topologies, stats.Servers, stats.Tunnels)
	_ = s.AddAudit(store.AuditEntry{
		At: time.Now().Unix(), Actor: "system", Action: "mesh.import.legacy",
		Detail: fmt.Sprintf(`{"agents":%d,"topologies":%d,"servers":%d,"tunnels":%d}`,
			stats.AgentsUpserted, stats.Topologies, stats.Servers, stats.Tunnels),
	})
	return nil
}

func storeIsEmpty(s *store.Store) (bool, error) {
	var n int
	if err := s.DB().QueryRow(`SELECT
		(SELECT COUNT(*) FROM agents) +
		(SELECT COUNT(*) FROM datacenters) +
		(SELECT COUNT(*) FROM topologies)`).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}

// meshAudit records one mesh domain event in the store's audit table
// (actor is the panel user; secrets and profile payloads never go in).
func meshAudit(actor, action, detail string) {
	s, err := MeshStore()
	if err != nil {
		return
	}
	_ = s.AddAudit(store.AuditEntry{At: time.Now().Unix(), Actor: actor, Action: action, Detail: detail})
}

// startMeshStaleSweep marks agents whose last_seen went quiet as offline
// (the panel KEEPS all other state — an agent drop must not lose data).
var (
	meshSweepOnce sync.Once
	meshSweepStop chan struct{}
)

func startMeshStaleSweep() {
	meshSweepOnce.Do(func() {
		meshSweepStop = make(chan struct{})
		go func() {
			t := time.NewTicker(meshStalePeriod)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					if s, err := MeshStore(); err == nil {
						_, _ = s.MarkStaleAgents(meshStaleSweep)
					} else {
						noteStoreRetry()
					}
				case <-meshSweepStop:
					return
				}
			}
		}()
	})
}

func meshStaleStop() {
	if meshSweepStop != nil {
		select {
		case <-meshSweepStop:
		default:
			close(meshSweepStop)
		}
		meshSweepOnce = sync.Once{}
	}
}
