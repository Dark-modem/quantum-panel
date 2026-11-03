package main

// gre-panel agent — the headless endpoint agent runtime. One outbound
// WebSocket to the central controller (the panel), local Matrix management
// over the existing matrix.go primitives, light health probes and traffic
// counters. No HTTP listeners, no dashboard, no shell surface: the agent
// only ever runs the allowlisted protocol operations.
//
// Layout of the agent files:
//
//	agent.go            CLI dispatch, runtime/reconnect loop, WS session
//	agent_state.go      agent.json + agent-state.json persistence
//	agent_matrix_local.go  desired-state application + allowlisted commands
//	agent_health.go     rolling 60s tunnel health window
//	agent_traffic.go    per-interface traffic counters
//	agent_detect.go     read-only PasarGuard Node detection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	agentWSPath     = "/api/agent/ws"
	agentEnrollPath = "/api/agent/enroll"

	agentUnitName    = "gre-agent"
	agentServiceUnit = "gre-agent.service"

	agentDialTimeout     = 15 * time.Second
	agentWelcomeTimeout  = 10 * time.Second // waiting for the controller welcome after hello
	agentBackoffCeiling  = 120 * time.Second
	agentHeartbeatPeriod = 30 * time.Second
)

// agentBackoffBase is the reconnect backoff start (doubles up to the
// ceiling); a var so tests can shrink it.
var agentBackoffBase = 2 * time.Second

// agentOpsWG tracks the session's async work (desired application, command
// execution, heartbeat loop, reconcile) so tests can wait for it to drain
// before rotating the configDir/matrix seams those goroutines read.
var agentOpsWG sync.WaitGroup

// AgentOpsWait blocks until the agent runtime's in-flight async operations
// have finished. Test-only drain aid; in production the process exits with
// the session.
func AgentOpsWait() { agentOpsWG.Wait() }

// agentWS is the WebSocket surface the runtime needs; *websocket.Conn
// satisfies it and tests can fake it.
type agentWS interface {
	WriteMessage(messageType int, data []byte) error
	ReadMessage() (messageType int, p []byte, err error)
	Close() error
}

// agentDialer dials the controller and returns a live connection. Seam:
// tests inject httptest servers here.
var agentDialer = func(ident agentIdentity) (agentWS, error) {
	return dialAgentWS(ident)
}

// agentWSURL builds the outbound WebSocket URL from the enrollment record:
// server "https://host:7443" + base_path "panel" → wss://host:7443/panel/api/agent/ws
// The installer contract passes --server WITH the base path already included
// ("https://host:7443[/base]"), so the controller-returned base_path is only
// appended when the server URL does not already end with it — appending it
// twice produced host/base/base/api/agent/ws and the WS 404'd.
func agentWSURL(ident agentIdentity) string {
	base := strings.TrimRight(strings.TrimSpace(ident.Server), "/")
	if !strings.Contains(base, "://") {
		base = "ws://" + base
	}
	if bp := strings.Trim(ident.BasePath, "/"); bp != "" {
		if !strings.HasSuffix(strings.ToLower(base), "/"+strings.ToLower(bp)) {
			base += "/" + bp
		}
	}
	u := base + agentWSPath
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return u
}

// agentSPKIPin is the TOFU pin of a leaf certificate: SHA-256 over the DER
// SubjectPublicKeyInfo (same shape as the peer-link pin in peer.go).
func agentSPKIPin(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// agentCheckTLSPin applies trust-on-first-use to the controller certificate:
// the first successful connect stores the pin in agent.json; any later
// mismatch aborts with a loud error (possible MITM).
func agentCheckTLSPin(ident *agentIdentity, got string) error {
	if got == "" {
		return nil
	}
	if ident.TLSPin == "" {
		ident.TLSPin = got
		if err := agentSaveIdentity(*ident); err != nil {
			log.Printf("[agent] cannot persist controller TLS pin: %v", err)
		}
		return nil
	}
	if !strings.EqualFold(ident.TLSPin, got) {
		return fmt.Errorf("controller TLS certificate changed: pinned %s, got %s (possible MITM — refusing to connect; verify the controller and clear tls_pin in agent.json to re-pin)", ident.TLSPin, got)
	}
	return nil
}

// dialAgentWS dials <server><base_path>/api/agent/ws with the agent auth
// headers, using standard certificate verification plus TOFU pinning of the
// leaf certificate as extra defense (the controller may terminate TLS itself
// or behind nginx with real certificates).
func dialAgentWS(ident agentIdentity) (agentWS, error) {
	hdr := http.Header{}
	hdr.Set(agentIDHeader, ident.ID)
	hdr.Set(agentKeyHeader, ident.Key)
	d := &websocket.Dialer{
		HandshakeTimeout: agentDialTimeout,
		TLSClientConfig:  &tls.Config{MinVersion: tls.VersionTLS12},
	}
	ws, resp, err := d.Dial(agentWSURL(ident), hdr)
	if err != nil {
		return nil, err
	}
	if resp != nil && resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		if err := agentCheckTLSPin(&ident, agentSPKIPin(resp.TLS.PeerCertificates[0])); err != nil {
			_ = ws.Close()
			return nil, err
		}
	}
	return ws, nil
}

