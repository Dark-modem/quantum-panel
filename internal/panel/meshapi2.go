package panel

// meshapi2.go — mesh REST API, second half: traffic/overhead/coefficient
// reporting, the agent view (with v2 session state), linking, engine
// catalogs, desired-state push, and the hardening/netopt profile flow with
// the safe-apply confirmation gate.
//
// PROFILE FLOW (safe-apply; the agent owns the rollback timer):
//  1. POST profile/hardening|netopt  {profile: {...}, rollback_timer_s: 120}
//     → command hardening.apply / netopt.profile.apply with
//     confirm=yes + rollback_timer_s. The agent applies, ARMS its timer and
//     answers; the panel records a pending confirmation (deadine =
//     applied_at + timer).
//  2. The UI shows the 120 s countdown banner.
//  3. POST profile/confirm → hardening.confirm (PROTOCOL-REQUESTS.md:
//     the allowlist entry is pending; until the agent knows it, the panel
//     surfaces the agent's rejection and the timer rolls back).
//     POST profile/rollback → the existing CmdHardeningRollback command.
//  4. No confirm inside the window → the agent rolls itself back.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	"github.com/Dark-modem/quantum-protocol"
)

// ---- usage / overhead / coefficient ---------------------------------------

func handleMeshUsage(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	q := r.URL.Query()
	from := meshQueryInt64OrNow(r, "from")
	to := meshQueryInt64OrNow(r, "to")
	if to == 0 {
		to = time.Now().Unix()
	}
	if from >= to {
		writeAPIError(w, r, "E-IN-06", "from must be < to (unix seconds)")
		return
	}
	var (
		usage store.Usage
		iran  store.Usage // iran-side streams only: the billable view
		scope string
		err   error
	)
	switch {
	case q.Get("tunnel_id") != "":
		tid, ok := meshQueryInt64(r, "tunnel_id")
		if !ok {
			writeAPIError(w, r, "E-IN-06", "bad tunnel_id")
			return
		}
		scope = fmt.Sprintf("tunnel:%d", tid)
		usage, iran, err = s.TunnelUsageDetailed(tid, from, to)
	case q.Get("server_id") != "":
		sid, ok := meshQueryInt64(r, "server_id")
		if !ok {
			writeAPIError(w, r, "E-IN-06", "bad server_id")
			return
		}
		scope = fmt.Sprintf("server:%d", sid)
		tunnels, e := s.TunnelsForServer(sid)
		if e != nil {
			err = e
			break
		}
		usage, iran, err = s.UsageForTunnelsDetailed(tunnelIDs(tunnels), from, to)
	case q.Get("topology_id") != "":
		tpid, ok := meshQueryInt64(r, "topology_id")
		if !ok {
			writeAPIError(w, r, "E-IN-06", "bad topology_id")
			return
		}
		scope = fmt.Sprintf("topology:%d", tpid)
		tunnels, e := s.ListTunnels(tpid)
		if e != nil {
			err = e
			break
		}
		usage, iran, err = s.UsageForTunnelsDetailed(tunnelIDs(tunnels), from, to)
	case q.Get("datacenter_id") != "":
		dcid, ok := meshQueryInt64(r, "datacenter_id")
		if !ok {
			writeAPIError(w, r, "E-IN-06", "bad datacenter_id")
			return
		}
		scope = fmt.Sprintf("datacenter:%d", dcid)
		servers, e := s.ListServers()
		if e != nil {
			err = e
			break
		}
		var ids []int64
		for _, sv := range servers {
			if sv.DatacenterID == dcid {
				tunnels, e := s.TunnelsForServer(sv.ID)
				if e != nil {
					err = e
					continue
				}
				ids = append(ids, tunnelIDs(tunnels)...)
			}
		}
		usage, iran, err = s.UsageForTunnelsDetailed(ids, from, to)
	default:
		writeAPIError(w, r, "E-IN-02", "one of tunnel_id, server_id, topology_id, datacenter_id is required")
		return
	}
	if err != nil {
		meshErr(w, r, err)
		return
	}
	// Per-direction overhead from the aggregated real bytes (absent =
	// unknown); billable = the iran-side streams only (download into the
	// Iran DC is what the Iran datacenter bills).
	resp := map[string]any{"scope": scope, "from": from, "to": to, "usage": usage,
		"note": "real bytes; no coefficient is ever applied"}
	if usage.PayloadRx > 0 {
		resp["overhead_rx"] = float64(usage.WireRx) / float64(usage.PayloadRx)
	}
	if usage.PayloadTx > 0 {
		resp["overhead_tx"] = float64(usage.WireTx) / float64(usage.PayloadTx)
	}
	resp["billable"] = map[string]uint64{
		"download_wire":    iran.WireRx,
		"upload_wire":      iran.WireTx,
		"download_payload": iran.PayloadRx,
		"upload_payload":   iran.PayloadTx,
	}
	writeJSON(w, resp)
}

