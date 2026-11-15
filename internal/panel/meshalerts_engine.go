// meshalerts_engine.go — TASK 3: the mesh alert engine (new surface,
// parallel to the pre-existing link-state alerts in alerts.go — that one
// watches HUB LINK states; this one watches agents, tunnels, hardening
// confirms, certs, disk, traffic). Rules over state the panel has:
//
//	agent_offline      registry last_seen/status older than N minutes
//	tunnel_down        StateReportV2 runtime Up=false while fresh
//	foreign_all_down   every tunnel to one foreign server down
//	hardening_expiring pending safe-apply confirm near its deadline
//	cert_expiry        HTTPS cert expires within N days
//	disk_usage         config-dir filesystem usage over N%
//	traffic_drop       tunnel Up but wire bytes frozen across 2 ticks
//
// Delivery: pluggable notifiers (meshalerts_notify.go) with per-key
// DEDUP (one open alert = one message, refreshed after a cooldown),
// RECOVERY messages on clear, and a STARTUP QUIET window (no storm
// after a panel restart).

package panel

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/pasarguard"
)

const (
	meshAlertQuietStart    = 10 * time.Minute // no alerts right after boot
	meshAlertCooldown      = 30 * time.Minute // re-notify an OPEN alert at most this often
	meshAlertTick          = 30 * time.Second
	meshAlertMaxHistory    = 200
	meshAlertCertWarnDays  = 14
	meshAlertHardeningFrac = 0.2 // warn when <20% of the confirm window remains
)

// meshAlertSettings is the operator-configurable part (persisted sealed).
type meshAlertSettings struct {
	Enabled         bool   `json:"enabled"`
	AgentOfflineMin int    `json:"agent_offline_min"`        // default 5
	CertExpireDays  int    `json:"cert_expire_days"`         // default 14
	DiskMaxPercent  int    `json:"disk_max_percent"`         // default 90
	TelegramToken   string `json:"telegram_token,omitempty"` // sealed at rest
	TelegramChat    string `json:"telegram_chat,omitempty"`  // sealed at rest
	WebhookURL      string `json:"webhook_url,omitempty"`    // sealed at rest
}

func defaultMeshAlertSettings() meshAlertSettings {
	return meshAlertSettings{Enabled: false, AgentOfflineMin: 5, CertExpireDays: meshAlertCertWarnDays, DiskMaxPercent: 90}
}

// meshAlertEvent is one evaluated condition snapshot.
type meshAlertEvent struct {
	Key      string // stable identity for dedup, e.g. "agent_offline:agt_x"
	Rule     string // rule name
	Target   string // agent/tunnel/server id
	Sev      string // "warn" | "crit"
	Text     string // human message (en; the UI translates the rule name)
	Now      time.Time
	Recovery bool // true = condition cleared
	// UI is the structured fa/en view (phase 13 TASK 2) for the API
	// view and the notifiers; nil for the phase-<13 rules.
	UI map[string]any
	// maintTargets (phase 16): EXTRA maintenance keys whose active
	// window should silence DELIVERY besides ev.Target — for
	// tunnel-scoped rules the AGENT ids of both ends (a tunnel event
	// must not page an operator who put that agent into maintenance).
	maintTargets []string
}

type meshAlertState struct {
	open     bool
	lastSent time.Time
	openedAt time.Time
	lastText string
	lastUI   map[string]any // the structured fa/en view of the last event
}

type meshAlertHistoryItem struct {
	At       int64  `json:"at"`
	Key      string `json:"key"`
	Rule     string `json:"rule"`
	Sev      string `json:"sev"`
	Text     string `json:"text"`
	Recovery bool   `json:"recovery"`
	// UI is the structured fa/en view (phase 13 rules attach it).
	UI map[string]any `json:"ui,omitempty"`
}