// agentRuntime owns the reconnect loop for one enrollment.
type agentRuntime struct {
	ident agentIdentity
	start time.Time
}

// agentJitter spreads a backoff by ±20% so fleets of agents never retry in
// lockstep.
var agentRand = rand.New(rand.NewSource(time.Now().UnixNano()))

func agentJitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*agentRand.Float64()))
}

// loop keeps exactly one controller connection alive: dial, serve until the
// session ends, then reconnect with exponential backoff 2s → 120s (±20%
// jitter). A refused welcome (revoked enrollment) exits 1 — the systemd
// supervisor restarts the process, but the enrollment itself is dead.
func (rt *agentRuntime) loop(ctx context.Context) int {
	backoff := agentBackoffBase
	for {
		if ctx.Err() != nil {
			return 0
		}
		ws, err := agentDialer(rt.ident)
		if err == nil {
			s := newAgentSession(rt, ws)
			serr := s.serve(ctx)
			if s.fatal {
				rt.recordWS(false, s.err().Error())
				return 1
			}
			if ctx.Err() != nil || serr == nil {
				rt.recordWS(false, "")
				return 0
			}
			if s.welcomed {
				backoff = agentBackoffBase // a full handshake happened; reset
			}
			err = serr
		}
		rt.recordWS(false, err.Error())
		log.Printf("[agent] %v; retrying in ~%s", err, backoff)
		select {
		case <-ctx.Done():
			return 0
		case <-time.After(agentJitter(backoff)):
		}
		if backoff < agentBackoffCeiling {
			backoff *= 2
			if backoff > agentBackoffCeiling {
				backoff = agentBackoffCeiling
			}
		}
	}
}

// recordWS journals the connection status so `agent status` (another
// process) can show it.
func (rt *agentRuntime) recordWS(connected bool, lastErr string) {
	_ = agentMutateState(func(st *agentPersistedState) {
		st.WS = &agentWSStatus{Connected: connected, LastError: lastErr, TS: time.Now().Unix(), StartedAt: rt.start.Unix()}
	})
}

// agentSession is one live controller connection.
type agentSession struct {
	rt    *agentRuntime
	ws    agentWS
	ident agentIdentity

	wmu sync.Mutex // serializes WS writes

	mu        sync.Mutex
	waiters   map[string][]chan json.RawMessage
	unclaimed map[string][]json.RawMessage

	done     chan struct{}
	doneOnce sync.Once

	errMu   sync.Mutex
	sessErr error

	welcomed bool
	fatal    bool // welcome said ok:false — enrollment dead, do not retry
}

func newAgentSession(rt *agentRuntime, ws agentWS) *agentSession {
	return &agentSession{
		rt: rt, ws: ws, ident: rt.ident,
		waiters:   map[string][]chan json.RawMessage{},
		unclaimed: map[string][]json.RawMessage{},
		done:      make(chan struct{}),
	}
}

// fail records the first session error (the cause the reconnect loop should
// report) and tears the connection down.
func (s *agentSession) fail(err error) {
	s.errMu.Lock()
	if s.sessErr == nil {
		s.sessErr = err
	}
	s.errMu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
	_ = s.ws.Close()
}

// failFatal overwrites whatever error raced in first with the decisive cause
// (e.g. a refused welcome) so it is journaled, then tears the session down.
func (s *agentSession) failFatal(err error) {
	s.errMu.Lock()
	s.sessErr = err
	s.errMu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
	_ = s.ws.Close()
}

