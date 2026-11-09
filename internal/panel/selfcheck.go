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
}

type selfcheckReport struct {
	At       time.Time      `json:"at"`
	OK       bool           `json:"ok"` // no critical findings
	Findings []checkFinding `json:"findings"`
}

func (r *selfcheckReport) add(id string, sev checkSeverity, title, what, fix, objects string) {
	r.Findings = append(r.Findings, checkFinding{ID: id, Severity: string(sev),
		Title: title, What: what, Fix: fix, Objects: objects})
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
		rep.add("tunnels.read", sevCritical, "Tunnels unreadable", err.Error(), "Check the panel log; the store may be corrupt.", "")
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
					fmt.Sprintf("Tunnel %s (engine %s) points at server %d which does not exist.", t.TunnelKey, t.Engine, sid),
					"Disable the tunnel or recreate the server.", t.TunnelKey)
				continue
			}
			ag, linked := byServer[sid]
			if !linked {
				rep.add("tunnel.agent-unlinked", sevWarning, "Enabled tunnel without a linked agent",
					fmt.Sprintf("Tunnel %s: server %q has no agent linked — the tunnel cannot run and its traffic is invisible.", t.TunnelKey, sv.Name),
					"Agents tab: enroll/link an agent to this server.", t.TunnelKey)
				continue
			}
			if uiAgentStatus(ag) != "online" {
				rep.add("tunnel.agent-offline", sevWarning, "Enabled tunnel whose agent is offline",
					fmt.Sprintf("Tunnel %s: agent %q is offline.", t.TunnelKey, ag.Name),
					"Check the agent service on that server (systemctl status quantum-agent).", t.TunnelKey)
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
					fmt.Sprintf("Tunnel %s uses engine %q but agent %q has never reported that engine's status (no heartbeat with engine_status yet, or the agent predates phase 11).",
						t.TunnelKey, t.Engine, ag.Name),
					"Wait for a heartbeat; if it persists, check the agent version.", t.TunnelKey)
			case !row.Desired:
				rep.add("tunnel.engine-disabled", sevCritical, "Enabled tunnel whose engine is not enabled on the agent",
					fmt.Sprintf("Tunnel %s uses engine %q which the agent %q reports as NOT enabled — the tunnel cannot run.", t.TunnelKey, t.Engine, ag.Name),
					"Agents > the agent > Engines: enable the engine, then push.", t.TunnelKey)
			case !row.Installed && row.Instances == 0:
				rep.add("tunnel.engine-not-installed", sevWarning, "Enabled tunnel whose engine never installed",
					fmt.Sprintf("Tunnel %s: engine %q is enabled on agent %q but reports not-installed (last error: %s).",
						t.TunnelKey, t.Engine, ag.Name, redactErrorText(row.LastError)),
					"Check the artifact for the agent's arch (Engines > Artifacts) and the agent log.", t.TunnelKey)
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
					fmt.Sprintf("Topology %q has a weight policy of %d (valid 0-1000).", tp.Name, w.Weight),
					"Fix via the weights API/UI.", tp.Name)
			}
			if w.TunnelID != nil {
				seen[*w.TunnelID]++
			}
		}
		for tid, n := range seen {
			if n > 1 {
				rep.add("weight.conflict", sevCritical, "Conflicting weight rows for one tunnel",
					fmt.Sprintf("Topology %q has %d tunnel-scope weight rows for tunnel %d — the resolution order makes only one effective.", tp.Name, n, tid),
					"Delete the extra rows (weights API), keep one.", tp.Name)
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
			fmt.Sprintf("%d sample(s) went backwards within one tunnel+agent stream (first: %s). Bytes between the drop and the next reading are LOST from the totals.",
				decreases, firstBad),
			"Usually a counter source restart without the unknown flag; check the engine's counter source. No action if bounded.", firstBad)
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
			fmt.Sprintf("%d stream(s) have raw samples in hours that were folded WITHOUT them (first: %s). Old-range reads undercount those streams.",
				holeStreams, firstHole),
			"Usually a fold that ran mid-hour before phase 11 fixed the watermark; re-fold by deleting the traffic_rollup_state row and letting the next fold rebuild.", firstHole)
	}
}

func checkSQLite(s *store.Store, rep *selfcheckReport) {
	var integrity string
	if err := s.DB().QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err == nil && integrity != "ok" {
		rep.add("sqlite.integrity", sevCritical, "SQLite integrity_check FAILED",
			"The database file failed its integrity check: "+integrity,
			"Stop the panel, back up the directory, and restore from the last good backup.", "")
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
				fmt.Sprintf("%d row(s) violate foreign keys (orphaned references).", bad),
				"Identify and delete the orphaned rows; check for manual DB edits.", "")
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
				fmt.Sprintf("%s has mode %04o — credentials inside are exposed to other local users.", p, uint32(mode)),
				fix, name)
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
			"The configured certificate failed to parse: "+err.Error(),
			"Re-issue with: quantum-panel tls issue (or tls ip).", "")
		return
	}
	days := int(time.Until(exp).Hours() / 24)
	switch {
	case days < 0:
		rep.add("tls.expired", sevCritical, "TLS certificate EXPIRED",
			fmt.Sprintf("The panel certificate expired %d days ago — agents/browsers refuse or warn.", -days),
			"quantum-panel tls renew (or issue).", "")
	case days < 15:
		rep.add("tls.expiring", sevWarning, "TLS certificate expiring soon",
			fmt.Sprintf("The panel certificate expires in %d days.", days),
			"quantum-panel tls renew; remember the agents' TOFU pin window if the KEY changes.", "")
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
			fmt.Sprintf("The filesystem holding %s is %d%% full — the store and backups need headroom.", configDir, pct),
			"Free space or move the data dir; check old WAL files.", configDir)
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
