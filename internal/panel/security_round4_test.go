package panel

// security_round4_test.go — BACKEND-29 (input security round 4): hostile
// input at every ingest point, per endpoint family. Round 3 proved the
// middleware chain (auth/CSRF/caps) parity; round 4 proves the PAYLOAD
// handling: unicode, control characters, 10k-length strings, JSON
// depth/nesting, negative/huge numbers, wrong types, duplicate keys —
// clean structured 4xx (or the documented tolerant behavior), never a
// 500, never store corruption. Agent-owned fields (applied_revision,
// ack error, hello catalog) get the same bounds as the round-2 report
// caps on the paths added since (R7 mirror, ack bridge, hello catalog).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	protocol "github.com/Dark-modem/quantum-protocol"
)

// r4DeepJSON builds a {"a":{"a":{...}}} body nested `depth` levels — past
// the encoding/json decoder limit (10000) this must fail as a structured
// 4xx, never a stack overflow or a hang.
func r4DeepJSON(depth int) string {
	return `{"a":` + strings.Repeat(`{"a":`, depth) + `1` + strings.Repeat(`}`, depth) + `}`
}

// TestRound4DatacenterCoefficientBounds — the CREATE path validates the
// price coefficient exactly like PATCH always did (round-4 finding F1):
// negative/absurd values get a clean 400 E-IN-06, a sane fractional value
// is accepted, and the store's chokepoint rejects anything a non-HTTP
// caller tries.
func TestRound4DatacenterCoefficientBounds(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)

	for _, body := range []string{
		`{"name":"r4-neg","kind":"iran","price_coefficient":-5}`,
		`{"name":"r4-huge","kind":"iran","price_coefficient":1000001}`,
		`{"name":"r4-huge2","kind":"iran","price_coefficient":1e308}`,
	} {
		st, sb := r3Do(t, c, srv, "POST", "/api/mesh/datacenters", body)
		if st != http.StatusBadRequest || !strings.Contains(sb, "E-IN-06") {
			t.Fatalf("hostile coefficient %s: %d/%q (want 400 E-IN-06)", body, st, sb)
		}
	}
	// Sane values still work: fractional, and the 0 = default path.
	st, _ := r3Do(t, c, srv, "POST", "/api/mesh/datacenters", `{"name":"r4-frac","kind":"iran","price_coefficient":0.5}`)
	if st != http.StatusOK {
		t.Fatalf("fractional coefficient: %d (want 200)", st)
	}
	st, _ = r3Do(t, c, srv, "POST", "/api/mesh/datacenters", `{"name":"r4-def","kind":"foreign"}`)
	if st != http.StatusOK {
		t.Fatalf("no coefficient: %d (want 200)", st)
	}
	dc, err := s.DatacenterByName("r4-def")
	if err != nil || dc.PriceCoefficient != 1.0 {
		t.Fatalf("unset coefficient must store the 1.0 default: %+v err=%v", dc, err)
	}
	// Store chokepoint (defense in depth for non-HTTP callers).
	if _, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "r4-store-neg", PriceCoefficient: -1}); err == nil {
		t.Fatalf("store must reject a negative coefficient")
	}
	// PATCH validates at the handler too (round-4: clean 400, not the
	// store error's E-SYS-01/5xx).
	dc, _ = s.DatacenterByName("r4-frac")
	st, sb := r3Do(t, c, srv, "PATCH", fmt.Sprintf("/api/mesh/datacenters/%d", dc.ID), `{"price_coefficient":-3}`)
	if st != http.StatusBadRequest || !strings.Contains(sb, "E-IN-06") {
		t.Fatalf("patch negative coefficient: %d/%q (want 400 E-IN-06)", st, sb)
	}
}