// meshAlertEngine holds state; one instance per process.
type meshAlertEngine struct {
	mu        sync.Mutex
	settings  meshAlertSettings
	states    map[string]*meshAlertState
	history   []meshAlertHistoryItem
	startedAt time.Time

	notifyMu  sync.Mutex
	notifiers []meshAlertNotifier
	// maintenance silences DELIVERY per target (agent/server id) until
	// the deadline; rules still evaluate + record history (phase 13).
	maintenance map[string]time.Time

	stop    chan struct{}
	stopped sync.Once
}

// SetMaintenance silences delivery for one target until the deadline
// (phase 13 TASK 2); rules keep evaluating and recording. In-memory
// primitive (tests); the API path uses maintenanceSet to persist.
func (e *meshAlertEngine) SetMaintenance(target string, until time.Time) {
	e.notifyMu.Lock()
	if e.maintenance == nil {
		e.maintenance = map[string]time.Time{}
	}
	e.maintenance[target] = until
	e.notifyMu.Unlock()
}

// maintenanceSet stores one window AND persists it into the sealed
// alert-settings file so the window survives a panel restart (phase 16
// API path). until.IsZero() clears the window.
func (e *meshAlertEngine) maintenanceSet(target string, until time.Time) error {
	e.SetMaintenance(target, until)
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.persistMeshSettings()
}

// maintenanceUntil: the active window for one target (0, false = none).
func (e *meshAlertEngine) maintenanceUntil(target string) (int64, bool) {
	e.notifyMu.Lock()
	defer e.notifyMu.Unlock()
	until, ok := e.maintenance[target]
	if !ok {
		return 0, false
	}
	if time.Now().After(until) {
		delete(e.maintenance, target)
		return 0, false
	}
	return until.Unix(), true
}

// maintenanceWindows: the ACTIVE windows for the alerts view (sorted by
// target; expired entries stay until the next maintenanceActive touch).
func (e *meshAlertEngine) maintenanceWindows() []map[string]any {
	e.notifyMu.Lock()
	defer e.notifyMu.Unlock()
	now := time.Now()
	out := []map[string]any{}
	for t, until := range e.maintenance {
		if now.After(until) {
			continue
		}
		out = append(out, map[string]any{"target": t, "until": until.Unix()})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["target"].(string) < out[j]["target"].(string)
	})
	return out
}

// maintenanceActive: is the target silenced right now?
func (e *meshAlertEngine) maintenanceActive(target string) bool {
	e.notifyMu.Lock()
	defer e.notifyMu.Unlock()
	until, ok := e.maintenance[target]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(e.maintenance, target)
		return false
	}
	return true
}

// maintenanceSilenced: is this event's delivery silenced — by its own
// target OR any of the extra (tunnel-end agent) targets?
func (e *meshAlertEngine) maintenanceSilenced(ev meshAlertEvent) bool {
	if e.maintenanceActive(ev.Target) {
		return true
	}
	for _, t := range ev.maintTargets {
		if e.maintenanceActive(t) {
			return true
		}
	}
	return false
}

// maintForEnds: the agent ids whose maintenance windows should silence
// alerts for a tunnel between the two servers (deduped; "" skipped).
func maintForEnds(agByServer map[int64]string, local, remote int64) []string {
	var out []string
	for _, ag := range []string{agByServer[local], agByServer[remote]} {
		if ag == "" {
			continue
		}
		dup := false
		for _, x := range out {
			if x == ag {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, ag)
		}
	}
	return out
}

// meshAlertNotifier is the pluggable delivery seam (tests fake it).
type meshAlertNotifier interface {
	Name() string
	Send(ev meshAlertEvent) error
}

var meshAlertEng = newMeshAlertEngine()

func newMeshAlertEngine() *meshAlertEngine {
	return &meshAlertEngine{
		settings:  defaultMeshAlertSettings(),
		states:    map[string]*meshAlertState{},
		startedAt: time.Now(),
		stop:      make(chan struct{}),
	}
}

func meshAlertSettingsPath() string { return filepath.Join(configDir, "mesh-alerts.json") }

