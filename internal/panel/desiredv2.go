package panel

// desiredv2.go — the desired-state compiler. Per agent it builds a
// protocol.DesiredV2 (engines, tunnels, balancing policy from the weight
// policies, hardening ref) from the store, fences it with a fresh
// revision, and delivers it over the agent WebSocket (v2 sessions only;
// v1 agents keep the frozen v1 matrix flow).
//
// Minimal revisions: the compiled document is compared against the last
// ACKED document (store applied_v2). Identical content → NO new revision
// and no push, so a weight edit that changes nothing for one agent never
// wakes it, while a real edit produces exactly one new revision per agent.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	"github.com/Dark-modem/quantum-protocol"
)

// meshDefaultHealth / meshDefaultFailover are the balancing defaults agents
// enforce locally when a topology sets none (the panel only sets POLICY —
// enforcement, health checks and failover happen on the agent, even when
// this panel is dead).
func meshDefaultHealth() protocol.HealthCheckParams {
	return protocol.HealthCheckParams{IntervalS: 10, TimeoutMS: 2000, Probe: "engine", ConsecutiveDown: 3, ConsecutiveUp: 2}
}

func meshDefaultFailover() protocol.FailoverRules {
	return protocol.FailoverRules{SwitchBackS: 300, MaxLossPct: 20}
}

// compileDesiredV2 builds the DesiredV2 for one agent + its warnings.
// skip (non-nil error nil, skip non-empty) means "nothing to do for this
// agent" — not linked, no tunnels, nothing changed.
// S4: the phases are compileDesiredTunnelSpecs (linked server + enabled
// tunnel specs), compileDesiredEngines (artifact stamping + engine flags),
// compileDesiredBalancing (weights/health/failover + scoped forwards) and
// stampDesiredSelf (R6 self stamp + alignment warnings) — bodies verbatim,
// every early return's exact (nil/skip/warnings/err) shape preserved.
func compileDesiredV2(s *store.Store, ag store.Agent) (desired *protocol.DesiredV2, skip string, warnings []string, err error) {
	server, specs, engineUsed, topoSeen, warnings, skip, err := compileDesiredTunnelSpecs(s, ag)
	if err != nil || skip != "" {
		return nil, skip, warnings, err
	}
	engines, err := compileDesiredEngines(s, ag, specs, engineUsed)
	if err != nil {
		return nil, "", nil, err
	}
	bal, forwards, targetGroups, warnings := compileDesiredBalancing(s, server, engineUsed, topoSeen, warnings)
	desired = &protocol.DesiredV2{
		Engines: engines, Tunnels: specs, Balancing: &bal,
		TargetGroups: targetGroups, Forwards: forwards,
	}
	warnings = stampDesiredSelf(server, specs, desired, warnings)
	return desired, "", warnings, nil
}