func (s *agentSession) err() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.sessErr
}

func (s *agentSession) send(typ string, payload any) error {
	b, err := agentEncode(typ, payload)
	if err != nil {
		return err
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.ws.WriteMessage(websocket.TextMessage, b)
}

// waitFor returns the next message of the given type, or times out. Used for
// the welcome handshake and the node register round-trip.
func (s *agentSession) waitFor(typ string, timeout time.Duration) (json.RawMessage, error) {
	s.mu.Lock()
	if msgs := s.unclaimed[typ]; len(msgs) > 0 {
		raw := msgs[0]
		if len(msgs) == 1 {
			delete(s.unclaimed, typ)
		} else {
			s.unclaimed[typ] = msgs[1:]
		}
		s.mu.Unlock()
		return raw, nil
	}
	ch := make(chan json.RawMessage, 1)
	s.waiters[typ] = append(s.waiters[typ], ch)
	s.mu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case raw := <-ch:
		return raw, nil
	case <-t.C:
		s.mu.Lock()
		for i, c := range s.waiters[typ] {
			if c == ch {
				s.waiters[typ] = append(s.waiters[typ][:i], s.waiters[typ][i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		return nil, fmt.Errorf("timed out waiting for %s after %s", typ, timeout)
	case <-s.done:
		return nil, fmt.Errorf("session closed while waiting for %s", typ)
	}
}

// deliver routes an incoming message to a waiter, or parks it briefly (one
// slot per type) so a waiter that subscribes after arrival still sees it.
func (s *agentSession) deliver(typ string, raw json.RawMessage) {
	s.mu.Lock()
	if chans := s.waiters[typ]; len(chans) > 0 {
		ch := chans[0]
		s.waiters[typ] = chans[1:]
		s.mu.Unlock()
		ch <- raw
		return
	}
	if len(s.unclaimed[typ]) < 8 {
		s.unclaimed[typ] = append(s.unclaimed[typ], raw)
	}
	s.mu.Unlock()
}

// serve runs one connection: hello/welcome handshake, heartbeat loop and the
// receive loop (which runs in its own goroutine). It returns when the
// session ends; a nil error means graceful shutdown.
func (s *agentSession) serve(ctx context.Context) error {
	go s.readLoop()
	defer func() {
		s.doneOnce.Do(func() { close(s.done) })
		_ = s.ws.Close()
	}()
	// Abort any in-flight wait (handshake, register round-trip) when the
	// runtime is shutting down.
	go func() {
		select {
		case <-ctx.Done():
			s.fail(nil)
		case <-s.done:
		}
	}()

	hostname, _ := os.Hostname()
	hello := agentHello{
		ID:       s.ident.ID,
		Version:  panelVersion,
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Caps:     []string{agentCapMatrix, agentCapTraffic, agentCapHealth, agentCapNodeDetect},
	}
	if err := s.send(agentMsgHello, hello); err != nil {
		s.fail(err)
		return s.err()
	}
	raw, err := s.waitFor(agentMsgWelcome, agentWelcomeTimeout)
	if err != nil {
		s.fail(err)
		return s.err()
	}
	var w agentWelcome
	if uerr := json.Unmarshal(raw, &w); uerr != nil {
		s.fail(uerr)
		return s.err()
	}
	if !w.OK {
		// Enrollment is dead (revoked / unknown agent): supervisor may
		// restart us, but re-enrollment is the fix.
		log.Printf("[agent] controller refused the session: %s (enrollment is dead; re-enroll)", w.Error)
		s.welcomed = true
		s.fatal = true
		s.failFatal(fmt.Errorf("controller refused the session: %s", w.Error))
		return s.err()
	}
	s.welcomed = true
	log.Printf("[agent] connected to %s (controller %s)", agentWSURL(s.ident), w.Server)
	s.rt.recordWS(true, "")

	// Finish whatever the last session left pending and ack it.
	agentOpsWG.Add(1)
	go func() {
		defer agentOpsWG.Done()
		agentReconcilePending(s)
	}()

	agentOpsWG.Add(1)
	go func() {
		defer agentOpsWG.Done()
		s.heartbeatLoop(ctx)
	}()

	select {
	case <-s.done:
	case <-ctx.Done():
		s.fail(nil) // graceful: SIGTERM/SIGINT
	}
	return s.err() // mutex-read: fail/failFatal write sessErr from other goroutines
}

// readLoop reads and routes every incoming message. Desired and command work
// runs in its own goroutines so the register round-trip and multi-second
// commands never block the protocol. Unknown types are ignored (forward
// compat); protocol/JSON errors drop the connection.
func (s *agentSession) readLoop() {
	for {
		_, raw, err := s.ws.ReadMessage()
		if err != nil {
			s.fail(err)
			return
		}
		typ, data, derr := agentDecode(raw)
		if derr != nil {
			log.Printf("[agent] dropping connection: %v", derr)
			s.fail(derr)
			return
		}
		switch typ {
		case agentMsgDesired:
			var d agentDesired
			if uerr := json.Unmarshal(data, &d); uerr != nil {
				log.Printf("[agent] malformed desired: %v", uerr)
				s.fail(uerr)
				return
			}
			go s.handleDesired(d)
		case agentMsgCommand:
			var c agentCommand
			if uerr := json.Unmarshal(data, &c); uerr != nil {
				log.Printf("[agent] malformed command: %v", uerr)
				s.fail(uerr)
				return
			}
			go s.handleCommand(c)
		case agentMsgWelcome:
			s.deliver(typ, data)
		default:
			// register_result and any unknown/forward-compat type: feed
			// waiters, park otherwise — never act on it here.
			s.deliver(typ, data)
		}
	}
}

// handleDesired journals the desired as pending (so a crash mid-apply is
// retried on the next boot without the controller), applies it, sends the
// ack, journals the outcome and pushes a fresh heartbeat.
func (s *agentSession) handleDesired(d agentDesired) {
	if err := agentMutateState(func(st *agentPersistedState) {
		dd := d
		st.Desired = &dd
		st.DesiredStatus = "pending"
	}); err != nil {
		log.Printf("[agent] cannot persist desired: %v", err)
	}
	agentMu.Lock()
	ack := agentApplyDesired(s, &d)
	agentMu.Unlock()
	if err := s.send(agentMsgAck, ack); err != nil {
		log.Printf("[agent] ack send failed: %v", err)
		s.fail(err)
		return // state stays pending; the next session reconciles + acks
	}
	agentRecordAck(d.Revision, ack)
	s.sendHeartbeat() // push fresh state so the controller sees the result now
}

// handleCommand executes one allowlisted op and echoes the command ID.
func (s *agentSession) handleCommand(c agentCommand) {
	agentMu.Lock()
	res := agentRunCommand(c)
	agentMu.Unlock()
	if err := s.send(agentMsgResult, res); err != nil {
		log.Printf("[agent] result send failed: %v", err)
		s.fail(err)
	}
}

// heartbeatLoop sends one heartbeat immediately (so a freshly connected
// controller gets the full picture at once), then every 30s.
func (s *agentSession) heartbeatLoop(ctx context.Context) {
	s.sendHeartbeat()
	t := time.NewTicker(agentHeartbeatPeriod)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-t.C:
			s.sendHeartbeat()
		}
	}
}

func (s *agentSession) sendHeartbeat() {
	hb := agentHeartbeat{
		TS:      time.Now().Unix(),
		UptimeS: int64(time.Since(s.rt.start).Seconds()),
		State:   agentBuildStateReport(),
	}
	if err := s.send(agentMsgHeartbeat, hb); err != nil {
		log.Printf("[agent] heartbeat failed: %v", err)
		s.fail(err)
	}
}

// agentRun is the `agent run` daemon: load the enrollment, reconcile what
// the last session left pending (controller or not), then keep one outbound
// connection alive. SIGTERM/SIGINT shut down gracefully with exit 0.
func agentRun() int {
	ident, ok := agentLoadIdentity()
	if !ok {
		fmt.Fprintf(os.Stderr, "agent: no enrollment found in %s — run `gre-panel agent enroll --server <url> --token <token>` first\n", configDir)
		return 2
	}
	log.Printf("[agent] starting agent %s for %s (v%s)", ident.ID, agentWSURL(ident), panelVersion)
	agentCurrentIdentity = ident
	agentUpdateCheckBootRollback() // restore the previous binary after crash-loops
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	rt := &agentRuntime{ident: ident, start: time.Now()}
	agentReconcilePending(nil) // self-healing before any controller contact
	go agentHealthLoop(ctx)
	go func() { // clear the update rollback journal once the boot is stable
		for ctx.Err() == nil {
			agentUpdateClearMarkerIfStable(rt.start)
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
			}
		}
	}()
	code := rt.loop(ctx)
	log.Printf("[agent] stopped (exit %d)", code)
	return code
}

// --- enrollment -------------------------------------------------------------

type agentEnrollRequest struct {
	Name     string   `json:"name"`
	Hostname string   `json:"hostname"`
	OS       string   `json:"os"`
	Arch     string   `json:"arch"`
	Version  string   `json:"version"`
	Caps     []string `json:"caps"`
}

type agentEnrollResponse struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	BasePath string `json:"base_path"`
}

