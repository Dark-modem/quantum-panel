package panel

// topo_dc.go — contract §2.21.2 (BACKEND-41, the DC-centric topology wizard
// backend): POST /api/mesh/topologies/dc creates a topology from TWO
// datacenters + agent subsets in ONE call — server-side resolution of the
// agents' linked servers (mesh store read only when open: uiOpen answers the
// structured E-STORE envelope and nothing is created when it cannot open),
// per-agent validation with the offender named (§1 envelope, 400), the §10.3
// one-DC-per-side invariant enforced on the resolved server sets (409
// E-TOPOLOGY-DC-CONFLICT; through this endpoint it is defense-in-depth — the
// per-agent validation already pins every server to the chosen DC), then the
// EXISTING GenerateTopology orchestration (same slug keys, idempotent by
// unique keys: re-running the same body converges) and, when push (default
// true), the EXISTING compile+push per selected agent with the same
// enable-before-use gate the push endpoint applies. Push failures are
// reported per agent and never roll the topology back.
//
// The created topology starts ENABLED (contract addendum): push compiles
// enabled topologies/tunnels only, so an disabled creation would push an
// empty document and defeat the wizard's purpose.
//
// Response: {status, topology_id, pairs, tunnels, pushes, warnings} — the
// tunnel rows carry stripped engine params (the §2.8 secret discipline) and
// the pushes[] rows use the contract's exact two shapes; designed skips
// (queued for delivery / already applied) ride warnings[] (see the §2.21.2
// addendum in UI-API-CONTRACT.md).

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// topoDcSide is one side of the §2.21.2 body.
type topoDcSide struct {
	DatacenterID int64    `json:"datacenter_id"`
	AgentIDs     []string `json:"agent_ids"`
}

// topoDcRequest is the §2.21.2 body. engine/tunnels_per_pair/push are
// optional (defaults: "matrix" / 1 / true).
type topoDcRequest struct {
	Name           string     `json:"name"`
	Iran           topoDcSide `json:"iran"`
	Foreign        topoDcSide `json:"foreign"`
	Engine         string     `json:"engine"`
	TunnelsPerPair int        `json:"tunnels_per_pair"`
	Push           *bool      `json:"push"`
}

