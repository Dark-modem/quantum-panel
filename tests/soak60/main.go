// soak60 — phase 12 TASK 3 scale/soak driver (standalone, black-box).
//
// Shape (SOAK_PAIRS pairs, default 30 = the historical run): N topologies
// × (1 iran + 1 foreign server) × 6 tunnels/pair; 2 fake v2 agents per
// pair each reporting those 6 tunnels → pairs*2 agents, pairs*12 tunnel
// counter streams over REAL websockets with realistic 30 s heartbeats
// (cumulative counters, proc[], engine_status[], resources). UI load:
// 20 concurrent pollers — 8 on /api/ui/overview (10 s) + 12 spread over
// the folded /api/ui alias mix (topologies, engines, alerts, pasarguard,
// agents list, tunnel detail; 2 each, 10 s). Every 30 s one metrics row
// lands in SOAK_OUT/metrics.csv; the final summary (p95 per endpoint,
// non-200 counts, WS-reconnect records) goes to SOAK_OUT/summary.json.
//
// WS reconnect under load (phase 20): SOAK_DROP_AT=<duration> (from
// start) + SOAK_DROP_N=<n> force-close the first N agents' live
// connections mid-soak; each agent's redial loop re-establishes, and
// per-agent drop→recovered latencies land in SOAK_OUT/reconnect.json.
//
// Run against a real panel process (SOAK_PANEL_PID lets the sampler read
// its /proc for CPU; SOAK_DB_PATH points at quantum.db for size/WAL
// tracking; both optional — a containerized panel samples those from
// outside the driver).
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/Dark-modem/quantum-protocol"
)

