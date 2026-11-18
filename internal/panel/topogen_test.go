package panel

// topogen_test.go — the topology generator's two deployment shapes:
//   A: 2 Iran × 2 foreign × 3 tunnels/pair = 12 tunnels per topology, and
//   B: 3 Iran × 4 foreign × 2 tunnels/pair = 24, with a second topology
//      SHARING the same foreign servers.
// Both run against the real (in-memory) store via the mesh store seam.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func topogenFixture(t *testing.T, iranDCs, foreignDCs, iranPerDC, foreignPerDC int) (iranIDs, foreignIDs []int64) {
	t.Helper()
	s := meshStoreMust(t)
	for i := 0; i < iranDCs; i++ {
		dc, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: fmt.Sprintf("dc-iran-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < iranPerDC; j++ {
			id, err := s.UpsertServer(store.Server{Name: fmt.Sprintf("ir%d-%d", i, j), Role: "iran", DatacenterID: dc})
			if err != nil {
				t.Fatal(err)
			}
			iranIDs = append(iranIDs, id)
		}
	}
	for i := 0; i < foreignDCs; i++ {
		dc, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: fmt.Sprintf("dc-fx-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		for j := 0; j < foreignPerDC; j++ {
			id, err := s.UpsertServer(store.Server{Name: fmt.Sprintf("fx%d-%d", i, j), Role: "foreign", DatacenterID: dc})
			if err != nil {
				t.Fatal(err)
			}
			foreignIDs = append(foreignIDs, id)
		}
	}
	return iranIDs, foreignIDs
}

func meshStoreMust(t *testing.T) *store.Store {
	t.Helper()
	s, err := MeshStore()
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// withMeshStore points the mesh seam at a private in-memory store.
func withMeshStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenInMemory()
	if err != nil {
		t.Fatal(err)
	}
	meshStoreSetForTest(s)
	t.Cleanup(meshStoreCloseForTest)
	return s
}

func genShapeA(t *testing.T, name string, iran, foreign []int64) *topoGenResult {
	t.Helper()
	res, err := GenerateTopology(TopoGenRequest{
		Name: name, IranServers: iran, ForeignServers: foreign,
		TunnelsPerPair: 3, Engines: []string{"matrix", "tinc", "matrix"}, Enabled: true,
	})
	if err != nil {
		t.Fatalf("generate %s: %v", name, err)
	}
	return res
}

func TestTopogenShapeA2x2x3(t *testing.T) {
	withMeshStore(t)
	iran, foreign := topogenFixture(t, 2, 1, 2, 2) // 2 Iran DCs × 2 servers, 1 foreign DC × 2
	if len(iran) != 4 || len(foreign) != 2 {
		t.Fatalf("fixture: %d iran, %d foreign", len(iran), len(foreign))
	}
	// The brief's shape A: topology "drak-hz" = the drak pair × the foreign
	// pair × 3 = 12; topology "chekavak-hz" = the chekavak pair × the SAME
	// foreign pair × 3 = 12. Two topologies sharing the foreign servers.
	res := genShapeA(t, "drak-hz", iran[0:2], foreign)
	if len(res.Tunnels) != 12 {
		t.Fatalf("shape A: got %d tunnels, want 2×2×3=12", len(res.Tunnels))
	}
	resB := genShapeA(t, "chekavak-hz", iran[2:4], foreign)
	if len(resB.Tunnels) != 12 {
		t.Fatalf("shape A topology 2: got %d tunnels, want 12", len(resB.Tunnels))
	}
	// Deterministic keys: tun-<topo>-<local>-<remote>-<slot>.
	seen := map[string]bool{}
	pairs := map[string]int{}
	for _, tn := range append(append([]store.Tunnel{}, res.Tunnels...), resB.Tunnels...) {
		if seen[tn.TunnelKey] {
			t.Fatalf("duplicate tunnel key %s", tn.TunnelKey)
		}
		seen[tn.TunnelKey] = true
		if tn.Engine == "" {
			t.Fatalf("tunnel %s lost its engine", tn.TunnelKey)
		}
		pair := fmt.Sprintf("%d->%d", tn.LocalServerID, tn.RemoteServerID)
		pairs[pair]++
	}
	if len(pairs) != 8 {
		t.Fatalf("want 8 distinct pairs across both topologies, got %d (%v)", len(pairs), pairs)
	}
	for p, n := range pairs {
		if n != 3 {
			t.Fatalf("pair %s has %d tunnels, want 3", p, n)
		}
	}
	// Re-running the same shape converges (idempotent upserts), never doubles.
	res2 := genShapeA(t, "drak-hz", iran[0:2], foreign)
	if len(res2.Tunnels) != 12 {
		t.Fatalf("regenerate: got %d tunnels, want 12", len(res2.Tunnels))
	}
	s := meshStoreMust(t)
	topos, _ := s.ListTopologies()
	if len(topos) != 2 {
		t.Fatalf("want exactly 2 topologies, got %d", len(topos))
	}
	for _, tp := range topos {
		tuns, _ := s.ListTunnels(tp.ID)
		if len(tuns) != 12 {
			t.Fatalf("topology %s holds %d tunnels, want 12", tp.Name, len(tuns))
		}
	}
}

