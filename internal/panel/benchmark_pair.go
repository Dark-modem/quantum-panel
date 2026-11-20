package panel

// Pair benchmark (the tunnels/benchmark page redesign): for one topology,
// one Iran agent and one foreign agent, the panel builds a REAL temporary
// tunnel through each engine enabled on BOTH agents, watches the agents
// converge it, reads the live latency and wire counters while it runs and
// removes it. The agents converge the temp tunnels through their normal
// desired-state flow — no new agent capability is required. Throughput rows
// report the counters' own delta: without user traffic through the tunnel
// the row says so instead of inventing numbers.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

type pairBenchResult struct {
	Engine    string  `json:"engine"`
	Status    string  `json:"status"` // ok | converge-timeout | stopped | failed
	ConvergeS float64 `json:"converge_s"`
	RTTMs     float64 `json:"rtt_ms"`
	DownBps   float64 `json:"down_bps"`
	UpBps     float64 `json:"up_bps"`
	Score     int     `json:"score"`
	Note      string  `json:"note,omitempty"`
	TunnelKey string  `json:"tunnel_key,omitempty"`
	Seconds   float64 `json:"seconds"`
}

type pairBenchState struct {
	mu           sync.Mutex
	running      bool
	stop         chan struct{}
	stopOnce     sync.Once
	store        *store.Store
	topoID       int64
	topoName     string
	iranSrv      int64
	foreignSrv   int64
	iranAgent    string
	foreignAgent string
	engines      []string
	cur          int
	phase        string // create | converge | measure | cleanup
	results      []pairBenchResult
	startedAt    time.Time
	finishedAt   time.Time
}

var (
	pairMu  sync.Mutex
	pairCur *pairBenchState
)

// The pair benchmark lives in memory while it runs — a panel restart kills
// the goroutine — but its partial results must NOT vanish with it: the
// state file is rewritten on every progress step and restored on boot, so
// the operator always sees what completed before the restart.
func benchmarkPairFile() string {
	return filepath.Join(configDir, "benchmark-pair.json")
}

func savePairStateLocked() {
	if pairCur == nil {
		return
	}
	pairMu.Unlock()
	defer pairMu.Lock()
	blob, err := json.MarshalIndent(map[string]any{
		"topo_name": pairCur.topoName,
		"engines":   pairCur.engines,
		"cur":       pairCur.cur,
		"phase":     pairCur.phase,
		"results":   pairCur.results,
		"started":   pairCur.startedAt.Unix(),
		"finished":  pairCur.finishedAt.Unix(),
	}, "", "  ")
	if err != nil {
		return
	}
	tmp := benchmarkPairFile() + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, benchmarkPairFile())
}

func restorePairState() {
	blob, err := os.ReadFile(benchmarkPairFile())
	if err != nil {
		return
	}
	var saved struct {
		TopoName string            `json:"topo_name"`
		Engines  []string          `json:"engines"`
		Cur      int               `json:"cur"`
		Phase    string            `json:"phase"`
		Results  []pairBenchResult `json:"results"`
		Started  int64             `json:"started"`
		Finished int64             `json:"finished"`
	}
	if err := json.Unmarshal(blob, &saved); err != nil {
		return
	}
	if len(saved.Results) == 0 {
		return
	}
	pairCur = &pairBenchState{
		topoName:   saved.TopoName,
		engines:    saved.Engines,
		cur:        saved.Cur,
		phase:      saved.Phase,
		results:    saved.Results,
		startedAt:  time.Unix(saved.Started, 0),
		finishedAt: time.Unix(saved.Finished, 0),
	}
	if saved.Finished == 0 && pairCur.cur < len(saved.Engines) {
		pairCur.results = append(pairCur.results, pairBenchResult{
			Engine: saved.Engines[pairCur.cur], Status: "stopped",
			Note: "the panel restarted while this engine was running",
		})
	}
}

var pairRestored sync.Once

func pairBenchSnapshot() map[string]any {
	pairRestored.Do(restorePairState)
	pairMu.Lock()
	defer pairMu.Unlock()
	if pairCur == nil {
		return map[string]any{"running": false}
	}
	p := pairCur
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]any{
		"running":    p.running,
		"topo_id":    p.topoID,
		"topo_name":  p.topoName,
		"engines":    p.engines,
		"cur":        p.cur,
		"cur_engine": "",
		"phase":      p.phase,
		"results":    p.results,
		"started_at": p.startedAt.Unix(),
	}
	if p.cur < len(p.engines) {
		out["cur_engine"] = p.engines[p.cur]
	}
	if !p.finishedAt.IsZero() {
		out["finished_at"] = p.finishedAt.Unix()
	}
	return out
}

func handleBenchmarkPairRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TopologyID    int64    `json:"topology_id"`
		IranServer    int64    `json:"iran_server"`
		ForeignServer int64    `json:"foreign_server"`
		Engines       []string `json:"engines"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
		writeAPIError(w, r, "E-BENCH-02", "invalid json")
		return
	}
	pairMu.Lock()
	defer pairMu.Unlock()
	if pairCur != nil && pairCur.running {
		writeAPIError(w, r, "E-BENCH-01", "a pair benchmark is already running")
		return
	}
	if body.TopologyID <= 0 || body.IranServer <= 0 || body.ForeignServer <= 0 {
		writeAPIError(w, r, "E-BENCH-02", "topology_id, iran_server and foreign_server are required")
		return
	}
	if len(body.Engines) == 0 {
		writeAPIError(w, r, "E-BENCH-02", "pick at least one engine")
		return
	}
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	topo, err := s.GetTopology(body.TopologyID)
	if err != nil {
		writeAPIError(w, r, "E-NOTFOUND", "topology not found")
		return
	}
	members, _ := s.TopologyMembers(body.TopologyID)
	has := func(id int64) bool {
		for _, m := range members {
			if m == id {
				return true
			}
		}
		return false
	}
	if !has(body.IranServer) || !has(body.ForeignServer) {
		writeAPIError(w, r, "E-BENCH-03", "both servers must be members of the topology")
		return
	}
	// every engine must be enabled on BOTH endpoint agents — the same rule
	// a real tunnel obeys
	h := uiStoreHandle{s}
	agIran, err := h.AgentByServer(body.IranServer)
	if err != nil {
		writeAPIError(w, r, "E-BENCH-04", "the Iran server has no enrolled agent")
		return
	}
	agForeign, err := h.AgentByServer(body.ForeignServer)
	if err != nil {
		writeAPIError(w, r, "E-BENCH-04", "the foreign server has no enrolled agent")
		return
	}
	engines := []string{}
	for _, e := range body.Engines {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		dI, _ := h.AgentEngineDesired(agIran.ID, e)
		dF, _ := h.AgentEngineDesired(agForeign.ID, e)
		if !dI || !dF {
			writeAPIError(w, r, "E-BENCH-05", fmt.Sprintf("engine %q is not enabled on both agents", e))
			return
		}
		engines = append(engines, e)
	}
	if len(engines) == 0 {
		writeAPIError(w, r, "E-BENCH-02", "pick at least one engine")
		return
	}
	st := &pairBenchState{
		running:      true,
		stop:         make(chan struct{}),
		store:        s,
		topoID:       body.TopologyID,
		topoName:     topo.Name,
		iranSrv:      body.IranServer,
		foreignSrv:   body.ForeignServer,
		iranAgent:    agIran.ID,
		foreignAgent: agForeign.ID,
		engines:      engines,
		startedAt:    time.Now(),
	}
	pairCur = st
	// savePairStateLocked manages the pairMu unlock/lock itself — the
	// handler already holds it, so an extra Lock here deadlocked the POST
	savePairStateLocked()
	LogSecurityAudit("pair_bench_start", cfg.Username, clientIP(r),
		fmt.Sprintf("topology=%q iran=%d foreign=%d engines=%d", topo.Name, body.IranServer, body.ForeignServer, len(engines)))
	go st.run()
	writeJSON(w, map[string]any{"status": "started", "engines": engines})
}

func (p *pairBenchState) stoppedNow() bool {
	select {
	case <-p.stop:
		return true
	default:
		return false
	}
}

func (p *pairBenchState) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.stop:
		return false
	case <-t.C:
		return true
	}
}

func (p *pairBenchState) finish() {
	p.mu.Lock()
	p.running = false
	p.finishedAt = time.Now()
	p.mu.Unlock()
	pairMu.Lock()
	savePairStateLocked()
	pairMu.Unlock()
}

func (p *pairBenchState) run() {
	for i, eng := range p.engines {
		p.mu.Lock()
		p.cur = i
		p.phase = "create"
		p.mu.Unlock()
		if p.stoppedNow() {
			p.pushResult(pairBenchResult{Engine: eng, Status: "stopped"})
			continue
		}
		engStart := time.Now()
		key := fmt.Sprintf("bench-%s-%d", eng, time.Now().Unix()%100000)
		id, err := p.store.UpsertTunnel(store.Tunnel{
			TunnelKey: key, TopologyID: p.topoID,
			LocalServerID: p.iranSrv, RemoteServerID: p.foreignSrv,
			Engine: eng, EngineParams: "{}", Enabled: true,
		})
		if err != nil {
			p.pushResult(pairBenchResult{Engine: eng, Status: "failed", Note: err.Error()})
			continue
		}
		res := pairBenchResult{Engine: eng, TunnelKey: key}

		// converge: the agents build the temp tunnel from their desired state
		p.mu.Lock()
		p.phase = "converge"
		p.mu.Unlock()
		deadline := time.Now().Add(90 * time.Second)
		converged := false
		for time.Now().Before(deadline) {
			if p.stoppedNow() {
				res.Status = "stopped"
				p.cleanupTunnel(id)
				p.pushResult(res)
				break
			}
			v, ok := uiVerdictsFn()[key]
			if ok && (v == "healthy" || v == "degraded") {
				converged = true
				break
			}
			if !p.sleep(3 * time.Second) {
				res.Status = "stopped"
				p.cleanupTunnel(id)
				p.pushResult(res)
				break
			}
		}
		if res.Status == "stopped" {
			p.finishEngine(engStart)
			continue
		}
		res.ConvergeS = float64(int(time.Since(engStart).Seconds()*10+0.5)) / 10
		if !converged {
			res.Status = "converge-timeout"
			res.Note = "the agents did not bring the tunnel up in 90s"
			p.cleanupTunnel(id)
			p.pushResult(res)
			p.finishEngine(engStart)
			continue
		}

		// measure: the agents' own counter samples over one report window;
		// without user traffic the row says so instead of inventing numbers
		p.mu.Lock()
		p.phase = "measure"
		p.mu.Unlock()
		down, up, have := p.measure(id)
		if have {
			res.DownBps = down
			res.UpBps = up
		} else {
			res.Note = "no user traffic through the tunnel — the counters did not move"
		}
		res.Status = "ok"
		if lat, ok := uiLatencyMap()[key]; ok && lat > 0 {
			res.RTTMs = lat
			res.Score = calculateScore(0, lat, 0)
		}

		p.mu.Lock()
		p.phase = "cleanup"
		p.mu.Unlock()
		p.cleanupTunnel(id)
		res.Seconds = float64(int(time.Since(engStart).Seconds()*10+0.5)) / 10
		p.pushResult(res)
		p.finishEngine(engStart)
	}
	p.finish()
}

func (p *pairBenchState) pushResult(r pairBenchResult) {
	p.mu.Lock()
	p.results = append(p.results, r)
	p.mu.Unlock()
	pairMu.Lock()
	savePairStateLocked()
	pairMu.Unlock()
}

func (p *pairBenchState) finishEngine(engStart time.Time) {
	p.mu.Lock()
	if len(p.results) > 0 {
		p.results[len(p.results)-1].Seconds = float64(int(time.Since(engStart).Seconds()*10+0.5)) / 10
	}
	p.mu.Unlock()
}

func (p *pairBenchState) cleanupTunnel(id int64) {
	_ = p.store.DeleteTunnel(id)
}

// measure waits for one fresh counter sample past tunnel creation, then for
// the NEXT one, and derives the wire throughput from that report window.
func (p *pairBenchState) measure(tunnelID int64) (down, up float64, have bool) {
	h := uiStoreHandle{p.store}
	deadline := time.Now().Add(150 * time.Second)
	var s1 store.TrafficSample
	gotFirst := false
	for time.Now().Before(deadline) {
		if p.stoppedNow() {
			return 0, 0, false
		}
		samples, err := h.RecentSamples(tunnelID, 1)
		if err == nil && len(samples) > 0 {
			if !gotFirst {
				s1 = samples[0]
				gotFirst = true
			} else if samples[0].At > s1.At {
				dt := samples[0].At - s1.At
				if dt <= 0 || !samples[0].CountersKnown || !s1.CountersKnown || samples[0].AgentID != s1.AgentID {
					return 0, 0, false
				}
				if samples[0].WireRx < s1.WireRx || samples[0].WireTx < s1.WireTx {
					return 0, 0, false
				}
				return float64(samples[0].WireRx-s1.WireRx) / float64(dt), float64(samples[0].WireTx-s1.WireTx) / float64(dt), true
			}
		}
		if !p.sleep(2 * time.Second) {
			return 0, 0, false
		}
	}
	return 0, 0, false
}

func handleBenchmarkPairGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, pairBenchSnapshot())
}

func handleBenchmarkPairStop(w http.ResponseWriter, r *http.Request) {
	pairMu.Lock()
	defer pairMu.Unlock()
	if pairCur != nil && pairCur.running {
		pairCur.stopOnce.Do(func() { close(pairCur.stop) })
	}
	writeJSON(w, map[string]any{"status": "stopping"})
}
