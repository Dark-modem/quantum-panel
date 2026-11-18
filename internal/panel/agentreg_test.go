package panel

// Tests for the agent registry store, enrollment and registry API handlers
// (agentreg.go). The hub/session tests live in agenthub_test.go.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
)

// agentTestTokenBody is a valid enrollment request body.
const agentTestTokenBody = `{"name":"lab-agent","hostname":"node1","os":"linux","arch":"amd64","version":"1.0.0","caps":["matrix","traffic"]}`

func agentTestReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func agentTestWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func agentTestFileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode()
}

// agentTestCreateToken mints one enrollment token through the real handler.
func agentTestCreateToken(t *testing.T, role string, days int) (id, token string) {
	t.Helper()
	body := fmt.Sprintf(`{"role":%q,"label":"lab","expires_days":%d}`, role, days)
	req := httptest.NewRequest("POST", "/api/agents/tokens", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleAgentTokenPost(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("token create: got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("token create decode: %v", err)
	}
	return out.ID, out.Token
}

// agentTestEnroll runs the enrollment handler with the given token header.
func agentTestEnroll(t *testing.T, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/agents/enroll", strings.NewReader(body))
	req.RemoteAddr = "203.0.113.9:4444"
	if token != "" {
		req.Header.Set(agentEnrollHeader, token)
	}
	rec := httptest.NewRecorder()
	handleAgentEnroll(rec, req)
	return rec
}

// agentTestLoadRegistry reads the store back (fresh load) under the lock.
func agentTestLoadRegistry(t *testing.T) ([]agentRecord, []agentEnrollToken) {
	t.Helper()
	agentRegMu.Lock()
	defer agentRegMu.Unlock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		t.Fatalf("registry load: %v", err)
	}
	return agents, tokens
}

// agentTestAgent returns the record with the given ID (fresh load). Note
// the load forces non-revoked agents offline (crash-recovery semantics) —
// use agentTestRawAgent when a test needs the status as persisted on disk.
func agentTestAgent(t *testing.T, id string) agentRecord {
	t.Helper()
	agents, _ := agentTestLoadRegistry(t)
	for _, a := range agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("agent %s not found in registry", id)
	return agentRecord{}
}

// agentTestRawAgent decodes agents.json directly, without the
// load-time offline forcing, so hub tests can assert the persisted status.
func agentTestRawAgent(t *testing.T, id string) agentRecord {
	t.Helper()
	agentRegMu.Lock()
	data := agentTestReadFile(t, agentRegistryPath())
	agentRegMu.Unlock()
	var stored agentRegistryFile
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatalf("decode agents.json: %v", err)
	}
	for _, a := range stored.Agents {
		if a.ID == id {
			return a
		}
	}
	t.Fatalf("agent %s not found in agents.json", id)
	return agentRecord{}
}