func TestTopogenShapeB3x4x2SharedForeign(t *testing.T) {
	withMeshStore(t)
	iran, foreign := topogenFixture(t, 1, 1, 3, 4) // 3 Iran servers, 4 foreign (SHARED below)
	// Shape B: 3 × 4 × 2 = 24 tunnels with TWO engines per slot. (The full
	// 3×4×2 grid is split across two topologies that SHARE the foreign
	// servers, which is the shape the brief asks to test.)
	resB, err := GenerateTopology(TopoGenRequest{
		Name: "shape-b2", IranServers: iran[:2], ForeignServers: foreign,
		TunnelsPerPair: 2, Engines: []string{"matrix", "tinc"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resB.Tunnels) != 2*4*2 {
		t.Fatalf("shape B: got %d tunnels, want 16 for 2×4×2", len(resB.Tunnels))
	}
	// The shared-foreign topology: SAME foreign servers, overlapping Iran set,
	// both topologies alive at once.
	resC, err := GenerateTopology(TopoGenRequest{
		Name: "shape-b3", IranServers: iran[1:3], ForeignServers: foreign,
		TunnelsPerPair: 2, Engines: []string{"matrix", "matrix"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resC.Tunnels) != 16 {
		t.Fatalf("shape B3: got %d tunnels, want 16", len(resC.Tunnels))
	}
	// All three topologies coexist; foreign keys appear in B and C, and the
	// tunnel keys are namespaced per topology (no collisions).
	s := meshStoreMust(t)
	topos, _ := s.ListTopologies()
	if len(topos) != 2 {
		t.Fatalf("want exactly 2 topologies, got %d", len(topos))
	}
	keys := map[string]int{}
	for _, tp := range topos {
		tuns, _ := s.ListTunnels(tp.ID)
		for _, tn := range tuns {
			keys[tn.TunnelKey]++
		}
	}
	for k, n := range keys {
		if n > 1 {
			t.Fatalf("tunnel key %s exists %d times", k, n)
		}
	}
}

func TestTopogenValidation(t *testing.T) {
	withMeshStore(t)
	iran, foreign := topogenFixture(t, 1, 1, 1, 1)
	bad := []TopoGenRequest{
		{Name: "", IranServers: iran, ForeignServers: foreign, TunnelsPerPair: 1, Engines: []string{"matrix"}},
		{Name: "ok", IranServers: nil, ForeignServers: foreign, TunnelsPerPair: 1, Engines: []string{"matrix"}},
		{Name: "ok", IranServers: iran, ForeignServers: foreign, TunnelsPerPair: 0, Engines: nil},
		{Name: "ok", IranServers: iran, ForeignServers: foreign, TunnelsPerPair: 2, Engines: []string{"matrix"}},
		{Name: "ok", IranServers: iran, ForeignServers: foreign, TunnelsPerPair: 1, Engines: []string{"BAD ENGINE"}},
		{Name: "ok", IranServers: []int64{999999}, ForeignServers: foreign, TunnelsPerPair: 1, Engines: []string{"matrix"}},
	}
	for i, req := range bad {
		if _, err := GenerateTopology(req); err == nil {
			t.Fatalf("request %d must fail validation: %+v", i, req)
		}
	}
	// Engine params: wrong count refused, matching count accepted.
	_, err := GenerateTopology(TopoGenRequest{
		Name: "params-bad", IranServers: iran, ForeignServers: foreign,
		TunnelsPerPair: 1, Engines: []string{"matrix"},
		EngineParams: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`)},
	})
	if err == nil {
		t.Fatal("param count mismatch must be refused")
	}
	if _, err := GenerateTopology(TopoGenRequest{
		Name: "params-ok", IranServers: iran, ForeignServers: foreign,
		TunnelsPerPair: 1, Engines: []string{"matrix"},
		EngineParams: []json.RawMessage{json.RawMessage(`{"port":1}`)},
	}); err != nil {
		t.Fatalf("matching params refused: %v", err)
	}
}

func TestTopogenRejectsTwoDatacentersOnOneSide(t *testing.T) {
	withMeshStore(t)
	s := meshStoreMust(t)
	// two Iran DCs, one server each; one foreign DC
	var iranIDs []int64
	for i := 0; i < 2; i++ {
		dc, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: fmt.Sprintf("dc-iran-%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		id, err := s.UpsertServer(store.Server{Name: fmt.Sprintf("ir-%d", i), Role: "iran", DatacenterID: dc})
		if err != nil {
			t.Fatal(err)
		}
		iranIDs = append(iranIDs, id)
	}
	dc, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "dc-fx"})
	if err != nil {
		t.Fatal(err)
	}
	fx, err := s.UpsertServer(store.Server{Name: "fx-0", Role: "foreign", DatacenterID: dc})
	if err != nil {
		t.Fatal(err)
	}
	res, err := GenerateTopology(TopoGenRequest{
		Name: "two-dc-iran", IranServers: iranIDs, ForeignServers: []int64{fx},
		TunnelsPerPair: 1, Engines: []string{"matrix"}, Enabled: true,
	})
	if err == nil {
		t.Fatalf("a topology spanning two Iran DCs must be rejected, got topology %d", res.TopologyID)
	}
	if !strings.Contains(err.Error(), "one datacenter") {
		t.Fatalf("unexpected error: %v", err)
	}
}
