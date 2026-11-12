// fakeagent — a minimal v2 agent for local UI audits and performance
// runs: enrolls with a token, connects over WS, and speaks the REAL v2
// envelope ({"v":2,"type":T,"data":P}): hello, then a heartbeat every few
// seconds carrying StateReportV2-shaped tunnel runtime, per-engine
// overhead samples and resources. NOT shipped.
package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	server      = flag.String("server", "http://127.0.0.1:8000/ui", "panel base URL incl base path")
	token       = flag.String("token", "", "enroll token")
	name        = flag.String("name", "fake-agent", "agent name")
	role        = flag.String("role", "iran", "role: iran|foreign")
	tunnels     = flag.Int("tunnels", 3, "tunnels to report (synthetic ids unless -tunnel-ids)")
	tunnelIDs   = flag.String("tunnel-ids", "", "comma-separated REAL tunnel ids to report (joins panel state)")
	downCount   = flag.Int("down", 0, "report the first N tunnels as DOWN (varied health)")
	traffic     = flag.Bool("traffic", true, "report wire/payload bytes")
	interval    = flag.Duration("interval", 3*time.Second, "report interval")
	desiredOut  = flag.String("desired-out", "", "write every received desired document to this file (e2e: verify the compiled state)")
	insecure    = flag.Bool("insecure", false, "skip TLS verification (wss to a self-signed demo panel)")
	enginesSpec = flag.String("engines", "", "engine_status variety: engine=running|stopped|disabled[,...] (default: matrix only)")
	tunnelEng   = flag.String("tunnel-engines", "", "per-tunnel engine overrides: id=engine[,...] (default matrix)")
	identityF   = flag.String("identity-file", "", "cache the enrolled id/key here; reuse on restart (no re-enroll)")
	cpumodel    = flag.String("cpu-model", "Fake Xeon", "reported CPU model")
	cores       = flag.Int("cores", 4, "reported cpu cores")
	memmb       = flag.Int("memmb", 4096, "reported mem_total_mb")
	diskgb      = flag.Int("diskgb", 80, "reported disk_total_gb")
)

