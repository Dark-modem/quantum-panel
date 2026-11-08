package panel

// meshapi.go — the mesh management-plane REST API. Every handler follows
// the panel's existing conventions: requireAuth on reads,
// requireAuth(requireCSRF(...)) on mutations (requireCSRF also records the
// security-audit line), writeJSON for results, writeAPIError for failures.
// Domain events are additionally recorded in the store's audit table
// (meshAudit) WITHOUT payloads that could carry secrets or profiles.
//
// Endpoint map (all under the panel base path):
//
//	GET   /api/mesh/overview
//	GET   /api/mesh/datacenters            POST /api/mesh/datacenters
//	PATCH /api/mesh/datacenters/{id}       DELETE /api/mesh/datacenters/{id}
//	GET   /api/mesh/servers                POST /api/mesh/servers
//	PATCH /api/mesh/servers/{id}           DELETE /api/mesh/servers/{id}
//	GET   /api/mesh/topologies             POST /api/mesh/topologies/generate
//	GET   /api/mesh/topologies/{id}        PATCH /api/mesh/topologies/{id}
//	DELETE /api/mesh/topologies/{id}
//	GET   /api/mesh/topologies/{id}/weights  POST (set one weight)
//	POST  /api/mesh/tunnels                PATCH /api/mesh/tunnels/{id}
//	DELETE /api/mesh/tunnels/{id}
//	GET   /api/mesh/forwards               POST /api/mesh/forwards
//	PATCH /api/mesh/forwards/{id}          DELETE /api/mesh/forwards/{id}
//	GET   /api/mesh/pasarguard             POST /api/mesh/pasarguard
//	PATCH /api/mesh/pasarguard/{id}        DELETE /api/mesh/pasarguard/{id}
//	GET   /api/mesh/usage                  (tunnel/server/datacenter/topology scopes)
//	GET   /api/mesh/overhead
//	GET   /api/mesh/coefficient            (suggested per Iran DC × engine)
//	GET   /api/mesh/agents                 (store view incl. v2 session state)
//	POST  /api/mesh/agents/{id}/link       (attach to a configured server)
//	POST  /api/mesh/agents/{id}/engine-catalog (record what the agent reports)
//	POST  /api/mesh/agents/{id}/push       (compile + deliver DesiredV2)
//	POST  /api/mesh/agents/{id}/profile/hardening  (apply; safe-apply armed)
//	POST  /api/mesh/agents/{id}/profile/netopt     (apply; safe-apply armed)
//	POST  /api/mesh/agents/{id}/profile/confirm    (keep; disarm the timer)
//	POST  /api/mesh/agents/{id}/profile/rollback   (undo now)
//	GET   /api/mesh/profiles/pending               (what awaits confirmation)

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

const meshMaxBody = 512 << 10 // 512 KiB: profiles and engine params stay small

func meshStoreOr503(w http.ResponseWriter, r *http.Request) *store.Store {
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		writeAPIError(w, r, "E-SYS-01", "mesh store unavailable: "+err.Error())
		return nil
	}
	return s
}

func meshReadBody(w http.ResponseWriter, r *http.Request, into any) bool {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, meshMaxBody))
	if err != nil {
		writeAPIError(w, r, "E-IN-01", "cannot read body (max "+strconv.Itoa(meshMaxBody)+" bytes)")
		return false
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		writeAPIError(w, r, "E-IN-02", "empty JSON body")
		return false
	}
	if err := json.Unmarshal(body, into); err != nil {
		writeAPIError(w, r, "E-IN-03", "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func meshPathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, false
	}
	return id, true
}

func meshQueryInt64(r *http.Request, key string) (int64, bool) {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// meshErr maps store errors onto HTTP responses.
func meshErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeAPIError(w, r, "E-IN-04", "no such record")
	case err != nil && strings.Contains(err.Error(), "referenced by other records"):
		writeAPIError(w, r, "E-IN-05", err.Error())
	default:
		writeAPIError(w, r, "E-SYS-01", meshSafeDetail(err))
	}
}

