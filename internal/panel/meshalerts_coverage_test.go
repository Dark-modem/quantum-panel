package panel

// meshalerts_coverage_test.go — BACKEND-19 TASK B: unit coverage for the
// four phase-13 rules that had no tests (hardening_expiring, cert_expiry,
// disk_usage, traffic_drop) and, in TestAlertPhase13RemainingRules, fire
// tests for the four phase-13 rules that existed but were never asserted
// (agent_disk_high, billing_spike, backup_old, desired_stuck). Together
// with meshalerts_test.go (agent_offline, tunnel_down, foreign_all_down,
// dedup/recovery/quiet) and agentmaintenance_test.go (maintenance
// silencing), every dispatch-intended rule class is covered at engine
// level. Known remaining gaps (documented, not invented): clock_drift is
// a catalog stub — StateReportV2 carries NO agent timestamp, so the rule
// needs a protocol+agent field first (deferred); panel_disk_full shells
// out to df and cannot fire hermetically (no seam yet).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-protocol"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func findRule(t *testing.T, evs []meshAlertEvent, rule string) meshAlertEvent {
	t.Helper()
	for _, ev := range evs {
		if ev.Rule == rule {
			return ev
		}
	}
	t.Fatalf("rule %q did not fire; events: %+v", rule, evs)
	return meshAlertEvent{}
}

// r3TestStore pins an in-memory store for the evaluate() paths that touch
// MeshStore (an unset store would lazily open a FILE db in the temp config
// dir and race the Windows TempDir cleanup). Returns the store for tests
// that seed rows.
func r3TestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := storeOpenForTest()
	if err != nil {
		t.Fatal(err)
	}
	MeshStoreSetForTest(s)
	t.Cleanup(MeshStoreCloseForTest)
	return s
}

func TestMeshAlertHardeningExpiringRule(t *testing.T) {
	e, _ := newTestEngine(t, time.Now().Add(-time.Hour))
	r3TestStore(t)
	// armed safe-apply with <20% of its confirm window remaining
	profilePendingMu.Lock()
	profilePendingBy["agt_hp"] = &profilePending{
		Kind: "hardening", AgentID: "agt_hp",
		AppliedAt: time.Now().Add(-900 * time.Second).Unix(),
		TimerSec:  1000,
		Deadline:  time.Now().Add(100 * time.Second).Unix(), // 100s < 20% of 1000s
	}
	profilePendingMu.Unlock()
	t.Cleanup(func() {
		profilePendingMu.Lock()
		delete(profilePendingBy, "agt_hp")
		profilePendingMu.Unlock()
	})
	evs := e.evaluate(time.Now())
	ev := findRule(t, evs, "hardening_expiring")
	if ev.Target != "agt_hp" {
		t.Fatalf("hardening_expiring must target the agent: %+v", ev)
	}
	// a window with plenty of time left must NOT fire
	profilePendingMu.Lock()
	profilePendingBy["agt_hp"].Deadline = time.Now().Add(900 * time.Second).Unix()
	profilePendingMu.Unlock()
	for _, ev := range e.evaluate(time.Now()) {
		if ev.Rule == "hardening_expiring" {
			t.Fatalf("hardening_expiring must not fire with ample time: %+v", ev)
		}
	}
}

func TestMeshAlertCertExpiryRule(t *testing.T) {
	e, _ := newTestEngine(t, time.Now().Add(-time.Hour))
	r3TestStore(t)
	// mint a self-signed cert expiring in 7 days (< CertExpireDays=14)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "p19-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(7 * 24 * time.Hour),
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.MkdirAll(tlsDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tlsCertFile(), pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(tlsCertFile()) })
	evs := e.evaluate(time.Now())
	ev := findRule(t, evs, "cert_expiry")
	if ev.Target != "https" {
		t.Fatalf("cert_expiry must target https: %+v", ev)
	}
}

func TestMeshAlertDiskUsageRule(t *testing.T) {
	e, _ := newTestEngine(t, time.Now().Add(-time.Hour))
	r3TestStore(t)
	// Hermetic: pin the probe (the real syscall probe depends on the HOST
	// disk — 4% used on one machine, 54% on another — which made the first
	// version of this test fail on a lightly-used WSL root).
	diskUsageFn = func(path string) (uint64, uint64, error) {
		if path != configDir {
			return 0, 0, fmt.Errorf("unexpected probe path %q", path)
		}
		return uint64(100) << 30, uint64(80) << 30, nil // 20% used
	}
	t.Cleanup(func() { diskUsageFn = getDiskUsage })
	// exactly at the ceiling fires (>=), one point below it does not
	e.settings.DiskMaxPercent = 20
	ev := findRule(t, e.evaluate(time.Now()), "disk_usage")
	if ev.Target != configDir {
		t.Fatalf("disk_usage must target the config dir: %+v", ev)
	}
	e.settings.DiskMaxPercent = 21
	for _, ev := range e.evaluate(time.Now()) {
		if ev.Rule == "disk_usage" {
			t.Fatalf("disk_usage fired below the ceiling: %+v", ev)
		}
	}
}