func TestAgentRegEnrollLifecycle(t *testing.T) {
	withTempConfig(t)
	// The enroll flow lazily opens the file-backed mesh store; close it
	// before the test's temp-dir cleanup races it on Windows (BACKEND-24).
	t.Cleanup(meshStoreCloseForTest)

	// Missing token header is a rejection.
	if rec := agentTestEnroll(t, "", agentTestTokenBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: got %d want 401", rec.Code)
	}

	_, token := agentTestCreateToken(t, "foreign", 7)

	// Bad body.
	if rec := agentTestEnroll(t, token, "{not json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad body: got %d want 400", rec.Code)
	}

	// Valid enrollment.
	rec := agentTestEnroll(t, token, agentTestTokenBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll: got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID       string `json:"id"`
		Key      string `json:"key"`
		BasePath string `json:"base_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("enroll decode: %v", err)
	}
	if !strings.HasPrefix(out.ID, "agt_") || len(out.ID) != 4+12 {
		t.Fatalf("agent id shape: %q", out.ID)
	}
	if len(out.Key) != agentKeyBytes*2 {
		t.Fatalf("agent key length: %d", len(out.Key))
	}
	a := agentTestAgent(t, out.ID)
	if a.Status != agentStatusOffline {
		t.Fatalf("new agent status: got %q want offline", a.Status)
	}
	if a.KeyHash != agentSha256Hex(out.Key) {
		t.Fatalf("stored key hash does not match the returned key")
	}
	if a.Role != "foreign" || a.PublicIP != "203.0.113.9" || a.Hostname != "node1" || a.OS != "linux" || a.Arch != "amd64" || a.Version != "1.0.0" {
		t.Fatalf("stored enrollment metadata wrong: %+v", a)
	}
	if !agentHasCap(a.Caps, "matrix") || !agentHasCap(a.Caps, "traffic") {
		t.Fatalf("caps not stored: %v", a.Caps)
	}
	if a.CreatedAt <= 0 {
		t.Fatalf("created_at missing: %d", a.CreatedAt)
	}

	// The plaintext key must never reach the store.
	agentRegMu.Lock()
	data := agentTestReadFile(t, agentRegistryPath())
	agentRegMu.Unlock()
	if strings.Contains(string(data), out.Key) {
		t.Fatalf("plaintext agent key found in agents.json")
	}

	// Single use: the same token again is a rejection.
	if rec := agentTestEnroll(t, token, agentTestTokenBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("token reuse: got %d want 401", rec.Code)
	}

	// Expired token is a rejection.
	_, expired := agentTestCreateToken(t, "iran", 7)
	agentRegMu.Lock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for i := range tokens {
		if tokens[i].TokenHash == agentSha256Hex(expired) {
			tokens[i].ExpiresAt = 1
		}
	}
	if err := saveAgentRegistry(agents, tokens); err != nil {
		t.Fatalf("save expired token: %v", err)
	}
	agentRegMu.Unlock()
	if rec := agentTestEnroll(t, expired, agentTestTokenBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired token: got %d want 401", rec.Code)
	}

	// Revoked token is a rejection.
	revID, revoked := agentTestCreateToken(t, "iran", 7)
	req := httptest.NewRequest("POST", "/api/agents/tokens/"+revID+"/revoke", strings.NewReader("{}"))
	req.SetPathValue("id", revID)
	rr := httptest.NewRecorder()
	handleAgentTokenRevoke(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("token revoke: got %d: %s", rr.Code, rr.Body.String())
	}
	if rec := agentTestEnroll(t, revoked, agentTestTokenBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: got %d want 401", rec.Code)
	}

	// Unknown token is a rejection.
	if rec := agentTestEnroll(t, "enr_00000000000000000000000000000000", agentTestTokenBody); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown token: got %d want 401", rec.Code)
	}
}

func TestAgentRegTokenLifecycle(t *testing.T) {
	withTempConfig(t)

	id, token := agentTestCreateToken(t, "iran", 3)
	if !strings.HasPrefix(token, "enr_") || len(token) != 4+32 {
		t.Fatalf("token shape: %q", token)
	}

	// Enroll an agent, then mint another token: the registry save that
	// stores the token must never drop existing agent records.
	recEnroll := agentTestEnroll(t, token, agentTestTokenBody)
	if recEnroll.Code != http.StatusOK {
		t.Fatalf("enroll: got %d: %s", recEnroll.Code, recEnroll.Body.String())
	}
	var enrolled struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(recEnroll.Body.Bytes(), &enrolled); err != nil {
		t.Fatalf("enroll decode: %v", err)
	}
	agentTestCreateToken(t, "foreign", 7)
	agentTestRawAgent(t, enrolled.ID) // must still exist after the second token save

	// List shows the prefix only, never the plaintext token or its hash.
	req := httptest.NewRequest("GET", "/api/agents/tokens", nil)
	rec := httptest.NewRecorder()
	handleAgentTokensGet(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tokens get: got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, token) || strings.Contains(body, agentSha256Hex(token)) {
		t.Fatalf("token material leaked through the list endpoint")
	}
	var out struct {
		Tokens []struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
			Role   string `json:"role"`
			Label  string `json:"label"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("tokens decode: %v", err)
	}
	if len(out.Tokens) < 2 {
		t.Fatalf("token list too short: %+v", out.Tokens)
	}
	var listed bool
	for _, tk := range out.Tokens {
		if tk.ID == id {
			listed = true
			if tk.Prefix != token[:8] || tk.Role != "iran" {
				t.Fatalf("token list entry wrong: %+v", tk)
			}
		}
	}
	if !listed {
		t.Fatalf("created token %s missing from the list", id)
	}

	// Default expiry is 7 days when expires_days is omitted.
	_, tok2 := agentTestCreateTokenRaw(t, `{"role":"foreign"}`)
	_, tokens := agentTestLoadRegistry(t)
	for _, tk := range tokens {
		if tk.TokenHash == agentSha256Hex(tok2) {
			want := tk.CreatedAt + 7*24*3600
			if tk.ExpiresAt < want-2 || tk.ExpiresAt > want+2 {
				t.Fatalf("default expiry: got %d want ~%d", tk.ExpiresAt, want)
			}
		}
	}

	// expires_days beyond the cap is rejected.
	req2 := httptest.NewRequest("POST", "/api/agents/tokens", strings.NewReader(`{"role":"iran","expires_days":366}`))
	rr := httptest.NewRecorder()
	handleAgentTokenPost(rr, req2)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expires_days cap: got %d want 400", rr.Code)
	}

	// Invalid role is rejected.
	req3 := httptest.NewRequest("POST", "/api/agents/tokens", strings.NewReader(`{"role":"both"}`))
	rr3 := httptest.NewRecorder()
	handleAgentTokenPost(rr3, req3)
	if rr3.Code != http.StatusBadRequest {
		t.Fatalf("role validation: got %d want 400", rr3.Code)
	}
}

