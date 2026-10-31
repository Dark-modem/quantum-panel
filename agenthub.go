package main

// Agent WebSocket hub: the controller side of the outbound agent session.
// An endpoint agent dials GET (agent ws route) with X-Agent-Id/X-Agent-Key,
// upgrades, sends hello, and stays connected — heartbeats in, desired
// states / commands out. The controller never dials the agent, so NAT'd
// endpoints work without any inbound management port.
//
// Concurrency rules:
//   - exactly one session goroutine per connection owns the read loop;
//     every write goes through agentConn.send under its write mutex.
//   - the hub map (agent ID -> current connection) is guarded by hubMu;
//     a newer connection supersedes (kicks) the older one.
//   - pending command result channels live on the connection and are
//     always drained on cleanup, so no waiter or channel leaks.
//   - registry file mutations all go through agentRegMu (agentreg.go).

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	agentWSReadLimit    = 256 << 10        // envelope cap per message
	agentWSReadTimeout  = 90 * time.Second // agents heartbeat every ~30s
	agentHelloTimeout   = 5 * time.Second  // hello must arrive right after the upgrade
	agentWriteTimeout   = 10 * time.Second
	agentRegisterBudget = 60 * time.Second // relay-to-hub-agent round trip
)

// agentPersistEvery batches heartbeat state persistence. A var so tests can
// shrink it and still exercise the batched path quickly.
var agentPersistEvery = 2 * time.Second

var agentHubUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		// Agents are not browsers and never send Origin. Any browser-origin
		// upgrade is rejected (there is no cookie auth here, but keep the
		// cross-site WebSocket hijacking surface closed, like the terminal).
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		return strings.EqualFold(CleanHost(origin), CleanHost(r.Host))
	},
}

// Hooks the coordinator (tunnel orchestration layer) wires at startup:
//   - agentAckObserver fires when an agent acks the pending desired
//     revision (e.g. to drive the other side of a new tunnel).
//   - agentRegisterRelay forwards a node agent's matrix register message to
//     its hub agent (as a matrix.register command) and returns the result.
//
// Both are nil in a stock panel; nil ack observer = no-op, nil relay =
// agents get an explicit "not available" error.
var (
	agentAckObserver   func(agentID string, ack agentAck)
	agentRegisterRelay func(reg agentRegisterMsg, fromAgentID string) agentRegisterResult
)

var (
	agentHubMu    sync.Mutex
	agentHubConns = map[string]*agentConn{} // agent ID -> current session
)

// agentConn is one live agent WebSocket session.
type agentConn struct {
	id        string
	ws        *websocket.Conn
	ip        string     // real client IP (X-Forwarded-For aware via ClientIP)
	writeMu   sync.Mutex // one writer at a time (single-writer discipline)
	closed    chan struct{}
	closeOnce sync.Once
	cmdMu     sync.Mutex
	pending   map[string]chan agentCommandResult // command ID -> waiter
	state     agentSessionState
}

// agentSessionState is the session-owned slice of the agent record. It
// lives here so heartbeats update memory immediately and persist to disk in
// batches, without clobbering registry fields other callers own (desired,
// acks, name...).
type agentSessionState struct {
	mu          sync.Mutex
	lastSeen    int64
	lastState   *agentStateReport
	prevTraffic *agentTrafficReport
	rates       map[string]agentIfaceRate
	lastPersist time.Time
}

// agentHubOnline reports whether the agent currently has a live session.
func agentHubOnline(id string) bool {
	agentHubMu.Lock()
	defer agentHubMu.Unlock()
	return agentHubConns[id] != nil
}

