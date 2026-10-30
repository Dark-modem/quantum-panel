package main

// Agent-driven Matrix tunnels: controller-side orchestration that turns
// one hub (Iran) agent + one node (foreign) agent into a provisioned
// Matrix tunnel. The controller never talks to tinc itself — it only
// pushes versioned desired state to the two agents and relays the node's
// pairing registration to the hub:
//
//	create  -> desired(hub apply)  -> hub ack {main_host}
//	        -> desired(node bundle)-> node applies, sends register
//	register -> command(matrix.register) to hub -> hub pairs the node
//	node ack -> tunnel active; health = hub's mesh pings
//
// This file depends on the agent hub (agenthub.go) only through the
// *_Fn seams below; integration wiring assigns them the real hub
// functions, and tests inject fakes. Keep it that way so the tunnel
// layer stays testable without a live hub.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- seams to the agent hub / registry (wired at integration) ----

// agentTrafficRateView is one interface's recent byte rate as seen by
// the controller (derived from consecutive agent heartbeats).
type agentTrafficRateView struct {
	Name  string
	Kind  string // "physical" | "tunnel"
	RxBps float64
	TxBps float64
}

// agentTunnelAgentInfo is the narrow registry view the tunnel layer needs.
type agentTunnelAgentInfo struct {
	ID              string
	Name            string
	Role            string
	Status          string
	PublicIP        string
	Version         string
	Hostname        string
	Caps            []string
	AppliedRevision string
	LastState       *agentStateReport
	TrafficRate     map[string]agentTrafficRateView
}

var (
	agentHubDeliverDesiredFn func(id string, d agentDesired) error
	agentHubCommandFn        func(id, op string, params map[string]string, timeout time.Duration) (agentCommandResult, error)
	agentHubOnlineFn         func(id string) bool
	agentRegistryLookupFn    func(id string) (agentTunnelAgentInfo, bool)
)

func agentTunnelHubWired() bool {
	return agentHubDeliverDesiredFn != nil && agentHubCommandFn != nil && agentHubOnlineFn != nil
}

// agentTunnelAgent fetches registry info, tolerating an unwired registry.
func agentTunnelAgent(id string) (agentTunnelAgentInfo, bool) {
	if agentRegistryLookupFn == nil {
		return agentTunnelAgentInfo{ID: id}, false
	}
	return agentRegistryLookupFn(id)
}

// ---- tunnel store: <configDir>/agent_tunnels.json (0600) ----

// Tunnel lifecycle states persisted in the record: "provisioning" (hub
// desired in flight), "registering" (node desired in flight), "active",
// "stopped" (powered off), "error".
const (
	agentTunnelStateProvisioning = "provisioning"
	agentTunnelStateRegistering  = "registering"
	agentTunnelStateActive       = "active"
	agentTunnelStateStopped      = "stopped"
	agentTunnelStateError        = "error"
)

type agentTunnel struct {
	ID          string `json:"id"` // "at_" + 8 hex chars
	Name        string `json:"name"`
	Instance    string `json:"instance"` // matrix instance id on BOTH agents
	HubAgentID  string `json:"hub_agent_id"`
	NodeAgentID string `json:"node_agent_id"`
	Family      string `json:"family"`
	MeshPort    int    `json:"mesh_port"` // 0 = hub agent auto-picks
	Ports       []int  `json:"ports,omitempty"`
	Token       string `json:"token"` // pairing token; 0600 file, never leaves the API

	Active      bool   `json:"active"` // desired power state
	State       string `json:"state"`
	Error       string `json:"error,omitempty"`
	HubMainHost string `json:"hub_main_host,omitempty"`

	HubRevision  string `json:"hub_revision,omitempty"`
	NodeRevision string `json:"node_revision,omitempty"`
	HubAckOK     bool   `json:"hub_ack_ok,omitempty"`
	HubAckError  string `json:"hub_ack_error,omitempty"`
	NodeAckOK    bool   `json:"node_ack_ok,omitempty"`
	NodeAckError string `json:"node_ack_error,omitempty"`

	CreatedAt int64 `json:"created_at"`
}

