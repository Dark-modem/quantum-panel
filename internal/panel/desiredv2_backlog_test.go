package panel

// desiredv2_backlog_test.go — BACKLOG-PANEL P18-1 (from
// PHASE17-AGENT-REPORT UNVERIFIED #1): a compile that ends up TUNNEL-LESS
// for a server that active wiring still names must say WHY in the push
// response's warnings (the mis-link / silently-unwired signal used to
// surface only as an absent delivery). Contract letter:
//   - the warning fires when the linked server is named by tunnels of an
//     ENABLED topology (disabled tunnel rows included — they are exactly
//     why the doc is tunnel-less);
//   - it does NOT fire for a server nothing names (fresh link), for
//     wiring retired with a disabled topology, or on the healthy path;
//   - the document shape is unchanged (the skip path still yields NO doc).

import (
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func seedBacklogWorld(t *testing.T, s *store.Store) (linkedServer, peer, topoID int64, agentID string) {
	t.Helper()
	dcIr, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "bl-iran"})
	if err != nil {
		t.Fatal(err)
	}
	dcFr, err := s.UpsertDatacenter(store.Datacenter{Kind: "foreign", Name: "bl-foreign"})
	if err != nil {
		t.Fatal(err)
	}
	linkedServer, err = s.UpsertServer(store.Server{Role: "iran", Name: "bl-ir", DatacenterID: dcIr})
	if err != nil {
		t.Fatal(err)
	}
	peer, err = s.UpsertServer(store.Server{Role: "foreign", Name: "bl-fr", DatacenterID: dcFr})
	if err != nil {
		t.Fatal(err)
	}
	topoID, err = s.UpsertTopology(store.Topology{Name: "bl-topo", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, linkedServer); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, peer); err != nil {
		t.Fatal(err)
	}
	agentID = "agt_bl_linked"
	if err := s.UpsertAgent(store.Agent{ID: agentID, ServerID: linkedServer, Name: "bl-ir",
		Role: "iran", Status: "online", Version: "v11", Arch: "amd64", OS: "linux",
		LastSeen: 1}); err != nil {
		t.Fatal(err)
	}
	return linkedServer, peer, topoID, agentID
}

func addTunnel(t *testing.T, s *store.Store, key string, topoID, local, peer int64, engine string, enabled bool) {
	t.Helper()
	if _, err := s.UpsertTunnel(store.Tunnel{TunnelKey: key, TopologyID: topoID,
		LocalServerID: local, RemoteServerID: peer, Engine: engine, EngineParams: "{}", Enabled: enabled}); err != nil {
		t.Fatal(err)
	}
}

func compileFor(t *testing.T, s *store.Store, agentID string) (doc bool, skip string, warnings []string) {
	t.Helper()
	ag, err := s.GetAgent(agentID)
	if err != nil {
		t.Fatal(err)
	}
	desired, skip, warnings, err := compileDesiredV2(s, ag)
	if err != nil {
		t.Fatal(err)
	}
	return desired != nil, skip, warnings
}

// Positive: enabled topology wires the linked server, but the tunnel rows
// are DISABLED → tunnel-less compile + the explicit mis-link warning.
func TestCompileDesiredV2TunnellessWarningDisabledTunnels(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	linked, peer, topoID, agentID := seedBacklogWorld(t, s)
	addTunnel(t, s, "bl-tun-a", topoID, linked, peer, "chisel", false)

	doc, skip, warnings := compileFor(t, s, agentID)
	if doc || skip == "" {
		t.Fatalf("tunnel-less compile expected: doc=%v skip=%q", doc, skip)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "still named by 1 tunnel") || !strings.Contains(joined, "bl-tun-a") {
		t.Fatalf("mis-link warning missing: %v", warnings)
	}
	if !strings.Contains(joined, "tunnel-less document") {
		t.Fatalf("warning must say the agent would receive a tunnel-less document: %v", warnings)
	}
}

// Positive 2: the tunnel rows are enabled but engine-less — same summary
// warning on top of the per-tunnel engine warnings.
func TestCompileDesiredV2TunnellessWarningEnginelessTunnels(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	linked, peer, topoID, agentID := seedBacklogWorld(t, s)
	addTunnel(t, s, "bl-tun-b", topoID, linked, peer, "", true)

	_, skip, warnings := compileFor(t, s, agentID)
	if skip == "" {
		t.Fatalf("engine-less tunnels must still compile tunnel-less")
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "no engine assigned") {
		t.Fatalf("per-tunnel engine warning missing: %v", warnings)
	}
	if !strings.Contains(joined, "still named by 1 tunnel") {
		t.Fatalf("mis-link summary warning missing: %v", warnings)
	}
}

// Negative A: a linked server NOTHING names (fresh link) skips silently —
// no warning (that is the normal pre-wiring state, not a mis-link).
func TestCompileDesiredV2TunnellessFreshServerNoWarning(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	_, _, _, agentID := seedBacklogWorld(t, s)
	// the seeded topology has no tunnels; the server is named by nothing
	_, skip, warnings := compileFor(t, s, agentID)
	if skip == "" || len(warnings) != 0 {
		t.Fatalf("fresh server must skip silently: skip=%q warnings=%v", skip, warnings)
	}
}

// Negative B: wiring retired with a DISABLED topology does NOT warn (the
// contract letter: tunnels of an ENABLED topology only).
func TestCompileDesiredV2TunnellessDisabledTopologyNoWarning(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	linked, peer, _, agentID := seedBacklogWorld(t, s)
	deadTopo, err := s.UpsertTopology(store.Topology{Name: "bl-dead", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	addTunnel(t, s, "bl-tun-dead", deadTopo, linked, peer, "chisel", true)
	_, skip, warnings := compileFor(t, s, agentID)
	if skip == "" || len(warnings) != 0 {
		t.Fatalf("disabled-topology wiring must not warn: skip=%q warnings=%v", skip, warnings)
	}
}

// Negative C: the healthy path compiles a document with NO warnings — the
// doc shape (tunnels present, self stamped) is exactly as before.
func TestCompileDesiredV2TunnellessHealthyNoWarning(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	linked, peer, topoID, agentID := seedBacklogWorld(t, s)
	addTunnel(t, s, "bl-tun-ok", topoID, linked, peer, "chisel", true)
	doc, skip, warnings := compileFor(t, s, agentID)
	if !doc || skip != "" || len(warnings) != 0 {
		t.Fatalf("healthy compile must yield a doc without warnings: doc=%v skip=%q warnings=%v", doc, skip, warnings)
	}
}