// compileDesiredTunnelSpecs resolves the agent's linked server and compiles
// the enabled-tunnel specs (S4 extract, body verbatim): per-tunnel topology
// checks feed warnings, the engine/topology usage sets feed the later
// phases, and the tunnel-less mis-link warning (BACKLOG P18-1) rides the
// skip return. Hard errors return nil warnings exactly like the inline
// original's early returns.
func compileDesiredTunnelSpecs(s *store.Store, ag store.Agent) (server store.Server, specs []protocol.TunnelSpec, engineUsed map[string]bool, topoSeen map[int64]bool, warnings []string, skip string, err error) {
	if ag.ServerID == 0 {
		return server, nil, nil, nil, nil, "agent not linked to a configured server (link it first)", nil
	}
	server, err = s.GetServer(ag.ServerID)
	if err != nil {
		return store.Server{}, nil, nil, nil, nil, "", fmt.Errorf("linked server: %v", err)
	}
	tunnels, err := s.TunnelsForServer(server.ID)
	if err != nil {
		return store.Server{}, nil, nil, nil, nil, "", err
	}
	engineUsed = map[string]bool{}
	topoSeen = map[int64]bool{}
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}
		topo, err := s.GetTopology(t.TopologyID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("tunnel %s: topology %d unreadable (%v), skipped", t.TunnelKey, t.TopologyID, err))
			continue
		}
		if !topo.Enabled {
			continue
		}
		if t.Engine == "" {
			warnings = append(warnings, fmt.Sprintf("tunnel %s has no engine assigned, skipped", t.TunnelKey))
			continue
		}
		local, remote := server.Name, server.Name
		if t.LocalServerID != server.ID {
			if ls, err := s.GetServer(t.LocalServerID); err == nil {
				local = ls.Name
			}
		}
		if t.RemoteServerID != server.ID {
			if rs, err := s.GetServer(t.RemoteServerID); err == nil {
				remote = rs.Name
			}
		}
		specs = append(specs, protocol.TunnelSpec{
			ID:           t.TunnelKey,
			TopologyID:   topo.Name,
			LocalServer:  local,
			RemoteServer: remote,
			Engine:       t.Engine,
			EngineParams: json.RawMessage(t.EngineParams),
			Enabled:      true,
		})
		engineUsed[t.Engine] = true
		topoSeen[topo.ID] = true
	}
	if len(specs) == 0 {
		// BACKLOG-PANEL (P18-1, from PHASE17-AGENT-REPORT UNVERIFIED #1):
		// a tunnel-less document for a server that active wiring still
		// names is the mis-link / silently-unwired signal — it used to
		// surface only as an absent delivery. Say exactly WHY the doc is
		// empty, per agent, in the push response's warnings.
		if named, total := namedByEnabledTopologies(s, server.ID); total > 0 {
			more := ""
			if total > len(named) {
				more = " …"
			}
			warnings = append(warnings, fmt.Sprintf(
				"server %q compiles NO tunnels but is still named by %d tunnel(s) of enabled topologies (%s%s) — the agent would receive a tunnel-less document; check disabled tunnels, missing engines, or a mis-link (R6)",
				server.Name, total, strings.Join(named, ", "), more))
		}
		return server, nil, engineUsed, topoSeen, warnings, "no enabled tunnels reference this server yet", nil
	}
	return server, specs, engineUsed, topoSeen, warnings, "", nil
}

// compileDesiredEngines stamps the artifact engines' tunnel params (phase
// 31 §10.4) and builds the desired engine list (S4 extract, body verbatim).
// Hard errors return exactly like the inline original — the caller's
// accumulated warnings are discarded.
func compileDesiredEngines(s *store.Store, ag store.Agent, specs []protocol.TunnelSpec, engineUsed map[string]bool) (engines []protocol.EngineSpec, err error) {
	// Phase 31 (contract §2.6, UI-API-REQUESTS §10.4): stamp the artifact
	// engines' tunnel params with the LATEST artifact version for this
	// agent's arch. A newly uploaded artifact then changes the desired
	// document → the push reaches the agent → the tunnels restart (artifact
	// engines never live-reload) → the restart re-resolves the signed
	// manifest and downloads the new version. Stored engine_params are
	// never modified; the stamp is derived per compile.
	stampArtifactVersions(s, specs, agentArchForArtifacts(ag))

	// Engines (Phase 11): a tunnel's engine is always enabled; the
	// per-agent DESIRED flags (the enable/disable endpoints) also enable
	// engines with no tunnels yet — matrix is desired-on by default.
	// Catalog engines nobody desires stay disabled (idle engines consume
	// zero resources).
	engEnabled := map[string]bool{}
	for e := range engineUsed {
		engEnabled[e] = true
	}
	if rows, derr := s.AgentEngines(ag.ID); derr == nil {
		for _, row := range rows {
			if row.Desired {
				engEnabled[row.Engine] = true
			} else if !engineUsed[row.Engine] {
				engEnabled[row.Engine] = false // explicitly disabled by the operator
			}
		}
	}
	if on, derr := s.AgentEngineDesired(ag.ID, "matrix"); derr == nil && on {
		engEnabled["matrix"] = true
	}
	for e, on := range engEnabled {
		engines = append(engines, protocol.EngineSpec{ID: e, Enabled: on})
	}
	v2state, err := s.AgentV2(ag.ID)
	if err != nil {
		return nil, fmt.Errorf("agent v2 state: %v", err)
	}
	if strings.TrimSpace(v2state.EngineCatalog) != "" {
		var catalog []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(v2state.EngineCatalog), &catalog); err != nil {
			return nil, fmt.Errorf("engine catalog: %v", err)
		}
		for _, c := range catalog {
			if c.ID != "" && !engEnabled[c.ID] {
				engines = append(engines, protocol.EngineSpec{ID: c.ID, Enabled: false})
			}
		}
	}
	sort.Slice(engines, func(i, j int) bool { return engines[i].ID < engines[j].ID })
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })
	return engines, nil
}

