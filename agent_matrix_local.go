package main

// Desired-state application and allowlisted command execution for the
// headless endpoint agent. Everything here reuses the existing Matrix
// lifecycle in matrix.go — the agent is a driver over the proven primitives
// (matrixSetup / matrixRemove / matrixProvisionNodeBundle /
// matrixRegisterHub), never a second implementation.
//
// Locking: agentMu serializes desired application and command execution;
// matrixMu (matrix.go) guards the Matrix state itself. The node-apply flow
// must NOT hold matrixMu across its register round-trip because
// matrixProvisionNodeBundle and agentResumeNodeBundle take matrixMu
// internally.

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// agentMu serializes desired-state application and command execution. The
// node flow waits up to 30s for the controller's register_result while
// holding it; later desireds and commands queue behind it.
var agentMu sync.Mutex

// agentLink is the connection seam the applier needs for flows that span
// messages (the node-bundle register round-trip). The real implementation
// wraps the WebSocket session; tests fake it. link may be nil during offline
// reconciliation (right after boot, before the first controller connection).
type agentLink interface {
	send(typ string, payload any) error
	waitFor(typ string, timeout time.Duration) (json.RawMessage, error)
}

var (
	// agentRegisterTimeout bounds the node→hub register round-trip.
	agentRegisterTimeout = 30 * time.Second
	// agentRestartWait bounds the post-restart service-active poll.
	agentRestartWait = 3 * time.Second
)

// errAgentDeferred marks a node apply that provisioned fine but cannot
// finish hub registration because no controller connection exists. The
// desired stays pending so the post-welcome reconcile completes the pairing.
var errAgentDeferred = errors.New("hub registration deferred until the controller is reachable")

// agentApplyDesired enforces the revision rules against the persisted journal
// and applies the desired's Matrix work. It never journals the outcome
// itself; the caller sends the ack and records it via agentRecordAck.
//
// Rules (d.Revision vs applied_revision):
//   - equal  → ok ack again, no work (idempotent replay)
//   - older  → ok:false "stale revision rejected", applied unchanged
//   - newer  → apply; ok ack on success, ok:false with the error otherwise
func agentApplyDesired(link agentLink, d *agentDesired) agentAck {
	if d.Revision == "" {
		return agentAck{Revision: d.Revision, OK: false, Error: "stale revision rejected"}
	}
	st := agentLoadState()
	if d.Revision == st.AppliedRevision {
		return agentAck{Revision: d.Revision, OK: true}
	}
	if !agentRevisionNewer(d.Revision, st.AppliedRevision) {
		return agentAck{Revision: d.Revision, OK: false, Error: "stale revision rejected"}
	}
	if d.Matrix == nil {
		return agentAck{Revision: d.Revision, OK: true}
	}
	switch d.Matrix.Action {
	case "remove":
		return agentApplyMatrixRemove(d, d.Matrix)
	case "apply":
		if d.Matrix.Hub != nil {
			return agentApplyMatrixHub(d, d.Matrix)
		}
		if d.Matrix.Bundle != "" {
			return agentApplyMatrixNode(link, d, d.Matrix)
		}
	}
	return agentAck{Revision: d.Revision, OK: false, Error: "unsupported desired matrix action"}
}

// agentApplyMatrixRemove tears the instance down (idempotent: a remove for
// an already-absent instance is still an ok ack).
func agentApplyMatrixRemove(d *agentDesired, m *agentDesiredMatrix) agentAck {
	in := matrixInstance{ID: m.Instance}
	if !in.valid() {
		return agentAck{Revision: d.Revision, OK: false, Error: fmt.Sprintf("invalid Matrix instance %q", m.Instance)}
	}
	matrixMu.Lock()
	defer matrixMu.Unlock()
	st := loadMatrixInstance(in)
	if st.Enabled || agentMatrixLeftovers(in) {
		if err := matrixRemove(in, st); err != nil {
			return agentAck{Revision: d.Revision, OK: false, Error: err.Error()}
		}
		return agentAck{Revision: d.Revision, OK: true, Detail: map[string]string{"removed": "true"}}
	}
	return agentAck{Revision: d.Revision, OK: true}
}

