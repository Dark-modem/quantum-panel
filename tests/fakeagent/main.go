// fakeagent — a minimal v2 agent for local UI audits and performance
// runs: enrolls with a token, connects over WS, and speaks the REAL v2
// wire — quantum-protocol types end to end (Hello, DesiredV2→Ack,
// Command→CommandResult, heartbeats carrying StateReportV2), encoded the
// way the real agent encodes a negotiated v2 session
// (protocol.EncodeVersioned(ProtoVersionV2, …), quantum-agent
// agentproto.go agentEncodeV2). BACKEND-33 aligned it to the real
// agent's v2 paths: R7 reported_at with a clock seam, per-tunnel
// overhead ratios, the R3 rich engine catalog, ack kind, the DesiredV2
// EngineSpec "id" key (the old hand-decode read "engine" and never
// flipped), applied-revision/engine state persisted across reconnects,
// hardening safe-apply mirror, no null last_error. Drift table:
// 00-ADMIN/PHASE33-BACKEND-REPORT.md. NOT shipped.
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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-protocol"
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

// agentReportClock mirrors the real agent's R7 clock seam (quantum-agent
// internal/agent agentReportClock): the agent's OWN clock stamped into
// every state report as reported_at. Tests override it (restore in
// t.Cleanup) — the heartbeat envelope ts stays the wall clock, like the
// real agent.
var agentReportClock = time.Now

// agentHeartbeatV2 mirrors the real agent's v2 heartbeat wrapper
// (quantum-agent agent.go sendHeartbeat): sender wall-clock ts, session
// uptime seconds, and the full StateReportV2. The fake's uptime is demo
// scale (fixed 86400).
type agentHeartbeatV2 struct {
	TS      int64                   `json:"ts"`
	UptimeS int64                   `json:"uptime_s"`
	State   *protocol.StateReportV2 `json:"state,omitempty"`
}

// fakeEngStat is one -engines entry: engine=running|stopped|disabled.
type fakeEngStat struct{ name, mode string }

// fakeRuntime is the per-connection mutable state the builders read —
// the part of a real agent between "desired applied" and "next report":
// engine enablement from the last desired, the revision it applied, the
// safe-apply mirror, and the demo counters. Process-lifetime (like the
// real agent's persisted state): a reconnect must NOT blank
// applied_revision or re-enable disabled engines.
type fakeRuntime struct {
	ids           []string
	engOf         map[string]string
	engStats      []fakeEngStat
	downCount     int
	traffic       bool
	engineEnabled bool
	appliedRev    string
	rx, tx        uint64
	prx, ptx      uint64
	rng           *rand.Rand
	hardening     *protocol.HardeningRuntime
	hostname      string
	cpuModel      string
	cores         int
	memMB         int
	diskGB        int
}

// buildHello mirrors protocol.Hello: identity id, caps, max_version 2,
// the string engine_catalog AND the R3 rich engines catalog (the panel
// stores the rich one and hard-validates tunnel engine ids against it;
// the string catalog stays for v1-era consumers, like the real agent's
// hello which carries both).
func (rt *fakeRuntime) buildHello(id string) *protocol.Hello {
	catalog := make([]string, 0, len(rt.engStats))
	infos := make([]protocol.EngineInfo, 0, len(rt.engStats))
	for _, es := range rt.engStats {
		catalog = append(catalog, es.name)
		infos = append(infos, protocol.EngineInfo{ID: es.name, Version: "fake-1.0"})
	}
	if len(catalog) == 0 {
		catalog = []string{"matrix"}
		infos = []protocol.EngineInfo{{ID: "matrix", Version: "fake-1.0"}}
	}
	return &protocol.Hello{
		ID:            id,
		Version:       "fake-1.0",
		Hostname:      rt.hostname,
		OS:            "linux",
		Arch:          "amd64",
		Caps:          []string{"matrix", "v2"},
		MaxVersion:    2,
		EngineCatalog: catalog,
		Engines:       infos,
	}
}

// buildDesiredAck mirrors the real agent's ack (quantum-agent v2.go:
// Kind "desired-v2"); the panel matches acks to the pending desired by
// revision and only trusts ok acks for AppliedRevision.
func buildDesiredAck(revision string) *protocol.Ack {
	return &protocol.Ack{Revision: revision, OK: true, Kind: "desired-v2"}
}