type agentTunnelStore struct {
	Version int           `json:"version"`
	Tunnels []agentTunnel `json:"tunnels"`
}

var (
	agentTunnelsMu   sync.Mutex
	agentTunnelsPath string
)

func agentTunnelsFile() string {
	if agentTunnelsPath == "" {
		agentTunnelsPath = filepath.Join(configDir, "agent_tunnels.json")
	}
	return agentTunnelsPath
}

func loadAgentTunnels() agentTunnelStore {
	var s agentTunnelStore
	data, err := os.ReadFile(agentTunnelsFile())
	if err != nil || len(data) == 0 {
		return agentTunnelStore{Version: 1}
	}
	if err := json.Unmarshal(data, &s); err != nil || s.Version != 1 {
		_ = os.Rename(agentTunnelsFile(), agentTunnelsFile()+".bad")
		log.Printf("agent_tunnels.json unreadable — starting empty (old file kept as .bad)")
		return agentTunnelStore{Version: 1}
	}
	return s
}

func saveAgentTunnels(s agentTunnelStore) error {
	s.Version = 1
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	p := agentTunnelsFile()
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// agentTunnelNextInstance picks the first free matrix instance id for a
// hub agent, based on the tunnels already assigned to it.
func agentTunnelNextInstance(tunnels []agentTunnel, hubAgentID string) string {
	used := map[string]bool{}
	for _, t := range tunnels {
		if t.HubAgentID == hubAgentID {
			used[t.Instance] = true
		}
	}
	for _, cand := range []string{"matrix", "matrix2", "matrix3", "matrix4", "matrix5", "matrix6", "matrix7", "matrix8"} {
		if !used[cand] {
			return cand
		}
	}
	return ""
}

// agentNewRevision is a monotonic desired-state revision (unix nanos).
func agentNewRevision() string { return strconv.FormatInt(time.Now().UnixNano(), 10) }

// ---- ack/wait plumbing ----

var (
	agentTunnelWaitMu sync.Mutex
	agentTunnelWaits  = map[string]chan agentAck{} // key: agentID + "|" + revision
)

func agentTunnelWaitKey(agentID, revision string) string { return agentID + "|" + revision }

func agentTunnelArmWait(agentID, revision string) <-chan agentAck {
	ch := make(chan agentAck, 1)
	agentTunnelWaitMu.Lock()
	agentTunnelWaits[agentTunnelWaitKey(agentID, revision)] = ch
	agentTunnelWaitMu.Unlock()
	return ch
}

func agentTunnelDisarmWait(agentID, revision string) {
	agentTunnelWaitMu.Lock()
	delete(agentTunnelWaits, agentTunnelWaitKey(agentID, revision))
	agentTunnelWaitMu.Unlock()
}

// agentTunnelAckObserved is installed as the hub's agentAckObserver at
// integration; it routes acks to armed waiters and records them.
func agentTunnelAckObserved(agentID string, ack agentAck) {
	agentTunnelWaitMu.Lock()
	if ch, ok := agentTunnelWaits[agentTunnelWaitKey(agentID, ack.Revision)]; ok {
		ch <- ack
	}
	agentTunnelWaitMu.Unlock()

	agentTunnelsMu.Lock()
	defer agentTunnelsMu.Unlock()
	s := loadAgentTunnels()
	changed := false
	for i := range s.Tunnels {
		t := &s.Tunnels[i]
		if t.HubAgentID == agentID && t.HubRevision == ack.Revision {
			t.HubAckOK, t.HubAckError = ack.OK, ack.Error
			changed = true
		}
		if t.NodeAgentID == agentID && t.NodeRevision == ack.Revision {
			t.NodeAckOK, t.NodeAckError = ack.OK, ack.Error
			changed = true
			if ack.OK && t.State == agentTunnelStateRegistering {
				t.State = agentTunnelStateActive
				t.Error = ""
			} else if !ack.OK {
				t.State = agentTunnelStateError
				t.Error = "node: " + ack.Error
			}
		}
	}
	if changed {
		if err := saveAgentTunnels(s); err != nil {
			log.Printf("agent tunnel: save after ack: %v", err)
		}
	}
}

// ---- provisioning orchestration ----

const (
	agentTunnelAckWait    = 180 * time.Second // apt + tinc setup can be slow
	agentTunnelPortWait   = 20 * time.Second
	agentTunnelCmdTimeout = 20 * time.Second
)

// controllerPanelURL is the Panel URL embedded in node bundles. The
// agent path never dials it (registration rides the controller WS), but
// bundle validation requires an explicit http(s)://IP endpoint.
func controllerPanelURL() string {
	return "http://" + getOutboundIP() + ":" + strconv.Itoa(cfg.Port)
}

// agentTunnelHubDesired builds the hub-side desired state for a tunnel.
func agentTunnelHubDesired(t *agentTunnel, hubAddr string, revision string) agentDesired {
	return agentDesired{Revision: revision, Matrix: &agentDesiredMatrix{
		Instance: t.Instance, Action: "apply",
		Hub: &agentDesiredHub{Family: t.Family, Port: t.MeshPort, Addr: hubAddr, Ports: t.Ports, Token: t.Token},
	}}
}

// agentTunnelNodeBundle builds the hmx1_ pairing bundle for the node
// agent once the hub has reported its hosts/main file and mesh port.
func agentTunnelNodeBundle(t *agentTunnel, hubAddr string, meshPort int, revision string) (string, error) {
	in := matrixInstance{ID: t.Instance}
	if !in.valid() {
		return "", fmt.Errorf("invalid matrix instance %q", t.Instance)
	}
	mainHost := strings.TrimSpace(t.HubMainHost)
	if mainHost == "" {
		return "", fmt.Errorf("hub host file missing")
	}
	if meshPort < 1 {
		return "", fmt.Errorf("hub mesh port unknown")
	}
	payload, err := json.Marshal(matrixBundlePayload{
		Panel: controllerPanelURL(), Token: t.Token, Family: t.Family,
		Addr: hubAddr, Port: meshPort, FIP: in.firstPeer(), Ports: t.Ports,
		MainHost: mainHost, Network: in.network(), Revision: revision,
	})
	if err != nil {
		return "", err
	}
	return matrixBundlePfx + base64.StdEncoding.EncodeToString(payload), nil
}

// agentTunnelWaitPort polls the hub agent's last state report for the
// instance's actual mesh port (needed when the desired said auto).
func agentTunnelWaitPort(agentID, instance string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for {
		if info, ok := agentTunnelAgent(agentID); ok && info.LastState != nil {
			for _, m := range info.LastState.Matrix {
				if m.Instance == instance && m.Port > 0 {
					return m.Port
				}
			}
		}
		if time.Now().After(deadline) {
			return 0
		}
		time.Sleep(2 * time.Second)
	}
}

// agentTunnelProvision drives both sides. Runs in its own goroutine; all
// failures land in the tunnel record (state=error), never in a panic.
func agentTunnelProvision(tunnelID string) {
	agentTunnelsMu.Lock()
	s := loadAgentTunnels()
	var t *agentTunnel
	for i := range s.Tunnels {
		if s.Tunnels[i].ID == tunnelID {
			t = &s.Tunnels[i]
			break
		}
	}
	if t == nil {
		agentTunnelsMu.Unlock()
		return
	}
	hubAddr := ""
	if info, ok := agentTunnelAgent(t.HubAgentID); ok {
		hubAddr = info.PublicIP
	}
	// Snapshot fields we need; release the store lock during network waits.
	job := *t
	agentTunnelsMu.Unlock()

	fail := func(stage, msg string) {
		agentTunnelsMu.Lock()
		s := loadAgentTunnels()
		for i := range s.Tunnels {
			if s.Tunnels[i].ID == tunnelID {
				s.Tunnels[i].State = agentTunnelStateError
				s.Tunnels[i].Error = stage + ": " + msg
			}
		}
		_ = saveAgentTunnels(s)
		agentTunnelsMu.Unlock()
		log.Printf("agent tunnel %s: %s: %s", tunnelID, stage, msg)
	}

	if hubAddr == "" {
		fail("hub", "hub agent public address unknown (agent never connected)")
		return
	}

	// 1. Hub desired.
	hubRev := agentNewRevision()
	hubCh := agentTunnelArmWait(job.HubAgentID, hubRev)
	defer agentTunnelDisarmWait(job.HubAgentID, hubRev)
	if err := agentHubDeliverDesiredFn(job.HubAgentID, agentTunnelHubDesired(&job, hubAddr, hubRev)); err != nil {
		fail("hub", err.Error())
		return
	}
	markState(tunnelID, agentTunnelStateProvisioning, hubRev, "")
	select {
	case ack := <-hubCh:
		if !ack.OK {
			fail("hub", ack.Error)
			return
		}
		agentTunnelsMu.Lock()
		s := loadAgentTunnels()
		for i := range s.Tunnels {
			if s.Tunnels[i].ID == tunnelID {
				s.Tunnels[i].HubMainHost = ack.Detail["main_host"]
			}
		}
		_ = saveAgentTunnels(s)
		agentTunnelsMu.Unlock()
	case <-time.After(agentTunnelAckWait):
		fail("hub", "hub agent did not acknowledge in time")
		return
	}

	// 2. Resolve the actual mesh port (auto case) and node bundle.
	meshPort := job.MeshPort
	if meshPort < 1 {
		meshPort = agentTunnelWaitPort(job.HubAgentID, job.Instance, agentTunnelPortWait)
	}
	agentTunnelsMu.Lock()
	s = loadAgentTunnels()
	hubMainHost := ""
	for i := range s.Tunnels {
		if s.Tunnels[i].ID == tunnelID {
			hubMainHost = s.Tunnels[i].HubMainHost
			if meshPort > 0 {
				s.Tunnels[i].MeshPort = meshPort
			}
		}
	}
	_ = saveAgentTunnels(s)
	agentTunnelsMu.Unlock()

	job.HubMainHost = hubMainHost
	bundle, err := agentTunnelNodeBundle(&job, hubAddr, meshPort, agentNewRevision())
	if err != nil {
		fail("node", err.Error())
		return
	}

	// 3. Node desired.
	nodeRev := agentNewRevision()
	nodeCh := agentTunnelArmWait(job.NodeAgentID, nodeRev)
	defer agentTunnelDisarmWait(job.NodeAgentID, nodeRev)
	nodeDesired := agentDesired{Revision: nodeRev, Matrix: &agentDesiredMatrix{Instance: job.Instance, Action: "apply", Bundle: bundle}}
	if err := agentHubDeliverDesiredFn(job.NodeAgentID, nodeDesired); err != nil {
		fail("node", err.Error())
		return
	}
	markState(tunnelID, agentTunnelStateRegistering, "", nodeRev)
	select {
	case ack := <-nodeCh:
		if !ack.OK {
			fail("node", ack.Error)
			return
		}
	case <-time.After(agentTunnelAckWait):
		fail("node", "node agent did not acknowledge in time")
		return
	}
	// agentTunnelAckObserved already flipped the state to active on the
	// node ack; refresh local copy so callers see the final state.
	agentTunnelsMu.Lock()
	s = loadAgentTunnels()
	for i := range s.Tunnels {
		if s.Tunnels[i].ID == tunnelID && s.Tunnels[i].State == agentTunnelStateRegistering {
			s.Tunnels[i].State = agentTunnelStateActive
		}
	}
	_ = saveAgentTunnels(s)
	agentTunnelsMu.Unlock()
	log.Printf("agent tunnel %s: provisioned (instance %s)", tunnelID, job.Instance)
}

func markState(tunnelID, state, hubRev, nodeRev string) {
	agentTunnelsMu.Lock()
	defer agentTunnelsMu.Unlock()
	s := loadAgentTunnels()
	for i := range s.Tunnels {
		if s.Tunnels[i].ID == tunnelID {
			if state != "" {
				s.Tunnels[i].State = state
			}
			if hubRev != "" {
				s.Tunnels[i].HubRevision = hubRev
			}
			if nodeRev != "" {
				s.Tunnels[i].NodeRevision = nodeRev
			}
		}
	}
	_ = saveAgentTunnels(s)
}

// ---- register relay (node -> controller -> hub command) ----

// agentTunnelRegisterRelay handles the node agent's register message:
// find the tunnel by pairing token, then drive the hub agent's
// matrix.register command. Implemented as a seam-assigned closure so
// tests can intercept the hub command path.
func agentTunnelRegisterRelay(reg agentRegisterMsg, fromAgentID string) agentRegisterResult {
	if !agentTunnelHubWired() {
		return agentRegisterResult{OK: false, Error: "agent hub not wired"}
	}
	agentTunnelsMu.Lock()
	s := loadAgentTunnels()
	var t *agentTunnel
	for i := range s.Tunnels {
		if subtleCompare(s.Tunnels[i].Token, reg.Token) {
			t = &s.Tunnels[i]
			break
		}
	}
	agentTunnelsMu.Unlock()
	if t == nil {
		return agentRegisterResult{OK: false, Error: "unknown pairing token"}
	}
	if fromAgentID != t.NodeAgentID {
		return agentRegisterResult{OK: false, Error: "registration from the wrong agent"}
	}
	start := time.Now()
	nodeAddr := ""
	if info, ok := agentTunnelAgent(t.NodeAgentID); ok {
		nodeAddr = info.PublicIP
	}
	params := map[string]string{
		"instance":  t.Instance,
		"token":     reg.Token,
		"name":      reg.Name,
		"host_file": reg.HostFile,
		"addr":      nodeAddr,
		"revision":  reg.Revision,
	}
	res, err := agentHubCommandFn(t.HubAgentID, agentCmdMatrixRegister, params, agentTunnelCmdTimeout)
	// A hub agent that dropped its WS for a moment fails fast; the node
	// only waits ~30s for this answer, so retry exactly once on a fast
	// failure and never on a slow (timeout) one.
	if err != nil && time.Since(start) < 5*time.Second {
		time.Sleep(7 * time.Second)
		res, err = agentHubCommandFn(t.HubAgentID, agentCmdMatrixRegister, params, agentTunnelCmdTimeout)
	}
	if err != nil {
		return agentRegisterResult{OK: false, Error: err.Error()}
	}
	if !res.OK {
		return agentRegisterResult{OK: false, Error: res.Error}
	}
	log.Printf("agent tunnel %s: node %s registered on the hub", t.ID, reg.Name)
	return agentRegisterResult{OK: true}
}

// subtleCompare is a tiny constant-time string comparison helper.
func subtleCompare(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := 0; i < len(a); i++ {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// ---- read view ----

// agentTunnelHealthView is the effective end-to-end health taken from
// the hub agent's latest mesh ping report.
func agentTunnelHealthView(t agentTunnel) *agentTunnelHealth {
	info, ok := agentTunnelAgent(t.HubAgentID)
	if !ok || info.LastState == nil {
		return nil
	}
	for _, h := range info.LastState.Health {
		if h.Instance == t.Instance {
			return &h
		}
	}
	return nil
}

// agentTrafficSummary sums one agent's rates by interface kind and
// derives the Iran billable amplification: physical NIC RX per payload
// byte pushed into the tunnel. UNKNOWN whenever a component is missing
// or zero — the controller never invents numbers.
func agentTrafficSummary(info agentTunnelAgentInfo) map[string]any {
	var nicRx, nicTx, tunRx, tunTx float64
	for _, r := range info.TrafficRate {
		switch r.Kind {
		case "physical":
			nicRx += r.RxBps
			nicTx += r.TxBps
		case "tunnel":
			tunRx += r.RxBps
			tunTx += r.TxBps
		}
	}
	out := map[string]any{
		"nic_rx_bps": nicRx, "nic_tx_bps": nicTx,
		"tunnel_rx_bps": tunRx, "tunnel_tx_bps": tunTx,
	}
	if nicRx > 0 && tunTx > 0 {
		out["billable_amplification"] = nicRx / tunTx
	} else {
		out["billable_amplification"] = nil
	}
	return out
}

func agentTunnelView(t agentTunnel) map[string]any {
	view := map[string]any{
		"id": t.ID, "name": t.Name, "engine": "matrix", "instance": t.Instance,
		"state": t.State, "active": t.Active, "family": t.Family,
		"mesh_port": t.MeshPort, "ports": t.Ports, "created_at": t.CreatedAt,
	}
	if t.Error != "" {
		view["error"] = t.Error
	}
	endpoint := func(agentID string, ackOK bool, ackErr string) map[string]any {
		v := map[string]any{"agent_id": agentID, "ack_ok": ackOK}
		if ackErr != "" {
			v["ack_error"] = ackErr
		}
		if info, ok := agentTunnelAgent(agentID); ok {
			v["agent_name"] = info.Name
			v["online"] = agentHubOnlineFn != nil && agentHubOnlineFn(agentID)
			v["role"] = info.Role
		} else {
			v["online"] = false
		}
		return v
	}
	view["hub"] = endpoint(t.HubAgentID, t.HubAckOK, t.HubAckError)
	view["node"] = endpoint(t.NodeAgentID, t.NodeAckOK, t.NodeAckError)
	// Billable traffic lives on the hub (Iran) side: its physical NIC RX
	// is what the provider charges, the tunnel interface is the payload.
	if info, ok := agentTunnelAgent(t.HubAgentID); ok && len(info.TrafficRate) > 0 {
		view["traffic"] = agentTrafficSummary(info)
	}
	if h := agentTunnelHealthView(t); h != nil {
		view["health"] = h
	}
	return view
}

// ---- HTTP handlers (coordinator wraps with requireAuth/requireCSRF) ----

func handleAgentTunnelsGet(w http.ResponseWriter, r *http.Request) {
	agentTunnelsMu.Lock()
	s := loadAgentTunnels()
	agentTunnelsMu.Unlock()
	out := make([]map[string]any, 0, len(s.Tunnels))
	for _, t := range s.Tunnels {
		out = append(out, agentTunnelView(t))
	}
	writeJSON(w, map[string]any{"tunnels": out})
}

func handleAgentTunnelsPost(w http.ResponseWriter, r *http.Request) {
	if !agentTunnelHubWired() {
		writeAPIError(w, r, "E-AGENT-04", "agent hub is not available")
		return
	}
	var q struct {
		Name        string `json:"name"`
		HubAgentID  string `json:"hub_agent_id"`
		NodeAgentID string `json:"node_agent_id"`
		Family      string `json:"family"`
		Port        int    `json:"port"`
		Ports       []int  `json:"ports"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&q); e != nil {
		writeAPIError(w, r, "E-AGENT-05", "invalid JSON body")
		return
	}
	q.Name = strings.TrimSpace(q.Name)
	if q.Name == "" {
		q.Name = "Matrix tunnel"
	}
	if len(q.Name) > 80 {
		writeAPIError(w, r, "E-AGENT-05", "name too long (max 80)")
		return
	}
	if q.Family != "ipv4" && q.Family != "ipv6" {
		writeAPIError(w, r, "E-AGENT-05", "family must be ipv4 or ipv6")
		return
	}
	if q.Port != 0 && (q.Port < 1024 || q.Port > 65535) {
		writeAPIError(w, r, "E-AGENT-05", "mesh port must be 0 (auto) or 1024-65535")
		return
	}
	if len(q.Ports) < 1 || len(q.Ports) > 20 {
		writeAPIError(w, r, "E-AGENT-05", "choose 1-20 forwarded ports (Matrix DNATs exactly these into the node)")
		return
	}
	seen := map[int]bool{}
	for _, p := range q.Ports {
		if p < 1 || p > 65535 || p == 22 || seen[p] {
			writeAPIError(w, r, "E-AGENT-05", fmt.Sprintf("invalid forwarded port %d", p))
			return
		}
		seen[p] = true
	}
	if q.HubAgentID == "" || q.NodeAgentID == "" {
		writeAPIError(w, r, "E-AGENT-05", "hub_agent_id and node_agent_id are required")
		return
	}
	if q.HubAgentID == q.NodeAgentID {
		writeAPIError(w, r, "E-AGENT-05", "hub and node must be different agents")
		return
	}
	hubInfo, ok := agentTunnelAgent(q.HubAgentID)
	if !ok {
		writeAPIError(w, r, "E-AGENT-03", "hub agent not found")
		return
	}
	nodeInfo, ok := agentTunnelAgent(q.NodeAgentID)
	if !ok {
		writeAPIError(w, r, "E-AGENT-03", "node agent not found")
		return
	}
	if hubInfo.Status == "revoked" || nodeInfo.Status == "revoked" {
		writeAPIError(w, r, "E-AGENT-05", "a revoked agent cannot be used")
		return
	}
	if !capsInclude(hubInfo.Caps, agentCapMatrix) || !capsInclude(nodeInfo.Caps, agentCapMatrix) {
		writeAPIError(w, r, "E-AGENT-05", "both agents must advertise the matrix capability")
		return
	}

	agentTunnelsMu.Lock()
	s := loadAgentTunnels()
	if len(s.Tunnels) >= 64 {
		agentTunnelsMu.Unlock()
		writeAPIError(w, r, "E-AGENT-05", "too many agent tunnels")
		return
	}
	instance := agentTunnelNextInstance(s.Tunnels, q.HubAgentID)
	if instance == "" {
		agentTunnelsMu.Unlock()
		writeAPIError(w, r, "E-AGENT-05", "no free matrix instance for this hub agent")
		return
	}
	t := agentTunnel{
		ID: "at_" + randHex(4), Name: q.Name, Instance: instance,
		HubAgentID: q.HubAgentID, NodeAgentID: q.NodeAgentID,
		Family: q.Family, MeshPort: q.Port, Ports: q.Ports,
		Token: randHex(32), Active: true,
		State: agentTunnelStateProvisioning, CreatedAt: time.Now().Unix(),
	}
	s.Tunnels = append(s.Tunnels, t)
	if err := saveAgentTunnels(s); err != nil {
		agentTunnelsMu.Unlock()
		writeAPIError(w, r, "E-AGENT-05", err.Error())
		return
	}
	agentTunnelsMu.Unlock()

	go agentTunnelProvision(t.ID)
	LogSecurityAudit("agent_tunnel_create", cfg.Username, ClientIP(r), "id="+t.ID+" hub="+q.HubAgentID+" node="+q.NodeAgentID)
	writeJSON(w, map[string]any{"tunnel": agentTunnelView(t)})
}

func findAgentTunnel(id string) (agentTunnel, agentTunnelStore, bool) {
	s := loadAgentTunnels()
	for _, t := range s.Tunnels {
		if t.ID == id {
			return t, s, true
		}
	}
	return agentTunnel{}, s, false
}

func handleAgentTunnelPower(w http.ResponseWriter, r *http.Request) {
	agentTunnelPowerRestart(w, r, false)
}

func handleAgentTunnelRestart(w http.ResponseWriter, r *http.Request) {
	agentTunnelPowerRestart(w, r, true)
}

// agentTunnelPowerRestart powers a tunnel on/off or restarts it by
// driving the allowlisted matrix commands on both agents.
func agentTunnelPowerRestart(w http.ResponseWriter, r *http.Request, restart bool) {
	if !agentTunnelHubWired() {
		writeAPIError(w, r, "E-AGENT-04", "agent hub is not available")
		return
	}
	id := r.PathValue("id")
	var q struct {
		Active *bool `json:"active"`
	}
	if !restart {
		if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&q); e != nil || q.Active == nil {
			writeAPIError(w, r, "E-AGENT-05", "body must be {\"active\": true|false}")
			return
		}
	}
	agentTunnelsMu.Lock()
	t, _, ok := findAgentTunnel(id)
	if !ok {
		agentTunnelsMu.Unlock()
		writeAPIError(w, r, "E-AGENT-03", "tunnel not found")
		return
	}
	agentTunnelsMu.Unlock()

	active := true
	if !restart && q.Active != nil {
		active = *q.Active
	}
	op := agentCmdMatrixStart
	if !active {
		op = agentCmdMatrixStop
	}
	if restart {
		op = agentCmdMatrixRestart
	}
	var failures []string
	for _, agentID := range []string{t.HubAgentID, t.NodeAgentID} {
		res, err := agentHubCommandFn(agentID, op, map[string]string{"instance": t.Instance}, agentTunnelCmdTimeout)
		if err != nil {
			failures = append(failures, agentID+": "+err.Error())
			continue
		}
		if !res.OK {
			failures = append(failures, agentID+": "+res.Error)
		}
	}
	agentTunnelsMu.Lock()
	s := loadAgentTunnels()
	for i := range s.Tunnels {
		if s.Tunnels[i].ID == id {
			if restart {
				// keep state; surface command failures as error only when both fail
				if len(failures) == 2 {
					s.Tunnels[i].State = agentTunnelStateError
					s.Tunnels[i].Error = strings.Join(failures, "; ")
				} else {
					s.Tunnels[i].Error = ""
					if s.Tunnels[i].State == agentTunnelStateError {
						s.Tunnels[i].State = agentTunnelStateActive
					}
				}
			} else if active {
				s.Tunnels[i].Active = true
				s.Tunnels[i].Error = ""
				if s.Tunnels[i].State == agentTunnelStateStopped || s.Tunnels[i].State == agentTunnelStateError {
					s.Tunnels[i].State = agentTunnelStateActive
				}
			} else {
				s.Tunnels[i].Active = false
				s.Tunnels[i].State = agentTunnelStateStopped
			}
		}
	}
	err := saveAgentTunnels(s)
	agentTunnelsMu.Unlock()
	if err != nil {
		writeAPIError(w, r, "E-AGENT-05", err.Error())
		return
	}
	if len(failures) > 0 {
		writeAPIError(w, r, "E-AGENT-06", strings.Join(failures, "; "))
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func handleAgentTunnelDelete(w http.ResponseWriter, r *http.Request) {
	if !agentTunnelHubWired() {
		writeAPIError(w, r, "E-AGENT-04", "agent hub is not available")
		return
	}
	id := r.PathValue("id")
	agentTunnelsMu.Lock()
	t, _, ok := findAgentTunnel(id)
	if !ok {
		agentTunnelsMu.Unlock()
		writeAPIError(w, r, "E-AGENT-03", "tunnel not found")
		return
	}
	s := loadAgentTunnels()
	kept := s.Tunnels[:0]
	for _, x := range s.Tunnels {
		if x.ID != id {
			kept = append(kept, x)
		}
	}
	s.Tunnels = kept
	err := saveAgentTunnels(s)
	agentTunnelsMu.Unlock()
	if err != nil {
		writeAPIError(w, r, "E-AGENT-05", err.Error())
		return
	}
	// Best-effort teardown on both agents: each removes its local instance.
	removeDesired := func(agentID string) {
		d := agentDesired{Revision: agentNewRevision(), Matrix: &agentDesiredMatrix{Instance: t.Instance, Action: "remove"}}
		_ = agentHubDeliverDesiredFn(agentID, d)
	}
	go removeDesired(t.HubAgentID)
	go removeDesired(t.NodeAgentID)
	LogSecurityAudit("agent_tunnel_delete", cfg.Username, ClientIP(r), "id="+id)
	writeJSON(w, map[string]string{"status": "ok", "detail": "tunnel removed; both endpoints are tearing down their local Matrix instances"})
}

func capsInclude(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