// handleMeshTopoCreateDC implements §2.21.2.
func handleMeshTopoCreateDC(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	s := h.Store

	var req topoDcRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, meshMaxBody))
	if err != nil {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "body", "reason": fmt.Sprintf("cannot read body (max %d bytes)", meshMaxBody),
		}, "")
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "body", "reason": "empty JSON body",
		}, "")
		return
	}
	if err := json.Unmarshal(body, &req); err != nil {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "body", "reason": "invalid JSON",
		}, "")
		return
	}

	// Defaults (§2.21.2): engine "matrix", tunnels_per_pair 1, push true.
	engine := req.Engine
	if engine == "" {
		engine = "matrix"
	}
	tpp := req.TunnelsPerPair
	if tpp == 0 {
		tpp = 1
	}
	push := true
	if req.Push != nil {
		push = *req.Push
	}

	// Body-level validation (400 E-VALIDATION, the field named).
	if !topoSlugRe.MatchString(req.Name) {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "name", "reason": "name must be 1-64 chars of [a-z0-9-_]",
		}, "")
		return
	}
	if !engineIDRe.MatchString(engine) {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "engine", "reason": "invalid engine id",
		}, "")
		return
	}
	if tpp < 1 || tpp > 26 {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "tunnels_per_pair", "reason": "tunnels_per_pair must be 1-26",
		}, "")
		return
	}

	// The datacenters must exist and carry the matching kind.
	dcIr, err := s.GetDatacenter(req.Iran.DatacenterID)
	if err != nil {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "iran.datacenter_id", "reason": "unknown datacenter",
		}, "")
		return
	}
	if dcIr.Kind != "iran" {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "iran.datacenter_id", "datacenters": []string{dcIr.Name},
			"reason": "datacenter kind is " + dcIr.Kind + ", want iran",
		}, "")
		return
	}
	dcFr, err := s.GetDatacenter(req.Foreign.DatacenterID)
	if err != nil {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "foreign.datacenter_id", "reason": "unknown datacenter",
		}, "")
		return
	}
	if dcFr.Kind != "foreign" {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "foreign.datacenter_id", "datacenters": []string{dcFr.Name},
			"reason": "datacenter kind is " + dcFr.Kind + ", want foreign",
		}, "")
		return
	}

	// Agent subsets: at least one per side; duplicates collapse (order kept).
	iranIDs := dcTopoDedup(req.Iran.AgentIDs)
	foreignIDs := dcTopoDedup(req.Foreign.AgentIDs)
	if len(iranIDs) == 0 {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "iran.agent_ids", "reason": "at least one agent per side is required",
		}, "")
		return
	}
	if len(foreignIDs) == 0 {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{
			"field": "foreign.agent_ids", "reason": "at least one agent per side is required",
		}, "")
		return
	}

	// Per-agent validation with the offender named (§2.21.2): the agent must
	// exist, have a LINKED server, that server must belong to the chosen
	// datacenter of the correct kind, and the agent's role must match the
	// side. One structured 400 naming the first offender.
	iranAgents, iranServers, iok := dcTopoResolveSide(w, r, s, iranIDs, dcIr, "iran")
	if !iok {
		return
	}
	foreignAgents, foreignServers, fok := dcTopoResolveSide(w, r, s, foreignIDs, dcFr, "foreign")
	if !fok {
		return
	}

	// §10.3 one-DC-per-side invariant on the RESOLVED server sets (409
	// E-TOPOLOGY-DC-CONFLICT). Defense-in-depth: the per-agent validation
	// already pins every server to the chosen DC, so this fires only if the
	// two checks ever drift. details mirror §10.3's
	// {side, datacenters, tunnels} with agents in tunnels' place (the
	// allowlisted `agents` key — contract addendum).
	if !dcTopoEnforceOneDC(w, r, s, req.Name, "iran", dcIr, iranAgents, iranServers) {
		return
	}
	if !dcTopoEnforceOneDC(w, r, s, req.Name, "foreign", dcFr, foreignAgents, foreignServers) {
		return
	}

	// The EXISTING generation orchestration: same slug keys, same
	// idempotency (unique keys — re-running the same body converges), and
	// its own single-DC check as the last belt. Enabled=true (addendum).
	engines := make([]string, tpp)
	for i := range engines {
		engines[i] = engine
	}
	res, err := GenerateTopology(TopoGenRequest{
		Name:           req.Name,
		IranServers:    serverIDsOf(iranServers),
		ForeignServers: serverIDsOf(foreignServers),
		TunnelsPerPair: tpp,
		Engines:        engines,
		Enabled:        true,
	})
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "generate topology"}, err.Error())
		return
	}

	// Push (default true): the EXISTING compile+push per selected agent —
	// both sides, dedup'd, in selection order — behind the same
	// enable-before-use gate the push endpoint applies. Failures are
	// reported per agent and never roll the topology back.
	pushes := []map[string]any{}
	warnings := append([]string{}, res.Warnings...)
	if push {
		pushed := map[string]bool{}
		for _, ag := range append(append([]store.Agent{}, iranAgents...), foreignAgents...) {
			if pushed[ag.ID] {
				continue
			}
			pushed[ag.ID] = true
			row, warn := dcTopoPushOne(s, ag.ID)
			pushes = append(pushes, row)
			if warn != "" {
				warnings = append(warnings, warn)
			}
		}
	}

	// The tunnel rows carry stripped engine params (the §2.8 discipline —
	// structural here: §2.21.2 carries no params, rows hold the "{}" default).
	tunnels := make([]store.Tunnel, 0, len(res.Tunnels))
	for _, t := range res.Tunnels {
		t.EngineParams = stripEngineSecrets(t.Engine, t.EngineParams)
		tunnels = append(tunnels, t)
	}

	meshAudit(meshActor(r), "mesh.topology.generate_dc",
		fmt.Sprintf(`{"topology_id":%d,"name":%q,"pairs":%d,"tunnels":%d,"push":%t,"pushed":%d}`,
			res.TopologyID, req.Name, len(iranServers)*len(foreignServers), len(res.Tunnels), push, len(pushes)))

	writeJSON(w, map[string]any{
		"status":      "ok",
		"topology_id": res.TopologyID,
		"pairs":       len(iranServers) * len(foreignServers),
		"tunnels":     tunnels,
		"pushes":      pushes,
		"warnings":    warnings,
	})
}

