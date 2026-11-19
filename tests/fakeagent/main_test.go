package main

// BACKEND-33 fakeagent alignment tests: each aligned v2 behavior is
// asserted against the REAL protocol types (quantum-protocol 9b9a27c) —
// the fake builds typed structures and every test round-trips the wire
// bytes through protocol.EncodeVersioned/DecodeAny, so the fake stays a
// faithful protocol citizen by construction. Hermetic: pure builders,
// fixed seed, injected clock; no network, no store.

import (
	"encoding/json"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-protocol"
)

func newTestRuntime() *fakeRuntime {
	return &fakeRuntime{
		ids:           []string{"t-a", "t-b", "t-c"},
		engStats:      []fakeEngStat{{"matrix", "running"}},
		engOf:         map[string]string{},
		traffic:       true,
		engineEnabled: true,
		rx:            1080,
		tx:            216,
		prx:           1000,
		ptx:           200,
		rng:           rand.New(rand.NewSource(42)),
		hostname:      "fake-agent",
		cpuModel:      "Fake Xeon",
		cores:         4,
		memMB:         4096,
		diskGB:        80,
	}
}

// injectClock overrides the R7 report clock seam for one test.
func injectClock(t *testing.T, fixed time.Time) {
	t.Helper()
	old := agentReportClock
	agentReportClock = func() time.Time { return fixed }
	t.Cleanup(func() { agentReportClock = old })
}

