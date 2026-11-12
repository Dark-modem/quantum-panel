package panel

// Agent registry: the controller-side store of enrolled endpoint agents and
// their one-time enrollment tokens (agents.json), plus the management API
// handlers the panel UI calls. The WebSocket hub lives in agenthub.go; the
// wire types live in agentproto.go.
//
// Storage follows the topology.go conventions: one JSON file under configDir
// guarded by its own mutex, atomic 0600 replaces via temp-file + rename, and
// a corrupt file is moved aside (.bad) so the panel keeps booting. Plaintext
// agent keys and enrollment tokens are shown once and only ever stored as
// SHA-256 hashes — they are never logged.

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	"unicode"
	"unicode/utf8"
)

// agentRecord is one enrolled endpoint agent. KeyHash is sha256 hex of the
// agent key (the plaintext key is shown once at enrollment). Desired is the
// pending desired state: it stays until the agent acks it applied.
type agentRecord struct {
	ID       string   `json:"id"` // "agt_" + 12 hex chars
	Name     string   `json:"name"`
	KeyHash  string   `json:"key_hash"`          // sha256 hex of the agent key
	Role     string   `json:"role,omitempty"`    // "iran"|"foreign" chosen at token creation
	Country  string   `json:"country,omitempty"` // ISO-3166 alpha-2 host country of a foreign agent
	Caps     []string `json:"caps,omitempty"`
	Version  string   `json:"version,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	OS       string   `json:"os,omitempty"`
	Arch     string   `json:"arch,omitempty"`
	PublicIP string   `json:"public_ip,omitempty"` // last WS/enroll remote address
	// PublicIPOverride is the operator-set explicit mesh address; when
	// non-empty it wins over the auto-detected PublicIP (Phase-4: a way
	// to set the address explicitly behind NAT/carrier-grade NAT).
	PublicIPOverride string                    `json:"public_ip_override,omitempty"`
	Status           string                    `json:"status"` // "online"|"offline"|"revoked"
	CreatedAt        int64                     `json:"created_at"`
	LastSeen         int64                     `json:"last_seen,omitempty"`
	AppliedRevision  string                    `json:"applied_revision,omitempty"`
	Desired          *agentDesired             `json:"desired,omitempty"`      // pending until acked
	LastAck          *agentAckRecord           `json:"last_ack,omitempty"`     // latest ack for a desired revision
	LastState        *agentStateReport         `json:"last_state,omitempty"`   // latest heartbeat state
	PrevTraffic      *agentTrafficReport       `json:"prev_traffic,omitempty"` // previous heartbeat's counters (for deltas)
	TrafficRate      map[string]agentIfaceRate `json:"traffic_rate,omitempty"`
	Revoked          bool                      `json:"revoked,omitempty"`
	LastError        string                    `json:"last_error,omitempty"`
}

// agentAckRecord is what the agent answered about one desired revision.
type agentAckRecord struct {
	Revision string `json:"revision"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	At       int64  `json:"at"`
}

// agentIfaceRate is the controller-derived bytes/sec for one interface,
// computed from two consecutive heartbeats' cumulative counters.
type agentIfaceRate struct {
	Name  string  `json:"name"`
	Kind  string  `json:"kind"`
	RxBps float64 `json:"rx_bps"`
	TxBps float64 `json:"tx_bps"`
	At    int64   `json:"at"`
}

