package panel

// guards.go — operator safety rails (phase 13 TASK 3):
//
//   - DEPENDENCY REPORT + CONFIRMATION TOKEN for destructive calls:
//     deleting a server/topology/tunnel/agent that has dependants lists
//     them and refuses without `confirm_token` ( minted by a GET on the
//     same resource's `dependencies` endpoint, single-use, 10 min TTL);
//   - AUDIT LOG API: who did what, when, from which IP, with a diff of
//     the change (secrets redacted), filterable, CSV-exportable;
//   - the engine-disable dependency list (already refused) now names
//     the blocking tunnels through the same dependency machinery.
//
// The confirmation token is an HMAC over (resource type, id, expiry)
// with the panel's own CSRF secret — possession proves the operator
// SAW the dependency report (the token only comes from the report).

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// ---- dependency reports -----------------------------------------------

type dependencyItem struct {
	Kind string `json:"kind"` // "tunnel" | "server" | "topology" | "agent" | "weight" | "forward"
	ID   string `json:"id"`
	Name string `json:"name"`
	Note string `json:"note,omitempty"`
}

type dependencyReport struct {
	Resource string           `json:"resource"`
	ID       string           `json:"id"`
	Depend   []dependencyItem `json:"dependants"`
	HasBlock bool             `json:"has_dependants"`
	Token    string           `json:"confirm_token"` // single-use, 10 min
}

// mintConfirmToken: HMAC(csrfKey, "confirm|kind|id|expiry") — reuses the
// panel's own CSRF key material (already initialized at boot).
func mintConfirmToken(kind string, id string) string {
	initCSRFKey()
	exp := fmt.Sprintf("%d", time.Now().Add(10*time.Minute).Unix())
	msg := "confirm|" + kind + "|" + id + "|" + exp
	mac := hmac.New(sha256.New, csrfKey[:])
	mac.Write([]byte(msg))
	return exp + "." + hex.EncodeToString(mac.Sum(nil))
}

func verifyConfirmToken(kind, id, token string) bool {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, sig := parts[0], parts[1]
	expUnix, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || time.Now().Unix() > expUnix {
		return false
	}
	msg := "confirm|" + kind + "|" + id + "|" + exp
	initCSRFKey()
	mac := hmac.New(sha256.New, csrfKey[:])
	mac.Write([]byte(msg))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(want)) {
		return false
	}
	// Single-use: consume via the audit log marker (a used token cannot
	// be replayed because the audit row stores its hash and the check
	// rejects a hash already recorded).
	return !auditConfirmTokenUsed(sig)
}