// roundTrip encodes a v2 envelope like the real agent does and unwraps
// it the way the panel does (tolerant DecodeAny).
func roundTrip(t *testing.T, typ string, payload any) json.RawMessage {
	t.Helper()
	b, err := protocol.EncodeVersioned(protocol.ProtoVersionV2, typ, payload)
	if err != nil {
		t.Fatalf("encode %s: %v", typ, err)
	}
	gotV, gotTyp, data, err := protocol.DecodeAny(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if gotV != protocol.ProtoVersionV2 || gotTyp != typ {
		t.Fatalf("envelope mismatch: v=%d type=%s", gotV, gotTyp)
	}
	return data
}

// TestFakeHelloRichCatalog: hello carries the string engine_catalog AND
// the R3 rich engines catalog (the panel stores the rich one and
// hard-validates tunnel engine ids against it).
func TestFakeHelloRichCatalog(t *testing.T) {
	rt := newTestRuntime()
	h := rt.buildHello("agt_x")
	if h.ID != "agt_x" || h.Version != "fake-1.0" || h.Hostname != "fake-agent" {
		t.Fatalf("hello identity wrong: %+v", h)
	}
	if h.MaxVersion != 2 {
		t.Fatalf("max_version = %d, want 2", h.MaxVersion)
	}
	if len(h.EngineCatalog) == 0 || len(h.Engines) != len(h.EngineCatalog) {
		t.Fatalf("catalog mismatch: catalog=%v engines=%v", h.EngineCatalog, h.Engines)
	}
	for i, e := range h.Engines {
		if e.ID != h.EngineCatalog[i] {
			t.Fatalf("rich catalog entry %d id %q != catalog %q", i, e.ID, h.EngineCatalog[i])
		}
		if e.Version == "" {
			t.Fatalf("rich catalog entry %d has no version", i)
		}
	}
	var back protocol.Hello
	if err := json.Unmarshal(roundTrip(t, protocol.MsgHello, h), &back); err != nil {
		t.Fatalf("hello wire round-trip: %v", err)
	}
	if len(back.Engines) != len(h.Engines) || back.Engines[0].ID != h.Engines[0].ID {
		t.Fatalf("hello round-trip lost the rich catalog: %+v", back)
	}
}

// TestFakeDesiredAckKind: the ack mirrors the real agent's
// {revision, ok, kind:"desired-v2"} (panel matches by revision).
func TestFakeDesiredAckKind(t *testing.T) {
	ack := buildDesiredAck("rev-7")
	if ack.Revision != "rev-7" || !ack.OK || ack.Kind != "desired-v2" {
		t.Fatalf("ack wrong: %+v", ack)
	}
	var back protocol.Ack
	if err := json.Unmarshal(roundTrip(t, protocol.MsgAck, ack), &back); err != nil {
		t.Fatalf("ack wire round-trip: %v", err)
	}
	if back.Revision != "rev-7" || !back.OK || back.Kind != "desired-v2" {
		t.Fatalf("ack round-trip wrong: %+v", back)
	}
}

// TestFakeDesiredEngineFlipAndRevisionEcho: desired engines are keyed
// "id" per protocol.EngineSpec — the old hand-decode read "engine" and
// NEVER flipped (drift D0). Also covers the applied_revision echo.
func TestFakeDesiredEngineFlipAndRevisionEcho(t *testing.T) {
	injectClock(t, time.Unix(1700000000, 0))
	rt := newTestRuntime()
	raw, _ := json.Marshal(protocol.DesiredV2{
		Revision: "rev-9",
		Engines:  []protocol.EngineSpec{{ID: "matrix", Enabled: false}},
	})
	var dv protocol.DesiredV2
	if err := json.Unmarshal(raw, &dv); err != nil {
		t.Fatalf("decode desired: %v", err)
	}
	ack := rt.applyDesired(&dv)
	if ack.Revision != "rev-9" || !ack.OK {
		t.Fatalf("ack wrong: %+v", ack)
	}
	if rt.engineEnabled {
		t.Fatal("matrix engine still enabled after desired disabled it — the old 'engine' key bug")
	}
	rep := rt.buildStateReportV2()
	if rep.AppliedRevision != "rev-9" {
		t.Fatalf("applied_revision = %q, want rev-9 (echo)", rep.AppliedRevision)
	}
	for _, es := range rep.EngineStatus {
		if es.Engine == "matrix" {
			if es.Enabled || es.Instances != 0 {
				t.Fatalf("matrix engine_status after disable: enabled=%v instances=%d", es.Enabled, es.Instances)
			}
		}
	}
}

// TestFakeStateReportReportedAtClockSeam: R7 reported_at rides the
// agent's own clock seam; the heartbeat ts stays the wall clock.
func TestFakeStateReportReportedAtClockSeam(t *testing.T) {
	fixed := time.Unix(1777777777, 0)
	injectClock(t, fixed)
	rt := newTestRuntime()
	rep := rt.buildStateReportV2()
	if rep.ReportedAt != fixed.Unix() {
		t.Fatalf("reported_at = %d, want %d (injected clock)", rep.ReportedAt, fixed.Unix())
	}
	hb := agentHeartbeatV2{TS: time.Now().Unix(), UptimeS: 86400, State: rep}
	if hb.TS == fixed.Unix() {
		t.Fatal("heartbeat ts must stay the wall clock, not the injected seam")
	}
	var back agentHeartbeatV2
	if err := json.Unmarshal(roundTrip(t, protocol.MsgHeartbeat, hb), &back); err != nil {
		t.Fatalf("heartbeat wire round-trip: %v", err)
	}
	if back.State == nil || back.State.ReportedAt != fixed.Unix() {
		t.Fatalf("heartbeat round-trip lost reported_at: %+v", back.State)
	}
}

// TestFakeStateReportShapes: the full state report mirrors the real
// agent's agentBuildStateReportV2 field by field.
func TestFakeStateReportShapes(t *testing.T) {
	injectClock(t, time.Unix(1700000000, 0))
	rt := newTestRuntime()
	rt.downCount = 1 // t-a DOWN, t-b/t-c up
	rt.appliedRev = "rev-1"
	rep := rt.buildStateReportV2()

	if rep.AppliedRevision != "rev-1" {
		t.Fatalf("applied_revision = %q", rep.AppliedRevision)
	}
	if rep.ReportedAt == 0 {
		t.Fatal("reported_at missing")
	}
	if len(rep.Tunnels) != 3 || len(rep.Proc) != 2 || len(rep.EngineStatus) != 1 {
		t.Fatalf("row counts wrong: tunnels=%d proc=%d engine_status=%d",
			len(rep.Tunnels), len(rep.Proc), len(rep.EngineStatus))
	}
	up := rep.Tunnels[1] // t-b: first UP row (t-a is DOWN with downCount=1)
	if !up.Up || !up.WireKnown || !up.PayloadKnown || !up.CountersKnown {
		t.Fatalf("up tunnel flags wrong: %+v", up)
	}
	if up.UptimeS <= 0 {
		t.Fatalf("up tunnel uptime_s = %d", up.UptimeS)
	}
	// Per-direction ratios = wire/payload of the row's own counters
	// (1080/3=360 wire over 1000/3=333 payload; 216/3=72 over 200/3=66 —
	// integer truncation makes the tx ratio slightly higher than 1.08).
	if up.OverheadRatio != 360.0/333.0 {
		t.Fatalf("overhead_ratio = %v, want %v", up.OverheadRatio, 360.0/333.0)
	}
	if up.OverheadRatioTx != 72.0/66.0 {
		t.Fatalf("overhead_ratio_tx = %v, want %v", up.OverheadRatioTx, 72.0/66.0)
	}
	if up.WireRx != 360 || up.PayloadRx != 333 || up.WireTx != 72 || up.PayloadTx != 66 {
		t.Fatalf("counter shares wrong: %+v", up)
	}
	// down tunnel: last_error set, uptime unknown, no overhead ratios
	down := rep.Tunnels[0]
	if down.Up || down.LastError == "" {
		t.Fatalf("down tunnel wrong: %+v", down)
	}
	if down.UptimeS != 0 {
		t.Fatalf("down tunnel uptime_s = %d, want 0 (real agent omits it)", down.UptimeS)
	}
	if down.OverheadRatio != 0 || down.OverheadRatioTx != 0 {
		t.Fatalf("down tunnel overhead ratios set: %+v", down)
	}
	// proc: known, one supervised process, rss bytes
	p := rep.Proc[0]
	if !p.Known || p.Processes != 1 || p.RSSBytes == 0 || p.CPUPercent <= 0 {
		t.Fatalf("proc row wrong: %+v", p)
	}
	// per-engine overhead samples grouped from the tunnels
	if len(rep.Engines) != 1 || rep.Engines[0].Engine != "matrix" || rep.Engines[0].Tunnels != 2 {
		t.Fatalf("engines sample wrong: %+v", rep.Engines)
	}
	if rep.Engines[0].Ratio < 1.0799 || rep.Engines[0].RatioTx < 1.0799 {
		t.Fatalf("engines sample ratios wrong: %+v", rep.Engines[0])
	}
	// engine installation state
	es := rep.EngineStatus[0]
	if es.Engine != "matrix" || !es.Installed || es.Version == "" || !es.Enabled || es.Instances != 2 || es.BytesOnDisk == 0 {
		t.Fatalf("engine_status wrong: %+v", es)
	}
	// resources
	r := rep.Resources
	if r == nil || r.CPUModel != "Fake Xeon" || r.CPUCores != 4 || r.MemTotalMB != 4096 || r.DiskTotalGB != 80 || r.UptimeS == 0 {
		t.Fatalf("resources wrong: %+v", r)
	}
	if r.MemUsedMB == 0 || r.MemUsedMB >= r.MemTotalMB {
		t.Fatalf("mem_used_mb implausible: %d", r.MemUsedMB)
	}
	// no pending journal → hardening absent (real agent: nil)
	if rep.Hardening != nil {
		t.Fatalf("hardening set with nothing pending: %+v", rep.Hardening)
	}
	// v1-carried sections legitimately absent for the fake (omitempty in
	// the real agent too when there is nothing to say): matrix,
	// update_progress, node_detect, errors, forwards.
	raw, err := json.Marshal(rep)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{`"matrix":`, `"update_progress":`, `"node_detect":`, `"errors":`, `"forwards":`} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("unexpected %s section in the fake's report", key)
		}
	}
}