// applyDesired converges the fake's engine state with the document —
// protocol.DesiredV2's engines are EngineSpec{ID, Enabled} keyed "id"
// (the old hand-decode read "engine" and never flipped anything) — and
// returns the ack. Other document sections (tunnels, hardening, self…)
// are beyond the fake's demo scope and are simply not acked against.
func (rt *fakeRuntime) applyDesired(dv *protocol.DesiredV2) *protocol.Ack {
	for _, e := range dv.Engines {
		if e.ID == "matrix" {
			rt.engineEnabled = e.Enabled
		}
	}
	if dv.Revision != "" {
		rt.appliedRev = dv.Revision
	}
	return buildDesiredAck(dv.Revision)
}

// applyFakeCommand answers one command. hardening.apply /
// netopt.profile.apply arm the safe-apply mirror (the following
// heartbeats carry protocol.HardeningRuntime, like a real agent's
// pending safe-apply journal; the result data shape is HARDENING.md §5b
// apply: {journal, armed, timer_unit, verify}); the matching confirm ops
// disarm it. Everything else gets a plain ok result — the old blanket
// hardening-shaped data on EVERY op was drift (real agents answer typed
// ops with op-specific data).
func (rt *fakeRuntime) applyFakeCommand(cmd *protocol.Command) *protocol.CommandResult {
	res := &protocol.CommandResult{ID: cmd.ID, OK: true, Output: "fake agent applied"}
	apply, confirm := false, false
	switch cmd.Op {
	case "hardening.apply", "netopt.profile.apply":
		apply = true
	case "hardening.confirm", "netopt.profile.confirm":
		confirm = true
	}
	if apply {
		// Rollback timer: the panel's spelling (rollback_timer_s) first,
		// the module's historical spelling (rollback_timeout_sec) second
		// — both accepted like the real agent does.
		timer := 3600
		if v, err := strconv.Atoi(cmd.Params["rollback_timer_s"]); err == nil && v >= 120 {
			timer = v
		} else if v, err := strconv.Atoi(cmd.Params["rollback_timeout_sec"]); err == nil && v >= 120 {
			timer = v
		}
		name := "fake-profile"
		if p := cmd.Params["payload"]; p != "" {
			var prof struct {
				Name string `json:"name"`
			}
			if json.Unmarshal([]byte(p), &prof) == nil && prof.Name != "" {
				name = prof.Name
			}
		} else if p := cmd.Params["profile"]; p != "" {
			var prof struct {
				Name string `json:"name"`
			}
			if json.Unmarshal([]byte(p), &prof) == nil && prof.Name != "" {
				name = prof.Name
			}
		}
		journal := fmt.Sprintf("hp-fake-%d", agentReportClock().Unix())
		rt.hardening = &protocol.HardeningRuntime{
			JournalID: journal,
			Name:      name,
			Armed:     true,
			DeadlineS: agentReportClock().Unix() + int64(timer),
			TimerUnit: "s",
		}
		res.Data = mustJSON(map[string]any{
			"journal": journal, "armed": true, "timer_unit": "s", "verify": "ok",
		})
	} else if confirm {
		journal := ""
		if rt.hardening != nil {
			journal = rt.hardening.JournalID
		}
		rt.hardening = nil
		res.Data = mustJSON(map[string]any{"journal": journal, "armed": false, "state": "confirmed"})
	}
	return res
}

