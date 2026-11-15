package panel

// selfcheck.go — the nightly self-check (phase 13 TASK 4): verifies the
// panel's invariants and reports each finding with a severity, a plain
// explanation and the fix path. Runs via `quantum-panel selfcheck` (CLI,
// exit 1 when CRITICAL findings exist) and GET /api/ui/selfcheck (API).
//
// Invariants checked (each with a seeded-broken-state test):
//   - every ENABLED tunnel has both endpoint servers present, agents
//     linked+online, and the engine enabled/installed on both;
//   - weight policies: no conflicting rows (two tunnel-scope rows for
//     the same tunnel), weights in 0..1000;
//   - usage counters monotonic per (tunnel, agent) stream in the raw
//     window (a DECREASE without the unknown flag = lost data);
//   - rollups cover every COMPLETE hour up to the watermark (no holes);
//   - SQLite integrity_check + foreign_key_check;
//   - secret file permissions 0600/0640 (panel.json, sessions.json,
//     gre_sealed.key when present);
//   - TLS certificate valid (when TLS is configured);
//   - time sync reasonable (NTP synchronized when timedatectl exists);
//   - disk space of the config dir's filesystem (>= 10% free).

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

type checkSeverity string

const (
	sevOK       checkSeverity = "ok"
	sevInfo     checkSeverity = "info"
	sevWarning  checkSeverity = "warning"
	sevCritical checkSeverity = "critical"
)