func TestMeshAlertTrafficDropRule(t *testing.T) {
	e, _ := newTestEngine(t, time.Now().Add(-time.Hour))
	s := r3TestStore(t)
	dc, _ := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "d1"})
	sv1, _ := s.UpsertServer(store.Server{Name: "ir", Role: "iran", DatacenterID: dc})
	sv2, _ := s.UpsertServer(store.Server{Name: "fr", Role: "foreign"})
	topo, _ := s.UpsertTopology(store.Topology{Name: "td"})
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "k-td", TopologyID: topo,
		LocalServerID: sv1, RemoteServerID: sv2, Engine: "matrix", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	meshRuntimeMu.Lock()
	meshRuntimeBy["agt_td"] = []protocol.TunnelRuntime{
		{TunnelID: "k-td", Engine: "matrix", Up: true, WireKnown: true}, // zero wire bytes
	}
	meshRuntimeMu.Unlock()
	t.Cleanup(func() {
		meshRuntimeMu.Lock()
		delete(meshRuntimeBy, "agt_td")
		meshRuntimeMu.Unlock()
	})
	now := time.Now()
	// first tick: the freeze window opens (no event yet)
	for _, ev := range e.evaluate(now) {
		if ev.Rule == "traffic_drop" {
			t.Fatalf("traffic_drop must not fire on the first frozen tick: %+v", ev)
		}
	}
	// second tick ≥ meshAlertTick later: the anomaly fires
	evs := e.evaluate(now.Add(2 * meshAlertTick))
	ev := findRule(t, evs, "traffic_drop")
	if ev.Target != "k-td" {
		t.Fatalf("traffic_drop must target the tunnel key: %+v", ev)
	}
	if e.trafficFrozen("k-td", now.Add(2*meshAlertTick)) != true { //nolint:staticcheck // direct state probe
		t.Fatal("freeze state must persist across ticks")
	}
	// recovery: wire bytes appear → the freeze state clears
	meshRuntimeMu.Lock()
	meshRuntimeBy["agt_td"] = []protocol.TunnelRuntime{
		{TunnelID: "k-td", Engine: "matrix", Up: true, WireKnown: true, WireRx: 5000, WireTx: 5000},
	}
	meshRuntimeMu.Unlock()
	for _, ev := range e.evaluate(now.Add(3 * meshAlertTick)) {
		if ev.Rule == "traffic_drop" {
			t.Fatalf("traffic_drop must not fire once wire bytes flow: %+v", ev)
		}
	}
	if _, ok := meshTrafficFrozenSeen["k-td"]; ok {
		t.Fatal("freeze state must clear when traffic resumes")
	}
}

// TestMeshAlertClockDriftRule — the phase-13 catalog stub became REAL with
// R7 (StateReportV2.ReportedAt): a drifting agent clock over the
// configurable threshold fires; a fresh clock does not; the drift is
// absolute (an agent clock RUNNING AHEAD fires too); a legacy agent
// (absent field, 0) skips silently.
func TestMeshAlertClockDriftRule(t *testing.T) {
	e, _ := newTestEngine13(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)
	now := time.Now()

	setReportedAt := func(agentID string, at int64) {
		meshRuntimeMu.Lock()
		meshReportedAtBy[agentID] = at
		meshRuntimeMu.Unlock()
	}
	clearReportedAt := func(agentID string) {
		meshRuntimeMu.Lock()
		delete(meshReportedAtBy, agentID)
		meshRuntimeMu.Unlock()
	}
	t.Cleanup(func() {
		meshRuntimeMu.Lock()
		meshReportedAtBy = map[string]int64{}
		meshRuntimeMu.Unlock()
	})

	findDrift := func(evs []meshAlertEvent) *meshAlertEvent {
		for i := range evs {
			if evs[i].Rule == "clock_drift" {
				return &evs[i]
			}
		}
		return nil
	}

	// legacy agent (nothing reported): skip silently
	var ev *meshAlertEvent
	if ev = findDrift(e.evaluatePhase13(now)); ev != nil {
		t.Fatalf("legacy agent must not fire clock_drift: %+v", *ev)
	}

	// drifting clock (200s behind, default threshold 90): fires, targets
	// the agent, carries the structured fa/en view
	setReportedAt(agIr, now.Unix()-200)
	if ev = findDrift(e.evaluatePhase13(now)); ev == nil {
		t.Fatal("clock_drift must fire for a 200s-behind clock")
	}
	if ev.Target != agIr || ev.Key != "clock_drift:"+agIr {
		t.Fatalf("clock_drift target/key wrong: %+v", *ev)
	}
	if ev.UI == nil {
		t.Fatal("clock_drift must carry the structured fa/en view")
	}

	// an agent clock RUNNING AHEAD is also drift (absolute difference)
	setReportedAt(agIr, now.Unix()+200)
	if ev = findDrift(e.evaluatePhase13(now)); ev == nil {
		t.Fatal("clock_drift must fire for a clock running ahead too")
	}

	// threshold is configurable: a tight ceiling fires on a small drift…
	e.settings.ClockDriftMaxS = 10
	setReportedAt(agIr, now.Unix()-20)
	if ev = findDrift(e.evaluatePhase13(now)); ev == nil {
		t.Fatal("clock_drift must fire at the tightened threshold")
	}
	// …and the default threshold ignores a small drift
	e.settings.ClockDriftMaxS = meshAlertClockDriftS
	setReportedAt(agIr, now.Unix()-60)
	for _, ev := range e.evaluatePhase13(now) {
		if ev.Rule == "clock_drift" {
			t.Fatalf("60s drift must NOT fire at the 90s default: %+v", ev)
		}
	}

	// fresh clock: no fire
	setReportedAt(agIr, now.Unix()-5)
	for _, ev := range e.evaluatePhase13(now) {
		if ev.Rule == "clock_drift" {
			t.Fatalf("clock_drift fired for a fresh clock: %+v", ev)
		}
	}
	clearReportedAt(agIr)
}

