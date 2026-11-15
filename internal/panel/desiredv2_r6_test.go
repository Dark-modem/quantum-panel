package panel

// desiredv2_r6_test.go — phase 17 R6 (PROTOCOL-REQUESTS.md): every
// compiled DesiredV2 carries the receiving agent's SELF — the linked
// mesh server name (the enrollment name) + the server role — spelled
// EXACTLY like the TunnelSpec LocalServer/RemoteServer strings, so the
// agent locates itself by NAME (FRP frps/frpc resolution). Pre-R6
// agents ignore the unknown JSON field (compat asserted on the wire
// bytes too).

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestDesiredV2SelfStamp(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	_, _, agIr, agFr := uiSeedWorld(t, s)

	for _, tc := range []struct {
		agentID string
		name    string
		role    string
	}{
		{agIr, "teh-1", "iran"},
		{agFr, "fra-1", "foreign"},
	} {
		ag, err := s.GetAgent(tc.agentID)
		if err != nil {
			t.Fatal(err)
		}
		desired, skip, warnings, err := compileDesiredV2(s, ag)
		if err != nil || skip != "" {
			t.Fatalf("compile %s: err=%v skip=%q", tc.agentID, err, skip)
		}
		if len(warnings) != 0 {
			t.Fatalf("unexpected warnings: %v", warnings)
		}
		if desired.Self == nil || desired.Self.Name != tc.name || desired.Self.Role != tc.role {
			t.Fatalf("self stamp wrong for %s: %+v (want name=%s role=%s)", tc.agentID, desired.Self, tc.name, tc.role)
		}
		if !desired.IsSelfServer(tc.name) {
			t.Fatalf("IsSelfServer(%q) must hold for %s", tc.name, tc.agentID)
		}
		if desired.IsSelfServer("some-other-server") {
			t.Fatal("IsSelfServer must not match a foreign name")
		}
		// Alignment: every tunnel names THIS agent's server on one end —
		// the property the agent's side-resolution relies on.
		for _, sp := range desired.Tunnels {
			if sp.LocalServer != tc.name && sp.RemoteServer != tc.name {
				t.Fatalf("tunnel %s: agent %s appears on neither end (%q/%q)", sp.ID, tc.agentID, sp.LocalServer, sp.RemoteServer)
			}
		}
		// The wire bytes carry the additive "self" field; a pre-R6 agent
		// decoding this document keeps every known field and drops self.
		b, err := json.Marshal(desired)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"self":{"name":"`+tc.name+`","role":"`) {
			t.Fatalf("wire doc missing the self field: %s", b)
		}
		var legacy struct {
			Engines   []map[string]any `json:"engines,omitempty"`
			Tunnels   []map[string]any `json:"tunnels,omitempty"`
			Balancing map[string]any   `json:"balancing,omitempty"`
		}
		if err := json.Unmarshal(b, &legacy); err != nil {
			t.Fatal(err)
		}
		// Revision is NOT stamped by the compiler (docWithRevision fences
		// it at push time) — engines/tunnels/balancing must survive a
		// legacy decode that has no self field.
		if len(legacy.Engines) == 0 || len(legacy.Tunnels) == 0 || legacy.Balancing == nil {
			t.Fatalf("legacy decode lost known fields: %+v", legacy)
		}
	}
}

func TestDesiredV2SelfUnlinkedAgentSkipped(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	// An enrolled but UNLINKED agent: unchanged pre-R6 behavior — skip,
	// no document at all (and therefore no self to stamp).
	unlinked := "agt_unlinked_r6"
	if err := s.UpsertAgent(store.Agent{ID: unlinked, Name: "nolink",
		Role: "iran", Status: "online", Version: "v11", Arch: "amd64", OS: "linux",
		LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	ag, err := s.GetAgent(unlinked)
	if err != nil {
		t.Fatal(err)
	}
	desired, skip, warnings, err := compileDesiredV2(s, ag)
	if err != nil {
		t.Fatal(err)
	}
	if desired != nil || skip == "" || len(warnings) != 0 {
		t.Fatalf("unlinked agent must skip cleanly: doc=%v skip=%q warnings=%v", desired, skip, warnings)
	}
}
