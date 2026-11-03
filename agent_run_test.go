package main

// Runtime/protocol tests for the agent: full WS round trip against an
// httptest controller, reconnect with backoff, fatal welcome refusal and
// offline (controller-less) reconciliation after a simulated crash.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeAgentLink satisfies agentLink for applier tests.
type fakeAgentLink struct {
	mu      sync.Mutex
	sent    []sentMsg
	results map[string]json.RawMessage
}

type sentMsg struct {
	typ string
	raw json.RawMessage
}

func (f *fakeAgentLink) send(typ string, payload any) error {
	b, err := agentEncode(typ, payload)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, sentMsg{typ: typ, raw: b})
	return nil
}

func (f *fakeAgentLink) waitFor(typ string, timeout time.Duration) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if raw, ok := f.results[typ]; ok {
		return raw, nil
	}
	return nil, errors.New("timed out waiting for " + typ)
}

func (f *fakeAgentLink) sentOf(typ string) []json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []json.RawMessage
	for _, m := range f.sent {
		if m.typ == typ {
			out = append(out, m.raw)
		}
	}
	return out
}

// fakeBlockingWS is an agentWS that never receives a welcome: reads block
// until Close, writes are recorded. Used for the shutdown-path test.
type fakeBlockingWS struct {
	mu     sync.Mutex
	writes [][]byte
	closed chan struct{}
	once   sync.Once
}

func newFakeBlockingWS() *fakeBlockingWS { return &fakeBlockingWS{closed: make(chan struct{})} }

func (f *fakeBlockingWS) WriteMessage(messageType int, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := make([]byte, len(data))
	copy(c, data)
	f.writes = append(f.writes, c)
	return nil
}

func (f *fakeBlockingWS) ReadMessage() (int, []byte, error) {
	<-f.closed
	return 0, nil, errors.New("use of closed network connection")
}

func (f *fakeBlockingWS) Close() error {
	f.once.Do(func() { close(f.closed) })
	return nil
}

// agentWSHelpers are tiny protocol shims shared by the server side of the
// httptest controllers below.
func agentSendEnv(c *websocket.Conn, typ string, payload any) error {
	b, err := agentEncode(typ, payload)
	if err != nil {
		return err
	}
	return c.WriteMessage(websocket.TextMessage, b)
}

func agentReadEnv(c *websocket.Conn) (string, json.RawMessage, error) {
	_, raw, err := c.ReadMessage()
	if err != nil {
		return "", nil, err
	}
	return agentDecode(raw)
}