// agentTestCreateTokenRaw mints a token from a raw JSON body (for edge cases).
func agentTestCreateTokenRaw(t *testing.T, body string) (id, token string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/agents/tokens", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handleAgentTokenPost(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("token create raw: got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("token decode: %v", err)
	}
	return out.ID, out.Token
}

func TestAgentRegTokenRevokeNotFound(t *testing.T) {
	withTempConfig(t)
	req := httptest.NewRequest("POST", "/api/agents/tokens/tok_ffffffffffff/revoke", strings.NewReader("{}"))
	req.SetPathValue("id", "tok_ffffffffffff")
	rec := httptest.NewRecorder()
	handleAgentTokenRevoke(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown token revoke: got %d want 404", rec.Code)
	}
}

func TestAgentRegPersistence(t *testing.T) {
	withTempConfig(t)

	_, token := agentTestCreateToken(t, "foreign", 7)
	rec := agentTestEnroll(t, token, agentTestTokenBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll: got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("enroll decode: %v", err)
	}

	// Simulate a crashed process that died while the agent was online.
	if err := agentRegistryUpdate(out.ID, func(a *agentRecord) {
		a.Status = agentStatusOnline
		a.LastSeen = 12345
	}); err != nil {
		t.Fatalf("mark online: %v", err)
	}

	// A "restart" = fresh load from the same file: the record survives, but
	// the stale online status is forced offline.
	a := agentTestAgent(t, out.ID)
	if a.Status != agentStatusOffline {
		t.Fatalf("restart recovery: got status %q want offline", a.Status)
	}
	if a.KeyHash != agentSha256Hex(out.Key) || a.LastSeen != 12345 || a.Role != "foreign" {
		t.Fatalf("record did not survive the restart intact: %+v", a)
	}

	// The store must be 0600 (POSIX; Windows maps chmod to a read-only bit
	// and reports 0666, so the exact check is Unix-only).
	agentRegMu.Lock()
	mode := agentTestFileMode(t, agentRegistryPath())
	agentRegMu.Unlock()
	if runtime.GOOS != "windows" && mode.Perm() != 0600 {
		t.Fatalf("agents.json permissions: got %v want 0600", mode.Perm())
	}
}