// TestFakeEngineStatusNoNullLastError: the old map-based builder emitted
// `"last_error": null` (protocol field is a string, omitempty — the real
// agent omits it); the typed builder must omit the key entirely.
func TestFakeEngineStatusNoNullLastError(t *testing.T) {
	rt := newTestRuntime()
	raw, err := json.Marshal(rt.buildStateReportV2())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), `"last_error":null`) {
		t.Fatalf("wire drift: report still emits null last_error: %s", raw)
	}
	var back protocol.StateReportV2
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// TestFakeHardeningMirror: hardening.apply arms the safe-apply mirror
// (reported in the following heartbeats like a real agent's pending
// journal), hardening.confirm clears it.
func TestFakeHardeningMirror(t *testing.T) {
	fixed := time.Unix(1700000100, 0)
	injectClock(t, fixed)
	rt := newTestRuntime()
	res := rt.applyFakeCommand(&protocol.Command{
		ID:     "c1",
		Op:     "hardening.apply",
		Params: map[string]string{"payload": `{"name":"p1"}`, "rollback_timer_s": "600"},
	})
	if !res.OK || res.ID != "c1" {
		t.Fatalf("apply result wrong: %+v", res)
	}
	var data map[string]any
	if err := json.Unmarshal(res.Data, &data); err != nil {
		t.Fatalf("apply result data: %v", err)
	}
	if data["armed"] != true || data["timer_unit"] != "s" || data["verify"] != "ok" {
		t.Fatalf("apply result data shape (HARDENING §5b): %v", data)
	}
	rep := rt.buildStateReportV2()
	h := rep.Hardening
	if h == nil || !h.Armed || h.Name != "p1" || h.TimerUnit != "s" {
		t.Fatalf("hardening mirror wrong: %+v", h)
	}
	if h.DeadlineS != fixed.Unix()+600 {
		t.Fatalf("deadline_s = %d, want %d (rollback timer 600)", h.DeadlineS, fixed.Unix()+600)
	}
	if !strings.HasPrefix(h.JournalID, "hp-fake-") {
		t.Fatalf("journal id wrong: %q", h.JournalID)
	}
	// confirm disarms → section absent again
	res2 := rt.applyFakeCommand(&protocol.Command{ID: "c2", Op: "hardening.confirm"})
	if !res2.OK {
		t.Fatalf("confirm result wrong: %+v", res2)
	}
	if rep2 := rt.buildStateReportV2(); rep2.Hardening != nil {
		t.Fatalf("hardening still pending after confirm: %+v", rep2.Hardening)
	}
	// generic commands get a plain ok result without hardening-shaped data
	res3 := rt.applyFakeCommand(&protocol.Command{ID: "c3", Op: "some.other.op"})
	if !res3.OK || res3.Data != nil {
		t.Fatalf("generic command result drifted: %+v", res3)
	}
}

// TestFakeMixedEngineOverheadGroups: per-engine samples group from the
// tunnels' counter pairs (one sample per engine, sorted by id).
func TestFakeMixedEngineOverheadGroups(t *testing.T) {
	rt := newTestRuntime()
	rt.engOf = map[string]string{"t-a": "gost", "t-b": "matrix", "t-c": "gost"}
	rep := rt.buildStateReportV2()
	if len(rep.Engines) != 2 {
		t.Fatalf("want 2 per-engine samples, got %d: %+v", len(rep.Engines), rep.Engines)
	}
	if rep.Engines[0].Engine != "gost" || rep.Engines[1].Engine != "matrix" {
		t.Fatalf("samples not grouped/sorted: %+v", rep.Engines)
	}
	if rep.Engines[0].Tunnels != 2 || rep.Engines[1].Tunnels != 1 {
		t.Fatalf("sample tunnel counts wrong: %+v", rep.Engines)
	}
	// each tunnel row carries its assigned engine
	for _, tr := range rep.Tunnels {
		want := rt.engOf[tr.TunnelID]
		if tr.Engine != want {
			t.Fatalf("tunnel %s engine = %q, want %q", tr.TunnelID, tr.Engine, want)
		}
	}
}