// compileDesiredBalancing resolves the per-tunnel weight rules with the
// store's specificity chain scoped to the topologies this agent tunnels
// for, the topology health/failover overrides and the topology-scoped
// forward rules with their target groups (S4 extract, body verbatim).
// Returns the policy, the sorted rules/groups and the possibly-extended
// warnings slice.
func compileDesiredBalancing(s *store.Store, server store.Server, engineUsed map[string]bool, topoSeen map[int64]bool, warnings []string) (bal protocol.BalancingPolicy, forwards []protocol.ForwardRule, targetGroups []protocol.TargetGroup, _ []string) {
	// Balancing: per-tunnel weight rules resolved with the store's
	// specificity chain, scoped to the topologies this agent tunnels for.
	bal = protocol.BalancingPolicy{HealthCheck: meshDefaultHealth(), Failover: meshDefaultFailover()}
	var (
		groupSeen = map[string]bool{}
		groupDefs = map[string]protocol.TargetGroup{}
	)
	for topoID := range topoSeen {
		tp, err := s.GetTopology(topoID)
		if err != nil {
			continue
		}
		_ = tp
		topoTunnels, err := s.ListTunnels(topoID)
		if err != nil {
			continue
		}
		for _, t := range topoTunnels {
			if !t.Enabled || !engineUsed[t.Engine] || (t.LocalServerID != server.ID && t.RemoteServerID != server.ID) {
				continue
			}
			w, err := s.WeightFor(topoID, t.LocalServerID, t.RemoteServerID, t.ID)
			if err != nil {
				w = 1
			}
			bal.Weights = append(bal.Weights, protocol.WeightRule{
				TopologyID:   topoNameCache(s, topoID),
				TunnelID:     t.TunnelKey,
				LocalServer:  serverNameCache(s, t.LocalServerID),
				RemoteServer: serverNameCache(s, t.RemoteServerID),
				Weight:       w,
			})
		}
		if tp.HealthCheck != "" {
			var hc protocol.HealthCheckParams
			if json.Unmarshal([]byte(tp.HealthCheck), &hc) == nil {
				bal.HealthCheck = hc
			}
		}
		if tp.Failover != "" {
			var fo protocol.FailoverRules
			if json.Unmarshal([]byte(tp.Failover), &fo) == nil {
				bal.Failover = fo
			}
		}
		// Forwards of this topology scoped to this agent's server
		// (PROTOCOL-REQUESTS R1 delivery): rules targeting another
		// server's listen side are skipped. A rule with a RemoteServerID
		// balances across that one server's tunnels; without one it
		// spans the topology's whole foreign pool.
		topoForwards, err := s.ListForwardRules(topoID)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("topology %s: forward rules unreadable (%v)", tp.Name, err))
			continue
		}
		for _, f := range topoForwards {
			if !f.Enabled || f.LocalServerID != server.ID {
				continue
			}
			fw, grp, ferr := compileForwardRule(s, topoID, f)
			if ferr != nil {
				warnings = append(warnings, fmt.Sprintf("forward %d: %v", f.ID, ferr))
				continue
			}
			forwards = append(forwards, *fw)
			if grp != nil {
				groupSeen[grp.ID] = true
				groupDefs[grp.ID] = *grp
			}
		}
	}

	// Target groups referenced by the compiled rules (the agent resolves
	// a group to its running tunnels via topology + optional remote
	// server — never a tunnel list, so any N/M/K and engine mix works).
	for gid := range groupSeen {
		if g, ok := groupDefs[gid]; ok {
			targetGroups = append(targetGroups, g)
		}
	}
	sort.Slice(targetGroups, func(i, j int) bool { return targetGroups[i].ID < targetGroups[j].ID })
	sort.Slice(forwards, func(i, j int) bool { return forwards[i].ID < forwards[j].ID })
	return bal, forwards, targetGroups, warnings
}

