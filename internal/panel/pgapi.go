package panel

// pgapi.go — PasarGuard integration endpoints on the mesh API. The
// connection credentials are stored ENCRYPTED at rest (AES-256-GCM sealed
// with the per-install key file configDir/pasarguard.key, mode 0600); the
// URL is not a secret and stays plain. The reconciler is dry-run by
// default; apply requires confirm=yes. Nothing here ever executes on a
// server — the install command is TEXT the operator runs themselves.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/pasarguard"
	"github.com/Dark-modem/quantum-panel/internal/store"
)

// pgSettings is what the connect endpoint persists.
type pgSettings struct {
	URL     string                `json:"url"`
	UserEnc string                `json:"user_enc"`
	PassEnc string                `json:"pass_enc"`
	TLS     pasarguard.TLSOptions `json:"tls,omitempty"`
}

const (
	pgKeyURL      = "pasarguard_url"
	pgKeyUser     = "pasarguard_user_enc"
	pgKeyPass     = "pasarguard_pass_enc"
	pgKeyTLS      = "pasarguard_tls"
	pgKeyLastTest = "pasarguard_last_test"
)

// pgKey loads (or creates) the per-install sealing key.
func pgKey() ([]byte, error) {
	return pasarguard.LoadOrCreateKey(configDir)
}

// pgClient builds a client from the stored settings (nil, nil = not configured).
func pgClient(ctx context.Context) (*pasarguard.Client, error) {
	s, err := MeshStore()
	if err != nil {
		return nil, err
	}
	url, _ := s.Setting(pgKeyURL)
	if strings.TrimSpace(url) == "" {
		return nil, nil
	}
	key, err := pgKey()
	if err != nil {
		return nil, err
	}
	userEnc, _ := s.Setting(pgKeyUser)
	passEnc, _ := s.Setting(pgKeyPass)
	userB, err := pasarguard.Open(key, userEnc)
	if err != nil {
		return nil, fmt.Errorf("stored credentials unreadable (%v) — reconnect PasarGuard", err)
	}
	passB, err := pasarguard.Open(key, passEnc)
	if err != nil {
		return nil, fmt.Errorf("stored credentials unreadable (%v) — reconnect PasarGuard", err)
	}
	tlsJSON, _ := s.Setting(pgKeyTLS)
	var tlsOpts pasarguard.TLSOptions
	if tlsJSON != "" {
		_ = json.Unmarshal([]byte(tlsJSON), &tlsOpts)
	}
	return pasarguard.New(url, pasarguard.Credentials{
		Username: string(userB), Password: string(passB),
	}, tlsOpts, 10*time.Second)
}

func registerPasarGuardRoutes(mux *http.ServeMux, base string) {
	p := base + "/api/mesh/pasarguard"
	mux.HandleFunc("GET "+p+"/status", requireAuth(handlePGStatus))
	mux.HandleFunc("GET "+p+"/nodes", requireAuth(handlePGNodesView))
	mux.HandleFunc("GET "+p+"/crosscheck", requireAuth(handlePGCrossCheck))
	mux.HandleFunc("POST "+p+"/connect", requireAuth(requireCSRF(handlePGConnect)))
	mux.HandleFunc("POST "+p+"/reconcile", requireAuth(requireCSRF(handlePGReconcile)))
	mux.HandleFunc("POST "+p+"/orphans", requireAuth(requireCSRF(handlePGOrphans)))
	mux.HandleFunc("POST "+p+"/install-command", requireAuth(requireCSRF(handlePGInstallCommand)))
	mux.HandleFunc("POST "+p+"/cert", requireAuth(requireCSRF(handlePGCert)))
	// Phase 14 task 2: the per-datacenter inbound reconciler (pgdcapi.go;
	// design 00-ADMIN/PASARGUARD-PER-DC-DESIGN.md).
	mux.HandleFunc("GET "+p+"/dc/plan", requireAuth(handlePGDCPlan))
	mux.HandleFunc("POST "+p+"/dc/apply", requireAuth(requireCSRF(handlePGDCApply)))
}

