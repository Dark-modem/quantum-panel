package panel

// pgdcapi.go — the per-datacenter PasarGuard reconciler (phase 14 task 2,
// design 00-ADMIN/PASARGUARD-PER-DC-DESIGN.md): one VLESS+Reality inbound
// per (foreign server × iran datacenter) inside the core config the
// server's node references, one host record per pair mapping what clients
// see (address = the iran server, port = 443, sni = the DC's SNI).
//
// Endpoints (same conventions as the legacy pgapi.go family):
//
//	GET  /api/mesh/pasarguard/dc/plan   dry-run: the diff per foreign server
//	POST /api/mesh/pasarguard/dc/apply  confirm=yes; books ports, PUTs the
//	                                    core (restart_nodes=true), then hosts
//	GET  /api/ui/datacenters/{id}/client-config   Xray client-config preview
//
// Safety contract:
//   - dry-run touches nothing upstream; apply never deletes (removed pairs
//     are reported stale, deletion stays an explicit operator action);
//   - REALITY keys are generated once per (server, dc) and sealed at rest
//     (reality_keys row, per-install key file). The private key exists
//     UNSEALED only in memory while building the inbound doc and never in
//     a log, an audit line or an API response (json:"-" on the plan).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Dark-modem/quantum-panel/internal/pasarguard"
	"github.com/Dark-modem/quantum-panel/internal/store"
)

// Settings keys (the pasarguard_settings KV table, same as the connection
// settings and the port allocator's keys).
const (
	pgKeyRealityKeys = "reality_keys" // sealed { "<server>:<dc>": {pvk_sealed, pbk} }
	pgKeySNIBase     = "reality_sni_base"
	pgKeyMode        = "pasarguard_mode" // informational: "legacy" | "per-dc"

	// pgDCDefaultDest is the REALITY dest when the per-DC setting is unset.
	pgDCDefaultDest = "www.microsoft.com:443"
)

// pgRealityKeyEntry is one sealed key row of the reality_keys store. The
// private key only ever appears sealed here; the public key is not secret
// (it goes into client configs).
type pgRealityKeyEntry struct {
	PVKSealed string `json:"pvk_sealed"`
	PBK       string `json:"pbk"`
}

// pgRealityKeyID is the store key for one (foreign server, datacenter)
// pair — server:dc order, slugged (safe in JSON and the ledger).
func pgRealityKeyID(serverSlug, dcSlug string) string {
	return serverSlug + ":" + dcSlug
}

// pgRealityKeys loads the whole sealed key store.
func pgRealityKeys(s *store.Store) (map[string]pgRealityKeyEntry, error) {
	out := map[string]pgRealityKeyEntry{}
	raw, _ := s.Setting(pgKeyRealityKeys)
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("reality_keys store unreadable: %v", err)
	}
	return out, nil
}

// pgRealityKeyFor returns the pair for one (server, dc), generating and
// persisting it when absent. Generation is once-per-pair: the sealed
// private key is written back immediately and the plaintext never leaves
// this function's caller unsealed.
func pgRealityKeyFor(s *store.Store, key []byte, id string) (pgRealityKeyEntry, error) {
	all, err := pgRealityKeys(s)
	if err != nil {
		return pgRealityKeyEntry{}, err
	}
	if e, ok := all[id]; ok && e.PVKSealed != "" && e.PBK != "" {
		return e, nil
	}
	pvkB64, pbkB64, err := pasarguard.GenerateRealityKeyPair()
	if err != nil {
		return pgRealityKeyEntry{}, err
	}
	sealed, err := pasarguard.Seal(key, []byte(pvkB64))
	if err != nil {
		return pgRealityKeyEntry{}, err
	}
	e := pgRealityKeyEntry{PVKSealed: sealed, PBK: pbkB64}
	all[id] = e
	b, err := json.Marshal(all)
	if err != nil {
		return pgRealityKeyEntry{}, err
	}
	if err := s.SetSetting(pgKeyRealityKeys, string(b)); err != nil {
		return pgRealityKeyEntry{}, err
	}
	return e, nil
}

// ---- ports (the phase 14 ledger; see portalloc.go) --------------------------

func pgDCPortPurpose(dcSlug string) string { return "xray-inbound:" + dcSlug }
func pgDCPortOwner(serverSlug, dcSlug string) string {
	return "pg-inbound:" + serverSlug + ":" + dcSlug
}

