package panel

// meshalerts_phase13.go — TASK 2 (phase 13): the NEW alert rules over
// the phase-11 data, all routed through the EXISTING meshAlertEngine
// (dedup/cooldown by Key, notifiers, history). Rules:
//
//	agent cpu/mem/disk thresholds (from the persisted ResourceReport)
//	engine enabled but no running instances (from agent_engines)
//	tunnel flapping (N state changes in M minutes, from tunnel_events)
//	billing anomaly: sudden drop-to-zero / x-fold growth vs the same
//	  hour last week (from the billing ranges)
//	clock drift (agent-reported clock vs panel time > threshold — REAL
//	since R7: StateReportV2.ReportedAt; legacy agents skip silently)
//	cert expiry (panel TLS; already a rule — kept)
//	backup age (no successful backup in N days, from the backup state)
//	panel data-dir disk usage
//	pending hardening confirm near expiry
//	queued desired undelivered > 15 min (the offline-pending watchdog)
//
// Every rule's TEXT goes through the SAME structured fa/en catalog
// (uiAlertText) so Telegram/webhook render both languages; grouping =
// the engine's Key dedup; maintenance mode silences per agent/server.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-protocol"
)

// ---- structured alert text (fa/en, same shape as the error catalog) ----

type uiAlertText struct {
	Title string `json:"title"`
	What  string `json:"what"`
	Cause string `json:"likely_cause"`
	Fix   string `json:"what_to_do"`
}

var alertTextCatalog = map[string]uiAlertText{
	"agent_cpu_high":     {"Agent CPU high", "Agent %s CPU at %.0f%%.", "Sustained load or a stuck process.", "Check the agent host's top processes; consider a bigger instance."},
	"agent_mem_high":     {"Agent memory high", "Agent %s memory at %.0f%%.", "A leak or an undersized host.", "Check RSS per engine; restart the agent if RSS grows unbounded."},
	"agent_disk_high":    {"Agent disk high", "Agent %s disk at %.0f%%.", "Logs/artifacts filling the disk.", "Clean old artifacts/logs; expand the disk before it is full."},
	"engine_not_running": {"Engine enabled but not running", "Engine %s on agent %s is enabled with 0 running instances.", "The engine failed to start or was never pushed a tunnel.", "Check the agent's last error (Agents > detail) and the artifact availability."},
	"tunnel_flapping":    {"Tunnel flapping", "Tunnel %s changed state %d times in %d minutes.", "An unstable link or a restarting engine process.", "Check the engine logs on both ends; inspect the link quality."},
	"billing_drop":       {"Traffic dropped to zero", "Tunnel %s carried ~0 bytes this hour vs %d last week.", "The tunnel is down, or the agent stopped reporting.", "Verify the tunnel state and the agent's last seen."},
	"billing_spike":      {"Traffic spike", "Tunnel %s carried %d bytes this hour (%.1fx the same hour last week).", "Genuine growth or abuse/anomaly.", "Verify with the consumer; consider rate limits."},
	"clock_drift":        {"Agent clock drift", "Agent %s heartbeat timestamp differs from panel time by %s.", "No NTP on the agent host.", "Enable NTP on the agent (timedatectl set-ntp true)."},
	"backup_old":         {"Backup is old", "The last successful backup is %s old (threshold %d days).", "Backups not scheduled or failing.", "Run the backup CLI and schedule it (cron/systemd timer)."},
	"panel_disk_full":    {"Panel disk almost full", "The panel data filesystem is %d%% full.", "Store growth or logs.", "Free space; check the retention/rollup settings."},
	"hardening_expiry":   {"Hardening confirm expiring", "Agent %s has a pending hardening confirm expiring in %s.", "The operator has not confirmed the profile.", "Confirm or roll back before the automatic rollback fires."},
	"desired_stuck":      {"Queued desired not delivered", "Agent %s has a queued desired state for %s (not yet acked).", "The agent has not reconnected since the change.", "Check the agent's connectivity; it will apply on reconnect."},
}