type checkFinding struct {
	ID       string `json:"id"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	What     string `json:"what"`
	Fix      string `json:"fix"`
	Objects  string `json:"objects,omitempty"`
	// Persian mirror (phase 16, UI-API-CONTRACT.md §2.14): rendered by
	// the Persian-first UI; absent → fall back to the English field.
	TitleFa string `json:"title_fa,omitempty"`
	WhatFa  string `json:"what_fa,omitempty"`
	FixFa   string `json:"fix_fa,omitempty"`
}

type selfcheckReport struct {
	At       time.Time      `json:"at"`
	OK       bool           `json:"ok"` // no critical findings
	Findings []checkFinding `json:"findings"`
}

// scFaText: the Persian mirror of one finding. what/fix are FORMAT
// templates fed the SAME args as the English one (explicit %[n] indexes
// keep the order language-free); title is static.
type scFaText struct {
	Title string
	What  string
	Fix   string
}

// selfcheckFa: one entry per finding id. An id without an entry (or an
// empty field inside) leaves the corresponding _fa field absent and the
// UI falls back to the English text.
var selfcheckFa = map[string]scFaText{
	"tunnels.read": {
		"خواندن تونل‌ها ممکن نشد",
		"خواندن پایگاه‌داده شکست خورد: %[1]v",
		"لاگ پنل را بررسی کنید؛ ممکن است پایگاه‌داده خراب باشد.",
	},
	"tunnel.missing-server": {
		"تونل فعال به سرور ناموجود اشاره می‌کند",
		"تونل %[1]s (موتور %[2]s) به سرور %[3]d اشاره می‌کند که وجود ندارد.",
		"تونل را غیرفعال کنید یا سرور را دوباره بسازید.",
	},
	"tunnel.agent-unlinked": {
		"تونل فعال بدون عامل متصل",
		"تونل %[1]s: سرور «%[2]s» هیچ عاملی متصل ندارد — تونل نمی‌تواند اجرا شود و ترافیکش دیده نمی‌شود.",
		"در تب عامل‌ها یک عامل را به این سرور ثبت/متصل کنید.",
	},
	"tunnel.agent-offline": {
		"تونل فعال با عامل آفلاین",
		"تونل %[1]s: عامل «%[2]s» آفلاین است.",
		"سرویس عامل را روی آن سرور بررسی کنید (systemctl status quantum-agent).",
	},
	"tunnel.engine-unreported": {
		"وضعیت موتور تونل فعال هرگز گزارش نشده",
		"تونل %[1]s از موتور %[2]s استفاده می‌کند اما عامل «%[3]s» هنوز وضعیت این موتور را گزارش نکرده است (نه heartbeat با engine_status آمده، نه عامل قدیمی‌تر از فاز ۱۱ است).",
		"منتظر heartbeat بمانید؛ اگر ادامه یافت، نسخهٔ عامل را بررسی کنید.",
	},
	"tunnel.engine-disabled": {
		"تونل فعال با موتور غیرفعال روی عامل",
		"تونل %[1]s از موتور %[2]s استفاده می‌کند که عامل «%[3]s» آن را غیرفعال گزارش می‌دهد — تونل نمی‌تواند اجرا شود.",
		"عامل‌ها > این عامل > موتورها: موتور را فعال کنید و سپس push بزنید.",
	},
	"tunnel.engine-not-installed": {
		"موتور تونل فعال هرگز نصب نشده",
		"تونل %[1]s: موتور %[2]s روی عامل «%[3]s» فعال است ولی نصب‌نشده گزارش می‌شود (آخرین خطا: %[4]s).",
		"مصرفی (artifact) برای معماری عامل را بررسی کنید (موتورها > مصرفی‌ها) و لاگ عامل را ببینید.",
	},
	"weight.range": {
		"وزن خارج از بازه",
		"توپولوژی «%[1]s» یک سیاست وزن %[2]d دارد (مجاز 0 تا 1000).",
		"از API/UI وزن‌ها اصلاح کنید.",
	},
	"weight.conflict": {
		"ردیف‌های وزن متعارض برای یک تونل",
		"توپولوژی «%[1]s» %[2]d ردیف وزن در سطح تونل برای تونل %[3]d دارد — ترتیب حل‌تعارض فقط یکی را مؤثر می‌کند.",
		"ردیف‌های اضافه را حذف کنید (API وزن‌ها) و یکی را نگه دارید.",
	},
	"counters.nonmonotonic": {
		"شمارنده‌های مصرف داخل یک جریان به عقب برگشته‌اند",
		"%[1]d نمونه در یک جریان تونل+عامل به عقب برگشته (اولین: %[2]s). بایت‌های بین افت و خواندن بعدی از مجموع‌ها حذف می‌شوند.",
		"معمولاً ری‌استارت منبع شمارنده بدون پرچم unknown است؛ منبع شمارندهٔ موتور را بررسی کنید. اگر محدود است نیازی به اقدام نیست.",
	},
	"rollup.never-ran": {
		"Rollup ساعتی هرگز اجرا نشده",
		"هیچ ساعت کامل هنوز جمع نشده (نصب تازه، یا rollup تازه شروع شده).",
		"کاری لازم نیست؛ واترمارک خودکار جلو می‌رود.",
	},
	"rollup.holes": {
		"حفره‌های rollup ساعتی زیر واترمارک",
		"%[1]d جریان نمونهٔ خام در ساعت‌هایی دارند که بدون آن‌ها جمع شده‌اند (اولین: %[2]s). خواندن بازه‌های قدیمی آن جریان‌ها کم‌شماری می‌شود.",
		"معمولاً foldی که وسط ساعت و پیش از رفع فاز ۱۱ اجرا شده؛ با حذف ردیف traffic_rollup_state، fold بعدی بازسازی می‌کند.",
	},
	"sqlite.integrity": {
		"بررسی یکپارچگی SQLite شکست خورد",
		"فایل پایگاه‌داده بررسی یکپارچگی را پاس نکرد: %[1]s",
		"پنل را متوقف کنید، از پوشهٔ داده پشتیبان بگیرید و از آخرین پشتیبان سالم بازیابی کنید.",
	},
	"sqlite.fk": {
		"نقض کلید خارجی موجود است",
		"%[1]d ردیف کلیدهای خارجی را نقض می‌کنند (ارجاع بی‌والد).",
		"ردیف‌های یتیم را پیدا و حذف کنید؛ ویرایش دستی پایگاه‌داده را بررسی کنید.",
	},
	"file.perms": {
		"فایل محرمانه برای گروه/سایر خواندنی است",
		"%[1]s با دسترسی %[2]04o ذخیره شده — اعتبارنامه‌های داخل آن در دسترس دیگر کاربران محلی است.",
		"این دستور را اجرا کنید: %[3]s",
	},
	"tls.unreadable": {
		"گواهی TLS خواندنی نیست",
		"گواهی پیکربندی‌شده تجزیه نشد: %[1]v",
		"دوباره صادر کنید: quantum-panel tls issue (یا tls ip).",
	},
	"tls.expired": {
		"گواهی TLS منقضی شده است",
		"گواهی پنل %[1]d روز پیش منقضی شده — عامل‌ها/مرورگرها هشدار می‌دهند یا رد می‌کنند.",
		"quantum-panel tls renew (یا issue).",
	},
	"tls.expiring": {
		"گواهی TLS به‌زودی منقضی می‌شود",
		"گواهی پنل تا %[1]d روز دیگر منقضی می‌شود.",
		"quantum-panel tls renew؛ اگر KEY عوض شود، پنجرهٔ pin شدن TOFU عامل‌ها را به یاد داشته باشید.",
	},
	"time.sync": {
		"زمان با NTP همگام نیست",
		"ساعت میزبان با NTP همگام نیست — پنجره‌های billing و بررسی گواهی به آن وابسته‌اند.",
		"کلاینت NTP را فعال کنید: timedatectl set-ntp true.",
	},
	"disk.full": {
		"دیسک تقریباً پر است",
		"فایل‌سیستمِ نگه‌دارندهٔ %[1]s تا %[2]d%% پر شده — پایگاه‌داده و پشتیبان‌ها جا لازم دارند.",
		"جا آزاد کنید یا پوشهٔ داده را جابه‌جا کنید؛ فایل‌های WAL قدیمی را بررسی کنید.",
	},
}

// fmtOrVerbatim: Sprintf only when the template actually carries format
// verbs; static texts (many fix lines, the Persian mirrors without
// numbers) must render verbatim instead of swallowing the arg list into
// a %!(EXTRA…) tail.
func fmtOrVerbatim(tmpl string, args []any) string {
	if len(args) == 0 || !strings.Contains(tmpl, "%") {
		return tmpl
	}
	return fmt.Sprintf(tmpl, args...)
}

// add records one finding. what/fix are FORMAT templates; args feed both
// the English render and (when the id has a catalog entry) the Persian
// one. Passing the dynamic values as args (never interpolated into the
// template) keeps % in values from breaking the render.
func (r *selfcheckReport) add(id string, sev checkSeverity, title, what, fix, objects string, args ...any) {
	f := checkFinding{ID: id, Severity: string(sev), Title: title, Objects: objects}
	f.What = fmtOrVerbatim(what, args)
	f.Fix = fmtOrVerbatim(fix, args)
	if fa, ok := selfcheckFa[id]; ok {
		if fa.Title != "" {
			f.TitleFa = fa.Title
		}
		if fa.What != "" {
			f.WhatFa = fmtOrVerbatim(fa.What, args)
		}
		if fa.Fix != "" {
			f.FixFa = fmtOrVerbatim(fa.Fix, args)
		}
	}
	r.Findings = append(r.Findings, f)
	if sev == sevCritical {
		r.OK = false
	}
}

// runSelfcheck executes every check against the CURRENT store.
func runSelfcheck(s *store.Store) *selfcheckReport {
	rep := &selfcheckReport{At: time.Now().UTC(), OK: true}
	checkTunnels(s, rep)
	checkWeights(s, rep)
	checkCountersMonotonic(s, rep)
	checkRollupCoverage(s, rep)
	checkSQLite(s, rep)
	checkFilePerms(rep)
	checkTLS(rep)
	checkTimeSync(rep)
	checkDisk(rep)
	return rep
}

func checkTunnels(s *store.Store, rep *selfcheckReport) {
	tunnels, err := s.ListAllTunnels()
	if err != nil {
		rep.add("tunnels.read", sevCritical, "Tunnels unreadable", "%[1]v", "Check the panel log; the store may be corrupt.", "", err)
		return
	}
	agents, _ := s.ListAgents()
	byServer := map[int64]store.Agent{}
	for _, a := range agents {
		if a.ServerID != 0 {
			byServer[a.ServerID] = a
		}
	}
	for _, t := range tunnels {
		if !t.Enabled {
			continue
		}
		for _, sid := range []int64{t.LocalServerID, t.RemoteServerID} {
			sv, err := s.GetServer(sid)
			if err != nil {
				rep.add("tunnel.missing-server", sevCritical, "Enabled tunnel references a missing server",
					"Tunnel %[1]s (engine %[2]s) points at server %[3]d which does not exist.",
					"Disable the tunnel or recreate the server.", t.TunnelKey, t.TunnelKey, t.Engine, sid)
				continue
			}
			ag, linked := byServer[sid]
			if !linked {
				rep.add("tunnel.agent-unlinked", sevWarning, "Enabled tunnel without a linked agent",
					"Tunnel %[1]s: server %[2]q has no agent linked — the tunnel cannot run and its traffic is invisible.",
					"Agents tab: enroll/link an agent to this server.", t.TunnelKey, t.TunnelKey, sv.Name)
				continue
			}
			if uiAgentStatus(ag) != "online" {
				rep.add("tunnel.agent-offline", sevWarning, "Enabled tunnel whose agent is offline",
					"Tunnel %[1]s: agent %[2]q is offline.",
					"Check the agent service on that server (systemctl status quantum-agent).", t.TunnelKey, t.TunnelKey, ag.Name)
			}
			// Engine state on the agent's side: use the agent's OWN
			// reported engine_status row when present (the legacy
			// desired-default rule would count the tunnel being checked
			// as its own justification and make the check vacuous).
			rows, _ := s.AgentEngines(ag.ID)
			var row *store.AgentEngine
			for i := range rows {
				if rows[i].Engine == t.Engine {
					row = &rows[i]
					break
				}
			}
			switch {
			case row == nil:
				rep.add("tunnel.engine-unreported", sevWarning, "Enabled tunnel whose engine state was never reported",
					"Tunnel %[1]s uses engine %[2]q but agent %[3]q has never reported that engine's status (no heartbeat with engine_status yet, or the agent predates phase 11).",
					"Wait for a heartbeat; if it persists, check the agent version.", t.TunnelKey, t.TunnelKey, t.Engine, ag.Name)
			case !row.Desired:
				rep.add("tunnel.engine-disabled", sevCritical, "Enabled tunnel whose engine is not enabled on the agent",
					"Tunnel %[1]s uses engine %[2]q which the agent %[3]q reports as NOT enabled — the tunnel cannot run.",
					"Agents > the agent > Engines: enable the engine, then push.", t.TunnelKey, t.TunnelKey, t.Engine, ag.Name)
			case !row.Installed && row.Instances == 0:
				rep.add("tunnel.engine-not-installed", sevWarning, "Enabled tunnel whose engine never installed",
					"Tunnel %[1]s: engine %[2]q is enabled on agent %[3]q but reports not-installed (last error: %[4]s).",
					"Check the artifact for the agent's arch (Engines > Artifacts) and the agent log.",
					t.TunnelKey, t.TunnelKey, t.Engine, ag.Name, redactErrorText(row.LastError))
			}
		}
	}
}

func checkWeights(s *store.Store, rep *selfcheckReport) {
	topos, _ := s.ListTopologies()
	for _, tp := range topos {
		ws, err := s.ListWeights(tp.ID)
		if err != nil {
			continue
		}
		seen := map[int64]int{} // tunnel id -> count of tunnel-scope rows
		for _, w := range ws {
			if w.Weight < 0 || w.Weight > 1000 {
				rep.add("weight.range", sevCritical, "Weight out of range",
					"Topology %[1]q has a weight policy of %[2]d (valid 0-1000).",
					"Fix via the weights API/UI.", tp.Name, tp.Name, w.Weight)
			}
			if w.TunnelID != nil {
				seen[*w.TunnelID]++
			}
		}
		for tid, n := range seen {
			if n > 1 {
				rep.add("weight.conflict", sevCritical, "Conflicting weight rows for one tunnel",
					"Topology %[1]q has %[2]d tunnel-scope weight rows for tunnel %[3]d — the resolution order makes only one effective.",
					"Delete the extra rows (weights API), keep one.", tp.Name, tp.Name, n, tid)
			}
		}
	}
}

func checkCountersMonotonic(s *store.Store, rep *selfcheckReport) {
	rows, err := s.DB().Query(`SELECT tunnel_id, agent_id, at, wire_rx, wire_tx, counters_known
		FROM traffic_samples ORDER BY tunnel_id, agent_id, at`)
	if err != nil {
		return
	}
	defer rows.Close()
	var lastKey string
	var lastRX, lastTX int64
	decreases := 0
	firstBad := ""
	for rows.Next() {
		var tid int64
		var agent string
		var at, rx, tx int64
		var known bool
		if err := rows.Scan(&tid, &agent, &at, &rx, &tx, &known); err != nil {
			return
		}
		k := fmt.Sprintf("%d\x00%s", tid, agent)
		if k != lastKey {
			lastKey, lastRX, lastTX = k, rx, tx
			continue
		}
		if known && (rx < lastRX || tx < lastTX) {
			decreases++
			if firstBad == "" {
				firstBad = fmt.Sprintf("tunnel %d agent %s at %d", tid, agent, at)
			}
		}
		lastRX, lastTX = rx, tx
	}
	if decreases > 0 {
		rep.add("counters.nonmonotonic", sevWarning, "Usage counters decreased inside a stream",
			"%[1]d sample(s) went backwards within one tunnel+agent stream (first: %[2]s). Bytes between the drop and the next reading are LOST from the totals.",
			"Usually a counter source restart without the unknown flag; check the engine's counter source. No action if bounded.",
			firstBad, decreases, firstBad)
	}
}

func checkRollupCoverage(s *store.Store, rep *selfcheckReport) {
	var done int64
	if err := s.DB().QueryRow(`SELECT done_until FROM traffic_rollup_state WHERE id = 1`).Scan(&done); err != nil {
		if err == sql.ErrNoRows {
			rep.add("rollup.never-ran", sevInfo, "Hourly rollups have never run",
				"No complete hour has been folded yet (a fresh install or the rollup just started).",
				"Nothing to do; the watermark advances automatically.", "")
			return
		}
		return
	}
	// Hole check: for every (tunnel, agent) stream present in raw
	// samples older than the watermark, at least one hourly row must
	// exist per hour that the stream reported.
	rows, err := s.DB().Query(`SELECT DISTINCT tunnel_id, agent_id, at/3600 FROM traffic_samples
		WHERE at/3600 < ? ORDER BY tunnel_id, agent_id, at/3600`, done)
	if err != nil {
		return
	}
	defer rows.Close()
	type sk struct {
		tid int64
		ag  string
	}
	hours := map[sk][]int64{}
	for rows.Next() {
		var k sk
		var h int64
		if err := rows.Scan(&k.tid, &k.ag, &h); err != nil {
			return
		}
		hours[k] = append(hours[k], h)
	}
	rows.Close()
	holeStreams := 0
	firstHole := ""
	for k, hs := range hours {
		present := map[int64]bool{}
		for _, h := range hs {
			present[h] = true
		}
		hrow, err := s.DB().Query(`SELECT hour FROM traffic_hourly WHERE tunnel_id = ? AND agent_id = ?`, k.tid, k.ag)
		if err != nil {
			continue
		}
		for hrow.Next() {
			var h int64
			_ = hrow.Scan(&h)
			present[h] = false // covered
		}
		hrow.Close()
		missing := 0
		for h, need := range present {
			if need {
				missing++
				if firstHole == "" {
					firstHole = fmt.Sprintf("tunnel %d agent %s hour %d", k.tid, k.ag, h)
				}
			}
		}
		if missing > 0 {
			holeStreams++
		}
	}
	if holeStreams > 0 {
		rep.add("rollup.holes", sevWarning, "Hourly rollup holes under the watermark",
			"%[1]d stream(s) have raw samples in hours that were folded WITHOUT them (first: %[2]s). Old-range reads undercount those streams.",
			"Usually a fold that ran mid-hour before phase 11 fixed the watermark; re-fold by deleting the traffic_rollup_state row and letting the next fold rebuild.",
			firstHole, holeStreams, firstHole)
	}
}

func checkSQLite(s *store.Store, rep *selfcheckReport) {
	var integrity string
	if err := s.DB().QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err == nil && integrity != "ok" {
		rep.add("sqlite.integrity", sevCritical, "SQLite integrity_check FAILED",
			"The database file failed its integrity check: %[1]s",
			"Stop the panel, back up the directory, and restore from the last good backup.", "", integrity)
	}
	rows, err := s.DB().Query(`PRAGMA foreign_key_check`)
	if err == nil {
		bad := 0
		for rows.Next() {
			bad++
		}
		rows.Close()
		if bad > 0 {
			rep.add("sqlite.fk", sevCritical, "Foreign-key violations present",
				"%[1]d row(s) violate foreign keys (orphaned references).",
				"Identify and delete the orphaned rows; check for manual DB edits.", "", bad)
		}
	}
}

// checkPermsDir is the directory the perms check scans (a seam: tests
// point it at a dedicated dir — the real configDir's files race with
// the hub's async session persistence).
var checkPermsDir = func() string { return configDir }

func checkFilePerms(rep *selfcheckReport) {
	if runtime.GOOS == "windows" {
		return
	}
	for _, name := range []string{"panel.json", "sessions.json", "gre_sealed.key", "agent-update.json"} {
		p := filepath.Join(checkPermsDir(), name)
		fi, err := os.Stat(p)
		if err != nil {
			continue // absent: fine
		}
		mode := fi.Mode().Perm()
		if mode&0o077 != 0 {
			fix := "chmod 600 " + p
			if mode&0o040 != 0 {
				fix = "chmod 640 " + p
			}
			rep.add("file.perms", sevCritical, "Secret file is group/world readable",
				"%[1]s has mode %[2]04o — credentials inside are exposed to other local users.",
				"%[3]s", name, p, uint32(mode), fix)
		}
	}
}

func checkTLS(rep *selfcheckReport) {
	if !tlsHasCert() {
		return
	}
	exp, _, err := tlsCertExpiry()
	if err != nil {
		rep.add("tls.unreadable", sevWarning, "TLS certificate unreadable",
			"The configured certificate failed to parse: %[1]v",
			"Re-issue with: quantum-panel tls issue (or tls ip).", "", err)
		return
	}
	days := int(time.Until(exp).Hours() / 24)
	switch {
	case days < 0:
		rep.add("tls.expired", sevCritical, "TLS certificate EXPIRED",
			"The panel certificate expired %[1]d days ago — agents/browsers refuse or warn.",
			"quantum-panel tls renew (or issue).", "", -days)
	case days < 15:
		rep.add("tls.expiring", sevWarning, "TLS certificate expiring soon",
			"The panel certificate expires in %[1]d days.",
			"quantum-panel tls renew; remember the agents' TOFU pin window if the KEY changes.", "", days)
	}
}

func checkTimeSync(rep *selfcheckReport) {
	if runtime.GOOS != "linux" {
		return
	}
	out, _ := runCmdTimeout(5*time.Second, "timedatectl", "show", "--property=NTPSynchronized", "--value")
	synced := strings.TrimSpace(string(out)) == "yes"
	if !synced {
		rep.add("time.sync", sevWarning, "Time not NTP-synchronized",
			"The host clock is not NTP-synchronized — billing windows and certificate checks depend on it.",
			"Enable the NTP client: timedatectl set-ntp true.", "")
	}
}

func checkDisk(rep *selfcheckReport) {
	if runtime.GOOS == "windows" {
		return
	}
	// statvfs via df: the config dir's filesystem
	out, _ := runCmdTimeout(5*time.Second, "df", "--output=pcent", configDir)
	lines := strings.Fields(strings.TrimSpace(string(out)))
	if len(lines) < 2 {
		return
	}
	pctStr := strings.TrimSuffix(lines[len(lines)-1], "%")
	var pct int
	if _, err := fmt.Sscanf(pctStr, "%d", &pct); err != nil {
		return
	}
	if pct >= 90 {
		sev := sevWarning
		if pct >= 95 {
			sev = sevCritical
		}
		rep.add("disk.full", sev, "Disk almost full",
			"The filesystem holding %[1]s is %[2]d%% full — the store and backups need headroom.",
			"Free space or move the data dir; check old WAL files.", configDir, configDir, pct)
	}
}

// cliSelfcheck implements `quantum-panel selfcheck`.
func cliSelfcheck(args []string) int {
	if v := os.Getenv("GRE_PANEL_DIR"); v != "" {
		configDir = v
	}
	s, err := MeshStore()
	if err != nil {
		fmt.Fprintln(os.Stderr, "selfcheck: store:", err)
		return 1
	}
	rep := runSelfcheck(s)
	brief := len(args) > 0 && args[0] == "--brief"
	for _, f := range rep.Findings {
		if brief && f.Severity == string(sevOK) {
			continue
		}
		fmt.Printf("[%s] %s\n  what: %s\n  fix:  %s\n", strings.ToUpper(f.Severity), f.Title, f.What, f.Fix)
	}
	n := 0
	for _, f := range rep.Findings {
		if f.Severity == string(sevCritical) || f.Severity == string(sevWarning) {
			n++
		}
	}
	fmt.Printf("selfcheck: %d finding(s), ok=%v\n", len(rep.Findings), rep.OK)
	if !rep.OK {
		return 1
	}
	_ = n
	return 0
}

// handleUISelfcheck serves GET /api/ui/selfcheck.
func handleUISelfcheck(w http.ResponseWriter, r *http.Request) {
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	writeJSON(w, runSelfcheck(h.Store))
}