// TestAgentWSRoundTrip speaks the whole protocol against an httptest
// controller: hello→welcome, desired→ack (with main_host detail), command
// (allowlisted)→result, and a heartbeat carrying the state report.
func TestAgentWSRoundTrip(t *testing.T) {
	agentLab(t)
	port, userPort := freeMeshPort(t), freeMeshPort(t)
	ident := agentIdentity{ID: "agt_rt", Key: "krt", Server: "http://127.0.0.1:1", BasePath: "panel", Name: "lab"}
	if err := agentSaveIdentity(ident); err != nil {
		t.Fatal(err)
	}

	var conns int32
	helloCh := make(chan agentHello, 8)
	ackCh := make(chan agentAck, 8)
	resultCh := make(chan agentCommandResult, 8)
	hbCh := make(chan agentHeartbeat, 8)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/panel"+agentWSPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get(agentIDHeader) != ident.ID || r.Header.Get(agentKeyHeader) != ident.Key {
			http.Error(w, "bad agent headers", http.StatusUnauthorized)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		atomic.AddInt32(&conns, 1)
		c.SetReadDeadline(time.Now().Add(20 * time.Second))
		typ, data, err := agentReadEnv(c)
		if err != nil || typ != agentMsgHello {
			return
		}
		var h agentHello
		_ = json.Unmarshal(data, &h)
		helloCh <- h
		if err := agentSendEnv(c, agentMsgWelcome, agentWelcome{OK: true, Server: "test-ctrl"}); err != nil {
			return
		}
		desired := agentDesired{Revision: "500", Matrix: &agentDesiredMatrix{
			Instance: "matrix", Action: "apply",
			Hub: &agentDesiredHub{Family: "ipv4", Port: port, Addr: "185.226.119.74", Ports: []int{userPort}, Token: strings.Repeat("a", 48)},
		}}
		if err := agentSendEnv(c, agentMsgDesired, desired); err != nil {
			return
		}
		ackSeen := false
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			c.SetReadDeadline(time.Now().Add(20 * time.Second))
			typ, data, err := agentReadEnv(c)
			if err != nil {
				return
			}
			switch typ {
			case agentMsgAck:
				var a agentAck
				_ = json.Unmarshal(data, &a)
				ackCh <- a
				ackSeen = true
				// Push an allowlisted command now that the hub exists.
				_ = agentSendEnv(c, agentMsgCommand, agentCommand{
					ID: "cmd-1", Op: agentCmdDiagPing,
					Params: map[string]string{"instance": "matrix", "target": "10.254.231.2"},
				})
			case agentMsgResult:
				var cr agentCommandResult
				_ = json.Unmarshal(data, &cr)
				resultCh <- cr
				if ackSeen {
					// Done; keep the outer loop draining heartbeats until the
					// client disconnects or the deadline passes.
					ackSeen = false
				}
			case agentMsgHeartbeat:
				var hb agentHeartbeat
				_ = json.Unmarshal(data, &hb)
				select {
				case hbCh <- hb:
				default:
				}
			}
		}
	}))
	t.Cleanup(srv.Close)

	oldDialer := agentDialer
	t.Cleanup(func() { agentDialer = oldDialer })
	agentDialer = func(agentIdentity) (agentWS, error) {
		u := strings.Replace(srv.URL, "http://", "ws://", 1) + "/panel" + agentWSPath
		hdr := http.Header{}
		hdr.Set(agentIDHeader, ident.ID)
		hdr.Set(agentKeyHeader, ident.Key)
		c, _, err := (&websocket.Dialer{}).Dial(u, hdr)
		return c, err
	}

	rt := &agentRuntime{ident: ident, start: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- rt.loop(ctx) }()

	select {
	case h := <-helloCh:
		if h.ID != ident.ID || h.Version != panelVersion || h.OS != runtime.GOOS || len(h.Caps) != 4 {
			t.Fatalf("hello = %+v", h)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no hello")
	}
	select {
	case a := <-ackCh:
		if !a.OK || a.Revision != "500" || !strings.Contains(a.Detail["main_host"], "BEGIN RSA PUBLIC KEY") {
			t.Fatalf("ack = %+v", a)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no ack for desired")
	}
	select {
	case cr := <-resultCh:
		if cr.ID != "cmd-1" || !cr.OK || cr.Output != "ping ok" {
			t.Fatalf("result = %+v", cr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no command result")
	}
	select {
	case hb := <-hbCh:
		if hb.State == nil || hb.State.Traffic == nil || hb.State.NodeDetect == nil {
			t.Fatalf("heartbeat state incomplete: %+v", hb.State)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no heartbeat received")
	}
	if st := agentLoadState(); st.AppliedRevision != "500" || st.DesiredStatus != "applied" || st.WS == nil || !st.WS.Connected {
		t.Fatalf("state after round trip = %+v", st)
	}

	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("graceful shutdown exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not stop on cancel")
	}
	AgentOpsWait() // drain async desired/command goroutines before the labs restore
	if atomic.LoadInt32(&conns) != 1 {
		t.Fatalf("round trip should hold one connection, got %d", conns)
	}
	if st := agentLoadState(); st.WS.Connected {
		t.Fatal("state should be journaled disconnected after shutdown")
	}
}

// TestAgentReconnectBackoff: a dropped connection is retried with backoff.
func TestAgentReconnectBackoff(t *testing.T) {
	agentLab(t)
	oldBase := agentBackoffBase
	agentBackoffBase = 5 * time.Millisecond
	t.Cleanup(func() { agentBackoffBase = oldBase })

	ident := agentIdentity{ID: "agt_r", Key: "k2", Server: "http://127.0.0.1:1"}
	if err := agentSaveIdentity(ident); err != nil {
		t.Fatal(err)
	}

	var conns, dials int32
	helloCh := make(chan struct{}, 16)
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		atomic.AddInt32(&conns, 1)
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		typ, _, err := agentReadEnv(c)
		if err != nil || typ != agentMsgHello {
			return
		}
		helloCh <- struct{}{}
		_ = agentSendEnv(c, agentMsgWelcome, agentWelcome{OK: true})
		// Drop immediately: the agent must reconnect with backoff.
	}))
	t.Cleanup(srv.Close)

	oldDialer := agentDialer
	t.Cleanup(func() { agentDialer = oldDialer })
	agentDialer = func(agentIdentity) (agentWS, error) {
		n := atomic.AddInt32(&dials, 1)
		if n == 1 {
			return nil, errors.New("connection refused (injected)")
		}
		u := strings.Replace(srv.URL, "http://", "ws://", 1) + agentWSPath
		c, _, err := (&websocket.Dialer{}).Dial(u, nil)
		return c, err
	}

	rt := &agentRuntime{ident: ident, start: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() { done <- rt.loop(ctx) }()

	hellos := 0
	deadline := time.Now().Add(15 * time.Second)
	for hellos < 2 && time.Now().Before(deadline) {
		select {
		case <-helloCh:
			hellos++
		case <-time.After(500 * time.Millisecond):
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d after cancel", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("loop did not stop")
	}
	AgentOpsWait() // drain async desired/command goroutines before the labs restore
	if hellos < 2 || atomic.LoadInt32(&conns) < 2 || atomic.LoadInt32(&dials) < 3 {
		t.Fatalf("expected injected dial failure then reconnect: hellos=%d conns=%d dials=%d",
			hellos, conns, dials)
	}
}

// TestAgentWelcomeRefusalExitsOne: welcome ok:false is fatal (exit 1), not a
// reconnectable failure — the enrollment is dead.
func TestAgentWelcomeRefusalExitsOne(t *testing.T) {
	agentLab(t)
	ident := agentIdentity{ID: "agt_x", Key: "kx", Server: "http://127.0.0.1:1"}
	if err := agentSaveIdentity(ident); err != nil {
		t.Fatal(err)
	}
	up := websocket.Upgrader{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if typ, _, err := agentReadEnv(c); err != nil || typ != agentMsgHello {
			return
		}
		_ = agentSendEnv(c, agentMsgWelcome, agentWelcome{OK: false, Error: "revoked"})
	}))
	t.Cleanup(srv.Close)
	oldDialer := agentDialer
	t.Cleanup(func() { agentDialer = oldDialer })
	agentDialer = func(agentIdentity) (agentWS, error) {
		u := strings.Replace(srv.URL, "http://", "ws://", 1) + agentWSPath
		c, _, err := (&websocket.Dialer{}).Dial(u, nil)
		return c, err
	}

	rt := &agentRuntime{ident: ident, start: time.Now()}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if code := rt.loop(ctx); code != 1 {
		t.Fatalf("refused welcome must exit 1, got %d", code)
	}
	if st := agentLoadState(); st.WS == nil || st.WS.Connected || !strings.Contains(st.WS.LastError, "revoked") {
		t.Fatalf("refusal not journaled: %+v", st.WS)
	}
}

