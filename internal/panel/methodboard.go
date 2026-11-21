package panel

// methodboard.go — BACKEND-42 (UI-API-CONTRACT §2.23.1): the method-probe
// board. For one iran↔kharej agent pair the panel probes EVERY available
// tunnel method with a REAL temporary tunnel per method — the pair
// benchmark's machinery (temporary tunnel → converge-wait → measurement →
// teardown, benchmark_pair.go) reused per method — and scores each method
// with a transparent, documented formula so the operator gets a "Deploy
// Best" recommendation. One GET route, cached board + single-flight
// background refresh with run=1 (the pair-benchmark pattern).
//
// OUR OWN implementation: C-Tunnel is AGPL-3.0 and ZERO of its code is
// copied into Quantum (the architect's 2026-11-22 adoption ruling). The
// board is built from the panel's own pair-benchmark machinery; the
// per-method layer/transport/ipver metadata lives in methodCatalog below,
// grounded in OUR drivers' documented behavior (quantum-agent driver
// headers + the panel's own enginetemplates.go param tables). Engines the
// panel has no grounded metadata for render "" — the field is always
// PRESENT, the value honest.
//
// The ctunnel methods[] join (BACKEND-43): once the cores MANIFEST
// publishes the ctunnel methods[] block (AGENT-29/30 land it), each
// manifest method id becomes an EXTRA board row under engine "ctunnel"
// with the manifest's own layer/transport/ipver — mboardManifestMethodsFn
// is the seam the cores-manifest cache (BACKEND-43) wires. The probe
// passes the method in EngineParams.method, which the AGENT-30 driver
// consumes; until that driver lands the manifest has no methods[], so no
// such rows exist and nothing can be probed through a wrong identity.
//
// HONESTY RULES carried over from the pair benchmark:
//   - converge-timeout is a real row (the agents did not bring the tunnel
//     up in the window) — never a fake score;
//   - the panel never injects user traffic, so throughput is the wire
//     counters' own delta over the measure window; when the counters did
//     not move the row says so (throughput null) instead of inventing;
//   - PMTU discovery needs an agent-side DF-probe capability the current
//     agents do not carry (verified quantum-agent 2f240d2: no remote
//     DF-probe op in protocol.ValidCommands) — the field stays present and
//     null, declared in pendingAgentFields, flagged AGENT-31. Nothing
//     invented.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// ---- the method catalog (panel-side metadata) -----------------------------

// methodBoardMeta describes one method/engine for the board's fixed
// columns. Values are the panel's own grounded knowledge (driver headers +
// enginetemplates.go):
//   - matrix  = the Matrix/tinc mesh → L3 over tcp+udp, IPv4 mesh addresses;
//   - wireguard = kernel WireGuard (wg_driver.go: qwg<id> iface, MTU 1420)
//     → L3 over UDP, v4+v6;
//   - ctunnel = the C-Tunnel exec-a-binary driver (ctunnel_driver.go:
//     L3 tunnel appliance, proto tcp|udp|both);
//   - frp/backhaul/gost/rathole/chisel/xray/wst = userspace L4 forwarders
//     (each driver header names its carrier: frp plain-TCP mux, backhaul
//     tcp/tcpmux/ws/wsmux, gost mux, rathole noise TCP, chisel SSH, xray
//     VLESS+Reality, wstunnel WS carrier forwarding tcp+udp);
//   - bagher = the panel's own SSH-pool carrier (the Bagher topology's
//     engine) → L4 over ssh;
//   - aestun = TUN-based UDP-carried tunnel (enginetemplates.go: tun_addr,
//     udp_port, quic obfs) → L3 over udp;
//   - rakhsh = carrier-forwarding tunnel (enginetemplates.go: carrier
//     tcp/tls/ws/quic/arq-udp, lanes) → L4;
//   - udp2raw = the raw-socket UDP disguise core → L4 over raw.
var methodCatalog = map[string]methodBoardMeta{
	"matrix":    {Layer: "l3", Transport: "tcp/udp", IPVer: "v4"},
	"wireguard": {Layer: "l3", Transport: "udp", IPVer: "v4+v6"},
	"ctunnel":   {Layer: "l3", Transport: "tcp/udp", IPVer: "v4"},
	"frp":       {Layer: "l4", Transport: "tcp", IPVer: "v4"},
	"backhaul":  {Layer: "l4", Transport: "tcp", IPVer: "v4"},
	"gost":      {Layer: "l4", Transport: "tcp", IPVer: "v4"},
	"rathole":   {Layer: "l4", Transport: "tcp", IPVer: "v4"},
	"chisel":    {Layer: "l4", Transport: "tcp", IPVer: "v4"},
	"xray":      {Layer: "l4", Transport: "tcp", IPVer: "v4"},
	"wst":       {Layer: "l4", Transport: "ws", IPVer: "v4"},
	"bagher":    {Layer: "l4", Transport: "ssh", IPVer: "v4"},
	"aestun":    {Layer: "l3", Transport: "udp", IPVer: "v4"},
	"rakhsh":    {Layer: "l4", Transport: "tcp/tls/ws/quic", IPVer: "v4"},
	"udp2raw":   {Layer: "l4", Transport: "raw", IPVer: "v4"},
}