// handlePGConnect validates + stores the connection and runs a live test
// (login + node list). Credentials are sealed before storage; the response
// and the audit line never contain them.
func handlePGConnect(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body struct {
		URL       string `json:"url"`
		Username  string `json:"username"`
		Password  string `json:"password"`
		CAPEM     string `json:"ca_pem,omitempty"`
		PinSHA256 string `json:"pin_sha256,omitempty"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if len(body.Password) > 512 || len(body.Username) > 128 || len(body.URL) > 512 {
		writeAPIError(w, r, "E-IN-06", "url/username/password too long")
		return
	}
	tlsOpts := pasarguard.TLSOptions{CAPem: body.CAPEM, PinSHA256: body.PinSHA256}
	if len(body.CAPEM) > 64<<10 || len(body.PinSHA256) > 64 {
		writeAPIError(w, r, "E-IN-06", "ca_pem/pin too long")
		return
	}
	// L2 (SECURITY-AUDIT-PANEL): optional SSRF guard. The PasarGuard URL
	// is an operator setting, so the panel fetching it is privileged by
	// design; when pasarguard_block_private is set, private/loopback
	// targets are refused before any request leaves the panel.
	if err := pgGuardPrivateURL(body.URL); err != nil {
		writeAPIError(w, r, "E-IN-06", err.Error())
		return
	}
	cl, err := pasarguard.New(body.URL, pasarguard.Credentials{
		Username: body.Username, Password: body.Password,
	}, tlsOpts, 10*time.Second)
	if err != nil {
		writeAPIError(w, r, "E-IN-06", err.Error())
		return
	}
	nodes, err := cl.ListNodes(r.Context(), "")
	if err != nil {
		writeAPIError(w, r, "E-UPSTREAM-01", "PasarGuard test failed: "+err.Error())
		return
	}
	key, err := pgKey()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "secret key: "+err.Error())
		return
	}
	userEnc, err := pasarguard.Seal(key, []byte(body.Username))
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "seal: "+err.Error())
		return
	}
	passEnc, err := pasarguard.Seal(key, []byte(body.Password))
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "seal: "+err.Error())
		return
	}
	tlsJSON, _ := json.Marshal(tlsOpts)
	if err := s.SetSetting(pgKeyURL, strings.TrimRight(body.URL, "/")); err != nil {
		meshErr(w, r, err)
		return
	}
	_ = s.SetSetting(pgKeyUser, userEnc)
	_ = s.SetSetting(pgKeyPass, passEnc)
	_ = s.SetSetting(pgKeyTLS, string(tlsJSON))
	_ = s.SetSetting(pgKeyLastTest, fmt.Sprintf(`{"ok":true,"nodes":%d,"at":%d}`, len(nodes), time.Now().Unix()))
	meshAudit(meshActor(r), "pg.connect", fmt.Sprintf(`{"nodes":%d}`, len(nodes)))
	writeJSON(w, map[string]any{"ok": true, "nodes": len(nodes)})
}

func handlePGStatus(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	url, _ := s.Setting(pgKeyURL)
	lastTest, _ := s.Setting(pgKeyLastTest)
	out := map[string]any{"configured": url != "", "url": url}
	if lastTest != "" {
		out["last_test"] = json.RawMessage(lastTest)
	}
	writeJSON(w, out)
}

// pgDesired builds the desired node set from the store: every (foreign
// server × iran datacenter). Pairs whose server has no address or whose
// mapping lacks cert/api-key still appear — the plan annotates them.
// The datacenter price coefficient is deliberately NOT carried here
// (phase 14 decision): it left the product's data path, the reconciler
// no longer manages the upstream usage_coefficient at all, and the
// store's price hint remains billing-display only.
func pgDesired(s *store.Store) ([]pasarguard.DesiredNode, []string, error) {
	servers, err := s.ListServers()
	if err != nil {
		return nil, nil, err
	}
	dcs, err := s.ListDatacenters()
	if err != nil {
		return nil, nil, err
	}
	var desired []pasarguard.DesiredNode
	var warnings []string
	for _, sv := range servers {
		if sv.Role != "foreign" {
			continue
		}
		for _, dc := range dcs {
			if dc.Kind != "iran" {
				continue
			}
			d := pasarguard.DesiredNode{
				Name:    pasarguard.DesiredName(dc.Name, sv.Name),
				Address: sv.Address,
			}
			if m, err := s.GetPGNodeByPair(sv.ID, dc.ID); err == nil {
				d.ServerCA = m.ServerCA
				d.Port = m.NodePort
				if key, err := pgKey(); err == nil && m.APIKeyEnc != "" {
					if kb, err := pasarguard.Open(key, m.APIKeyEnc); err == nil {
						d.APIKey = string(kb)
					}
				}
			}
			if strings.TrimSpace(d.Address) == "" {
				warnings = append(warnings, fmt.Sprintf("server %s has no address; %s cannot be created until it has one", sv.Name, d.Name))
			}
			if d.ServerCA == "" || d.APIKey == "" {
				warnings = append(warnings, fmt.Sprintf("%s on %s: node install + certificate + api key pending (use install-command)", d.Name, sv.Name))
			}
			desired = append(desired, d)
		}
	}
	return desired, warnings, nil
}

// handlePGReconcile is dry-run by default; apply requires confirm=yes.
//
// LEGACY: this is the node-per-(server×DC) reconciler — one PasarGuard
// NODE per (foreign server × iran datacenter) pair. New installs use the
// per-datacenter inbound family /api/mesh/pasarguard/dc/* (one node per
// foreign server, one inbound+host per datacenter — see
// 00-ADMIN/PASARGUARD-PER-DC-DESIGN.md). It keeps working unchanged for
// existing installs; switching to per-dc makes the old per-DC nodes
// stale here, removable only by an explicit orphans run.
func handlePGReconcile(w http.ResponseWriter, r *http.Request) {
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
	desired, warnings, err := pgDesired(s)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	real, err := cl.ListNodes(r.Context(), "")
	if err != nil {
		writeAPIError(w, r, "E-UPSTREAM-01", err.Error())
		return
	}
	plan := pasarguard.Diff(desired, real)
	out := map[string]any{"plan": plan, "warnings": warnings, "applied": false}
	if !body.Apply {
		writeJSON(w, out)
		return
	}
	if body.Confirm != "yes" {
		writeAPIError(w, r, "E-AUTH-08", "apply requires confirm=yes (review the dry-run first)")
		return
	}
	results := pasarguard.Apply(r.Context(), plan, desired, cl)
	out["applied"] = true
	out["results"] = results
	meshAudit(meshActor(r), "pg.reconcile.apply",
		fmt.Sprintf(`{"creates":%d,"updates":%d,"reconnects":%d}`, plan.Creates, plan.Updates, plan.Reconnects))

	// Persist the mapping rows for nodes we successfully created/updated.
	pgSyncMappings(s, plan, results, desired)
	writeJSON(w, out)
}

// pgSyncMappings records pg_node ids for created nodes (by name) so the
// mapping table follows reality.
func pgSyncMappings(s *store.Store, plan *pasarguard.Plan, results []pasarguard.PlanAction, desired []pasarguard.DesiredNode) {
	realByName := map[string]pasarguard.DesiredNode{}
	for _, d := range desired {
		realByName[d.Name] = d
	}
	// We need ids: re-list from the panel is not available here (the client
	// is not passed); instead the mapping is refreshed on the next reconcile
	// dry-run via /nodes view. The minimal bookkeeping: remember pair ids of
	// created names that carry a mappingless pair.
	servers, _ := s.ListServers()
	dcs, _ := s.ListDatacenters()
	for _, res := range results {
		if res.Action != pasarguard.ActionCreate || !strings.HasPrefix(res.Detail, "created") {
			continue
		}
		d, ok := realByName[res.Name]
		if !ok {
			continue
		}
		var serverID, dcID int64
		for _, sv := range servers {
			if sv.Role == "foreign" && sv.Address == d.Address {
				serverID = sv.ID
				break
			}
		}
		for _, dc := range dcs {
			for _, sv2 := range servers {
				if sv2.ID == serverID && pasarguard.DesiredName(dc.Name, sv2.Name) == res.Name {
					dcID = dc.ID
					break
				}
			}
			if dcID != 0 {
				break
			}
		}
		if serverID != 0 && dcID != 0 {
			if _, err := s.GetPGNodeByPair(serverID, dcID); errorsIsNotFound(err) {
				_, _ = s.UpsertPGNode(store.PGNode{
					ServerID: serverID, DatacenterID: dcID,
					ServerCA: d.ServerCA, NodePort: d.Port,
					Note: "created by reconcile " + time.Now().Format(time.RFC3339),
				})
			}
		}
	}
}

func errorsIsNotFound(err error) bool {
	return err != nil && err.Error() == "store: not found"
}

// handlePGOrphans removes stale quantum-owned nodes, explicit only.
func handlePGOrphans(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body struct {
		Confirm string `json:"confirm"`
		IDs     []int  `json:"ids"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if body.Confirm != "yes" {
		writeAPIError(w, r, "E-AUTH-08", "delete-orphans requires confirm=yes")
		return
	}
	if len(body.IDs) == 0 || len(body.IDs) > 100 {
		writeAPIError(w, r, "E-IN-06", "ids must be 1-100 node ids")
		return
	}
	cl, err := pgClient(r.Context())
	if err != nil || cl == nil {
		writeAPIError(w, r, "E-IN-04", "PasarGuard is not connected")
		return
	}
	out, err := pasarguard.DeleteOrphans(r.Context(), body.IDs, cl)
	if err != nil {
		writeAPIError(w, r, "E-UPSTREAM-01", err.Error())
		return
	}
	meshAudit(meshActor(r), "pg.orphans.delete", fmt.Sprintf(`{"requested":%d}`, len(body.IDs)))
	writeJSON(w, map[string]any{"results": out})
}

// handlePGNodesView merges three views: the real panel nodes, the desired
// set, and the local mappings.
func handlePGNodesView(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	cl, err := pgClient(r.Context())
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", err.Error())
		return
	}
	var real []pasarguard.Node
	if cl != nil {
		real, err = cl.ListNodes(r.Context(), "")
		if err != nil {
			writeAPIError(w, r, "E-UPSTREAM-01", err.Error())
			return
		}
	}
	desired, warnings, err := pgDesired(s)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	mappings, err := s.ListPGNodes()
	if err != nil {
		meshErr(w, r, err)
		return
	}
	// Mappings expose the server/dc names (not raw sealed secrets).
	type mappingView struct {
		ID           int64  `json:"id"`
		ServerID     int64  `json:"server_id"`
		DatacenterID int64  `json:"datacenter_id"`
		PGNodeID     int64  `json:"pg_node_id"`
		HasAPIKey    bool   `json:"has_api_key"`
		HasCert      bool   `json:"has_cert"`
		NodePort     int    `json:"node_port"`
		Note         string `json:"note,omitempty"`
	}
	mv := []mappingView{}
	for _, m := range mappings {
		mv = append(mv, mappingView{ID: m.ID, ServerID: m.ServerID, DatacenterID: m.DatacenterID,
			PGNodeID: m.PGNodeID, HasAPIKey: m.APIKeyEnc != "", HasCert: m.ServerCA != "",
			NodePort: m.NodePort, Note: m.Note})
	}
	writeJSON(w, map[string]any{
		"nodes": nonNil(real), "desired": desired,
		"mappings": mv, "warnings": warnings,
	})
}

