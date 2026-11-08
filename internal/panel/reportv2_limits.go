package panel

// reportv2_limits.go — hostile-agent hardening for the v2 report
// ingestion (phase 12 TASK 2, security round 2). A compromised or
// malicious agent owns its own binary: it can send ANY wire shape, so
// the panel caps everything before touching the store:
//
//   - array bounds: tunnels/proc ≤ 4096 rows, engine_status ≤ 256,
//     errors ≤ 64 — a million-row report must not become a million
//     store queries;
//   - string bounds: tunnel ids ≤ 128 (the same rule the tunnel API
//     enforces), version/error text ≤ 512 chars — truncated, not
//     rejected, so honest agents with chatty errors still report;
//   - number sanity: negative or NaN CPU/RSS/counters are dropped (the
//     JSON decoder already rejects NaN literals, but a negative int is
//     legal JSON and must never reach the DB);
//   - OWNERSHIP: a stream only stores samples for tunnels whose pair
//     includes the REPORTING agent's own server — an agent cannot
//     inflate another tunnel's usage by reporting its key;
//   - proc/engine-status writes ride the SAME throttle as samples
//     (meshSampleMinGap) — heartbeat spam must not turn into a DB
//     write flood through the upsert path.
//
// Everything here FAILS CLOSED on the hostile side and SILENTLY on the
// honest side (honest agents never hit any cap).

import (
	"strings"

	"github.com/Dark-modem/quantum-panel/internal/store"
	"github.com/Dark-modem/quantum-protocol"
)

const (
	maxReportTunnels      = 4096
	maxReportProc         = 4096
	maxReportEngineStatus = 256
	maxReportErrors       = 64
	maxReportString       = 512
	maxTunnelIDLen        = 128
)

func capReportString(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// agentAllowedTunnels resolves the reporting agent's OWN tunnel set
// (its server's pair). Nil = the agent is unlinked (accept nothing).
func agentAllowedTunnels(s *store.Store, agentID string) (map[string]int64, error) {
	ag, err := s.GetAgent(agentID)
	if err != nil || ag.ServerID == 0 {
		return map[string]int64{}, nil
	}
	tunnels, err := s.TunnelsForServer(ag.ServerID)
	if err != nil {
		return map[string]int64{}, nil
	}
	out := map[string]int64{}
	for _, t := range tunnels {
		out[t.TunnelKey] = t.ID
	}
	return out, nil
}

// sanitizeReportTunnels caps + filters the report's tunnel rows against
// the agent's own tunnels. Returns (allowed rows, dropped count).
func sanitizeReportTunnels(rep *protocol.StateReportV2, allowed map[string]int64) ([]protocol.TunnelRuntime, int) {
	if len(rep.Tunnels) > maxReportTunnels {
		rep.Tunnels = rep.Tunnels[:maxReportTunnels]
	}
	out := make([]protocol.TunnelRuntime, 0, len(rep.Tunnels))
	dropped := 0
	for _, tr := range rep.Tunnels {
		tr.TunnelID = capReportString(tr.TunnelID, maxTunnelIDLen)
		tr.Engine = capReportString(tr.Engine, 64)
		tr.LastError = capReportString(tr.LastError, maxReportString)
		tid, ok := allowed[tr.TunnelID]
		if !ok || tid == 0 {
			dropped++ // not this agent's tunnel: usage inflation attempt
			continue
		}
		out = append(out, tr)
	}
	return out, dropped
}

// sanitizeReportProc caps + filters the proc rows (known + owned only,
// negative values dropped).
func sanitizeReportProc(rep *protocol.StateReportV2, allowed map[string]int64) []protocol.TunnelProc {
	if len(rep.Proc) > maxReportProc {
		rep.Proc = rep.Proc[:maxReportProc]
	}
	out := make([]protocol.TunnelProc, 0, len(rep.Proc))
	for _, tp := range rep.Proc {
		tp.TunnelID = capReportString(tp.TunnelID, maxTunnelIDLen)
		if tp.TunnelID == "" || !tp.Known {
			continue
		}
		if _, ok := allowed[tp.TunnelID]; !ok {
			continue
		}
		if tp.RSSBytes < 0 || tp.CPUPercent < 0 || tp.Processes < 0 {
			continue // hostile/nonsensical numbers never reach the DB
		}
		if tp.CPUPercent > 100*64 { // >64 busy cores: nonsense cap
			continue
		}
		out = append(out, tp)
	}
	return out
}

// sanitizeReportEngines caps + filters engine status rows.
func sanitizeReportEngines(rep *protocol.StateReportV2) []protocol.EngineStatusReport {
	if len(rep.EngineStatus) > maxReportEngineStatus {
		rep.EngineStatus = rep.EngineStatus[:maxReportEngineStatus]
	}
	out := make([]protocol.EngineStatusReport, 0, len(rep.EngineStatus))
	for _, es := range rep.EngineStatus {
		es.Engine = capReportString(strings.TrimSpace(es.Engine), 64)
		if es.Engine == "" {
			continue
		}
		es.Version = capReportString(es.Version, 128)
		es.LastError = capReportString(es.LastError, maxReportString)
		if es.BytesOnDisk < 0 || es.Instances < 0 {
			continue
		}
		out = append(out, es)
	}
	return out
}