// agentEnrollToken is one single-use enrollment token. Only the SHA-256
// hash and an 8-char display prefix are stored; the plaintext is returned
// exactly once by handleAgentTokenPost.
type agentEnrollToken struct {
	ID          string `json:"id"`
	TokenHash   string `json:"token_hash"`
	TokenPrefix string `json:"token_prefix"` // first 8 chars for UI display
	Role        string `json:"role"`
	Country     string `json:"country,omitempty"`
	Label       string `json:"label,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
	UsedBy      string `json:"used_by,omitempty"`
	UsedAt      int64  `json:"used_at,omitempty"`
	Revoked     bool   `json:"revoked,omitempty"`
}

const (
	agentRegistryVersion = 1
	agentRegistryMaxFile = 4 << 20
	agentMaxRecords      = 200
	agentMaxTokens       = 100
	agentMaxBody         = 16 << 10
	agentKeyBytes        = 24 // 48 hex chars
	agentIDBytes         = 6  // "agt_" + 12 hex chars
	agentStatusOnline    = "online"
	agentStatusOffline   = "offline"
	agentStatusRevoked   = "revoked"
)

var agentRegMu sync.Mutex // guards agents.json load/mutate/save, independent of other locks

// agentErrUnknownAgent marks operations on an ID that is not registered.
var agentErrUnknownAgent = fmt.Errorf("agent not found")

// agentCommandTimeout is the round-trip budget handleAgentCommandPost gives
// an agent. A var so tests can exercise the timeout path quickly.
var agentCommandTimeout = 20 * time.Second

type agentRegistryFile struct {
	Version int                `json:"version"`
	Agents  []agentRecord      `json:"agents"`
	Tokens  []agentEnrollToken `json:"tokens"`
}

func agentRegistryPath() string { return filepath.Join(configDir, "agents.json") }

// agentRegistryLoad reads agents.json. A missing file is an empty registry.
// A corrupt file is renamed agents.json.bad and the registry starts empty,
// logged loudly — a panel restart must never be blocked by a damaged store.
// The file is read and closed before the .bad rename (Windows cannot rename
// an open file). Every load forces non-revoked agents offline: the file may
// be the stale leftover of a crashed process, and only a live hub session
// may claim online (the hub re-marks its sessions right after loading).
// Caller holds agentRegMu.
func agentRegistryLoad() ([]agentRecord, []agentEnrollToken, error) {
	path := agentRegistryPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []agentRecord{}, []agentEnrollToken{}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(data) > agentRegistryMaxFile {
		return nil, nil, fmt.Errorf("agent registry file too large")
	}
	var stored agentRegistryFile
	if err := json.Unmarshal(data, &stored); err != nil {
		bad := path + ".bad"
		if rerr := os.Rename(path, bad); rerr == nil {
			log.Printf("[agents] AGENT REGISTRY CORRUPT — moved to %s; starting empty", bad)
		} else {
			log.Printf("[agents] AGENT REGISTRY CORRUPT — could not move it aside: %v", rerr)
		}
		return []agentRecord{}, []agentEnrollToken{}, nil
	}
	if stored.Version != agentRegistryVersion {
		return nil, nil, fmt.Errorf("unsupported agent registry version %d", stored.Version)
	}
	if stored.Agents == nil {
		stored.Agents = []agentRecord{}
	}
	if stored.Tokens == nil {
		stored.Tokens = []agentEnrollToken{}
	}
	for i := range stored.Agents {
		if !stored.Agents[i].Revoked {
			stored.Agents[i].Status = agentStatusOffline
		}
	}
	return stored.Agents, stored.Tokens, nil
}

// saveAgentRegistry atomically replaces agents.json (0600, temp + rename);
// a failed write leaves the previous file intact. Caller holds agentRegMu.
func saveAgentRegistry(agents []agentRecord, tokens []agentEnrollToken) error {
	data, err := json.MarshalIndent(agentRegistryFile{
		Version: agentRegistryVersion, Agents: agents, Tokens: tokens,
	}, "", "  ")
	if err != nil || len(data) > agentRegistryMaxFile {
		return fmt.Errorf("cannot encode agent registry")
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(configDir, ".agents-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, agentRegistryPath())
}

// agentRegistryUpdate applies fn to one agent record and persists the store.
// agentRegMu must NOT be held (nested use would deadlock).
func agentRegistryUpdate(id string, fn func(*agentRecord)) error {
	agentRegMu.Lock()
	defer agentRegMu.Unlock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		return err
	}
	found := false
	for i := range agents {
		if agents[i].ID == id {
			fn(&agents[i])
			found = true
			break
		}
	}
	if !found {
		return agentErrUnknownAgent
	}
	return saveAgentRegistry(agents, tokens)
}

// agentSha256Hex is the stored form of every agent key and enroll token.
func agentSha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// agentValidText trims and bounds a free-text registry field.
func agentValidText(s string, max int) string {
	s = strings.TrimSpace(s)
	if !utf8.ValidString(s) || len(s) > max {
		return ""
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return ""
		}
	}
	return s
}

// agentValidCaps sanitizes the capability list.
func agentValidCaps(caps []string) []string {
	out := []string{}
	for _, c := range caps {
		c = agentValidText(c, 64)
		if c != "" {
			out = append(out, c)
		}
		if len(out) >= 16 {
			break
		}
	}
	return out
}

// ---- enrollment (unauthenticated; token is the credential) ----

// agentPublicIPUsable reports whether an IP address is a real public
// address: not loopback, not unspecified, not link-local. The mesh hub
// address must never be a 127.0.0.1 or ::1 enrollment artifact
// (Phase-4 bug fix: a 127.0.0.1 enrollment poisoned the mesh hub
// address).
func agentPublicIPUsable(ipStr string) bool {
	ip := net.ParseIP(strings.TrimSpace(ipStr))
	if ip == nil {
		return false
	}
	return !ip.IsLoopback() && !ip.IsUnspecified() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast()
}

// agentSafePublicIP returns the IP if usable, else "" (the mesh hub
// address must never be a loopback/link-local artifact).
func agentSafePublicIP(ipStr string) string {
	if agentPublicIPUsable(ipStr) {
		return strings.TrimSpace(ipStr)
	}
	return ""
}

// handleAgentEnroll trades a one-time enrollment token for a permanent
// agent identity. POST, no panel session: the endpoint agent calls this
// before it has credentials. Errors are deliberately generic so scanners
// cannot distinguish unknown/used/expired/revoked tokens.
func handleAgentEnroll(w http.ResponseWriter, r *http.Request) {
	ip := ClientIP(r)
	token := strings.TrimSpace(r.Header.Get(agentEnrollHeader))
	if token == "" {
		LogSecurityAudit("agent_enroll_rejected", "agent", ip, "missing enroll token")
		agentPlainError(w, http.StatusUnauthorized, "enrollment denied")
		return
	}
	var body struct {
		Name     string   `json:"name"`
		Hostname string   `json:"hostname"`
		OS       string   `json:"os"`
		Arch     string   `json:"arch"`
		Version  string   `json:"version"`
		Caps     []string `json:"caps"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, agentMaxBody)).Decode(&body); err != nil {
		agentPlainError(w, http.StatusBadRequest, "invalid enrollment request")
		return
	}

	sumHex := agentSha256Hex(token)
	agentRegMu.Lock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		agentRegMu.Unlock()
		log.Printf("[agents] cannot read agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot read agent registry")
		return
	}
	now := time.Now().Unix()
	var tok *agentEnrollToken
	for i := range tokens {
		if subtle.ConstantTimeCompare([]byte(tokens[i].TokenHash), []byte(sumHex)) == 1 {
			tok = &tokens[i]
			break
		}
	}
	if tok == nil || tok.Revoked || tok.UsedBy != "" || (tok.ExpiresAt > 0 && now >= tok.ExpiresAt) {
		agentRegMu.Unlock()
		reason := "invalid"
		if tok != nil {
			switch {
			case tok.Revoked:
				reason = "revoked"
			case tok.UsedBy != "":
				reason = "already used"
			case tok.ExpiresAt > 0 && now >= tok.ExpiresAt:
				reason = "expired"
			}
		}
		LogSecurityAudit("agent_enroll_rejected", "agent", ip, "enroll token "+reason)
		agentPlainError(w, http.StatusUnauthorized, "enrollment denied")
		return
	}
	if len(agents) >= agentMaxRecords {
		agentRegMu.Unlock()
		agentPlainError(w, http.StatusConflict, "agent registry is full")
		return
	}

	id := "agt_" + randHex(agentIDBytes)
	key := randHex(agentKeyBytes)
	name := agentValidText(body.Name, 120)
	if name == "" {
		if name = agentValidText(body.Hostname, 120); name == "" {
			name = "agent"
		}
	}
	rec := agentRecord{
		ID:        id,
		Name:      name,
		KeyHash:   agentSha256Hex(key),
		Role:      tok.Role,
		Country:   tok.Country,
		Caps:      agentValidCaps(body.Caps),
		Version:   agentValidText(body.Version, 64),
		Hostname:  agentValidText(body.Hostname, 255),
		OS:        agentValidText(body.OS, 64),
		Arch:      agentValidText(body.Arch, 64),
		PublicIP:  agentSafePublicIP(ip),
		Status:    agentStatusOffline,
		CreatedAt: now,
	}
	tok.UsedBy = id
	tok.UsedAt = now
	agents = append(agents, rec)
	if err := saveAgentRegistry(agents, tokens); err != nil {
		agentRegMu.Unlock()
		log.Printf("[agents] cannot save agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot save agent registry")
		return
	}
	agentRegMu.Unlock()

	// Mirror the enrollment into the mesh store (best effort: enrollment
	// succeeds even if the store is down; the registry file stays
	// authoritative for identity and credentials).
	meshMirrorAgentEnroll(rec)
	LogSecurityAudit("agent_enroll", id, ip, "role="+tok.Role)
	writeJSON(w, map[string]string{"id": id, "key": key, "base_path": cfg.BasePath})
}