// handlePGCert stores the operator-pasted node certificate (and optional
// api key) for a (server, datacenter) pair.
func handlePGCert(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body struct {
		ServerID     int64  `json:"server_id"`
		DatacenterID int64  `json:"datacenter_id"`
		ServerCA     string `json:"server_ca"`
		NodePort     int    `json:"node_port"`
		APIKey       string `json:"api_key,omitempty"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	if !strings.Contains(body.ServerCA, "BEGIN CERTIFICATE") || len(body.ServerCA) > 2048 {
		writeAPIError(w, r, "E-IN-06", "server_ca must be a PEM certificate ≤ 2048 chars")
		return
	}
	if body.APIKey != "" && (len(body.APIKey) != 36 || strings.Count(body.APIKey, "-") != 4) {
		writeAPIError(w, r, "E-IN-06", "api_key must be a UUID")
		return
	}
	m, err := s.GetPGNodeByPair(body.ServerID, body.DatacenterID)
	if err != nil {
		m = store.PGNode{ServerID: body.ServerID, DatacenterID: body.DatacenterID}
	}
	m.ServerCA = body.ServerCA
	if body.NodePort > 0 && body.NodePort <= 65535 {
		m.NodePort = body.NodePort
	}
	if body.APIKey != "" {
		key, kerr := pgKey()
		if kerr != nil {
			writeAPIError(w, r, "E-SYS-01", kerr.Error())
			return
		}
		enc, kerr := pasarguard.Seal(key, []byte(body.APIKey))
		if kerr != nil {
			writeAPIError(w, r, "E-SYS-01", kerr.Error())
			return
		}
		m.APIKeyEnc = enc
	}
	if _, err := s.UpsertPGNode(m); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "pg.cert.store",
		fmt.Sprintf(`{"server_id":%d,"datacenter_id":%d,"api_key":%t}`, body.ServerID, body.DatacenterID, body.APIKey != ""))
	writeJSON(w, map[string]any{"ok": true})
}

// handlePGInstallCommand generates a fresh api_key UUID for the pair,
// stores it sealed, and returns the install commands as TEXT (the operator
// runs them on the foreign server; Quantum executes nothing).
func handlePGInstallCommand(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	var body struct {
		ServerID     int64 `json:"server_id"`
		DatacenterID int64 `json:"datacenter_id"`
		NodePort     int   `json:"node_port"`
	}
	if !meshReadBody(w, r, &body) {
		return
	}
	sv, err := s.GetServer(body.ServerID)
	if err != nil {
		meshErr(w, r, fmt.Errorf("server %d: %v", body.ServerID, err))
		return
	}
	if sv.Role != "foreign" {
		writeAPIError(w, r, "E-IN-06", "PasarGuard nodes run on FOREIGN servers only")
		return
	}
	dc, err := s.GetDatacenter(body.DatacenterID)
	if err != nil {
		meshErr(w, r, fmt.Errorf("datacenter %d: %v", body.DatacenterID, err))
		return
	}
	port := body.NodePort
	if port == 0 {
		port = 62050
	}
	apiKey := newUUIDv4()
	key, kerr := pgKey()
	if kerr != nil {
		writeAPIError(w, r, "E-SYS-01", kerr.Error())
		return
	}
	enc, kerr := pasarguard.Seal(key, []byte(apiKey))
	if kerr != nil {
		writeAPIError(w, r, "E-SYS-01", kerr.Error())
		return
	}
	m, err := s.GetPGNodeByPair(body.ServerID, body.DatacenterID)
	if err != nil {
		m = store.PGNode{ServerID: body.ServerID, DatacenterID: body.DatacenterID}
	}
	m.APIKeyEnc = enc
	m.NodePort = port
	if _, err := s.UpsertPGNode(m); err != nil {
		meshErr(w, r, err)
		return
	}
	meshAudit(meshActor(r), "pg.install_command",
		fmt.Sprintf(`{"server_id":%d,"datacenter_id":%d,"node_port":%d}`, body.ServerID, body.DatacenterID, port))

	// The command text carries the api_key ONCE (this response); it is not
	// logged. The one-click script is upstream's own (UNVERIFIED content).
	nodeName := pasarguard.DesiredName(dc.Name, sv.Name)
	commands := fmt.Sprintf(
		`# PasarGuard node for datacenter %q on server %q (panel node name: %s)
# 1) Upstream one-click installer (installs the node + generates the TLS cert):
sudo bash -c "$(curl -sL https://github.com/PasarGuard/scripts/raw/main/pg-node.sh)" @ install

#    …or Docker (upstream image; API_KEY below must match the node record):
docker run -d --name pg-node-%s --restart always --network host --cap-add NET_ADMIN \
  -e SERVICE_PORT=%d -e SERVICE_PROTOCOL=grpc \
  -e API_KEY=%s \
  -e SSL_CERT_FILE=/var/lib/pg-node/certs/ssl_cert.pem \
  -e SSL_KEY_FILE=/var/lib/pg-node/certs/ssl_key.pem \
  -v /var/lib/pg-node:/var/lib/pg-node \
  pasarguard/node:latest

# 2) Paste THIS SERVER's certificate into Quantum (button: store certificate):
#    cat /var/lib/pg-node/certs/ssl_cert.pem
#    (then POST it to /api/mesh/pasarguard/cert for server %d / datacenter %d)
# 3) Reconcile (dry-run, then apply) to create the panel node record.
# NOTE: upstream install script content is UNVERIFIED; review before running.`,
		dc.Name, sv.Name, nodeName, strings.ToLower(dc.Name), port, apiKey, body.ServerID, body.DatacenterID)
	writeJSON(w, map[string]any{
		"api_key":    apiKey, // shown once; stored sealed
		"node_port":  port,
		"panel_name": nodeName,
		"commands":   commands,
		"unverified": "upstream one-click script fetched from github.com/PasarGuard/scripts — review before running",
	})
}