// pgDCPort resolves the 127.0.0.1 inbound port for one (server, dc): the
// ledger row booked for the owner when present, otherwise — dry run — the
// next free suggestion WITHOUT booking, or — apply — a real booking.
// An existing ledger row IS the booking: an apply re-run returns it as-is
// (the insert's unique constraint arbitrates BETWEEN allocators only).
func pgDCPort(s *store.Store, serverID int64, serverSlug, dcSlug string, book bool) (int, error) {
	owner := pgDCPortOwner(serverSlug, dcSlug)
	allocs, err := s.ListPortAllocations(serverID)
	if err != nil {
		return 0, err
	}
	planned := 0
	for _, a := range allocs {
		if a.OwnerKey == owner {
			planned = a.Port
			break
		}
	}
	if planned > 0 {
		return planned, nil
	}
	// The deterministic suggestion: dry run AND the apply's preferred
	// candidate converge on the same port for the same store state.
	sug := freePortSuggestions(s, serverID, 1)
	if len(sug) == 0 {
		return 0, errNoFreePort
	}
	if !book {
		return sug[0], nil
	}
	return AllocateServerPort(s, serverID, "tcp", pgDCPortPurpose(dcSlug), owner, sug[0])
}

// ---- desired enumeration ----------------------------------------------------

// pgDCPair is one desired (foreign server × iran datacenter) inbound.
type pgDCPair struct {
	Server        store.Server
	DC            store.Datacenter
	NodeID        int64  // the server's upstream node (resolves the core)
	ClientAddress string // the iran server address clients use ("" when the dc has none)
}

// pgDCPairs enumerates the desired pairs: every FOREIGN server with a
// linked pg_node × every iran datacenter. The node linkage is per server
// (the new model is ONE node per foreign server); a mapping row without
// an upstream node id yet means the node was never provisioned and there
// is no core config to manage — warned and skipped.
func pgDCPairs(s *store.Store) ([]pgDCPair, []string, error) {
	servers, err := s.ListServers()
	if err != nil {
		return nil, nil, err
	}
	dcs, err := s.ListDatacenters()
	if err != nil {
		return nil, nil, err
	}
	mappings, err := s.ListPGNodes()
	if err != nil {
		return nil, nil, err
	}

	nodeByServer := map[int64]int64{}
	mappedServers := map[int64]bool{}
	for _, m := range mappings {
		mappedServers[m.ServerID] = true
		if m.PGNodeID > 0 && nodeByServer[m.ServerID] == 0 {
			nodeByServer[m.ServerID] = m.PGNodeID
		}
	}
	// The client-facing address: the FIRST iran server of the dc with an
	// address (the common mesh is one iran server per dc).
	iranAddressByDC := map[int64]string{}
	for _, sv := range servers {
		if sv.Role != "iran" || sv.DatacenterID == 0 {
			continue
		}
		if _, ok := iranAddressByDC[sv.DatacenterID]; !ok && strings.TrimSpace(sv.Address) != "" {
			iranAddressByDC[sv.DatacenterID] = sv.Address
		}
	}

	// Warnings once per subject (server/dc), not per pair.
	var warnings []string
	for _, dc := range dcs {
		if dc.Kind == "iran" && iranAddressByDC[dc.ID] == "" {
			warnings = append(warnings, fmt.Sprintf("datacenter %s has no iran server with an address; its host records carry no address", dc.Name))
		}
	}
	for _, sv := range servers {
		if sv.Role != "foreign" {
			continue
		}
		switch {
		case nodeByServer[sv.ID] == 0 && mappedServers[sv.ID]:
			warnings = append(warnings, fmt.Sprintf("server %s: pg_node mapping has no upstream node id yet (reconcile or install first); skipping", sv.Name))
		case nodeByServer[sv.ID] == 0:
			warnings = append(warnings, fmt.Sprintf("server %s: no pg_node mapping (install + reconcile first); skipping", sv.Name))
		case strings.TrimSpace(sv.Address) == "":
			warnings = append(warnings, fmt.Sprintf("server %s has no address; its Reality SNI falls back to invalid", sv.Name))
		}
	}

	var pairs []pgDCPair
	for _, dc := range dcs {
		if dc.Kind != "iran" {
			continue
		}
		for _, sv := range servers {
			if sv.Role != "foreign" {
				continue
			}
			if nodeID := nodeByServer[sv.ID]; nodeID > 0 {
				pairs = append(pairs, pgDCPair{Server: sv, DC: dc, NodeID: nodeID, ClientAddress: iranAddressByDC[dc.ID]})
			}
		}
	}
	return pairs, warnings, nil
}

