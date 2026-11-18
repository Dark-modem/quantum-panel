package panel

// uirows.go — the SHARED UI payload builders (REFACTOR-S2, refactor plan
// §6.2): one per-tunnel row builder and one per-side identity resolver,
// each the SINGLE code path for the payloads that used to carry duplicate
// inline copies — the agent topologies rows (uiapi_agents.go, contract
// §2.3), the topology detail rows (uiapi_topo.go, §2.8) and the legacy
// /api/topologies per-side identity (topology_identity.go, §2.19). JSON
// shapes byte-identical: uirows_dedup_test.go byte-compares every moved
// builder against frozen pre-refactor copies, uiapi_test.go and
// topology_dashboard_contract_test.go guard the served contracts.

import (
	"net"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// uiTunnelVerdict names one tunnel's live state from the verdict map: a
// disabled tunnel is "disabled" regardless of runtime, an enabled tunnel
// without a fresh verdict is "unknown". The three identical inline blocks
// this replaced (uiTunnelRow, the agent detail rows, the tunnel detail)
// can no longer drift apart.
func uiTunnelVerdict(t store.Tunnel, verdicts map[string]string) string {
	verdict := "unknown"
	if !t.Enabled {
		verdict = "disabled"
	} else if v, ok := verdicts[t.TunnelKey]; ok {
		verdict = v
	}
	return verdict
}

// uiAgentCountries maps serverID → the country of the agent enrolled
// against it (the servers table carries no country; the agent enrolled
// against it does). The identical inline maps this replaced — the agent
// detail's destination flags and the agent topologies side summary — stay
// one code path; an unlinked agent contributes nothing.
func (h uiStoreHandle) uiAgentCountries() map[int64]string {
	countries := map[int64]string{}
	if ags, err := h.ListAgents(); err == nil {
		for _, a := range ags {
			if a.ServerID != 0 {
				countries[a.ServerID] = a.Country
			}
		}
	}
	return countries
}

// uiLatencyMap: one locked pass over the runtime for every fresh
// tunnel's latency (the per-row scans were an N+1 hotspot in the
// phase-12 soak).
func uiLatencyMap() map[string]float64 {
	out := map[string]float64{}
	now := time.Now().Unix()
	meshRuntimeMu.Lock()
	for agentID, rts := range meshRuntimeBy {
		if now-meshRuntimeSeen[agentID] > 90 {
			continue
		}
		for _, tr := range rts {
			if tr.LatencyMs > 0 {
				out[tr.TunnelID] = tr.LatencyMs
			}
		}
	}
	meshRuntimeMu.Unlock()
	return out
}

// uiTunnelRow renders one row from PRECOMPUTED shares (pcts) and the
// shared latency map — per-row recomputation was the topology detail's
// N+1 hotspot (phase-12 soak). The ONE per-tunnel summary row: the
// topology detail and the §2.3 agent topologies both render through it,
// so the two views cannot drift.
func (h uiStoreHandle) uiTunnelRow(s *store.Store, topo store.Topology, t store.Tunnel,
	verdicts map[string]string, now int64, pcts map[int64]float64, lat map[string]float64) map[string]any {
	verdict := uiTunnelVerdict(t, verdicts)
	w, _ := h.WeightFor(topo.ID, t.LocalServerID, t.RemoteServerID, t.ID)
	usage, _, err := h.RangeUsageDetailed([]int64{t.ID}, now-now%86400, now)
	if err != nil {
		usage = store.Usage{}
	}
	var latency any
	if l, ok := lat[t.TunnelKey]; ok {
		latency = l
	}
	row := map[string]any{
		"id": t.ID, "tunnel_key": t.TunnelKey, "engine": t.Engine, "enabled": t.Enabled,
		"local_server":  h.uiServerRef(t.LocalServerID),
		"remote_server": h.uiServerRef(t.RemoteServerID),
		"state":         verdict,
		"weight":        w,
		"traffic_today": map[string]any{
			"wire_rx_bytes":    usage.WireRx,
			"wire_tx_bytes":    usage.WireTx,
			"payload_rx_bytes": usage.PayloadRx,
			"payload_tx_bytes": usage.PayloadTx,
		},
		"latency_ms": latency,
	}
	// Only connected tunnels carry weight — a dropped tunnel shows no
	// resolved share and the survivors re-normalize without it.
	if p := pcts[t.ID]; p > 0 {
		row["resolved_percent"] = p
	}
	return row
}

// ---- §2.19 per-side identity (folded in from topology_identity.go, S2) ----

// topologySideServer is one matched mesh server of a topology side.
type topologySideServer struct {
	Name         string `json:"name"`
	Datacenter   string `json:"datacenter"`
	DatacenterEN string `json:"datacenter_en"`
	CC           string `json:"cc"`
	IP           string `json:"ip"`
}

// topologySideInfo is the per-side identity block of a topology row
// (contract §2.19). Empty strings + an empty servers list mean "nothing
// resolved" — honest absence the UI already renders as country labels.
type topologySideInfo struct {
	ServerName   string               `json:"server_name"`
	Datacenter   string               `json:"datacenter"`
	DatacenterEN string               `json:"datacenter_en"`
	CC           string               `json:"cc"`
	Servers      []topologySideServer `json:"servers"`
}

// topologyIdentityView carries the mesh-store rows the resolution reads,
// loaded ONCE per payload so N topologies cost three queries, not 3N.
type topologyIdentityView struct {
	servers    []store.Server
	dcs        map[int64]store.Datacenter
	serverByID map[int64]store.Server
	// agentIP maps a linked agent's reported public IP to its server id.
	agentIP map[string]int64
}

// meshStorePeek returns the OPEN mesh store WITHOUT opening one. The
// legacy topology handlers must not create quantum.db as a side effect
// (their file-purity contract: the metadata surface writes only
// topologies.json), and a panel that never used the mesh API has no store
// to resolve from — identity stays empty and the invariant passes until
// the store exists.
func meshStorePeek() *store.Store {
	meshStoreMu.Lock()
	defer meshStoreMu.Unlock()
	return meshStore
}

// loadTopologyIdentityView reads the mesh store once (via peek: an
// already-open store only). Any failure keeps the view empty: identity
// fields stay empty and the invariant passes.
func loadTopologyIdentityView() *topologyIdentityView {
	v := &topologyIdentityView{
		dcs:        map[int64]store.Datacenter{},
		serverByID: map[int64]store.Server{},
		agentIP:    map[string]int64{},
	}
	s := meshStorePeek()
	if s == nil {
		return v
	}
	servers, err := s.ListServers()
	if err != nil {
		return v
	}
	v.servers = servers
	for _, sv := range servers {
		v.serverByID[sv.ID] = sv
		if sv.DatacenterID != 0 {
			if dc, err := s.GetDatacenter(sv.DatacenterID); err == nil {
				v.dcs[sv.DatacenterID] = dc
			}
		}
	}
	agents, err := s.ListAgents()
	if err != nil {
		return v
	}
	for _, a := range agents {
		if a.ServerID == 0 || a.PublicIP == "" {
			continue
		}
		if ip := net.ParseIP(strings.TrimSpace(a.PublicIP)); ip != nil {
			v.agentIP[ip.String()] = a.ServerID
		}
	}
	return v
}

// serverForIP resolves one endpoint IP to a mesh server id: the server's
// own address first, then the linked agents' reported public IP. 0 = no
// match (the endpoint stays unchecked/anonymous).
func (v *topologyIdentityView) serverForIP(ipStr string) int64 {
	if v == nil {
		return 0
	}
	for _, sv := range v.servers {
		if ip := net.ParseIP(strings.TrimSpace(sv.Address)); ip != nil && ip.String() == ipStr {
			return sv.ID
		}
	}
	return v.agentIP[ipStr]
}

// topologySideIdentity resolves a topology's per-side identity: every
// attached tunnel endpoint IP is matched against the mesh store and the
// matched server's ROLE decides the side bucket (iran/foreign) — a
// server's role is authoritative, so the topology's own declared role
// cannot cross-contaminate the sides. The first matched server per side
// (tunnel_ids order) fills the summary fields; servers[] lists every
// distinct match.
func topologySideIdentity(t Topology, inventory []topologyTunnel, v *topologyIdentityView) (topologySideInfo, topologySideInfo) {
	iran := topologySideInfo{CC: "IR", Servers: []topologySideServer{}}
	foreign := topologySideInfo{CC: strings.ToUpper(strings.TrimSpace(t.Country)), Servers: []topologySideServer{}}
	if v == nil || len(v.servers) == 0 {
		return iran, foreign
	}
	byID := topologyTunnelByID(inventory)
	seen := map[int64]bool{}
	for _, id := range t.TunnelIDs {
		tt, ok := byID[id]
		if !ok {
			continue
		}
		local, remote := topologyTunnelEnds(tt)
		for _, ipStr := range append(append([]string{}, local...), remote...) {
			sid := v.serverForIP(ipStr)
			if sid == 0 || seen[sid] {
				continue
			}
			sv := v.serverByID[sid]
			side := &foreign
			if sv.Role == "iran" {
				side = &iran
			}
			seen[sid] = true
			entry := topologySideServer{Name: sv.Name, IP: ipStr, CC: side.CC}
			if dc, ok := v.dcs[sv.DatacenterID]; ok {
				entry.Datacenter, entry.DatacenterEN = dc.Name, dc.DatacenterEN
			}
			side.Servers = append(side.Servers, entry)
		}
	}
	for _, side := range []*topologySideInfo{&iran, &foreign} {
		if len(side.Servers) > 0 {
			side.ServerName = side.Servers[0].Name
			side.Datacenter = side.Servers[0].Datacenter
			side.DatacenterEN = side.Servers[0].DatacenterEN
		}
	}
	return iran, foreign
}