// TestAgentOfflineReconcile simulates a crash: a pending desired is on disk,
// a fresh runtime (no controller) applies it on boot; the node flow stays
// pending until the controller finishes the register round-trip.
func TestAgentOfflineReconcile(t *testing.T) {
	agentLab(t)
	// Hub desired pending on disk (crash before ack).
	port, userPort := freeMeshPort(t), freeMeshPort(t)
	hubDesired := agentHubDesired("900", port, []int{userPort})
	if err := agentMutateState(func(st *agentPersistedState) {
		d := *hubDesired
		st.Desired = &d
		st.DesiredStatus = "pending"
	}); err != nil {
		t.Fatal(err)
	}
	agentReconcilePending(nil)
	st := agentLoadState()
	if st.AppliedRevision != "900" || st.DesiredStatus != "applied" {
		t.Fatalf("offline hub reconcile = applied:%q status:%q", st.AppliedRevision, st.DesiredStatus)
	}
	if !loadMatrixInstance(matrixLegacyInstance()).Enabled {
		t.Fatal("hub instance not applied offline")
	}

	// Node desired pending with no controller: provisions, defers pairing.
	in2 := matrixInstance{ID: "matrix2"}
	nodeDesired := &agentDesired{Revision: "901", Matrix: &agentDesiredMatrix{
		Instance: in2.ID, Action: "apply",
		Bundle: agentTestBundle(t, in2, freeMeshPort(t), freeMeshPort(t), "901"),
	}}
	if err := agentMutateState(func(st *agentPersistedState) {
		d := *nodeDesired
		st.Desired = &d
		st.DesiredStatus = "pending"
	}); err != nil {
		t.Fatal(err)
	}
	agentReconcilePending(nil)
	st = agentLoadState()
	if st.DesiredStatus != "pending" {
		t.Fatalf("node apply must stay pending without a controller, got %q", st.DesiredStatus)
	}
	if st.AppliedRevision != "900" {
		t.Fatalf("deferred node apply must not advance the revision, got %q", st.AppliedRevision)
	}
	if !loadMatrixInstance(in2).Enabled {
		t.Fatal("node provisioning did not happen offline")
	}

	// "Controller connects": the same pending desired completes and acks.
	link := &fakeAgentLink{results: map[string]json.RawMessage{agentMsgRegisterResult: []byte(`{"ok":true}`)}}
	agentReconcilePending(link)
	st = agentLoadState()
	if st.AppliedRevision != "901" || st.DesiredStatus != "applied" {
		t.Fatalf("post-connect reconcile = applied:%q status:%q", st.AppliedRevision, st.DesiredStatus)
	}
	if regs := link.sentOf(agentMsgRegister); len(regs) != 1 {
		t.Fatalf("expected one register message, got %d", len(regs))
	}
}
