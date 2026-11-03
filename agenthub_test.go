package main

// Tests for the agent WebSocket hub (agenthub.go): connection auth, session
// lifecycle, heartbeats/traffic rates, desired delivery, command round
// trips, revocation kick and supersede. Real gorilla clients over httptest
// on loopback only.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// agentHubTestServer stands up the agent-facing routes (no session auth —
// token handlers are assumed to be wrapped upstream in production).
func agentHubTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/agents/tokens", handleAgentTokenPost)
	mux.HandleFunc("POST /api/agents/enroll", handleAgentEnroll)
	mux.HandleFunc("POST /api/agents/{id}/desired", handleAgentDesiredPost)
	mux.HandleFunc("POST /api/agents/{id}/command", handleAgentCommandPost)
	mux.HandleFunc("POST /api/agents/{id}/revoke", handleAgentRevoke)
	mux.HandleFunc("GET /api/agent-ws", handleAgentWS)
	srv := httptest.NewServer(mux)
	t.Cleanup(func() {
		agentHubDrainAll()
		srv.Close()
	})
	return srv
}

// agentHubEnrollAgent enrolls through the real HTTP handlers and returns
// the agent identity (id + one-time plaintext key).
func agentHubEnrollAgent(t *testing.T, srv *httptest.Server, caps []string) (string, string) {
	t.Helper()
	tokenBody := `{"role":"foreign","label":"hub-lab","expires_days":7}`
	resp, err := http.Post(srv.URL+"/api/agents/tokens", "application/json", strings.NewReader(tokenBody))
	if err != nil {
		t.Fatalf("token create: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token create: got %d: %s", resp.StatusCode, raw)
	}
	var tok struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &tok); err != nil {
		t.Fatalf("token decode: %v", err)
	}
	enrollBody := fmt.Sprintf(`{"name":"hub-lab","hostname":"node1","os":"linux","arch":"amd64","version":"1.0.0","caps":%s}`, agentTestJSON(t, caps))
	req, err := http.NewRequest("POST", srv.URL+"/api/agents/enroll", strings.NewReader(enrollBody))
	if err != nil {
		t.Fatalf("enroll req: %v", err)
	}
	req.Header.Set(agentEnrollHeader, tok.Token)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("enroll: %v", err)
	}
	defer resp2.Body.Close()
	raw2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("enroll: got %d: %s", resp2.StatusCode, raw2)
	}
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw2, &out); err != nil {
		t.Fatalf("enroll decode: %v", err)
	}
	return out.ID, out.Key
}

func agentTestJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// agentHubDial connects with agent credentials.
func agentHubDial(t *testing.T, srvURL, id, key string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	hdr := http.Header{}
	hdr.Set(agentIDHeader, id)
	hdr.Set(agentKeyHeader, key)
	return websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srvURL, "http")+"/api/agent-ws", hdr)
}

// agentHubHandshake dials, sends hello (the agent speaks first per the
// protocol) with the default caps and consumes the welcome reply.
func agentHubHandshake(t *testing.T, srvURL, id, key string) *websocket.Conn {
	t.Helper()
	return agentHubHandshakeCaps(t, srvURL, id, key, []string{agentCapMatrix, agentCapTraffic})
}

// agentHubHandshakeCaps is agentHubHandshake with explicit hello caps.
func agentHubHandshakeCaps(t *testing.T, srvURL, id, key string, caps []string) *websocket.Conn {
	t.Helper()
	ws, resp, err := agentHubDial(t, srvURL, id, key)
	if err != nil {
		t.Fatalf("dial: %v (resp %v)", err, resp)
	}
	hello := agentHello{ID: id, Version: "1.0.0", Hostname: "node1", OS: "linux", Arch: "amd64", Caps: caps}
	agentTestSendEnv(t, ws, agentMsgHello, hello)
	typ, data := agentTestReadEnv(t, ws)
	if typ != agentMsgWelcome {
		t.Fatalf("first message: got %q want welcome", typ)
	}
	var w agentWelcome
	if err := json.Unmarshal(data, &w); err != nil || !w.OK || w.Server != panelVersion {
		t.Fatalf("welcome wrong: %s (%v)", data, err)
	}
	return ws
}