func agentCaps() []string {
	return []string{agentCapMatrix, agentCapTraffic, agentCapHealth, agentCapNodeDetect}
}

// agentEnroll implements `agent enroll --server <url> --token <token>`:
// POST <server>/api/agent/enroll and persist the identity to agent.json.
func agentEnroll(args []string) int {
	fs := flag.NewFlagSet("agent enroll", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	server := fs.String("server", "", "controller base URL, e.g. https://controller.example:7443[/base]")
	token := fs.String("token", "", "enrollment token issued by the controller")
	name := fs.String("name", "", "display name for this endpoint")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	*server = strings.TrimRight(strings.TrimSpace(*server), "/")
	*token = strings.TrimSpace(*token)
	if *server == "" || *token == "" {
		fmt.Fprintln(os.Stderr, "usage: gre-panel agent enroll --server <url> --token <token> [--name <name>]")
		return 1
	}
	hostname, _ := os.Hostname()
	display := strings.TrimSpace(*name)
	if display == "" {
		display = hostname
	}
	body, _ := json.Marshal(agentEnrollRequest{
		Name: display, Hostname: hostname,
		OS: runtime.GOOS, Arch: runtime.GOARCH, Version: panelVersion,
		Caps: agentCaps(),
	})
	req, err := http.NewRequest(http.MethodPost, *server+agentEnrollPath, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent: bad server URL:", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(agentEnrollHeader, *token)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent: enrollment request failed:", err)
		return 1
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "agent: enrollment rejected (HTTP %d): %s\n", resp.StatusCode, trimLen(string(b), 200))
		return 1
	}
	var er agentEnrollResponse
	if err := json.Unmarshal(b, &er); err != nil || er.ID == "" || er.Key == "" {
		fmt.Fprintln(os.Stderr, "agent: malformed enrollment response from controller")
		return 1
	}
	ident := agentIdentity{
		ID: er.ID, Key: er.Key,
		Server: *server, BasePath: strings.Trim(er.BasePath, "/"),
		Name: display, EnrolledAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := agentSaveIdentity(ident); err != nil {
		fmt.Fprintln(os.Stderr, "agent: cannot persist enrollment:", err)
		return 1
	}
	fmt.Println("==================================================")
	fmt.Println("[OK] Agent enrolled.")
	fmt.Printf("Agent ID:  %s\n", ident.ID)
	fmt.Printf("Server:    %s\n", agentWSURL(ident))
	fmt.Printf("State dir: %s\n", configDir)
	fmt.Println("Next:      gre-panel agent install   (or run `gre-panel agent run` directly)")
	fmt.Println("==================================================")
	return 0
}

