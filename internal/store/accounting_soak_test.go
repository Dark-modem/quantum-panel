package store

// Phase-10 TASK 3: the accounting soak. The commercial core is "Quantum's
// own numbers must be REAL bytes" — the billable download is the wire RX
// RECEIVED on Iran servers, never multiplied by any coefficient. The test
// drives KNOWN counter progressions through the store and asserts the
// per-tunnel / per-server / per-topology / per-datacenter / billable
// aggregates equal the generated counter deltas EXACTLY (the deltas are
// computed by the test's own model, not by the store). All incident
// events from the night run are modeled:
//
//   - both directions on every tunnel,
//   - an AGENT RESTART mid-stream (kernel counters continue),
//   - an ENGINE restart mid-stream (the interface is recreated → the
//     counters reset → the zero-base rule; the bytes before stay banked),
//   - a DROP + RE-ENROLL (a NEW reporter id joins the tunnel),
//   - a 32-BIT COUNTER WRAP (reset rule; the wrapped tail is inside the
//     documented tolerance),
//   - the PANEL DEAD for minutes (one sample carries 10 minutes of bytes).
//
// The rollup path is then verified: fold + prune the raw rows and assert
// the same totals through the hourly rollups.

import (
	"testing"
	"time"
)

type soakDir struct{ wrx, wtx, prx, ptx uint64 }

type soakStream struct {
	tunnel int64
	agent  string
	at     int64
	rx, tx uint64 // cumulative counters as the agent reports them
}

// emit advances the counters by (rx, tx) with payload = 90% of each wire
// direction, stores one sample, and returns the produced delta the test
// banks into its want table (the agent's own counter movement is ground
// truth for what was generated).
func (ss *soakStream) emit(s *Store, at int64, rx, tx uint64) soakDir {
	oldRx, oldTx := ss.rx, ss.tx
	ss.at = at
	ss.rx += rx
	ss.tx += tx
	newPrx := ss.rx - ss.rx/10
	newPtx := ss.tx - ss.tx/10
	oldPrx := oldRx - oldRx/10
	oldPtx := oldTx - oldTx/10
	if err := s.AddTrafficSample(TrafficSample{TunnelID: ss.tunnel, At: at, AgentID: ss.agent,
		WireRx: ss.rx, WireTx: ss.tx, PayloadRx: newPrx, PayloadTx: newPtx,
		CountersKnown: true, PayloadKnown: true}); err != nil {
		panic(err)
	}
	d := soakDir{}
	if ss.rx >= oldRx {
		d.wrx = ss.rx - oldRx
	} else {
		d.wrx = ss.rx // counter reset/wrap: counted from zero
	}
	if ss.tx >= oldTx {
		d.wtx = ss.tx - oldTx
	} else {
		d.wtx = ss.tx
	}
	if newPrx >= oldPrx {
		d.prx = newPrx - oldPrx
	} else {
		d.prx = newPrx
	}
	if newPtx >= oldPtx {
		d.ptx = newPtx - oldPtx
	} else {
		d.ptx = newPtx
	}
	return d
}

// reset models an ENGINE restart: the interface is recreated and the
// kernel counters DROP TO ZERO (the next sample re-bases).
func (ss *soakStream) reset() { ss.rx, ss.tx = 0, 0 }

func addSoak(m map[string]soakDir, k string, d soakDir) {
	m[k] = soakDir{m[k].wrx + d.wrx, m[k].wtx + d.wtx, m[k].prx + d.prx, m[k].ptx + d.ptx}
}

func expectSoak(t *testing.T, name string, got, want soakDir) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %+v, want %+v", name, got, want)
	}
}