// handleAgentWS upgrades an authenticated agent connection and runs the
// session until the socket dies. No panel session auth: the agent key is
// the credential, verified (hashed, constant-time) BEFORE the upgrade.
func handleAgentWS(w http.ResponseWriter, r *http.Request) {
	ip := ClientIP(r)
	reject := func(why string) {
		LogSecurityAudit("agent_ws_rejected", "agent", ip, "path="+r.URL.Path+" "+why)
		http.Error(w, "agent authentication failed", http.StatusForbidden)
	}
	id := strings.TrimSpace(r.Header.Get(agentIDHeader))
	key := strings.TrimSpace(r.Header.Get(agentKeyHeader))
	if id == "" || key == "" {
		reject("missing credentials")
		return
	}
	agentRegMu.Lock()
	agents, _, err := agentRegistryLoad()
	var keyHash string
	known, revoked := false, false
	if err == nil {
		for i := range agents {
			if agents[i].ID == id {
				keyHash, known, revoked = agents[i].KeyHash, true, agents[i].Revoked
				break
			}
		}
	}
	agentRegMu.Unlock()
	if err != nil {
		log.Printf("[agents] cannot read agent registry: %v", err)
		reject("registry unavailable")
		return
	}
	// Constant-time hash comparison: the key never gets a timing oracle.
	if !known || !constantTimeHashEqual(keyHash, agentSha256Hex(key)) {
		reject("unknown agent or bad key")
		return
	}
	if revoked {
		reject("agent revoked")
		return
	}

	ws, err := agentHubUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already wrote the HTTP error
	}
	conn := &agentConn{
		id:      id,
		ws:      ws,
		ip:      ClientIP(r), // honors X-Forwarded-For from a trusted loopback proxy
		closed:  make(chan struct{}),
		pending: map[string]chan agentCommandResult{},
	}
	agentHubMu.Lock()
	if old := agentHubConns[id]; old != nil {
		old.shutdown("superseded")
	}
	agentHubConns[id] = conn
	agentHubMu.Unlock()

	defer conn.cleanup()
	conn.run()
}

// constantTimeHashEqual is a constant-time comparison of two lowercase hex
// sha256 digests.
func constantTimeHashEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	v := byte(0)
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// run performs the hello handshake, pushes any pending desired state, then
// serves the read loop until the connection dies.
func (c *agentConn) run() {
	c.ws.SetReadLimit(agentWSReadLimit)
	c.ws.SetReadDeadline(time.Now().Add(agentHelloTimeout))
	typ, data, err := c.readEnvelope()
	if err != nil {
		return
	}
	if typ != agentMsgHello {
		return
	}
	var hello agentHello
	if err := json.Unmarshal(data, &hello); err != nil || hello.ID != c.id {
		return // hello must claim the authenticated identity
	}
	if err := c.send(agentMsgWelcome, agentWelcome{OK: true, Server: panelVersion}); err != nil {
		return
	}
	c.applyHello(&hello)

	// Deliver whatever desired state was queued while the agent was offline.
	if d := agentPendingDesired(c.id); d != nil {
		_ = c.send(agentMsgDesired, *d)
	}

	for {
		c.ws.SetReadDeadline(time.Now().Add(agentWSReadTimeout))
		typ, data, err := c.readEnvelope()
		if err != nil {
			return
		}
		switch typ {
		case agentMsgHeartbeat:
			c.handleHeartbeat(data)
		case agentMsgAck:
			c.handleAck(data)
		case agentMsgResult:
			c.handleResult(data)
		case agentMsgRegister:
			c.handleRegister(data)
		default:
			// unknown types are ignored (forward compatibility)
		}
	}
}

// readEnvelope reads one protocol envelope; decode errors close the session.
func (c *agentConn) readEnvelope() (string, json.RawMessage, error) {
	_, raw, err := c.ws.ReadMessage()
	if err != nil {
		return "", nil, err
	}
	typ, data, err := agentDecode(raw)
	if err != nil {
		return "", nil, err
	}
	return typ, data, nil
}

// send is the only outbound write path (single-writer discipline).
func (c *agentConn) send(typ string, payload any) error {
	msg, err := agentEncode(typ, payload)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(agentWriteTimeout))
	return c.ws.WriteMessage(websocket.TextMessage, msg)
}

// shutdown kicks the session with a close reason ("superseded", "revoked").
// Safe to call from any goroutine, including while the session goroutine is
// blocked in ReadMessage (Close unblocks it; its cleanup then sees it is no
// longer the current session).
func (c *agentConn) shutdown(reason string) {
	c.closeOnce.Do(func() { close(c.closed) })
	msg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason)
	_ = c.ws.WriteControl(websocket.CloseMessage, msg, time.Now().Add(3*time.Second))
	_ = c.ws.Close()
}