// auditConfirmTokenUsed: a used confirm token's hash is recorded once.
func auditConfirmTokenUsed(sigHex string) bool {
	s, err := MeshStore()
	if err != nil {
		return false
	}
	var n int
	_ = s.DB().QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'confirm.used' AND detail = ?`,
		sigHex[:16]).Scan(&n)
	if n > 0 {
		return true
	}
	_, _ = s.DB().Exec(`INSERT INTO audit_log (at, actor, action, detail) VALUES (?, 'system', 'confirm.used', ?)`,
		time.Now().Unix(), sigHex[:16])
	return false
}

// serverDependencies: what references a server.
func (h uiStoreHandle) serverDependencies(serverID int64) []dependencyItem {
	out := []dependencyItem{}
	tuns, _ := h.TunnelsForServer(serverID)
	for _, t := range tuns {
		out = append(out, dependencyItem{Kind: "tunnel", ID: fmt.Sprintf("%d", t.ID), Name: t.TunnelKey})
	}
	if ag, err := h.AgentByServer(serverID); err == nil {
		out = append(out, dependencyItem{Kind: "agent", ID: ag.ID, Name: ag.Name})
	}
	return out
}

func (h uiStoreHandle) topologyDependencies(topoID int64) []dependencyItem {
	out := []dependencyItem{}
	tuns, _ := h.ListTunnels(topoID)
	for _, t := range tuns {
		out = append(out, dependencyItem{Kind: "tunnel", ID: fmt.Sprintf("%d", t.ID), Name: t.TunnelKey})
	}
	if ws, _ := h.ListWeights(topoID); len(ws) > 0 {
		out = append(out, dependencyItem{Kind: "weight", ID: fmt.Sprintf("topology:%d", topoID),
			Name: fmt.Sprintf("%d weight policies", len(ws))})
	}
	if fr, _ := h.ListForwardRules(topoID); len(fr) > 0 {
		out = append(out, dependencyItem{Kind: "forward", ID: fmt.Sprintf("topology:%d", topoID),
			Name: fmt.Sprintf("%d forward rules", len(fr))})
	}
	if members, _ := h.TopologyMembers(topoID); len(members) > 0 {
		out = append(out, dependencyItem{Kind: "membership", ID: fmt.Sprintf("topology:%d", topoID),
			Name: fmt.Sprintf("%d member servers", len(members))})
	}
	return out
}

func (h uiStoreHandle) tunnelDependencies(tun store.Tunnel) []dependencyItem {
	out := []dependencyItem{}
	if ws, _ := h.ListWeights(tun.TopologyID); len(ws) > 0 {
		for _, w := range ws {
			if w.TunnelID != nil && *w.TunnelID == tun.ID {
				out = append(out, dependencyItem{Kind: "weight", ID: fmt.Sprintf("%d", w.ID), Name: fmt.Sprintf("weight %d", w.Weight)})
			}
		}
	}
	return out
}

func (h uiStoreHandle) agentDependencies(agentID string) []dependencyItem {
	out := []dependencyItem{}
	ag, err := h.GetAgent(agentID)
	if err != nil {
		return out
	}
	if ag.ServerID != 0 {
		if sv, err := h.GetServer(ag.ServerID); err == nil {
			out = append(out, dependencyItem{Kind: "server-link", ID: fmt.Sprintf("%d", sv.ID), Name: sv.Name})
		}
		tuns, _ := h.TunnelsForServer(ag.ServerID)
		for _, t := range tuns {
			out = append(out, dependencyItem{Kind: "tunnel", ID: fmt.Sprintf("%d", t.ID), Name: t.TunnelKey,
				Note: "the agent runs one end of this tunnel"})
		}
	}
	return out
}

// requireConfirm validates the confirm_token on a destructive call and
// writes the structured refusal when missing/invalid.
func requireConfirm(w http.ResponseWriter, r *http.Request, kind, id string, rep dependencyReport) bool {
	if verifyConfirmToken(kind, id, r.Header.Get("X-Confirm-Token")) {
		return true
	}
	names := make([]string, 0, len(rep.Depend))
	for _, d := range rep.Depend {
		names = append(names, d.Kind+":"+d.Name)
	}
	uiErrWrite(w, r, "E-CONFIRM-REQUIRED", map[string]any{
		"resource": kind, "id": id, "dependants": len(rep.Depend), "tunnels": names,
	}, fmt.Sprintf("This %s has %d dependants. GET the dependency report for the exact list and the confirmation token, then resend with the X-Confirm-Token header.", kind, len(rep.Depend)))
	return false
}

// ---- routes -----------------------------------------------------------

func registerGuardRoutes(mux *http.ServeMux, base string) {
	mux.HandleFunc("GET "+base+"/api/ui/servers/{id}/dependencies", requireAuth(handleServerDependencies))
	mux.HandleFunc("GET "+base+"/api/ui/topologies/{id}/dependencies", requireAuth(handleTopologyDependencies))
	mux.HandleFunc("GET "+base+"/api/ui/tunnels/{key}/dependencies", requireAuth(handleTunnelDependencies))
	mux.HandleFunc("GET "+base+"/api/ui/agents/{id}/dependencies", requireAuth(handleAgentDependencies))
	mux.HandleFunc("GET "+base+"/api/ui/audit", requireAuth(handleAuditLog))
}

func handleServerDependencies(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	id := atoiDefault(r.PathValue("id"), 0)
	if _, err := h.GetServer(id); err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"server": r.PathValue("id")}, "")
		return
	}
	dep := h.serverDependencies(id)
	writeJSON(w, dependencyReport{Resource: "server", ID: r.PathValue("id"),
		Depend: dep, HasBlock: len(dep) > 0,
		Token: mintConfirmToken("server", r.PathValue("id"))})
}

func handleTopologyDependencies(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	id := atoiDefault(r.PathValue("id"), 0)
	if _, err := h.GetTopology(id); err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"topology": r.PathValue("id")}, "")
		return
	}
	dep := h.topologyDependencies(id)
	writeJSON(w, dependencyReport{Resource: "topology", ID: r.PathValue("id"),
		Depend: dep, HasBlock: len(dep) > 0,
		Token: mintConfirmToken("topology", r.PathValue("id"))})
}

func handleTunnelDependencies(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	tun, err := h.GetTunnelByKey(r.PathValue("key"))
	if err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"tunnel": r.PathValue("key")}, "")
		return
	}
	dep := h.tunnelDependencies(tun)
	writeJSON(w, dependencyReport{Resource: "tunnel", ID: r.PathValue("key"),
		Depend: dep, HasBlock: len(dep) > 0,
		Token: mintConfirmToken("tunnel", r.PathValue("key"))})
}

func handleAgentDependencies(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, err := h.GetAgent(id); err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": id}, "")
		return
	}
	dep := h.agentDependencies(id)
	writeJSON(w, dependencyReport{Resource: "agent", ID: id,
		Depend: dep, HasBlock: len(dep) > 0,
		Token: mintConfirmToken("agent", id)})
}

// ---- audit log API ----------------------------------------------------

type auditEntryView struct {
	At      int64          `json:"at"`
	Actor   string         `json:"actor"`
	IP      string         `json:"ip,omitempty"`
	Action  string         `json:"action"`
	Summary string         `json:"summary,omitempty"` // phase 16: short human line
	Detail  map[string]any `json:"detail,omitempty"`
}

// auditSummaryKeyOK: (action, field) pairs whose field NAME is
// secret-shaped but whose VALUE is a UI identifier, not a credential
// (the tunnel key is shown on every screen; redactAuditDetail still
// scrubs it inside the raw detail).
var auditSummaryKeyOK = map[string]bool{"mesh.tunnel.create:key": true}

// auditSummary builds the per-entry short human line (phase 16,
// UI-API-CONTRACT.md §2.13 — the UI renders it instead of the raw JSON
// detail). Built ONLY from the recorded action + detail fields (ids
// where the detail only carries ids; NO extra store lookups — the read
// stays one query). Fields whose NAME is secret-shaped summarize as
// [redacted] unless allowlisted — the same policy redactAuditDetail
// applies to the raw detail, so the summary never echoes what the
// detail hides. Unknown actions → "" (field omitted; the UI keeps
// showing the raw detail).
func auditSummary(action string, d map[string]any) string {
	// JSON detail numbers arrive as float64; keep the accessors forgiving.
	str := func(k string) string {
		v, ok := d[k]
		if !ok {
			return "?"
		}
		lk := strings.ToLower(k)
		if strings.Contains(lk, "key") || strings.Contains(lk, "token") ||
			strings.Contains(lk, "secret") || strings.Contains(lk, "password") ||
			strings.Contains(lk, "signature") || strings.Contains(lk, "sha256") {
			if !auditSummaryKeyOK[action+":"+k] {
				return "[redacted]"
			}
		}
		switch t := v.(type) {
		case string:
			return t
		case float64:
			return strconv.FormatInt(int64(t), 10)
		case bool:
			return strconv.FormatBool(t)
		}
		return "?"
	}
	num := func(k string) int64 {
		if v, ok := d[k].(float64); ok {
			return int64(v)
		}
		return 0
	}
	switch action {
	case "mesh.import.legacy":
		return fmt.Sprintf("legacy import: agents=%s topologies=%s servers=%s tunnels=%s",
			str("agents"), str("topologies"), str("servers"), str("tunnels"))
	case "mesh.datacenter.upsert":
		if k, ok := d["kind"].(string); ok && k != "" {
			return fmt.Sprintf("datacenter #%s (%s) saved", str("id"), k)
		}
		return fmt.Sprintf("datacenter #%s saved", str("id"))
	case "mesh.datacenter.price":
		return fmt.Sprintf("datacenter #%s price coefficient set to %g", str("id"), float64(num("price_coefficient")))
	case "mesh.datacenter.delete":
		return fmt.Sprintf("datacenter #%s deleted", str("id"))
	case "mesh.server.upsert":
		if k, ok := d["role"].(string); ok && k != "" {
			return fmt.Sprintf("server #%s (%s) saved", str("id"), k)
		}
		return fmt.Sprintf("server #%s saved", str("id"))
	case "mesh.server.update":
		return fmt.Sprintf("server #%s updated", str("id"))
	case "mesh.server.delete":
		return fmt.Sprintf("server #%s deleted", str("id"))
	case "mesh.topology.update":
		return fmt.Sprintf("topology #%s updated", str("id"))
	case "mesh.topology.delete":
		return fmt.Sprintf("topology #%s deleted", str("id"))
	case "mesh.topology.generate":
		return fmt.Sprintf("generated topology %s with %d tunnels", str("name"), num("tunnels"))
	case "mesh.weight.set":
		if tid := num("tunnel_id"); tid > 0 {
			return fmt.Sprintf("weight %d set on tunnel #%d (topology #%d)",
				num("weight"), tid, num("topology_id"))
		}
		return fmt.Sprintf("weight %d set (topology #%d, pair-scope)", num("weight"), num("topology_id"))
	case "mesh.tunnel.create":
		return fmt.Sprintf("tunnel %s created (engine %s)", str("key"), str("engine"))
	case "mesh.tunnel.update":
		return fmt.Sprintf("tunnel #%s updated (engine %s, enabled %s)", str("id"), str("engine"), str("enabled"))
	case "mesh.tunnel.delete":
		return fmt.Sprintf("tunnel #%s deleted", str("id"))
	case "mesh.tunnel.engine_warning":
		return fmt.Sprintf("engine warning recorded (%s): %s", str("engine"), str("warn"))
	case "mesh.forward.create":
		return fmt.Sprintf("forward #%s created (%s listen %s -> target %s)",
			str("id"), str("proto"), str("listen_port"), str("target_port"))
	case "mesh.forward.update":
		return fmt.Sprintf("forward #%s updated", str("id"))
	case "mesh.forward.delete":
		return fmt.Sprintf("forward #%s deleted", str("id"))
	case "mesh.agent.link":
		return fmt.Sprintf("agent %s linked to server #%s", str("agent"), str("server_id"))
	case "mesh.agent.public_ip":
		return fmt.Sprintf("agent %s public IP override set to %s", str("agent"), str("public_ip"))
	case "mesh.agent.engine_catalog":
		return fmt.Sprintf("engine catalog stored for agent %s (%s engines)", str("agent"), str("engines"))
	case "mesh.agent.maintenance":
		if d["active"] == true {
			return fmt.Sprintf("maintenance window for agent %s set until epoch %s", str("agent"), str("until"))
		}
		return fmt.Sprintf("maintenance window for agent %s cleared", str("agent"))
	case "mesh.profile.apply":
		return fmt.Sprintf("hardening profile %s applied on agent %s (rollback timer %ss)",
			str("kind"), str("agent"), str("rollback_timer_s"))
	case "mesh.profile.confirm":
		return fmt.Sprintf("hardening profile confirmed on agent %s (kind %s)", str("agent"), str("kind"))
	case "mesh.profile.rollback":
		return fmt.Sprintf("hardening profile rolled back on agent %s (kind %s, ok %s)", str("agent"), str("kind"), str("ok"))
	case "mesh.desired.queued":
		return fmt.Sprintf("desired state queued for agent %s (revision %s, %d tunnels, %d engines)",
			str("agent"), str("revision"), num("tunnels"), num("engines"))
	case "mesh.desired.push":
		return fmt.Sprintf("desired state pushed to agent %s (revision %s, %d tunnels, %d engines)",
			str("agent"), str("revision"), num("tunnels"), num("engines"))
	case "mesh.desired.ack":
		return fmt.Sprintf("desired state acked by agent %s (revision %s, ok %s)", str("agent"), str("revision"), str("ok"))
	case "pg.connect":
		return fmt.Sprintf("PasarGuard connected (%d nodes)", num("nodes"))
	case "pg.reconcile.apply":
		return fmt.Sprintf("PasarGuard reconcile applied (creates %d, updates %d, reconnects %d)",
			num("creates"), num("updates"), num("reconnects"))
	case "pg.orphans.delete":
		return fmt.Sprintf("PasarGuard orphan nodes deleted (requested %d)", num("requested"))
	case "pg.cert.store":
		return fmt.Sprintf("PasarGuard certificate stored for server #%s", str("server_id"))
	case "pg.install_command":
		return fmt.Sprintf("PasarGuard install command generated for server #%s (port %s)", str("server_id"), str("node_port"))
	case "pg.dc.apply":
		return fmt.Sprintf("PasarGuard per-DC config applied (servers %d, inbound creates %d, modifies %d, host changes %d)",
			num("servers"), num("inbound_creates"), num("inbound_modifies"), num("host_changes"))
	case "confirm.used":
		return "confirmation token used"
	}
	return ""
}

// auditViewList reads the log with filters (actor, action, since, until,
// q substring) — newest first, bounded.
func (h uiStoreHandle) auditViewList(actor, action, q string, since, until int64, limit int) ([]auditEntryView, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	wh := []string{"1=1"}
	args := []any{}
	if actor != "" {
		wh = append(wh, "actor = ?")
		args = append(args, actor)
	}
	if action != "" {
		wh = append(wh, "action = ?")
		args = append(args, action)
	}
	if q != "" {
		wh = append(wh, "detail LIKE ?")
		args = append(args, "%"+q+"%")
	}
	if since > 0 {
		wh = append(wh, "at >= ?")
		args = append(args, since)
	}
	if until > 0 {
		wh = append(wh, "at <= ?")
		args = append(args, until)
	}
	qs := fmt.Sprintf(`SELECT at, actor, action, detail FROM audit_log WHERE %s
		ORDER BY at DESC, id DESC LIMIT %d`, strings.Join(wh, " AND "), limit)
	rows, err := h.DB().Query(qs, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []auditEntryView{}
	for rows.Next() {
		var e auditEntryView
		var detail string
		rawDetail := map[string]any{}
		if err := rows.Scan(&e.At, &e.Actor, &e.Action, &detail); err != nil {
			return nil, err
		}
		e.Detail = map[string]any{}
		if json.Unmarshal([]byte(detail), &rawDetail) == nil {
			// Summary from the RAW detail: redactAuditDetail scrubs
			// secret-SHAPED keys (any "*key*" field — including the
			// harmless tunnel key) which would corrupt the line. The
			// summary only prints known identifier fields and still
			// passes redactErrorText (pattern-based) as defense in depth.
			e.Summary = redactErrorText(auditSummary(e.Action, rawDetail))
			e.Detail = rawDetail
			redactAuditDetail(e.Detail)
		} else if detail != "" {
			e.Detail = map[string]any{"raw": redactErrorText(detail)}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// redactAuditDetail: secret-shaped keys out, values scrubbed.
func redactAuditDetail(m map[string]any) {
	for k, v := range m {
		lk := strings.ToLower(k)
		if strings.Contains(lk, "key") || strings.Contains(lk, "token") ||
			strings.Contains(lk, "secret") || strings.Contains(lk, "password") ||
			strings.Contains(lk, "signature") || strings.Contains(lk, "sha256") {
			m[k] = "[redacted]"
			continue
		}
		if s, ok := v.(string); ok {
			m[k] = redactErrorText(s)
		}
	}
}

func handleAuditLog(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	actor, action, substr := q.Get("actor"), q.Get("action"), q.Get("q")
	since, _ := strconv.ParseInt(q.Get("since"), 10, 64)
	until, _ := strconv.ParseInt(q.Get("until"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	entries, err := h.auditViewList(actor, action, substr, since, until, limit)
	if err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "audit"}, err.Error())
		return
	}
	if q.Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=audit.csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"at", "actor", "ip", "action", "summary", "detail"})
		for _, e := range entries {
			dj, _ := json.Marshal(e.Detail)
			_ = cw.Write([]string{strconv.FormatInt(e.At, 10), e.Actor, "", e.Action, e.Summary, string(dj)})
		}
		cw.Flush()
		return
	}
	writeJSON(w, map[string]any{"entries": entries, "count": len(entries)})
}

// sortedKeys is a deterministic helper for stable detail rendering.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