// meshSafeDetail maps raw store/driver error text to fixed strings —
// sqlite internals (table/column names, driver markers, lock states)
// must not reach the client (SECURITY-AUDIT-PANEL L1). Operator-actionable
// messages (wrapFK's "referenced by other records", ErrNotFound) keep
// their own branches above.
func meshSafeDetail(err error) string {
	if err == nil {
		return "request failed"
	}
	msg := err.Error()
	for _, marker := range []string{"SQLITE_", "sqlite:", "no such table", "no such column",
		"no such function", "UNIQUE constraint", "NOT NULL constraint", "database is locked",
		"modernc.org", "malformed"} {
		if strings.Contains(strings.ToLower(msg), strings.ToLower(marker)) {
			return "store operation failed (details in the panel error journal)"
		}
	}
	return msg
}

func registerMeshRoutes(mux *http.ServeMux, base string) {
	p := base + "/api/mesh"
	registerPasarGuardRoutes(mux, base)
	// reads
	mux.HandleFunc("GET "+p+"/overview", requireAuth(handleMeshOverview))
	mux.HandleFunc("GET "+p+"/datacenters", requireAuth(handleMeshDCList))
	mux.HandleFunc("GET "+p+"/servers", requireAuth(handleMeshServerList))
	mux.HandleFunc("GET "+p+"/topologies", requireAuth(handleMeshTopoList))
	mux.HandleFunc("GET "+p+"/topologies/{id}", requireAuth(handleMeshTopoGet))
	mux.HandleFunc("GET "+p+"/topologies/{id}/weights", requireAuth(handleMeshWeightsList))
	mux.HandleFunc("GET "+p+"/forwards", requireAuth(handleMeshForwardsList))
	mux.HandleFunc("GET "+p+"/pasarguard", requireAuth(handleMeshPGList))
	mux.HandleFunc("GET "+p+"/usage", requireAuth(handleMeshUsage))
	mux.HandleFunc("GET "+p+"/overhead", requireAuth(handleMeshOverhead))
	mux.HandleFunc("GET "+p+"/coefficient", requireAuth(handleMeshCoefficient))
	mux.HandleFunc("GET "+p+"/agents", requireAuth(handleMeshAgentsList))
	mux.HandleFunc("GET "+p+"/profiles/pending", requireAuth(handleMeshProfilesPending))
	// mutations
	mux.HandleFunc("POST "+p+"/datacenters", requireAuth(requireCSRF(handleMeshDCCreate)))
	mux.HandleFunc("PATCH "+p+"/datacenters/{id}", requireAuth(requireCSRF(handleMeshDCPatch)))
	mux.HandleFunc("DELETE "+p+"/datacenters/{id}", requireAuth(requireCSRF(handleMeshDCDelete)))
	mux.HandleFunc("POST "+p+"/servers", requireAuth(requireCSRF(handleMeshServerCreate)))
	mux.HandleFunc("PATCH "+p+"/servers/{id}", requireAuth(requireCSRF(handleMeshServerPatch)))
	mux.HandleFunc("DELETE "+p+"/servers/{id}", requireAuth(requireCSRF(handleMeshServerDelete)))
	mux.HandleFunc("POST "+p+"/topologies/generate", requireAuth(requireCSRF(handleMeshTopoGenerate)))
	mux.HandleFunc("PATCH "+p+"/topologies/{id}", requireAuth(requireCSRF(handleMeshTopoPatch)))
	mux.HandleFunc("DELETE "+p+"/topologies/{id}", requireAuth(requireCSRF(handleMeshTopoDelete)))
	mux.HandleFunc("POST "+p+"/topologies/{id}/weights", requireAuth(requireCSRF(handleMeshWeightSet)))
	mux.HandleFunc("POST "+p+"/tunnels", requireAuth(requireCSRF(handleMeshTunnelCreate)))
	mux.HandleFunc("PATCH "+p+"/tunnels/{id}", requireAuth(requireCSRF(handleMeshTunnelPatch)))
	mux.HandleFunc("DELETE "+p+"/tunnels/{id}", requireAuth(requireCSRF(handleMeshTunnelDelete)))
	mux.HandleFunc("POST "+p+"/forwards", requireAuth(requireCSRF(handleMeshForwardCreate)))
	mux.HandleFunc("PATCH "+p+"/forwards/{id}", requireAuth(requireCSRF(handleMeshForwardPatch)))
	mux.HandleFunc("DELETE "+p+"/forwards/{id}", requireAuth(requireCSRF(handleMeshForwardDelete)))
	mux.HandleFunc("POST "+p+"/pasarguard", requireAuth(requireCSRF(handleMeshPGCreate)))
	mux.HandleFunc("PATCH "+p+"/pasarguard/{id}", requireAuth(requireCSRF(handleMeshPGPatch)))
	mux.HandleFunc("DELETE "+p+"/pasarguard/{id}", requireAuth(requireCSRF(handleMeshPGDelete)))
	mux.HandleFunc("POST "+p+"/agents/{id}/link", requireAuth(requireCSRF(handleMeshAgentLink)))
	mux.HandleFunc("POST "+p+"/agents/{id}/public-ip", requireAuth(requireCSRF(handleMeshAgentPublicIP)))
	mux.HandleFunc("POST "+p+"/agents/{id}/engine-catalog", requireAuth(requireCSRF(handleMeshAgentCatalog)))
	mux.HandleFunc("POST "+p+"/agents/{id}/push", requireAuth(requireCSRF(handleMeshAgentPush)))
	mux.HandleFunc("POST "+p+"/agents/{id}/profile/hardening", requireAuth(requireCSRF(handleMeshProfileHardening)))
	mux.HandleFunc("POST "+p+"/agents/{id}/profile/netopt", requireAuth(requireCSRF(handleMeshProfileNetopt)))
	mux.HandleFunc("POST "+p+"/agents/{id}/profile/confirm", requireAuth(requireCSRF(handleMeshProfileConfirm)))
	mux.HandleFunc("POST "+p+"/agents/{id}/profile/rollback", requireAuth(requireCSRF(handleMeshProfileRollback)))
}