// cleanup runs exactly once per session, from the session goroutine.
func (c *agentConn) cleanup() {
	c.closeOnce.Do(func() { close(c.closed) })
	// Fail every pending command waiter: the channels are closed (waiters
	// receive zero,false) and removed so late results find nothing to send on.
	c.cmdMu.Lock()
	pending := c.pending
	c.pending = map[string]chan agentCommandResult{}
	c.cmdMu.Unlock()
	for _, ch := range pending {
		close(ch)
	}
	_ = c.ws.Close()

	agentHubMu.Lock()
	current := agentHubConns[c.id] == c
	if current {
		delete(agentHubConns, c.id)
	}
	agentHubMu.Unlock()
	if current {
		if err := agentRegistryUpdate(c.id, func(rec *agentRecord) {
			if !rec.Revoked {
				rec.Status = agentStatusOffline
			}
		}); err != nil {
			log.Printf("[agents] cannot mark %s offline: %v", c.id, err)
		}
	}
}

// applyHello persists the hello-declared metadata and marks the agent online.
func (c *agentConn) applyHello(h *agentHello) {
	now := time.Now().Unix()
	c.state.mu.Lock()
	c.state.lastSeen = now
	c.state.lastPersist = time.Time{} // force the immediate online persist below
	c.state.mu.Unlock()
	err := agentRegistryUpdate(c.id, func(rec *agentRecord) {
		rec.Status = agentStatusOnline
		rec.PublicIP = c.remoteIP()
		rec.LastSeen = now
		if h.Version != "" {
			rec.Version = agentValidText(h.Version, 64)
		}
		if h.Hostname != "" {
			rec.Hostname = agentValidText(h.Hostname, 255)
		}
		if h.OS != "" {
			rec.OS = agentValidText(h.OS, 64)
		}
		if h.Arch != "" {
			rec.Arch = agentValidText(h.Arch, 64)
		}
		if len(h.Caps) > 0 {
			rec.Caps = agentValidCaps(h.Caps)
		}
	})
	if err != nil {
		log.Printf("[agents] cannot mark %s online: %v", c.id, err)
	}
	c.state.mu.Lock()
	c.state.lastPersist = time.Now()
	c.state.mu.Unlock()
}

func (c *agentConn) remoteIP() string {
	if c.ip != "" {
		return c.ip
	}
	addr := c.ws.RemoteAddr().String()
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}

// handleHeartbeat records liveness and the latest state; traffic rates are
// derived from consecutive cumulative counters. Disk persistence is batched
// (at most one write per agentPersistEvery).
func (c *agentConn) handleHeartbeat(data json.RawMessage) {
	var hb agentHeartbeat
	if json.Unmarshal(data, &hb) != nil {
		return
	}
	now := time.Now()
	c.state.mu.Lock()
	c.state.lastSeen = now.Unix()
	if hb.State != nil {
		c.state.lastState = hb.State
		if hb.State.Traffic != nil {
			c.state.updateTrafficLocked(hb.State.Traffic)
		}
	}
	due := now.Sub(c.state.lastPersist) >= agentPersistEvery
	c.state.mu.Unlock()
	if due {
		c.persistSessionState()
	}
}

// updateTrafficLocked derives per-interface Bps from two consecutive
// cumulative counter reports. A counter reset (current < previous, e.g.
// reboot) is treated as a zero base. Caller holds state.mu.
func (s *agentSessionState) updateTrafficLocked(cur *agentTrafficReport) {
	if s.prevTraffic != nil {
		elapsed := cur.TS - s.prevTraffic.TS
		if elapsed > 0 {
			if s.rates == nil {
				s.rates = map[string]agentIfaceRate{}
			}
			prevByName := make(map[string]agentIfaceCounters, len(s.prevTraffic.Ifaces))
			for _, p := range s.prevTraffic.Ifaces {
				prevByName[p.Name] = p
			}
			for _, c := range cur.Ifaces {
				p, ok := prevByName[c.Name]
				if !ok || !c.Known {
					continue // unreadable counters must never invent a rate
				}
				rxDelta, txDelta := uint64(0), uint64(0)
				if c.Rx >= p.Rx {
					rxDelta = c.Rx - p.Rx
				} else {
					rxDelta = c.Rx // counter reset: previous is the zero base
				}
				if c.Tx >= p.Tx {
					txDelta = c.Tx - p.Tx
				} else {
					txDelta = c.Tx
				}
				s.rates[c.Name] = agentIfaceRate{
					Name: c.Name, Kind: c.Kind,
					RxBps: float64(rxDelta) / float64(elapsed),
					TxBps: float64(txDelta) / float64(elapsed),
					At:    time.Now().Unix(),
				}
			}
		}
	}
	s.prevTraffic = cur
}