// agentMatrixLeftovers reports instance files with no enabled state — the
// debris of a failed setup — so an explicit remove can always finish the
// cleanup.
func agentMatrixLeftovers(in matrixInstance) bool {
	for _, p := range []string{matrixDirOf(in), matrixUnitPathOf(in), matrixStatePathOf(in)} {
		if _, err := os.Lstat(p); err == nil {
			return true
		}
	}
	return false
}

// agentApplyMatrixHub applies the hub (Iran side) desired configuration.
// On success the ack detail carries "main_host" — the hub's tinc host file,
// which the controller needs to build the node pairing bundle.
func agentApplyMatrixHub(d *agentDesired, m *agentDesiredMatrix) agentAck {
	in := matrixInstance{ID: m.Instance}
	if !in.valid() {
		return agentAck{Revision: d.Revision, OK: false, Error: fmt.Sprintf("invalid Matrix instance %q", m.Instance)}
	}
	h := m.Hub
	matrixMu.Lock()
	defer matrixMu.Unlock()
	port := h.Port
	if port == 0 {
		// Port 0 = agent picks a free mesh port, exactly like the panel flow.
		p, err := matrixAutoMeshPort(in, h.Ports)
		if err != nil {
			return agentAck{Revision: d.Revision, OK: false, Error: err.Error()}
		}
		port = p
	}
	if err := validateMatrixSettings(h.Family, h.Addr, port, h.Ports); err != nil {
		return agentAck{Revision: d.Revision, OK: false, Error: err.Error()}
	}
	st := loadMatrixInstance(in)
	want := matrixState{
		Enabled: true, Network: in.network(), Role: "hub",
		Family: h.Family, Port: port, Addr: h.Addr, Ports: h.Ports,
		Name: "main", IP: in.hubIP(), Token: h.Token, Revision: d.Revision,
	}
	if st.Enabled && st.Network == in.network() {
		if agentHubConfigEqual(st, want) {
			// Idempotent: same configuration. Only keep the bundle revision
			// in sync so later node registrations validate.
			if st.Revision != d.Revision {
				st.Revision = d.Revision
				if err := saveMatrixInstance(in, st); err != nil {
					return agentAck{Revision: d.Revision, OK: false, Error: err.Error()}
				}
			}
			return agentHubMainHostAck(d, in)
		}
		// Config drift: full rebuild (which also drops any stale pairing).
		if err := matrixRemove(in, st); err != nil {
			return agentAck{Revision: d.Revision, OK: false, Error: err.Error()}
		}
	} else if st.Enabled {
		return agentAck{Revision: d.Revision, OK: false, Error: fmt.Sprintf("instance %s is enabled with a different network; remove it first", in.ID)}
	}
	if err := matrixSetup(in, want, ""); err != nil {
		return agentAck{Revision: d.Revision, OK: false, Error: err.Error()}
	}
	return agentHubMainHostAck(d, in)
}

// agentHubConfigEqual compares the user-visible hub configuration only;
// peers and the recorded revision are lifecycle bookkeeping, not config.
func agentHubConfigEqual(a, b matrixState) bool {
	return a.Family == b.Family && a.Port == b.Port && a.Addr == b.Addr &&
		a.Token == b.Token && agentPortsEqual(a.Ports, b.Ports)
}

func agentPortsEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// agentHubMainHostAck reads the hub's hosts/main file for the ack detail.
func agentHubMainHostAck(d *agentDesired, in matrixInstance) agentAck {
	main, err := os.ReadFile(filepath.Join(matrixDirOf(in), "hosts", "main"))
	if err != nil {
		return agentAck{Revision: d.Revision, OK: false, Error: "hub applied but hosts/main is missing: " + err.Error()}
	}
	return agentAck{Revision: d.Revision, OK: true, Detail: map[string]string{"main_host": string(main)}}
}

