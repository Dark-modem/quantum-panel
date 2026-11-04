package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTopologyUsageRenameCurrentAssignmentAndExclusive(t *testing.T) {
	s := newUsage()
	for _, key := range []string{"main", "p2", "matrix"} {
		s.observe(key, key, t0, up64(0), up64(0))
		s.observe(key, key, t0.Add(time.Minute), up64(100), up64(20))
	}
	groups := []Topology{{ID: "a", Name: "Old", TunnelIDs: []string{"main", "peer:2"}}, {ID: "b", Name: "Mesh", TunnelIDs: []string{"matrix"}}}
	rows, unassigned := s.topologyTotals(t0.Add(time.Minute), time.Hour, 60, groups, nil, nil)
	if rows[0].Rx != 200 || rows[0].Total != 240 || rows[0].TunnelCount != 2 || !rows[0].Known || rows[1].Rx != 100 || unassigned.TunnelCount != 0 {
		t.Fatalf("bad grouping: %+v %+v", rows, unassigned)
	}
	groups[0].Name = "Renamed"
	groups[0].TunnelIDs = []string{"main"}
	groups[1].TunnelIDs = []string{"matrix", "peer:2"}
	rows, _ = s.topologyTotals(t0.Add(time.Minute), time.Hour, 60, groups, nil, nil)
	if rows[0].Name != "Renamed" || rows[0].Rx != 100 || rows[1].Rx != 200 {
		t.Fatalf("rename/current assignment must preserve ledger: %+v", rows)
	}
	groups[1].TunnelIDs = append(groups[1].TunnelIDs, "main")
	rows, _ = s.topologyTotals(t0.Add(time.Minute), time.Hour, 60, groups, nil, nil)
	if rows[0].Known || rows[1].Known || rows[0].Rx+rows[1].Rx != 300 {
		t.Fatalf("duplicate assignment must be unknown and never double count: %+v", rows)
	}
}

func TestTopologyUsageMissingIdleUnknownAndUnassigned(t *testing.T) {
	s := newUsage()
	s.observe("main", "main", t0, up64(800), up64(900))
	groups := []Topology{{ID: "a", Name: "Main", TunnelIDs: []string{"main", "peer:99"}}}
	rows, unassigned := s.topologyTotals(t0, time.Hour, 60, groups, []topologyTunnel{{ID: "xray:8"}}, nil)
	if rows[0].Known || unassigned.Known || rows[0].TunnelCount != 2 || unassigned.TunnelCount != 1 {
		t.Fatalf("baseline/missing counters are not measured zero: %+v %+v", rows, unassigned)
	}
	s.observe("main", "main", t0.Add(time.Minute), up64(800), up64(900))
	groups[0].TunnelIDs = []string{"main"}
	rows, _ = s.topologyTotals(t0.Add(time.Minute), time.Hour, 60, groups, nil, nil)
	if !rows[0].Known || rows[0].Total != 0 {
		t.Fatalf("measured idle is known: %+v", rows)
	}
	rows, _ = s.topologyTotals(t0.Add(time.Minute), time.Hour, 60, groups, []topologyTunnel{}, nil)
	if rows[0].Known {
		t.Fatal("fresh ledger for a now-absent inventory ID cannot be known")
	}
	s.observe("main", "main", t0.Add(2*time.Minute), nil, nil)
	rows, _ = s.topologyTotals(t0.Add(2*time.Minute), time.Hour, 60, groups, nil, nil)
	if rows[0].Known {
		t.Fatal("latest missing sample must remain unknown")
	}
	s.observe("p7", "removed", t0, up64(0), up64(0))
	s.observe("p7", "removed", t0.Add(time.Minute), up64(30), up64(5))
	rows, unassigned = s.topologyTotals(t0.Add(10*time.Minute), time.Hour, 60, groups, nil, nil)
	if rows[0].Known || unassigned.Known || unassigned.Rx != 30 || unassigned.Total != 35 {
		t.Fatalf("stale/removed history stays recorded and explicitly unknown: %+v %+v", rows, unassigned)
	}
}

