package panel

// guards_test.go — TASK 3 (phase 13): destructive-op guard rails and
// the audit log API, over the real HTTP surface.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// TestGuardServerDeleteRequiresConfirmation: deleting a server WITH
// dependants refuses without the token, the dependency report lists
// them and mints it, and the token completes the delete.
func TestGuardServerDeleteRequiresConfirmation(t *testing.T) {
	srv := uiServer(t)
	c := meshSession(t)
	s := meshStoreMust(t)
	dc, _ := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "g-tehran"})
	sv, _ := s.UpsertServer(store.Server{Role: "iran", Name: "g-ir", DatacenterID: dc})
	topo, _ := s.UpsertTopology(store.Topology{Name: "g-topo", Enabled: true})
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "g-t1", TopologyID: topo,
		LocalServerID: sv, RemoteServerID: sv, Engine: "chisel", Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// 1. DELETE without a token → structured refusal with the count.
	resp, body := meshPutRaw(t, c, srv, "DELETE", "/api/mesh/servers", map[string]any{})
	_ = resp
	_ = body
	del := func(id int64) (int, string) {
		req, _ := http.NewRequest("DELETE", srv.URL+"/api/mesh/servers/"+itoa64(id), nil)
		httpResp, err := c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer httpResp.Body.Close()
		var sb strings.Builder
		buf := make([]byte, 8192)
		for {
			n, rerr := httpResp.Body.Read(buf)
			sb.Write(buf[:n])
			if rerr != nil {
				break
			}
		}
		return httpResp.StatusCode, sb.String()
	}
	code, body := del(sv)
	if code != 409 || !strings.Contains(body, "E-CONFIRM-REQUIRED") {
		t.Fatalf("delete without confirm: %d %s", code, body)
	}
	if !strings.Contains(body, "g-t1") {
		t.Fatalf("the refusal must name the dependent tunnel: %s", body)
	}

	// 2. The dependency report lists dependants + mints a token.
	dresp, err := c.Get(srv.URL + "/api/ui/servers/" + itoa64(sv) + "/dependencies")
	if err != nil {
		t.Fatal(err)
	}
	defer dresp.Body.Close()
	var rep struct {
		HasBlock bool   `json:"has_dependants"`
		Token    string `json:"confirm_token"`
		Depend   []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"dependants"`
	}
	if err := json.NewDecoder(dresp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if !rep.HasBlock || rep.Token == "" {
		t.Fatalf("dependency report: %+v", rep)
	}
	kinds := map[string]bool{}
	for _, d := range rep.Depend {
		kinds[d.Kind] = true
	}
	if !kinds["tunnel"] {
		t.Fatalf("the report must list the tunnel: %+v", rep.Depend)
	}

	// 3. The token is BOUND to (kind, id): delete the tunnel first (the
	// FK would block the server delete), then the server — and a replay
	// of the SAME token against ANOTHER server refuses.
	if err := deleteTunnelByKey(t, s, "g-t1"); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("DELETE", srv.URL+"/api/mesh/servers/"+itoa64(sv), nil)
	req.Header.Set("X-Confirm-Token", rep.Token)
	r2, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != 200 {
		t.Fatalf("delete with the token: %d %s", r2.StatusCode, b2)
	}
	// A second server with its own tunnel; the replayed token (minted
	// for the FIRST server's id) must be refused for it.
	sv2, _ := s.UpsertServer(store.Server{Role: "iran", Name: "g-ir2", DatacenterID: dc})
	topo2, _ := s.UpsertTopology(store.Topology{Name: "g-topo2", Enabled: true})
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "g-t2", TopologyID: topo2,
		LocalServerID: sv2, RemoteServerID: sv2, Engine: "chisel", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	req3, _ := http.NewRequest("DELETE", srv.URL+"/api/mesh/servers/"+itoa64(sv2), nil)
	req3.Header.Set("X-Confirm-Token", rep.Token)
	r3, err := c.Do(req3)
	if err != nil {
		t.Fatal(err)
	}
	r3.Body.Close()
	if r3.StatusCode == 200 {
		t.Fatal("a token minted for another id must be refused")
	}
}

func itoa64(v int64) string { return strconv.FormatInt(v, 10) }

// TestAuditLogAPIFilterAndRedaction: actions land in the log with the
// actor; the API filters and redacts secret-shaped detail values.
func TestAuditLogAPIFilterAndRedaction(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	now := time.Now().Unix()
	_ = s.AddAudit(store.AuditEntry{At: now, Actor: "op-a", Action: "mesh.tunnel.create",
		Detail: `{"key":"SUPER-SECRET-KEY-123","tunnel":"tl-a"}`})
	_ = s.AddAudit(store.AuditEntry{At: now + 1, Actor: "op-b", Action: "mesh.server.delete",
		Detail: `{"id":7,"password":"hunter2"}`})

	c := meshSession(t)
	// Filter by actor.
	resp, err := c.Get(srv.URL + "/api/ui/audit?actor=op-a")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Entries []auditEntryView `json:"entries"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Entries) != 1 || out.Entries[0].Actor != "op-a" {
		t.Fatalf("actor filter: %+v", out.Entries)
	}
	// Redaction: no secret values anywhere.
	resp2, err := c.Get(srv.URL + "/api/ui/audit")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var all struct {
		Entries []auditEntryView `json:"entries"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&all)
	raw, _ := json.Marshal(all)
	for _, secret := range []string{"SUPER-SECRET-KEY-123", "hunter2"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q leaked into the audit API", secret)
		}
	}
	// CSV export.
	resp3, err := c.Get(srv.URL + "/api/ui/audit?format=csv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp3.Body.Close()
	buf := make([]byte, 4096)
	n, _ := resp3.Body.Read(buf)
	csvText := string(buf[:n])
	if !strings.HasPrefix(csvText, "at,actor,ip,action,detail") {
		t.Fatalf("csv header wrong: %q", csvText)
	}
	if strings.Contains(csvText, "hunter2") {
		t.Fatal("csv leaks secrets")
	}
}

// deleteTunnelByKey deletes one tunnel by key (test helper).
func deleteTunnelByKey(t *testing.T, s *store.Store, key string) error {
	t.Helper()
	tun, err := s.GetTunnelByKey(key)
	if err != nil {
		return err
	}
	return s.DeleteTunnel(tun.ID)
}