func agentTestReadEnv(t *testing.T, ws *websocket.Conn) (string, json.RawMessage) {
	t.Helper()
	_, raw, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("read envelope: %v", err)
	}
	typ, data, err := agentDecode(raw)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	return typ, data
}

func agentTestSendEnv(t *testing.T, ws *websocket.Conn, typ string, payload any) {
	t.Helper()
	msg, err := agentEncode(typ, payload)
	if err != nil {
		t.Fatalf("encode %s: %v", typ, err)
	}
	if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatalf("send %s: %v", typ, err)
	}
}

// agentTestWaitFor polls cond until true or the 5s deadline.
func agentTestWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// agentHubPost runs one authenticated-less management POST and returns the
// status code and body.
func agentHubPost(t *testing.T, srv *httptest.Server, path, body string) (int, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func TestAgentHubHappyPath(t *testing.T) {
	withTempConfig(t)
	agentPersistEvery = 30 * time.Millisecond
	t.Cleanup(func() { agentPersistEvery = 2 * time.Second })

	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix, agentCapTraffic})
	ws := agentHubHandshake(t, srv.URL, id, key)
	defer ws.Close()

	// Hello metadata was persisted and the agent is marked online.
	agentTestWaitFor(t, "hello metadata", func() bool {
		a := agentTestRawAgent(t, id)
		return a.Status == agentStatusOnline && a.Version == "1.0.0" && a.Hostname == "node1" && a.OS == "linux"
	})
	if a := agentTestAgent(t, id); a.PublicIP == "" {
		t.Fatalf("public_ip not recorded on connect")
	}

	// Two heartbeats with cumulative counters: the controller derives Bps.
	hb1 := agentHeartbeat{TS: 1000, UptimeS: 10, State: &agentStateReport{
		Traffic: &agentTrafficReport{TS: 1000, Ifaces: []agentIfaceCounters{
			{Name: "eth0", Kind: "physical", Rx: 1000, Tx: 2000, Known: true},
		}},
	}}
	hb2 := agentHeartbeat{TS: 1010, UptimeS: 20, State: &agentStateReport{
		AppliedRevision: "", Traffic: &agentTrafficReport{TS: 1010, Ifaces: []agentIfaceCounters{
			{Name: "eth0", Kind: "physical", Rx: 6000, Tx: 8000, Known: true},
		}},
	}}
	agentTestSendEnv(t, ws, agentMsgHeartbeat, hb1)
	time.Sleep(60 * time.Millisecond)
	agentTestSendEnv(t, ws, agentMsgHeartbeat, hb2)
	agentTestWaitFor(t, "traffic rate", func() bool {
		a := agentTestRawAgent(t, id)
		r, ok := a.TrafficRate["eth0"]
		return ok && r.Kind == "physical"
	})
	if r := agentTestRawAgent(t, id).TrafficRate["eth0"]; r.RxBps < 499 || r.RxBps > 501 || r.TxBps < 599 || r.TxBps > 601 {
		t.Fatalf("rate math: got %+v want rx=500 tx=600", r)
	}
	if st := agentTestRawAgent(t, id).LastState; st == nil || st.Traffic == nil || len(st.Traffic.Ifaces) != 1 || st.Traffic.Ifaces[0].Rx != 6000 {
		t.Fatalf("last state not stored: %+v", agentTestRawAgent(t, id).LastState)
	}

	// Counter reset (reboot): current < previous is treated as a zero base.
	hb3 := agentHeartbeat{TS: 1020, UptimeS: 30, State: &agentStateReport{
		Traffic: &agentTrafficReport{TS: 1020, Ifaces: []agentIfaceCounters{
			{Name: "eth0", Kind: "physical", Rx: 100, Tx: 500, Known: true},
		}},
	}}
	agentTestSendEnv(t, ws, agentMsgHeartbeat, hb3)
	agentTestWaitFor(t, "counter reset rate", func() bool {
		r, ok := agentTestRawAgent(t, id).TrafficRate["eth0"]
		return ok && r.RxBps >= 9 && r.RxBps <= 12 // reset: 100 B over 10 s ≈ 10 B/s
	})
	if r := agentTestRawAgent(t, id).TrafficRate["eth0"]; r.TxBps < 49 || r.TxBps > 51 {
		t.Fatalf("tx rate after reset: got %+v want tx=50", r)
	}

	// Desired state posted while online arrives immediately.
	status, body := agentHubPost(t, srv, "/api/agents/"+id+"/desired",
		`{"matrix":{"instance":"matrix","action":"apply","hub":{"family":"ipv4","port":443,"addr":"198.51.100.1","ports":[443,8443],"token":"hmx1_PAIR"}}}`)
	if status != http.StatusOK {
		t.Fatalf("desired post: got %d: %s", status, body)
	}
	var dOut struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(body), &dOut); err != nil || dOut.Revision == "" {
		t.Fatalf("desired post decode: %s (%v)", body, err)
	}
	typ, data := agentTestReadEnv(t, ws)
	if typ != agentMsgDesired {
		t.Fatalf("expected desired push, got %q", typ)
	}
	var d agentDesired
	if err := json.Unmarshal(data, &d); err != nil || d.Revision != dOut.Revision || d.Matrix == nil || d.Matrix.Instance != "matrix" {
		t.Fatalf("delivered desired wrong: %s (%v)", data, err)
	}

	// Ack(ok): applied revision recorded, desired cleared, ack stored.
	agentTestSendEnv(t, ws, agentMsgAck, agentAck{Revision: d.Revision, OK: true})
	agentTestWaitFor(t, "ack applied", func() bool {
		a := agentTestAgent(t, id)
		return a.AppliedRevision == d.Revision && a.Desired == nil && a.LastAck != nil && a.LastAck.OK
	})

	// The ack observer hook fires for a matched ack (coordinator seam).
	observed := make(chan agentAck, 1)
	agentAckObserver = func(agentID string, ack agentAck) {
		if agentID == id {
			select {
			case observed <- ack:
			default:
			}
		}
	}
	t.Cleanup(func() { agentAckObserver = nil })

	// Ack(fail): desired is kept, last_error recorded.
	status, body = agentHubPost(t, srv, "/api/agents/"+id+"/desired",
		`{"matrix":{"instance":"matrix","action":"remove"}}`)
	if status != http.StatusOK {
		t.Fatalf("desired post 2: got %d: %s", status, body)
	}
	var dOut2 struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(body), &dOut2); err != nil || dOut2.Revision == "" {
		t.Fatalf("desired post 2 decode: %s", body)
	}
	typ, data = agentTestReadEnv(t, ws)
	if typ != agentMsgDesired {
		t.Fatalf("expected desired push 2, got %q", typ)
	}
	var d2 agentDesired
	if err := json.Unmarshal(data, &d2); err != nil || d2.Revision != dOut2.Revision {
		t.Fatalf("delivered desired 2 wrong: %s", data)
	}
	agentTestSendEnv(t, ws, agentMsgAck, agentAck{Revision: d2.Revision, OK: false, Error: "tinc refused the config"})
	select {
	case ack := <-observed:
		if ack.Revision != d2.Revision || ack.OK || ack.Error != "tinc refused the config" {
			t.Fatalf("observer got wrong ack: %+v", ack)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("ack observer never fired")
	}
	agentTestWaitFor(t, "failed ack kept desired", func() bool {
		a := agentTestAgent(t, id)
		return a.Desired != nil && a.Desired.Revision == d2.Revision && a.LastError == "tinc refused the config" &&
			a.LastAck != nil && !a.LastAck.OK
	})
}