func TestUnassignedUsageRowVisibility(t *testing.T) {
	if unassignedUsageVisible(topologyUsageRow{usageAmount: usageAmount{Known: true}}) {
		t.Fatal("empty unassigned bucket must not be emitted as a placeholder row")
	}
	for name, row := range map[string]topologyUsageRow{
		"rx": {usageAmount: usageAmount{Rx: 1, Known: true}, TunnelCount: 1},
		"tx": {usageAmount: usageAmount{Tx: 1, Known: true}, TunnelCount: 1},
	} {
		if !unassignedUsageVisible(row) {
			t.Fatalf("unassigned with a live tunnel and measured traffic (%s) must stay visible: %+v", name, row)
		}
	}
	for name, row := range map[string]topologyUsageRow{
		// Unknown traffic must never satisfy visibility, even with a count.
		"unknown":    {usageAmount: usageAmount{Rx: 30, Tx: 5}, TunnelCount: 1},
		"stale-only": {usageAmount: usageAmount{Rx: 5, Known: true}},          // no live unassigned tunnel
		"zero":       {usageAmount: usageAmount{Known: true}, TunnelCount: 1}, // measured idle
		"count-only": {usageAmount: usageAmount{Known: true, Rx: 0, Tx: 0}, TunnelCount: 1},
	} {
		if unassignedUsageVisible(row) {
			t.Fatalf("unassigned without a live tunnel and measured traffic (%s) must not render: %+v", name, row)
		}
	}
}

func TestTopologyUsageLegacyMainAliasNotDoubleCounted(t *testing.T) {
	s := newUsage()
	for _, key := range []string{"main", "p1"} {
		s.observe(key, key, t0, up64(0), up64(0))
		s.observe(key, key, t0.Add(time.Minute), up64(90), up64(10))
	}
	snap := &HubSnapshot{Peers: []peerLive{{peerRecord: peerRecord{ID: 1, GreIf: "gre-tunnel"}}}}
	groups := []Topology{{ID: "a", TunnelIDs: []string{"main", "peer:1"}}}
	rows, unassigned := s.topologyTotals(t0.Add(time.Minute), time.Hour, 60, groups, nil, snap)
	if rows[0].Total != 100 || rows[0].TunnelCount != 1 || unassigned.Total != 0 || !rows[0].Known {
		t.Fatalf("same counter alias must count once: %+v %+v", rows, unassigned)
	}
	groups = []Topology{{ID: "a", TunnelIDs: []string{"main"}}, {ID: "b", TunnelIDs: []string{"peer:1"}}}
	rows, _ = s.topologyTotals(t0.Add(time.Minute), time.Hour, 60, groups, nil, snap)
	if rows[0].Known || rows[1].Known || rows[0].Total+rows[1].Total != 100 {
		t.Fatalf("cross-topology alias conflict cannot double count: %+v", rows)
	}
}