// buildStateReportV2 mirrors the real agent's agentBuildStateReportV2
// (quantum-agent internal/agent/v2.go): applied-revision echo, per-tunnel
// runtime with both byte views plus per-direction overhead ratios
// (present only when that direction's counter pair is known and payload
// > 0), per-engine overhead samples grouped from the tunnels' counter
// pairs (sorted by engine id), proc samples, engine installation state,
// resources, the safe-apply mirror, and the agent's own clock (R7
// reported_at).
func (rt *fakeRuntime) buildStateReportV2() *protocol.StateReportV2 {
	out := &protocol.StateReportV2{
		AppliedRevision: rt.appliedRev,
		Tunnels:         make([]protocol.TunnelRuntime, 0, len(rt.ids)),
		Proc:            []protocol.TunnelProc{},
		EngineStatus:    make([]protocol.EngineStatusReport, 0, len(rt.engStats)),
		Resources:       rt.buildResources(),
		ReportedAt:      agentReportClock().Unix(),
	}
	samples := map[string]*protocol.EngineOverheadSample{}
	for i, id := range rt.ids {
		up := i >= rt.downCount
		eng, ok := rt.engOf[id]
		if !ok {
			eng = "matrix"
		}
		tr := protocol.TunnelRuntime{
			TunnelID:      id,
			Engine:        eng,
			Up:            up,
			WireKnown:     true,
			PayloadKnown:  rt.traffic,
			CountersKnown: true, // legacy field: wire-known
		}
		if up {
			tr.LatencyMs = 12.0 + float64(i) + rt.rng.Float64()
			tr.LossPct = rt.rng.Float64() * 0.4
			tr.UptimeS = 3600 + int64(i)*60
			if rt.traffic {
				tr.WireRx = rt.rx / uint64(len(rt.ids))
				tr.WireTx = rt.tx / uint64(len(rt.ids))
				tr.PayloadRx = rt.prx / uint64(len(rt.ids))
				tr.PayloadTx = rt.ptx / uint64(len(rt.ids))
				if tr.PayloadRx > 0 {
					tr.OverheadRatio = float64(tr.WireRx) / float64(tr.PayloadRx)
				}
				if tr.PayloadTx > 0 {
					tr.OverheadRatioTx = float64(tr.WireTx) / float64(tr.PayloadTx)
				}
				es := samples[eng]
				if es == nil {
					es = &protocol.EngineOverheadSample{Engine: eng}
					samples[eng] = es
				}
				es.Tunnels++
				es.WireRx += tr.WireRx
				es.PayloadRx += tr.PayloadRx
				es.WireTx += tr.WireTx
				es.PayloadTx += tr.PayloadTx
			}
			out.Proc = append(out.Proc, protocol.TunnelProc{
				TunnelID:   id,
				Known:      true,
				CPUPercent: 3.0 + rt.rng.Float64()*9,
				RSSBytes:   12 << 20,
				Processes:  1,
			})
		} else {
			tr.LastError = "dial tcp 195.201.0.1:443: connect: connection refused"
		}
		out.Tunnels = append(out.Tunnels, tr)
	}
	names := make([]string, 0, len(samples))
	for n := range samples {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		es := samples[n]
		if es.PayloadRx > 0 {
			es.Ratio = float64(es.WireRx) / float64(es.PayloadRx)
		}
		if es.PayloadTx > 0 {
			es.RatioTx = float64(es.WireTx) / float64(es.PayloadTx)
		}
		out.Engines = append(out.Engines, *es)
	}
	for _, es := range rt.engStats {
		installed, enabled := true, false
		instances := 0
		switch es.mode {
		case "running":
			enabled = true
			instances = len(rt.ids) - rt.downCount
		case "stopped":
			enabled = true
		case "disabled":
		}
		if es.name == "matrix" {
			// matrix mirrors the last desired's engine enablement
			enabled = rt.engineEnabled
			if enabled {
				instances = len(rt.ids) - rt.downCount
			} else {
				instances = 0
			}
		}
		out.EngineStatus = append(out.EngineStatus, protocol.EngineStatusReport{
			Engine:      es.name,
			Installed:   installed,
			Version:     "fake-1.0",
			BytesOnDisk: 9 << 20,
			Enabled:     enabled,
			Instances:   instances,
		})
	}
	out.Hardening = rt.hardening // nil when nothing pending (real agent: no journal)
	return out
}

