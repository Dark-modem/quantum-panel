package panel

// guards_audit_summary_test.go — phase 16: the audit read API carries a
// short per-entry summary built from the recorded detail; unknown
// actions stay summary-less; the summary survives the redaction pass
// (the tunnel "key" field in the RAW detail is scrubbed, the summary is
// not).

import (
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestAuditSummaryPerEntry(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	now := time.Now().Unix()
	rows := []store.AuditEntry{
		{At: now, Actor: "admin", Action: "mesh.tunnel.create",
			Detail: `{"id":12,"key":"tl_main_1","engine":"matrix"}`},
		{At: now, Actor: "admin", Action: "mesh.topology.generate",
			Detail: `{"topology_id":1,"tunnels":12,"name":"main-mesh"}`},
		{At: now, Actor: "admin", Action: "mesh.agent.link",
			Detail: `{"agent":"agt_ab12cd34ef56","server_id":3}`},
		{At: now, Actor: "admin", Action: "mesh.desired.push",
			Detail: `{"agent":"agt_ab12cd34ef56","revision":"1762425000123456789","tunnels":6,"engines":2}`},
		{At: now, Actor: "admin", Action: "mesh.action.not.invented.yet",
			Detail: `{"x":1}`},
	}
	for _, r := range rows {
		if err := s.AddAudit(r); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := uiStoreHandle{s}.auditViewList("", "", "", 0, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	byAction := map[string]string{}
	for _, e := range entries {
		byAction[e.Action] = e.Summary
	}
	want := map[string]string{
		"mesh.tunnel.create":           "tunnel tl_main_1 created (engine matrix)",
		"mesh.topology.generate":       "generated topology main-mesh with 12 tunnels",
		"mesh.agent.link":              "agent agt_ab12cd34ef56 linked to server #3",
		"mesh.desired.push":            "desired state pushed to agent agt_ab12cd34ef56 (revision 1762425000123456789, 6 tunnels, 2 engines)",
		"mesh.action.not.invented.yet": "",
	}
	for action, exp := range want {
		if byAction[action] != exp {
			t.Fatalf("%s summary: got %q want %q", action, byAction[action], exp)
		}
	}

	// The redacted DETAIL must still scrub the secret-shaped field even
	// though the summary keeps the tunnel key.
	for _, e := range entries {
		if e.Action == "mesh.tunnel.create" {
			if e.Detail["key"] != "[redacted]" {
				t.Fatalf("detail key must stay redacted: %v", e.Detail)
			}
		}
	}
}