func tunnelIDs(tunnels []store.Tunnel) []int64 {
	ids := make([]int64, 0, len(tunnels))
	for _, t := range tunnels {
		ids = append(ids, t.ID)
	}
	return ids
}

func meshQueryInt64OrNow(r *http.Request, key string) int64 {
	if v, ok := meshQueryInt64(r, key); ok {
		return v
	}
	return 0
}

func handleMeshOverhead(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	// The engines table is small; expose every engine we have seen.
	dcs, _ := s.ListDatacenters()
	type engineView struct {
		Engine  string  `json:"engine"`
		Ratio   float64 `json:"ratio"`    // rx direction; 0 = unknown (never guessed)
		RatioTx float64 `json:"ratio_tx"` // upload direction; 0 = unknown
	}
	engines := []engineView{}
	for _, e := range meshKnownEngines(s) {
		ratio, err := s.EngineOverheadRatio(e)
		if err != nil {
			continue
		}
		ratioTx, err := s.EngineOverheadRatioTx(e)
		if err != nil {
			ratioTx = 0
		}
		engines = append(engines, engineView{Engine: e, Ratio: ratio, RatioTx: ratioTx})
	}
	writeJSON(w, map[string]any{"engines": engines, "datacenters": nonNil(dcs)})
}

// meshKnownEngines lists engine ids the panel has overhead observations or
// tunnel rows for.
func meshKnownEngines(s *store.Store) []string {
	seen := map[string]bool{}
	var out []string
	if rows, err := s.DB().Query(`SELECT DISTINCT engine FROM tunnels WHERE engine != ''`); err == nil {
		for rows.Next() {
			var e string
			if rows.Scan(&e) == nil && e != "" && !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
		rows.Close()
	}
	if rows, err := s.DB().Query(`SELECT engine FROM engine_overhead`); err == nil {
		for rows.Next() {
			var e string
			if rows.Scan(&e) == nil && e != "" && !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
		rows.Close()
	}
	return out
}

func handleMeshCoefficient(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	dcid, ok := meshQueryInt64(r, "datacenter_id")
	if !ok {
		writeAPIError(w, r, "E-IN-02", "datacenter_id is required")
		return
	}
	dc, err := s.GetDatacenter(dcid)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	engines := meshKnownEngines(s)
	if e := strings.TrimSpace(r.URL.Query().Get("engine")); e != "" {
		engines = []string{e}
	}
	type suggestion struct {
		Engine      string  `json:"engine"`
		PriceRatio  float64 `json:"price_ratio"`
		Overhead    float64 `json:"overhead"`
		Coefficient float64 `json:"suggested_coefficient"` // 0 = overhead unknown
	}
	out := []suggestion{}
	for _, e := range engines {
		overhead, err := s.EngineOverheadRatio(e)
		if err != nil {
			overhead = 0
		}
		coef, err := s.SuggestedCoefficient(dcid, e)
		if err != nil {
			coef = 0
		}
		out = append(out, suggestion{Engine: e, PriceRatio: dc.PriceCoefficient,
			Overhead: overhead, Coefficient: coef})
	}
	writeJSON(w, map[string]any{
		"datacenter": dc, "suggestions": out,
		"note": "suggestion = price ratio x measured overhead; Quantum bytes stay real, the coefficient is a Pasarguard quota-side hint",
	})
}

// ---- agents ------------------------------------------------------------------

func handleMeshAgentsList(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	agents, err := s.ListAgents()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	type agentView struct {
		store.Agent
		ProtoVersion  int                         `json:"proto_version"`
		EngineCatalog json.RawMessage             `json:"engine_catalog,omitempty"`
		HasPending    bool                        `json:"has_pending_desired"`
		AppliedV2     bool                        `json:"has_applied_v2"`
		LastError     string                      `json:"last_error,omitempty"`
		Runtime       []protocolTunnelRuntimeView `json:"runtime,omitempty"`
	}
	out := []agentView{}
	for _, a := range agents {
		v := agentView{Agent: a}
		if st, err := s.AgentV2(a.ID); err == nil {
			v.ProtoVersion = st.ProtoVersion
			v.HasPending = st.DesiredV2 != ""
			v.AppliedV2 = st.AppliedV2 != ""
			v.LastError = st.LastError
			if strings.TrimSpace(st.EngineCatalog) != "" {
				_ = json.Unmarshal([]byte(st.EngineCatalog), &v.EngineCatalog)
			}
		}
		if rt := meshRuntimeSnapshot(a.ID); rt != nil {
			v.Runtime = rt
		}
		out = append(out, v)
	}
	writeJSON(w, map[string]any{"agents": out})
}

func handleMeshAgentLink(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	agentID := r.PathValue("id")
	if !validAgentID(agentID) {
		writeAPIError(w, r, "E-IN-06", "bad agent id")
		return
	}
	var body struct {
		ServerID int64 `json:"server_id"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if _, err := s.GetServer(body.ServerID); err != nil {
		meshErr(w, r, fmt.Errorf("server %d: %v", body.ServerID, err))
		return
	}
	if err := s.LinkAgentServer(agentID, body.ServerID); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.agent.link", fmt.Sprintf(`{"agent":%q,"server_id":%d}`, agentID, body.ServerID))
	writeJSON(w, map[string]any{"ok": true})
}

// handleMeshAgentPublicIP sets or clears the operator-explicit mesh
// address for one agent (Phase-4: a way to set the address explicitly
// when the agent is behind NAT and its enroll/WS remote address is not
// usable). Body {"public_ip":"203.0.113.7"} sets the override;
// {"public_ip":""} clears it and returns to auto-detection. A non-empty
// value must be a real public address: loopback/unspecified/link-local
// are rejected with a clear error instead of poisoning the mesh.
func handleMeshAgentPublicIP(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if !validAgentID(agentID) {
		writeAPIError(w, r, "E-IN-06", "bad agent id")
		return
	}
	var body struct {
		PublicIP string `json:"public_ip"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	ip := strings.TrimSpace(body.PublicIP)
	if ip != "" && !agentPublicIPUsable(ip) {
		writeAPIError(w, r, "E-IN-07",
			"public_ip must be a usable public address (not loopback, unspecified or link-local); send \"\" to clear the override")
		return
	}
	err := agentRegistryUpdate(agentID, func(rec *agentRecord) {
		rec.PublicIPOverride = ip
	})
	if err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.agent.public_ip", fmt.Sprintf(`{"agent":%q,"public_ip":%q}`, agentID, ip))
	writeJSON(w, map[string]any{"ok": true, "public_ip_override": ip})
}

// validAgentID matches the enrollment id shape: "agt_" + 12..32 lowercase
// hex (the generator uses 12 today; tolerate a longer id if the enrollment
// length ever grows).
func validAgentID(id string) bool {
	const prefix = "agt_"
	if !strings.HasPrefix(id, prefix) {
		return false
	}
	hex := id[len(prefix):]
	if len(hex) < 12 || len(hex) > 32 {
		return false
	}
	for _, c := range hex {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

type protocolTunnelRuntimeView = protocol.TunnelRuntime

// handleMeshAgentCatalog records the engine catalog an agent reported
// (today via the operator/API bridge; see PROTOCOL-REQUESTS.md for the
// in-protocol report message).
func handleMeshAgentCatalog(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	agentID := r.PathValue("id")
	if !validAgentID(agentID) {
		writeAPIError(w, r, "E-IN-06", "bad agent id")
		return
	}
	var body struct {
		Catalog json.RawMessage `json:"catalog"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	// Shape check: must be a JSON array; entries keep only {"id", "params_schema"}
	// style fields — stored verbatim after a size cap (AgentV2 enforces it).
	var entries []map[string]any
	if err := json.Unmarshal(body.Catalog, &entries); err != nil || len(entries) > 256 {
		writeAPIError(w, r, "E-IN-06", "catalog must be a JSON array of <= 256 objects")
		return
	}
	canon, _ := json.Marshal(entries)
	if _, err := s.AgentV2(agentID); err != nil {
		meshErr(w, r, err)
		return
	}
	v2, _ := s.AgentV2(agentID)
	if err := s.SetAgentV2Session(agentID, v2.ProtoVersion, string(canon)); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.agent.engine_catalog", fmt.Sprintf(`{"agent":%q,"engines":%d}`, agentID, len(entries)))
	writeJSON(w, map[string]any{"ok": true})
}

// ---- profiles (hardening / netopt) with the safe-apply gate -------------------

// profilePending tracks an armed safe-apply window per agent so the UI can
// render the countdown and confirm/rollback can find the context.
type profilePending struct {
	Kind      string `json:"kind"` // hardening | netopt
	AgentID   string `json:"agent_id"`
	Revision  string `json:"revision,omitempty"`
	AppliedAt int64  `json:"applied_at"`
	TimerSec  int    `json:"rollback_timer_s"`
	Deadline  int64  `json:"deadline"`
	Note      string `json:"note,omitempty"`
}

var (
	profilePendingMu sync.Mutex
	profilePendingBy = map[string]*profilePending{}
)

func profilePendingRecord(agentID, kind string, timerSec int, note string) *profilePending {
	if timerSec <= 0 {
		timerSec = 120
	}
	p := &profilePending{Kind: kind, AgentID: agentID, AppliedAt: time.Now().Unix(),
		TimerSec: timerSec, Deadline: time.Now().Add(time.Duration(timerSec) * time.Second).Unix(), Note: note}
	profilePendingMu.Lock()
	defer profilePendingMu.Unlock()
	profilePendingBy[agentID] = p
	return p
}

func profilePendingResolve(agentID string) *profilePending {
	profilePendingMu.Lock()
	defer profilePendingMu.Unlock()
	p := profilePendingBy[agentID]
	delete(profilePendingBy, agentID)
	return p
}

func handleMeshProfilesPending(w http.ResponseWriter, r *http.Request) {
	profilePendingMu.Lock()
	defer profilePendingMu.Unlock()
	out := []*profilePending{}
	for _, p := range profilePendingBy {
		out = append(out, p)
	}
	writeJSON(w, map[string]any{"pending": out})
}

// meshProfileApply is the shared body of the hardening/netopt apply endpoints.
func meshProfileApply(w http.ResponseWriter, r *http.Request, kind string) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	agentID := r.PathValue("id")
	if !validAgentID(agentID) {
		writeAPIError(w, r, "E-IN-06", "bad agent id")
		return
	}
	var body struct {
		Profile        json.RawMessage `json:"profile"`
		RollbackTimerS int             `json:"rollback_timer_s"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	profile := strings.TrimSpace(string(body.Profile))
	const maxProfile = 64 << 10
	if profile == "" || profile == "null" || len(profile) > maxProfile || !json.Valid([]byte(profile)) {
		writeAPIError(w, r, "E-IN-06", "profile must be valid JSON <= 64 KiB")
		return
	}
	timer := body.RollbackTimerS
	if timer <= 0 {
		timer = 120
	}
	if timer < 120 {
		timer = 120 // never shorter: the operator must be able to react
	}
	if timer > 3600 {
		timer = 3600
	}
	// Mutating profile ops REQUIRE the confirm flag and carry the rollback
	// timer (protocol v2 contract; the agent refuses to apply otherwise).
	op := protocol.CmdHardeningApply
	if kind == "netopt" {
		op = protocol.CmdNetProfileApply
	}
	res, err := agentHubCommand(agentID, op, map[string]string{
		protocol.ParamConfirm:        "yes",
		protocol.ParamRollbackTimerS: fmt.Sprintf("%d", timer),
		"payload":                    profile,
	}, 90*time.Second)
	if err != nil {
		writeAPIError(w, r, "E-AGENT-01", kind+" apply failed: "+err.Error())
		return
	}
	if !res.OK {
		writeAPIError(w, r, "E-AGENT-02", kind+" apply rejected by agent: "+res.Error)
		return
	}
	pending := profilePendingRecord(agentID, kind, timer, "")
	meshAudit(meshActor(r), "mesh.profile.apply",
		fmt.Sprintf(`{"agent":%q,"kind":%q,"rollback_timer_s":%d,"ok":%t}`, agentID, kind, timer, res.OK))
	writeJSON(w, map[string]any{
		"ok": true, "agent_id": agentID, "kind": kind,
		"rollback_timer_s": timer, "deadline": pending.Deadline,
		"result": json.RawMessage(res.Output),
		"note":   "the agent armed its automatic rollback; confirm within the window or it reverts",
	})
}

func handleMeshProfileHardening(w http.ResponseWriter, r *http.Request) {
	meshProfileApply(w, r, "hardening")
}

func handleMeshProfileNetopt(w http.ResponseWriter, r *http.Request) {
	meshProfileApply(w, r, "netopt")
}

func handleMeshProfileConfirm(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	agentID := r.PathValue("id")
	if !validAgentID(agentID) {
		writeAPIError(w, r, "E-IN-06", "bad agent id")
		return
	}
	pending := profilePendingResolve(agentID)
	if pending == nil {
		writeAPIError(w, r, "E-IN-04", "no pending profile confirmation for this agent")
		return
	}
	// hardening.confirm / netopt.profile.confirm: see PROTOCOL-REQUESTS.md —
	// the protocol allowlist entry is requested; until it lands the agent
	// rejects the op and the answer says exactly that (the timer keeps
	// running: fail-closed).
	op := "hardening.confirm"
	if pending.Kind == "netopt" {
		op = "netopt.profile.confirm"
	}
	res, err := agentHubCommand(agentID, op, map[string]string{}, 30*time.Second)
	if err != nil {
		// Re-arm the pending entry: the window is still open, only the
		// confirm attempt failed.
		profilePendingRecord(agentID, pending.Kind, pending.TimerSec, "confirm attempt failed")
		writeAPIError(w, r, "E-AGENT-01", "confirm failed: "+err.Error())
		return
	}
	if !res.OK {
		profilePendingRecord(agentID, pending.Kind, pending.TimerSec, "agent rejected confirm: "+res.Error)
		writeAPIError(w, r, "E-AGENT-02", "agent rejected confirm ("+res.Error+"); it will roll back at the deadline unless the protocol confirm op is available")
		return
	}
	meshAudit(meshActor(r), "mesh.profile.confirm", fmt.Sprintf(`{"agent":%q,"kind":%q}`, agentID, pending.Kind))
	writeJSON(w, map[string]any{"ok": true, "agent_id": agentID, "kind": pending.Kind})
}

func handleMeshProfileRollback(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	agentID := r.PathValue("id")
	if !validAgentID(agentID) {
		writeAPIError(w, r, "E-IN-06", "bad agent id")
		return
	}
	kind := "hardening"
	if strings.TrimSpace(r.URL.Query().Get("kind")) == "netopt" {
		kind = "netopt"
	}
	op := protocol.CmdHardeningRollback
	if kind == "netopt" {
		op = protocol.CmdNetProfileRollback
	}
	res, err := agentHubCommand(agentID, op, map[string]string{
		protocol.ParamConfirm: "yes",
	}, 90*time.Second)
	if err != nil {
		writeAPIError(w, r, "E-AGENT-01", kind+" rollback failed: "+err.Error())
		return
	}
	profilePendingResolve(agentID)
	meshAudit(meshActor(r), "mesh.profile.rollback", fmt.Sprintf(`{"agent":%q,"kind":%q,"ok":%t}`, agentID, kind, res.OK))
	writeJSON(w, map[string]any{"ok": res.OK, "result": json.RawMessage(res.Output)})
}