func TestAgentHubDesiredDeliveryOffline(t *testing.T) {
	withTempConfig(t)
	agentPersistEvery = 30 * time.Millisecond
	t.Cleanup(func() { agentPersistEvery = 2 * time.Second })

	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})

	// Desired queued while the agent is offline: no error, stays pending.
	status, body := agentHubPost(t, srv, "/api/agents/"+id+"/desired",
		`{"matrix":{"instance":"matrix","action":"apply","bundle":"hmx1_bundle"}}`)
	if status != http.StatusOK {
		t.Fatalf("offline desired post: got %d: %s", status, body)
	}
	var dOut struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(body), &dOut); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if a := agentTestRawAgent(t, id); a.Desired == nil || a.Desired.Revision != dOut.Revision {
		t.Fatalf("pending desired not persisted: %+v", agentTestRawAgent(t, id).Desired)
	}

	// On the next connect it is pushed right after welcome.
	ws := agentHubHandshake(t, srv.URL, id, key)
	defer ws.Close()
	typ, data := agentTestReadEnv(t, ws)
	if typ != agentMsgDesired {
		t.Fatalf("post-welcome push: got %q want desired", typ)
	}
	var d agentDesired
	if err := json.Unmarshal(data, &d); err != nil || d.Revision != dOut.Revision || d.Matrix == nil || d.Matrix.Bundle != "hmx1_bundle" {
		t.Fatalf("post-welcome desired wrong: %s", data)
	}
}

