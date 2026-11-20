package panel

// BACKEND-35 B-AG1 reproduction: GET /api/mesh/usage?topology_id=… answered
// E-SYS-01 "store operation failed" after the e2e's panel-restart leg
// (REFACTOR-A1-AGENT-REPORT §3.1: 4/4 repro on panel main d398740 AND
// ui/live e55e485), while per-tunnel usage reads kept working.
//
// ROOT CAUSE (proven by the binary repro in
// 00-ADMIN/tools/backend35-repro.sh + B-AG2's raw-error logging):
// TunnelUsageDetailed's retention fallback queries traffic_hourly — a
// table the fold job (RollupTrafficHourly) created lazily, NEVER by a
// migration. A read whose window holds a tunnel with NO raw samples (a
// quiet tunnel, or one the ingest ownership gate never sees samples for)
// hits the fallback before the fold job has ever ticked and gets
// "no such table: traffic_hourly" — masked into E-SYS-01 by meshErr. The
// topology read aggregates EVERY tunnel of the topology, so one quiet
// tunnel poisons it; the per-tunnel reads the e2e measured only touched
// tunnels with samples. The restart correlation: the first topology-scope
// read simply happens after the restart, and the fallback fires on the
// fresh store until the first ingest tick at a :00 second creates the
// table.
//
// FIX: migration 11 moves both fold tables (traffic_hourly +
// traffic_rollup_state) into the schema chain, verbatim the DDL the fold
// job always used. This test seeds a topology with one reporting and one
// quiet tunnel, restarts the store the way the e2e restart leg does
// (close + reopen; migrate() re-runs the version-gated chain), and
// asserts the topology-scoped read works — RED on main before the fix
// (the fallback's "no such table"), PASS after.
//
// Hermetic: temp-dir SQLite only, no network, no containers.