// pgDCCoreID resolves the core config the server's node references
// (upstream node.core_config_id; 0/absent falls back to the default core 1).
func pgDCCoreID(ctx context.Context, cl *pasarguard.Client, nodeID int64) (int, error) {
	n, err := cl.GetNode(ctx, int(nodeID))
	if err != nil {
		return 0, err
	}
	if n.CoreConfigID > 0 {
		return n.CoreConfigID, nil
	}
	return 1, nil
}

// pgDCSNI builds the DC's Reality SNI: "q-<dcslug>.<base>" where base is
// the "reality_sni_base" setting; without one the FOREIGN server's
// address stands in (unique per server, needs no DNS). One inbound, one
// SNI — independent SNIs are exactly why the design chooses one inbound
// per datacenter instead of shared serverNames lists.
func pgDCSNI(s *store.Store, sv store.Server, dc store.Datacenter) string {
	base, _ := s.Setting(pgKeySNIBase)
	base = strings.TrimSpace(base)
	if base == "" {
		base = strings.TrimSpace(sv.Address)
	}
	if base == "" {
		base = "invalid"
	}
	return fmt.Sprintf("q-%s.%s", pasarguard.SlugPart(dc.Name), base)
}

// pgDCDest reads the per-DC REALITY dest ("reality_dest_<dcslug>"),
// defaulting to www.microsoft.com:443.
func pgDCDest(s *store.Store, dc store.Datacenter) string {
	v, _ := s.Setting("reality_dest_" + pasarguard.SlugPart(dc.Name))
	v = strings.TrimSpace(v)
	if v == "" {
		return pgDCDefaultDest
	}
	return v
}

// ---- plan build -------------------------------------------------------------

// pgDCServerPlan is one foreign server's plan: one core, one diff, one
// apply. The apply re-reads the core record itself (the PUT is a full
// replace and plans age), so nothing of the GET snapshot is carried.
type pgDCServerPlan struct {
	Server   string                    `json:"server"`
	ServerID int64                     `json:"server_id"`
	CoreID   int                       `json:"core_id"`
	Plan     *pasarguard.DCInboundPlan `json:"plan"`
}

// pgDCPlansFor builds the desired inbounds+hosts for every pair and diffs
// them against the real core configs. book=false is the dry run (ports
// suggested, nothing booked); book=true books each pair's port with the
// planned one preferred. Reality keys are generated (and sealed) on first
// sight of a pair; the private key exists unsealed only inside this
// function's inbound docs.
func pgDCPlansFor(ctx context.Context, s *store.Store, cl *pasarguard.Client, book bool) ([]*pgDCServerPlan, []string, error) {
	pairs, warnings, err := pgDCPairs(s)
	if err != nil {
		return nil, warnings, err
	}
	key, err := pgKey()
	if err != nil {
		return nil, warnings, err
	}

	byServer := map[int64][]pgDCPair{}
	var order []int64
	for _, p := range pairs {
		if _, ok := byServer[p.Server.ID]; !ok {
			order = append(order, p.Server.ID)
		}
		byServer[p.Server.ID] = append(byServer[p.Server.ID], p)
	}

	out := []*pgDCServerPlan{}
	for _, sid := range order {
		prs := byServer[sid]
		sv := prs[0].Server
		coreID, err := pgDCCoreID(ctx, cl, prs[0].NodeID)
		if err != nil {
			return nil, warnings, fmt.Errorf("server %s node %d: %v", sv.Name, prs[0].NodeID, err)
		}
		var desired []pasarguard.InboundPlan
		for _, pr := range prs {
			dcSlug := pasarguard.SlugPart(pr.DC.Name)
			svSlug := pasarguard.SlugPart(sv.Name)
			entry, err := pgRealityKeyFor(s, key, pgRealityKeyID(svSlug, dcSlug))
			if err != nil {
				return nil, warnings, fmt.Errorf("reality keys for %s/%s: %v", sv.Name, pr.DC.Name, err)
			}
			pvkB, err := pasarguard.Open(key, entry.PVKSealed)
			if err != nil {
				return nil, warnings, fmt.Errorf("reality key unseal for %s/%s: %v", sv.Name, pr.DC.Name, err)
			}
			pvkB64 := string(pvkB)
			shortID, err := pasarguard.RealityShortID(pvkB64)
			if err != nil {
				return nil, warnings, fmt.Errorf("reality shortId for %s/%s: %v", sv.Name, pr.DC.Name, err)
			}
			port, err := pgDCPort(s, sv.ID, svSlug, dcSlug, book)
			if err != nil {
				return nil, warnings, fmt.Errorf("port for %s/%s: %v", sv.Name, pr.DC.Name, err)
			}
			desired = append(desired, pasarguard.InboundPlan{
				Tag:    pasarguard.DCInboundTag(dcSlug, svSlug),
				Listen: "127.0.0.1",
				Port:   port,
				SNIs:   []string{pgDCSNI(s, sv, pr.DC)},
				Dest:   pgDCDest(s, pr.DC),
				// shortId derives from the key pair: stable across applies.
				ShortIDs:      []string{shortID},
				PrivateKey:    pvkB64,
				PublicKey:     entry.PBK,
				ClientAddress: pr.ClientAddress,
			})
		}
		core, err := cl.GetCore(ctx, coreID)
		if err != nil {
			return nil, warnings, fmt.Errorf("core %d on server %s: %v", coreID, sv.Name, err)
		}
		cfg, _ := core["config"].(map[string]any)
		if cfg == nil {
			return nil, warnings, fmt.Errorf("core %d on server %s: config missing or not an object", coreID, sv.Name)
		}
		hosts, err := cl.ListHosts(ctx)
		if err != nil {
			return nil, warnings, fmt.Errorf("host list on server %s: %v", sv.Name, err)
		}
		plan := pasarguard.DiffDCInbounds(cfg, desired, hosts, pasarguard.NamePrefix)
		plan.Server, plan.CoreID = sv.Name, coreID
		out = append(out, &pgDCServerPlan{Server: sv.Name, ServerID: sv.ID, CoreID: coreID, Plan: plan})
	}
	return out, warnings, nil
}