// persistSessionState writes the session-owned fields onto the stored
// record (freshly loaded, so concurrent registry edits are preserved).
func (c *agentConn) persistSessionState() {
	c.state.mu.Lock()
	snap := agentSessionState{
		lastSeen:    c.state.lastSeen,
		lastState:   c.state.lastState,
		prevTraffic: c.state.prevTraffic,
		rates:       c.state.rates,
	}
	c.state.lastPersist = time.Now()
	c.state.mu.Unlock()
	if err := agentRegistryUpdate(c.id, func(rec *agentRecord) {
		if rec.Revoked {
			return // revoked mid-session: keep the revoked status
		}
		rec.Status = agentStatusOnline
		rec.LastSeen = snap.lastSeen
		rec.LastState = snap.lastState
		rec.PrevTraffic = snap.prevTraffic
		if len(snap.rates) > 0 {
			rec.TrafficRate = snap.rates
		} else {
			rec.TrafficRate = nil
		}
	}); err != nil {
		log.Printf("[agents] cannot persist session state for %s: %v", c.id, err)
	}
}

// handleAck resolves the pending desired revision.
func (c *agentConn) handleAck(data json.RawMessage) {
	var ack agentAck
	if json.Unmarshal(data, &ack) != nil || ack.Revision == "" {
		return
	}
	matched := false
	err := agentRegistryUpdate(c.id, func(rec *agentRecord) {
		if rec.Desired == nil || rec.Desired.Revision != ack.Revision {
			return // stale or duplicate ack: ignore
		}
		rec.LastAck = &agentAckRecord{
			Revision: ack.Revision, OK: ack.OK, Error: ack.Error, At: time.Now().Unix(),
		}
		if ack.OK {
			rec.AppliedRevision = ack.Revision
			rec.Desired = nil
			rec.LastError = ""
		} else {
			rec.LastError = ack.Error
		}
		matched = true
	})
	if err != nil {
		log.Printf("[agents] cannot record ack for %s: %v", c.id, err)
		return
	}
	if matched && agentAckObserver != nil {
		hook := agentAckObserver
		ack := ack
		go hook(c.id, ack)
	}
}

// handleResult completes one pending command round trip. Unknown/late IDs
// (already timed out, or the session died) are dropped.
func (c *agentConn) handleResult(data json.RawMessage) {
	var res agentCommandResult
	if json.Unmarshal(data, &res) != nil || res.ID == "" {
		return
	}
	c.cmdMu.Lock()
	ch := c.pending[res.ID]
	delete(c.pending, res.ID)
	c.cmdMu.Unlock()
	if ch != nil {
		ch <- res // buffered(1) and just removed: always delivers
	}
}

// handleRegister relays a node's matrix register message to its hub agent
// via the coordinator's hook, synchronously within a 60s budget.
func (c *agentConn) handleRegister(data json.RawMessage) {
	var reg agentRegisterMsg
	if json.Unmarshal(data, &reg) != nil || reg.Token == "" || reg.Name == "" {
		_ = c.send(agentMsgRegisterResult, agentRegisterResult{OK: false, Error: "invalid register message"})
		return
	}
	hook := agentRegisterRelay
	if hook == nil {
		_ = c.send(agentMsgRegisterResult, agentRegisterResult{OK: false, Error: "register relay not available"})
		return
	}
	resCh := make(chan agentRegisterResult, 1)
	go func() {
		defer func() {
			if recover() != nil {
				select {
				case resCh <- agentRegisterResult{OK: false, Error: "register relay failed"}:
				default:
				}
			}
		}()
		resCh <- hook(reg, c.id)
	}()
	timer := time.NewTimer(agentRegisterBudget)
	defer timer.Stop()
	select {
	case res := <-resCh:
		_ = c.send(agentMsgRegisterResult, res)
	case <-timer.C:
		_ = c.send(agentMsgRegisterResult, agentRegisterResult{OK: false, Error: "register relay timed out"})
	case <-c.closed:
		// session died mid-relay: nothing to answer
	}
}

// ---- outbound API (HTTP handlers + coordinator) ----