// stampDesiredSelf stamps the receiving agent's SELF (R6) and validates
// spec alignment against it, extending warnings (S4 extract, body verbatim).
func stampDesiredSelf(server store.Server, specs []protocol.TunnelSpec, desired *protocol.DesiredV2, warnings []string) []string {
	// R6 (PROTOCOL-REQUESTS.md): stamp the receiving agent's SELF — the
	// mesh server name it is linked to (the enrollment name), spelled
	// exactly like the LocalServer/RemoteServer strings above — so the
	// agent locates itself by NAME (FRP frps/frpc role resolution).
	// Alignment validation: every spec must reference this server name
	// on one end, else the agent cannot find itself in the document.
	if strings.TrimSpace(server.Name) == "" {
		warnings = append(warnings, fmt.Sprintf("server %d has an empty name: self identifier NOT stamped (R6)", server.ID))
	} else {
		desired.Self = &protocol.SelfInfo{Name: server.Name, Role: server.Role}
		for _, sp := range specs {
			if sp.LocalServer != server.Name && sp.RemoteServer != server.Name {
				warnings = append(warnings, fmt.Sprintf("tunnel %s references %q/%q but self is %q (R6 alignment)", sp.ID, sp.LocalServer, sp.RemoteServer, server.Name))
			}
		}
	}
	return warnings
}

// agentArchForArtifacts resolves the arch an agent's artifacts resolve
// against (the enable handler's same default).
func agentArchForArtifacts(ag store.Agent) string {
	if strings.TrimSpace(ag.Arch) == "" {
		return "amd64"
	}
	return ag.Arch
}

// latestArtifactVersionFor picks the artifact version the agent's own
// manifest resolution would download: highest version string for the
// engine+arch, ties resolved to the newest upload (ListEngineArtifacts
// is uploaded_at DESC — the same order the R5 manifest serves and the
// agent's strict `>` walk keeps). "" = no artifact for that arch.
func latestArtifactVersionFor(s *store.Store, engine, arch string) string {
	arts, err := s.ListEngineArtifacts()
	if err != nil {
		return ""
	}
	return latestArtifactVersionIn(arts, engine, arch)
}

// latestArtifactVersionIn delegates the artifact-pin pick to the shared
// protocol.PickArtifactVersion (REFACTOR-S6/A6 D5: the logic was mirrored
// panel-side; the protocol now owns it — same plain-string comparison
// with the pinned "1.9">"1.10" lexicographic quirk, first row wins ties,
// empty versions never win).
func latestArtifactVersionIn(arts []store.EngineArtifact, engine, arch string) string {
	rows := make([]protocol.ArtifactVersionRow, 0, len(arts))
	for _, a := range arts {
		rows = append(rows, protocol.ArtifactVersionRow{Engine: a.Engine, Goarch: a.Goarch, Version: a.Version})
	}
	return protocol.PickArtifactVersion(rows, engine, arch)
}