// ---- endpoints ---------------------------------------------------------------

// handlePGDCPlan is the per-DC dry run (default): the diff per foreign
// server, nothing touched upstream.
func handlePGDCPlan(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	cl, err := pgClient(r.Context())
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", err.Error())
		return
	}
	if cl == nil {
		writeAPIError(w, r, "E-IN-04", "PasarGuard is not connected (use connect first)")
		return
	}
	plans, warnings, err := pgDCPlansFor(r.Context(), s, cl, false)
	if err != nil {
		writeAPIError(w, r, "E-UPSTREAM-01", err.Error())
		return
	}
	drift := false
	for _, p := range plans {
		if p.Plan.Drift() {
			drift = true
		}
	}
	writeJSON(w, map[string]any{
		"plans": nonNil(plans), "warnings": nonNil(warnings),
		"applied": false, "drift": drift,
	})
}

// handlePGDCApply executes per-DC plans: same enumeration (with ports
// booked), then per server the core PUT with restart_nodes=true followed
// by the host creates/modifies. NEVER deletes. confirm=yes required, same
// gate as the legacy reconcile.
func handlePGDCApply(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body struct {
		Apply   bool   `json:"apply"`
		Confirm string `json:"confirm"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	cl, err := pgClient(r.Context())
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", err.Error())
		return
	}
	if cl == nil {
		writeAPIError(w, r, "E-IN-04", "PasarGuard is not connected (use connect first)")
		return
	}
	if !body.Apply {
		plans, warnings, err := pgDCPlansFor(r.Context(), s, cl, false)
		if err != nil {
			writeAPIError(w, r, "E-UPSTREAM-01", err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"plans": nonNil(plans), "warnings": nonNil(warnings), "applied": false,
		})
		return
	}
	if body.Confirm != "yes" {
		writeAPIError(w, r, "E-AUTH-08", "apply requires confirm=yes (review the dry-run first)")
		return
	}
	plans, warnings, err := pgDCPlansFor(r.Context(), s, cl, true)
	if err != nil {
		writeAPIError(w, r, "E-UPSTREAM-01", err.Error())
		return
	}
	results := []map[string]any{}
	creates, modifies, hostChanges := 0, 0, 0
	for _, sp := range plans {
		rows, err := pasarguard.ApplyDCInbounds(r.Context(), cl, sp.CoreID, sp.Plan)
		if err != nil {
			results = append(results, map[string]any{
				"server": sp.Server, "core_id": sp.CoreID, "error": err.Error(),
			})
			continue
		}
		creates += len(sp.Plan.Create)
		modifies += len(sp.Plan.Modify)
		hostChanges += len(sp.Plan.HostsCreate) + len(sp.Plan.HostsModify)
		results = append(results, map[string]any{
			"server": sp.Server, "core_id": sp.CoreID, "results": rows,
		})
	}
	// Audit carries counts only — never the plan (it contains key material
	// fields that are json:"-" but stay out of logs by construction too).
	meshAudit(meshActor(r), "pg.dc.apply",
		fmt.Sprintf(`{"servers":%d,"inbound_creates":%d,"inbound_modifies":%d,"host_changes":%d}`,
			len(plans), creates, modifies, hostChanges))
	writeJSON(w, map[string]any{
		"plans": nonNil(plans), "results": results,
		"warnings": nonNil(warnings), "applied": true,
	})
}

// handleUIDCClientConfig serves the Xray client-config preview for one
// iran datacenter (UI-API-REQUESTS.md): one outbound per iran server of
// the DC — address = that server's public address, port = 443 (the
// forward rule's listen port) — with the DC's Reality fields (sni,
// publicKey, shortId) from the first foreign-server pair with a linked
// node. A DC served by several foreign servers gets a note: every extra
// server runs its own inbound with its own keys, so the preview shows the
// primary pair.
func handleUIDCClientConfig(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	dc, err := s.GetDatacenter(atoiDefault(r.PathValue("id"), 0))
	if err != nil {
		meshErr(w, r, err)
		return
	}
	if dc.Kind != "iran" {
		writeAPIError(w, r, "E-IN-06", "client-config is for IRAN datacenters")
		return
	}
	servers, err := s.ListServers()
	if err != nil {
		meshErr(w, r, err)
		return
	}

	// The primary foreign pair: first foreign server with a linked node.
	var pairServer store.Server
	found := false
	var notes []string
	for _, sv := range servers {
		if sv.Role != "foreign" {
			continue
		}
		m, err := s.GetPGNodeByPair(sv.ID, dc.ID)
		if err != nil || m.PGNodeID <= 0 {
			continue
		}
		if !found {
			pairServer, found = sv, true
		} else {
			notes = append(notes, fmt.Sprintf("foreign server %s also serves this datacenter with its own inbound/keys; the preview shows %s", sv.Name, pairServer.Name))
		}
	}
	if !found {
		writeAPIError(w, r, "E-IN-04", "no foreign server with a linked PasarGuard node serves this datacenter yet")
		return
	}

	dcSlug := pasarguard.SlugPart(dc.Name)
	svSlug := pasarguard.SlugPart(pairServer.Name)
	key, err := pgKey()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", err.Error())
		return
	}
	entry, err := pgRealityKeyFor(s, key, pgRealityKeyID(svSlug, dcSlug))
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", err.Error())
		return
	}
	pvkB, err := pasarguard.Open(key, entry.PVKSealed)
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", err.Error())
		return
	}
	shortID, err := pasarguard.RealityShortID(string(pvkB))
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", err.Error())
		return
	}

	sni := pgDCSNI(s, pairServer, dc)
	outbounds := []map[string]any{}
	for _, sv := range servers {
		if sv.Role != "iran" || sv.DatacenterID != dc.ID || strings.TrimSpace(sv.Address) == "" {
			continue
		}
		outbounds = append(outbounds, map[string]any{
			"tag":       fmt.Sprintf("q-%s-%s", dcSlug, pasarguard.SlugPart(sv.Name)),
			"address":   sv.Address,
			"port":      443,
			"sni":       sni,
			"publicKey": entry.PBK,
			"shortId":   shortID,
		})
	}
	out := map[string]any{
		"datacenter": dc,
		"outbounds":  outbounds,
		"sni":        sni,
		"publicKey":  entry.PBK,
		"shortId":    shortID,
	}
	// Optional balancer fragment: with more than one outbound the client
	// can round-robin them; selectors match every q-<dc>-<server> tag.
	if len(outbounds) > 1 {
		out["balancer"] = map[string]any{
			"tag":      fmt.Sprintf("q-%s-bal", dcSlug),
			"selector": []string{fmt.Sprintf("q-%s-", dcSlug)},
			"strategy": "roundRobin",
		}
	}
	if notes != nil {
		out["notes"] = notes
	}
	writeJSON(w, out)
}