// meshAlertSealKey reuses the per-install pasarguard key file for
// sealing the notifier credentials (one key file per install, 0600).
func meshAlertSealKey() ([]byte, error) { return pasarguard.LoadOrCreateKey(configDir) }

// loadMeshAlertSettings reads + unseals the persisted settings (missing
// file = defaults with Enabled=false: quiet until the operator opts in).
func (e *meshAlertEngine) loadMeshAlertSettings() {
	raw, err := os.ReadFile(meshAlertSettingsPath())
	if err != nil {
		return
	}
	var stored struct {
		Enabled             bool             `json:"enabled"`
		AgentOfflineMin     int              `json:"agent_offline_min"`
		CertExpireDays      int              `json:"cert_expire_days"`
		DiskMaxPercent      int              `json:"disk_max_percent"`
		TelegramTokenSealed string           `json:"telegram_token_sealed"`
		TelegramChatSealed  string           `json:"telegram_chat_sealed"`
		WebhookURLSealed    string           `json:"webhook_url_sealed"`
		Maintenance         map[string]int64 `json:"maintenance"` // phase 16
	}
	if json.Unmarshal(raw, &stored) != nil {
		return
	}
	e.settings = defaultMeshAlertSettings()
	e.settings.Enabled = stored.Enabled
	if stored.AgentOfflineMin > 0 {
		e.settings.AgentOfflineMin = stored.AgentOfflineMin
	}
	if stored.CertExpireDays > 0 {
		e.settings.CertExpireDays = stored.CertExpireDays
	}
	if stored.DiskMaxPercent > 0 && stored.DiskMaxPercent <= 99 {
		e.settings.DiskMaxPercent = stored.DiskMaxPercent
	}
	if key, err := meshAlertSealKey(); err == nil {
		if b, err := pasarguard.Open(key, stored.TelegramTokenSealed); err == nil {
			e.settings.TelegramToken = string(b)
		}
		if b, err := pasarguard.Open(key, stored.TelegramChatSealed); err == nil {
			e.settings.TelegramChat = string(b)
		}
		if b, err := pasarguard.Open(key, stored.WebhookURLSealed); err == nil {
			e.settings.WebhookURL = string(b)
		}
	}
	// Restore the maintenance windows (phase 16): skip expired ones.
	e.notifyMu.Lock()
	e.maintenance = map[string]time.Time{}
	now := time.Now()
	for t, u := range stored.Maintenance {
		if t != "" && now.Before(time.Unix(u, 0)) {
			e.maintenance[t] = time.Unix(u, 0)
		}
	}
	e.notifyMu.Unlock()
	e.rebuildMeshNotifiers()
}

// rebuildMeshNotifiers wires the enabled delivery channels.
func (e *meshAlertEngine) rebuildMeshNotifiers() {
	e.notifyMu.Lock()
	defer e.notifyMu.Unlock()
	var ns []meshAlertNotifier
	if e.settings.TelegramToken != "" && e.settings.TelegramChat != "" {
		ns = append(ns, &meshTelegramNotifier{token: e.settings.TelegramToken, chat: e.settings.TelegramChat})
	}
	if e.settings.WebhookURL != "" {
		ns = append(ns, &meshWebhookNotifier{url: e.settings.WebhookURL})
	}
	e.notifiers = ns
}

