package panel

// uifold_test.go — phase 17: every folded /api/ui read answers like its
// legacy /api/mesh twin (SAME handler → same payload, additive alias),
// and the legacy routes keep working.

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestUIFoldReadAliases(t *testing.T) {
	srv := uiServer(t)
	c := meshSession(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)

	pairs := [][2]string{
		{"/api/ui/topologies", "/api/mesh/topologies"},
		{"/api/ui/mesh/overview", "/api/mesh/overview"},
		{"/api/ui/engines", "/api/mesh/engines"},
		{"/api/ui/engines/artifacts", "/api/mesh/engines/artifacts"},
		{"/api/ui/engines/keysnippet", "/api/mesh/engines/keysnippet"},
		{"/api/ui/engines/templates", "/api/mesh/engines/templates"},
		{"/api/ui/alerts", "/api/mesh/alerts"},
		{"/api/ui/pasarguard", "/api/mesh/pasarguard"},
		{"/api/ui/pasarguard/overhead", "/api/mesh/overhead"},
		{"/api/ui/profiles/pending", "/api/mesh/profiles/pending"},
	}
	for _, p := range pairs {
		folded, legacy := p[0], p[1]
		m := meshGetMap(t, c, srv, folded) // 200 + valid JSON object, else t.Fatal
		if len(m) == 0 {
			t.Fatalf("%s returned an empty object", folded)
		}
		resp, err := c.Get(srv.URL + legacy)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("legacy %s must keep working: %d", legacy, resp.StatusCode)
		}
		var legacyOut map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&legacyOut); err != nil {
			t.Fatalf("legacy %s not json: %v", legacy, err)
		}
	}

	// Payload spot-checks: the folded alerts view carries the phase-16
	// maintenance array; the folded topology list enumerates topologies.
	m := meshGetMap(t, c, srv, "/api/ui/alerts")
	if _, ok := m["maintenance"]; !ok {
		t.Fatalf("folded alerts view missing maintenance: %v", m)
	}
	m = meshGetMap(t, c, srv, "/api/ui/topologies")
	if _, ok := m["topologies"]; !ok {
		t.Fatalf("folded topology list missing topologies: %v", m)
	}

	// Coefficient needs its query param (same validation as the twin).
	respC, err := c.Get(srv.URL + "/api/ui/pasarguard/coefficient")
	if err != nil {
		t.Fatal(err)
	}
	defer respC.Body.Close()
	if respC.StatusCode != http.StatusBadRequest {
		t.Fatalf("folded coefficient without datacenter_id must 400, got %d", respC.StatusCode)
	}
}