// ---- overview ------------------------------------------------------------

func handleMeshOverview(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	dcs, err := s.ListDatacenters()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	servers, err := s.ListServers()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	topos, err := s.ListTopologies()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	agents, err := s.ListAgents()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	type topoView struct {
		store.Topology
		Members     []int64        `json:"members"`
		TunnelCount int            `json:"tunnel_count"`
		Tunnels     []store.Tunnel `json:"tunnels,omitempty"`
	}
	views := make([]topoView, 0, len(topos))
	for _, tp := range topos {
		members, _ := s.TopologyMembers(tp.ID)
		tunnels, _ := s.ListTunnels(tp.ID)
		views = append(views, topoView{Topology: tp, Members: members, TunnelCount: len(tunnels)})
	}
	writeJSON(w, map[string]any{
		"datacenters": nonNil(dcs), "servers": nonNil(servers),
		"topologies": views, "agents": nonNil(agents),
	})
}

func nonNil[T any](v []T) []T {
	if v == nil {
		return []T{}
	}
	return v
}

// ---- datacenters ----------------------------------------------------------

func handleMeshDCList(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	dcs, err := s.ListDatacenters()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"datacenters": nonNil(dcs)})
}

func handleMeshDCCreate(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body struct {
		Name             string  `json:"name"`
		Kind             string  `json:"kind"`
		PriceCoefficient float64 `json:"price_coefficient"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if body.Kind != "iran" && body.Kind != "foreign" {
		writeAPIError(w, r, "E-IN-06", "kind must be iran or foreign")
		return
	}
	if body.PriceCoefficient == 0 {
		body.PriceCoefficient = 1.0
	}
	id, err := s.UpsertDatacenter(store.Datacenter{
		Kind: body.Kind, Name: strings.TrimSpace(body.Name),
		PriceCoefficient: body.PriceCoefficient, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.datacenter.upsert", fmt.Sprintf(`{"id":%d,"kind":%q}`, id, body.Kind))
	writeJSON(w, map[string]any{"id": id})
}

func handleMeshDCPatch(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	var body struct {
		PriceCoefficient *float64 `json:"price_coefficient"`
	}
	if !meshReadBody(w, r, &body) || body.PriceCoefficient == nil {
		writeAPIError(w, r, "E-IN-02", "price_coefficient is required")
		return
	}
	if err := s.SetDatacenterPriceCoefficient(id, *body.PriceCoefficient); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.datacenter.price", fmt.Sprintf(`{"id":%d,"price_coefficient":%g}`, id, *body.PriceCoefficient))
	writeJSON(w, map[string]any{"ok": true})
}

func handleMeshDCDelete(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	if err := s.DeleteDatacenter(id); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.datacenter.delete", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

// meshActor is the authenticated user for audit lines. The panel has a
// single admin account; sessions only prove possession of it.
func meshActor(r *http.Request) string {
	return cfg.Username
}

// ---- servers ---------------------------------------------------------------

func handleMeshServerList(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	servers, err := s.ListServers()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"servers": nonNil(servers)})
}

func handleMeshServerCreate(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body store.Server
	if !meshReadBody(w, r, &body) {
		return
	}
	if body.Role != "iran" && body.Role != "foreign" {
		writeAPIError(w, r, "E-IN-06", "role must be iran or foreign")
		return
	}
	if len(body.Address) > 255 {
		writeAPIError(w, r, "E-IN-06", "address too long")
		return
	}
	id, err := s.UpsertServer(store.Server{
		Name: strings.TrimSpace(body.Name), Role: body.Role,
		DatacenterID: body.DatacenterID, Address: body.Address, CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.server.upsert", fmt.Sprintf(`{"id":%d,"role":%q}`, id, body.Role))
	writeJSON(w, map[string]any{"id": id})
}

func handleMeshServerPatch(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	sv, err := s.GetServer(id)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	var body struct {
		Name         *string `json:"name"`
		Address      *string `json:"address"`
		DatacenterID *int64  `json:"datacenter_id"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if body.Name != nil {
		sv.Name = strings.TrimSpace(*body.Name)
	}
	if body.Address != nil {
		if len(*body.Address) > 255 {
			writeAPIError(w, r, "E-IN-06", "address too long")
			return
		}
		sv.Address = *body.Address
	}
	if body.DatacenterID != nil {
		sv.DatacenterID = *body.DatacenterID
	}
	if err := s.UpdateServer(sv); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.server.update", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

func handleMeshServerDelete(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	if err := s.DeleteServer(id); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.server.delete", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

// ---- topologies -------------------------------------------------------------

func handleMeshTopoList(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	topos, err := s.ListTopologies()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"topologies": nonNil(topos)})
}

func handleMeshTopoGet(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	tp, err := s.GetTopology(id)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	members, _ := s.TopologyMembers(id)
	tunnels, err := s.ListTunnels(id)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	weights, _ := s.ListWeights(id)
	forwards, _ := s.ListForwardRules(id)
	writeJSON(w, map[string]any{
		"topology": tp, "members": nonNil(members),
		"tunnels": nonNil(tunnels), "weights": nonNil(weights), "forwards": nonNil(forwards),
	})
}

func handleMeshTopoGenerate(w http.ResponseWriter, r *http.Request) {
	var req TopoGenRequest
	if !meshReadBody(w, r, &req) {
		return
	}
	res, err := GenerateTopology(req)
	if err != nil {
		writeAPIError(w, r, "E-IN-06", err.Error())
		return
	}
	writeJSON(w, res)
}

func handleMeshTopoPatch(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	tp, err := s.GetTopology(id)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	var body struct {
		Enabled     *bool            `json:"enabled"`
		HealthCheck *json.RawMessage `json:"health_check"`
		Failover    *json.RawMessage `json:"failover"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if body.Enabled != nil {
		tp.Enabled = *body.Enabled
	}
	if _, err := s.UpsertTopology(store.Topology{Name: tp.Name, Enabled: tp.Enabled, CreatedAt: tp.CreatedAt}); err != nil {
		meshErr(w, r, err)
		return
	}
	if body.HealthCheck != nil || body.Failover != nil {
		hc, fo := tp.HealthCheck, tp.Failover
		if body.HealthCheck != nil {
			hc = string(*body.HealthCheck)
		}
		if body.Failover != nil {
			fo = string(*body.Failover)
		}
		if err := s.SetTopologyBalancing(id, hc, fo); err != nil {
			meshErr(w, r, err)
			return
		}
	}
	meshAudit(meshActor(r), "mesh.topology.update", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

func handleMeshTopoDelete(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	if err := s.DeleteTopology(id); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.topology.delete", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

// ---- weights ------------------------------------------------------------------

func handleMeshWeightsList(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	weights, err := s.ListWeights(id)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"weights": nonNil(weights)})
}

func handleMeshWeightSet(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	topoID, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	var body struct {
		LocalServerID  *int64 `json:"local_server_id"`
		RemoteServerID *int64 `json:"remote_server_id"`
		TunnelID       *int64 `json:"tunnel_id"`
		Weight         *int   `json:"weight"`
	}
	if !meshReadBody(w, r, &body) || body.Weight == nil {
		writeAPIError(w, r, "E-IN-02", "weight is required")
		return
	}
	if *body.Weight < 0 || *body.Weight > 1000 {
		writeAPIError(w, r, "E-IN-06", "weight must be 0-1000 (0 = excluded from balancing)")
		return
	}
	// The tunnel (when given) must belong to this topology.
	if body.TunnelID != nil {
		t, err := s.GetTunnel(*body.TunnelID)
		if err != nil {
			meshErr(w, r, err)
			return
		}
		if t.TopologyID != topoID {
			writeAPIError(w, r, "E-IN-06", "tunnel belongs to another topology")
			return
		}
	}
	var localID, remoteID, tunnelID int64
	if body.LocalServerID != nil {
		localID = *body.LocalServerID
	}
	if body.RemoteServerID != nil {
		remoteID = *body.RemoteServerID
	}
	if body.TunnelID != nil {
		tunnelID = *body.TunnelID
	}
	if err := s.SetWeightPolicy(topoID, localID, remoteID, tunnelID, *body.Weight); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.weight.set",
		fmt.Sprintf(`{"topology_id":%d,"tunnel_id":%d,"weight":%d}`, topoID, tunnelID, *body.Weight))
	writeJSON(w, map[string]any{"ok": true})
}

// ---- tunnels -------------------------------------------------------------------

func handleMeshTunnelCreate(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body struct {
		TopologyID     int64           `json:"topology_id"`
		LocalServerID  int64           `json:"local_server_id"`
		RemoteServerID int64           `json:"remote_server_id"`
		TunnelKey      string          `json:"tunnel_key"`
		Engine         string          `json:"engine"`
		EngineParams   json.RawMessage `json:"engine_params"`
		Enabled        bool            `json:"enabled"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if _, err := s.GetTopology(body.TopologyID); err != nil {
		meshErr(w, r, err)
		return
	}
	if _, err := s.GetServer(body.LocalServerID); err != nil {
		meshErr(w, r, fmt.Errorf("local server %d: %v", body.LocalServerID, err))
		return
	}
	if _, err := s.GetServer(body.RemoteServerID); err != nil {
		meshErr(w, r, fmt.Errorf("remote server %d: %v", body.RemoteServerID, err))
		return
	}
	key := strings.TrimSpace(body.TunnelKey)
	if len(key) == 0 || len(key) > 128 || tunnelKeyRe.MatchString(key) == false {
		writeAPIError(w, r, "E-IN-06", "tunnel_key must be 1-128 chars of [a-z0-9-_.]")
		return
	}
	params := "{}"
	if len(body.EngineParams) > 0 {
		if !json.Valid(body.EngineParams) || len(body.EngineParams) > 16<<10 {
			writeAPIError(w, r, "E-IN-06", "engine_params must be valid JSON <= 16 KiB")
			return
		}
		params = string(body.EngineParams)
	}
	if warn, err := validateEngineAgainstCatalog(s, body.LocalServerID, body.Engine); err != nil {
		writeAPIError(w, r, "E-IN-06", err.Error())
		return
	} else if warn != "" {
		meshAudit(meshActor(r), "mesh.tunnel.engine_warning", fmt.Sprintf(`{"engine":%q,"warn":%q}`, body.Engine, warn))
	}
	id, err := s.UpsertTunnel(store.Tunnel{
		TunnelKey: key, TopologyID: body.TopologyID,
		LocalServerID: body.LocalServerID, RemoteServerID: body.RemoteServerID,
		Engine: body.Engine, EngineParams: params, Enabled: body.Enabled,
	})
	if err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.tunnel.create", fmt.Sprintf(`{"id":%d,"key":%q,"engine":%q}`, id, key, body.Engine))
	writeJSON(w, map[string]any{"id": id})
}

var tunnelKeyRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-_.]{0,127}$`)

func handleMeshTunnelPatch(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	t, err := s.GetTunnel(id)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	var body struct {
		Engine       *string          `json:"engine"`
		EngineParams *json.RawMessage `json:"engine_params"`
		Enabled      *bool            `json:"enabled"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if body.Engine != nil {
		if warn, err := validateEngineAgainstCatalog(s, t.LocalServerID, *body.Engine); err != nil {
			writeAPIError(w, r, "E-IN-06", err.Error())
			return
		} else if warn != "" {
			meshAudit(meshActor(r), "mesh.tunnel.engine_warning", fmt.Sprintf(`{"engine":%q,"warn":%q}`, *body.Engine, warn))
		}
		t.Engine = *body.Engine
	}
	if body.EngineParams != nil {
		if !json.Valid(*body.EngineParams) || len(*body.EngineParams) > 16<<10 {
			writeAPIError(w, r, "E-IN-06", "engine_params must be valid JSON <= 16 KiB")
			return
		}
		t.EngineParams = string(*body.EngineParams)
	}
	if body.Enabled != nil {
		t.Enabled = *body.Enabled
	}
	if err := s.UpdateTunnel(t); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.tunnel.update", fmt.Sprintf(`{"id":%d,"engine":%q,"enabled":%t}`, id, t.Engine, t.Enabled))
	writeJSON(w, map[string]any{"ok": true})
}

