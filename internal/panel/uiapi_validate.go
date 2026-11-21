package panel

// uiapi_validate.go — the enable-before-use gate (Phase 11, requirement
// 5): a tunnel whose engine is not enabled (or has no signed artifact)
// on one of the two endpoint agents is REFUSED with an actionable,
// structured error naming the agent, the engine and the exact UI path.

import (
	"net/http"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// uiEngineReadiness checks one (server, engine) pair for the agent's
// enable state and artifact availability. Returns "" when ready to use,
// else a structured error is written and the code is returned.
func uiEngineReadiness(w http.ResponseWriter, r *http.Request, s *store.Store,
	serverID int64, engine string, role string) string {
	ag, err := s.AgentByServer(serverID)
	if err != nil {
		return "" // no agent linked yet: the existing link/push flow handles that
	}
	enabled, err := s.AgentEngineDesired(ag.ID, engine)
	if err != nil || enabled {
		// Legacy/default-enabled engines pass here; the artifact check
		// below still applies to non-kernel engines.
	} else {
		uiErrWrite(w, r, "E-ENGINE-NOT-ENABLED", map[string]any{
			"engine": engine, "agent": ag.ID, "agent_name": ag.Name, "role": role,
		}, "Engine "+engine+" is not enabled on agent "+ag.ID+". Open Agents > "+ag.ID+" > Engines and enable it.")
		return "E-ENGINE-NOT-ENABLED"
	}
	if uiArtifactEngines[engine] {
		arch := ag.Arch
		if arch == "" {
			arch = "amd64"
		}
		artifacts, _ := s.ListEngineArtifacts()
		found := false
		for _, a := range artifacts {
			if a.Engine == engine && a.Goarch == arch {
				found = true
				break
			}
		}
		if !found {
			uiErrWrite(w, r, "E-ENGINE-NO-ARTIFACT", map[string]any{
				"engine": engine, "agent": ag.ID, "agent_name": ag.Name, "arch": arch,
			}, "")
			return "E-ENGINE-NO-ARTIFACT"
		}
	}
	return ""
}

// uiValidateTunnelEngines runs the readiness gate on BOTH endpoints of a
// tunnel about to be created/enabled. wired into create + push.
func uiValidateTunnelEngines(w http.ResponseWriter, r *http.Request, s *store.Store,
	localServerID, remoteServerID int64, engine string) bool {
	if engine == "" {
		return true
	}
	if code := uiEngineReadiness(w, r, s, localServerID, engine, "local"); code != "" {
		return false
	}
	if remoteServerID != 0 && remoteServerID != localServerID {
		if code := uiEngineReadiness(w, r, s, remoteServerID, engine, "remote"); code != "" {
			return false
		}
	}
	return true
}

// uiValidateAgentTunnels is the push-path gate: every enabled tunnel the
// agent is about to receive must pass readiness on both ends.
func uiValidateAgentTunnels(w http.ResponseWriter, r *http.Request, s *store.Store, agentID string) bool {
	ag, err := s.GetAgent(agentID)
	if err != nil || ag.ServerID == 0 {
		return true // nothing to check yet
	}
	tunnels, err := s.TunnelsForServer(ag.ServerID)
	if err != nil {
		return true
	}
	seen := map[string]bool{}
	for _, t := range tunnels {
		if !t.Enabled || seen[t.Engine] {
			continue
		}
		seen[t.Engine] = true
		if !uiValidateTunnelEngines(w, r, s, t.LocalServerID, t.RemoteServerID, t.Engine) {
			return false
		}
	}
	return true
}

// uiEngineReadinessCheck is the non-writing variant of uiEngineReadiness
// (BACKEND-41 per-agent push reporting): same policy, but the failure comes
// back as (catalog code, operator-safe message) instead of a written
// response — the caller decides where it surfaces.
func uiEngineReadinessCheck(s *store.Store, serverID int64, engine string) (string, string) {
	ag, err := s.AgentByServer(serverID)
	if err != nil {
		return "", "" // no agent linked yet: the existing link/push flow handles that
	}
	enabled, err := s.AgentEngineDesired(ag.ID, engine)
	if err == nil && !enabled {
		return "E-ENGINE-NOT-ENABLED",
			"Engine " + engine + " is not enabled on agent " + ag.ID + ". Open Agents > " + ag.ID + " > Engines and enable it."
	}
	if uiArtifactEngines[engine] {
		arch := ag.Arch
		if arch == "" {
			arch = "amd64"
		}
		artifacts, _ := s.ListEngineArtifacts()
		found := false
		for _, a := range artifacts {
			if a.Engine == engine && a.Goarch == arch {
				found = true
				break
			}
		}
		if !found {
			return "E-ENGINE-NO-ARTIFACT",
				"no signed " + engine + " artifact for arch " + arch + " on agent " + ag.ID + " (upload one under Engines > artifacts)"
		}
	}
	return "", ""
}

// uiAgentTunnelsReady is the non-writing variant of uiValidateAgentTunnels:
// every enabled tunnel the agent would receive must pass readiness on both
// ends; answers (code, message) of the first unready (server, engine).
func uiAgentTunnelsReady(s *store.Store, agentID string) (string, string) {
	ag, err := s.GetAgent(agentID)
	if err != nil || ag.ServerID == 0 {
		return "", "" // nothing to check yet
	}
	tunnels, err := s.TunnelsForServer(ag.ServerID)
	if err != nil {
		return "", ""
	}
	seen := map[string]bool{}
	for _, t := range tunnels {
		if !t.Enabled || seen[t.Engine] {
			continue
		}
		seen[t.Engine] = true
		for _, srv := range []int64{t.LocalServerID, t.RemoteServerID} {
			if srv == 0 {
				continue
			}
			if code, msg := uiEngineReadinessCheck(s, srv, t.Engine); code != "" {
				return code, msg
			}
		}
	}
	return "", ""
}
