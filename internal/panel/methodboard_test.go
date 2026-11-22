package panel

// methodboard_test.go — BACKEND-42 hermetic tests: the scoring formula is
// checked against hand-computed values, the probe engine runs against
// swapped seams (no network, no agents, tiny timeouts) and the handler's
// hostile-input matrix is exercised directly. Every seam restores in
// t.Cleanup.

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// ---- scoring formula (hand-computed expectations) -------------------------

func TestMbenchScoringFormula(t *testing.T) {
	// latency only (weights renormalize): 30 ms → term 0.9 → 90
	s, basis, measured := mbenchScore(30, true, 0, false, false, 0)
	if !measured || s != 90 || len(basis) != 1 || basis[0] != "latency" {
		t.Fatalf("latency-only: got score=%d basis=%v measured=%v, want 90 [latency] true", s, basis, measured)
	}
	// throughput only: 100 Mbps → log10(1+1e8)/8 = 1.0 → 100
	s, _, _ = mbenchScore(0, false, 100_000_000, true, false, 0)
	if s != 100 {
		t.Fatalf("throughput 100Mbps: got %d, want 100", s)
	}
	// throughput only: 1 Mbps → log10(1+1e6)/8 ≈ 0.751 → 75
	s, _, _ = mbenchScore(0, false, 1_000_000, true, false, 0)
	if s != 75 {
		t.Fatalf("throughput 1Mbps: got %d, want 75", s)
	}
	// latency+throughput: 150 ms (0.5) + 10 Mbps (log10(1+1e7)/8 ≈ 0.8756)
	// → 0.6*0.5 + 0.4*0.8756 = 0.65024 → 65
	s, basis, _ = mbenchScore(150, true, 10_000_000, true, false, 0)
	if s != 65 || len(basis) != 2 {
		t.Fatalf("latency+throughput: got %d %v, want 65 [latency throughput]", s, basis)
	}
	// loss is multiplicative: 50% loss halves the composite
	sLat, _, _ := mbenchScore(30, true, 0, false, false, 0)
	sLoss, basisLoss, _ := mbenchScore(30, true, 0, false, true, 50)
	if sLoss != sLat/2 || len(basisLoss) != 2 {
		t.Fatalf("loss: got %d %v, want %d with the loss basis", sLoss, basisLoss, sLat/2)
	}
	// loss clamps: 150% reported → 0
	s, _, _ = mbenchScore(30, true, 0, false, true, 150)
	if s != 0 {
		t.Fatalf("loss clamp: got %d, want 0", s)
	}
	// nothing measured → measured=false (the caller renders score null)
	_, _, measured = mbenchScore(0, false, 0, false, false, 0)
	if measured {
		t.Fatalf("nothing measured: measured=true, want false")
	}
	// term edges: 0 ms → 1.0; ≥300 ms → 0; throughput 0 → 0; huge → 1.0
	if mbenchLatencyTerm(0) != 1 || mbenchLatencyTerm(300) != 0 || mbenchLatencyTerm(3000) != 0 {
		t.Fatalf("latency term edges: %v %v %v", mbenchLatencyTerm(0), mbenchLatencyTerm(300), mbenchLatencyTerm(3000))
	}
	if mbenchThroughputTerm(0) != 0 || mbenchThroughputTerm(1e12) != 1 {
		t.Fatalf("throughput term edges: %v %v", mbenchThroughputTerm(0), mbenchThroughputTerm(1e12))
	}
}

// ---- candidates (the deterministic method list) ----------------------------