func handleMeshTunnelDelete(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	if err := s.DeleteTunnel(id); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.tunnel.delete", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

// ---- forwards ------------------------------------------------------------------

func handleMeshForwardsList(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	topoID, _ := meshQueryInt64(r, "topology_id")
	forwards, err := s.ListForwardRules(topoID)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"forwards": nonNil(forwards)})
}

func handleMeshForwardCreate(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body store.ForwardRule
	if !meshReadBody(w, r, &body) {
		return
	}
	if _, err := s.GetServer(body.LocalServerID); err != nil {
		meshErr(w, r, fmt.Errorf("local server %d: %v", body.LocalServerID, err))
		return
	}
	id, err := s.CreateForwardRule(body)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.forward.create",
		fmt.Sprintf(`{"id":%d,"listen_port":%d,"proto":%q}`, id, body.ListenPort, body.Proto))
	writeJSON(w, map[string]any{"id": id})
}

func handleMeshForwardPatch(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	f, err := s.GetForwardRule(id)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	var body struct {
		Enabled *bool   `json:"enabled"`
		Target  *string `json:"target"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if body.Enabled != nil {
		f.Enabled = *body.Enabled
	}
	if body.Target != nil {
		if len(*body.Target) > 255 {
			writeAPIError(w, r, "E-IN-06", "target too long")
			return
		}
		f.Target = *body.Target
	}
	if err := s.UpdateForwardRule(f); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.forward.update", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

func handleMeshForwardDelete(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	if err := s.DeleteForwardRule(id); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.forward.delete", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

// ---- pasarguard nodes (record only this phase) ----------------------------------

func handleMeshPGList(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	nodes, err := s.ListPasarguardNodes()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"pasarguard_nodes": nonNil(nodes)})
}

func handleMeshPGCreate(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body store.PasarguardNode
	if !meshReadBody(w, r, &body) {
		return
	}
	if _, err := s.GetServer(body.ServerID); err != nil {
		meshErr(w, r, fmt.Errorf("server %d: %v", body.ServerID, err))
		return
	}
	if _, err := s.GetDatacenter(body.DatacenterID); err != nil {
		meshErr(w, r, fmt.Errorf("datacenter %d: %v", body.DatacenterID, err))
		return
	}
	if len(body.Name) > 200 {
		writeAPIError(w, r, "E-IN-06", "name too long")
		return
	}
	id, err := s.UpsertPasarguardNode(body)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.pasarguard.upsert", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"id": id})
}

func handleMeshPGPatch(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	var body struct {
		Coefficient *float64 `json:"coefficient"`
	}
	if !meshReadBody(w, r, &body) || body.Coefficient == nil {
		writeAPIError(w, r, "E-IN-02", "coefficient is required")
		return
	}
	if err := s.SetPasarguardCoefficient(id, *body.Coefficient); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.pasarguard.coefficient", fmt.Sprintf(`{"id":%d,"coefficient":%g}`, id, *body.Coefficient))
	writeJSON(w, map[string]any{"ok": true})
}

func handleMeshPGDelete(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	id, ok := meshPathID(r)
	if !ok {
		writeAPIError(w, r, "E-IN-04", "")
		return
	}
	if err := s.DeletePasarguardNode(id); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "mesh.pasarguard.delete", fmt.Sprintf(`{"id":%d}`, id))
	writeJSON(w, map[string]any{"ok": true})
}

// serveMeshPage renders the mesh management page (session-authed like the
// rest of the UI; the page itself is static, all data comes from the API).
// serveWizardPage serves the guided setup wizard (phase 7) at
// <base>/mesh-wizard — the same embedded-asset pattern as the mesh page.
func serveWizardPage(w http.ResponseWriter, r *http.Request) {
	serveAsset("wizard.html", "text/html; charset=utf-8").ServeHTTP(w, r)
}

func serveMeshPage(w http.ResponseWriter, r *http.Request) {
	serveAsset("mesh.html", "text/html; charset=utf-8").ServeHTTP(w, r)
}