func TestAgentRegCorruptFileStartsEmpty(t *testing.T) {
	withTempConfig(t)
	agentTestWriteFile(t, agentRegistryPath(), []byte("{corrupted"))
	agents, tokens := agentTestLoadRegistry(t)
	if len(agents) != 0 || len(tokens) != 0 {
		t.Fatalf("corrupt file did not start empty: %d agents %d tokens", len(agents), len(tokens))
	}
	if _, err := os.Stat(agentRegistryPath() + ".bad"); err != nil {
		t.Fatalf("corrupt file was not moved aside: %v", err)
	}
}

func TestAgentRegAPIViews(t *testing.T) {
	withTempConfig(t)

	_, token := agentTestCreateToken(t, "foreign", 7)
	rec := agentTestEnroll(t, token, agentTestTokenBody)
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("enroll decode: %v", err)
	}
	a := agentTestAgent(t, out.ID)

	// Queue a desired state whose hub payload embeds a secret: the list and
	// detail views must summarize it, never echo the payload.
	rev := "1700000000000000000"
	d := agentDesired{Revision: rev, Matrix: &agentDesiredMatrix{
		Instance: "matrix", Action: "apply",
		Hub: &agentDesiredHub{Family: "ipv4", Port: 443, Addr: "198.51.100.1", Ports: []int{443}, Token: "hmx1_SECRET"},
	}}
	if err := agentHubDeliverDesired(out.ID, d); err != nil {
		t.Fatalf("deliver desired: %v", err)
	}

	// Unknown agent detail -> E-AGENT-03 404.
	req404 := httptest.NewRequest("GET", "/api/agents/agt_ffffffffffff", nil)
	req404.SetPathValue("id", "agt_ffffffffffff")
	rec404 := httptest.NewRecorder()
	handleAgentGet(rec404, req404)
	if rec404.Code != http.StatusNotFound || !strings.Contains(rec404.Body.String(), "E-AGENT-03") {
		t.Fatalf("unknown agent detail: got %d %s", rec404.Code, rec404.Body.String())
	}

	// List view: has_pending_desired true, no hub token, no key hash.
	reqList := httptest.NewRequest("GET", "/api/agents", nil)
	recList := httptest.NewRecorder()
	handleAgentsGet(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("agents list: got %d", recList.Code)
	}
	listBody := recList.Body.String()
	if strings.Contains(listBody, "hmx1_SECRET") || strings.Contains(listBody, a.KeyHash) {
		t.Fatalf("list view leaked the desired payload or key hash")
	}
	var list struct {
		Agents []struct {
			ID                string `json:"id"`
			Status            string `json:"status"`
			HasPendingDesired bool   `json:"has_pending_desired"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(recList.Body.Bytes(), &list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	if len(list.Agents) != 1 || list.Agents[0].ID != out.ID || !list.Agents[0].HasPendingDesired {
		t.Fatalf("agents list wrong: %+v", list.Agents)
	}

	// Detail view: summary of the pending desired only.
	reqGet := httptest.NewRequest("GET", "/api/agents/"+out.ID, nil)
	reqGet.SetPathValue("id", out.ID)
	recGet := httptest.NewRecorder()
	handleAgentGet(recGet, reqGet)
	if recGet.Code != http.StatusOK {
		t.Fatalf("agent detail: got %d: %s", recGet.Code, recGet.Body.String())
	}
	detailBody := recGet.Body.String()
	if strings.Contains(detailBody, "hmx1_SECRET") || strings.Contains(detailBody, a.KeyHash) {
		t.Fatalf("detail view leaked the hub payload or key hash")
	}
	var detail map[string]any
	if err := json.Unmarshal(recGet.Body.Bytes(), &detail); err != nil {
		t.Fatalf("detail decode: %v", err)
	}
	pd, ok := detail["pending_desired"].(map[string]any)
	if !ok || pd["revision"] != rev || pd["instance"] != "matrix" || pd["action"] != "apply" {
		t.Fatalf("pending desired summary wrong: %v", detail["pending_desired"])
	}
	if _, has := detail["last_state"]; !has {
		t.Fatalf("detail view missing last_state key")
	}
}
