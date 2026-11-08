package panel

// reportv2.go — ingestion of protocol.StateReportV2 heartbeats from v2
// agent sessions. What is stored where:
//
//   - per-tunnel wire AND payload counters → store traffic_samples as the
//     RAW cumulative readings; usage math (deltas, reset handling) happens
//     at query time, so a reboot can never double-count;
//   - per-engine overhead samples → store engine_overhead (derived ratio,
//     real bytes in, ratio out — never hand-entered);
//   - live tunnel health (up/latency/engine) → the in-memory runtime map
//     below (30 s heartbeat cadence repopulates it after a panel restart);
//   - agent identity mirror (status/last_seen/applied revision) → store
//     agents, so the mesh API and the legacy registry agree.
//
// The Pasarguard coefficient and the datacenter price hint are NEVER
// applied here: Quantum statistics are always real bytes.

import (
	"log"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	"github.com/Dark-modem/quantum-protocol"
)

var (
	meshRuntimeMu   sync.Mutex
	meshRuntimeBy   = map[string][]protocol.TunnelRuntime{} // agentID → latest report
	meshRuntimeSeen = map[string]int64{}                    // agentID → last ingest at
)

// meshRuntimeSnapshot returns a copy of an agent's latest tunnel runtimes.
func meshRuntimeSnapshot(agentID string) []protocol.TunnelRuntime {
	meshRuntimeMu.Lock()
	defer meshRuntimeMu.Unlock()
	rt := meshRuntimeBy[agentID]
	out := make([]protocol.TunnelRuntime, len(rt))
	copy(out, rt)
	return out
}

// meshRuntimeStale marks the live view of agents unseen for a while
// (status reads combine this with the store's offline flag).
func meshRuntimeFresh(agentID string, maxAge time.Duration) bool {
	meshRuntimeMu.Lock()
	defer meshRuntimeMu.Unlock()
	return time.Now().Unix()-meshRuntimeSeen[agentID] < int64(maxAge.Seconds())
}

// meshSampleMinGap throttles sample writes (identical-second heartbeats
// must not flood the table); a var so tests can disable the throttle.
var meshSampleMinGap = 5 * time.Second
var meshLastSampleAt = map[string]int64{}
var meshSampleMu sync.Mutex

// ingestStateReportV2 stores one v2 state report. Called from the hub's
// heartbeat path for negotiated-v2 sessions only.
func ingestStateReportV2(agentID string, rep *protocol.StateReportV2) {
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		return
	}
	now := time.Now().Unix()

	meshSampleMu.Lock()
	lastAt := meshLastSampleAt[agentID]
	meshLastSampleAt[agentID] = now
	meshSampleMu.Unlock()
	if now-lastAt < int64(meshSampleMinGap.Seconds()) && lastAt != 0 {
		// Hammering heartbeats must not flood the samples table; the latest
		// report still updates the runtime view below.
	} else {
		// Hostile-agent hardening (phase 12 TASK 2): caps + OWNERSHIP
		// before anything touches the store — an agent may only report
		// ITS OWN server's tunnels, and row/string/number limits apply.
		allowed, aerr := agentAllowedTunnels(s, agentID)
		if aerr != nil {
			allowed = map[string]int64{}
		}
		rep.Tunnels, _ = sanitizeReportTunnels(rep, allowed)
		samples := make([]store.TrafficSample, 0, len(rep.Tunnels))
		for _, tr := range rep.Tunnels {
			tid := allowed[tr.TunnelID] // pre-resolved: no per-row query
			if tid == 0 {
				continue
			}
			samples = append(samples, store.TrafficSample{
				TunnelID:      tid,
				At:            now,
				WireRx:        tr.WireRx,
				WireTx:        tr.WireTx,
				PayloadRx:     tr.PayloadRx,
				PayloadTx:     tr.PayloadTx,
				CountersKnown: tr.CountersKnown,
				AgentID:       agentID, // each reporter is its own counter stream
				PayloadKnown:  tr.PayloadKnown,
			})
		}
		if err := s.AddTrafficSamplesBatch(samples); err != nil {
			log.Printf("[mesh] sample batch: %v", err)
		}
		for _, es := range rep.Engines {
			if es.Engine == "" || len(es.Engine) > 64 {
				continue
			}
			_ = s.ObserveEngineOverhead(es.Engine, es.WireRx, es.PayloadRx, es.WireTx, es.PayloadTx)
		}
		// Phase 11 UI data (throttled WITH the samples — heartbeat spam
		// must not become a DB write flood through the upsert paths).
		ingestUISections(s, agentID, rep, now)
	}

	meshRuntimeMu.Lock()
	meshRuntimeBy[agentID] = rep.Tunnels
	meshRuntimeSeen[agentID] = now
	meshRuntimeMu.Unlock()

	// Persist per-tunnel state transitions + errors into the bounded
	// event log (drives the tunnel detail view's history). The flip
	// detection MUST see every heartbeat (it compares consecutive
	// reports); the event WRITES stay naturally rare (flips only).
	recordTunnelEvents(s, agentID, rep, now)

	// Mirror the session facts into the store's agents table (the store has
	// no revoked concept; revocation lives in the legacy registry).
	if rep.AppliedRevision != "" {
		_, _ = s.DB().Exec(`UPDATE agents SET applied_revision = ? WHERE id = ?`, rep.AppliedRevision, agentID)
	}
	_, _ = s.DB().Exec(`UPDATE agents SET status = 'online', last_seen = ? WHERE id = ?`, now, agentID)

	// Retention: cheap and rare. Prune raw samples past retention AND
	// fold every COMPLETE hour into traffic_hourly first (the watermark
	// makes the fold idempotent; without a production caller the hourly
	// table stayed empty and 30d ranges lost their history).
	if now%60 == 0 {
		if n, err := s.RollupTrafficHourly(now - 2*3600); err != nil {
			log.Printf("[mesh] traffic rollup: %v", err)
		} else if n > 0 {
			log.Printf("[mesh] folded %d hourly traffic rows", n)
		}
		if n, err := s.PruneTrafficSamples(now - 30*24*3600); err == nil && n > 0 {
			log.Printf("[mesh] pruned %d traffic samples", n)
		}
		if n, err := s.PruneTunnelEvents(now - 7*24*3600); err == nil && n > 0 {
			log.Printf("[mesh] pruned %d tunnel events", n)
		}
	}
}