// stampArtifactVersions is the phase-31 re-sync-on-enable lever (§2.6):
// every artifact-engine tunnel spec whose operator params are a JSON
// object gains `_artifact_version` = the latest artifact version for the
// agent's arch. Params that are absent, invalid JSON, or not objects stay
// untouched (the operator's params are opaque and never rewritten in the
// store). The stamp is what makes a newly uploaded artifact change the
// desired document — the minimal-revision rule then delivers the push and
// the agent's restart re-resolves the manifest.
func stampArtifactVersions(s *store.Store, specs []protocol.TunnelSpec, arch string) {
	arts, err := s.ListEngineArtifacts()
	if err != nil || len(arts) == 0 {
		return
	}
	latest := map[string]string{}
	for _, a := range arts {
		if a.Goarch != arch || !uiArtifactEngines[a.Engine] {
			continue
		}
		if v := latestArtifactVersionIn(arts, a.Engine, arch); v != "" {
			latest[a.Engine] = v
		}
	}
	if len(latest) == 0 {
		return
	}
	for i := range specs {
		version, ok := latest[specs[i].Engine]
		if !ok || len(specs[i].EngineParams) == 0 {
			continue
		}
		var fields map[string]any
		if json.Unmarshal(specs[i].EngineParams, &fields) != nil || fields == nil {
			continue // arrays/scalars stay untouched: only object params get the stamp
		}
		if s2, _ := fields["_artifact_version"].(string); s2 == version {
			continue // already stamped at this version — keep the bytes stable
		}
		fields["_artifact_version"] = version
		if b, merr := json.Marshal(fields); merr == nil {
			specs[i].EngineParams = json.RawMessage(b)
		}
	}
}

// compileForwardRule maps one store forward rule to its wire form and
// the target group it needs (balancing group for the topology's pool,
// scoped to one remote server when the rule names one). group == nil for
// explicit-target rules.
func compileForwardRule(s *store.Store, topoID int64, f store.ForwardRule) (*protocol.ForwardRule, *protocol.TargetGroup, error) {
	topoName := topoNameCache(s, topoID)
	wire := &protocol.ForwardRule{
		ID:         fmt.Sprintf("fw-%d", f.ID),
		ListenPort: f.ListenPort,
		Proto:      f.Proto,
		Enabled:    &f.Enabled,
		TopologyID: topoName,
	}
	if !protocol.ValidForwardProto(f.Proto) {
		return nil, nil, fmt.Errorf("invalid proto %q", f.Proto)
	}
	if f.Target != "" {
		wire.Target = f.Target // explicit single target (panel-chosen)
		return wire, nil, nil
	}
	var gid, remoteName string
	if f.RemoteServerID != nil && *f.RemoteServerID != 0 {
		remoteName = serverNameCache(s, *f.RemoteServerID)
		gid = fmt.Sprintf("grp-%s-%s", topoName, remoteName)
	} else {
		gid = fmt.Sprintf("grp-%s-pool", topoName)
	}
	wire.TargetGroup = gid
	// TargetPort carries the rule's explicit foreign-side port when the
	// operator set one; 0 keeps the historical default (serve the same
	// port as the listen side).
	wire.TargetPort = f.TargetPort
	if wire.TargetPort <= 0 {
		wire.TargetPort = f.ListenPort
	}
	return wire, &protocol.TargetGroup{ID: gid, TopologyID: topoName, RemoteServer: remoteName}, nil
}

// namedByEnabledTopologies lists tunnel keys that still name serverID on
// one of their ends AND belong to an ENABLED topology (BACKLOG-PANEL
// P18-1): a server wired into active topologies whose agent compiles a
// tunnel-less document is the mis-link / silently-unwired signal. Disabled
// tunnels of an enabled topology count (a disabled row is exactly why the
// doc ends up tunnel-less); tunnels of disabled topologies do not (that
// wiring was deliberately retired). Returns up to maxNamedWarningKeys keys
// for the message plus the TOTAL count.
func namedByEnabledTopologies(s *store.Store, serverID int64) (keys []string, total int) {
	const maxNamedWarningKeys = 5
	rows, err := s.TunnelsForServer(serverID)
	if err != nil {
		return nil, 0
	}
	for _, t := range rows {
		topo, err := s.GetTopology(t.TopologyID)
		if err != nil || !topo.Enabled {
			continue
		}
		total++
		if len(keys) < maxNamedWarningKeys {
			keys = append(keys, t.TunnelKey)
		}
	}
	return keys, total
}

// name caches keep the compiler off the DB for repeated lookups inside one
// compile (small topology shapes; correctness does not depend on them).
var (
	nameCacheMu sync.Mutex
	nameCacheS  *store.Store
	nameCacheSv = map[int64]string{}
	nameCacheTp = map[int64]string{}
)