// --- systemd unit -----------------------------------------------------------

// agentUnitDir and agentSystemctl are seams: tests point the directory at a
// temp dir and stub systemctl. (Deliberately not runSystemctl — that is the
// panel's own seam; the agent never runs panel services.)
var agentUnitDir = "/etc/systemd/system"

var agentSystemctl = func(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s failed: %w: %s", strings.Join(args, " "), err, trimLen(string(out), 300))
	}
	return nil
}

func agentUnitContent(exe string) string {
	if strings.ContainsAny(exe, " \t") {
		exe = "\"" + exe + "\""
	}
	return "[Unit]\nDescription=Quantum Panel Agent\nAfter=network-online.target\nWants=network-online.target\n\n" +
		"[Service]\nType=simple\nExecStart=" + exe + " agent run\nRestart=always\nRestartSec=5\n\n" +
		"[Install]\nWantedBy=multi-user.target\n"
}

// agentInstall writes the gre-agent unit and enables it.
// filepathAbs: tiny indirection so tests never depend on where the test
// binary actually lives.
var filepathAbs = filepath.Abs

// --- CLI dispatch -----------------------------------------------------------

// agentCmd is the `gre-panel agent` entry point, dispatched from main() as
// `case "agent": os.Exit(agentCmd(os.Args[2:]))`. It scopes configDir from
// GRE_PANEL_DIR before anything else, exactly like cliSetPassword.
func agentCmd(args []string) int {
	if v := os.Getenv("GRE_PANEL_DIR"); v != "" {
		configDir = v
	}
	if len(args) == 0 {
		agentUsage()
		return 1
	}
	switch args[0] {
	case "enroll":
		return agentEnroll(args[1:])
	case "run":
		return agentRun()
	case "install":
		return agentInstall()
	case "uninstall":
		return agentUninstall()
	case "status":
		return agentStatusCmd()
	case "--help", "-h", "help":
		agentUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "agent: unknown subcommand %q\n", args[0])
		agentUsage()
		return 1
	}
}