func TestMethodBoardCandidates(t *testing.T) {
	// no catalogs: exactly the panel-known engines, uiKnownEngines order
	got := methodBoardCandidates(nil, nil)
	if len(got) != len(uiKnownEngines) {
		t.Fatalf("no catalogs: %d candidates, want %d", len(got), len(uiKnownEngines))
	}
	for i, e := range uiKnownEngines {
		if got[i] != e {
			t.Fatalf("order drift at %d: %q vs %q", i, got[i], e)
		}
	}
	// catalog-agreed extras land alphabetically after the known set; an
	// engine only ONE agent reports never becomes a candidate
	iran := []string{"matrix", "zengine", "solo"}
	kharej := []string{"matrix", "zengine"}
	got = methodBoardCandidates(iran, kharej)
	want := append(append([]string{}, uiKnownEngines...), "zengine")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("catalog extras: got %v, want %v", got, want)
	}
	// the manifest join (the BACKEND-43 seam): every method id of an agreed
	// core appends; duplicate ids dedup
	oldMethods, oldEntry := mboardManifestMethodsFn, mboardManifestEntryFn
	t.Cleanup(func() { mboardManifestMethodsFn, mboardManifestEntryFn = oldMethods, oldEntry })
	mboardManifestMethodsFn = func(core string) []methodManifestEntry {
		if core == "zengine" {
			return []methodManifestEntry{
				{ID: "zm-1", Engine: "zengine", Layer: "L3", Transport: "udp", IPVer: "both"},
				{ID: "zm-2", Engine: "zengine", Layer: "L2", Transport: "raw-ip", IPVer: "v4"},
				{ID: "matrix"}, // a duplicate id never re-probes an existing row
			}
		}
		return nil
	}
	mboardManifestEntryFn = func(method string) (methodManifestEntry, bool) {
		if method == "zm-1" {
			return methodManifestEntry{ID: "zm-1", Engine: "zengine", Layer: "L3", Transport: "udp", IPVer: "both"}, true
		}
		return methodManifestEntry{}, false
	}
	got = methodBoardCandidates(iran, kharej)
	if len(got) != len(want)+2 || got[len(got)-2] != "zm-1" || got[len(got)-1] != "zm-2" {
		t.Fatalf("manifest join: got %v, want %v + [zm-1 zm-2]", got, want)
	}
	// entry lookup: a manifest method resolves with the manifest's metadata,
	// a catalog engine does not
	if m, ok := mboardManifestEntryOf("zm-1"); !ok || m.Engine != "zengine" || m.Layer != "L3" {
		t.Fatalf("manifest entry lookup: %v %v", m, ok)
	}
	if _, ok := mboardManifestEntryOf("matrix"); ok {
		t.Fatalf("catalog engine misresolved as a manifest method")
	}
}

// ---- the probe engine (swapped seams, tiny timeouts) -----------------------

// mboardFixtureCtl drives the fake runtime the probe seams read.
type mboardFixtureCtl struct {
	mu           sync.Mutex
	verdicts     map[string]string
	latency      map[string]float64
	loss         map[string]float64
	autoSample   bool          // emit increasing counter samples per seam call
	sampleN      map[int64]int // per-tunnel sample counter
	arming       bool          // the arming goroutine acts
	armDelay     time.Duration // verdict arm delay after the tunnel appears
	latencyDelay time.Duration // latency arm delay (0 = never arm latency)
	lossPct      float64       // armed together with the verdict (0 = none)
}

func (c *mboardFixtureCtl) armVerdict(key, verdict string) {
	c.mu.Lock()
	c.verdicts[key] = verdict
	c.mu.Unlock()
}

func (c *mboardFixtureCtl) armLatency(key string, ms float64) {
	c.mu.Lock()
	c.latency[key] = ms
	c.mu.Unlock()
}