// agentApplyMatrixNode applies the node (foreign side) bundle: provision via
// the proven matrixProvisionNodeBundle, then register with the hub through
// the controller (register / register_result over the same connection).
// Registration failure rolls the provisioning back, exactly like the manual
// HTTP flow.
func agentApplyMatrixNode(link agentLink, d *agentDesired, m *agentDesiredMatrix) agentAck {
	res, err := agentProvisionNode(m.Bundle)
	if err != nil {
		return agentAck{Revision: d.Revision, OK: false, Error: err.Error()}
	}
	if link == nil {
		// Offline reconcile: the bundle is applied; registration needs the
		// controller, so the desired stays pending.
		return agentAck{Revision: d.Revision, OK: false, Error: errAgentDeferred.Error()}
	}
	reg := agentRegisterMsg{
		Instance: res.Instance.ID,
		Token:    res.Token,
		Name:     res.Name,
		HostFile: res.HostFile,
		Revision: res.Revision,
	}
	if err := link.send(agentMsgRegister, reg); err != nil {
		agentRollbackNode(res)
		return agentAck{Revision: d.Revision, OK: false, Error: "hub registration failed: " + err.Error()}
	}
	raw, err := link.waitFor(agentMsgRegisterResult, agentRegisterTimeout)
	if err != nil {
		agentRollbackNode(res)
		return agentAck{Revision: d.Revision, OK: false, Error: "hub registration failed: " + err.Error()}
	}
	var rr struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error,omitempty"`
		MainHost string `json:"main_host,omitempty"` // optional forward-compat field
	}
	uerr := json.Unmarshal(raw, &rr)
	if uerr != nil || !rr.OK {
		agentRollbackNode(res)
		msg := rr.Error
		if msg == "" && uerr != nil {
			msg = uerr.Error()
		}
		if msg == "" {
			msg = "rejected"
		}
		return agentAck{Revision: d.Revision, OK: false, Error: "hub registration " + msg}
	}
	detail := map[string]string{}
	if rr.MainHost != "" {
		detail["hub_main_host"] = rr.MainHost
	}
	return agentAck{Revision: d.Revision, OK: true, Detail: detail}
}

// agentRollbackNode tears down a freshly provisioned node whose hub
// registration failed, mirroring handleMatrixForeignSetup.
func agentRollbackNode(res matrixNodeApplyResult) {
	matrixMu.Lock()
	defer matrixMu.Unlock()
	_ = matrixRemove(res.Instance, res.State)
}

// agentProvisionNode applies a node bundle, resuming cleanly when the bundle
// was already provisioned by an earlier attempt that died before pairing
// (self-healing across agent restarts).
func agentProvisionNode(bundle string) (matrixNodeApplyResult, error) {
	res, err := matrixProvisionNodeBundle(bundle)
	if err == nil {
		return res, nil
	}
	var oe *matrixOpError
	if !errors.As(err, &oe) || oe.Code != "E-MATRIX-01" {
		return matrixNodeApplyResult{}, err
	}
	return agentResumeNodeBundle(bundle)
}

// agentResumeNodeBundle validates that the already-enabled instance really
// is the bundle's node and rebuilds the provisioning result from disk.
func agentResumeNodeBundle(bundle string) (matrixNodeApplyResult, error) {
	b, in, err := parseMatrixBundleFor(bundle)
	if err != nil {
		return matrixNodeApplyResult{}, err
	}
	matrixMu.Lock()
	defer matrixMu.Unlock()
	st := loadMatrixInstance(in)
	if !st.Enabled || st.Role != "node" || st.Network != in.network() ||
		st.Family != b.Family || st.Addr != b.Addr || st.Panel != b.Panel {
		return matrixNodeApplyResult{}, fmt.Errorf("instance %s is already enabled with a different configuration; remove it first", in.ID)
	}
	self, err := os.ReadFile(filepath.Join(matrixDirOf(in), "hosts", st.Name))
	if err != nil {
		return matrixNodeApplyResult{}, err
	}
	return matrixNodeApplyResult{Instance: in, State: st, BundlePort: b.Port, Panel: b.Panel, Token: b.Token, Name: st.Name, HostFile: string(self), Revision: b.Revision}, nil
}