// TestRound4NameCaps — datacenter/server names are bounded and
// control-character-free on create AND patch (round-4 finding F2); unicode
// names keep working; the boundary length is accepted.
func TestRound4NameCaps(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)

	long := strings.Repeat("x", 300)
	for _, tc := range []struct{ path, body string }{
		{"/api/mesh/datacenters", `{"name":"` + long + `","kind":"iran"}`},
		{"/api/mesh/servers", `{"name":"` + long + `","role":"iran"}`},
	} {
		st, sb := r3Do(t, c, srv, "POST", tc.path, tc.body)
		if st != http.StatusBadRequest || !strings.Contains(sb, "E-IN-06") {
			t.Fatalf("oversized name on %s: %d/%q (want 400 E-IN-06)", tc.path, st, sb)
		}
	}
	// Control characters are rejected (JSON \u0001 in the name).
	st, sb := r3Do(t, c, srv, "POST", "/api/mesh/datacenters", `{"name":"bad\u0001name","kind":"iran"}`)
	if st != http.StatusBadRequest || !strings.Contains(sb, "E-IN-06") {
		t.Fatalf("control-char name: %d/%q (want 400 E-IN-06)", st, sb)
	}
	// PATCH a hostile server name.
	st, _ = r3Do(t, c, srv, "POST", "/api/mesh/servers", `{"name":"r4-srv","role":"iran"}`)
	if st != http.StatusOK {
		t.Fatalf("server create: %d", st)
	}
	sv, err := s.ServerByName("r4-srv")
	if err != nil {
		t.Fatal(err)
	}
	st, sb = r3Do(t, c, srv, "PATCH", fmt.Sprintf("/api/mesh/servers/%d", sv.ID), `{"name":"`+long+`"}`)
	if st != http.StatusBadRequest || !strings.Contains(sb, "E-IN-06") {
		t.Fatalf("patch oversized name: %d/%q (want 400 E-IN-06)", st, sb)
	}
	// Unicode + the 200-char boundary stay accepted.
	st, _ = r3Do(t, c, srv, "POST", "/api/mesh/datacenters", `{"name":"تهران-آزادی","kind":"iran"}`)
	if st != http.StatusOK {
		t.Fatalf("unicode name: %d (want 200)", st)
	}
	if _, err := s.DatacenterByName("تهران-آزادی"); err != nil {
		t.Fatalf("unicode name not stored: %v", err)
	}
	boundary := strings.Repeat("b", 200)
	st, _ = r3Do(t, c, srv, "POST", "/api/mesh/datacenters", `{"name":"`+boundary+`","kind":"iran"}`)
	if st != http.StatusOK {
		t.Fatalf("200-char name (boundary): %d (want 200)", st)
	}
}

