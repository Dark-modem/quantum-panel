package panel

// offline_pending_test.go — TASK 1 (phase 13): the offline-pending
// desired contract through the REAL hub with the e2e fake WS agents.
//
// The fake's reads run in a BACKGROUND GOROUTINE (never with read
// deadlines — a gorilla read deadline poisons the connection), so the
// tests poll recorded envelopes instead of racing the socket.
//
//	1. a push to a NEVER-CONNECTED agent QUEUES (the old gate refused —
//	   the compat-matrix defect); the first hello delivers it, the ack
//	   applies and clears;
//	2. a LOST ACK is recovered: the first post-hello heartbeat triggers
//	   exactly ONE re-delivery of the same revision (no storm);
//	3. two pushes while offline COALESCE into the latest document;
//	4. a KNOWN-v1 agent still refuses DesiredV2 (frozen matrix flow).

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/Dark-modem/quantum-protocol"
)

// hubRecorder wraps a connected fake agent with a background reader.
type hubRecorder struct {
	fa   *fakeAgentV2
	mu   sync.Mutex
	want []protocol.DesiredV2
	stop chan struct{}
	done chan struct{}
}

func startHubRecorder(fa *fakeAgentV2) *hubRecorder {
	r := &hubRecorder{fa: fa, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(r.done)
		for {
			select {
			case <-r.stop:
				return
			default:
			}
			// A raw read (no t.Fatal): the close() below races this read
			// by design, and a dead socket just ends the recorder.
			_, raw, err := fa.ws.ReadMessage()
			if err != nil {
				return
			}
			_, typ, data, derr := protocol.DecodeAny(raw)
			if derr != nil {
				continue
			}
			if typ == protocol.MsgDesired {
				var d protocol.DesiredV2
				if json.Unmarshal(data, &d) == nil {
					r.mu.Lock()
					r.want = append(r.want, d)
					r.mu.Unlock()
				}
			}
		}
	}()
	return r
}

func (r *hubRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.want)
}

func (r *hubRecorder) last() protocol.DesiredV2 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.want) == 0 {
		return protocol.DesiredV2{}
	}
	return r.want[len(r.want)-1]
}

func (r *hubRecorder) close() {
	close(r.stop)
	r.fa.ws.Close()
	<-r.done
}

// waitForCount polls until the recorder holds >= n desired envelopes.
func waitForCount(t *testing.T, r *hubRecorder, n int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if r.count() >= n {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("expected >= %d desired envelopes, saw %d", n, r.count())
}

// assertQuiet fails if ANOTHER desired envelope arrives within the window.
func assertQuiet(t *testing.T, r *hubRecorder, window time.Duration, before int) {
	t.Helper()
	time.Sleep(window)
	if n := r.count(); n != before {
		t.Fatalf("expected silence after %d deliveries, saw %d", before, n)
	}
}

func TestHubOfflinePushQueuedAndAppliedAtHello(t *testing.T) {
	f := e2eSetup(t)
	offlineSeed(t, f, "hub-off")
	// Enroll + link + catalog but DO NOT connect: never a session.
	id, key := f.enrollLinkCatalog(t, "offline-new", f.iran[0])

	res := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+id+"/push", nil)
	if res["pushed"] != false || res["queued"] != true {
		t.Fatalf("push to a never-connected agent must queue: %v", res)
	}
	if v, err := f.s.AgentV2(id); err != nil || strings.TrimSpace(v.DesiredV2) == "" {
		t.Fatalf("the queued document must be durably pending (err=%v)", err)
	}

	// The agent connects: hello delivers the pending doc; ack applies.
	fa := dialFakeAgentV2(t, f.srv, id, key)
	rec := startHubRecorder(fa)
	defer rec.close()
	waitForCount(t, rec, 1, 5*time.Second)
	d := rec.last()
	fa.ack(d, true, "")
	agentTestWaitFor(t, "pending cleared + applied_v2 set after ack", func() bool {
		v2, err := f.s.AgentV2(id)
		return err == nil && strings.TrimSpace(v2.DesiredV2) == "" && strings.TrimSpace(v2.AppliedV2) != ""
	})
}

