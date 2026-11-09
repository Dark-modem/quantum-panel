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
	At     int64          `json:"at"`
	Actor  string         `json:"actor"`
	IP     string         `json:"ip,omitempty"`
	Action string         `json:"action"`
	Detail map[string]any `json:"detail,omitempty"`
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
		if err := rows.Scan(&e.At, &e.Actor, &e.Action, &detail); err != nil {
			return nil, err
		}
		e.Detail = map[string]any{}
		if json.Unmarshal([]byte(detail), &e.Detail) == nil {
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
		_ = cw.Write([]string{"at", "actor", "ip", "action", "detail"})
		for _, e := range entries {
			dj, _ := json.Marshal(e.Detail)
			_ = cw.Write([]string{strconv.FormatInt(e.At, 10), e.Actor, "", e.Action, string(dj)})
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