// TestRound4AgentAckBounds — the ack is agent-owned input (finding F3):
// a hostile ack carries an envelope-sized revision/error; both the legacy
// registry and the store's v2 mirror must persist bounded, redacted text.
func TestRound4AgentAckBounds(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)

	// Seed the legacy registry record with a pending desired revision.
	ag := agentRecord{ID: "agt_r4acktest0000", Name: "r4", Role: "iran", Status: agentStatusOffline}
	agentRegMu.Lock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		t.Fatal(err)
	}
	agents = append(agents, ag)
	if err := saveAgentRegistry(agents, tokens); err != nil {
		t.Fatal(err)
	}
	agentRegMu.Unlock()
	if err := agentRegistryUpdate(ag.ID, func(rec *agentRecord) {
		rec.Desired = &agentDesired{Revision: "rev-1"}
	}); err != nil {
		t.Fatal(err)
	}
	// Seed the store's v2 pending mirror (AckDesiredV2 matches on the
	// revision substring) + the agent row.
	if err := s.UpsertAgent(store.Agent{ID: ag.ID, Name: "r4", Role: "iran", Status: "offline"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPendingDesiredV2(ag.ID, `{"revision":"rev-1","topology":{}}`); err != nil {
		t.Fatal(err)
	}

	secret := strings.Repeat("a1b2c3d4e5f6a7b8", 4) // 64 hex chars: key-material shaped
	big := "password=" + secret + " " + strings.Repeat("E", 100*1024)
	payload := fmt.Sprintf(`{"id":%q,"revision":"rev-1","ok":false,"error":%q}`, ag.ID, big)
	c := &agentConn{id: ag.ID}
	c.handleAck([]byte(payload))

	var rec *agentRecord
	if err := agentRegistryUpdate(ag.ID, func(r *agentRecord) { cp := *r; rec = &cp }); err != nil {
		t.Fatal(err)
	}
	if rec.LastError == "" {
		t.Fatalf("ack error must be recorded (matched revision)")
	}
	if len(rec.LastError) > 500 {
		t.Fatalf("registry last_error not capped: %d bytes", len(rec.LastError))
	}
	if strings.Contains(rec.LastError, secret) {
		t.Fatalf("registry last_error leaked the secret-shaped material: %q", rec.LastError)
	}
	if rec.AppliedRevision != "" {
		t.Fatalf("ok=false ack must not advance the applied revision: %q", rec.AppliedRevision)
	}
	// ok=true ack with the SAME hostile-size error field: the revision is
	// accepted (matches the pending one) and the applied revision advances.
	okPayload := fmt.Sprintf(`{"id":%q,"revision":"rev-1","ok":true,"error":%q}`, ag.ID, big)
	c.handleAck([]byte(okPayload))
	if err := agentRegistryUpdate(ag.ID, func(r *agentRecord) { cp := *r; rec = &cp }); err != nil {
		t.Fatal(err)
	}
	if rec.AppliedRevision != "rev-1" {
		t.Fatalf("ok ack must advance the applied revision: %q", rec.AppliedRevision)
	}
	if rec.LastError != "" {
		t.Fatalf("ok ack must clear last_error: %q", rec.LastError)
	}
	// Store mirror (meshHandleAckV2 ran inside handleAck): last_error capped.
	v2, err := s.AgentV2(ag.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v2.LastError) > 500 {
		t.Fatalf("store last_error not capped: %d bytes", len(v2.LastError))
	}
	if strings.Contains(v2.LastError, secret) {
		t.Fatalf("store last_error leaked secret-shaped material")
	}
	if v2.DesiredV2 != "" {
		t.Fatalf("matched ok=false keeps pending: %q", v2.DesiredV2)
	}
}

// TestRound4AppliedRevisionCap — the R7-era report mirror caps the
// agent-supplied revision before it lands in the agents row (finding F3).
func TestRound4AppliedRevisionCap(t *testing.T) {
	s := abuseSeed(t)
	meshSampleMinGap = 0
	t.Cleanup(func() { meshSampleMinGap = 5 * time.Second })

	rep := &protocol.StateReportV2{
		Tunnels: []protocol.TunnelRuntime{{TunnelID: "ab-t1", Up: true}},
		Resources: &protocol.ResourceReport{
			CPUPercent: 5, MemTotalMB: 1024, MemUsedMB: 256,
		},
		AppliedRevision: strings.Repeat("r", 100*1024),
		ReportedAt:      time.Now().Unix(),
	}
	ingestStateReportV2("agt_good", rep)
	time.Sleep(50 * time.Millisecond)
	a, err := s.GetAgent("agt_good")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.AppliedRevision) > 128 {
		t.Fatalf("applied_revision not capped: %d bytes", len(a.AppliedRevision))
	}
	if a.MemUsedMB != 256 {
		t.Fatalf("honest resource data must survive: %+v", a)
	}
}