// TestAlertPhase13RemainingRules — the phase-13 rules that existed since
// phase 13 but had NO fire assertion anywhere: agent_disk_high,
// billing_spike, backup_old, desired_stuck. Contract = the phase-13
// catalog (alertTextCatalog) + the evaluatePhase13 branches.
func TestAlertPhase13RemainingRules(t *testing.T) {
	e, _ := newTestEngine13(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)
	now := time.Now()

	// agent_disk_high: 95% of the agent's reported disk (threshold 90%);
	// CPU/memory kept healthy so ONLY the disk rule fires from this report.
	if err := s.UpdateAgentResources(agIr, 4, 10, 0.5, 8192, 800, 100, 95, 86400, "kvm"); err != nil {
		t.Fatal(err)
	}
	// billing_spike: last week carried 2 MB, this hour 20 MB (>8x) on the
	// seeded tunnel; a sibling tunnel with FLAT traffic must not spike.
	tun, err := s.GetTunnelByKey("tl-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTrafficSample(store.TrafficSample{TunnelID: tun.ID, AgentID: agIr,
		At: now.Add(-7 * 24 * time.Hour).Unix(), WireRx: 2 << 20, WireTx: 1 << 20, CountersKnown: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTrafficSample(store.TrafficSample{TunnelID: tun.ID, AgentID: agIr,
		At: now.Add(-time.Minute).Unix(), WireRx: 20 << 20, WireTx: 0, CountersKnown: true}); err != nil {
		t.Fatal(err)
	}
	// backup_old: the backup state file 8 days stale (threshold 7 days).
	bst := filepath.Join(configDir, "backup-state.json")
	if err := os.WriteFile(bst, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-8 * 24 * time.Hour)
	if err := os.Chtimes(bst, stale, stale); err != nil {
		t.Fatal(err)
	}
	// desired_stuck: a queued desired revision + last_seen 16 min ago
	// (threshold >15 min). Fresh last_seen must NOT fire (asserted below).
	ag, err := s.GetAgent(agIr)
	if err != nil {
		t.Fatal(err)
	}
	ag.LastSeen = now.Add(-16 * time.Minute).Unix()
	if err := s.UpsertAgent(ag); err != nil {
		t.Fatal(err)
	}
	dv, err := json.Marshal(protocol.DesiredV2{Revision: "rev-stuck-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetPendingDesiredV2(agIr, string(dv)); err != nil {
		t.Fatal(err)
	}

	evs := e.evaluatePhase13(now)
	got := map[string]meshAlertEvent{}
	for _, ev := range evs {
		if _, dup := got[ev.Rule]; !dup {
			got[ev.Rule] = ev
		}
	}
	for _, want := range []string{"agent_disk_high", "billing_spike", "backup_old", "desired_stuck"} {
		ev, ok := got[want]
		if !ok {
			fired := make([]string, 0, len(got))
			for r := range got {
				fired = append(fired, r)
			}
			sort.Strings(fired)
			t.Fatalf("rule %q did not fire; fired: %v", want, fired)
		}
		if ev.UI == nil {
			t.Fatalf("rule %s must carry the structured fa/en view", want)
		}
	}
	if got["agent_disk_high"].Target != agIr {
		t.Fatalf("agent_disk_high must target the agent: %+v", got["agent_disk_high"])
	}
	if got["billing_spike"].Target != tun.TunnelKey {
		t.Fatalf("billing_spike must target the tunnel key: %+v", got["billing_spike"])
	}

	// negatives: fresh backup state + a healthy disk → those two must not
	// fire (billing/desired state intentionally left as-is — out of scope).
	if err := os.Chtimes(bst, now, now); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAgentResources(agIr, 4, 10, 0.5, 8192, 800, 100, 50, 86400, "kvm"); err != nil {
		t.Fatal(err)
	}
	for _, ev := range e.evaluatePhase13(now) {
		if ev.Rule == "backup_old" || ev.Rule == "agent_disk_high" {
			t.Fatalf("rule %q must not fire in the healthy state: %+v", ev.Rule, ev)
		}
	}
}