func serverNameCache(s *store.Store, id int64) string {
	nameCacheMu.Lock()
	defer nameCacheMu.Unlock()
	if nameCacheS != s {
		nameCacheS = s
		nameCacheSv = map[int64]string{}
		nameCacheTp = map[int64]string{}
	}
	if n, ok := nameCacheSv[id]; ok {
		return n
	}
	n := fmt.Sprintf("server-%d", id)
	if sv, err := s.GetServer(id); err == nil {
		n = sv.Name
	}
	nameCacheSv[id] = n
	return n
}

func topoNameCache(s *store.Store, id int64) string {
	nameCacheMu.Lock()
	defer nameCacheMu.Unlock()
	if nameCacheS != s {
		nameCacheS = s
		nameCacheSv = map[int64]string{}
		nameCacheTp = map[int64]string{}
	}
	if n, ok := nameCacheTp[id]; ok {
		return n
	}
	n := fmt.Sprintf("topology-%d", id)
	if tp, err := s.GetTopology(id); err == nil {
		n = tp.Name
	}
	nameCacheTp[id] = n
	return n
}

// compileAndPushAgent compiles for one agent and, when the document
// actually changed, delivers it with a fresh revision. It answers with the
// delivery outcome so the API can show skip/warn reasons.
func compileAndPushAgent(s *store.Store, agentID string) (map[string]any, error) {
	ag, err := s.GetAgent(agentID)
	if err != nil {
		return nil, err
	}
	v2state, err := s.AgentV2(agentID)
	if err != nil {
		return nil, err
	}
	// The live session's negotiated version wins while a session exists
	// (the store value updates at hello time but a push can race it).
	if v := agentHubSessionVersion(agentID); v > 0 {
		v2state.ProtoVersion = v
	}
	// Phase 13 TASK 1: an agent that has NEVER connected (last_seen == 0,
	// proto_version still at its default) used to be refused here, which
	// silently dropped offline pushes for fresh enrollments (the
	// compat-matrix defect). Now: COMPILE + QUEUE — the pending document
	// is delivered at hello if the agent negotiates v2, re-delivered
	// after the first post-hello heartbeat if the ack was lost, and
	// cleared ONLY on the ack (store.AckDesiredV2). A KNOWN v1 agent
	// (connected with proto_version 1) still cannot take DesiredV2.
	neverConnected := ag.LastSeen == 0 && agentHubSessionVersion(agentID) == 0
	if v2state.ProtoVersion == 1 && !neverConnected {
		return map[string]any{
			"agent_id": agentID, "pushed": false,
			"skip": "agent speaks protocol v1; DesiredV2 needs a v2 session (v1 agents keep the frozen matrix flow)",
		}, nil
	}
	desired, skip, warnings, err := compileDesiredV2(s, ag)
	if err != nil {
		return nil, err
	}
	if neverConnected {
		// Never connected: queue only (delivery happens at hello). The
		// revision is assigned NOW — meshPendingDesiredV2 refuses
		// revision-less documents, and the ack must match it.
		desired.Revision = newMeshRevision()
		doc, merr := json.Marshal(desired)
		if merr != nil {
			return nil, merr
		}
		if err := s.SetPendingDesiredV2(agentID, string(doc)); err != nil {
			return nil, err
		}
		meshAudit(meshActor(nil), "mesh.desired.queued",
			fmt.Sprintf(`{"agent":%q,"revision":%q,"tunnels":%d,"engines":%d}`,
				agentID, desired.Revision, len(desired.Tunnels), len(desired.Engines)))
		return map[string]any{"agent_id": agentID, "pushed": false, "queued": true,
			"revision": desired.Revision,
			"skip":     "agent has not connected yet; desired queued for its first session",
			"warnings": warnings}, nil
	}
	if skip != "" {
		return map[string]any{"agent_id": agentID, "pushed": false, "skip": skip, "warnings": warnings}, nil
	}
	doc, err := json.Marshal(desired)
	if err != nil {
		return nil, err
	}
	// Minimal-revision rule: identical content (modulo revision) → no push.
	if v2state.AppliedV2 != "" && desiredEqual(v2state.AppliedV2, string(doc)) {
		return map[string]any{"agent_id": agentID, "pushed": false,
			"skip": "desired state already applied (no change since last ack)", "warnings": warnings}, nil
	}
	desired.Revision = newMeshRevision()
	// Offline agents get the document QUEUED for the next connect
	// (persisted pending, delivered by the hub at hello) and an honest
	// pushed=false — the API must not claim a delivery that did not
	// happen (PROTOCOL-REQUESTS R1 flow + E2E drop contract).
	if err := agentHubDeliverDesiredV2(agentID, *desired); err != nil {
		if errors.Is(err, errAgentSessionDead) {
			return map[string]any{"agent_id": agentID, "pushed": false,
				"skip": "agent offline; desired queued for the next connect", "revision": desired.Revision,
				"warnings": warnings}, nil
		}
		return nil, err
	}
	meshAudit(meshActor(nil), "mesh.desired.push",
		fmt.Sprintf(`{"agent":%q,"revision":%q,"tunnels":%d,"engines":%d}`,
			agentID, desired.Revision, len(desired.Tunnels), len(desired.Engines)))
	return map[string]any{"agent_id": agentID, "pushed": true,
		"revision": desired.Revision, "warnings": warnings}, nil
}

