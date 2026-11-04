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
func compileDesiredV2(s *store.Store, ag store.Agent) (desired *protocol.DesiredV2, skip string, warnings []string, err error) {
	if ag.ServerID == 0 {
		return nil, "agent not linked to a configured server (link it first)", nil, nil
	}
	server, err := s.GetServer(ag.ServerID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("linked server: %v", err)
	}
	tunnels, err := s.TunnelsForServer(server.ID)
	if err != nil {
		return nil, "", nil, err
	}
	var specs []protocol.TunnelSpec
	engineUsed := map[string]bool{}
	topoSeen := map[int64]bool{}
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
		return nil, "no enabled tunnels reference this server yet", nil, nil
	}

	// Engines: used ones enabled; catalog engines that are idle disabled
	// (the engine framework uninstalls idle engines — they must consume
	// zero resources).
	var engines []protocol.EngineSpec
	for e := range engineUsed {
		engines = append(engines, protocol.EngineSpec{ID: e, Enabled: true})
	}
	v2state, err := s.AgentV2(ag.ID)
	if err != nil {
		return nil, "", nil, fmt.Errorf("agent v2 state: %v", err)
	}
	if strings.TrimSpace(v2state.EngineCatalog) != "" {
		var catalog []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(v2state.EngineCatalog), &catalog); err != nil {
			return nil, "", nil, fmt.Errorf("engine catalog: %v", err)
		}
		for _, c := range catalog {
			if c.ID != "" && !engineUsed[c.ID] {
				engines = append(engines, protocol.EngineSpec{ID: c.ID, Enabled: false})
			}
		}
	}
	sort.Slice(engines, func(i, j int) bool { return engines[i].ID < engines[j].ID })
	sort.Slice(specs, func(i, j int) bool { return specs[i].ID < specs[j].ID })

	// Balancing: per-tunnel weight rules resolved with the store's
	// specificity chain, scoped to the topologies this agent tunnels for.
	bal := protocol.BalancingPolicy{HealthCheck: meshDefaultHealth(), Failover: meshDefaultFailover()}
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
				TopologyID:  topoNameCache(s, topoID),
				TunnelID:    t.TunnelKey,
				LocalServer: serverNameCache(s, t.LocalServerID),
				RemoteServer: serverNameCache(s, t.RemoteServerID),
				Weight:      w,
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
	}

	desired = &protocol.DesiredV2{
		Engines: engines, Tunnels: specs, Balancing: &bal,
	}
	return desired, "", warnings, nil
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
	if v2state.ProtoVersion < 2 {
		return map[string]any{
			"agent_id": agentID, "pushed": false,
			"skip": "agent speaks protocol v1; DesiredV2 needs a v2 session (v1 agents keep the frozen matrix flow)",
		}, nil
	}
	desired, skip, warnings, err := compileDesiredV2(s, ag)
	if err != nil {
		return nil, err
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
	if err := agentHubDeliverDesiredV2(agentID, *desired); err != nil {
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
		return conn.sendVersioned(protocol.MsgDesired, d)
	}
	return nil
}

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
