package panel

// topology_identity.go — phase 31 (UI-API-REQUESTS §10, contract §2.19):
// per-side server/datacenter identity for the legacy /api/topologies
// payload (§10.2), the per-tunnel weight + today-traffic enrichment
// (§10.1) and the one-DC-per-side invariant check for the manual attach
// path (§10.3). All read-only over the mesh store, the cluster config
// and the usage ledger: an unreadable store degrades to empty identities
// and a vacuously-true invariant, never a failed payload or a blocked
// attach — the backend only enforces what it can resolve.
import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

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

// topologyTunnelEnds returns the distinct normalized endpoint IPs of one
// inventory tunnel: its local end (this host) and its remote end(s).
func topologyTunnelEnds(t topologyTunnel) (local, remote []string) {
	remote = topologyEndpoints(t)
	if ip := net.ParseIP(strings.TrimSpace(t.localIP)); ip != nil {
		local = []string{ip.String()}
	}
	return local, remote
}

// topologyTunnelByID indexes the inventory for the per-topology walks.
func topologyTunnelByID(inventory []topologyTunnel) map[string]topologyTunnel {
	byID := make(map[string]topologyTunnel, len(inventory))
	for _, t := range inventory {
		byID[t.ID] = t
	}
	return byID
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

// topologyDCIssue names one side's datacenter mix for the structured
// E-TOPOLOGY-DC-CONFLICT envelope (contract §1/§2.19).
type topologyDCIssue struct {
	Side        string
	Datacenters []string
	Tunnels     []string
}

// topologyDCViolations checks the owner's one-DC-per-side rule over the
// candidate tunnel set of a topology: a topology spans exactly one Iran
// datacenter and one foreign datacenter (many servers within those DCs
// are fine). Endpoints resolving to no mesh server are unchecked.
func topologyDCViolations(t Topology, inventory []topologyTunnel, v *topologyIdentityView) []topologyDCIssue {
	if v == nil || len(v.servers) == 0 {
		return nil
	}
	byID := topologyTunnelByID(inventory)
	seenDC := map[string]map[int64]bool{}    // side -> dc ids
	perDC := map[string]map[int64][]string{} // side -> dc id -> tunnel ids
	for _, id := range t.TunnelIDs {
		tt, ok := byID[id]
		if !ok {
			continue
		}
		local, remote := topologyTunnelEnds(tt)
		for _, ipStr := range append(append([]string{}, local...), remote...) {
			sid := v.serverForIP(ipStr)
			if sid == 0 {
				continue
			}
			sv := v.serverByID[sid]
			if sv.Role != "iran" && sv.Role != "foreign" {
				continue
			}
			if seenDC[sv.Role] == nil {
				seenDC[sv.Role] = map[int64]bool{}
				perDC[sv.Role] = map[int64][]string{}
			}
			if !seenDC[sv.Role][sv.DatacenterID] {
				seenDC[sv.Role][sv.DatacenterID] = true
				perDC[sv.Role][sv.DatacenterID] = []string{}
			}
			dcTunnels := perDC[sv.Role][sv.DatacenterID]
			if len(dcTunnels) == 0 || dcTunnels[len(dcTunnels)-1] != id {
				perDC[sv.Role][sv.DatacenterID] = append(dcTunnels, id)
			}
		}
	}
	var issues []topologyDCIssue
	for _, side := range []string{"iran", "foreign"} {
		if len(seenDC[side]) < 2 {
			continue
		}
		issue := topologyDCIssue{Side: side, Datacenters: []string{}, Tunnels: []string{}}
		seenTunnel := map[string]bool{}
		for dcid, tunnels := range perDC[side] {
			name := fmt.Sprintf("dc:%d", dcid)
			if dc, ok := v.dcs[dcid]; ok && dc.Name != "" {
				name = dc.Name
			}
			issue.Datacenters = append(issue.Datacenters, name)
			for _, id := range tunnels {
				if !seenTunnel[id] {
					seenTunnel[id] = true
					issue.Tunnels = append(issue.Tunnels, id)
				}
			}
		}
		sort.Strings(issue.Datacenters)
		sort.Strings(issue.Tunnels)
		issues = append(issues, issue)
	}
	return issues
}

// topologyDCConflictWrite answers the structured rejection: the offending
// side (iran first — the issues are built in that order), the DC names
// and the offending tunnel ids ride details (allowlisted identifiers).
func topologyDCConflictWrite(w http.ResponseWriter, r *http.Request, t Topology, issues []topologyDCIssue) {
	issue := issues[0]
	uiErrWrite(w, r, "E-TOPOLOGY-DC-CONFLICT", map[string]any{
		"topology":    t.Name,
		"side":        issue.Side,
		"datacenters": issue.Datacenters,
		"tunnels":     issue.Tunnels,
	}, "")
}

// topologyTunnelWeights maps tunnel ids onto the cluster LB member weight
// (cluster.json pools: link 0 = "main", N = "peer:N"): the highest
// configured weight among pools that are not disabled. Engines without an
// LB concept (matrix/bagher/xray) and links in no pool stay 0 (unset).
func topologyTunnelWeights() map[string]int {
	out := map[string]int{}
	for _, pool := range loadClusterConfig().Pools {
		if pool.Disabled {
			continue
		}
		for _, m := range pool.Members {
			if m.Link < 0 {
				continue
			}
			id := "main"
			if m.Link != 0 {
				id = "peer:" + strconv.Itoa(m.Link)
			}
			if m.Weight > out[id] {
				out[id] = m.Weight
			}
		}
	}
	return out
}

// topologyTodayTraffic sums today's (UTC day) per-tunnel usage from the
// panel's usage ledger under ONE lock (the same source the usage report
// reads). A tunnel without a known current measurement has no entry —
// the caller serves today_traffic:null (honest unknown, never a fake 0).
func topologyTodayTraffic(ids []string, now time.Time) map[string]usageAmount {
	out := map[string]usageAmount{}
	if len(ids) == 0 {
		return out
	}
	window := time.Duration(now.Unix()-now.Unix()%86400) * time.Second
	trafficUsage.mu.Lock()
	defer trafficUsage.mu.Unlock()
	for _, id := range ids {
		key := topologyUsageKey(id)
		if !usageKnown(trafficUsage.links[key], now) {
			continue
		}
		sr, ok := trafficUsage.seriesLocked(key, now, window, 3600)
		if !ok {
			continue
		}
		out[id] = usageAmount{Rx: sr.Rx, Tx: sr.Tx, Total: sr.Rx + sr.Tx, Known: true}
	}
	return out
}

// topologyEnrichedPayload builds the GET /api/topologies response: the
// stored groupings + per-side identity (§10.2) and the tunnels inventory
// + weight/today_traffic (§10.1). The base rows ride a JSON round-trip of
// the existing structs so every field that used to be served keeps its
// exact name and shape (omitempty semantics included) — additive only.
func topologyEnrichedPayload(topologies []Topology, inventory []topologyTunnel) map[string]any {
	view := loadTopologyIdentityView()
	rows := make([]map[string]any, 0, len(topologies))
	ids := []string{}
	for _, t := range topologies {
		row := topologyJSONRoundTrip(t)
		iran, foreign := topologySideIdentity(t, inventory, view)
		row["iran"], row["foreign"] = iran, foreign
		rows = append(rows, row)
		ids = append(ids, t.TunnelIDs...)
	}
	weights := topologyTunnelWeights()
	today := topologyTodayTraffic(ids, time.Now())
	tunnels := make([]map[string]any, 0, len(inventory))
	for _, tt := range inventory {
		row := topologyJSONRoundTrip(tt)
		row["weight"] = weights[tt.ID]
		if u, ok := today[tt.ID]; ok {
			row["today_traffic"] = map[string]uint64{"wire_rx_bytes": u.Rx, "wire_tx_bytes": u.Tx}
		} else {
			row["today_traffic"] = nil
		}
		tunnels = append(tunnels, row)
	}
	return map[string]any{"topologies": rows, "tunnels": tunnels}
}

// topologyJSONRoundTrip marshals v through its json tags into a mutable
// map, preserving the previously-served field names and omitempty rules.
func topologyJSONRoundTrip(v any) map[string]any {
	row := map[string]any{}
	if b, err := json.Marshal(v); err == nil {
		_ = json.Unmarshal(b, &row)
	}
	return row
}