func TestAgentHubCommand(t *testing.T) {
	withTempConfig(t)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapTraffic}) // deliberately no "matrix"

	// Unknown agent -> E-AGENT-03 / 404.
	status, body := agentHubPost(t, srv, "/api/agents/agt_ffffffffffff/command", `{"op":"diag.ping"}`)
	if status != http.StatusNotFound || !strings.Contains(body, "E-AGENT-03") {
		t.Fatalf("unknown agent: got %d %s", status, body)
	}
	// Known agent, offline -> E-AGENT-01.
	status, body = agentHubPost(t, srv, "/api/agents/"+id+"/command", `{"op":"diag.ping"}`)
	if status != http.StatusBadGateway || !strings.Contains(body, "E-AGENT-01") {
		t.Fatalf("offline agent: got %d %s", status, body)
	}

	ws := agentHubHandshakeCaps(t, srv.URL, id, key, []string{agentCapTraffic}) // hello must not re-grant matrix
	defer ws.Close()

	// Unknown op -> E-AGENT-02 / 400.
	status, body = agentHubPost(t, srv, "/api/agents/"+id+"/command", `{"op":"rm.rf"}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "E-AGENT-02") {
		t.Fatalf("unknown op: got %d %s", status, body)
	}
	// matrix.* without the matrix cap -> E-AGENT-02 / 400.
	status, body = agentHubPost(t, srv, "/api/agents/"+id+"/command", `{"op":"matrix.restart"}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "E-AGENT-02") {
		t.Fatalf("cap enforcement: got %d %s", status, body)
	}

	// Round trip: the command envelope reaches the agent, the result
	// returns to the HTTP caller.
	resCh := make(chan string, 1)
	go func() {
		code, b := agentHubPost(t, srv, "/api/agents/"+id+"/command", `{"op":"diag.ping","params":{"target":"10.0.0.1"}}`)
		resCh <- fmt.Sprintf("%d %s", code, b)
	}()
	typ, data := agentTestReadEnv(t, ws)
	if typ != agentMsgCommand {
		t.Fatalf("expected command, got %q", typ)
	}
	var cmd agentCommand
	if err := json.Unmarshal(data, &cmd); err != nil || cmd.Op != "diag.ping" || !strings.HasPrefix(cmd.ID, "cmd_") || cmd.Params["target"] != "10.0.0.1" {
		t.Fatalf("command envelope wrong: %s (%v)", data, err)
	}
	agentTestSendEnv(t, ws, agentMsgResult, agentCommandResult{ID: cmd.ID, OK: true, Output: "pong 10.0.0.1"})
	select {
	case got := <-resCh:
		var res agentCommandResult
		if !strings.HasPrefix(got, "200 ") || json.Unmarshal([]byte(strings.SplitN(got, " ", 2)[1]), &res) != nil ||
			!res.OK || res.Output != "pong 10.0.0.1" || res.ID != cmd.ID {
			t.Fatalf("command result wrong: %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("command round trip never completed")
	}

	// Slow agent: timeout surfaces as E-AGENT-01, and the pending entry is
	// cleaned up (the next command still works).
	agentCommandTimeout = 300 * time.Millisecond
	t.Cleanup(func() { agentCommandTimeout = 20 * time.Second })
	status, body = agentHubPost(t, srv, "/api/agents/"+id+"/command", `{"op":"diag.ping"}`)
	if status != http.StatusBadGateway || !strings.Contains(body, "E-AGENT-01") {
		t.Fatalf("slow agent: got %d %s", status, body)
	}
	agentCommandTimeout = 20 * time.Second
	typ, data = agentTestReadEnv(t, ws)
	if typ != agentMsgCommand {
		t.Fatalf("expected timed-out command envelope, got %q", typ)
	}
	var stale agentCommand
	_ = json.Unmarshal(data, &stale)
	// A late result for the timed-out command must be dropped harmlessly...
	agentTestSendEnv(t, ws, agentMsgResult, agentCommandResult{ID: stale.ID, OK: true, Output: "late"})
	// ...and the round trip still works afterwards.
	resCh2 := make(chan string, 1)
	go func() {
		code, b := agentHubPost(t, srv, "/api/agents/"+id+"/command", `{"op":"diag.ping"}`)
		resCh2 <- fmt.Sprintf("%d %s", code, b)
	}()
	typ, data = agentTestReadEnv(t, ws)
	if typ != agentMsgCommand {
		t.Fatalf("expected follow-up command, got %q", typ)
	}
	var cmd2 agentCommand
	_ = json.Unmarshal(data, &cmd2)
	agentTestSendEnv(t, ws, agentMsgResult, agentCommandResult{ID: cmd2.ID, OK: true, Output: "ok2"})
	agentTestWaitFor(t, "follow-up command result", func() bool {
		select {
		case got := <-resCh2:
			if !strings.HasPrefix(got, "200 ") {
				t.Fatalf("follow-up command failed: %s", got)
			}
			return true
		default:
			return false
		}
	})
}

func TestAgentHubRevokeKicks(t *testing.T) {
	withTempConfig(t)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	ws := agentHubHandshake(t, srv.URL, id, key)
	defer ws.Close()

	agentTestWaitFor(t, "agent online", func() bool { return agentHubOnline(id) })

	status, body := agentHubPost(t, srv, "/api/agents/"+id+"/revoke", "{}")
	if status != http.StatusOK {
		t.Fatalf("revoke: got %d: %s", status, body)
	}
	// The live session is closed.
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := ws.ReadMessage()
	if err == nil {
		t.Fatalf("expected the revoked connection to be closed")
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) && ce.Text != "revoked" {
		t.Fatalf("close reason: got %q want revoked", ce.Text)
	}
	agentTestWaitFor(t, "kick lands", func() bool { return !agentHubOnline(id) })
	a := agentTestRawAgent(t, id)
	if !a.Revoked || a.Status != agentStatusRevoked {
		t.Fatalf("revoked record wrong: %+v", a)
	}
	// The revoked agent cannot reconnect.
	if _, resp, derr := agentHubDial(t, srv.URL, id, key); derr == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked reconnect: err=%v resp=%v", derr, resp)
	}
}