// buildResources mirrors protocol.ResourceReport; the -cpu-model/-cores/
// -memmb/-diskgb demo knobs ride the real field names.
func (rt *fakeRuntime) buildResources() *protocol.ResourceReport {
	return &protocol.ResourceReport{
		CPUModel:    rt.cpuModel,
		CPUCores:    rt.cores,
		CPUPercent:  8.0 + rt.rng.Float64()*20,
		LoadAvg1:    0.4,
		MemTotalMB:  uint64(rt.memMB),
		MemUsedMB:   uint64(rt.memMB)/3 + uint64(rt.rng.Intn(400)),
		DiskTotalGB: uint64(rt.diskGB),
		DiskUsedGB:  uint64(rt.diskGB) / 4,
		UptimeS:     86400,
	}
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

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("{}")
	}
	return b
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
	var engStats []fakeEngStat
	if *enginesSpec != "" {
		for _, kv := range strings.Split(*enginesSpec, ",") {
			kv = strings.TrimSpace(kv)
			if i := strings.Index(kv, "="); i > 0 {
				engStats = append(engStats, fakeEngStat{kv[:i], kv[i+1:]})
			}
		}
	} else {
		engStats = []fakeEngStat{{"matrix", "running"}}
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

	// Process-lifetime agent state (see fakeRuntime): survives reconnects
	// like the real agent's persisted applied revision and engine state.
	rt := &fakeRuntime{
		engOf:         engOf,
		engStats:      engStats,
		ids:           ids,
		downCount:     *downCount,
		traffic:       *traffic,
		engineEnabled: true,
		rng:           rng,
		hostname:      *name,
		cpuModel:      *cpumodel,
		cores:         *cores,
		memMB:         *memmb,
		diskGB:        *diskgb,
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
		// A real agent's reconnect never waits forever: bound the dial (a
		// dropped SYN after a container restart must RETRY, not hang).
		dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		if *insecure {
			dialer = &websocket.Dialer{
				HandshakeTimeout: 10 * time.Second,
				TLSClientConfig:  &tls.Config{InsecureSkipVerify: true},
			}
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
		write := func(typ string, payload any) {
			// The real agent encodes a negotiated v2 session exactly like
			// this (agentEncodeV2 → protocol.EncodeVersioned
			// (ProtoVersionV2, …)); v1 fallback is out of scope — the fake
			// is a v2-only citizen.
			if b, err := protocol.EncodeVersioned(protocol.ProtoVersionV2, typ, payload); err == nil {
				wmu.Lock()
				_ = ws.WriteMessage(websocket.TextMessage, b)
				wmu.Unlock()
			}
		}
		write(protocol.MsgHello, rt.buildHello(er.ID))
		// A real agent heartbeats immediately on session start so a
		// (re)connected controller gets the full state at once.
		write(protocol.MsgHeartbeat, agentHeartbeatV2{
			TS: time.Now().Unix(), UptimeS: 86400, State: rt.buildStateReportV2(),
		})
		go func() {
			<-sig
			ws.Close()
		}()
		// inbound: drain welcome; APPLY desired documents into this
		// agent's own engine_status report (the UI reads it back) and —
		// for the e2e — persist the document exactly as received; ANSWER
		// commands with a result so panel flows that need an agent result
		// — e.g. the hardening safe-apply pending-confirm window —
		// complete.
		go func() {
			for {
				_, raw, err := ws.ReadMessage()
				if err != nil {
					close(dead)
					return
				}
				_, typ, data, err := protocol.DecodeAny(raw)
				if err != nil {
					continue
				}
				switch typ {
				case protocol.MsgDesired:
					var dv protocol.DesiredV2
					if json.Unmarshal(data, &dv) != nil {
						continue
					}
					ack := rt.applyDesired(&dv)
					if *desiredOut != "" {
						if err := os.WriteFile(*desiredOut, data, 0o644); err != nil {
							log.Printf("desired-out: %v", err)
						}
					}
					if ack.Revision != "" {
						write(protocol.MsgAck, ack)
					}
				case protocol.MsgCommand:
					var cmd protocol.Command
					if json.Unmarshal(data, &cmd) != nil || cmd.ID == "" {
						continue
					}
					write(protocol.MsgResult, rt.applyFakeCommand(&cmd))
				}
			}
		}()
		tick := time.NewTicker(*interval)
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
				rt.rx += uint64(1200000 + rt.rng.Intn(600000)) // demo scale: ~1.2GB/h per agent ≈ the backfilled history
				rt.tx += uint64(80000 + rt.rng.Intn(40000))
				// wire ≈ payload × ~1.08 (realistic overhead)
				rt.prx = uint64(float64(rt.rx) / 1.08)
				rt.ptx = uint64(float64(rt.tx) / 1.08)
				write(protocol.MsgHeartbeat, agentHeartbeatV2{
					TS: time.Now().Unix(), UptimeS: 86400, State: rt.buildStateReportV2(),
				})
			}
		}
	}
}
