package panel

// agentwire.go glues the agent hub/registry (agentreg.go, agenthub.go) to
// the tunnel orchestration layer (agent_topology.go) and the HTTP route
// table. It is the ONLY place that knows both sides, so the tunnel layer
// stays testable without a live hub.

// agentTunnelRegistryLookup adapts a registry record to the narrow view
// the tunnel layer consumes (including traffic rates for the estimate).
func agentTunnelRegistryLookup(id string) (agentTunnelAgentInfo, bool) {
	agentRegMu.Lock()
	agents, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		return agentTunnelAgentInfo{ID: id}, false
	}
	for i := range agents {
		if agents[i].ID != id {
			continue
		}
		rec := agents[i]
		publicIP := rec.PublicIP
		if rec.PublicIPOverride != "" {
			publicIP = rec.PublicIPOverride // operator-set explicit address wins
		}
		info := agentTunnelAgentInfo{
			ID:              rec.ID,
			Name:            rec.Name,
			Role:            rec.Role,
			Status:          rec.Status,
			PublicIP:        publicIP,
			Version:         rec.Version,
			Hostname:        rec.Hostname,
			Caps:            rec.Caps,
			AppliedRevision: rec.AppliedRevision,
			LastState:       rec.LastState,
		}
		if len(rec.TrafficRate) > 0 {
			info.TrafficRate = make(map[string]agentTrafficRateView, len(rec.TrafficRate))
			for k, r := range rec.TrafficRate {
				info.TrafficRate[k] = agentTrafficRateView{Name: r.Name, Kind: r.Kind, RxBps: r.RxBps, TxBps: r.TxBps}
			}
		}
		return info, true
	}
	return agentTunnelAgentInfo{ID: id}, false
}

// wireAgentSubsystem connects the hub functions and hooks to the tunnel
// orchestration. Called once from main() on the controller path; the
// headless agent path (gre-panel agent) never runs it.
func wireAgentSubsystem() {
	agentHubDeliverDesiredFn.set(agentHubDeliverDesired)
	agentHubCommandFn.set(agentHubCommand)
	agentHubOnlineFn.set(agentHubOnline)
	agentRegistryLookupFn.set(agentTunnelRegistryLookup)
	agentAckObserver.set(agentTunnelAckObserved)
	agentRegisterRelay.set(agentTunnelRegisterRelay)
	// Audit F3: finish what a previous controller process left behind.
	// Runs once: a synchronous store scan, then one-shot goroutines for
	// the returned work — there is no retry timer anywhere, and the
	// resume-aware pipeline re-applies the same instance with fresh
	// revisions, so it can never duplicate services or firewall rules.
	provision, teardown := reconcileAgentTunnels()
	for _, id := range provision {
		go agentTunnelProvision(id)
	}
	for _, id := range teardown {
		go agentTunnelRedeliverTeardown(id)
	}
}