import (
	"bytes"
	"errors"
	"log"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func seedE2EShapeStore(t *testing.T, dbPath string) *store.Store {
	t.Helper()
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("boot #1 store open: %v", err)
	}
	now := time.Now().Unix()
	dc, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "ir-dc", PriceCoefficient: 1.0})
	if err != nil {
		t.Fatalf("seed dc: %v", err)
	}
	dcf, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "hz-dc", PriceCoefficient: 1.0})
	if err != nil {
		t.Fatalf("seed dc foreign: %v", err)
	}
	ir, err := s.UpsertServer(store.Server{Name: "ir-drak-1", Role: "iran", DatacenterID: dc, Address: "10.200.0.11"})
	if err != nil {
		t.Fatalf("seed server ir: %v", err)
	}
	hz1, err := s.UpsertServer(store.Server{Name: "hz-fra-1", Role: "foreign", DatacenterID: dcf, Address: "10.200.0.21"})
	if err != nil {
		t.Fatalf("seed server hz1: %v", err)
	}
	hz2, err := s.UpsertServer(store.Server{Name: "hz-fra-2", Role: "foreign", DatacenterID: dcf, Address: "10.200.0.22"})
	if err != nil {
		t.Fatalf("seed server hz2: %v", err)
	}
	tp, err := s.UpsertTopology(store.Topology{Name: "e2e-topo", Enabled: true})
	if err != nil {
		t.Fatalf("seed topology: %v", err)
	}
	tk1, err := s.UpsertTunnel(store.Tunnel{
		TunnelKey: "tk-1", TopologyID: tp, LocalServerID: ir, RemoteServerID: hz1,
		Engine: "gost", EngineParams: "{}", Enabled: true, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("seed tunnel 1: %v", err)
	}
	if _, err := s.UpsertTunnel(store.Tunnel{
		TunnelKey: "tk-2", TopologyID: tp, LocalServerID: ir, RemoteServerID: hz2,
		Engine: "gost", EngineParams: "{}", Enabled: true, CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed tunnel 2: %v", err)
	}
	// Only tunnel tk-1 gets samples: tk-2 is the QUIET tunnel (the e2e's
	// ownership-gated/never-reported shape) — its read takes the retention
	// fallback, which is where main answers "no such table: traffic_hourly".
	for j := 0; j < 3; j++ {
		err := s.AddTrafficSample(store.TrafficSample{
			TunnelID: tk1, At: now - 120 + int64(j*10),
			AgentID: "agt_seed", CountersKnown: true, PayloadKnown: true,
			WireRx: uint64(1000 * (j + 1)), WireTx: uint64(500 * (j + 1)),
			PayloadRx: uint64(900 * (j + 1)), PayloadTx: uint64(450 * (j + 1)),
		})
		if err != nil {
			t.Fatalf("seed sample: %v", err)
		}
	}
	return s
}

func TestMeshUsageTopologyReadSurvivesRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "quantum.db")

	// Boot #1 (seed): one reporting tunnel + one quiet tunnel.
	s1 := seedE2EShapeStore(t, dbPath)

	// THE READ THAT FAILS ON MAIN: the topology scope aggregates both
	// tunnels; the quiet tunnel's retention fallback queries
	// traffic_hourly, which does not exist until the fold job first ticks.
	// (RED run: HTTP 500 E-SYS-01 "store operation failed" before the
	// migration landed.)
	req := httptest.NewRequest("GET", "/api/mesh/usage?topology_id=1&from=0&to=0", nil)
	w := httptest.NewRecorder()
	meshStoreSetForTest(s1)
	handleMeshUsage(w, req)
	if w.Code != 200 {
		t.Fatalf("topology usage read on the seeded store: HTTP %d: %s", w.Code, w.Body.String())
	}

	// THE RESTART LEG: the panel process dies and boots again on the same
	// GRE_PANEL_DIR — the store is closed and reopened from disk and the
	// version-gated migration chain re-runs (this is what delivers
	// migration 11 to existing deployments).
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	s2, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("boot #2 store open (the restart): %v", err)
	}
	meshStoreSetForTest(s2)
	t.Cleanup(meshStoreCloseForTest)

	// The e2e's failing call, post-restart, through the full handler.
	req2 := httptest.NewRequest("GET", "/api/mesh/usage?topology_id=1&from=0&to=0", nil)
	w2 := httptest.NewRecorder()
	handleMeshUsage(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("post-restart topology usage read: HTTP %d: %s", w2.Code, w2.Body.String())
	}
	if !strings.Contains(w2.Body.String(), `"usage"`) {
		t.Fatalf("unexpected body: %s", w2.Body.String())
	}
	// The reporting tunnel's bytes survive the restart (raw rows in
	// window): zero-base rule = first sample 1000 + two deltas of 1000.
	if !strings.Contains(w2.Body.String(), `"wire_rx":3000`) {
		t.Fatalf("reporting tunnel counters missing: %s", w2.Body.String())
	}

	// The quiet tunnel's own read takes the fallback too and must answer
	// zeros (never an error) — the retention-fallback contract.
	req3 := httptest.NewRequest("GET", "/api/mesh/usage?tunnel_id=2&from=0&to=0", nil)
	w3 := httptest.NewRecorder()
	handleMeshUsage(w3, req3)
	if w3.Code != 200 {
		t.Fatalf("quiet-tunnel read (retention fallback): HTTP %d: %s", w3.Code, w3.Body.String())
	}

	// The fold job's watermark table must exist for the rollup to work.
	if _, err := s2.DB().Query(`SELECT done_until FROM traffic_rollup_state WHERE id = 1`); err != nil {
		t.Fatalf("traffic_rollup_state missing after migration: %v", err)
	}
}

// TestMeshErrPersistsRawDetailBehindMask (B-AG2, REFACTOR-A1 §3.2): the
// client and the error journal see only the masked string (L1 — sqlite
// internals never reach the client), but the raw store error now lands in
// the durable log (panel.log) so masked failures are diagnosable. Before
// this fix the masked E-SYS-01 text was persisted nowhere (checked
// panel-errors.log + panel.log — both clean).
func TestMeshErrPersistsRawDetailBehindMask(t *testing.T) {
	var buf bytes.Buffer
	oldW := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldW) })

	req := httptest.NewRequest("GET", "/api/mesh/usage?topology_id=1", nil)
	w := httptest.NewRecorder()
	meshErr(w, req, errors.New("SQL logic error: no such table: traffic_hourly (1) (synthetic B-AG2 probe)"))

	// The client sees the masked string only.
	body := w.Body.String()
	if !strings.Contains(body, "store operation failed (details in the panel error journal)") {
		t.Fatalf("client body lost the masked string: %s", body)
	}
	if strings.Contains(body, "traffic_hourly") || strings.Contains(body, "SQL logic") {
		t.Fatalf("raw store text leaked to the client (L1 violation): %s", body)
	}
	// The durable log carries the full raw detail, tagged as masked-facing.
	durable := buf.String()
	if !strings.Contains(durable, "no such table: traffic_hourly (1) (synthetic B-AG2 probe)") {
		t.Fatalf("durable log lost the raw store detail: %s", durable)
	}
	if !strings.Contains(durable, "masked in the API response") {
		t.Fatalf("durable log line missing the masking tag: %s", durable)
	}
	if !strings.Contains(durable, "GET /api/mesh/usage") {
		t.Fatalf("durable log line missing the request context: %s", durable)
	}
}