func TestAgentHubSuperseded(t *testing.T) {
	withTempConfig(t)
	agentPersistEvery = 30 * time.Millisecond
	t.Cleanup(func() { agentPersistEvery = 2 * time.Second })

	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	ws1 := agentHubHandshake(t, srv.URL, id, key)
	defer ws1.Close()

	ws2 := agentHubHandshake(t, srv.URL, id, key)
	defer ws2.Close()

	// The first session is closed with the superseded reason.
	ws1.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, _, err := ws1.ReadMessage()
	if err == nil {
		t.Fatalf("expected the superseded connection to be closed")
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) && ce.Text != "superseded" {
		t.Fatalf("close reason: got %q want superseded", ce.Text)
	}
	// The replacement session is live and its liveness persists (the old
	// session's cleanup must not flip the agent offline).
	agentTestSendEnv(t, ws2, agentMsgHeartbeat, agentHeartbeat{TS: 500, UptimeS: 5})
	agentTestWaitFor(t, "replacement session stays online", func() bool {
		a := agentTestRawAgent(t, id)
		return agentHubOnline(id) && a.Status == agentStatusOnline && a.LastSeen >= 500
	})
}

func TestAgentHubAuthNegative(t *testing.T) {
	withTempConfig(t)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})

	// Wrong key -> 403 before the upgrade.
	if _, resp, err := agentHubDial(t, srv.URL, id, "deadbeef"+strings.Repeat("0", 40)); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong key: err=%v resp=%v", err, resp)
	}
	// Unknown agent id -> 403.
	if _, resp, err := agentHubDial(t, srv.URL, "agt_ffffffffffff", key); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown id: err=%v resp=%v", err, resp)
	}

	// Hello claiming a different agent identity: closed without welcome,
	// and the agent can dial again afterwards.
	ws, resp, err := agentHubDial(t, srv.URL, id, key)
	if err != nil {
		t.Fatalf("dial: %v (resp %v)", err, resp)
	}
	defer ws.Close()
	agentTestSendEnv(t, ws, agentMsgHello, agentHello{ID: "agt_ffffffffffff"})
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatalf("expected the mismatched-hello session to be closed")
	}
	if _, resp, derr := agentHubDial(t, srv.URL, id, key); derr != nil {
		t.Fatalf("dial after mismatched hello failed: %v (resp %v)", derr, resp)
	} else {
		_ = resp.Body.Close()
	}
}