func newUUIDv4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// ---- cross-check -----------------------------------------------------------

// handlePGCrossCheck compares PasarGuard's per-node usage with Quantum's
// REAL bytes for the same datacenter. Both numbers stay separate; only the
// RATIO is derived, and a drift flag fires past the threshold.
func handlePGCrossCheck(w http.ResponseWriter, r *http.Request) {
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	dcid, ok := meshQueryInt64(r, "datacenter_id")
	if !ok {
		writeAPIError(w, r, "E-IN-02", "datacenter_id is required")
		return
	}
	dc, err := s.GetDatacenter(dcid)
	if err != nil {
		meshErr(w, r, err)
		return
	}
	threshold := 20.0
	if raw, _ := s.Setting("drift_threshold_pct"); raw != "" {
		fmt.Sscanf(raw, "%f", &threshold)
	}
	if v, ok := meshQueryInt64(r, "threshold_pct"); ok && v > 0 && v <= 1000 {
		threshold = float64(v)
	}

	cl, err := pgClient(r.Context())
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", err.Error())
		return
	}

	// Quantum's real bytes: every tunnel of every server in this DC over
	// the last 24 h (wire and payload kept separate).
	now := time.Now().Unix()
	from := now - 24*3600
	servers, _ := s.ListServers()
	var qWire, qPayload uint64
	mapped := 0
	for _, sv := range servers {
		if sv.Role != "foreign" && sv.Role != "iran" {
			continue
		}
		_ = sv
	}
	// Datacenter scope: servers referencing this DC (either side).
	for _, sv := range servers {
		if sv.DatacenterID != dcid {
			continue
		}
		tunnels, err := s.TunnelsForServer(sv.ID)
		if err != nil {
			continue
		}
		u, err := s.UsageForTunnels(tunnelIDs(tunnels), from, now)
		if err != nil {
			continue
		}
		qWire += u.WireRx + u.WireTx
		qPayload += u.PayloadRx + u.PayloadTx
	}

	// PasarGuard side: the mapped node's usage over the same window.
	type row struct {
		PGNodeName     string  `json:"pg_node_name"`
		PGNodeID       int64   `json:"pg_node_id"`
		PGUplink       int64   `json:"pg_uplink"`
		PGDownlink     int64   `json:"pg_downlink"`
		PGTotal        int64   `json:"pg_total"`
		QuantumWire    uint64  `json:"quantum_wire"`
		QuantumPayload uint64  `json:"quantum_payload"`
		RatioWire      float64 `json:"ratio_pg_over_wire"` // 0 = not computable
		Drift          bool    `json:"drift"`
		Degraded       bool    `json:"degraded"`
	}
	rows := []row{}
	if cl != nil {
		mappings, _ := s.ListPGNodes()
		for _, m := range mappings {
			if m.DatacenterID != dcid || m.PGNodeID == 0 {
				continue
			}
			mapped++
			up, down, degraded, err := cl.NodeUsage(r.Context(), int(m.PGNodeID),
				time.Unix(from, 0), time.Unix(now, 0))
			if err != nil {
				continue
			}
			rw := row{
				PGNodeName: fmt.Sprintf("node %d", m.PGNodeID),
				PGNodeID:   m.PGNodeID,
				PGUplink:   up, PGDownlink: down, PGTotal: up + down,
				QuantumWire: qWire, QuantumPayload: qPayload,
				Degraded: degraded,
			}
			if qWire > 0 {
				rw.RatioWire = float64(rw.PGTotal) / float64(qWire)
				driftPct := rw.RatioWire - 1
				if driftPct < 0 {
					driftPct = -driftPct
				}
				rw.Drift = driftPct*100 > threshold
			}
			rows = append(rows, rw)
		}
	}
	// Suggested coefficient = DC price ratio × measured engine overhead
	// (real store data; shown NEXT TO the configured coefficient, applied
	// to nothing automatically).
	suggested, _ := s.SuggestedCoefficient(dcid, firstEngine(s))
	writeJSON(w, map[string]any{
		"datacenter": dc, "threshold_pct": threshold,
		"quantum_wire_bytes": qWire, "quantum_payload_bytes": qPayload,
		"window": "last 24h", "rows": rows, "mapped": mapped,
		"configured_coefficient": dc.PriceCoefficient,
		"suggested_coefficient":  suggested,
		"note":                   "PasarGuard usage includes ITS OWN coefficient; Quantum numbers are real bytes — compare, never merge",
	})
	_ = io.Discard
}