// persistMeshSettings seals the notifier credentials and stores them.
func (e *meshAlertEngine) persistMeshSettings() error {
	key, err := meshAlertSealKey()
	if err != nil {
		return err
	}
	tokSealed, err := pasarguard.Seal(key, []byte(e.settings.TelegramToken))
	if err != nil {
		return err
	}
	chatSealed, err := pasarguard.Seal(key, []byte(e.settings.TelegramChat))
	if err != nil {
		return err
	}
	urlSealed, err := pasarguard.Seal(key, []byte(e.settings.WebhookURL))
	if err != nil {
		return err
	}
	// Active maintenance windows only (caller holds e.mu; notifyMu nests).
	maintSnap := map[string]int64{}
	now := time.Now()
	e.notifyMu.Lock()
	for t, u := range e.maintenance {
		if t != "" && now.Before(u) {
			maintSnap[t] = u.Unix()
		}
	}
	e.notifyMu.Unlock()
	man, err := json.Marshal(map[string]any{
		"enabled":               e.settings.Enabled,
		"agent_offline_min":     e.settings.AgentOfflineMin,
		"cert_expire_days":      e.settings.CertExpireDays,
		"disk_max_percent":      e.settings.DiskMaxPercent,
		"telegram_token_sealed": tokSealed,
		"telegram_chat_sealed":  chatSealed,
		"webhook_url_sealed":    urlSealed,
		"maintenance":           maintSnap,
	})
	if err != nil {
		return err
	}
	return os.WriteFile(meshAlertSettingsPath(), man, 0o600)
}

// evaluate produces the CURRENT alert events (no state mutation).
func (e *meshAlertEngine) evaluate(now time.Time) []meshAlertEvent {
	var evs []meshAlertEvent
	agentRegMu.Lock()
	recs, _, err := agentRegistryLoad()
	agentRegMu.Unlock()
	if err == nil {
		for _, rec := range recs {
			if rec.Revoked {
				continue
			}
			offlineFor := now.Sub(time.Unix(rec.LastSeen, 0))
			if rec.Status != "online" || offlineFor > time.Duration(e.settings.AgentOfflineMin)*time.Minute {
				evs = append(evs, meshAlertEvent{
					Key: "agent_offline:" + rec.ID, Rule: "agent_offline", Target: rec.ID,
					Sev: "warn", Now: now,
					Text: fmt.Sprintf("agent %s offline for %s", rec.ID, offlineFor.Round(time.Minute)),
				})
			}
		}
	}
	if s, err := MeshStore(); err == nil {
		downByServer := map[int64]int{}
		totalByServer := map[int64]int{}
		// agent id per server (phase 16): tunnel events carry the END
		// agents' ids as maintenance targets so a window on one agent
		// also silences its tunnels' alerts.
		agByServer := map[int64]string{}
		if agents, err := s.ListAgents(); err == nil {
			for _, a := range agents {
				if a.ServerID != 0 {
					agByServer[a.ServerID] = a.ID
				}
			}
		}
		maintByServer := map[int64][]string{} // remote sid -> local-end agents
		topos, _ := s.ListTopologies()
		for _, topo := range topos {
			tuns, err := s.ListTunnels(topo.ID)
			if err != nil {
				continue
			}
			for _, t := range tuns {
				if !t.Enabled {
					continue
				}
				totalByServer[t.RemoteServerID]++
				mt := maintForEnds(agByServer, t.LocalServerID, t.RemoteServerID)
				if len(mt) > 0 {
					maintByServer[t.RemoteServerID] = append(maintByServer[t.RemoteServerID], mt...)
				}
				rt := e.runtimeFor(t.TunnelKey)
				if rt == nil {
					continue // no report yet: not an alert (startup truth)
				}
				if rt.WireKnown && rt.WireSum > 0 {
					// traffic resumed: reset the freeze window, so a FUTURE
					// freeze still requires two consecutive dead ticks
					// (round-3 fix: the entry used to linger forever and a
					// re-freeze would fire after a single tick)
					delete(meshTrafficFrozenSeen, t.TunnelKey)
				}
				if !rt.Up {
					downByServer[t.RemoteServerID]++
					evs = append(evs, meshAlertEvent{
						Key: "tunnel_down:" + t.TunnelKey, Rule: "tunnel_down", Target: t.TunnelKey,
						Sev: "crit", Now: now, maintTargets: mt,
						Text: fmt.Sprintf("tunnel %s (%s) is DOWN", t.TunnelKey, rt.Engine),
					})
				} else if rt.WireKnown && rt.WireSum == 0 && e.trafficFrozen(t.TunnelKey, now) {
					evs = append(evs, meshAlertEvent{
						Key: "traffic_drop:" + t.TunnelKey, Rule: "traffic_drop", Target: t.TunnelKey,
						Sev: "warn", Now: now, maintTargets: mt,
						Text: fmt.Sprintf("tunnel %s up but wire traffic frozen across 2 checks", t.TunnelKey),
					})
				}
			}
		}
		for sid, down := range downByServer {
			if down > 0 && down == totalByServer[sid] && totalByServer[sid] > 0 {
				evs = append(evs, meshAlertEvent{
					Key: fmt.Sprintf("foreign_all_down:%d", sid), Rule: "foreign_all_down",
					Target: fmt.Sprintf("server-%d", sid), Sev: "crit", Now: now,
					maintTargets: maintByServer[sid],
					Text:         fmt.Sprintf("ALL %d tunnels to server %d are down", down, sid),
				})
			}
		}
	}
	profilePendingMu.Lock()
	for _, p := range profilePendingBy {
		deadline := time.Unix(p.Deadline, 0)
		remain := time.Until(deadline)
		total := time.Duration(p.TimerSec) * time.Second
		if remain > 0 && total > 0 && float64(remain) < float64(total)*meshAlertHardeningFrac {
			evs = append(evs, meshAlertEvent{
				Key: "hardening_expiring:" + p.AgentID, Rule: "hardening_expiring", Target: p.AgentID,
				Sev: "warn", Now: now,
				Text: fmt.Sprintf("safe-apply confirm on %s expires in %s (rollback armed)", p.AgentID, remain.Round(time.Second)),
			})
		}
	}
	profilePendingMu.Unlock()
	if exp, _, err := tlsCertExpiry(); err == nil {
		days := time.Until(exp).Hours() / 24
		if days < float64(e.settings.CertExpireDays) {
			evs = append(evs, meshAlertEvent{
				Key: "cert_expiry", Rule: "cert_expiry", Target: "https",
				Sev: "warn", Now: now,
				Text: fmt.Sprintf("HTTPS certificate expires in %.0f days (%s)", days, exp.Format("2006-01-02")),
			})
		}
	}
	if total, free, err := getDiskUsage(configDir); err == nil && total > 0 {
		pct := 100 - int(100*free/total)
		if pct >= e.settings.DiskMaxPercent {
			evs = append(evs, meshAlertEvent{
				Key: "disk_usage", Rule: "disk_usage", Target: configDir,
				Sev: "warn", Now: now,
				Text: fmt.Sprintf("panel data dir at %d%% disk usage (limit %d%%)", pct, e.settings.DiskMaxPercent),
			})
		}
	}
	evs = append(evs, e.evaluatePhase13(now)...)
	return evs
}