type methodBoardMeta struct{ Layer, Transport, IPVer string }

// methodManifestEntry is one manifest method the board may probe (the
// BACKEND-43 cores-manifest passthrough fills these). Layer/Transport/IPVer
// ride the manifest's own vocabulary; the board row lowercases layer to the
// panel's column style and keeps transport/ipver verbatim.
type methodManifestEntry struct {
	ID        string // the manifest method id (verbatim)
	Engine    string // the core the method belongs to ("ctunnel")
	Layer     string
	Transport string
	IPVer     string
}

// mboardManifestMethodsFn is the seam the BACKEND-43 manifest cache wires
// (production = the cached methods[] of the named core, empty when the
// manifest does not publish methods[]). Tests swap it; the zero default
// keeps BACKEND-42 behavior exactly (no manifest rows yet).
var mboardManifestMethodsFn = func(core string) []methodManifestEntry { return nil }

// mboardManifestEntryFn is the reverse lookup (method id → manifest entry)
// over EVERY manifest core — not just the uiKnownEngines names: the cores
// repo names differ from engine ids (wstunnel vs wst), so the join cannot
// guess the core from the method id. The BACKEND-43 cache wires it.
var mboardManifestEntryFn = func(method string) (methodManifestEntry, bool) {
	return methodManifestEntry{}, false
}

