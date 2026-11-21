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
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

const meshMaxBody = 512 << 10 // 512 KiB: profiles and engine params stay small

func meshStoreOr503(w http.ResponseWriter, r *http.Request) *store.Store {
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		// BACKEND-36 (PHASE35 leftover): the store-open error text carries
		// filesystem paths and driver internals — L1-gray on the wire.
		// B-AG2 discipline, the same split as meshErr below: the full raw
		// detail goes to the durable log (panel.log), while the client and
		// the client-served error journal keep only the masked string.
		log.Printf("[mesh] store open failed behind E-SYS-01 on %s %s (masked in the API response): %s",
			r.Method, r.URL.Path, capReportString(err.Error(), maxReportString))
		writeAPIError(w, r, "E-SYS-01", "mesh store unavailable (details in the panel error journal)")
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
		// B-AG2 (REFACTOR-A1 §3.2): the client AND this error journal see
		// only the masked string (L1 — sqlite internals must not reach the
		// client), so the underlying store error was persisted nowhere and
		// masked failures were undiagnosable. The raw detail goes to the
		// durable log (panel.log) here, capped like every reported string.
		if err != nil {
			log.Printf("[mesh] store error behind E-SYS-01 on %s %s (masked in the API response): %s",
				r.Method, r.URL.Path, capReportString(err.Error(), maxReportString))
		}
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
	mux.HandleFunc("GET "+p+"/meta", requireAuth(handleMeshMeta))
	mux.HandleFunc("GET "+p+"/weights/check", requireAuth(handleMeshWeightsCheck))
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
	mux.HandleFunc("POST "+p+"/topologies/dc", requireAuth(requireCSRF(handleMeshTopoCreateDC)))
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

// handleMeshMeta tells the mesh UI which shell features to offer (the
// collapsed legacy link) and its own version. Read-only, authenticated.
func handleMeshMeta(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"version":        panelVersion,
		"legacy_enabled": legacyOpsEnabled(),
	})
}

