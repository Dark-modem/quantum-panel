package panel

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Controller-side protocol security coverage beyond the hub's own tests:
// empty credentials, unsolicited command results, stale/duplicate acks,
// and command-ID uniqueness. Requirement: the session must survive every
// malformed-but-authenticated input without corrupting the registry.

func TestAgentHubEmptyCredentialsRejected(t *testing.T) {
	withTempConfig(t)
	t.Cleanup(meshStoreCloseForTest) // the enroll mirror opens quantum.db; close before the temp-dir race (BACKEND-24)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})

	for _, tc := range []struct{ id, key string }{{"", key}, {id, ""}, {"", ""}, {"   ", key}} {
		if _, resp, err := agentHubDial(t, srv.URL, tc.id, tc.key); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Fatalf("empty credential pair (id=%q key=%q) must be 403: err=%v resp=%v", tc.id, tc.key, err, resp)
		} else {
			_ = resp.Body.Close()
		}
	}
}

func TestAgentHubUnsolicitedResultSurvives(t *testing.T) {
	withTempConfig(t)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	ws := agentHubHandshakeCaps(t, srv.URL, id, key, []string{agentCapMatrix})
	defer ws.Close()

	// A result for a command the controller never issued (late, replayed,
	// or fabricated) must be dropped quietly: no panic, session alive.
	for _, cmdID := range []string{"cmd_doesnotexist", "", strings.Repeat("x", 500)} {
		agentTestSendEnv(t, ws, agentMsgResult, agentCommandResult{ID: cmdID, OK: true})
	}
	// The read loop must still answer a real round trip afterwards.
	resCh := make(chan string, 1)
	go func() {
		code, body := agentHubPost(t, srv, "/api/agents/"+id+"/command", `{"op":"diag.ping","params":{"target":"10.254.231.2"}}`)
		resCh <- strconv.Itoa(code) + "|" + body
	}()
	typ, data := agentTestReadEnv(t, ws)
	if typ != agentMsgCommand {
		t.Fatalf("session died after unsolicited results: got %q", typ)
	}
	var cmd agentCommand
	if err := json.Unmarshal(data, &cmd); err != nil {
		t.Fatal(err)
	}
	agentTestSendEnv(t, ws, agentMsgResult, agentCommandResult{ID: cmd.ID, OK: true, Output: "ping ok"})
	select {
	case got := <-resCh:
		if !strings.HasPrefix(got, "200|") {
			t.Fatalf("command round trip failed: %s", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("command round trip timed out")
	}
}

func TestAgentHubStaleAndDuplicateAcksIgnored(t *testing.T) {
	withTempConfig(t)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	ws := agentHubHandshakeCaps(t, srv.URL, id, key, []string{agentCapMatrix})
	defer ws.Close()

	// Push a pending desired, then ack a WRONG revision, then the right one.
	if err := agentHubDeliverDesired(id, agentDesired{Revision: "100", Matrix: &agentDesiredMatrix{Instance: "matrix", Action: "remove"}}); err != nil {
		t.Fatal(err)
	}
	agentTestReadEnv(t, ws) // the desired push

	agentTestSendEnv(t, ws, agentMsgAck, agentAck{Revision: "42", OK: true})   // unknown revision
	agentTestSendEnv(t, ws, agentMsgAck, agentAck{Revision: "100", OK: false}) // the real one, failed
	time.Sleep(300 * time.Millisecond)

	rec, ok := agentTunnelRegistryLookup(id)
	if !ok {
		t.Fatal("agent record vanished")
	}
	if rec.AppliedRevision != "" {
		t.Fatalf("failed ack must not set applied revision, got %q", rec.AppliedRevision)
	}

	// Duplicate success acks for the same revision: the first clears the
	// pending desired, the second must be ignored (no state resurrection).
	_ = agentHubDeliverDesired(id, agentDesired{Revision: "101", Matrix: &agentDesiredMatrix{Instance: "matrix", Action: "remove"}})
	agentTestReadEnv(t, ws)
	agentTestSendEnv(t, ws, agentMsgAck, agentAck{Revision: "101", OK: true})
	agentTestSendEnv(t, ws, agentMsgAck, agentAck{Revision: "101", OK: true})
	time.Sleep(300 * time.Millisecond)

	rec, _ = agentTunnelRegistryLookup(id)
	if rec.AppliedRevision != "101" || agentPendingDesired(id) != nil {
		t.Fatalf("applied revision / pending desired wrong after duplicate acks: applied=%q pending=%v", rec.AppliedRevision, agentPendingDesired(id))
	}
}

func TestAgentHubCommandIDsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := "cmd_" + randHex(4)
		if seen[id] {
			t.Fatalf("duplicate command id generated: %s", id)
		}
		seen[id] = true
	}
}

// A malformed envelope (bad JSON) must not kill the session.
func TestAgentHubGarbageEnvelopeSurvives(t *testing.T) {
	withTempConfig(t)
	srv := agentHubTestServer(t)
	id, key := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	ws := agentHubHandshakeCaps(t, srv.URL, id, key, []string{agentCapMatrix})
	defer ws.Close()

	_ = ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := ws.WriteMessage(websocket.TextMessage, []byte("this is not json")); err != nil {
		t.Fatalf("write garbage: %v", err)
	}
	// The server may close the connection on a decode error — allowed —
	// but it must not panic; a follow-up dial proves the hub is healthy.
	if ws2, resp, err := agentHubDial(t, srv.URL, id, key); err != nil {
		t.Fatalf("hub unusable after garbage: %v (resp %v)", err, resp)
	} else {
		_ = ws2.Close()
		_ = resp.Body.Close()
	}
}