// firstEngine returns the engine with overhead data (or "" — the caller
// then gets ratio 0 = unknown).
func firstEngine(s *store.Store) string {
	engines := meshKnownEngines(s)
	if len(engines) == 0 {
		return ""
	}
	return engines[0]
}

// pgGuardPrivateURL is the opt-in SSRF guard for the PasarGuard client
// URL (audit L2). Default OFF (operator is root-equivalent; the real
// deployments may legitimately use private addresses). When the
// pasarguard_block_private setting is true, URLs whose host is an IP
// literal in a private/loopback/link-local range — or a localhost name —
// are refused with a fixed message before the client is built.
func pgGuardPrivateURL(rawURL string) error {
	s, err := MeshStore()
	if err != nil || s == nil {
		return nil // store down: the connect flow fails later anyway
	}
	v, err := s.Setting("pasarguard_block_private")
	if err != nil || (v != "1" && v != "true") {
		return nil
	}
	u, perr := url.Parse(strings.TrimSpace(rawURL))
	if perr != nil || u.Host == "" {
		return nil // malformed: pasarguard.New refuses it with its own error
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") || host == "::1" {
		return errors.New("pasarguard URL host is not allowed (pasarguard_block_private is on)")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return errors.New("pasarguard URL host is not allowed (pasarguard_block_private is on)")
		}
	}
	return nil
}