// TestHubAckLostRedeliveredAfterHeartbeat: the agent swallows the first
// delivery (ack lost); the first post-hello heartbeat re-delivers the
// same revision EXACTLY once; the late ack applies it.
func TestHubAckLostRedeliveredAfterHeartbeat(t *testing.T) {
	f := e2eSetup(t)
	offlineSeed(t, f, "hub-ackloss")
	id, key := f.enrollLinkCatalog(t, "ack-loss", f.iran[0])
	fa := dialFakeAgentV2(t, f.srv, id, key) // live session
	rec := startHubRecorder(fa)
	defer rec.close()

	res := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+id+"/push", nil)
	if res["pushed"] != true {
		t.Fatalf("live push must deliver: %v", res)
	}
	waitForCount(t, rec, 1, 5*time.Second) // the (lost) delivery
	before := rec.count()

	// The first post-hello heartbeat re-delivers the SAME revision.
	fa.report(protocol.StateReportV2{Tunnels: []protocol.TunnelRuntime{}})
	waitForCount(t, rec, before+1, 5*time.Second)
	redelivered := rec.last()
	if redelivered.Revision != rec.want[0].Revision {
		t.Fatalf("re-delivery must repeat the same revision: %s vs %s",
			redelivered.Revision, rec.want[0].Revision)
	}

	// No storm: a second heartbeat adds nothing. (The hello/push race
	// may have delivered more than one envelope up front — measure the
	// quiet baseline AFTER the count settles, then compare.)
	time.Sleep(800 * time.Millisecond)
	settled := rec.count()
	fa.report(protocol.StateReportV2{Tunnels: []protocol.TunnelRuntime{}})
	assertQuiet(t, rec, 1200*time.Millisecond, settled)

	// The (late) ack applies the re-delivered revision.
	fa.ack(redelivered, true, "")
	agentTestWaitFor(t, "late ack applies after re-delivery", func() bool {
		v2, err := f.s.AgentV2(id)
		return err == nil && strings.TrimSpace(v2.DesiredV2) == "" && strings.TrimSpace(v2.AppliedV2) != ""
	})
}

// TestHubTwoOfflinePushesCoalesce: two pushes while the agent is
// offline collapse into the LATEST document (exactly one delivery).
func TestHubTwoOfflinePushesCoalesce(t *testing.T) {
	f := e2eSetup(t)
	topoID := offlineSeed(t, f, "hub-co")
	id, key := f.enrollLinkCatalog(t, "coalesce", f.iran[0])

	meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+id+"/push", nil)
	if err := f.s.SetWeightPolicy(topoID, 0, 0, 0, 70); err != nil {
		t.Fatal(err)
	}
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+id+"/push", nil)

	fa := dialFakeAgentV2(t, f.srv, id, key)
	rec := startHubRecorder(fa)
	defer rec.close()
	waitForCount(t, rec, 1, 5*time.Second)
	assertQuiet(t, rec, 1200*time.Millisecond, 1) // coalesced: ONE delivery

	d := rec.last()
	found := false
	if d.Balancing != nil {
		for _, w := range d.Balancing.Weights {
			if w.Weight == 70 {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the delivered document must be the NEWEST (weight 70): %+v", d.Balancing)
	}
}

// TestHubV1AgentRefusesOfflinePush: the frozen v1 flow is untouched.
func TestHubV1AgentRefusesOfflinePush(t *testing.T) {
	f := e2eSetup(t)
	offlineSeed(t, f, "hub-v1")
	id, _ := f.enrollLinkCatalog(t, "v1-offline", f.iran[0])
	// A KNOWN v1 agent: it HAS connected (last_seen > 0) with v1.
	if err := f.s.SetAgentV2Session(id, 1, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.DB().Exec(`UPDATE agents SET last_seen = 1794180000 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	res := meshPut(t, f.client, f.srv, "POST", "/api/mesh/agents/"+id+"/push", nil)
	if res["pushed"] != false || res["queued"] == true {
		t.Fatalf("a known-v1 agent must still refuse DesiredV2: %v", res)
	}
	if v, _ := f.s.AgentV2(id); strings.TrimSpace(v.DesiredV2) != "" {
		t.Fatal("no pending document may queue for a v1 agent")
	}
}

// offlineSeed creates one 1+1 topology with one tunnel so the queued
// desired doc has real content.
func offlineSeed(t *testing.T, f *e2eFixture, name string) int64 {
	t.Helper()
	meshPut(t, f.client, f.srv, "POST", "/api/mesh/topologies/generate", TopoGenRequest{
		Name: name, IranServers: f.iran[0:1], ForeignServers: f.foreign[0:1],
		TunnelsPerPair: 1, Engines: []string{"matrix"}, Enabled: true,
	})
	return topoIDByName(t, f.s, name)
}