func TestServerUsagePhysicalIndependentResetAndPersistence(t *testing.T) {
	old := trafficUsage
	trafficUsage = newUsage()
	t.Cleanup(func() { trafficUsage = old })
	observeServerInterfaceUsage(t0, map[string][2]uint64{"eth0": {1000, 2000}, "ens5": {500, 800}}, true)
	trafficUsage.mu.Lock()
	total, _ := trafficUsage.serverTotalsLocked(t0, time.Hour, 60)
	trafficUsage.mu.Unlock()
	if total.Known || total.Total != 0 {
		t.Fatalf("first physical sample is only a baseline: %+v", total)
	}
	observeServerInterfaceUsage(t0.Add(time.Minute), map[string][2]uint64{"eth0": {1100, 2100}, "ens5": {510, 820}}, true)
	// One NIC reset, one disappears, another arrives carrying a huge existing
	// counter: only eth0's new reset reading is additional measured traffic.
	observeServerInterfaceUsage(t0.Add(2*time.Minute), map[string][2]uint64{"eth0": {5, 7}, "ens9": {900000, 800000}}, true)
	observeServerInterfaceUsage(t0.Add(3*time.Minute), nil, false)
	observeServerInterfaceUsage(t0.Add(4*time.Minute), map[string][2]uint64{"eth0": {15, 17}, "ens9": {900010, 800020}}, true)
	// Older samples must not look like a fresh counter reset.
	observeServerInterfaceUsage(t0.Add(time.Minute), map[string][2]uint64{"eth0": {1, 2}}, true)
	trafficUsage.observe("matrix", "Mesh overhead", t0, up64(0), up64(0))
	trafficUsage.observe("matrix", "Mesh overhead", t0.Add(4*time.Minute), up64(5000), up64(6000))
	trafficUsage.mu.Lock()
	total, window := trafficUsage.serverTotalsLocked(t0.Add(4*time.Minute), time.Hour, 60)
	trafficUsage.mu.Unlock()
	if total.Rx != 135 || total.Tx != 157 || total.Total != 292 || !total.Known || window.Total != 292 || len(window.Points) != 3 || len(window.Days) != 1 {
		t.Fatalf("physical is independent/reset-safe: %+v %+v", total, window)
	}
	if links := trafficUsage.all(t0.Add(4*time.Minute), time.Hour, 60); links[0].Rx != 5000 || links[0].Tx != 6000 {
		t.Fatalf("physical must never inflate compatibility tunnel total: %+v", links)
	}
	path := filepath.Join(t.TempDir(), "traffic_peers.json")
	if err := trafficUsage.save(path, t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	trafficUsage = newUsage()
	trafficUsage.load(path, t0.Add(5*time.Minute))
	observeServerInterfaceUsage(t0.Add(5*time.Minute), map[string][2]uint64{"eth0": {25, 27}, "ens9": {900020, 800030}}, true)
	trafficUsage.prune(t0.Add(8 * 24 * time.Hour))
	trafficUsage.mu.Lock()
	total, window = trafficUsage.serverTotalsLocked(t0.Add(8*24*time.Hour), time.Hour, 60)
	trafficUsage.mu.Unlock()
	if total.Rx != 155 || total.Tx != 177 || total.StartedAt != t0.Unix() || window.Total != 0 || total.Known {
		t.Fatalf("persisted lifetime survives retention; absent sample unknown: %+v %+v", total, window)
	}
}

func TestTopologyUsageSnapshotCanonicalSpecialEngines(t *testing.T) {
	old := trafficUsage
	trafficUsage = newUsage()
	t.Cleanup(func() { trafficUsage = old })
	mk := func(at time.Time, delta uint64) *HubSnapshot {
		return &HubSnapshot{CountersAt: at,
			Main:      &peerLive{peerRecord: peerRecord{ID: 0}, Rx: up64(100 + delta), Tx: up64(200 + delta)},
			Peers:     []peerLive{{peerRecord: peerRecord{ID: 1, GreIf: "gre-tunnel"}, Rx: up64(100 + delta), Tx: up64(200 + delta)}},
			Local:     tunnelStatus{Matrix: &matrixStatus{Enabled: true, Rx: up64(300 + delta), Tx: up64(400 + delta)}},
			CarrierOK: true, CarrierTags: map[string][2]uint64{"bagher-1": {500 + delta, 600 + delta}, "bagher-2": {700 + delta, 800 + delta}, "xray-3": {900 + delta, 1000 + delta}},
		}
	}
	usageOnSnapshot(mk(t0, 0))
	usageOnSnapshot(mk(t0.Add(time.Minute), 20))
	groups := []Topology{{ID: "a", TunnelIDs: []string{"main", "peer:1", "matrix", "bagher", "bagher:2", "xray:3"}}}
	rows, _ := trafficUsage.topologyTotals(t0.Add(time.Minute), time.Hour, 60, groups, nil, mk(t0.Add(time.Minute), 20))
	if rows[0].Total != 200 || rows[0].TunnelCount != 5 || !rows[0].Known {
		t.Fatalf("canonical special engines + no main alias double count: %+v", rows)
	}
	if _, duplicate := trafficUsage.links["p1"]; duplicate {
		t.Fatal("new main alias must not produce a second history")
	}
	usageOnSnapshot(&HubSnapshot{CountersAt: t0.Add(2 * time.Minute), Local: tunnelStatus{Matrix: &matrixStatus{Enabled: true}}, DevData: devLines(map[string][2]uint64{matrixIfaceName: {330, 430}})})
	if l := trafficUsage.links["matrix"]; l.LastRx != 330 || l.CumulativeRx != 30 {
		t.Fatalf("qmx0 fallback must use already-read counters: %+v", l)
	}
	usageOnSnapshot(&HubSnapshot{CountersAt: t0.Add(3 * time.Minute), LocalRx: up64(1000), LocalTx: up64(2000), CarrierOK: true, CarrierTags: map[string][2]uint64{"bagher-1": {600, 700}}})
	if trafficUsage.links["main"].LastRx != 120 {
		t.Fatal("Bagher-only LocalRx aggregate must not be duplicated into main")
	}
}

func TestTopologyUsageAPITruthfulContractNoProbes(t *testing.T) {
	oldUsage, oldDir, oldInventory := trafficUsage, configDir, topologyInventory
	trafficUsage, configDir = newUsage(), t.TempDir()
	topologyInventory = func() []topologyTunnel { return []topologyTunnel{{ID: "main"}, {ID: "peer:2"}} }
	t.Cleanup(func() { trafficUsage, configDir, topologyInventory = oldUsage, oldDir, oldInventory })
	if err := saveTopologies([]Topology{{ID: "a", Name: "A", Role: "iran", CreatedAt: t0.Unix(), TunnelIDs: []string{"main", "peer:2"}}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	trafficUsage.observe("main", "main", now.Add(-time.Minute), up64(0), up64(0))
	trafficUsage.observe("main", "main", now, up64(30), up64(40))
	observeServerInterfaceUsage(now.Add(-time.Minute), map[string][2]uint64{"eth0": {1000, 2000}}, true)
	observeServerInterfaceUsage(now, map[string][2]uint64{"eth0": {1100, 2200}}, true)
	// Detached tunnel history (a removed tunnel's stale records) must never
	// resurrect the fallback row, even with recorded traffic.
	trafficUsage.observe("p9", "removed", now.Add(-time.Minute), up64(0), up64(0))
	trafficUsage.observe("p9", "removed", now, up64(3), up64(4))
	rec := httptest.NewRecorder()
	handleUsageGet(rec, httptest.NewRequest(http.MethodGet, "/api/traffic/usage?range=1h", nil))
	var response struct {
		Server serverUsageTotal   `json:"server_total"`
		Window serverUsageWindow  `json:"server_window"`
		Rows   []topologyUsageRow `json:"topologies"`
		Basis  string             `json:"grouping_basis"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Server.Total != 300 || response.Window.Total != 300 || response.Rows[0].Total != 70 || response.Rows[0].Known || response.Basis != "current_assignments" {
		t.Fatalf("additive contract: %s", rec.Body.String())
	}
	var withoutStale map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &withoutStale); err != nil {
		t.Fatal(err)
	}
	if _, exists := withoutStale["unassigned"]; exists {
		t.Fatalf("detached history must not render the unassigned row: %s", rec.Body.String())
	}
	// A live unassigned tunnel with measured traffic keeps the fallback row.
	topologyInventory = func() []topologyTunnel { return []topologyTunnel{{ID: "main"}, {ID: "peer:2"}, {ID: "peer:9"}} }
	rec = httptest.NewRecorder()
	handleUsageGet(rec, httptest.NewRequest(http.MethodGet, "/api/traffic/usage?range=1h", nil))
	var withUnassigned struct {
		Unassigned topologyUsageRow `json:"unassigned"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &withUnassigned); err != nil {
		t.Fatal(err)
	}
	if withUnassigned.Unassigned.Total != 7 || withUnassigned.Unassigned.TunnelCount != 1 || !withUnassigned.Unassigned.Known {
		t.Fatalf("live unassigned tunnel with measured traffic must stay visible: %s", rec.Body.String())
	}
	if err := os.WriteFile(topologyPath(), []byte("bad json"), 0600); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	handleUsageGet(rec, httptest.NewRequest(http.MethodGet, "/api/traffic/usage", nil))
	if !strings.Contains(rec.Body.String(), `"topology_known":false`) || !strings.Contains(rec.Body.String(), `"topology_error"`) {
		t.Fatalf("corrupt grouping cannot silently appear empty: %s", rec.Body.String())
	}
	var withoutUnassigned map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &withoutUnassigned); err != nil {
		t.Fatal(err)
	}
	if _, exists := withoutUnassigned["unassigned"]; exists {
		t.Fatalf("no unassigned row without topologies: %s", rec.Body.String())
	}
}

// Deleting a tunnel must remove its usage records in the same request: not
// left as an unassigned row, not resurrected from the ledger file on restart.
func TestPurgeTunnelUsageOnDelete(t *testing.T) {
	old, oldDir := trafficUsage, configDir
	trafficUsage, configDir = newUsage(), t.TempDir()
	t.Cleanup(func() { trafficUsage, configDir = old, oldDir })
	trafficUsage.observe("main", "Main", t0, up64(0), up64(0))
	trafficUsage.observe("main", "Main", t0.Add(time.Minute), up64(30), up64(4))
	trafficUsage.observe("p2", "Peer two", t0, up64(0), up64(0))
	trafficUsage.observe("p2", "Peer two", t0.Add(time.Minute), up64(7), up64(2))
	trafficUsage.observe("xray:4", "Xray", t0, up64(0), up64(0))
	trafficUsage.observe("xray:4", "Xray", t0.Add(time.Minute), up64(9), up64(1))
	trafficUsage.mu.Lock()
	trafficUsage.links["p1"] = &usageLink{Name: "legacy", AliasOf: usageMainKey, Days: map[string]usageDay{}}
	trafficUsage.mu.Unlock()
	if err := trafficUsage.save(usageFile(), t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Deleting one tunnel purges exactly its records; live tunnels keep their
	// 7-day history.
	purgeTunnelUsage("peer:2")
	if trafficUsage.links["p2"] != nil {
		t.Fatal("deleting a tunnel must purge its usage records immediately")
	}
	if trafficUsage.links[usageMainKey] == nil || trafficUsage.links["xray:4"] == nil {
		t.Fatal("unrelated live links must survive the purge")
	}
	// Deleting the main tunnel takes its alias history (legacy peer 1) with it.
	purgeTunnelUsage(usageMainKey)
	if trafficUsage.links[usageMainKey] != nil || trafficUsage.links["p1"] != nil {
		t.Fatal("main tunnel deletion must purge its records and alias history")
	}
	if trafficUsage.links["xray:4"] == nil {
		t.Fatal("unrelated live links must survive the purge")
	}
	// The purged state is on disk: a restart must not resurrect the records.
	fresh := newUsage()
	if n := fresh.load(usageFile(), t0.Add(2*time.Minute)); n != 1 {
		t.Fatalf("restart must load only the surviving link, got %d", n)
	}
	if fresh.links["p2"] != nil || fresh.links[usageMainKey] != nil || fresh.links["p1"] != nil {
		t.Fatal("deleted tunnels must never be resurrected from the ledger file")
	}
}

// A legacy peer 1 shares the main tunnel's counter, so its purge must cover
// "main" too; a standalone peer purges only its own key.
func TestPurgeRemovedPeerUsageKeys(t *testing.T) {
	old, oldDir := trafficUsage, configDir
	trafficUsage, configDir = newUsage(), t.TempDir()
	t.Cleanup(func() { trafficUsage, configDir = old, oldDir })
	for _, key := range []string{"main", "p1", "p3"} {
		trafficUsage.observe(key, key, t0, up64(0), up64(0))
		trafficUsage.observe(key, key, t0.Add(time.Minute), up64(4), up64(4))
	}
	purgeRemovedPeerUsage(&peerRecord{ID: 1, Legacy: true}, 1)
	if trafficUsage.links["p1"] != nil || trafficUsage.links[usageMainKey] != nil || trafficUsage.links["p3"] == nil {
		t.Fatalf("legacy peer 1 removal must purge p1 and main only: %+v", trafficUsage.links)
	}
	purgeRemovedPeerUsage(&peerRecord{ID: 3, GreIf: "gre-t3"}, 3)
	if trafficUsage.links["p3"] != nil || trafficUsage.links[usageMainKey] != nil {
		t.Fatalf("standalone peer removal must purge p3 only: %+v", trafficUsage.links)
	}
}

// Deleting a topology purges the usage records still keyed by its listed
// tunnels in the same request.
func TestTopologyDeletePurgesItsUsageRecords(t *testing.T) {
	topologyFixture(t)
	old := trafficUsage
	trafficUsage = newUsage()
	t.Cleanup(func() { trafficUsage = old })
	// peer:5's tunnel is already gone; only its ledger history remains, still
	// listed in the topology's stored bindings.
	trafficUsage.observe("p5", "Removed tunnel", t0, up64(0), up64(0))
	trafficUsage.observe("p5", "Removed tunnel", t0.Add(time.Minute), up64(11), up64(6))
	group := topologyDeleteStored(t, []string{"peer:5"})
	topologyInventory = func() []topologyTunnel { return []topologyTunnel{{ID: "peer:8", State: "healthy"}} }
	w := topologyRequest(http.MethodDelete, group.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("DELETE stale topology: %d %s", w.Code, w.Body.String())
	}
	trafficUsage.mu.Lock()
	_, alive := trafficUsage.links["p5"]
	trafficUsage.mu.Unlock()
	if alive {
		t.Fatal("deleting a topology must purge the usage records of its listed tunnels")
	}
}

// Zero topologies plus stale ledger history: the API must not emit an
// unassigned row at all (the reported «تونل‌های بدون توپولوژی» bug).
func TestUsageAPIStaleHistoryAloneShowsNoUnassignedRow(t *testing.T) {
	oldUsage, oldDir, oldInventory, oldSnap := trafficUsage, configDir, topologyInventory, snapPtr.Load()
	trafficUsage, configDir = newUsage(), t.TempDir()
	topologyInventory = func() []topologyTunnel { return []topologyTunnel{} }
	snapPtr.Store(nil)
	t.Cleanup(func() {
		trafficUsage, configDir, topologyInventory = oldUsage, oldDir, oldInventory
		snapPtr.Store(oldSnap)
	})
	// What the stale deleted tunnel left behind: recent records whose counters
	// are no longer measured (unknown) and whose ID is no longer in the
	// inventory — the exact shape of the reported bug.
	now := time.Now()
	trafficUsage.observe("p7", "deleted tunnel", now.Add(-time.Minute), up64(0), up64(0))
	trafficUsage.observe("p7", "deleted tunnel", now, up64(30), up64(5))
	rec := httptest.NewRecorder()
	handleUsageGet(rec, httptest.NewRequest(http.MethodGet, "/api/traffic/usage?range=7d", nil))
	var out struct {
		Topologies []topologyUsageRow `json:"topologies"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Topologies) != 0 {
		t.Fatalf("no topology rows without topologies: %s", rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if _, exists := m["unassigned"]; exists {
		t.Fatalf("stale history alone must not emit an unassigned row: %s", rec.Body.String())
	}
}
