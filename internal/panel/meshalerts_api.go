// meshalerts_api.go — TASK 3 REST surface for the /mesh Alerts tab.
//
//	GET  /api/mesh/alerts           (open alerts + history + settings,
//	                                 secrets NEVER echoed — only booleans)
//	POST /api/mesh/alerts/settings  (auth+CSRF; thresholds + notifiers;
//	                                 empty secret fields keep the old
//	                                 sealed values)
//	POST /api/mesh/alerts/test      (auth+CSRF; send a test event)

package panel

import (
	"net/http"
	"strings"
	"time"
)

func registerMeshAlertRoutes(mux *http.ServeMux, base string) {
	p := base + "/api/mesh/alerts"
	mux.HandleFunc("GET "+p, requireAuth(handleMeshAlertsView))
	mux.HandleFunc("POST "+p+"/settings", requireAuth(requireCSRF(handleMeshAlertsSettings)))
	mux.HandleFunc("POST "+p+"/test", requireAuth(requireCSRF(handleMeshAlertsTest)))
}

func handleMeshAlertsView(w http.ResponseWriter, r *http.Request) {
	open, history, st := meshAlertEng.currentAlertsView()
	writeJSON(w, map[string]any{
		"enabled":           st.Enabled,
		"agent_offline_min": st.AgentOfflineMin,
		"cert_expire_days":  st.CertExpireDays,
		"disk_max_percent":  st.DiskMaxPercent,
		"telegram_set":      st.TelegramToken != "" && st.TelegramChat != "",
		"webhook_set":       st.WebhookURL != "",
		"open":              open,
		"history":           history,
		// Phase 16: ACTIVE maintenance windows (contract §2.15) so the
		// rules card renders them without extra calls.
		"maintenance":           meshAlertEng.maintenanceWindows(),
		"quiet_until_startup_s": int(meshAlertQuietStart.Seconds()),
	})
}

func handleMeshAlertsSettings(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Enabled         *bool  `json:"enabled"`
		AgentOfflineMin *int   `json:"agent_offline_min"`
		CertExpireDays  *int   `json:"cert_expire_days"`
		DiskMaxPercent  *int   `json:"disk_max_percent"`
		TelegramToken   string `json:"telegram_token"` // "" = keep, "-" = clear
		TelegramChat    string `json:"telegram_chat"`
		WebhookURL      string `json:"webhook_url"`
	}
	if !meshReadBody(w, r, &q) {
		return
	}
	meshAlertEng.mu.Lock()
	st := &meshAlertEng.settings
	if q.Enabled != nil {
		st.Enabled = *q.Enabled
	}
	if q.AgentOfflineMin != nil && *q.AgentOfflineMin >= 1 && *q.AgentOfflineMin <= 1440 {
		st.AgentOfflineMin = *q.AgentOfflineMin
	}
	if q.CertExpireDays != nil && *q.CertExpireDays >= 1 && *q.CertExpireDays <= 365 {
		st.CertExpireDays = *q.CertExpireDays
	}
	if q.DiskMaxPercent != nil && *q.DiskMaxPercent >= 10 && *q.DiskMaxPercent <= 99 {
		st.DiskMaxPercent = *q.DiskMaxPercent
	}
	if q.TelegramToken == "-" {
		st.TelegramToken = ""
	} else if strings.TrimSpace(q.TelegramToken) != "" {
		st.TelegramToken = strings.TrimSpace(q.TelegramToken)
	}
	if q.TelegramChat == "-" {
		st.TelegramChat = ""
	} else if strings.TrimSpace(q.TelegramChat) != "" {
		st.TelegramChat = strings.TrimSpace(q.TelegramChat)
	}
	if q.WebhookURL == "-" {
		st.WebhookURL = ""
	} else if strings.TrimSpace(q.WebhookURL) != "" {
		u := strings.TrimSpace(q.WebhookURL)
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			meshAlertEng.mu.Unlock()
			writeAPIError(w, r, "E-IN-04", "webhook_url must be http(s)")
			return
		}
		st.WebhookURL = u
	}
	err := meshAlertEng.persistMeshSettings()
	meshAlertEng.rebuildMeshNotifiers()
	meshAlertEng.mu.Unlock()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "cannot persist alert settings: "+err.Error())
		return
	}
	LogSecurityAudit("alerts_settings_changed", cfg.Username, ClientIP(r),
		"telegram_set="+(boolToStr(st.TelegramToken != ""))+" webhook_set="+(boolToStr(st.WebhookURL != "")))
	writeJSON(w, map[string]any{"status": "ok"})
}

// handleMeshAlertsTest fires a test event through the configured notifiers
// (and records it in history) so the operator can prove delivery works.
func handleMeshAlertsTest(w http.ResponseWriter, r *http.Request) {
	ev := meshAlertEvent{
		Key: "test", Rule: "test", Target: cfg.Username, Sev: "info",
		Text: "test notification from the Quantum panel Alerts tab",
		Now:  time.Now(),
	}
	meshAlertEng.notifyMu.Lock()
	ns := append([]meshAlertNotifier(nil), meshAlertEng.notifiers...)
	meshAlertEng.notifyMu.Unlock()
	if len(ns) == 0 {
		writeJSON(w, map[string]any{"sent": 0, "note": "no notifier configured — fill Telegram or webhook first"})
		return
	}
	sent := 0
	for _, n := range ns {
		if err := n.Send(ev); err == nil {
			sent++
		}
	}
	meshAlertEng.mu.Lock()
	meshAlertEng.history = append(meshAlertEng.history, meshAlertHistoryItem{
		At: ev.Now.Unix(), Key: "test", Rule: "test", Sev: "info", Text: ev.Text,
	})
	meshAlertEng.mu.Unlock()
	writeJSON(w, map[string]any{"sent": sent, "notifiers": len(ns)})
}