// runtimeFor resolves the latest report for a tunnel key by scanning the
// live runtime map (read-only; the map lives in reportv2.go).
func (e *meshAlertEngine) runtimeFor(tunnelKey string) *meshTunnelRuntimeView {
	meshRuntimeMu.Lock()
	for _, rts := range meshRuntimeBy {
		for _, rt := range rts {
			if rt.TunnelID == tunnelKey {
				meshRuntimeMu.Unlock()
				return &meshTunnelRuntimeView{Up: rt.Up, Engine: rt.Engine,
					WireKnown: rt.WireKnown, WireSum: rt.WireRx + rt.WireTx}
			}
		}
	}
	meshRuntimeMu.Unlock()
	return nil
}

// meshTunnelRuntimeView hides the protocol types from this file's logic.
type meshTunnelRuntimeView struct {
	Up        bool
	Engine    string
	WireKnown bool
	WireSum   uint64
}

// meshTrafficFrozenSeen tracks two consecutive zero-traffic ticks.
var meshTrafficFrozenSeen = map[string]time.Time{}

func (e *meshAlertEngine) trafficFrozen(key string, now time.Time) bool {
	first, ok := meshTrafficFrozenSeen[key]
	if !ok {
		meshTrafficFrozenSeen[key] = now
		return false
	}
	return now.Sub(first) >= meshAlertTick // zero last tick AND now
}