// dcTopoDedup collapses duplicate agent ids, keeping first-occurrence order.
func dcTopoDedup(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// dcTopoResolveSide validates one side's agent subset and resolves it to the
// linked servers (dedup by server id, order kept). Answers the structured
// 400 naming the first offender; ok=false means the error was written.
func dcTopoResolveSide(w http.ResponseWriter, r *http.Request, s *store.Store,
	ids []string, dc store.Datacenter, side string) ([]store.Agent, []store.Server, bool) {
	fail := func(agID, agName, reason string) bool {
		d := map[string]any{"field": side + ".agent_ids", "reason": reason}
		if agID != "" {
			d["agent"] = agID
			d["agent_name"] = agName
		}
		uiErrWrite(w, r, "E-VALIDATION", d, "")
		return false
	}
	role := side // the agent role and the server role both equal the side name
	agents := make([]store.Agent, 0, len(ids))
	seenSrv := map[int64]bool{}
	servers := make([]store.Server, 0, len(ids))
	for _, id := range ids {
		if !validAgentID(id) {
			return nil, nil, fail(id, "", "invalid agent id")
		}
		ag, err := s.GetAgent(id)
		if err != nil {
			return nil, nil, fail(id, "", "unknown agent")
		}
		if ag.ServerID == 0 {
			return nil, nil, fail(id, ag.Name, "agent has no linked server")
		}
		sv, err := s.GetServer(ag.ServerID)
		if err != nil {
			return nil, nil, fail(id, ag.Name, "linked server is unreadable")
		}
		if sv.DatacenterID != dc.ID {
			other := dcNameOf(s, sv.DatacenterID)
			return nil, nil, fail(id, ag.Name,
				fmt.Sprintf("linked server %q sits in datacenter %q, want %q", sv.Name, other, dc.Name))
		}
		if sv.Role != role {
			return nil, nil, fail(id, ag.Name,
				fmt.Sprintf("linked server %q has role %q, want %q", sv.Name, sv.Role, role))
		}
		if ag.Role != role {
			return nil, nil, fail(id, ag.Name,
				fmt.Sprintf("agent role is %q, want %q (an agent may appear on only one side)", ag.Role, role))
		}
		agents = append(agents, ag)
		if !seenSrv[sv.ID] {
			seenSrv[sv.ID] = true
			servers = append(servers, sv)
		}
	}
	return agents, servers, true
}

// dcTopoEnforceOneDC is the §10.3 invariant on the resolved set: every
// server on the side must sit in the chosen datacenter. Writes the 409
// E-TOPOLOGY-DC-CONFLICT envelope (details.side / details.datacenters /
// details.agents — the tunnels analog, contract addendum) and returns false
// when the side violates it.
func dcTopoEnforceOneDC(w http.ResponseWriter, r *http.Request, s *store.Store,
	topoName, side string, chosen store.Datacenter, agents []store.Agent, servers []store.Server) bool {
	names := map[int64]bool{chosen.ID: true}
	for _, sv := range servers {
		names[sv.DatacenterID] = true
	}
	if len(names) <= 1 {
		return true
	}
	dcNames := make([]string, 0, len(names))
	for id := range names {
		dcNames = append(dcNames, dcNameOf(s, id))
	}
	sort.Strings(dcNames)
	agIDs := make([]string, 0, len(agents))
	for _, ag := range agents {
		agIDs = append(agIDs, ag.ID)
	}
	sort.Strings(agIDs)
	uiErrWrite(w, r, "E-TOPOLOGY-DC-CONFLICT", map[string]any{
		"topology":    topoName,
		"side":        side,
		"datacenters": dcNames,
		"agents":      agIDs,
	}, "")
	return false
}

// dcTopoPushOne compiles+pushes for one selected agent behind the same
// enable-before-use gate the push endpoint applies (check-only variant —
// the failure rides the per-agent row, not a response-level error).
// Returns the contract row plus an optional warning (designed skips:
// queued-for-delivery / already-applied are rows of status "ok" with the
// reason in warnings[] — honest, and the rows keep the contract's two
// shapes; a v1-protocol agent can never take DesiredV2 and reports failed).
func dcTopoPushOne(s *store.Store, agentID string) (map[string]any, string) {
	if code, msg := uiAgentTunnelsReady(s, agentID); code != "" {
		return map[string]any{"agent_id": agentID, "status": "failed", "error": msg}, ""
	}
	res, err := compileAndPushAgent(s, agentID)
	if err != nil {
		return map[string]any{"agent_id": agentID, "status": "failed",
			"error": redactErrorText(err.Error())}, ""
	}
	skip, _ := res["skip"].(string)
	switch {
	case res["pushed"] == true:
		return map[string]any{"agent_id": agentID, "status": "ok"}, ""
	case skip != "" && strings.Contains(skip, "protocol v1"):
		// A known v1 agent cannot ever receive DesiredV2: an honest failure,
		// not a silent ok (the operator must move that agent to v2).
		return map[string]any{"agent_id": agentID, "status": "failed", "error": skip}, ""
	case skip != "":
		// Queued for delivery (never-connected / offline) or already applied
		// (idempotent re-run): the designed outcomes ride warnings[].
		return map[string]any{"agent_id": agentID, "status": "ok"},
			"agent " + agentID + ": " + skip
	default:
		return map[string]any{"agent_id": agentID, "status": "ok"}, ""
	}
}

// dcNameOf resolves a datacenter display name; an unknown id answers its
// numeric form (honest, never a fake name).
func dcNameOf(s *store.Store, id int64) string {
	if id == 0 {
		return "(none)"
	}
	if dc, err := s.GetDatacenter(id); err == nil && dc.Name != "" {
		return dc.Name
	}
	return fmt.Sprintf("dc-%d", id)
}

func serverIDsOf(svs []store.Server) []int64 {
	out := make([]int64, 0, len(svs))
	for _, sv := range svs {
		out = append(out, sv.ID)
	}
	return out
}