// desiredEqual compares two DesiredV2 JSON documents ignoring their
// revision fields (the minimal-revision diff).
func desiredEqual(appliedJSON, candidateJSON string) bool {
	var a, b protocol.DesiredV2
	if json.Unmarshal([]byte(appliedJSON), &a) != nil || json.Unmarshal([]byte(candidateJSON), &b) != nil {
		return false
	}
	a.Revision, b.Revision = "", ""
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

// newMeshRevision is a strictly increasing decimal-string revision
// (protocol.RevisionNewer semantics): unix nano is unique per push and
// always newer than every earlier value on the same clock.
func newMeshRevision() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

// handleMeshAgentPush is the API entry that forces a compile+push for one
// agent (the CRUD handlers do NOT auto-push; pushing is explicit so the
// operator can batch edits first).
func handleMeshAgentPush(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	agentID := r.PathValue("id")
	if !validAgentID(agentID) {
		writeAPIError(w, r, "E-IN-06", "bad agent id")
		return
	}
	// Phase 11 requirement 5: pushing must not ship tunnels whose engine
	// is not enabled (or has no artifact) on one of the two ends.
	if !uiValidateAgentTunnels(w, r, s, agentID) {
		return
	}
	res, err := compileAndPushAgent(s, agentID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			meshErr(w, r, err)
			return
		}
		writeAPIError(w, r, "E-AGENT-01", err.Error())
		return
	}
	writeJSON(w, res)
}

// ---- hub-side v2 helpers ------------------------------------------------

// recordSessionV2 persists the negotiated protocol version to the store
// (best effort: the session runs even if the store is unavailable).
func (c *agentConn) recordSessionV2() {
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		return
	}
	v2, err := s.AgentV2(c.id)
	if err != nil {
		return // not in the store (legacy registry only): nothing to record
	}
	if v2.ProtoVersion == c.protoV {
		return
	}
	_ = s.SetAgentV2Session(c.id, c.protoV, v2.EngineCatalog)
}

// recordEngineCatalog persists the engine catalog the agent reported in
// hello (PROTOCOL-REQUESTS R3): rich entries when present, else the id
// list. Best effort like recordSessionV2. Round-4 input security: the
// hello path had NO caps (bounded only by the 256 KiB WS envelope) while
// the operator-side catalog route enforces ≤256 entries — apply the same
// policy plus per-field string bounds.
const (
	maxHelloCatalogEntries = 256
	maxHelloCatalogIDLen   = 128
	maxHelloCatalogVerLen  = 128
	maxHelloCatalogSchema  = 4 << 10
)