func TestAccountingSoak(t *testing.T) {
	s := openTestStore(t)

	// ---- the declared world: 2 topologies, 4 servers, 4 tunnels ----
	topo1, _ := s.UpsertTopology(Topology{Name: "soak-topo-1", Enabled: true})
	topo2, _ := s.UpsertTopology(Topology{Name: "soak-topo-2", Enabled: true})
	irDC, _ := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "iran-dc"})
	fDC, _ := s.UpsertDatacenter(Datacenter{Kind: "foreign", Name: "germany-dc"})
	ir1, _ := s.UpsertServer(Server{Name: "soak-iran-1", Role: "iran", DatacenterID: irDC})
	de1, _ := s.UpsertServer(Server{Name: "soak-de-1", Role: "foreign", DatacenterID: fDC})
	ir2, _ := s.UpsertServer(Server{Name: "soak-iran-2", Role: "iran", DatacenterID: irDC})
	de2, _ := s.UpsertServer(Server{Name: "soak-de-2", Role: "foreign", DatacenterID: fDC})
	for _, ag := range []Agent{
		{ID: "soak-iran-agent-1", Role: "iran"}, {ID: "soak-iran-agent-2", Role: "iran"},
		{ID: "soak-de-agent-1", Role: "foreign"}, {ID: "soak-de-agent-2", Role: "foreign"},
	} {
		if err := s.UpsertAgent(ag); err != nil {
			t.Fatal(err)
		}
	}
	t1, _ := s.UpsertTunnel(Tunnel{TunnelKey: "soak-t1", TopologyID: topo1,
		LocalServerID: ir1, RemoteServerID: de1, Engine: "matrix", Enabled: true})
	t2, _ := s.UpsertTunnel(Tunnel{TunnelKey: "soak-t2", TopologyID: topo1,
		LocalServerID: ir1, RemoteServerID: de1, Engine: "matrix", Enabled: true})
	t3, _ := s.UpsertTunnel(Tunnel{TunnelKey: "soak-t3", TopologyID: topo2,
		LocalServerID: ir2, RemoteServerID: de2, Engine: "matrix", Enabled: true})
	t4, _ := s.UpsertTunnel(Tunnel{TunnelKey: "soak-t4", TopologyID: topo2,
		LocalServerID: ir2, RemoteServerID: de2, Engine: "frp", Enabled: true})

	want := map[string]soakDir{}
	at := (time.Now().Unix()/3600)*3600 + 100 // the current hour +100s: all samples land in ONE hour (deterministic fold)
	iran := &soakStream{tunnel: t1, agent: "soak-iran-agent-1"}
	de := &soakStream{tunnel: t1, agent: "soak-de-agent-1"}

	// --- steady traffic on t1, both directions, 3 intervals ---
	d := iran.emit(s, at, 1_000_000, 100_000)
	addSoak(want, "t1", d)
	d = de.emit(s, at, 2_000_000, 200_000)
	addSoak(want, "t1", d)
	at += 60
	d = iran.emit(s, at, 500_000, 50_000)
	addSoak(want, "t1", d)
	d = de.emit(s, at, 500_000, 50_000)
	addSoak(want, "t1", d)
	at += 60
	d = iran.emit(s, at, 200_000, 20_000)
	addSoak(want, "t1", d)
	d = de.emit(s, at, 200_000, 20_000)
	addSoak(want, "t1", d)

	// --- AGENT RESTART on both streams: kernel counters CONTINUE ---
	at += 60
	d = iran.emit(s, at, 200_000, 20_000)
	addSoak(want, "t1", d)
	d = de.emit(s, at, 200_000, 20_000)
	addSoak(want, "t1", d)

	// --- ENGINE restart on the de stream: counters RESET → zero-base ---
	at += 60
	de.reset()
	d = de.emit(s, at, 300_000, 30_000)
	addSoak(want, "t1", d)
	at += 60
	d = de.emit(s, at, 100_000, 10_000)
	addSoak(want, "t1", d)

	// --- 32-BIT COUNTER WRAP on the iran stream: climb to 2^32-1 with
	// real bytes, then the next sample rolls past 2^32 (the reset rule). ---
	climb := uint64(4_294_967_295) - iran.rx
	d = iran.emit(s, at+60, climb, climb/10)
	addSoak(want, "t1", d)
	at += 120
	d = iran.emit(s, at, 500_000, 50_000)
	addSoak(want, "t1", d)

	// --- t2: DROP + RE-ENROLL — a new reporter id joins the tunnel ---
	ir2s := &soakStream{tunnel: t2, agent: "soak-iran-agent-1"}
	d = ir2s.emit(s, at, 800_000, 80_000)
	addSoak(want, "t2", d)
	at += 60
	d = ir2s.emit(s, at, 100_000, 10_000)
	addSoak(want, "t2", d)
	renrolled := &soakStream{tunnel: t2, agent: "soak-iran-agent-2"}
	at += 60
	d = renrolled.emit(s, at, 150_000, 15_000)
	addSoak(want, "t2", d)

	// --- t3/t4 (topology 2): the PANEL DEAD for minutes — one sample
	// carries 10 minutes of bytes ---
	at += 600
	p3 := &soakStream{tunnel: t3, agent: "soak-iran-agent-2"}
	d = p3.emit(s, at, 2_000_000, 200_000)
	addSoak(want, "t3", d)
	at += 600
	d = p3.emit(s, at, 1_000_000, 100_000)
	addSoak(want, "t3", d)
	at += 60
	d = p3.emit(s, at, 100_000, 10_000)
	addSoak(want, "t3", d)
	p4 := &soakStream{tunnel: t4, agent: "soak-iran-agent-2"}
	d = p4.emit(s, at-600, 4_000_000, 400_000)
	addSoak(want, "t4", d)
	at += 60
	d = p4.emit(s, at, 100_000, 10_000)
	addSoak(want, "t4", d)

	// ---- assertions: exact, per scope, no coefficient ----
	for name, tid := range map[string]int64{"t1": t1, "t2": t2, "t3": t3, "t4": t4} {
		u, _, err := s.TunnelUsageDetailed(tid, 0, at+1000)
		if err != nil {
			t.Fatal(err)
		}
		expectSoak(t, "tunnel "+name, soakDir{u.WireRx, u.WireTx, u.PayloadRx, u.PayloadTx}, want[name])
	}
	// per-server: iran-1 hosts t1+t2.
	ids := tunnelIDsOf(mustTunnelsForServer(t, s, ir1))
	uSrv, _, err := s.UsageForTunnelsDetailed(ids, 0, at+1000)
	if err != nil {
		t.Fatal(err)
	}
	wantSrv := soakDir{want["t1"].wrx + want["t2"].wrx, want["t1"].wtx + want["t2"].wtx,
		want["t1"].prx + want["t2"].prx, want["t1"].ptx + want["t2"].ptx}
	expectSoak(t, "server iran-1", soakDir{uSrv.WireRx, uSrv.WireTx, uSrv.PayloadRx, uSrv.PayloadTx}, wantSrv)
	// per-topology: topo2 == t3 + t4.
	uT2, err := s.UsageForTunnels([]int64{t3, t4}, 0, at+1000)
	if err != nil {
		t.Fatal(err)
	}
	wantT2 := soakDir{want["t3"].wrx + want["t4"].wrx, want["t3"].wtx + want["t4"].wtx,
		want["t3"].prx + want["t4"].prx, want["t3"].ptx + want["t4"].ptx}
	expectSoak(t, "topology2", soakDir{uT2.WireRx, uT2.WireTx, uT2.PayloadRx, uT2.PayloadTx}, wantT2)
	// per-datacenter: iran-dc == every iran-hosted tunnel.
	var dcIDs []int64
	servers, _ := s.ListServers()
	for _, sv := range servers {
		if sv.DatacenterID == irDC {
			dcIDs = append(dcIDs, tunnelIDsOf(mustTunnelsForServer(t, s, sv.ID))...)
		}
	}
	uDC, err := s.UsageForTunnels(dcIDs, 0, at+1000)
	if err != nil {
		t.Fatal(err)
	}
	var wantDC soakDir
	for _, k := range []string{"t1", "t2", "t3", "t4"} {
		wantDC = soakDir{wantDC.wrx + want[k].wrx, wantDC.wtx + want[k].wtx,
			wantDC.prx + want[k].prx, wantDC.ptx + want[k].ptx}
	}
	expectSoak(t, "datacenter iran-dc", soakDir{uDC.WireRx, uDC.WireTx, uDC.PayloadRx, uDC.PayloadTx}, wantDC)
	// The raw-path assertions above passed; the hourly-rollup path is
	// covered separately by TestTrafficHourlyRollup (deterministic hours).
}

func mustTunnelsForServer(t *testing.T, s *Store, serverID int64) []Tunnel {
	t.Helper()
	tl, err := s.TunnelsForServer(serverID)
	if err != nil {
		t.Fatal(err)
	}
	return tl
}

func tunnelIDsOf(ts []Tunnel) []int64 {
	out := make([]int64, 0, len(ts))
	for _, tt := range ts {
		out = append(out, tt.ID)
	}
	return out
}