// envelope is the v2 wire shape (quantum-protocol Envelope).
type envelope struct {
	V    int             `json:"v"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

func wrap(v int, typ string, payload any) ([]byte, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{V: v, Type: typ, Data: data})
}

// agentIdentity is the enrolled credential pair (id + secret key).
type agentIdentity struct {
	ID  string `json:"id"`
	Key string `json:"key"`
}

func loadIdentity() agentIdentity {
	if *identityF == "" {
		return agentIdentity{}
	}
	b, err := os.ReadFile(*identityF)
	if err != nil {
		return agentIdentity{}
	}
	var id agentIdentity
	if json.Unmarshal(b, &id) != nil || id.ID == "" || id.Key == "" {
		return agentIdentity{}
	}
	fmt.Println("identity reused:", id.ID)
	return id
}

func doEnroll(base string) agentIdentity {
	enrollURL := base + "/api/agent/enroll"
	req, err := http.NewRequest("POST", enrollURL,
		strings.NewReader(fmt.Sprintf(`{"name":%q,"hostname":%q,"os":"linux","arch":"amd64","caps":["matrix","v2"]}`,
			*name, *name)))
	if err != nil {
		log.Fatal("enroll: ", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Enroll-Token", *token)
	client := http.DefaultClient
	if *insecure {
		client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatal("enroll: ", err)
	}
	var er agentIdentity
	_ = json.NewDecoder(resp.Body).Decode(&er)
	resp.Body.Close()
	if er.ID == "" {
		log.Fatal("enroll failed (token consumed? pass a fresh token via -token and the panel's bootstrap enroll route)")
	}
	fmt.Println("enrolled:", er.ID)
	return er
}

func main() {
	flag.Parse()
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	base := strings.TrimRight(*server, "/")
	// identity: reuse a cached enrollment when -identity-file is given (the
	// live demo restarts its container; re-enrolling would mint NEW agent
	// records every time — the panel enrolls are create-only).
	er := loadIdentity()
	if er.ID == "" {
		er = doEnroll(base)
		if *identityF != "" {
			if b, err := json.Marshal(er); err == nil {
				_ = os.WriteFile(*identityF, b, 0o600)
			}
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt)

	// per-tunnel engine assignment (demo: mixed-engine topologies)
	engOf := map[string]string{}
	if *tunnelEng != "" {
		for _, kv := range strings.Split(*tunnelEng, ",") {
			kv = strings.TrimSpace(kv)
			if i := strings.Index(kv, "="); i > 0 {
				engOf[kv[:i]] = kv[i+1:]
			}
		}
	}
	// engine-status variety: engine=running|stopped|disabled
	type engStat struct{ name, mode string }
	var engStats []engStat
	if *enginesSpec != "" {
		for _, kv := range strings.Split(*enginesSpec, ",") {
			kv = strings.TrimSpace(kv)
			if i := strings.Index(kv, "="); i > 0 {
				engStats = append(engStats, engStat{kv[:i], kv[i+1:]})
			}
		}
	} else {
		engStats = []engStat{{"matrix", "running"}}
	}

	ids := []string{}
	if *tunnelIDs != "" {
		for _, s := range strings.Split(*tunnelIDs, ",") {
			if s = strings.TrimSpace(s); s != "" {
				ids = append(ids, s)
			}
		}
	}
	for len(ids) < *tunnels {
		ids = append(ids, fmt.Sprintf("t-%s-%d", *name, len(ids)))
	}

reconnect:
	for {
		select {
		case <-sig:
			return
		default:
		}
		wsURL := "ws" + strings.TrimPrefix(base, "http") + "/api/agent/ws"
		hdr := http.Header{}
		hdr.Set("X-Agent-Id", er.ID)
		hdr.Set("X-Agent-Key", er.Key)
		dialer := websocket.DefaultDialer
		if *insecure {
			dialer = &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
		}
		ws, _, err := dialer.Dial(wsURL, hdr)
		if err != nil {
			log.Printf("ws dial: %v (retry 2s)", err)
			select {
			case <-sig:
				return
			case <-time.After(2 * time.Second):
				continue
			}
		}
		dead := make(chan struct{})
		var wmu sync.Mutex // gorilla: one writer at a time
		// engineEnabled mirrors the last desired state (phase-11: the UI's
		// engine table reads the agent's own engine_status report — a real
		// agent flips these when it applies the desired document).
		engineEnabled := true
		write := func(typ string, payload any) {
			if b, err := wrap(2, typ, payload); err == nil {
				wmu.Lock()
				_ = ws.WriteMessage(websocket.TextMessage, b)
				wmu.Unlock()
			}
		}
		// hello (data = protocol.Hello; ID must match the authenticated identity)
		hello := map[string]any{
			"id": er.ID, "version": "fake-1.0", "hostname": *name,
			"os": "linux", "arch": "amd64", "caps": []string{"matrix", "v2"},
			"max_version": 2, "engine_catalog": func() []string {
				cs := []string{}
				for _, es := range engStats {
					cs = append(cs, es.name)
				}
				if len(cs) == 0 {
					cs = []string{"matrix"}
				}
				return cs
			}(),
		}
		write("hello", hello)
		go func() {
			<-sig
			ws.Close()
		}()
		// inbound: drain welcome/desired; ANSWER commands with a result
		// (ok=true) so panel flows that need an agent ack — e.g. the
		// hardening safe-apply pending-confirm window — complete.
		go func() {
			<-sig
			ws.Close()
		}()
		// inbound: drain welcome/desired; ANSWER commands with a result
		// (ok=true) so panel flows that need an agent ack — e.g. the
		// hardening safe-apply pending-confirm window — complete.
		go func() {
			for {
				_, raw, err := ws.ReadMessage()
				if err != nil {
					close(dead)
					return
				}
				var env envelope
				if json.Unmarshal(raw, &env) != nil {
					continue
				}
				if env.Type == "desired" {
					// APPLY the desired engine list into this agent's own
					// engine_status report (the UI reads it back), and — for
					// the e2e — persist the document exactly as received.
					var dv struct {
						Engines []struct {
							Engine  string `json:"engine"`
							Enabled *bool  `json:"enabled"`
						} `json:"engines"`
					}
					if json.Unmarshal(env.Data, &dv) == nil {
						for _, e := range dv.Engines {
							if e.Engine == "matrix" && e.Enabled != nil {
								engineEnabled = *e.Enabled
							}
						}
					}
					if *desiredOut != "" {
						if err := os.WriteFile(*desiredOut, env.Data, 0o644); err != nil {
							log.Printf("desired-out: %v", err)
						}
					}
					continue
				}
				if env.Type != "command" {
					continue
				}
				var cmd struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(env.Data, &cmd) != nil || cmd.ID == "" {
					continue
				}
				write("result", map[string]any{
					"id": cmd.ID, "ok": true, "output": "fake agent applied",
					"data": map[string]any{"journal": "fake apply", "armed": true,
						"timer_unit": "s", "verify": "ok"},
				})
			}
		}()
		tick := time.NewTicker(*interval)
		var rx, tx, prx, ptx uint64
		report := func() {
			rt := make([]map[string]any, len(ids))
			proc := make([]map[string]any, 0, len(ids))
			for i, id := range ids {
				up := i >= *downCount
				eng, ok := engOf[id]
				if !ok {
					eng = "matrix"
				}
				t := map[string]any{
					"tunnel_id": id, "engine": eng, "up": up,
					"wire_known": true, "payload_known": *traffic, "counters_known": true,
					// v2 additive fields the new UI reads (contract §4)
					"uptime_s": uint64(3600 + i*60),
				}
				if up {
					t["latency_ms"] = 12.0 + float64(i) + rng.Float64()
					t["loss_pct"] = rng.Float64() * 0.4
					proc = append(proc, map[string]any{"tunnel_id": id,
						"cpu_percent": 3.0 + rng.Float64()*9, "rss_bytes": 12 << 20, "known": true})
					if *traffic {
						t["wire_rx"] = rx / uint64(len(ids))
						t["wire_tx"] = tx / uint64(len(ids))
						t["payload_rx"] = prx / uint64(len(ids))
						t["payload_tx"] = ptx / uint64(len(ids))
					}
				} else {
					t["last_error"] = "dial tcp 195.201.0.1:443: connect: connection refused"
				}
				rt[i] = t
			}
			beat := map[string]any{
				"applied_revision": "1",
				"tunnels":          rt,
				"proc":             proc,
				"engine_status": func() []map[string]any {
					out := []map[string]any{}
					for _, es := range engStats {
						installed, enabled := true, false
						instances := 0
						switch es.mode {
						case "running":
							enabled = true
							instances = len(ids) - *downCount
						case "stopped":
							enabled = true
						case "disabled":
						}
						if es.name == "matrix" {
							enabled = engineEnabled
							if enabled {
								instances = len(ids) - *downCount
							} else {
								instances = 0
							}
						}
						out = append(out, map[string]any{
							"engine": es.name, "installed": installed, "version": "fake-1.0",
							"bytes_on_disk": uint64(9 << 20), "enabled": enabled,
							"instances": instances, "last_error": map[string]any(nil),
						})
					}
					return out
				}(),
				"resources": map[string]any{
					"cpu_model": *cpumodel, "cpu_cores": *cores,
					"cpu_percent": 8.0 + rng.Float64()*20, "load_avg_1": 0.4,
					"mem_total_mb": *memmb, "mem_used_mb": uint64(*memmb)/3 + uint64(rng.Intn(400)),
					"disk_total_gb": *diskgb, "disk_used_gb": *diskgb / 4, "uptime_s": 86400,
				},
			}
			if *traffic && prx > 0 {
				// one per-engine overhead sample (wire ≈ payload × 1.08)
				beat["engines"] = []map[string]any{{
					"engine": "matrix", "tunnels": len(ids),
					"wire_rx": rx, "payload_rx": prx, "wire_tx": tx, "payload_tx": ptx,
					"ratio": float64(rx) / float64(prx), "ratio_tx": float64(tx) / float64(ptx),
				}}
			}
			write("heartbeat", map[string]any{
				"ts": time.Now().Unix(), "uptime_s": 86400, "state": beat,
			})
		}
		for {
			select {
			case <-sig:
				ws.Close()
				return
			case <-dead:
				log.Printf("session closed by panel, reconnecting")
				ws.Close()
				goto reconnect
			case <-tick.C:
				rx += uint64(1200000 + rng.Intn(600000)) // demo scale: ~1.2GB/h per agent ≈ the backfilled history
				tx += uint64(80000 + rng.Intn(40000))
				// wire ≈ payload × ~1.08 (realistic overhead)
				prx = uint64(float64(rx) / 1.08)
				ptx = uint64(float64(tx) / 1.08)
				report()
			}
		}
	}
}