// agentPendingDesired returns a copy of the agent's queued desired state,
// or nil. It is what a freshly connected agent receives after welcome.
func agentPendingDesired(id string) *agentDesired {
	agentRegMu.Lock()
	defer agentRegMu.Unlock()
	agents, _, err := agentRegistryLoad()
	if err != nil {
		return nil
	}
	for i := range agents {
		if agents[i].ID == id && agents[i].Desired != nil {
			d := *agents[i].Desired
			return &d
		}
	}
	return nil
}

// agentErrPendingTeardown is returned when a delivery would silently
// overwrite a pending, unacked teardown desired (audit F2): the old Matrix
// instance would then never be torn down. Callers should surface the error
// and retry once the teardown acks; reconcileAgentTunnels re-drives such
// work on the next controller start.
var agentErrPendingTeardown = errors.New("agent has a pending teardown desired; retry after it is acked")

// agentHubDeliverDesired persists the desired state (overwriting any pending
// one) and pushes it to a live session immediately. An offline agent is not
// an error: it delivers on next connect.
func agentHubDeliverDesired(id string, d agentDesired) error {
	agentRegMu.Lock()
	agents, tokens, err := agentRegistryLoad()
	if err != nil {
		agentRegMu.Unlock()
		return err
	}
	found := false
	for i := range agents {
		if agents[i].ID == id {
			// Teardown guard (audit F2): a pending, unacked remove must
			// not be silently replaced by new work. Applies may supersede
			// applies (last write wins), a remove supersedes an apply
			// (delete during a mid-flight provisioning), and a
			// same-instance remove refreshes a pending remove; anything
			// else waits for the teardown to ack.
			if pd := agents[i].Desired; pd != nil && pd.Matrix != nil && d.Matrix != nil {
				if pd.Matrix.Action == "remove" &&
					(d.Matrix.Action != "remove" || pd.Matrix.Instance != d.Matrix.Instance) {
					agentRegMu.Unlock()
					return agentErrPendingTeardown
				}
			}
			agents[i].Desired = &d
			found = true
			break
		}
	}
	if !found {
		agentRegMu.Unlock()
		return agentErrUnknownAgent
	}
	if err := saveAgentRegistry(agents, tokens); err != nil {
		agentRegMu.Unlock()
		return err
	}
	agentRegMu.Unlock()

	agentHubMu.Lock()
	conn := agentHubConns[id]
	agentHubMu.Unlock()
	if conn != nil {
		_ = conn.send(agentMsgDesired, d)
	}
	return nil
}

// agentHubCommand sends one allowlisted command and waits for the agent's
// result (or the timeout). Offline agents fail immediately; the pending
// entry is always cleaned up, whatever the outcome.
func agentHubCommand(id, op string, params map[string]string, timeout time.Duration) (agentCommandResult, error) {
	agentHubMu.Lock()
	conn := agentHubConns[id]
	agentHubMu.Unlock()
	if conn == nil {
		return agentCommandResult{}, errors.New("agent is offline")
	}
	cmdID := "cmd_" + randHex(4)
	ch := make(chan agentCommandResult, 1)
	conn.cmdMu.Lock()
	conn.pending[cmdID] = ch
	conn.cmdMu.Unlock()

	if err := conn.send(agentMsgCommand, agentCommand{ID: cmdID, Op: op, Params: params}); err != nil {
		conn.takePending(cmdID)
		return agentCommandResult{}, errors.New("agent connection failed")
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res, ok := <-ch:
		if !ok {
			return agentCommandResult{}, errors.New("agent connection closed")
		}
		return res, nil
	case <-timer.C:
		conn.takePending(cmdID)
		return agentCommandResult{}, errors.New("agent command timed out")
	case <-conn.closed:
		conn.takePending(cmdID)
		return agentCommandResult{}, errors.New("agent connection closed")
	}
}

// takePending removes and returns one pending command channel.
func (c *agentConn) takePending(cmdID string) chan agentCommandResult {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()
	ch := c.pending[cmdID]
	delete(c.pending, cmdID)
	return ch
}

// agentHubKick closes an agent's live session (if any) with a reason.
func agentHubKick(id, reason string) {
	agentHubMu.Lock()
	conn := agentHubConns[id]
	agentHubMu.Unlock()
	if conn != nil {
		conn.shutdown(reason)
	}
}
