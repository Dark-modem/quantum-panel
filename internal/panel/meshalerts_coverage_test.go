package panel

// meshalerts_coverage_test.go — BACKEND-19 TASK B: unit coverage for the
// four phase-13 rules that had no tests (hardening_expiring, cert_expiry,
// disk_usage, traffic_drop). Together with the existing meshalerts_test.go
// (agent_offline, tunnel_down, foreign_all_down, dedup/recovery/quiet) and
// agentmaintenance_test.go (maintenance silencing), every intended rule is
// now covered at the engine level.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
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
	// threshold floor is 10% (settings validation): any real machine is
	// above it, so the rule must fire for the config dir
	e.settings.DiskMaxPercent = 10
	evs := e.evaluate(time.Now())
	ev := findRule(t, evs, "disk_usage")
	if ev.Target != configDir {
		t.Fatalf("disk_usage must target the config dir: %+v", ev)
	}
	// an unreachable ceiling (99%) must NOT fire on a healthy disk
	e.settings.DiskMaxPercent = 99
	for _, ev := range e.evaluate(time.Now()) {
		if ev.Rule == "disk_usage" {
			t.Fatalf("disk_usage fired at 99%% ceiling on a healthy disk: %+v", ev)
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