// faAlertText: the Persian mirror (rendered by the notifier as its own
// block; the UI renders both from the API view).
var faAlertText = map[string]string{
	"agent_cpu_high":     "پردازنده عامل بالاست",
	"agent_mem_high":     "حافظه عامل بالاست",
	"agent_disk_high":    "دیسک عامل پر است",
	"engine_not_running": "موتور فعال است ولی اجرا نمی‌شود",
	"tunnel_flapping":    "تونل ناپایدار است",
	"billing_drop":       "ترافیک ناگهان صفر شده",
	"billing_spike":      "جهش ناگهانی ترافیک",
	"clock_drift":        "انحراف ساعت عامل",
	"backup_old":         "پشتیبان قدیمی است",
	"panel_disk_full":    "دیسک پنل تقریباً پر است",
	"hardening_expiry":   "تأیید hardening نزدیک انقضا",
	"desired_stuck":      "پیکربندی در صف مانده",
}

// alertUI renders the structured view for an event (the UI/API reads
// this; the notifier formats both languages into the message). The
// en-what is the pre-formatted human line.
func alertUI(rule, formatted string) map[string]any {
	t, ok := alertTextCatalog[rule]
	if !ok {
		return map[string]any{"title": map[string]string{"en": rule}, "what": map[string]string{"en": formatted}}
	}
	return map[string]any{
		"title":        map[string]string{"en": t.Title, "fa": faAlertText[rule]},
		"what":         map[string]string{"en": formatted, "fa": alertWhatFa(rule, formatted)},
		"likely_cause": map[string]string{"en": t.Cause, "fa": alertCauseFa[rule]},
		"what_to_do":   map[string]string{"en": t.Fix, "fa": alertFixFa[rule]},
		"formatted":    formatted,
	}
}

var alertCauseFa = map[string]string{
	"agent_cpu_high":     "بار پایدار یا فرایند گیر کرده.",
	"agent_mem_high":     "نشت حافظه یا میزبان کوچک.",
	"agent_disk_high":    "لاگ‌ها/مصرفی‌ها دیسک را پر می‌کنند.",
	"engine_not_running": "موتور اجرا نشده یا تونلی برایش اعمال نشده.",
	"tunnel_flapping":    "لینک ناپایدار یا ری‌استارت شدن فرایند موتور.",
	"billing_drop":       "تونل قطع است یا عامل گزارش نمی‌دهد.",
	"billing_spike":      "رشد واقعی یا مصرف غیرعادی.",
	"clock_drift":        "NTP روی میزبان عامل فعال نیست.",
	"backup_old":         "پشتیبان‌گیری زمان‌بندی نشده یا شکست خورده.",
	"panel_disk_full":    "رشد پایگاه‌داده یا لاگ‌ها.",
	"hardening_expiry":   "اپراتور هنوز پروفایل را تأیید نکرده.",
	"desired_stuck":      "عامل از زمان تغییر reconnect نشده.",
}

func alertWhatFa(rule, formatted string) string {
	if w, ok := alertWhatFaMap[rule]; ok {
		return w + " (" + formatted + ")"
	}
	return formatted
}

var alertWhatFaMap = map[string]string{
	"agent_cpu_high":     "پردازنده عامل بیش از آستانه است",
	"agent_mem_high":     "حافظه عامل بیش از آستانه است",
	"agent_disk_high":    "دیسک عامل بیش از آستانه پر است",
	"engine_not_running": "موتور فعال است ولی هیچ نمونه‌ای اجرا نمی‌شود",
	"tunnel_flapping":    "تونل در پنجره کوتاه چند بار تغییر وضعیت داده",
	"billing_drop":       "ترافیک این ساعت نسبت به هفته قبل تقریباً صفر است",
	"billing_spike":      "ترافیک این ساعت چند برابر هفته قبل است",
	"clock_drift":        "ساعت عامل با ساعت پنل نمی‌خواند",
	"backup_old":         "آخرین پشتیبان قدیمی‌تر از آستانه است",
	"panel_disk_full":    "فایل‌سیستم داده پنل تقریباً پر است",
	"hardening_expiry":   "تأیید hardening به‌زودی منقضی می‌شود",
	"desired_stuck":      "پیکربندی در صف مانده و هنوز ACK نشده",
}