// meshMirrorAgentEnroll upserts an enrolled agent into the store's agents
// table so the management plane sees new endpoints without a re-import.
// The key hash is NOT copied: the registry keeps the credential, the store
// only mirrors identity and session state.
func meshMirrorAgentEnroll(rec agentRecord) {
	s, err := MeshStore()
	if err != nil {
		noteStoreRetry()
		return
	}
	if err := s.UpsertAgent(store.Agent{
		ID: rec.ID, Name: rec.Name, Role: rec.Role, Country: rec.Country, Status: rec.Status,
		Version: rec.Version, Hostname: rec.Hostname, PublicIP: rec.PublicIP,
	}); err != nil {
		log.Printf("[mesh] cannot mirror agent enrollment for %s: %v", rec.ID, err)
	}
}

// ---- enrollment token management (session-authenticated upstream) ----

// handleAgentTokenPost mints one single-use enrollment token. The plaintext
// token is returned here and never again — the store keeps only its hash.
func handleAgentTokenPost(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Role        string `json:"role"`
		Country     string `json:"country"`
		Label       string `json:"label"`
		ExpiresDays int    `json:"expires_days"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		agentPlainError(w, http.StatusBadRequest, "body must be a JSON object")
		return
	}
	if body.Role != "iran" && body.Role != "foreign" {
		agentPlainError(w, http.StatusBadRequest, "role must be iran or foreign")
		return
	}
	// Country applies to foreign agents only: an upper-case ISO-3166
	// alpha-2 code, or empty when unknown/iran.
	country := strings.ToUpper(strings.TrimSpace(body.Country))
	if body.Role == "iran" {
		country = ""
	}
	if country != "" && (len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z') {
		agentPlainError(w, http.StatusBadRequest, "country must be a 2-letter ISO code")
		return
	}
	days := body.ExpiresDays
	if days <= 0 {
		days = 7
	}
	if days > 365 {
		agentPlainError(w, http.StatusBadRequest, "expires_days must be 1-365")
		return
	}
	label := agentValidText(body.Label, 120)

	token := "enr_" + randHex(16)
	now := time.Now()
	agentRegMu.Lock()
	defer agentRegMu.Unlock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		log.Printf("[agents] cannot read agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot read agent registry")
		return
	}
	live := 0
	for _, t := range tokens {
		if !t.Revoked && t.UsedBy == "" {
			live++
		}
	}
	if live >= agentMaxTokens {
		agentPlainError(w, http.StatusConflict, "too many unused enrollment tokens")
		return
	}
	t := agentEnrollToken{
		ID:          "tok_" + randHex(agentIDBytes),
		TokenHash:   agentSha256Hex(token),
		TokenPrefix: token[:8],
		Role:        body.Role,
		Country:     country,
		Label:       label,
		CreatedAt:   now.Unix(),
		ExpiresAt:   now.Add(time.Duration(days) * 24 * time.Hour).Unix(),
	}
	if err := saveAgentRegistry(agents, append(tokens, t)); err != nil {
		log.Printf("[agents] cannot save agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot save agent registry")
		return
	}
	LogSecurityAudit("agent_token_created", cfg.Username, ClientIP(r), "role="+body.Role+" prefix="+t.TokenPrefix)
	writeJSON(w, map[string]any{"id": t.ID, "token": token, "expires_at": t.ExpiresAt, "prefix": t.TokenPrefix})
}

// handleAgentTokensGet lists tokens without hashes or plaintext.
func handleAgentTokensGet(w http.ResponseWriter, r *http.Request) {
	agentRegMu.Lock()
	_, tokens, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		log.Printf("[agents] cannot read agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot read agent registry")
		return
	}
	out := []map[string]any{}
	for _, t := range tokens {
		out = append(out, map[string]any{
			"id":         t.ID,
			"prefix":     t.TokenPrefix,
			"role":       t.Role,
			"label":      t.Label,
			"created_at": t.CreatedAt,
			"expires_at": t.ExpiresAt,
			"used_by":    t.UsedBy,
			"used_at":    t.UsedAt,
			"revoked":    t.Revoked,
		})
	}
	writeJSON(w, map[string]any{"tokens": out})
}

// handleAgentTokenRevoke disables one enrollment token (POST to match the
// repo's mutating-verb style).
func handleAgentTokenRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agentRegMu.Lock()
	defer agentRegMu.Unlock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		log.Printf("[agents] cannot read agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot read agent registry")
		return
	}
	found := false
	for i := range tokens {
		if tokens[i].ID == id {
			tokens[i].Revoked = true
			found = true
			break
		}
	}
	if !found {
		agentPlainError(w, http.StatusNotFound, "token not found")
		return
	}
	if err := saveAgentRegistry(agents, tokens); err != nil {
		log.Printf("[agents] cannot save agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot save agent registry")
		return
	}
	LogSecurityAudit("agent_token_revoked", cfg.Username, ClientIP(r), "token="+id)
	writeJSON(w, map[string]any{"status": "ok", "id": id})
}

// ---- registry API (session-authenticated upstream) ----

// agentPlainError is the registry's plain JSON error for causes that have no
// E-AGENT catalog entry (bad bodies, auth denials, disk failures).
func agentPlainError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// agentAPIError mirrors writeAPIError's {error_code,error,hint} shape for
// the E-AGENT-xx family, which has no entries in the shared errCatalog yet
// (writeAPIError would rewrite unknown codes to E-SYS-01/400). Codes:
//
//	E-AGENT-01 502 agent offline or timed out
//	E-AGENT-02 400 operation not allowed
//	E-AGENT-03 404 agent not found
func agentAPIError(w http.ResponseWriter, r *http.Request, code string, status int, detail string) {
	hints := map[string]string{
		"E-AGENT-01": "Retry once the agent shows online, or check the agent service on the endpoint.",
		"E-AGENT-02": "Use an allowlisted operation that this agent's capabilities permit.",
		"E-AGENT-03": "Reload the agent list; the agent may have been removed.",
	}
	msgs := map[string]string{
		"E-AGENT-01": "agent is offline or timed out",
		"E-AGENT-02": "operation not allowed",
		"E-AGENT-03": "agent not found",
	}
	msg := detail
	if msg == "" {
		msg = msgs[code]
	}
	ep := ""
	if r != nil {
		ep = r.Method + " " + r.URL.Path
	}
	recordError(code, ep, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error_code": code, "error": msg, "hint": hints[code]})
}

// agentPublicView is the list/dash view of an agent: no key hash, no desired
// payload (it embeds pairing tokens), no full last-state dump. The hub's
// live session wins over the persisted status so a just-connected agent
// never flashes offline between persists.
func agentPublicView(rec agentRecord) map[string]any {
	status := rec.Status
	if !rec.Revoked && agentHubOnline(rec.ID) {
		status = agentStatusOnline
	}
	if status == "" {
		status = agentStatusOffline
	}
	caps := rec.Caps
	if caps == nil {
		caps = []string{}
	}
	return map[string]any{
		"id":                  rec.ID,
		"name":                rec.Name,
		"role":                rec.Role,
		"status":              status,
		"version":             rec.Version,
		"hostname":            rec.Hostname,
		"os":                  rec.OS,
		"arch":                rec.Arch,
		"public_ip":           rec.PublicIP,
		"public_ip_override":  rec.PublicIPOverride,
		"caps":                caps,
		"last_seen":           rec.LastSeen,
		"applied_revision":    rec.AppliedRevision,
		"has_pending_desired": rec.Desired != nil,
		"revoked":             rec.Revoked,
	}
}

func handleAgentsGet(w http.ResponseWriter, r *http.Request) {
	agentRegMu.Lock()
	agents, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		log.Printf("[agents] cannot read agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot read agent registry")
		return
	}
	out := []map[string]any{}
	for _, rec := range agents {
		out = append(out, agentPublicView(rec))
	}
	writeJSON(w, map[string]any{"agents": out})
}

func handleAgentGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	agentRegMu.Lock()
	agents, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		log.Printf("[agents] cannot read agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot read agent registry")
		return
	}
	var rec agentRecord
	found := false
	for _, a := range agents {
		if a.ID == id {
			rec, found = a, true
			break
		}
	}
	if !found {
		agentAPIError(w, r, "E-AGENT-03", http.StatusNotFound, "")
		return
	}
	view := agentPublicView(rec)
	view["created_at"] = rec.CreatedAt
	view["last_ack"] = rec.LastAck
	view["last_error"] = rec.LastError
	view["last_state"] = rec.LastState
	view["traffic_rate"] = rec.TrafficRate
	if rec.Desired != nil {
		summary := map[string]any{"revision": rec.Desired.Revision}
		if rec.Desired.Matrix != nil {
			// The hub payload embeds a pairing token — summarize, never echo it.
			summary["instance"] = rec.Desired.Matrix.Instance
			summary["action"] = rec.Desired.Matrix.Action
		}
		view["pending_desired"] = summary
	}
	writeJSON(w, view)
}

// handleAgentRevoke permanently disables an agent and kicks its live
// session. The record stays (audit trail) but can never connect again.
func handleAgentRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := agentRegistryUpdate(id, func(rec *agentRecord) {
		rec.Revoked = true
		rec.Status = agentStatusRevoked
		rec.Desired = nil
	})
	if err == agentErrUnknownAgent {
		agentAPIError(w, r, "E-AGENT-03", http.StatusNotFound, "")
		return
	}
	if err != nil {
		log.Printf("[agents] cannot save agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot save agent registry")
		return
	}
	agentHubKick(id, "revoked")
	LogSecurityAudit("agent_revoked", cfg.Username, ClientIP(r), "agent="+id)
	writeJSON(w, map[string]any{"status": "ok", "id": id})
}

// handleAgentCommandPost relays one allowlisted operation to a live agent
// and waits for its result. matrix.* ops additionally require the
// "matrix" capability.
func handleAgentCommandPost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Op     string            `json:"op"`
		Params map[string]string `json:"params"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		agentAPIError(w, r, "E-AGENT-02", http.StatusBadRequest, "body must be {\"op\",\"params\"}")
		return
	}
	agentRegMu.Lock()
	agents, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err != nil {
		log.Printf("[agents] cannot read agent registry: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot read agent registry")
		return
	}
	var rec agentRecord
	found := false
	for _, a := range agents {
		if a.ID == id {
			rec, found = a, true
			break
		}
	}
	if !found {
		agentAPIError(w, r, "E-AGENT-03", http.StatusNotFound, "")
		return
	}
	if !agentValidCommands[body.Op] {
		agentAPIError(w, r, "E-AGENT-02", http.StatusBadRequest, "unknown operation "+body.Op)
		return
	}
	if strings.HasPrefix(body.Op, "matrix.") && !agentHasCap(rec.Caps, agentCapMatrix) {
		agentAPIError(w, r, "E-AGENT-02", http.StatusBadRequest, "agent lacks the matrix capability")
		return
	}
	res, err := agentHubCommand(id, body.Op, body.Params, agentCommandTimeout)
	if err != nil {
		agentAPIError(w, r, "E-AGENT-01", http.StatusBadGateway, "")
		return
	}
	writeJSON(w, res)
}