func agentUsage() {
	fmt.Fprint(os.Stderr, `usage: gre-panel agent <command>
  enroll --server <url> --token <token> [--name <name>]   enroll this endpoint with the controller
  run                                                     run the agent daemon
  install                                                 install and start the systemd unit
  uninstall                                               stop and remove the systemd unit
  status                                                  print the current agent state as JSON
`)
}

// agentStatusCmd prints the agent's persisted view of itself as JSON. It is
// a separate process from `agent run`, so everything it reports comes from
// agent.json / agent-state.json (including the last WS status the run loop
// journaled). The agent key is never printed.
func agentStatusCmd() int {
	ident, ok := agentLoadIdentity()
	if !ok {
		fmt.Fprintf(os.Stderr, "agent: not enrolled (no %s) — run `gre-panel agent enroll` first\n", agentIdentityPath())
		return 1
	}
	st := agentLoadState()
	out := map[string]any{
		"id":               ident.ID,
		"name":             ident.Name,
		"server":           ident.Server,
		"base_path":        ident.BasePath,
		"applied_revision": st.AppliedRevision,
		"pending_desired":  st.Desired != nil && st.DesiredStatus == "pending",
		"matrix":           agentMatrixStatuses(false),
	}
	if st.Desired != nil {
		out["desired_revision"] = st.Desired.Revision
		out["desired_status"] = st.DesiredStatus
	}
	if st.LastAck != nil {
		out["last_ack"] = st.LastAck
	}
	if st.WS != nil {
		out["ws"] = st.WS
		if st.WS.StartedAt > 0 {
			out["uptime_s"] = time.Now().Unix() - st.WS.StartedAt
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
	return 0
}

func agentInstall() int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent: cannot locate the executable:", err)
		return 1
	}
	if exe, err = filepathAbs(exe); err != nil {
		fmt.Fprintln(os.Stderr, "agent: cannot resolve the executable path:", err)
		return 1
	}
	path := filepath.Join(agentUnitDir, agentServiceUnit)
	if err := os.WriteFile(path, []byte(agentUnitContent(exe)), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "agent: cannot write %s: %v\n", path, err)
		return 1
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", agentUnitName}} {
		if err := agentSystemctl(args...); err != nil {
			fmt.Fprintln(os.Stderr, "agent:", err)
			return 1
		}
	}
	fmt.Printf("[OK] %s installed and started (unit: %s)\n", agentUnitName, path)
	return 0
}

// agentUninstall stops and removes the agent unit. It never touches the
// quantum-matrix* units — tunnels keep running under their own units.
func agentUninstall() int {
	_ = agentSystemctl("disable", "--now", agentUnitName)
	path := filepath.Join(agentUnitDir, agentServiceUnit)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "agent: cannot remove %s: %v\n", path, err)
		return 1
	}
	if err := agentSystemctl("daemon-reload"); err != nil {
		fmt.Fprintln(os.Stderr, "agent:", err)
		return 1
	}
	fmt.Printf("[OK] %s uninstalled; Matrix tunnels were left untouched\n", agentUnitName)
	return 0
}