// agentReconcilePending applies a pending desired from the journal. With a
// nil link (right after boot, before the first controller connection) hub
// and remove flows run fully offline; a node flow provisions but defers its
// hub registration and stays pending, so the post-welcome reconcile finishes
// and acks it. Failures before the first connection also stay pending: once
// the controller is reachable the desired is retried and its ack delivered.
func agentReconcilePending(link agentLink) {
	st := agentLoadState()
	if st.Desired == nil || st.DesiredStatus != "pending" {
		return
	}
	d := *st.Desired
	agentMu.Lock()
	ack := agentApplyDesired(link, &d)
	agentMu.Unlock()
	if link == nil {
		if ack.OK {
			agentRecordAck(d.Revision, ack)
		} else {
			log.Printf("[agent] offline reconcile of revision %s not finished (retries after connect): %s", d.Revision, ack.Error)
		}
		return
	}
	if err := link.send(agentMsgAck, ack); err != nil {
		log.Printf("[agent] reconcile ack send failed: %v", err)
		return // stays pending; retried next session
	}
	agentRecordAck(d.Revision, ack)
}

// agentRecordAck journals the ack outcome: ok → applied (revision advanced),
// deferred → stays pending, anything else → failed with the error recorded.
// A superseded desired (a newer one arrived meanwhile) is never clobbered.
func agentRecordAck(revision string, ack agentAck) {
	deferred := ack.Error == errAgentDeferred.Error()
	_ = agentMutateState(func(st *agentPersistedState) {
		if st.Desired != nil && st.Desired.Revision != revision {
			return
		}
		ackCopy := ack
		st.LastAck = &ackCopy
		switch {
		case ack.OK:
			st.AppliedRevision = revision
			st.DesiredStatus = "applied"
		case deferred:
			st.DesiredStatus = "pending"
		default:
			st.DesiredStatus = "failed"
		}
	})
}

// agentRunCommand executes one allowlisted controller command. Unknown ops
// are refused; there is no generic exec surface.
func agentRunCommand(cmd agentCommand) agentCommandResult {
	res := agentCommandResult{ID: cmd.ID}
	if !agentValidCommands[cmd.Op] {
		res.Error = "operation not allowed"
		return res
	}
	p := cmd.Params
	if p == nil {
		p = map[string]string{}
	}
	switch cmd.Op {
	case agentCmdMatrixRestart, agentCmdMatrixStart, agentCmdMatrixStop:
		return agentRunMatrixPower(cmd.Op, p, res)
	case agentCmdMatrixRegister:
		return agentRunMatrixRegister(p, res)
	case agentCmdDiagPing:
		return agentRunDiagPing(p, res)
	}
	res.Error = "operation not allowed"
	return res
}

// agentServiceActiveWait polls matrixServiceActive for up to d.
func agentServiceActiveWait(in matrixInstance, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if matrixServiceActive(in) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// agentRunMatrixPower implements matrix.restart / matrix.start / matrix.stop.
// stop is stop only: the unit stays enabled, so start or reconciliation
// brings it back and the state survives agent restarts.
func agentRunMatrixPower(op string, p map[string]string, res agentCommandResult) agentCommandResult {
	in := matrixInstance{ID: strings.TrimSpace(p["instance"])}
	if !in.valid() {
		res.Error = fmt.Sprintf("invalid Matrix instance %q", p["instance"])
		return res
	}
	matrixMu.Lock()
	defer matrixMu.Unlock()
	st := loadMatrixInstance(in)
	if !st.Enabled || st.Network != in.network() {
		res.Error = "no owned Matrix network on instance " + in.ID
		return res
	}
	switch op {
	case agentCmdMatrixRestart:
		// Same semantics as handleMatrixRestartPost, plus an active poll.
		if _, err := matrixExec("systemctl", "restart", in.unit()); err != nil {
			res.Error = err.Error()
			return res
		}
		if st.Role == "hub" && len(st.Peers) > 0 {
			if err := matrixActivateForward(in, &st); err != nil {
				res.Error = err.Error()
				return res
			}
		}
		if !agentServiceActiveWait(in, agentRestartWait) {
			res.Error = "Matrix service did not become active after restart"
			return res
		}
		res.OK = true
	case agentCmdMatrixStart:
		if err := matrixServiceStart(in); err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
	default: // agentCmdMatrixStop
		if _, err := matrixExec("systemctl", "stop", in.unit()); err != nil {
			res.Error = err.Error()
			return res
		}
		res.OK = true
	}
	return res
}

// agentRunMatrixRegister is the agent-side half of node pairing: the
// controller relays a node's register message to the hub agent. The hub
// instance is resolved by pairing token exactly like handleMatrixRegister.
func agentRunMatrixRegister(p map[string]string, res agentCommandResult) agentCommandResult {
	token, name, hostFile := p["token"], p["name"], p["host_file"]
	if token == "" || name == "" || hostFile == "" {
		res.Error = "token, name and host_file are required"
		return res
	}
	matrixMu.Lock()
	defer matrixMu.Unlock()
	var in matrixInstance
	var st matrixState
	found := false
	for _, candidate := range matrixInstances() {
		c := loadMatrixInstance(candidate)
		if c.Enabled && c.Network == candidate.network() && c.Role == "hub" && c.Token != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(c.Token)) == 1 {
			in, st, found = candidate, c, true
			break
		}
	}
	if !found {
		res.Error = "registration denied"
		return res
	}
	if req := strings.TrimSpace(p["instance"]); req != "" && req != in.ID {
		res.Error = "pairing token does not belong to instance " + req
		return res
	}
	main, err := matrixRegisterHub(in, st, name, p["addr"], hostFile, p["revision"])
	if err != nil {
		var ue *matrixOpError
		if errors.As(err, &ue) {
			res.Error = ue.Msg
			return res
		}
		res.Error = err.Error()
		return res
	}
	res.OK = true
	res.Output = main // the hub's hosts/main, as the legacy HTTP flow returns
	return res
}