// handleAgentDesiredPost queues the Matrix desired state for one agent. The
// revision is controller-assigned (unix nanos, strictly monotonic per the
// protocol). Delivery to a live session is immediate; an offline agent
// picks the pending state up on its next connect.
func handleAgentDesiredPost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Matrix *struct {
			Instance string           `json:"instance"`
			Action   string           `json:"action"`
			Hub      *agentDesiredHub `json:"hub"`
			Bundle   string           `json:"bundle"`
		} `json:"matrix"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, agentMaxBody)).Decode(&body); err != nil || body.Matrix == nil {
		agentPlainError(w, http.StatusBadRequest, "body must be {\"matrix\":{\"instance\",\"action\",\"hub\",\"bundle\"}}")
		return
	}
	m := body.Matrix
	m.Instance = agentValidText(m.Instance, 32)
	if m.Instance == "" || (m.Action != "apply" && m.Action != "remove") {
		agentPlainError(w, http.StatusBadRequest, "matrix.instance and matrix.action (apply|remove) are required")
		return
	}
	m.Bundle = agentValidText(m.Bundle, 8192)
	if m.Hub != nil {
		if m.Hub.Family != "ipv4" && m.Hub.Family != "ipv6" {
			agentPlainError(w, http.StatusBadRequest, "hub.family must be ipv4 or ipv6")
			return
		}
		if m.Hub.Port < 0 || m.Hub.Port > 65535 {
			agentPlainError(w, http.StatusBadRequest, "hub.port must be 0-65535")
			return
		}
		if len(m.Hub.Addr) > 255 || len(m.Hub.Token) > 512 || len(m.Hub.Ports) > 20 {
			agentPlainError(w, http.StatusBadRequest, "hub payload too large")
			return
		}
		for _, p := range m.Hub.Ports {
			if p < 1 || p > 65535 {
				agentPlainError(w, http.StatusBadRequest, "hub.ports must be 1-65535")
				return
			}
		}
	}
	d := agentDesired{
		Revision: strconv.FormatInt(time.Now().UnixNano(), 10),
		Matrix: &agentDesiredMatrix{
			Instance: m.Instance,
			Action:   m.Action,
			Hub:      m.Hub,
			Bundle:   m.Bundle,
		},
	}
	if err := agentHubDeliverDesired(id, d); err != nil {
		if err == agentErrUnknownAgent {
			agentAPIError(w, r, "E-AGENT-03", http.StatusNotFound, "")
			return
		}
		log.Printf("[agents] cannot deliver desired state: %v", err)
		agentPlainError(w, http.StatusInternalServerError, "cannot save agent registry")
		return
	}
	LogSecurityAudit("agent_desired", cfg.Username, ClientIP(r),
		"agent="+id+" instance="+m.Instance+" action="+m.Action+" revision="+d.Revision)
	writeJSON(w, map[string]any{"status": "ok", "revision": d.Revision})
}

// agentHasCap reports whether the capability list contains cap.
func agentHasCap(caps []string, cap string) bool {
	for _, c := range caps {
		if c == cap {
			return true
		}
	}
	return false
}

// agentRegistryRename renames one enrolled agent (UI rename action).
func agentRegistryRename(id, name string) error {
	agentRegMu.Lock()
	defer agentRegMu.Unlock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		return err
	}
	hit := false
	for i := range agents {
		if agents[i].ID == id {
			agents[i].Name = name
			hit = true
		}
	}
	if !hit {
		return agentErrUnknownAgent
	}
	return saveAgentRegistry(agents, tokens)
}

// agentRegistryDelete removes one enrolled agent entirely (UI delete action;
// the running hub session for a live agent is dropped by the hub itself).
func agentRegistryDelete(id string) error {
	agentRegMu.Lock()
	defer agentRegMu.Unlock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		return err
	}
	var keep []agentRecord
	for _, a := range agents {
		if a.ID != id {
			keep = append(keep, a)
		}
	}
	if len(keep) == len(agents) {
		return agentErrUnknownAgent
	}
	return saveAgentRegistry(keep, tokens)
}