// methodBoardRow is one board row (§2.23.1). The fixed identity columns
// ride from the catalog (or the manifest); the measurements are
// honest-absence: null means "not measured", never 0.
type methodBoardRow struct {
	Method       string   `json:"method"`
	Engine       string   `json:"engine"`
	Layer        string   `json:"layer"`
	Transport    string   `json:"transport"`
	IPVer        string   `json:"ipver"`
	Reachability string   `json:"reachability"` // ok | converge-timeout | stopped | failed | unprobed
	LatencyMS    any      `json:"latency_ms"`   // median of the samples; null = none
	LatencyN     int      `json:"latency_samples"`
	Throughput   any      `json:"throughput"` // wire bps over the measure window; null = counters did not move
	PMTU         any      `json:"pmtu"`       // null until the agent-side DF-probe capability lands (AGENT-31)
	LossPct      any      `json:"loss_pct"`   // engine-reported loss when the engine measures it
	Score        any      `json:"score"`      // 0..100; 0 = blocked; null = reachable but nothing measured
	ScoreBasis   []string `json:"score_basis"`
	Recommended  bool     `json:"recommended"`
	ConvergeS    float64  `json:"converge_s"`
	Note         string   `json:"note,omitempty"`
	TunnelKey    string   `json:"tunnel_key,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// mboardPendingField declares one board capability the CURRENT agent
// surface cannot measure (§2.23.1 pending_agent_fields) — declared, never
// invented; the agent-side work is flagged AGENT-31 (the agent-fields
// wave; AGENT-30 is the ctunnel driver upgrade).
type mboardPendingField struct {
	Field string `json:"field"`
	Flag  string `json:"flag"`
	Note  string `json:"note"`
}

var mboardPendingAgentFields = []mboardPendingField{
	{Field: "pmtu", Flag: "AGENT-31", Note: "DF-probe size search needs an agent-side capability the current agents do not carry — declared null, never invented"},
}

// ---- scoring (transparent; §2.23.1 documents exactly this) ----------------
//
//	score = round(100 × loss_term × weighted mean of the MEASURED terms)
//	  latency_term    = clamp(1 − latency_ms/300, 0, 1)   (300 ms = the same RTT floor calculateScore uses)
//	  throughput_term = clamp(log10(1 + bps)/8, 0, 1)     (8 = log10(1 + 100 Mbps): 100 Mbps → 1.0, 1 Mbps → 0.75)
//	  loss_term       = 1 − clamp(loss_pct, 0, 100)/100   (unknown loss = neutral 1.0)
//	weights: latency 0.6 / throughput 0.4; UNMEASURED components drop out
//	and the remaining weights renormalize (score_basis records what counted).
//	Blocked rows (converge-timeout/stopped/failed) score 0. Reachable rows
//	with NOTHING measured score null and are excluded from Deploy Best.

const (
	mbenchLatWeight     = 0.6
	mbenchThroughWeight = 0.4
	mbenchLatCapMs      = 300.0
	mbenchTPLogDenom    = 8.0 // log10(1 + 100 Mbps)
)

func mbenchLatencyTerm(latencyMS float64) float64 {
	t := 1 - latencyMS/mbenchLatCapMs
	if t < 0 {
		return 0
	}
	if t > 1 {
		return 1
	}
	return t
}

func mbenchThroughputTerm(bps float64) float64 {
	if bps <= 0 {
		return 0
	}
	t := math.Log10(1+bps) / mbenchTPLogDenom
	if t > 1 {
		return 1
	}
	return t
}

// mbenchScore computes the composite for one measured row. latencyOK /
// throughputOK / lossOK say which components arrived. Returns the 0..100
// score, the basis list and measured=false when NOTHING was measured (the
// caller renders score null and excludes the row from Deploy Best).
func mbenchScore(latencyMS float64, latencyOK bool, throughputBPS float64, throughputOK bool, lossOK bool, lossPct float64) (int, []string, bool) {
	basis := []string{}
	weightSum := 0.0
	acc := 0.0
	if latencyOK {
		acc += mbenchLatWeight * mbenchLatencyTerm(latencyMS)
		weightSum += mbenchLatWeight
		basis = append(basis, "latency")
	}
	if throughputOK {
		acc += mbenchThroughWeight * mbenchThroughputTerm(throughputBPS)
		weightSum += mbenchThroughWeight
		basis = append(basis, "throughput")
	}
	lossTerm := 1.0
	if lossOK {
		lossPct = math.Max(0, math.Min(100, lossPct))
		lossTerm = 1 - lossPct/100
		basis = append(basis, "loss")
	}
	if weightSum == 0 {
		return 0, basis, false
	}
	raw := 100 * lossTerm * acc / weightSum
	return int(math.Round(raw)), basis, true
}

// ---- engine state + single-flight ----------------------------------------

// mboardState is one pair's board: the in-flight run OR the last finished
// one. Like the pair benchmark, the run lives in memory while a panel
// restart kills the goroutine — the rows persist to a state file on every
// progress step and restore on the next read.
type mboardState struct {
	mu         sync.Mutex
	iran       string
	kharej     string
	iranName   string
	kharejName string
	methods    []string // the candidate method list the run probes, in order
	running    bool
	stop       chan struct{}
	stopOnce   sync.Once
	store      *store.Store
	rows       []methodBoardRow
	startedAt  time.Time
	finishedAt time.Time
	boardErr   string
	cur        string
	// endpoint resolution, written once by run() before probing
	iranSrv   int64
	kharejSrv int64
	topoID    int64
}

var (
	mboardMu sync.Mutex
	mboardBy = map[string]*mboardState{}
)

func mboardKey(iran, kharej string) string { return iran + "\x00" + kharej }

func methodBoardFile() string {
	return filepath.Join(configDir, "benchmark-method-board.json")
}

// saveMBoardLocked persists every known board snapshot; called with
// mboardMu held (the run loop and the handler both already hold it — the
// pair benchmark's savePairStateLocked unlock/relock dance exists because
// IT was called from paths that held the lock differently).
func saveMBoardLocked() {
	if len(mboardBy) == 0 {
		return
	}
	type savedBoard struct {
		Iran       string           `json:"iran"`
		Kharej     string           `json:"kharej"`
		IranName   string           `json:"iran_name"`
		KharejName string           `json:"kharej_name"`
		Rows       []methodBoardRow `json:"rows"`
		Started    int64            `json:"started"`
		Finished   int64            `json:"finished"`
		Error      string           `json:"error"`
	}
	out := []savedBoard{}
	for _, st := range mboardBy {
		st.mu.Lock()
		b := savedBoard{Iran: st.iran, Kharej: st.kharej, IranName: st.iranName,
			KharejName: st.kharejName, Started: st.startedAt.Unix(),
			Finished: st.finishedAt.Unix(), Error: st.boardErr}
		if !st.running { // a running board's partial rows persist through pushRow
			b.Rows = st.rows
		}
		st.mu.Unlock()
		out = append(out, b)
	}
	blob, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return
	}
	tmp := methodBoardFile() + ".tmp"
	if err := os.WriteFile(tmp, blob, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, methodBoardFile())
}

// mboardRestored guards the one-time restore (the pairRestored pattern).
var mboardRestored sync.Once

// restoreMBoards reloads the last persisted boards after a restart. Rows of
// a board that never finished keep their partial rows; the board carries an
// honest restart note (the pair benchmark's "the panel restarted" row, as a
// board-level error since the in-flight method left no row).
func restoreMBoards() {
	blob, err := os.ReadFile(methodBoardFile())
	if err != nil {
		return
	}
	var saved []struct {
		Iran       string           `json:"iran"`
		Kharej     string           `json:"kharej"`
		IranName   string           `json:"iran_name"`
		KharejName string           `json:"kharej_name"`
		Rows       []methodBoardRow `json:"rows"`
		Started    int64            `json:"started"`
		Finished   int64            `json:"finished"`
		Error      string           `json:"error"`
	}
	if err := json.Unmarshal(blob, &saved); err != nil {
		return
	}
	for _, b := range saved {
		if b.Iran == "" || b.Kharej == "" || len(b.Rows) == 0 {
			continue
		}
		st := &mboardState{
			iran: b.Iran, kharej: b.Kharej, iranName: b.IranName, kharejName: b.KharejName,
			rows: b.Rows, startedAt: time.Unix(b.Started, 0),
			finishedAt: time.Unix(b.Finished, 0), boardErr: b.Error,
		}
		if b.Finished == 0 {
			// the goroutine died with the panel mid-run: the partial rows stay,
			// the board says so, run=1 starts a fresh pass
			st.boardErr = "the panel restarted while this board was running — re-run for a fresh pass"
		}
		mboardBy[mboardKey(b.Iran, b.Kharej)] = st
	}
}

// mboardScoreOf reads a row's score across both number shapes the row can
// carry: int from the live scorer, float64 from a JSON-restored board (the
// state file round-trips through encoding/json, which makes every number a
// float64 — the Deploy Best pass must survive the restart).
func mboardScoreOf(row methodBoardRow) (int, bool) {
	switch sc := row.Score.(type) {
	case int:
		return sc, true
	case float64:
		return int(math.Round(sc)), true
	}
	return 0, false
}

// markRecommendation flags the Deploy Best winner on the finished slice
// (highest score; ties → lower latency, then method name — deterministic).
func markRecommendation(rows []methodBoardRow) {
	best := -1
	var bestLat float64 = math.Inf(1)
	bestName := ""
	for i := range rows {
		rows[i].Recommended = false
		sc, ok := mboardScoreOf(rows[i])
		if !ok || sc <= 0 {
			continue
		}
		lat := math.Inf(1)
		if l, ok := rows[i].LatencyMS.(float64); ok {
			lat = l
		}
		better := false
		switch {
		case sc > best:
			better = true
		case sc == best:
			better = lat < bestLat || (lat == bestLat && rows[i].Method < bestName)
		}
		if better {
			best, bestLat, bestName = sc, lat, rows[i].Method
		}
	}
	if bestName != "" {
		for i := range rows {
			if rows[i].Method == bestName {
				rows[i].Recommended = true
			}
		}
	}
}

func mboardSnapshot(st *mboardState) map[string]any {
	st.mu.Lock()
	defer st.mu.Unlock()
	rows := st.rows
	if rows == nil {
		if st.startedAt.IsZero() && !st.running {
			// a board never probed renders the full catalog as honest
			// unprobed rows — the UI sees the method surface immediately
			rows = unprobedRows(st.methods)
		} else {
			rows = []methodBoardRow{} // a running board's rows-so-far
		}
	}
	// Deploy Best is recomputed on EVERY snapshot: a restored board, a
	// mid-run board and a finished board all render a consistent winner
	// without persisting the flag.
	markRecommendation(rows)
	best := ""
	for i := range rows {
		if rows[i].Recommended {
			best = rows[i].Method
			break
		}
	}
	phase := "idle"
	if st.running {
		phase = "probing"
	} else if !st.finishedAt.IsZero() {
		phase = "done"
	}
	out := map[string]any{
		"iran":                 st.iran,
		"kharej":               st.kharej,
		"iran_name":            st.iranName,
		"kharej_name":          st.kharejName,
		"running":              st.running,
		"cached":               !st.startedAt.IsZero(),
		"phase":                phase,
		"cur_method":           st.cur,
		"rows":                 rows,
		"best":                 bestAny(best),
		"pending_agent_fields": mboardPendingAgentFields,
		"error":                st.boardErr,
	}
	if !st.startedAt.IsZero() {
		out["started_at"] = st.startedAt.Unix()
	}
	if !st.finishedAt.IsZero() {
		out["finished_at"] = st.finishedAt.Unix()
	}
	return out
}

// bestAny renders the empty recommendation as null (never "").
func bestAny(best string) any {
	if best == "" {
		return nil
	}
	return best
}

// ---- available methods for a pair -----------------------------------------

// methodBoardCandidates returns the deterministic method list for a pair:
// the panel-known engines (uiKnownEngines order) plus the engines BOTH
// agents' reported catalogs agree on (alphabetical extras), plus the
// manifest methods of the catalog-agreed cores (the BACKEND-43 join; empty
// until a manifest publishes methods[]). Catalog-engine rows keep
// method == engine; manifest-method rows carry their own method id.
func methodBoardCandidates(iranCatalog, kharejCatalog []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, e := range uiKnownEngines {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	inKharej := map[string]bool{}
	for _, e := range kharejCatalog {
		inKharej[e] = true
	}
	both := []string{}
	for _, e := range iranCatalog {
		if e == "" || seen[e] || !inKharej[e] {
			continue
		}
		seen[e] = true
		both = append(both, e)
	}
	sort.Strings(both)
	out = append(out, both...)
	// the manifest-method join: for each catalog-agreed CORE whose manifest
	// publishes methods[], every method id becomes a board row (engine =
	// core, method = the manifest's own id — verbatim, no reshaping)
	for _, core := range both {
		for _, m := range mboardManifestMethodsFn(core) {
			if m.ID == "" || seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			out = append(out, m.ID)
		}
	}
	return out
}

// mboardManifestEntryOf looks a candidate method up in the manifest join —
// manifest methods answer the manifest's own metadata; the bool says
// whether the method IS a manifest method.
func mboardManifestEntryOf(method string) (methodManifestEntry, bool) {
	return mboardManifestEntryFn(method)
}

// agentEngineCatalogList parses the agent's stored v2 engine catalog (the
// shape recordEngineCatalog persists: [{id, version?, params_schema?}]).
func agentEngineCatalogList(s *store.Store, agentID string) []string {
	v2, err := s.AgentV2(agentID)
	if err != nil || strings.TrimSpace(v2.EngineCatalog) == "" {
		return nil
	}
	var catalog []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(v2.EngineCatalog), &catalog); err != nil {
		return nil
	}
	out := []string{}
	for _, c := range catalog {
		if c.ID != "" {
			out = append(out, c.ID)
		}
	}
	return out
}

// ---- probe seams (hermetic tests swap these; production wires the real
// machinery exactly like the pair benchmark reads it) -----------------------

var (
	mprobeVerdictsFn = uiVerdictsFn
	mprobeLatencyFn  = uiLatencyMap
	// mprobeSamplesFn isolates the counter-sample read (the pair benchmark's
	// measure()'s RecentSamples path); it takes the run's own store so a
	// hermetic test's injected store is the one read.
	mprobeSamplesFn = func(s *store.Store, tunnelID int64) ([]store.TrafficSample, error) {
		return uiStoreHandle{s}.RecentSamples(tunnelID, 1)
	}
	// mprobePMTUFn is the DF-probe seam — production stays "no capability"
	// until AGENT-31 lands an agent-side DF-probe op (the field stays null,
	// declared in mboardPendingAgentFields; never a guessed value).
	mprobePMTUFn = func(tunnelKey string) (int, bool) { return 0, false }
	// mprobeLossMapFn: one locked pass over the runtime for the tunnels
	// whose engine MEASURES packet loss (TunnelRuntime.LossPct is omitted
	// when the engine does not measure it; measured-zero is
	// indistinguishable from unreported and stays neutral in the score).
	mprobeLossMapFn = func() map[string]float64 { return mboardRuntimeLoss() }

	mprobeConvergeTimeout = 90 * time.Second
	mprobeConvergePoll    = 3 * time.Second
	mprobeMeasureDeadline = 75 * time.Second
	mprobeMeasurePoll     = 2 * time.Second
	mprobeLatencyCap      = 5 // median of up to N samples
)

// ---- the probe run ---------------------------------------------------------

func (p *mboardState) stoppedNow() bool {
	select {
	case <-p.stop:
		return true
	default:
		return false
	}
}

func (p *mboardState) sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-p.stop:
		return false
	case <-t.C:
		return true
	}
}

func (p *mboardState) setPhase(cur string) {
	p.mu.Lock()
	p.cur = cur
	p.mu.Unlock()
}

// pushRow appends a finished row and persists (mboardMu taken here — the
// save walks the whole map).
func (p *mboardState) pushRow(r methodBoardRow) {
	p.mu.Lock()
	p.rows = append(p.rows, r)
	p.mu.Unlock()
	mboardMu.Lock()
	saveMBoardLocked()
	mboardMu.Unlock()
}

func (p *mboardState) finish() {
	p.mu.Lock()
	p.running = false
	p.finishedAt = time.Now()
	p.mu.Unlock()
	mboardMu.Lock()
	saveMBoardLocked()
	mboardMu.Unlock()
}

// probeOne probes ONE method with a temporary tunnel — the pair-benchmark
// sequence (create → converge-wait → measure → teardown) with honest
// timeout rows and cleanup on every exit path.
func (p *mboardState) probeOne(method string) {
	row := methodBoardRow{Method: method}
	if m, ok := mboardManifestEntryOf(method); ok {
		row.Engine = m.Engine
		row.Layer = strings.ToLower(strings.TrimSpace(m.Layer))
		row.Transport = m.Transport
		row.IPVer = m.IPVer
	} else {
		row.Engine = method
		if meta, ok := methodCatalog[method]; ok {
			row.Layer, row.Transport, row.IPVer = meta.Layer, meta.Transport, meta.IPVer
		}
	}
	if p.stoppedNow() {
		row.Reachability = "stopped"
		p.pushRow(row)
		return
	}
	engStart := time.Now()
	key := "mprobe-" + method + "-" + randHex(4)
	row.TunnelKey = key
	p.mu.Lock()
	iranSrv, kharejSrv, topoID := p.iranSrv, p.kharejSrv, p.topoID
	p.mu.Unlock()
	params := "{}"
	if _, isManifest := mboardManifestEntryOf(method); isManifest {
		// the AGENT-30 driver consumes method from the engine params; a
		// driver that predates it probes its default — but a manifest method
		// row only exists once the manifest publishes methods[], which only
		// happens with the v2 core, so the sequencing keeps the identity
		// honest
		params = `{"method":` + strconv.Quote(method) + `}`
	}
	id, err := p.store.UpsertTunnel(store.Tunnel{
		TunnelKey: key, TopologyID: topoID,
		LocalServerID: iranSrv, RemoteServerID: kharejSrv,
		Engine: row.Engine, EngineParams: params, Enabled: true,
	})
	if err != nil {
		row.Reachability = "failed"
		row.Error = "the temporary probe tunnel could not be created"
		p.pushRow(row)
		return
	}
	cleaned := false
	cleanup := func() {
		if cleaned {
			return
		}
		cleaned = true
		_ = p.store.DeleteTunnel(id)
	}
	defer cleanup()

	// converge: the agents build the temp tunnel from their desired state —
	// a converge-timeout is a real row (the pair-benchmark's honesty rule)
	deadline := time.Now().Add(mprobeConvergeTimeout)
	converged := false
	for time.Now().Before(deadline) {
		if p.stoppedNow() {
			row.Reachability = "stopped"
			p.pushRow(row)
			return
		}
		if v, ok := mprobeVerdictsFn()[key]; ok && (v == "healthy" || v == "degraded") {
			converged = true
			break
		}
		if !p.sleep(mprobeConvergePoll) {
			row.Reachability = "stopped"
			p.pushRow(row)
			return
		}
	}
	row.ConvergeS = float64(int(time.Since(engStart).Seconds()*10+0.5)) / 10
	if !converged {
		row.Reachability = "converge-timeout"
		row.Score = 0
		row.Note = "the agents did not bring the probe tunnel up in " + fmt.Sprintf("%.0f", mprobeConvergeTimeout.Seconds()) + "s"
		p.pushRow(row)
		return
	}

	// measure: latency samples (median of up to N) + the wire counters' own
	// delta over one report window. The panel never injects traffic: without
	// user traffic the throughput stays null and the row says so.
	latSamples := []float64{}
	var s0 store.TrafficSample
	gotFirst := false
	throughputOK := false
	var throughputBPS float64
	window := time.Now().Add(mprobeMeasureDeadline)
	for time.Now().Before(window) {
		if p.stoppedNow() {
			row.Reachability = "stopped"
			p.pushRow(row)
			return
		}
		lat := mprobeLatencyFn()[key]
		if lat > 0 && len(latSamples) < mprobeLatencyCap {
			latSamples = append(latSamples, lat)
		}
		if samples, err := mprobeSamplesFn(p.store, id); err == nil && len(samples) > 0 {
			if !gotFirst {
				// counters from before the tunnel converged say nothing about
				// the probe window — start from a fresh sample
				if samples[0].At >= engStart.Unix() {
					s0 = samples[0]
					gotFirst = true
				}
			} else if samples[0].At > s0.At {
				if s0.CountersKnown && samples[0].CountersKnown && samples[0].AgentID == s0.AgentID &&
					samples[0].WireRx >= s0.WireRx && samples[0].WireTx >= s0.WireTx {
					dt := samples[0].At - s0.At
					if dt > 0 {
						throughputBPS = float64(samples[0].WireRx-s0.WireRx+samples[0].WireTx-s0.WireTx) / float64(dt)
						throughputOK = true
					}
				}
				break // one report window decided the throughput leg
			}
		}
		if len(latSamples) >= mprobeLatencyCap && throughputOK {
			break
		}
		if !p.sleep(mprobeMeasurePoll) {
			row.Reachability = "stopped"
			p.pushRow(row)
			return
		}
	}
	row.Reachability = "ok"
	row.LatencyN = len(latSamples)
	if len(latSamples) > 0 {
		sort.Float64s(latSamples)
		mid := len(latSamples) / 2
		med := latSamples[mid]
		if len(latSamples)%2 == 0 {
			med = (latSamples[mid-1] + latSamples[mid]) / 2
		}
		row.LatencyMS = math.Round(med*10) / 10
	}
	if throughputOK {
		row.Throughput = math.Round(throughputBPS)
	} else {
		row.Note = "no user traffic moved through the probe tunnel in the measure window — the panel does not inject traffic"
	}
	if loss, ok := mprobeLossMapFn()[key]; ok && loss > 0 {
		row.LossPct = math.Round(loss*10) / 10
	}
	if size, ok := mprobePMTUFn(key); ok && size > 0 {
		row.PMTU = size
	}
	// score (the transparent composite; the measured sets decide the basis)
	score, basis, measured := mbenchScore(
		floatAny(row.LatencyMS), row.LatencyMS != nil,
		throughputBPS, throughputOK,
		row.LossPct != nil, floatAny(row.LossPct))
	if !measured {
		row.Score = nil // reachable but nothing measurable — excluded from Deploy Best
	} else {
		row.Score = score
		row.ScoreBasis = basis
	}
	p.pushRow(row)
}

func floatAny(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

// mboardRuntimeLoss is the production loss source (one locked pass over the
// fresh runtime view).
func mboardRuntimeLoss() map[string]float64 {
	out := map[string]float64{}
	now := time.Now().Unix()
	meshRuntimeMu.Lock()
	for agentID, rts := range meshRuntimeBy {
		if now-meshRuntimeSeen[agentID] > 90 {
			continue
		}
		for _, tr := range rts {
			if tr.LossPct > 0 {
				out[tr.TunnelID] = tr.LossPct
			}
		}
	}
	meshRuntimeMu.Unlock()
	return out
}

func (p *mboardState) run() {
	// resolve the endpoints once; a failure marks the whole board honestly
	// (rows stay unprobed) instead of erroring the GET
	s := p.store
	agIran, err := s.GetAgent(p.iran)
	var iranSrv int64
	if err == nil {
		iranSrv = agIran.ServerID
		p.mu.Lock()
		p.iranName = agIran.Name
		p.mu.Unlock()
	}
	agKharej, err2 := s.GetAgent(p.kharej)
	var kharejSrv int64
	if err2 == nil {
		kharejSrv = agKharej.ServerID
		p.mu.Lock()
		p.kharejName = agKharej.Name
		p.mu.Unlock()
	}
	iranSrvName := ""
	kharejSrvName := ""
	if iranSrv > 0 {
		if sv, gerr := s.GetServer(iranSrv); gerr == nil {
			iranSrvName = sv.Name
		}
	}
	if kharejSrv > 0 {
		if sv, gerr := s.GetServer(kharejSrv); gerr == nil {
			kharejSrvName = sv.Name
		}
	}
	topoID, topoErr := commonEnabledTopology(s, iranSrv, kharejSrv)
	if err != nil || err2 != nil || iranSrv == 0 || kharejSrv == 0 || topoErr != nil || iranSrvName == "" || kharejSrvName == "" {
		p.mu.Lock()
		p.boardErr = mboardResolveErr(err, err2, iranSrv, kharejSrv, topoErr)
		p.rows = unprobedRows(p.methods)
		p.mu.Unlock()
		p.finish()
		return
	}
	p.mu.Lock()
	p.topoID, p.iranSrv, p.kharejSrv = topoID, iranSrv, kharejSrv
	p.mu.Unlock()
	for _, method := range p.methods {
		p.setPhase(method)
		p.probeOne(method)
		p.mu.Lock()
		p.cur = ""
		p.mu.Unlock()
	}
	p.finish()
}

func mboardResolveErr(iranErr, kharejErr error, iranSrv, kharejSrv int64, topoErr error) string {
	switch {
	case iranErr != nil:
		return "the iran agent could not be resolved"
	case kharejErr != nil:
		return "the kharej agent could not be resolved"
	case iranSrv == 0:
		return "the iran agent is not linked to a server yet"
	case kharejSrv == 0:
		return "the kharej agent is not linked to a server yet"
	case topoErr != nil:
		return "no enabled topology contains both endpoint servers — the probe tunnels have nowhere to live"
	}
	return ""
}

// unprobedRows builds the catalog-shaped rows for a board that could not
// run (honest unprobed, not fake zeros).
func unprobedRows(methods []string) []methodBoardRow {
	rows := []methodBoardRow{}
	for _, m := range methods {
		r := methodBoardRow{Method: m, Reachability: "unprobed"}
		if entry, isManifest := mboardManifestEntryOf(m); isManifest {
			r.Engine = entry.Engine
			r.Layer = strings.ToLower(strings.TrimSpace(entry.Layer))
			r.Transport = entry.Transport
			r.IPVer = entry.IPVer
		} else {
			r.Engine = m
			if meta, ok := methodCatalog[m]; ok {
				r.Layer, r.Transport, r.IPVer = meta.Layer, meta.Transport, meta.IPVer
			}
		}
		rows = append(rows, r)
	}
	return rows
}

// commonEnabledTopology finds the enabled topology that contains BOTH
// endpoint servers (lowest id wins — deterministic). Temporary probe
// tunnels must live in a real enabled topology or the agents never
// converge them (compileDesiredTunnelSpecs skips tunnels of missing or
// disabled topologies).
func commonEnabledTopology(s *store.Store, iranSrv, kharejSrv int64) (int64, error) {
	if iranSrv == 0 || kharejSrv == 0 {
		return 0, fmt.Errorf("endpoint server missing")
	}
	topos, err := s.ListTopologies()
	if err != nil {
		return 0, err
	}
	best := int64(0)
	for _, topo := range topos {
		if !topo.Enabled {
			continue
		}
		members, err := s.TopologyMembers(topo.ID)
		if err != nil {
			continue
		}
		hasIran, hasKharej := false, false
		for _, m := range members {
			if m == iranSrv {
				hasIran = true
			}
			if m == kharejSrv {
				hasKharej = true
			}
		}
		if hasIran && hasKharej && (best == 0 || topo.ID < best) {
			best = topo.ID
		}
	}
	if best == 0 {
		return 0, fmt.Errorf("no enabled topology contains both endpoints")
	}
	return best, nil
}

// ---- API ------------------------------------------------------------------

// handleMethodBoard serves GET /api/benchmark/method-board
// (?iran=<agent>&kharej=<agent>[&run=1]) — cached board + single-flight
// background refresh with run=1 (the pair-benchmark pattern).
func handleMethodBoard(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	iran := strings.TrimSpace(q.Get("iran"))
	kharej := strings.TrimSpace(q.Get("kharej"))
	run := q.Get("run") == "1" // any other value is inert (§2.22 refresh discipline)
	if iran == "" || kharej == "" {
		writeAPIError(w, r, "E-IN-02", "iran and kharej agent ids are required")
		return
	}
	if iran == kharej {
		writeAPIError(w, r, "E-IN-06", "iran and kharej must be two distinct agents")
		return
	}
	if !validAgentID(iran) || !validAgentID(kharej) {
		writeAPIError(w, r, "E-IN-06", "bad agent id")
		return
	}
	mboardMu.Lock()
	defer mboardMu.Unlock()
	mboardRestored.Do(restoreMBoards)
	st := mboardBy[mboardKey(iran, kharej)]
	if st == nil {
		// resolve the method list for the snapshot shape before any run
		methods := append([]string{}, uiKnownEngines...)
		if s := meshStorePeek(); s != nil {
			methods = methodBoardCandidates(
				agentEngineCatalogList(s, iran), agentEngineCatalogList(s, kharej))
		}
		st = &mboardState{iran: iran, kharej: kharej, methods: methods}
		mboardBy[mboardKey(iran, kharej)] = st
	}
	if run && !st.running {
		s := meshStorePeek()
		if s == nil {
			writeAPIError(w, r, "E-SYS-01", "mesh store unavailable (details in the panel error journal)")
			return
		}
		// single-flight: one run per pair; the run goroutine owns the store
		// reference and the method-list snapshot taken now
		st.mu.Lock()
		st.running = true
		st.stop = make(chan struct{})
		st.stopOnce = sync.Once{}
		st.store = s
		st.startedAt = time.Now()
		st.finishedAt = time.Time{}
		st.boardErr = ""
		st.rows = nil
		st.methods = methodBoardCandidates(
			agentEngineCatalogList(s, iran), agentEngineCatalogList(s, kharej))
		st.mu.Unlock()
		go st.run()
	}
	writeJSON(w, mboardSnapshot(st))
}