// agentRunDiagPing pings one mesh address through the instance interface.
func agentRunDiagPing(p map[string]string, res agentCommandResult) agentCommandResult {
	in := matrixInstance{ID: strings.TrimSpace(p["instance"])}
	if !in.valid() {
		res.Error = fmt.Sprintf("invalid Matrix instance %q", p["instance"])
		return res
	}
	target := strings.TrimSpace(p["target"])
	if net.ParseIP(target) == nil {
		res.Error = "target must be an IP address"
		return res
	}
	if !matrixMeshPing(in, target) {
		res.Error = "ping failed"
		return res
	}
	res.OK = true
	res.Output = "ping ok"
	return res
}

// agentBuildStateReport assembles the full host picture carried by every
// heartbeat: what is applied, instance summaries, the latest health windows,
// traffic counters and the (read-only) PasarGuard Node signal. Light probes
// only — never a bandwidth test, never a snapshot collector.
func agentBuildStateReport() *agentStateReport {
	st := agentLoadState()
	return &agentStateReport{
		AppliedRevision: st.AppliedRevision,
		Matrix:          agentMatrixStatuses(true),
		Health:          agentHealthSnapshot(),
		Traffic:         agentCollectTraffic(),
		Resources:       agentCollectResources(),
		NodeDetect:      agentDetectNode(),
	}
}

// agentMatrixStatuses summarizes every local Matrix instance. With probe on,
// peer/hub reachability is pinged; the status CLI runs it without probes.
func agentMatrixStatuses(probe bool) []agentMatrixStatus {
	matrixMu.Lock()
	defer matrixMu.Unlock()
	applied := agentLoadState().AppliedRevision
	out := []agentMatrixStatus{}
	for _, in := range matrixInstances() {
		st := loadMatrixInstance(in)
		owned := st.Enabled && st.Network == in.network()
		s := agentMatrixStatus{
			Instance: in.ID,
			Role:     st.Role,
			Network:  st.Network,
			Enabled:  st.Enabled,
			Service:  owned && matrixServiceActive(in),
			Iface:    in.iface(),
			Port:     st.Port,
			MeshIP:   st.IP,
			Revision: applied,
		}
		s.IfaceUp = s.Service && agentIfaceUp(in.iface())
		switch st.Role {
		case "hub":
			s.Registered = len(st.Peers) > 0
		case "node":
			_, err := os.Stat(filepath.Join(matrixDirOf(in), "hosts", "main"))
			s.Registered = st.Enabled && err == nil
		}
		if probe && owned {
			for _, p := range st.Peers {
				s.Peers = append(s.Peers, agentPeerPing{Name: p.Name, IP: p.IP, OK: s.Service && matrixMeshPing(in, p.IP)})
			}
			if st.Role == "node" {
				s.Peers = append(s.Peers, agentPeerPing{Name: "hub", IP: in.hubIP(), OK: s.Service && matrixMeshPing(in, in.hubIP())})
			}
		}
		out = append(out, s)
	}
	return out
}