func (c *agentConn) recordEngineCatalog(h *agentHello) {
	if len(h.Engines) == 0 && len(h.EngineCatalog) == 0 {
		return
	}
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		return
	}
	if _, err := s.AgentV2(c.id); err != nil {
		return // not in the store (legacy registry only)
	}
	type catEntry struct {
		ID           string          `json:"id"`
		Version      string          `json:"version,omitempty"`
		ParamsSchema json.RawMessage `json:"params_schema,omitempty"`
	}
	entries := make([]catEntry, 0, len(h.Engines))
	for _, e := range h.Engines {
		if e.ID == "" {
			continue
		}
		schema := e.ParamsSchema
		if len(schema) > maxHelloCatalogSchema {
			schema = nil // an oversized schema is dropped, not truncated (must stay valid JSON)
		}
		entries = append(entries, catEntry{
			ID:           capReportString(e.ID, maxHelloCatalogIDLen),
			Version:      capReportString(e.Version, maxHelloCatalogVerLen),
			ParamsSchema: schema,
		})
		if len(entries) >= maxHelloCatalogEntries {
			break
		}
	}
	if len(entries) == 0 {
		for _, id := range h.EngineCatalog {
			if id == "" {
				continue
			}
			entries = append(entries, catEntry{ID: capReportString(id, maxHelloCatalogIDLen)})
			if len(entries) >= maxHelloCatalogEntries {
				break
			}
		}
	}
	if len(entries) == 0 {
		return
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return
	}
	if err := s.SetAgentV2Session(c.id, c.protoV, string(b)); err != nil {
		log.Printf("[agents] cannot store engine catalog for %s: %v", c.id, err)
	}
}

// docWithRevision re-marshals a desired document that already carries
// its assigned revision (the caller assigns before queuing).
func docWithRevision(d *protocol.DesiredV2) string {
	b, _ := json.Marshal(d)
	return string(b)
}

// meshPendingDesiredV2 returns the agent's pending DesiredV2 document.
func meshPendingDesiredV2(agentID string) (protocol.DesiredV2, bool) {
	var d protocol.DesiredV2
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		return d, false
	}
	v2, err := s.AgentV2(agentID)
	if err != nil || strings.TrimSpace(v2.DesiredV2) == "" {
		return d, false
	}
	if json.Unmarshal([]byte(v2.DesiredV2), &d) != nil || d.Revision == "" {
		return d, false
	}
	return d, true
}

// agentHubDeliverDesiredV2 persists the pending DesiredV2 and pushes it to
// a live v2 session immediately (offline is fine: it delivers on connect).
func agentHubDeliverDesiredV2(agentID string, d protocol.DesiredV2) error {
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		return err
	}
	doc, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if err := s.SetPendingDesiredV2(agentID, string(doc)); err != nil {
		return err
	}
	agentHubMu.Lock()
	conn := agentHubConns[agentID]
	agentHubMu.Unlock()
	if conn != nil && conn.protoV >= protocol.ProtoVersionV2 {
		if err := conn.sendVersioned(protocol.MsgDesired, d); err != nil {
			// The session died between the map lookup and the write (the
			// read loop deregisters asynchronously). The document IS
			// durably pending; drop the stale session and tell the
			// caller the delivery did NOT happen (the push handler then
			// reports "queued for reconnect" instead of a false claim).
			log.Printf("[mesh] push to %s hit a dead session (will deliver on reconnect): %v", agentID, err)
			agentHubMu.Lock()
			if agentHubConns[agentID] == conn {
				delete(agentHubConns, agentID)
			}
			agentHubMu.Unlock()
			go conn.shutdown("dead session on push: " + err.Error())
			return errAgentSessionDead
		}
	}
	return nil
}

// errAgentSessionDead: the hub map still held a session whose socket was
// already gone. The desired document is durably pending; the push should
// be reported as queued, not delivered.
var errAgentSessionDead = errors.New("agent session died before delivery")

// meshHandleAckV2 bridges an ack to the store's v2 desired state. The v1
// registry handling in handleAck is unchanged (frozen flow); this only
// maintains the store mirror.
func meshHandleAckV2(agentID, revision string, ok bool, ackErr string) {
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		return
	}
	matched, err := s.AckDesiredV2(agentID, revision, ok, ackErr)
	if err == nil && matched {
		meshAudit("agent:"+agentID, "mesh.desired.ack",
			fmt.Sprintf(`{"agent":%q,"revision":%q,"ok":%t}`, agentID, revision, ok))
	}
}
