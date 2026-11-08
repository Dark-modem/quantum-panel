package panel

// meshweightscheck_test.go — the dashboard's first-run checklist reads
// ONE boolean (any non-default weight policy) instead of fanning out to
// /topologies/{id}/weights per topology (phase-10 e2e: 22 parallel
// fetches starved the browser pool). Pins the endpoint + route.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestMeshWeightsCheck(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	mux := http.NewServeMux()
	registerMeshRoutes(mux, "")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client := meshSession(t)

	get := func() map[string]any {
		resp, err := client.Get(srv.URL + "/api/mesh/weights/check")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("weights/check status = %d", resp.StatusCode)
		}
		var v map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if v := get(); v["any_custom"] != false {
		t.Fatalf("fresh store any_custom = %v, want false", v["any_custom"])
	}

	s := withMeshStore(t)
	topo, _ := s.UpsertTopology(store.Topology{Name: "wt", Enabled: true})
	local, _ := s.UpsertServer(store.Server{Name: "wl", Role: "iran"})
	remote, _ := s.UpsertServer(store.Server{Name: "wf", Role: "foreign"})
	tun, _ := s.UpsertTunnel(store.Tunnel{TunnelKey: "wk", TopologyID: topo,
		LocalServerID: local, RemoteServerID: remote, Enabled: true})
	if err := s.SetWeightPolicy(topo, 0, 0, tun, 7); err != nil {
		t.Fatal(err)
	}
	if v := get(); v["any_custom"] != true {
		t.Fatalf("after a weight-7 policy any_custom = %v, want true", v["any_custom"])
	}
	// a default-weight policy (weight == 1) does not count as custom
	if err := s.SetWeightPolicy(topo, 0, 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if v := get(); v["any_custom"] != true {
		t.Fatalf("default-weight policy must not flip any_custom, got %v", v["any_custom"])
	}
}