// handleMeshWeightsCheck answers ONE boolean for the dashboard's first-run
// checklist: does any non-default weight policy exist? (The checklist used
// to fetch /topologies/{id}/weights for EVERY topology on every render —
// with many topologies that fan-out starved the browser's connection pool
// and Chromium answered ERR_INSUFFICIENT_RESOURCES, phase-10 e2e.)
// Read-only, authenticated.
func handleMeshWeightsCheck(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM weight_policies WHERE weight != 1`).Scan(&n); err != nil {
		meshErr(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"any_custom": n > 0})
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

// meshNameValid bounds operator free-text names (round 4: datacenter and
// server names had no length cap and accepted control characters; the PG
// name ≤200 and topology ≤64 bounds already existed). Trims, rejects
// control characters and invalid UTF-8, caps the length. Empty stays
// allowed where it was before (the handlers decide).
func meshNameValid(name string, max int) (string, bool) {
	name = strings.TrimSpace(name)
	if len(name) > max {
		return name, false
	}
	if !utf8.ValidString(name) {
		return name, false
	}
	for _, rn := range name {
		if unicode.IsControl(rn) {
			return name, false
		}
	}
	return name, true
}

const meshNameMax = 200

func handleMeshDCCreate(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body struct {
		Name             string  `json:"name"`
		Kind             string  `json:"kind"`
		PriceCoefficient float64 `json:"price_coefficient"`
		DatacenterEN     string  `json:"datacenter_en"` // phase 26 §9: optional English name
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if body.Kind != "iran" && body.Kind != "foreign" {
		writeAPIError(w, r, "E-IN-06", "kind must be iran or foreign")
		return
	}
	name, ok := meshNameValid(body.Name, meshNameMax)
	if !ok {
		writeAPIError(w, r, "E-IN-06", "name must be at most 200 characters without control characters")
		return
	}
	body.Name = name
	// Round-4: the create path validates the coefficient HERE (clean 400
	// E-IN-06) — the store's UpsertDatacenter keeps the same range check as
	// the defense-in-depth chokepoint for non-HTTP callers.
	if body.PriceCoefficient < 0 || body.PriceCoefficient > 1e6 {
		writeAPIError(w, r, "E-IN-06", "price_coefficient must be 0-1000000 (0 = default 1.0)")
		return
	}
	dcEn := strings.TrimSpace(body.DatacenterEN)
	if len(dcEn) > 100 {
		writeAPIError(w, r, "E-IN-06", "datacenter_en must be at most 100 characters")
		return
	}
	if body.PriceCoefficient == 0 {
		body.PriceCoefficient = 1.0
	}
	id, err := s.UpsertDatacenter(store.Datacenter{
		Kind: body.Kind, Name: strings.TrimSpace(body.Name), DatacenterEN: dcEn,
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
		DatacenterEN     *string  `json:"datacenter_en"` // phase 26 §9: optional English name
	}
	if !meshReadBody(w, r, &body) || (body.PriceCoefficient == nil && body.DatacenterEN == nil) {
		writeAPIError(w, r, "E-IN-02", "price_coefficient or datacenter_en is required")
		return
	}
	if body.PriceCoefficient != nil {
		// Round-4: a clean 400 at the handler — the store's identical range
		// check surfaced here as E-SYS-01/5xx for what is a validation error.
		if *body.PriceCoefficient < 0 || *body.PriceCoefficient > 1e6 {
			writeAPIError(w, r, "E-IN-06", "price_coefficient must be 0-1000000")
			return
		}
		if err := s.SetDatacenterPriceCoefficient(id, *body.PriceCoefficient); err != nil {
			meshErr(w, r, err)
			return
		}
	}
	if body.DatacenterEN != nil {
		en := strings.TrimSpace(*body.DatacenterEN)
		if len(en) > 100 {
			writeAPIError(w, r, "E-IN-06", "datacenter_en must be at most 100 characters")
			return
		}
		if err := s.SetDatacenterEN(id, en); err != nil {
			meshErr(w, r, err)
			return
		}
	}
	meshAudit(meshActor(r), "mesh.datacenter.price",
		func() string {
			if body.PriceCoefficient == nil {
				return fmt.Sprintf(`{"id":%d,"datacenter_en_set":true}`, id)
			}
			return fmt.Sprintf(`{"id":%d,"price_coefficient":%g,"datacenter_en_set":false}`, id, *body.PriceCoefficient)
		}())
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
	name, ok := meshNameValid(body.Name, meshNameMax)
	if !ok {
		writeAPIError(w, r, "E-IN-06", "name must be at most 200 characters without control characters")
		return
	}
	body.Name = name
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
		patched, ok := meshNameValid(*body.Name, meshNameMax)
		if !ok {
			writeAPIError(w, r, "E-IN-06", "name must be at most 200 characters without control characters")
			return
		}
		sv.Name = patched
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
	h := uiStoreHandle{s}
	if dep := h.serverDependencies(id); len(dep) > 0 {
		if !requireConfirm(w, r, "server", fmt.Sprintf("%d", id),
			dependencyReport{Resource: "server", Depend: dep, HasBlock: true,
				Token: mintConfirmToken("server", fmt.Sprintf("%d", id))}) {
			return
		}
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
	// Phase 9 2a: SECRET engine_params fields are never echoed back.
	for i := range tunnels {
		tunnels[i].EngineParams = stripEngineSecrets(tunnels[i].Engine, tunnels[i].EngineParams)
	}
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
	h := uiStoreHandle{s}
	if dep := h.topologyDependencies(id); len(dep) > 0 {
		if !requireConfirm(w, r, "topology", fmt.Sprintf("%d", id),
			dependencyReport{Resource: "topology", Depend: dep, HasBlock: true,
				Token: mintConfirmToken("topology", fmt.Sprintf("%d", id))}) {
			return
		}
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
		np := string(body.EngineParams)
		// Phase 9 2a: a re-create of an EXISTING key merges — masked
		// secret fields (empty in the form) keep their stored values.
		if prev, err := s.GetTunnelByKey(key); err == nil && prev.EngineParams != "" {
			np = mergeEngineSecrets(body.Engine, prev.EngineParams, np)
		}
		params = np
	}
	if warn, err := validateEngineAgainstCatalog(s, body.LocalServerID, body.Engine); err != nil {
		writeAPIError(w, r, "E-IN-06", err.Error())
		return
	} else if warn != "" {
		meshAudit(meshActor(r), "mesh.tunnel.engine_warning", fmt.Sprintf(`{"engine":%q,"warn":%q}`, body.Engine, warn))
	}
	// Phase 11 requirement 5: the engine must be ENABLED on both endpoint
	// agents (with a signed artifact for its arch) before a tunnel can
	// use it — the refusal names the agent, engine and UI path.
	if body.Enabled && !uiValidateTunnelEngines(w, r, s, body.LocalServerID, body.RemoteServerID, body.Engine) {
		return
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
		np := mergeEngineSecrets(t.Engine, t.EngineParams, string(*body.EngineParams))
		t.EngineParams = np
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
	if t, terr := s.GetTunnel(id); terr == nil {
		h := uiStoreHandle{s}
		if dep := h.tunnelDependencies(t); len(dep) > 0 {
			if !requireConfirm(w, r, "tunnel", t.TunnelKey,
				dependencyReport{Resource: "tunnel", Depend: dep, HasBlock: true,
					Token: mintConfirmToken("tunnel", t.TunnelKey)}) {
				return
			}
		}
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
	// Phase 14: the listen port is validated BEFORE the rule row exists —
	// a port that is well-known, booked in the ledger, already used by
	// another rule on this server, or observed listening on the agent
	// would otherwise only explode as a failed bind on the agent. target_port
	// passes through as given: 0 (= same as listen) is the valid default
	// for group-backed rules, and an explicit value is the foreign-side
	// override the compiler forwards.
	reserved, err := reservedPortsFor(s, body.LocalServerID)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	if reserved[body.ListenPort] {
		uiErrWrite(w, r, "E-ENGINE-PORT-CONFLICT",
			map[string]any{"port": body.ListenPort, "alternatives": freePortSuggestions(s, body.LocalServerID, 3)},
			fmt.Sprintf("listen port %d is taken on server %d", body.ListenPort, body.LocalServerID))
		return
	}
	id, err := s.CreateForwardRule(body)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	// Record the allocation under the rule's owner key so the delete path
	// releases it without remembering the port. The row IS the arbiter:
	// a concurrent create that won the slot rolls this rule back instead
	// of double-booking.
	if aerr := s.AllocatePort(body.LocalServerID, body.ListenPort, body.Proto, "forward_rule", fmt.Sprintf("forward:%d", id)); aerr != nil {
		_ = s.DeleteForwardRule(id)
		if errors.Is(aerr, store.ErrPortTaken) {
			uiErrWrite(w, r, "E-ENGINE-PORT-CONFLICT",
				map[string]any{"port": body.ListenPort, "alternatives": freePortSuggestions(s, body.LocalServerID, 3)},
				fmt.Sprintf("listen port %d was just taken on server %d", body.ListenPort, body.LocalServerID))
			return
		}
		meshErr(w, r, aerr)
		return
	}
	meshAudit(meshActor(r), "mesh.forward.create",
		fmt.Sprintf(`{"id":%d,"listen_port":%d,"target_port":%d,"proto":%q}`, id, body.ListenPort, body.TargetPort, body.Proto))
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
	// Read the rule first: the port release needs the server id (the
	// allocation ledger is keyed per server, not globally).
	f, err := s.GetForwardRule(id)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	if err := s.DeleteForwardRule(id); err != nil {
		meshErr(w, r, err)
		return
	}
	// Phase 14: free the port the create booked (release is a no-op for
	// pre-ledger rules that never had an allocation row).
	if err := s.ReleasePortOwner(f.LocalServerID, fmt.Sprintf("forward:%d", id)); err != nil {
		// A stuck reservation is worse than none, but failing the delete
		// over bookkeeping would strand the rule; report and move on.
		noteStoreRetry()
		log.Printf("[mesh] forward %d: release port allocation: %v", id, err)
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