// satI64 saturates a uint64 into int64 (a hostile MaxUint64 must not
// wrap into a negative number — gosec G115).
func satI64(v uint64) int64 {
	if v > uint64(1)<<63-1 {
		return int64(uint64(1)<<63 - 1)
	}
	return int64(v)
}

// ingestUISections persists the Phase-11 report sections: host metrics
// into agents, engine_status[] into agent_engines, proc[] into
// tunnel_proc. All best-effort: a UI-data failure must never break the
// tunnel data path.
func ingestUISections(s *store.Store, agentID string, rep *protocol.StateReportV2, now int64) {
	if r := rep.Resources; r != nil {
		// Sanity: negatives/nonsense never reach the agents table.
		cpuPct, load := r.CPUPercent, r.LoadAvg1
		if cpuPct < 0 || cpuPct > 100*64 {
			cpuPct = 0
		}
		if load < 0 {
			load = 0
		}
		memTotal, memUsed := satI64(r.MemTotalMB), satI64(r.MemUsedMB)
		diskTotal, diskUsed := float64(r.DiskTotalGB), float64(r.DiskUsedGB)
		uptime := satI64(r.UptimeS)
		if memUsed < 0 || diskUsed < 0 || uptime < 0 {
			return // hostile metrics block: drop the whole section
		}
		_ = s.UpdateAgentResources(agentID, r.CPUCores, cpuPct, load,
			memTotal, memUsed, diskTotal, diskUsed, uptime, capReportString(r.Virt, 64))
	}
	allowed, aerr := agentAllowedTunnels(s, agentID)
	if aerr != nil {
		allowed = map[string]int64{}
	}
	engines := make([]store.AgentEngine, 0, len(rep.EngineStatus))
	for _, es := range sanitizeReportEngines(rep) {
		engines = append(engines, store.AgentEngine{
			AgentID: agentID, Engine: es.Engine, Installed: es.Installed,
			Version: es.Version, BytesOnDisk: es.BytesOnDisk,
			Desired: es.Enabled, Instances: es.Instances,
			LastError: es.LastError, UpdatedAt: now,
		})
	}
	if err := s.UpsertAgentEnginesBatch(engines); err != nil {
		log.Printf("[mesh] engine status batch: %v", err)
	}
	procs := make([]store.TunnelProcNow, 0, len(rep.Proc))
	for _, tp := range sanitizeReportProc(rep, allowed) {
		tid := allowed[tp.TunnelID]
		if tid == 0 {
			continue
		}
		procs = append(procs, store.TunnelProcNow{
			TunnelID: tid, AgentID: agentID, CPUPct: tp.CPUPercent,
			RSSBytes: tp.RSSBytes, Processes: int(tp.Processes), At: now,
		})
	}
	if err := s.UpsertTunnelProcBatch(procs); err != nil {
		log.Printf("[mesh] proc batch: %v", err)
	}
}

// recordTunnelEvents writes a tunnel_events row on every observed state
// flip (healthy→down and back via Up) and on every reported per-tunnel
// error. Bounded by the 7-day prune above.
func recordTunnelEvents(s *store.Store, agentID string, rep *protocol.StateReportV2, now int64) {
	prev := meshRuntimeSnapshot(agentID)
	wasUp := map[string]bool{}
	wasErr := map[string]string{}
	for _, tr := range prev {
		wasUp[tr.TunnelID] = tr.Up
		if tr.LastError != "" {
			wasErr[tr.TunnelID] = tr.LastError
		}
	}
	for _, tr := range rep.Tunnels {
		tid, err := tunnelIDByKey(s, tr.TunnelID)
		if err != nil {
			continue
		}
		if was, seen := wasUp[tr.TunnelID]; seen && was != tr.Up {
			from, to := "down", "up"
			if was {
				from, to = "up", "down"
			}
			_ = s.AddTunnelEvent(store.TunnelEvent{
				TunnelID: tid, At: now, Kind: "state", FromState: from, ToState: to,
			})
		}
		if tr.LastError != "" && tr.LastError != wasErr[tr.TunnelID] {
			_ = s.AddTunnelEvent(store.TunnelEvent{
				TunnelID: tid, At: now, Kind: "error", Code: "E-ENGINE-UNKNOWN-ERROR",
				Text: redactErrorText(tr.LastError),
			})
		}
	}
}

// tunnelIDByKey resolves a tunnel_key to the store id (one query; the
// heartbeat path hits it once per tunnel per ~30 s).
func tunnelIDByKey(s *store.Store, key string) (int64, error) {
	t, err := s.GetTunnelByKey(key)
	if err != nil {
		return 0, err
	}
	return t.ID, nil
}