// mboardProbeFixture wires an in-memory store with one enabled topology
// over two linked servers + two agents, and swaps every probe seam for a
// hermetic one (restored in t.Cleanup). The arming goroutine watches the
// store's tunnel list: every NEW probe tunnel gets a healthy verdict and a
// latency sample after armDelay — tests that want converge-timeouts just
// set armDelay = 0 and never arm (ctl.arming = false).
func mboardProbeFixture(t *testing.T) (*mboardState, *mboardFixtureCtl) {
	t.Helper()
	withTempConfig(t)
	s := withMeshStore(t)
	iranSrv, err := s.UpsertServer(store.Server{Name: "ir-0", Role: "iran"})
	if err != nil {
		t.Fatal(err)
	}
	kharejSrv, err := s.UpsertServer(store.Server{Name: "fx-0", Role: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	topoID, err := s.UpsertTopology(store.Topology{Name: "probe-topo", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, iranSrv); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTopologyMember(topoID, kharejSrv); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgent(store.Agent{ID: "agt_111122223333", Name: "iran-a", ServerID: iranSrv}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgent(store.Agent{ID: "agt_444455556666", Name: "kharej-a", ServerID: kharejSrv}); err != nil {
		t.Fatal(err)
	}
	st := &mboardState{iran: "agt_111122223333", kharej: "agt_444455556666",
		methods: []string{"matrix", "ctunnel"}, store: s,
		iranSrv: iranSrv, kharejSrv: kharejSrv, topoID: topoID}

	oldV, oldL, oldS, oldLoss, oldPMTU := mprobeVerdictsFn, mprobeLatencyFn, mprobeSamplesFn, mprobeLossMapFn, mprobePMTUFn
	oldCT, oldCP, oldMD, oldMP := mprobeConvergeTimeout, mprobeConvergePoll, mprobeMeasureDeadline, mprobeMeasurePoll
	t.Cleanup(func() {
		mprobeVerdictsFn, mprobeLatencyFn, mprobeSamplesFn, mprobeLossMapFn, mprobePMTUFn = oldV, oldL, oldS, oldLoss, oldPMTU
		mprobeConvergeTimeout, mprobeConvergePoll, mprobeMeasureDeadline, mprobeMeasurePoll = oldCT, oldCP, oldMD, oldMP
	})
	mprobeConvergeTimeout = 2 * time.Second
	mprobeConvergePoll = 40 * time.Millisecond
	mprobeMeasureDeadline = 2 * time.Second
	mprobeMeasurePoll = 40 * time.Millisecond

	ctl := &mboardFixtureCtl{verdicts: map[string]string{}, latency: map[string]float64{},
		loss: map[string]float64{}, sampleN: map[int64]int{}}
	mprobeVerdictsFn = func() map[string]string {
		ctl.mu.Lock()
		defer ctl.mu.Unlock()
		out := make(map[string]string, len(ctl.verdicts))
		for k, v := range ctl.verdicts {
			out[k] = v
		}
		return out
	}
	mprobeLatencyFn = func() map[string]float64 {
		ctl.mu.Lock()
		defer ctl.mu.Unlock()
		out := make(map[string]float64, len(ctl.latency))
		for k, v := range ctl.latency {
			out[k] = v
		}
		return out
	}
	mprobeLossMapFn = func() map[string]float64 {
		ctl.mu.Lock()
		defer ctl.mu.Unlock()
		out := make(map[string]float64, len(ctl.loss))
		for k, v := range ctl.loss {
			out[k] = v
		}
		return out
	}
	mprobeSamplesFn = func(_ *store.Store, tunnelID int64) ([]store.TrafficSample, error) {
		ctl.mu.Lock()
		defer ctl.mu.Unlock()
		if !ctl.autoSample {
			return nil, nil
		}
		ctl.sampleN[tunnelID]++
		n := ctl.sampleN[tunnelID]
		return []store.TrafficSample{{TunnelID: tunnelID, At: time.Now().Unix(),
			WireRx: uint64(n) * 1_000_000, WireTx: uint64(n) * 500_000,
			CountersKnown: true, AgentID: "agt_111122223333"}}, nil
	}
	mprobePMTUFn = func(string) (int, bool) { return 0, false } // no capability — the honest default

	// the arming goroutine: arms every NEW probe tunnel (verdict healthy +
	// a latency sample) armDelay after it appears; driven by ctl.arming
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		armed := map[string]bool{}
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
			}
			ctl.mu.Lock()
			arming := ctl.arming
			ctl.mu.Unlock()
			if !arming {
				continue
			}
			tuns, err := s.ListTunnels(st.topoID)
			if err != nil {
				return
			}
			for _, tn := range tuns {
				if armed[tn.TunnelKey] || !strings.HasPrefix(tn.TunnelKey, "mprobe-") {
					continue
				}
				armed[tn.TunnelKey] = true
				key := tn.TunnelKey
				go func() {
					time.Sleep(ctl.armDelay)
					ctl.armVerdict(key, "healthy")
					ctl.mu.Lock()
					lossPct := ctl.lossPct
					ctl.mu.Unlock()
					if lossPct > 0 {
						ctl.mu.Lock()
						ctl.loss[key] = lossPct
						ctl.mu.Unlock()
					}
					ctl.mu.Lock()
					latencyDelay := ctl.latencyDelay
					ctl.mu.Unlock()
					if latencyDelay > 0 {
						time.Sleep(latencyDelay)
						ctl.armLatency(key, 21)
					}
				}()
			}
		}
	}()
	return st, ctl
}

func TestMboardProbeConvergeTimeoutIsARow(t *testing.T) {
	st, _ := mboardProbeFixture(t)
	// the arming goroutine never arms (arming=false default) → the converge
	// deadline hits → an honest timeout row with score 0
	st.probeOne("matrix")
	if len(st.rows) != 1 {
		t.Fatalf("rows: %d, want 1", len(st.rows))
	}
	r := st.rows[0]
	if r.Reachability != "converge-timeout" || r.Score.(int) != 0 || r.TunnelKey == "" || r.ConvergeS <= 0 {
		t.Fatalf("converge-timeout row: %+v", r)
	}
	if r.LatencyMS != nil || r.Throughput != nil || r.PMTU != nil {
		t.Fatalf("timeout row must not carry measurements: %+v", r)
	}
	if r.Note == "" {
		t.Fatalf("timeout row must say when the window closed")
	}
	// the temporary probe tunnel is GONE (teardown on every exit path)
	tuns, err := st.store.ListTunnels(st.topoID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tn := range tuns {
		if tn.TunnelKey == r.TunnelKey {
			t.Fatalf("probe tunnel %s was not torn down", r.TunnelKey)
		}
	}
}

func TestMboardProbeReachableButNothingMeasured(t *testing.T) {
	st, ctl := mboardProbeFixture(t)
	ctl.arming = true
	ctl.armDelay = 60 * time.Millisecond // converges...
	ctl.autoSample = false               // ...but no counters move...
	ctl.latencyDelay = 0                 // ...and no latency is ever reported
	st.probeOne("ctunnel")
	if len(st.rows) != 1 {
		t.Fatalf("rows: %d, want 1", len(st.rows))
	}
	r := st.rows[0]
	if r.Reachability != "ok" {
		t.Fatalf("reachability %q, want ok", r.Reachability)
	}
	if r.Score != nil || r.LatencyMS != nil || r.Throughput != nil || r.LatencyN != 0 {
		t.Fatalf("nothing-measured row must carry nulls: %+v", r)
	}
	if r.Note == "" {
		t.Fatalf("nothing-measured row must say why (no injected traffic)")
	}
	// a null-score row is never the Deploy Best winner
	rows := append([]methodBoardRow{}, st.rows...)
	markRecommendation(rows)
	for i := range rows {
		if rows[i].Recommended {
			t.Fatalf("null-score row recommended: %+v", rows[i])
		}
	}
}

func TestMboardProbeMeasuresLatencyMedianAndThroughput(t *testing.T) {
	st, ctl := mboardProbeFixture(t)
	ctl.arming = true
	ctl.armDelay = 30 * time.Millisecond
	ctl.latencyDelay = 20 * time.Millisecond
	ctl.autoSample = true // the counter seam emits rising wire counters
	st.probeOne("matrix")
	if len(st.rows) != 1 {
		t.Fatalf("rows: %d, want 1", len(st.rows))
	}
	r := st.rows[0]
	if r.Reachability != "ok" {
		t.Fatalf("reachability %q (%s)", r.Reachability, r.Note)
	}
	if r.LatencyMS == nil || r.LatencyN < 2 {
		t.Fatalf("latency samples missing: %+v", r)
	}
	if lat := r.LatencyMS.(float64); lat != 21 {
		t.Fatalf("median latency %v, want the armed 21", lat)
	}
	if r.Throughput == nil || r.Throughput.(float64) <= 0 {
		t.Fatalf("throughput from the counter delta missing: %+v", r)
	}
	if r.Score == nil || r.Score.(int) <= 0 {
		t.Fatalf("a measured row must score: %+v", r)
	}
	if len(r.ScoreBasis) == 0 {
		t.Fatalf("score basis empty: %+v", r)
	}
	// PMTU stays null (no agent capability — honest)
	if r.PMTU != nil {
		t.Fatalf("pmtu must stay null with no DF-probe capability: %+v", r)
	}
}

func TestMboardProbeLossAndPMTUSeams(t *testing.T) {
	st, ctl := mboardProbeFixture(t)
	ctl.arming = true
	ctl.armDelay = 30 * time.Millisecond
	ctl.latencyDelay = 20 * time.Millisecond
	ctl.autoSample = true
	ctl.lossPct = 2.5 // armed together with the verdict
	st.probeOne("matrix")
	r := st.rows[0]
	if r.LossPct == nil || r.LossPct.(float64) != 2.5 {
		t.Fatalf("loss seam: %+v", r)
	}
	// PMTU: the production seam is "no capability" — stays null
	if r.PMTU != nil {
		t.Fatalf("pmtu must stay null: %+v", r)
	}
}

func TestMboardProbeCreateFailureIsARow(t *testing.T) {
	st, _ := mboardProbeFixture(t)
	// a nonexistent topology makes the tunnel insert fail (FK) → failed row
	st.topoID = 999
	st.probeOne("matrix")
	if len(st.rows) != 1 || st.rows[0].Reachability != "failed" || st.rows[0].Error == "" {
		t.Fatalf("create-failure row: %+v", st.rows)
	}
	if st.rows[0].Score != nil {
		t.Fatalf("failed row must not score: %+v", st.rows[0])
	}
}

// ---- handler: hostile-input matrix + single-flight -------------------------

func mboardHandlerReq(t *testing.T, q string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/benchmark/method-board?"+q, nil)
	w := httptest.NewRecorder()
	handleMethodBoard(w, r)
	return w
}

func TestMethodBoardHandlerInputMatrix(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	// missing params → E-IN-02
	if w := mboardHandlerReq(t, ""); !strings.Contains(w.Body.String(), "E-IN-02") {
		t.Fatalf("missing params: %s", w.Body.String())
	}
	if w := mboardHandlerReq(t, "iran=agt_xxxxxxxxxxxx"); !strings.Contains(w.Body.String(), "E-IN-02") {
		t.Fatalf("missing kharej: %s", w.Body.String())
	}
	// same agent twice → E-IN-06
	if w := mboardHandlerReq(t, "iran=agt_xxxxxxxxxxxx&kharej=agt_xxxxxxxxxxxx"); !strings.Contains(w.Body.String(), "E-IN-06") {
		t.Fatalf("same agent: %s", w.Body.String())
	}
	// bad agent ids (hostile-input matrix) → E-IN-06
	for _, bad := range []string{"../../etc/passwd", "agt_short", "agt_" + strings.Repeat("g", 40), "<script>", "agt_!!!"} {
		if w := mboardHandlerReq(t, "iran="+bad+"&kharej=agt_999988887777"); !strings.Contains(w.Body.String(), "E-IN-06") {
			t.Fatalf("bad agent %q: %s", bad, w.Body.String())
		}
	}
}

type mboardSnap struct {
	Running            bool                 `json:"running"`
	Phase              string               `json:"phase"`
	Cached             bool                 `json:"cached"`
	Best               any                  `json:"best"`
	Error              string               `json:"error"`
	Rows               []methodBoardRow     `json:"rows"`
	PendingAgentFields []mboardPendingField `json:"pending_agent_fields"`
}

func mboardDecodeSnap(t *testing.T, w *httptest.ResponseRecorder) mboardSnap {
	t.Helper()
	var out mboardSnap
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode snapshot: %v — body %.200s", err, w.Body.String())
	}
	return out
}

func TestMethodBoardHandlerSnapshotAndSingleFlight(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	iranSrv, err := s.UpsertServer(store.Server{Name: "ir-1", Role: "iran"})
	if err != nil {
		t.Fatal(err)
	}
	kharejSrv, err := s.UpsertServer(store.Server{Name: "fx-1", Role: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	topoID, err := s.UpsertTopology(store.Topology{Name: "topo-1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = s.AddTopologyMember(topoID, iranSrv)
	_ = s.AddTopologyMember(topoID, kharejSrv)
	_ = s.UpsertAgent(store.Agent{ID: "agt_aaaabbbbcccc", Name: "iran-a", ServerID: iranSrv})
	_ = s.UpsertAgent(store.Agent{ID: "agt_dddd0000eeee", Name: "kharej-a", ServerID: kharejSrv})
	const q = "iran=agt_aaaabbbbcccc&kharej=agt_dddd0000eeee"

	// first GET without run: the cached empty board answers 200 with the
	// catalog-shaped rows (reachability unprobed) — the UI renders the
	// method catalog immediately
	w := mboardHandlerReq(t, q)
	if w.Code != 200 {
		t.Fatalf("cached GET: %d %s", w.Code, w.Body.String())
	}
	snap := mboardDecodeSnap(t, w)
	if len(snap.Rows) != len(uiKnownEngines) || snap.Best != nil || snap.Running {
		t.Fatalf("cached snapshot: running=%v rows=%d best=%v", snap.Running, len(snap.Rows), snap.Best)
	}
	if snap.Rows[0].Reachability != "unprobed" {
		t.Fatalf("first snapshot rows must be unprobed: %+v", snap.Rows[0])
	}

	// hermetic probe timing (restored): converge fails fast for every engine
	oldCT, oldCP, oldMD, oldMP := mprobeConvergeTimeout, mprobeConvergePoll, mprobeMeasureDeadline, mprobeMeasurePoll
	mprobeConvergeTimeout, mprobeConvergePoll = 120*time.Millisecond, 20*time.Millisecond
	mprobeMeasureDeadline, mprobeMeasurePoll = 60*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() {
		mprobeConvergeTimeout, mprobeConvergePoll, mprobeMeasureDeadline, mprobeMeasurePoll = oldCT, oldCP, oldMD, oldMP
	})

	// run=1 starts the background probe; the snapshot answers immediately
	w = mboardHandlerReq(t, q+"&run=1")
	if w.Code != 200 {
		t.Fatalf("run GET: %d %s", w.Code, w.Body.String())
	}
	snap = mboardDecodeSnap(t, w)
	if !snap.Cached || (snap.Phase != "probing" && snap.Phase != "done") {
		t.Fatalf("run snapshot: %+v", snap)
	}
	// single-flight: an immediate second run=1 must not restart the run
	w = mboardHandlerReq(t, q+"&run=1")
	snap2 := mboardDecodeSnap(t, w)
	if snap.Phase == "probing" && !snap2.Running && snap2.Phase != "done" {
		t.Fatalf("single-flight violated: %+v → %+v", snap.Phase, snap2)
	}
	// wait for the run to finish (bounded) and check the honest timeout rows
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		snap = mboardDecodeSnap(t, mboardHandlerReq(t, q))
		if snap.Phase == "done" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if snap.Phase != "done" {
		t.Fatalf("run never finished: %+v", snap)
	}
	if snap.Error != "" {
		t.Fatalf("resolved endpoints must not error: %q", snap.Error)
	}
	if len(snap.Rows) != len(uiKnownEngines) {
		t.Fatalf("rows %d, want %d (no catalogs)", len(snap.Rows), len(uiKnownEngines))
	}
	for _, r := range snap.Rows {
		if r.Reachability != "converge-timeout" && r.Reachability != "ok" && r.Reachability != "unprobed" {
			t.Fatalf("row reachability %q: %+v", r.Reachability, r)
		}
		// the snapshot decodes through JSON, so every number is a float64 —
		// the score is read through the shape-tolerant helper
		if sc, ok := mboardScoreOf(r); ok && r.Reachability == "converge-timeout" && sc != 0 {
			t.Fatalf("timeout row must score 0: %+v", r)
		}
	}
	// the honest pending-field declaration rides every snapshot
	if len(snap.PendingAgentFields) == 0 || snap.PendingAgentFields[0].Field != "pmtu" || snap.PendingAgentFields[0].Flag != "AGENT-31" {
		t.Fatalf("pending_agent_fields: %+v", snap.PendingAgentFields)
	}
}

// ---- persistence: the restart note ---------------------------------------

func TestMboardRestoreUnfinishedBoard(t *testing.T) {
	withTempConfig(t)
	blob := `[{"iran":"agt_001122334455","kharej":"agt_66778899aabb","iran_name":"iran-a","kharej_name":"kharej-a",
		"rows":[{"method":"matrix","engine":"matrix","reachability":"ok","latency_ms":25,"latency_samples":3,"score":92,"score_basis":["latency"]}],
		"started":` + strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10) + `,"finished":0,"error":""}]`
	path := filepath.Join(configDir, "benchmark-method-board.json")
	if err := os.WriteFile(path, []byte(blob), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreMBoards()
	mboardMu.Lock()
	st := mboardBy[mboardKey("agt_001122334455", "agt_66778899aabb")]
	mboardMu.Unlock()
	if st == nil {
		t.Fatalf("board not restored")
	}
	st.mu.Lock()
	rows, boardErr := st.rows, st.boardErr
	st.mu.Unlock()
	if len(rows) != 1 || rows[0].Method != "matrix" {
		t.Fatalf("restored rows: %+v", rows)
	}
	if boardErr == "" {
		t.Fatalf("an unfinished board must carry the restart note")
	}
	// the recommendation is recomputed from the restored rows (the state
	// file round-trips numbers as float64 — the Deploy Best pass survives)
	snap := mboardSnapshot(st)
	got := snap["rows"].([]methodBoardRow)
	if snap["best"] != "matrix" {
		t.Fatalf("restored best: %v", snap["best"])
	}
	if len(got) != 1 {
		t.Fatalf("restored rows snapshot: %+v", got)
	}
	if sc, ok := mboardScoreOf(got[0]); !ok || sc != 92 {
		t.Fatalf("restored score: %+v (%v %v)", got[0], got[0].Score, ok)
	}
	// a corrupt state file never panics the restore
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	restoreMBoards()
}