var alertFixFa = map[string]string{
	"agent_cpu_high":     "پردازش‌های میزبان عامل را بررسی کنید؛ در صورت نیاز منبع بیشتر.",
	"agent_mem_high":     "RSS هر موتور را بررسی کنید؛ در صورت رشد بی‌پایان عامل را ری‌استارت کنید.",
	"agent_disk_high":    "مصرفی/لاگ قدیمی را پاک کنید؛ پیش از پر شدن دیسک را بزرگ کنید.",
	"engine_not_running": "خطای آخر عامل (Agents > detail) و موجود بودن مصرف را بررسی کنید.",
	"tunnel_flapping":    "لاگ موتور را در دو سر بررسی کنید؛ کیفیت لینک را بسنجید.",
	"billing_drop":       "وضعیت تونل و آخرین اتصال عامل را بررسی کنید.",
	"billing_spike":      "با مصرف‌کننده بررسی کنید؛ در صورت نیاز محدودیت بگذارید.",
	"clock_drift":        "روی عامل NTP را فعال کنید (timedatectl set-ntp true).",
	"backup_old":         "دستور backup را اجرا و زمان‌بندی کنید (cron/systemd timer).",
	"panel_disk_full":    "فضا آزاد کنید؛ نگه‌داری/rollup را بررسی کنید.",
	"hardening_expiry":   "پیش از rollback خودکار تأیید یا برگردانید.",
	"desired_stuck":      "اتصال عامل را بررسی کنید؛ با reconnect اعمال می‌شود.",
}

// alertEmit builds a meshAlertEvent with the structured fa/en view
// attached (the notifier/UI read ev.UI). The variadic args are the
// rule's numbers for the fa what-line (best effort).
func (e *meshAlertEngine) alertEmit(evs *[]meshAlertEvent, now time.Time, rule, key, target, sev, formatted string, args ...any) {
	ev := meshAlertEvent{
		Key: key, Rule: rule, Target: target, Sev: sev, Now: now,
		Text: formatted,
	}
	ev.UI = alertUI(rule, formatted)
	*evs = append(*evs, ev)
}

// ---- the phase-13 rule pass (called from evaluate) ---------------------