func TestAgentHubRegisterRelay(t *testing.T) {
	withTempConfig(t)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	ws := agentHubHandshake(t, srv.URL, id, key)
	defer ws.Close()

	// No relay wired: an explicit, honest error result.
	agentTestSendEnv(t, ws, agentMsgRegister, agentRegisterMsg{Instance: "matrix", Token: "hmx1_T", Name: "node1", HostFile: "-----BEGIN"})
	typ, data := agentTestReadEnv(t, ws)
	if typ != agentMsgRegisterResult {
		t.Fatalf("expected register_result, got %q", typ)
	}
	var res agentRegisterResult
	if err := json.Unmarshal(data, &res); err != nil || res.OK || res.Error == "" {
		t.Fatalf("nil relay result wrong: %s (%v)", data, err)
	}

	// Relay wired: the message reaches the hook, the result comes back.
	type captured struct {
		reg  agentRegisterMsg
		from string
	}
	got := make(chan captured, 1)
	agentRegisterRelay = func(reg agentRegisterMsg, fromAgentID string) agentRegisterResult {
		select {
		case got <- captured{reg, fromAgentID}:
		default:
		}
		return agentRegisterResult{OK: true}
	}
	t.Cleanup(func() { agentRegisterRelay = nil })
	agentTestSendEnv(t, ws, agentMsgRegister, agentRegisterMsg{Instance: "matrix", Token: "hmx1_T", Name: "node1", HostFile: "-----BEGIN", Revision: "1700"})
	select {
	case c := <-got:
		if c.from != id || c.reg.Name != "node1" || c.reg.Token != "hmx1_T" || c.reg.Revision != "1700" {
			t.Fatalf("relay captured wrong message: %+v from=%s", c.reg, c.from)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("register relay hook never fired")
	}
	typ, data = agentTestReadEnv(t, ws)
	if typ != agentMsgRegisterResult {
		t.Fatalf("expected register_result 2, got %q", typ)
	}
	if err := json.Unmarshal(data, &res); err != nil || !res.OK {
		t.Fatalf("relay result wrong: %s (%v)", data, err)
	}
}
