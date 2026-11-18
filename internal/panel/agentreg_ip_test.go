package panel

// Tests for the Phase-4 public_ip validation: a loopback or link-local
// remote address must never poison the mesh hub address.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentPublicIPUsable(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"198.51.100.1", true},
		{"203.0.113.1", true},
		{"2001:db8::1", true},
		{"127.0.0.1", false},
		{"::1", false},
		{"0.0.0.0", false},
		{"::", false},
		{"169.254.1.1", false},
		{"fe80::1", false},
		{"", false},
		{"not-an-ip", false},
	}
	for _, c := range cases {
		if got := agentPublicIPUsable(c.ip); got != c.want {
			t.Errorf("agentPublicIPUsable(%q) = %v, want %v", c.ip, got, c.want)
		}
	}
}

func TestAgentSafePublicIP(t *testing.T) {
	if got := agentSafePublicIP("198.51.100.1"); got != "198.51.100.1" {
		t.Fatalf("safe public IP = %q", got)
	}
	if got := agentSafePublicIP("127.0.0.1"); got != "" {
		t.Fatalf("loopback must return empty, got %q", got)
	}
	if got := agentSafePublicIP("169.254.1.1"); got != "" {
		t.Fatalf("link-local must return empty, got %q", got)
	}
	if got := agentSafePublicIP("0.0.0.0"); got != "" {
		t.Fatalf("unspecified must return empty, got %q", got)
	}
}

// TestEnrollLoopbackIPDoesNotPoison verifies that an enrollment arriving
// from 127.0.0.1 does not set PublicIP to 127.0.0.1 (the mesh hub
// address bug from Phase 3's real-server deployment).
func TestEnrollLoopbackIPDoesNotPoison(t *testing.T) {
	withTempConfig(t)
	// Close the SQLite store before t.TempDir's RemoveAll: on Windows an
	// open quantum.db handle blocks the cleanup (LIFO order puts this first).
	t.Cleanup(meshStoreCloseForTest)

	// Create a token via the real handler.
	tok := createTestToken(t)

	// Enroll from 127.0.0.1 (loopback): the handler must succeed but
	// must NOT set PublicIP to 127.0.0.1 in the agent record.
	req := httptest.NewRequest("POST", "/api/agent/enroll", strings.NewReader(
		`{"name":"test-agent","hostname":"h","os":"linux","arch":"amd64","version":"v1","caps":["matrix"]}`))
	req.Header.Set(agentEnrollHeader, tok)
	req.RemoteAddr = "127.0.0.1:12345"
	rr := httptest.NewRecorder()
	handleAgentEnroll(rr, req)

	// The enroll itself should succeed (the loopback IP is valid for
	// the HTTP layer), but the PublicIP must be empty.
	if rr.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", rr.Code, rr.Body.String())
	}
	agentRegMu.Lock()
	agents, _, _ := agentRegistryLoad()
	agentRegMu.Unlock()
	for _, a := range agents {
		if a.PublicIP == "127.0.0.1" {
			t.Errorf("agent %s PublicIP = 127.0.0.1 (loopback poisoned the record)", a.ID)
		}
	}
}

// createTestToken creates one enrollment token via the real handler.
func createTestToken(t *testing.T) string {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/agents/tokens", strings.NewReader(
		`{"role":"iran","label":"test","expires_days":1}`))
	handleAgentTokenPost(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("token create: %d %s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Token
}

// TestMeshAgentPublicIPEndpoint covers the explicit override endpoint: a
// usable address is stored and wins in the tunnel-layer lookup, a
// loopback one is rejected with a clear error, and an empty value clears
// the override back to auto-detection.
func TestMeshAgentPublicIPEndpoint(t *testing.T) {
	withTempConfig(t)
	// Close the SQLite store before t.TempDir's RemoveAll (Windows keeps
	// the quantum.db handle locked; LIFO order puts this cleanup first).
	t.Cleanup(meshStoreCloseForTest)
	tok := createTestToken(t)

	// Enroll from a usable public address so the record exists.
	req := httptest.NewRequest("POST", "/api/agent/enroll", strings.NewReader(
		`{"name":"n","hostname":"h","os":"linux","arch":"amd64","version":"v1","caps":["matrix"]}`))
	req.Header.Set(agentEnrollHeader, tok)
	req.RemoteAddr = "198.51.100.1:1111"
	rr := httptest.NewRecorder()
	handleAgentEnroll(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enroll: %d %s", rr.Code, rr.Body.String())
	}
	agentRegMu.Lock()
	agents, _, _ := agentRegistryLoad()
	agentRegMu.Unlock()
	if len(agents) != 1 {
		t.Fatalf("want 1 enrolled agent, got %d", len(agents))
	}
	id := agents[0].ID

	setOverride := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/mesh/agents/"+id+"/public-ip", strings.NewReader(body))
		req.SetPathValue("id", id)
		handleMeshAgentPublicIP(rr, req)
		return rr
	}

	// Loopback must be rejected with a clear error, not stored.
	if rr := setOverride(`{"public_ip":"127.0.0.1"}`); rr.Code == http.StatusOK {
		t.Fatalf("loopback override must be rejected, got 200")
	}
	agentRegMu.Lock()
	agents, _, _ = agentRegistryLoad()
	agentRegMu.Unlock()
	if agents[0].PublicIPOverride != "" {
		t.Fatalf("rejected override leaked into the record: %q", agents[0].PublicIPOverride)
	}

	// A usable address is stored and wins in the tunnel-layer lookup.
	if rr := setOverride(`{"public_ip":"203.0.113.9"}`); rr.Code != http.StatusOK {
		t.Fatalf("set override: %d %s", rr.Code, rr.Body.String())
	}
	agentRegMu.Lock()
	agents, _, _ = agentRegistryLoad()
	agentRegMu.Unlock()
	if agents[0].PublicIPOverride != "203.0.113.9" {
		t.Fatalf("override not stored: %q", agents[0].PublicIPOverride)
	}
	info, ok := agentTunnelRegistryLookup(id)
	if !ok || info.PublicIP != "203.0.113.9" {
		t.Fatalf("lookup PublicIP = %q (want override 203.0.113.9)", info.PublicIP)
	}

	// Empty clears the override back to auto-detection.
	if rr := setOverride(`{"public_ip":""}`); rr.Code != http.StatusOK {
		t.Fatalf("clear override: %d %s", rr.Code, rr.Body.String())
	}
	agentRegMu.Lock()
	agents, _, _ = agentRegistryLoad()
	agentRegMu.Unlock()
	if agents[0].PublicIPOverride != "" {
		t.Fatalf("override not cleared: %q", agents[0].PublicIPOverride)
	}
	info, ok = agentTunnelRegistryLookup(id)
	if !ok || info.PublicIP != "198.51.100.1" {
		t.Fatalf("lookup after clear = %q (want auto-detected 198.51.100.1)", info.PublicIP)
	}
}