func (e *meshAlertEngine) evaluatePhase13(now time.Time) []meshAlertEvent {
	var evs []meshAlertEvent
	s, err := MeshStore()
	if err != nil {
		return evs
	}

	// 1. agent cpu/mem/disk thresholds (persisted ResourceReport).
	agents, _ := s.ListAgents()
	for _, a := range agents {
		if a.Status != "online" || a.MemTotalMB == 0 {
			continue
		}
		memPct := 0.0
		if a.MemTotalMB > 0 {
			memPct = float64(a.MemUsedMB) / float64(a.MemTotalMB) * 100
		}
		if a.CPUPct >= 90 {
			e.alertEmit(&evs, now, "agent_cpu_high", "agent_cpu:"+a.ID, a.ID, "warn",
				fmt.Sprintf("agent %s CPU at %.0f%%", a.Name, a.CPUPct), a.Name, a.CPUPct)
		}
		if memPct >= 90 {
			e.alertEmit(&evs, now, "agent_mem_high", "agent_mem:"+a.ID, a.ID, "warn",
				fmt.Sprintf("agent %s memory at %.0f%%", a.Name, memPct), a.Name, memPct)
		}
		if a.DiskTotalGB > 0 {
			diskPct := a.DiskUsedGB / a.DiskTotalGB * 100
			if diskPct >= 90 {
				e.alertEmit(&evs, now, "agent_disk_high", "agent_disk:"+a.ID, a.ID, "warn",
					fmt.Sprintf("agent %s disk at %.0f%%", a.Name, diskPct), a.Name, diskPct)
			}
		}
	}

	// 2. engine enabled but nothing running.
	for _, a := range agents {
		rows, _ := s.AgentEngines(a.ID)
		for _, row := range rows {
			if row.Desired && row.Installed && row.Instances == 0 {
				e.alertEmit(&evs, now, "engine_not_running", "engine_idle:"+a.ID+":"+row.Engine, a.ID, "warn",
					fmt.Sprintf("engine %s on agent %s enabled with 0 instances", row.Engine, a.Name), row.Engine, a.Name)
			}
		}
	}

	// 3. tunnel flapping: >= 4 state changes in 15 minutes.
	since := now.Add(-15 * time.Minute).Unix()
	// Collect FIRST, close the rows, THEN resolve names — the store pool
	// is 1 connection and a nested query while iterating deadlocks.
	flapRows, err := s.DB().Query(`SELECT te.tunnel_id, COUNT(*) FROM tunnel_events te
		WHERE te.kind = 'state' AND te.at >= ? GROUP BY te.tunnel_id HAVING COUNT(*) >= 4`, since)
	if err == nil {
		type flap struct {
			tid, n int64
		}
		var flaps []flap
		for flapRows.Next() {
			var f flap
			if flapRows.Scan(&f.tid, &f.n) == nil {
				flaps = append(flaps, f)
			}
		}
		flapRows.Close()
		for _, f := range flaps {
			tun, err := s.GetTunnel(f.tid)
			key := "flap"
			name := fmt.Sprintf("tunnel %d", f.tid)
			if err == nil {
				key = "tunnel_flap:" + tun.TunnelKey
				name = tun.TunnelKey
			}
			e.alertEmit(&evs, now, "tunnel_flapping", key, name, "warn",
				fmt.Sprintf("tunnel %s changed state %d times in 15 minutes", name, f.n), name, f.n)
		}
	}

	// 4. billing anomalies per enabled tunnel: this hour vs the same
	// hour last week (drop to ~0, or >8x growth).
	tunnels, _ := s.ListAllTunnels()
	hourStart := now.Truncate(time.Hour).Unix() // same location as now — no TZ mixing
	lastWeekStart := hourStart - 7*86400
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}
		// a tunnel that is currently DOWN is already covered by the
		// tunnel_down rule — billing_drop on top of it is noise (review 4)
		downNow := false
		meshRuntimeMu.Lock()
		now2 := time.Now().Unix()
		for aid, rts := range meshRuntimeBy {
			// inline the freshness check: meshRuntimeFresh() takes the SAME
			// mutex — calling it while holding meshRuntimeMu self-deadlocks
			if now2-meshRuntimeSeen[aid] >= 120 {
				continue // stale runtime snapshots never suppress the rule
			}
			for _, tr := range rts {
				if tr.TunnelID == t.TunnelKey && !tr.Up {
					downNow = true
				}
			}
		}
		meshRuntimeMu.Unlock()
		if downNow {
			continue
		}
		// Like-for-like hourly DELTAS from the rollup (review round 4): the
		// old pair ran RangeUsageDetailed, whose zero-base rule counts a
		// stream's first in-window reading as full usage — any counter reset
		// or mid-chain window start inflated the baseline and fired false
		// billing_drop storms. Both sides are now honest hourly deltas:
		//   cur  = the last COMPLETE hour (the rollup lags ≤2h by design)
		//   prev = the same clock hour, 7 days earlier
		curH := (hourStart-2*3600)/3600 - 1 // last hour with a GUARANTEED rollup row (the fold horizon is exclusive)
		prevH := lastWeekStart / 3600
		var curD, prevD int64
		if err := s.DB().QueryRow(`SELECT COALESCE(SUM(last_rx - first_rx), 0) FROM traffic_hourly
			WHERE tunnel_id = ? AND hour = ?`, t.ID, curH).Scan(&curD); err != nil {
			continue
		}
		if err := s.DB().QueryRow(`SELECT COALESCE(SUM(last_rx - first_rx), 0) FROM traffic_hourly
			WHERE tunnel_id = ? AND hour = ?`, t.ID, prevH).Scan(&prevD); err != nil {
			continue
		}
		if curD < 0 {
			curD = 0
		}
		switch {
		case prevD > 1<<20 && curD < prevD/100:
			e.alertEmit(&evs, now, "billing_drop", "billing_drop:"+t.TunnelKey, t.TunnelKey, "warn",
				fmt.Sprintf("tunnel %s carried ~0 bytes in the last complete hour vs %d the same hour last week", t.TunnelKey, prevD),
				t.TunnelKey, prevD)
		case prevD > 1<<20 && float64(curD) > 8*float64(prevD):
			e.alertEmit(&evs, now, "billing_spike", "billing_spike:"+t.TunnelKey, t.TunnelKey, "warn",
				fmt.Sprintf("tunnel %s carried %d bytes in the last complete hour (%.1fx the same hour last week)", t.TunnelKey, curD,
					float64(curD)/float64(prevD)),
				t.TunnelKey, curD, float64(curD)/float64(prevD))
		}
	}

	// 5. backup age (state file mtime; written by the backup API/CLI).
	if fi, err := statBackupState(); err == nil {
		age := now.Sub(fi)
		if age > 7*24*time.Hour {
			e.alertEmit(&evs, now, "backup_old", "backup_age", "panel", "warn",
				fmt.Sprintf("the last successful backup is %s old", age.Round(24*time.Hour)),
				age.Round(24*time.Hour).String(), 7)
		}
	}

	// 6. panel data-dir disk (reuse the selfcheck probe result shape).
	// (The engine runs on the panel host; the df probe is cheap.)
	if pct := panelDiskPct(); pct >= 90 {
		sev := "warn"
		if pct >= 95 {
			sev = "crit"
		}
		e.alertEmit(&evs, now, "panel_disk_full", "panel_disk", "panel", sev,
			fmt.Sprintf("the panel data filesystem is %d%% full", pct), pct)
	}

	// 7. pending hardening confirm near expiry.
	profilePendingMu.Lock()
	for _, pp := range profilePendingBy {
		remain := time.Until(time.Unix(pp.Deadline, 0))
		if remain > 0 && remain < 30*time.Second {
			e.alertEmit(&evs, now, "hardening_expiry", "hardening:"+pp.AgentID, pp.AgentID, "warn",
				fmt.Sprintf("agent %s hardening confirm expires in %s", pp.AgentID, remain.Round(time.Second)),
				pp.AgentID, remain.Round(time.Second).String())
		}
	}
	profilePendingMu.Unlock()

	// 8. queued desired undelivered > 15 min (the offline-pending watchdog).
	v2rows, _ := s.ListAgents()
	for _, a := range v2rows {
		v2, err := s.AgentV2(a.ID)
		if err != nil || strings.TrimSpace(v2.DesiredV2) == "" {
			continue
		}
		var d protocol.DesiredV2
		if json.Unmarshal([]byte(v2.DesiredV2), &d) != nil || d.Revision == "" {
			continue
		}
		// The pending row has no timestamp — the agent's last_ack/last_seen
		// bounds it: undelivered for >15 min = last_seen older AND pending.
		if a.LastSeen > 0 && now.Unix()-a.LastSeen > 15*60 {
			e.alertEmit(&evs, now, "desired_stuck", "desired_stuck:"+a.ID, a.ID, "info",
				fmt.Sprintf("agent %s has a queued desired state for %s", a.Name, time.Duration(now.Unix()-a.LastSeen)*time.Second),
				a.Name, (time.Duration(now.Unix()-a.LastSeen) * time.Second).String())
		}
	}

	// 9. clock drift (R7, PROTOCOL-REQUESTS): the agent's own report clock
	// vs panel time. The phase-13 stub became REAL when the protocol grew
	// StateReportV2.ReportedAt — legacy agents (absent field, 0) skip
	// silently. Online agents only: an offline agent already has its own
	// alert, and a stale last report says nothing about its clock NOW.
	for _, a := range v2rows {
		if a.Status != "online" {
			continue
		}
		rep, ok := meshReportedAt(a.ID)
		if !ok || rep <= 0 {
			continue // legacy agent / nothing reported yet: not an alert
		}
		drift := now.Unix() - rep
		if drift < 0 {
			drift = -drift
		}
		if drift > int64(e.settings.ClockDriftMaxS) {
			e.alertEmit(&evs, now, "clock_drift", "clock_drift:"+a.ID, a.ID, "warn",
				fmt.Sprintf("agent %s clock is %s off panel time", a.Name, (time.Duration(drift)*time.Second).Round(time.Second)),
				a.Name, (time.Duration(drift) * time.Second).String())
		}
	}
	return evs
}

// statBackupState reads the backup state file's mtime (absent = zero).
func statBackupState() (time.Time, error) {
	fi, err := os.Stat(filepath.Join(configDir, "backup-state.json"))
	if err != nil {
		return time.Time{}, err
	}
	return fi.ModTime(), nil
}

// panelDiskPct: the data-dir filesystem usage percent (-1 unknown).
func panelDiskPct() int {
	out, err := runCmdTimeout(5*time.Second, "df", "--output=pcent", configDir)
	if err != nil {
		return -1
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) < 2 {
		return -1
	}
	p := fields[len(fields)-1]
	p = strings.TrimSuffix(p, "%")
	n := 0
	if _, err := fmt.Sscanf(p, "%d", &n); err != nil {
		return -1
	}
	return n
}
