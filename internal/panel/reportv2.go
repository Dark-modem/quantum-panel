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

const meshSampleMinGap = 5 * time.Second // do not persist identical-second samples
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
		for _, tr := range rep.Tunnels {
			tid, err := tunnelIDByKey(s, tr.TunnelID)
			if err != nil {
				continue // unknown tunnel id: the store only holds declared tunnels
			}
			_ = s.AddTrafficSample(store.TrafficSample{
				TunnelID:      tid,
				At:            now,
				WireRx:        tr.WireRx,
				WireTx:        tr.WireTx,
				PayloadRx:     tr.PayloadRx,
				PayloadTx:     tr.PayloadTx,
				CountersKnown: tr.CountersKnown,
			})
		}
		for _, es := range rep.Engines {
			if es.Engine == "" {
				continue
			}
			_ = s.ObserveEngineOverhead(es.Engine, es.WireRx, es.PayloadRx)
		}
	}

	meshRuntimeMu.Lock()
	meshRuntimeBy[agentID] = rep.Tunnels
	meshRuntimeSeen[agentID] = now
	meshRuntimeMu.Unlock()

	// Mirror the session facts into the store's agents table (the store has
	// no revoked concept; revocation lives in the legacy registry).
	if rep.AppliedRevision != "" {
		_, _ = s.DB().Exec(`UPDATE agents SET applied_revision = ? WHERE id = ?`, rep.AppliedRevision, agentID)
	}
	_, _ = s.DB().Exec(`UPDATE agents SET status = 'online', last_seen = ? WHERE id = ?`, now, agentID)

	// Retention: cheap and rare.
	if now%60 == 0 {
		if n, err := s.PruneTrafficSamples(now - 30*24*3600); err == nil && n > 0 {
			log.Printf("[mesh] pruned %d traffic samples", n)
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