// tick evaluates once and reconciles state → notifications.
func (e *meshAlertEngine) tick(now time.Time) {
	evs := e.evaluate(now)
	e.mu.Lock()
	defer e.mu.Unlock()
	quiet := now.Sub(e.startedAt) < meshAlertQuietStart
	seen := map[string]bool{}
	for _, ev := range evs {
		seen[ev.Key] = true
		st := e.states[ev.Key]
		if st == nil {
			st = &meshAlertState{}
			e.states[ev.Key] = st
		}
		if !st.open {
			st.open = true
			st.openedAt = now
			st.lastText = ev.Text
			if !quiet {
				e.dispatchLocked(ev, false)
				st.lastSent = now
			}
		} else if now.Sub(st.lastSent) >= meshAlertCooldown {
			st.lastText = ev.Text
			if !quiet {
				e.dispatchLocked(ev, false)
				st.lastSent = now
			}
		}
	}
	// recoveries: states open but no matching event now
	for key, st := range e.states {
		if !st.open || seen[key] {
			continue
		}
		st.open = false
		ev := meshAlertEvent{Key: key, Target: key, Now: now, Recovery: true,
			Text: "recovered: " + st.lastText}
		if i := strings.IndexByte(key, ':'); i >= 0 {
			ev.Rule = key[:i]
			ev.Target = key[i+1:]
		} else {
			ev.Rule = key
		}
		if !quiet {
			e.dispatchLocked(ev, true)
		}
	}
}

// dispatchLocked sends through every notifier + records history.
// Caller holds e.mu; notifier errors never block the loop.
func (e *meshAlertEngine) dispatchLocked(ev meshAlertEvent, recovery bool) {
	ev.Recovery = recovery
	e.notifyMu.Lock()
	ns := append([]meshAlertNotifier(nil), e.notifiers...)
	e.notifyMu.Unlock()
	maint := e.maintenanceSilenced(ev) // rules still record; delivery silenced
	if !maint {
		for _, n := range ns {
			_ = n.Send(ev) // synchronous: notifiers carry their own timeouts
		}
	}
	e.history = append(e.history, meshAlertHistoryItem{
		At: ev.Now.Unix(), Key: ev.Key, Rule: ev.Rule, Sev: ev.Sev,
		Text: ev.Text, Recovery: recovery, UI: ev.UI,
	})
	// Remember the structured view for the OPEN banner too.
	if st := e.states[ev.Key]; st != nil {
		st.lastUI = ev.UI
	}
	if len(e.history) > meshAlertMaxHistory {
		e.history = e.history[len(e.history)-meshAlertMaxHistory:]
	}
}

// Run starts the tick loop (one goroutine per process).
func (e *meshAlertEngine) Run() {
	e.loadMeshAlertSettings()
	go func() {
		t := time.NewTicker(meshAlertTick)
		defer t.Stop()
		for {
			select {
			case <-e.stop:
				return
			case now := <-t.C:
				e.tick(now)
			}
		}
	}()
}

func (e *meshAlertEngine) Stop() { e.stopped.Do(func() { close(e.stop) }) }

// currentAlertsView is what the API/UI renders: open alerts + history.
func (e *meshAlertEngine) currentAlertsView() (open []meshAlertHistoryItem, history []meshAlertHistoryItem, settings meshAlertSettings) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for key, st := range e.states {
		if !st.open {
			continue
		}
		rule := key
		if i := strings.IndexByte(key, ':'); i >= 0 {
			rule = key[:i]
		}
		open = append(open, meshAlertHistoryItem{
			At: st.openedAt.Unix(), Key: key, Rule: rule,
			Sev: "crit", Text: st.lastText, UI: st.lastUI,
		})
	}
	history = append([]meshAlertHistoryItem(nil), e.history...)
	sort.Slice(history, func(i, j int) bool { return history[i].At > history[j].At })
	if len(history) > 50 {
		history = history[:50]
	}
	return open, history, e.settings
}