var (
	panelURL string // http://127.0.0.1:PORT/base
	panelPID int
	outDir   string
	duration time.Duration
	dbPath   string
	cores    = 1
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

type client struct {
	http *http.Client
	csrf string
}

func login(user, pass string) (*client, error) {
	jar, _ := cookiejar.New(nil)
	c := &client{http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	body, _ := json.Marshal(map[string]string{"username": user, "password": pass})
	resp, err := c.http.Post(panelURL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("login: %d %s", resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if tok, ok := out["csrf_token"].(string); ok {
		c.csrf = tok
	}
	return c, nil
}

func (c *client) get(path string) (int, []byte) {
	resp, err := c.http.Get(panelURL + path)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (c *client) post(path string, body any) (int, []byte) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", panelURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", c.csrf)
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func (c *client) mustPost(path string, body any) map[string]any {
	code, raw := c.post(path, body)
	if code != 200 {
		panic(fmt.Sprintf("POST %s: %d %s", path, code, raw))
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

func (c *client) mustGet(path string) map[string]any {
	code, raw := c.get(path)
	if code != 200 {
		panic(fmt.Sprintf("GET %s: %d %s", path, code, raw))
	}
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return m
}

type soakAgent struct {
	id, key    string
	tunnels    []string
	wireRX     uint64
	heartbeats int
	sendErrs   int
	// phase 20: WS-reconnect tracking (all under mu)
	curWS     *websocket.Conn
	dropAt    time.Time // set by the dropper right before force-close
	recoverAt time.Time // set on the first successful heartbeat write after the drop
	hbFail    bool      // set by heartbeat() on a write error → the session loop redials
	mu        sync.Mutex
}

var (
	startAt     = time.Now()
	agents      []*soakAgent
	tunnelKeys  []string
	agentsMu    sync.Mutex
	latMu       sync.Mutex
	lat         = map[string][]float64{}
	failMu      sync.Mutex
	failCount   = map[string]int{} // endpoint name -> non-200 responses
	totalHB     int64
	totalSendEr int64
	soakPairs   = 30
)

func recordLat(name string, ms float64) {
	latMu.Lock()
	lat[name] = append(lat[name], ms)
	latMu.Unlock()
}

func recordFail(name string) {
	failMu.Lock()
	failCount[name]++
	failMu.Unlock()
}

func main() {
	panelURL = strings.TrimRight(env("SOAK_PANEL_URL", ""), "/")
	outDir = env("SOAK_OUT", "/tmp/soak60")
	dbPath = env("SOAK_DB_PATH", "")
	panelPID = envInt("SOAK_PANEL_PID", 0)
	cores = envInt("SOAK_CORES", 1)
	if cores < 1 {
		cores = 1
	}
	if soakPairs = envInt("SOAK_PAIRS", 30); soakPairs < 1 {
		soakPairs = 30
	}
	var err error
	if duration, err = time.ParseDuration(env("SOAK_DURATION", "20m")); err != nil {
		panic(err)
	}
	user, pass := env("SOAK_USER", "admin"), env("SOAK_PASS", "")
	if panelURL == "" || pass == "" {
		panic("SOAK_PANEL_URL and SOAK_PASS required")
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		panic(err)
	}

	c, err := login(user, pass)
	if err != nil {
		panic(err)
	}
	seed(c)
	fmt.Printf("[soak] seeded %d topologies, %d tunnels, %d agents\n", soakPairs, len(tunnelKeys), len(agents))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for _, a := range agents {
		wg.Add(1)
		go func(a *soakAgent) {
			defer wg.Done()
			runAgent(a, stop)
		}(a)
	}

	// phase 20: mid-soak WS reconnect probe — force-close the first
	// SOAK_DROP_N agents' live connections at SOAK_DROP_AT; each agent's
	// own redial loop re-establishes (the reconnect latencies are a
	// property of the redial loop's granularity, not the panel's).
	dropAt, dropErr := time.ParseDuration(env("SOAK_DROP_AT", ""))
	dropN := envInt("SOAK_DROP_N", 5)
	if dropErr == nil && dropAt > 0 && dropN > 0 {
		if dropN > len(agents) {
			dropN = len(agents)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			delay := dropAt - time.Since(startAt)
			if delay > 0 {
				time.Sleep(delay)
			}
			dropped := 0
			for _, a := range agents {
				if dropped >= dropN {
					break
				}
				a.mu.Lock()
				ws := a.curWS
				if ws != nil {
					a.dropAt = time.Now()
				}
				a.mu.Unlock()
				if ws != nil {
					_ = ws.Close() // the agent notices on its next write and redials
					dropped++
				}
			}
			fmt.Printf("[soak] dropped %d agent connections at %s\n", dropped, dropAt)
		}()
	}

	// ---- UI pollers with latency recording ----
	poll := func(name, path string, every time.Duration) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					start := time.Now()
					if code, _ := c.get(path); code == 200 {
						recordLat(name, float64(time.Since(start).Microseconds())/1000.0)
					} else if code != 0 {
						recordFail(name)
					}
				}
			}
		}()
	}
	// phase 20: exactly 20 concurrent pollers — 8 overview + 12 over the
	// folded /api/ui alias mix (2 pollers per alias, 10 s cadence).
	for i := 0; i < 8; i++ {
		poll("overview", "/api/ui/overview", 10*time.Second)
	}
	aliasMix := []string{
		"topologies_list", "/api/ui/topologies",
		"engines_tab", "/api/ui/engines",
		"alerts_view", "/api/ui/alerts",
		"pasarguard_list", "/api/ui/pasarguard",
		"agents_list", "/api/ui/agents",
		"tunnel_detail", "/api/ui/tunnels/" + tunnelKeys[0],
	}
	for i := 0; i < 12; i++ {
		poll(aliasMix[(i*2)%len(aliasMix)], aliasMix[(i*2+1)%len(aliasMix)], 10*time.Second)
	}

	// ---- metrics sampler every 30 s ----
	metrics, err := os.Create(outDir + "/metrics.csv")
	if err != nil {
		panic(err)
	}
	defer metrics.Close()
	fmt.Fprintln(metrics, "t_s,cpu_pct,rss_mb,goroutines,heap_mb,fds,db_mb,wal_mb,heartbeats,send_errs")
	cpuLast := readProcJiffies(panelPID)
	sampleStart := time.Now()
	sample := func() {
		now := time.Now()
		stats := c.mustGet("/api/selfstats")
		proc, _ := stats["process"].(map[string]any)
		cpuNow := readProcJiffies(panelPID)
		cpuPct := 0.0
		if dt := now.Sub(sampleStart).Seconds(); dt > 0 && cpuNow > cpuLast {
			cpuPct = float64(cpuNow-cpuLast) / 100.0 / dt * 100 / float64(cores)
		}
		cpuLast, sampleStart = cpuNow, now
		agentsMu.Lock()
		hb, errs := totalHB, totalSendEr
		agentsMu.Unlock()
		fmt.Fprintf(metrics, "%.0f,%.1f,%.1f,%v,%.1f,%v,%.2f,%.2f,%d,%d\n",
			time.Since(startAt).Seconds(), cpuPct,
			toF(proc["rss_bytes"])/1e6, proc["goroutines"],
			toF(proc["heap_alloc"])/1e6, proc["open_fds"],
			fileMB(dbPath), fileMB(dbPath+"-wal"), hb, errs)
	}
	sample()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		<-t.C
		if time.Now().After(deadline) {
			break
		}
		sample()
	}
	close(stop)
	wg.Wait()

	summary := map[string]any{
		"duration_s": time.Since(startAt).Seconds(),
		"p95_ms":     map[string]float64{},
		"pairs":      soakPairs,
		"agents":     len(agents),
		"tunnels":    len(tunnelKeys),
		"pollers":    20,
	}
	latMu.Lock()
	for name, vals := range lat {
		sort.Float64s(vals)
		summary["p95_ms"].(map[string]float64)[name] = vals[len(vals)*95/100]
	}
	latMu.Unlock()
	failMu.Lock()
	summary["ui_non200"] = failCount
	failMu.Unlock()
	// reconnect records: only agents that were actually dropped
	type rec struct {
		Agent       string `json:"agent"`
		DroppedAtMs int64  `json:"dropped_at_ms"`
		RecoveredMs int64  `json:"recovered_after_ms"`
	}
	recs := []rec{}
	agentsMu.Lock()
	for _, a := range agents {
		a.mu.Lock()
		if !a.dropAt.IsZero() {
			r := rec{Agent: a.id, DroppedAtMs: a.dropAt.Sub(startAt).Milliseconds()}
			if !a.recoverAt.IsZero() {
				r.RecoveredMs = a.recoverAt.Sub(a.dropAt).Milliseconds()
			}
			recs = append(recs, r)
		}
		a.mu.Unlock()
	}
	agentsMu.Unlock()
	if b, err := json.MarshalIndent(recs, "", "  "); err == nil {
		_ = os.WriteFile(outDir+"/reconnect.json", b, 0o644)
	}
	summary["dropped_agents"] = len(recs)
	sample()
	b, _ := json.MarshalIndent(summary, "", "  ")
	_ = os.WriteFile(outDir+"/summary.json", b, 0o644)
	fmt.Printf("[soak] done: %s\n", b)
}

func seed(c *client) {
	c.mustPost("/api/mesh/datacenters", map[string]any{"kind": "iran", "name": "soak-iran", "price_coefficient": 1.0})
	c.mustPost("/api/mesh/datacenters", map[string]any{"kind": "foreign", "name": "soak-foreign", "price_coefficient": 1.0})
	dcs := c.mustGet("/api/mesh/datacenters")
	dcID := map[string]float64{}
	for _, raw := range dcs["datacenters"].([]any) {
		m := raw.(map[string]any)
		dcID[m["name"].(string)] = m["id"].(float64)
	}
	iranServers, foreignServers := []float64{}, []float64{}
	pairs := [][2]float64{}
	for i := 0; i < soakPairs; i++ {
		_, raw1 := c.post("/api/mesh/servers", map[string]any{
			"name": fmt.Sprintf("soak-ir-%02d", i), "role": "iran",
			"datacenter_id": dcID["soak-iran"], "address": fmt.Sprintf("10.190.0.%d", i+1)})
		_, raw2 := c.post("/api/mesh/servers", map[string]any{
			"name": fmt.Sprintf("soak-fr-%02d", i), "role": "foreign",
			"datacenter_id": dcID["soak-foreign"], "address": fmt.Sprintf("10.191.0.%d", i+1)})
		var irM, frM map[string]any
		_ = json.Unmarshal(raw1, &irM)
		_ = json.Unmarshal(raw2, &frM)
		iranServers = append(iranServers, irM["id"].(float64))
		foreignServers = append(foreignServers, frM["id"].(float64))
		pairs = append(pairs, [2]float64{irM["id"].(float64), frM["id"].(float64)})
	}
	for i := 0; i < soakPairs; i++ {
		c.mustPost("/api/mesh/topologies/generate", map[string]any{
			"name":             fmt.Sprintf("soak-topo-%02d", i),
			"iran_servers":     []float64{iranServers[i]},
			"foreign_servers":  []float64{foreignServers[i]},
			"tunnels_per_pair": 6,
			"engines":          []string{"matrix", "tinc", "matrix", "tinc", "matrix", "tinc"},
			"enabled":          true,
		})
		for side, serverID := range []float64{pairs[i][0], pairs[i][1]} {
			role := "iran"
			if side == 1 {
				role = "foreign"
			}
			agentsMu.Lock()
			agents = append(agents, enrollAgent(c, role, i, serverID))
			agentsMu.Unlock()
		}
	}
	// Attach tunnel keys (both ends of a pair report the same 6).
	for i := 0; i < soakPairs; i++ {
		topos := c.mustGet("/api/mesh/topologies")
		var topoID float64
		for _, raw := range topos["topologies"].([]any) {
			m := raw.(map[string]any)
			if m["name"].(string) == fmt.Sprintf("soak-topo-%02d", i) {
				topoID = m["id"].(float64)
			}
		}
		detail := c.mustGet(fmt.Sprintf("/api/mesh/topologies/%v", topoID))
		keys := []string{}
		for _, raw := range detail["tunnels"].([]any) {
			keys = append(keys, raw.(map[string]any)["tunnel_key"].(string))
		}
		agentsMu.Lock()
		agents[i*2].tunnels = keys
		agents[i*2+1].tunnels = keys
		agentsMu.Unlock()
		tunnelKeys = append(tunnelKeys, keys...)
	}
	for _, a := range agents {
		c.mustPost("/api/mesh/agents/"+a.id+"/push", nil)
	}
}

func enrollAgent(c *client, role string, idx int, serverID float64) *soakAgent {
	tok := c.mustPost("/api/agents/tokens", map[string]any{"role": role, "label": "soak", "expires_days": 1})
	enrollBody := fmt.Sprintf(`{"name":"soak-%s-%02d","hostname":"soak","os":"linux","arch":"amd64","version":"soak","caps":[]}`, role, idx)
	req, _ := http.NewRequest("POST", panelURL+"/api/agent/enroll", strings.NewReader(enrollBody))
	req.Header.Set("X-Enroll-Token", tok["token"].(string))
	resp, err := c.http.Do(req)
	if err != nil {
		panic(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		panic(fmt.Sprintf("enroll: %d %s", resp.StatusCode, raw))
	}
	var er struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	_ = json.Unmarshal(raw, &er)
	c.mustPost("/api/mesh/agents/"+er.ID+"/link", map[string]any{"server_id": serverID})
	c.mustPost("/api/mesh/agents/"+er.ID+"/engine-catalog",
		map[string]any{"catalog": []map[string]any{{"id": "matrix"}, {"id": "tinc"}}})
	return &soakAgent{id: er.ID, key: er.Key}
}

func toF(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return -1
}

func fileMB(path string) float64 {
	if path == "" {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return float64(fi.Size()) / 1e6
}

func readProcJiffies(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(b)
	i := strings.LastIndex(s, ")")
	if i < 0 {
		return 0
	}
	f := strings.Fields(s[i+2:])
	if len(f) < 13 {
		return 0
	}
	ut, _ := strconv.ParseFloat(f[11], 64)
	st, _ := strconv.ParseFloat(f[12], 64)
	return int64(ut + st)
}

// runAgent holds one agent's websocket session: hello → welcome, then a
// 30 s heartbeat loop with growing counters + proc + engine status.
func runAgent(a *soakAgent, stop chan struct{}) {
	hdr := http.Header{}
	hdr.Set(protocol.HeaderAgentID, a.id)
	hdr.Set(protocol.HeaderAgentKey, a.key)
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	for {
		select {
		case <-stop:
			return
		default:
		}
		ws, _, err := dialer.Dial(strings.Replace(panelURL, "http://", "ws://", 1)+"/api/agent/ws", hdr)
		if err != nil {
			a.mu.Lock()
			wasDropped := !a.dropAt.IsZero()
			a.mu.Unlock()
			if wasDropped {
				fmt.Printf("[soak-agent] %s dial error after drop: %v\n", a.id, err)
			}
			time.Sleep(2 * time.Second)
			continue
		}
		hj, _ := json.Marshal(protocol.Hello{ID: a.id, Version: "soak", Hostname: "soak",
			OS: "linux", Arch: "amd64", MaxVersion: protocol.ProtoVersionV2})
		_ = ws.WriteJSON(protocol.Envelope{V: 2, Type: protocol.MsgHello, Data: hj})
		ws.SetReadDeadline(time.Now().Add(15 * time.Second))
		var env protocol.Envelope
		if err := ws.ReadJSON(&env); err != nil || env.Type != protocol.MsgWelcome {
			ws.Close()
			time.Sleep(2 * time.Second)
			continue
		}
		ws.SetReadDeadline(time.Time{})
		a.mu.Lock()
		a.curWS = ws
		a.mu.Unlock()
		tick := time.NewTicker(30 * time.Second)
		heartbeat := func() {
			agentsMu.Lock()
			a.heartbeats++
			totalHB++
			agentsMu.Unlock()
			a.wireRX += uint64(20000 + a.heartbeats*137)
			tuns := make([]protocol.TunnelRuntime, 0, len(a.tunnels))
			procs := make([]protocol.TunnelProc, 0, len(a.tunnels))
			for _, k := range a.tunnels {
				tuns = append(tuns, protocol.TunnelRuntime{
					TunnelID: k, Engine: "matrix", Up: true, LatencyMs: 12.5,
					WireRx: a.wireRX, WireTx: a.wireRX / 8,
					PayloadRx: a.wireRX - a.wireRX/16, PayloadTx: a.wireRX / 9,
					CountersKnown: true, WireKnown: true, PayloadKnown: true,
					UptimeS: int64(a.heartbeats * 30),
				})
				procs = append(procs, protocol.TunnelProc{
					TunnelID: k, Known: true,
					CPUPercent: 1.0 + float64(a.heartbeats%7), RSSBytes: int64(8+a.heartbeats%20) << 20,
					Processes: 1,
				})
			}
			st := protocol.StateReportV2{
				AppliedRevision: "1000000000000000001",
				Tunnels:         tuns,
				Proc:            procs,
				EngineStatus: []protocol.EngineStatusReport{
					{Engine: "matrix", Installed: true, Enabled: true, Instances: len(a.tunnels)},
					{Engine: "tinc", Installed: true, Enabled: false},
				},
				Resources: &protocol.ResourceReport{CPUCores: 4, CPUPercent: 22.5,
					MemTotalMB: 8192, MemUsedMB: 3100, DiskTotalGB: 80, DiskUsedGB: 30, UptimeS: 100000, Virt: "kvm"},
			}
			hb, _ := json.Marshal(protocol.HeartbeatV2{TS: time.Now().Unix(), State: &st})
			ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := ws.WriteJSON(protocol.Envelope{V: 2, Type: protocol.MsgHeartbeat, Data: hb}); err != nil {
				a.mu.Lock()
				dropped := !a.dropAt.IsZero()
				a.hbFail = true // the session loop must redial (phase 20)
				a.mu.Unlock()
				if dropped {
					fmt.Printf("[soak-agent] %s heartbeat write error: %v\n", a.id, err)
				}
				agentsMu.Lock()
				a.sendErrs++
				totalSendEr++
				agentsMu.Unlock()
			} else {
				// phase 20: a successful write after a forced drop = the
				// redial loop re-established the session
				a.mu.Lock()
				if !a.dropAt.IsZero() && a.recoverAt.IsZero() {
					a.recoverAt = time.Now()
					fmt.Printf("[soak-agent] %s re-established after %.1fs\n",
						a.id, a.recoverAt.Sub(a.dropAt).Seconds())
				}
				a.mu.Unlock()
			}
		}
		heartbeat()
		// phase 20: a dead connection must behave like the real agent —
		// redial. The phase-12 loop had NO error-exit path: after a
		// mid-stream drop the fake agent kept ticking on the dead conn
		// forever (every heartbeat write failing, never re-establishing).
	session:
		for {
			select {
			case <-stop:
				ws.Close()
				return
			case <-tick.C:
				heartbeat()
				a.mu.Lock()
				hbFail := a.hbFail
				a.mu.Unlock()
				if hbFail {
					a.mu.Lock()
					a.hbFail = false
					if a.curWS == ws {
						a.curWS = nil
					}
					a.mu.Unlock()
					ws.Close()
					time.Sleep(2 * time.Second) // agent-like redial backoff
					break session
				}
			}
		}
	}
}