// TestRound4HelloCatalogCaps — the hello catalog path applies the same
// policy as the operator-side route (≤256 entries, bounded fields;
// finding F4). A 5000-entry hello with 200 KiB ids collapses to a bounded
// catalog that FITS the store's 64 KiB limit instead of being dropped.
func TestRound4HelloCatalogCaps(t *testing.T) {
	s := withMeshStore(t)
	ag := "agt_r4catalog000"
	if err := s.UpsertAgent(store.Agent{ID: ag, Name: "r4", Role: "iran", Status: "offline"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentV2Session(ag, 2, "[]"); err != nil {
		t.Fatal(err)
	}
	helloJSON := fmt.Sprintf(`{"id":%q,"engines":[%s]}`, ag, func() string {
		parts := make([]string, 0, 5000)
		for i := 0; i < 5000; i++ {
			parts = append(parts, fmt.Sprintf(`{"id":"eng-%d-%s","version":"v%d","params_schema":{"x":"%s"}}`,
				i, strings.Repeat("E", 200*1024/1024), i, strings.Repeat("s", 8192)))
		}
		return strings.Join(parts, ",")
	}())
	var hello agentHello
	if err := json.Unmarshal([]byte(helloJSON), &hello); err != nil {
		t.Fatal(err)
	}
	c := &agentConn{id: ag, protoV: 2}
	c.recordEngineCatalog(&hello)

	v2, err := s.AgentV2(ag)
	if err != nil {
		t.Fatal(err)
	}
	if v2.EngineCatalog == "" {
		t.Fatalf("bounded catalog must be stored (not dropped by the 64 KiB guard)")
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(v2.EngineCatalog), &entries); err != nil {
		t.Fatalf("stored catalog is not valid JSON: %v", err)
	}
	if len(entries) > 256 {
		t.Fatalf("catalog entries not capped: %d", len(entries))
	}
	for _, e := range entries {
		if id, _ := e["id"].(string); len(id) > 128 {
			t.Fatalf("catalog id not capped: %d bytes", len(id))
		}
		if sch, ok := e["params_schema"]; ok && sch != nil {
			if b, _ := json.Marshal(sch); len(b) > 4096 {
				t.Fatalf("catalog schema not capped: %d bytes", len(b))
			}
		}
	}
}

// TestRound4ProbePingHostGuard — the benchmark target_host override must
// never reach ping as an OPTION string (round-4 finding F5). The guard
// fires BEFORE the exec, so this stays hermetic.
func TestRound4ProbePingHostGuard(t *testing.T) {
	for _, host := range []string{"-f", "--help", "-w999999", "-"} {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, _, _, _, _, err := probePing(ctx, host, 1)
		cancel()
		if err == nil || !strings.Contains(err.Error(), "invalid host") {
			t.Fatalf("probePing(%q) must refuse option-shaped hosts, got %v", host, err)
		}
	}
}

// TestRound4HostileBodyMatrix — per endpoint family, adversarial JSON
// bodies get structured 4xx (or the documented tolerant behavior) and the
// store stays consistent afterwards. Families: mesh CRUD, tunnels,
// alerts settings, maintenance, backup, tokens, enroll.
func TestRound4HostileBodyMatrix(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	uiSeedWorld(t, s)
	c := meshSession(t)
	bare := &http.Client{}

	// deep nesting + wrong types + garbage: structured 4xx everywhere.
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/mesh/datacenters", r4DeepJSON(20000)},
		{"POST", "/api/mesh/datacenters", `{"name":123,"kind":"iran"}`},
		{"POST", "/api/mesh/datacenters", `[]`},
		{"POST", "/api/mesh/servers", r4DeepJSON(20000)},
		{"POST", "/api/mesh/tunnels", r4DeepJSON(20000)},
		{"POST", "/api/mesh/alerts/settings", r4DeepJSON(20000)},
		{"POST", "/api/ui/agents/agt_ir_seeded/maintenance", r4DeepJSON(20000)},
		{"POST", "/api/mesh/backup", r4DeepJSON(20000)},
		{"PATCH", "/api/mesh/datacenters/999999", `{"price_coefficient":"x"}`},
	} {
		st, sb := r3Do(t, c, srv, tc.method, tc.path, tc.body)
		if st < 400 || st >= 500 || !strings.Contains(sb, "E-") {
			t.Fatalf("hostile body %s %s: %d/%q (want 4xx E-*)", tc.method, tc.path, st, sb)
		}
	}

	// Duplicate keys: Go's decoder is last-wins — documented, deterministic,
	// no corruption (the stored name is exactly the LAST value).
	st, _ := r3Do(t, c, srv, "POST", "/api/mesh/datacenters",
		`{"name":"r4dup-a","name":"r4dup-b","kind":"iran"}`)
	if st != http.StatusOK {
		t.Fatalf("duplicate keys: %d (want 200)", st)
	}
	if _, err := s.DatacenterByName("r4dup-b"); err != nil {
		t.Fatalf("last-wins name missing: %v", err)
	}
	if _, err := s.DatacenterByName("r4dup-a"); err == nil {
		t.Fatalf("first duplicate key must not be stored")
	}

	// Tunnels: hostile keys / params (charset, length, JSON validity).
	for _, body := range []string{
		`{"topology_id":1,"local_server_id":1,"remote_server_id":2,"tunnel_key":"<script>alert(1)</script>","engine":"chisel"}`,
		`{"topology_id":1,"local_server_id":1,"remote_server_id":2,"tunnel_key":"` + strings.Repeat("k", 200) + `","engine":"chisel"}`,
		`{"topology_id":1,"local_server_id":1,"remote_server_id":2,"tunnel_key":"túnel-uni","engine":"chisel"}`,
		`{"topology_id":1,"local_server_id":1,"remote_server_id":2,"tunnel_key":"r4-ok","engine":"chisel","engine_params":"` + strings.Repeat("p", 17*1024) + `"}`,
		`{"topology_id":1,"local_server_id":1,"remote_server_id":2,"tunnel_key":"r4-ok","engine":"chisel","engine_params":{"x":` + r4DeepJSON(20000) + `}}`,
	} {
		st, sb := r3Do(t, c, srv, "POST", "/api/mesh/tunnels", body)
		// The deep-nesting case fails at the JSON decoder (E-IN-03); the
		// rest fail at the field validators (E-IN-06). Both are 400s.
		if st != http.StatusBadRequest || !strings.Contains(sb, "E-IN-") {
			t.Fatalf("hostile tunnel body %.60q: %d/%q (want 400 E-IN-*)", body, st, sb)
		}
	}

	// Alerts settings: out-of-range numbers are IGNORED (settings stay),
	// a hostile webhook scheme is rejected.
	def := meshAlertEng.settings.AgentOfflineMin
	st, _ = r3Do(t, c, srv, "POST", "/api/mesh/alerts/settings", `{"agent_offline_min":-999}`)
	if st != http.StatusOK {
		t.Fatalf("alerts settings hostile min: %d (want 200)", st)
	}
	if got := meshAlertEng.settings.AgentOfflineMin; got != def {
		t.Fatalf("out-of-range agent_offline_min must be ignored: %d -> %d", def, got)
	}
	st, sb := r3Do(t, c, srv, "POST", "/api/mesh/alerts/settings", `{"webhook_url":"javascript:alert(1)"}`)
	// E-IN-04's catalog status is 404 (the id-parse meaning); the envelope
	// code is the assertion (round-3 documented behavior).
	if st < 400 || st >= 500 || !strings.Contains(sb, "E-IN-04") {
		t.Fatalf("javascript webhook: %d/%q (want 4xx E-IN-04)", st, sb)
	}

	// Maintenance: past and >30d windows rejected.
	st, sb = r3Do(t, c, srv, "POST", "/api/ui/agents/agt_ir_seeded/maintenance", `{"until":-1}`)
	if st < 400 || st >= 500 || !strings.Contains(sb, "E-VALIDATION") {
		t.Fatalf("past maintenance: %d/%q", st, sb)
	}
	st, sb = r3Do(t, c, srv, "POST", "/api/ui/agents/agt_ir_seeded/maintenance", fmt.Sprintf(`{"until":%d}`, time.Now().Add(31*24*time.Hour).Unix()))
	if st < 400 || st >= 500 || !strings.Contains(sb, "E-VALIDATION") {
		t.Fatalf("31d maintenance: %d/%q", st, sb)
	}

	// Tokens: role enum + datacenter cap + expires window.
	for _, body := range []string{
		`{"role":"nope"}`,
		`{"role":"iran","datacenter":"` + strings.Repeat("d", 101) + `"}`,
		`{"role":"iran","expires_days":366}`,
	} {
		st, sb := r3Do(t, bare, srv, "POST", "/api/agents/tokens", body)
		if st != http.StatusBadRequest {
			t.Fatalf("hostile token body %.50q: %d/%q (want 400)", body, st, sb)
		}
	}
	// The 100-char datacenter boundary passes.
	st, _ = r3Do(t, bare, srv, "POST", "/api/agents/tokens", `{"role":"iran","datacenter":"`+strings.Repeat("d", 100)+`"}`)
	if st != http.StatusOK {
		t.Fatalf("100-char datacenter (boundary): %d (want 200)", st)
	}

	// Enroll (unauth): hostile bodies answer clean 400/401 (the handler
	// parses the bounded body before the token compare — pre-existing,
	// documented order; rate-limited + no registry mutation either way).
	for _, body := range []string{
		r4DeepJSON(20000),
		`{"name":"` + strings.Repeat("n", 10000) + `"}`,
		`{"caps":"` + strings.Repeat("c", 10000) + `"}`,
	} {
		req, _ := http.NewRequest("POST", srv.URL+"/api/agents/enroll", strings.NewReader(body))
		req.Header.Set("X-Enroll-Token", "enr_badbadbadbad")
		resp, err := bare.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("hostile enroll body: %d (want 400 or 401)", resp.StatusCode)
		}
	}
	agents, _, err := agentRegistryLoad()
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("hostile enroll storm must not create records: %+v", agents)
	}

	// No store corruption: the reads still answer valid JSON with the
	// seeded + accepted rows intact.
	st, sb = r3Do(t, c, srv, "GET", "/api/mesh/datacenters", "")
	if st != http.StatusOK {
		t.Fatalf("datacenters read after hostile storm: %d", st)
	}
	var list struct {
		Datacenters []map[string]any `json:"datacenters"`
	}
	if err := json.Unmarshal([]byte(sb), &list); err != nil || len(list.Datacenters) == 0 {
		t.Fatalf("datacenters list corrupt after hostile storm: %v", err)
	}
}
