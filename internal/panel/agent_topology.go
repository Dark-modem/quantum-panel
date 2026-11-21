package panel

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
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// agentSeam is a mutex-guarded function seam. Production wires these once at
// startup (agentwire.go) and never touches them again; tests swap them and
// restore them afterwards — while hub sessions, ack observers, provisioning
// goroutines and delete teardowns may still be reading them, so plain func
// vars are data races under `-race` (Session 4 Linux CI closure).
type agentSeam[F any] struct {
	mu sync.RWMutex
	fn F
}

func (s *agentSeam[F]) get() F  { s.mu.RLock(); defer s.mu.RUnlock(); return s.fn }
func (s *agentSeam[F]) set(f F) { s.mu.Lock(); s.fn = f; s.mu.Unlock() }

var (
	agentHubDeliverDesiredFn agentSeam[func(id string, d agentDesired) error]
	agentHubCommandFn        agentSeam[func(id, op string, params map[string]string, timeout time.Duration) (agentCommandResult, error)]
	agentHubOnlineFn         agentSeam[func(id string) bool]
	agentRegistryLookupFn    agentSeam[func(id string) (agentTunnelAgentInfo, bool)]
)

func agentTunnelHubWired() bool {
	return agentHubDeliverDesiredFn.get() != nil && agentHubCommandFn.get() != nil && agentHubOnlineFn.get() != nil
}

// agentTunnelAgent fetches registry info, tolerating an unwired registry.
func agentTunnelAgent(id string) (agentTunnelAgentInfo, bool) {
	lookup := agentRegistryLookupFn.get()
	if lookup == nil {
		return agentTunnelAgentInfo{ID: id}, false
	}
	return lookup(id)
}

// ---- tunnel store: <configDir>/agent_tunnels.json (0600) ----

// Tunnel lifecycle states persisted in the record: "provisioning" (hub
// desired in flight), "registering" (node desired in flight), "active",
// "stopped" (powered off), "error", and "deleting" — a tombstone: the
// delete was requested and the record stays until both agents acked their
// teardown desired, keeping the matrix instance name reserved and the
// remove revisions available for the ack observer.
const (
	agentTunnelStateProvisioning = "provisioning"
	agentTunnelStateRegistering  = "registering"
	agentTunnelStateActive       = "active"
	agentTunnelStateStopped      = "stopped"
	agentTunnelStateError        = "error"
	agentTunnelStateDeleting     = "deleting"
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

	// Deleting-tombstone teardown acks (audit F2): each flips true when
	// that agent acked its remove desired; both true ⇒ the tombstone is
	// dropped and the instance name freed.
	HubTeardownOK  bool `json:"hub_teardown_ok,omitempty"`
	NodeTeardownOK bool `json:"node_teardown_ok,omitempty"`

	CreatedAt int64 `json:"created_at"`
}

type agentTunnelStore struct {
	Version int           `json:"version"`
	Tunnels []agentTunnel `json:"tunnels"`
}

var (
	agentTunnelsMu      sync.Mutex
	agentTunnelsPathVal atomic.Value // string
)

func agentTunnelsFile() string {
	if v := agentTunnelsPathVal.Load(); v != nil {
		return v.(string)
	}
	p := filepath.Join(configDir, "agent_tunnels.json")
	agentTunnelsPathVal.Store(p)
	return p
}

// setAgentTunnelsPath is the test seam for redirecting the tunnel store
// (atomic: the file path is read by hub sessions, ack observers and
// provisioning goroutines that can outlive the test that rotates it).
func setAgentTunnelsPath(p string) { agentTunnelsPathVal.Store(p) }

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
// hub agent, based on the tunnels already assigned to it. The pool is the
// full valid range (matrix plus matrix2..matrix<max+1>) — one tunnel engine
// must never run out of disjoint instances before the server does.
func agentTunnelNextInstance(tunnels []agentTunnel, hubAgentID string) string {
	used := map[string]bool{}
	for _, t := range tunnels {
		if t.HubAgentID == hubAgentID {
			used[t.Instance] = true
		}
	}
	for n := 0; n <= matrixMaxInstances+1; n++ {
		if n == 1 {
			continue // matrix1 was never a valid instance id
		}
		cand := "matrix"
		if n > 0 {
			cand = "matrix" + strconv.Itoa(n)
		}
		if !used[cand] {
			return cand
		}
	}
	return ""
}

// agentTunnelWG tracks in-flight provisioning goroutines (test quiesce).
var agentTunnelWG sync.WaitGroup

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
			if t.State == agentTunnelStateDeleting {
				// Teardown ack (F2): a failed remove stays on the
				// tombstone for the next reconcile retry; a good one
				// completes this side.
				t.HubTeardownOK = ack.OK
				if ack.OK {
					t.Error = ""
				} else {
					t.Error = "hub teardown: " + ack.Error
				}
			} else if ack.OK {
				// Preserve ack artifacts across controller restarts (F3):
				// the provisioning goroutine that normally records
				// main_host may be gone by the time this ack arrives.
				if mh := ack.Detail["main_host"]; mh != "" {
					t.HubMainHost = mh
				}
			}
			changed = true
		}
		if t.NodeAgentID == agentID && t.NodeRevision == ack.Revision {
			t.NodeAckOK, t.NodeAckError = ack.OK, ack.Error
			if t.State == agentTunnelStateDeleting {
				t.NodeTeardownOK = ack.OK
				if ack.OK {
					t.Error = ""
				} else {
					t.Error = "node teardown: " + ack.Error
				}
			} else if ack.OK && t.State == agentTunnelStateRegistering {
				t.State = agentTunnelStateActive
				t.Error = ""
			} else if !ack.OK && t.State == agentTunnelStateRegistering {
				t.State = agentTunnelStateError
				t.Error = "node: " + ack.Error
			}
			changed = true
		}
	}
	if changed {
		// A tombstone whose both endpoints acked the teardown is fully
		// cleaned up: drop it (freeing the instance name) in this pass.
		kept := s.Tunnels[:0]
		for _, x := range s.Tunnels {
			if x.State == agentTunnelStateDeleting && x.HubTeardownOK && x.NodeTeardownOK {
				log.Printf("agent tunnel %s: both endpoints acked teardown; record removed", x.ID)
				continue
			}
			kept = append(kept, x)
		}
		s.Tunnels = kept
		if err := saveAgentTunnels(s); err != nil {
			log.Printf("agent tunnel: save after ack: %v", err)
		}
	}
}

// ---- provisioning orchestration ----

// Wait budgets are vars so tests can shrink them (and so an in-flight
// provisioning goroutine left over from a finished test never lingers).
var (
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
	// Snapshot the hub seams once: the wiring must not change under an
	// in-flight provisioning run (tests restore fakes mid-flight).
	deliverFn, cmdFn := agentHubDeliverDesiredFn.get(), agentHubCommandFn.get()
	if deliverFn == nil || cmdFn == nil || !agentTunnelHubWired() {
		log.Printf("agent tunnel %s: agent hub not wired; provisioning aborted", tunnelID)
		return
	}
	agentTunnelsMu.Lock()
	s := loadAgentTunnels()
	var t *agentTunnel
	for i := range s.Tunnels {
		if s.Tunnels[i].ID == tunnelID {
			t = &s.Tunnels[i]
			break
		}
	}
	// Only a tunnel that still wants provisioning may run the pipeline:
	// reconcile re-runs it for incomplete records, never for active,
	// stopped or deleting ones (a deleting tombstone is owned by the
	// delete flow).
	if t == nil || t.State != agentTunnelStateProvisioning {
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
				if s.Tunnels[i].State == agentTunnelStateDeleting {
					continue // the delete flow owns this record now
				}
				s.Tunnels[i].State = agentTunnelStateError
				s.Tunnels[i].Error = stage + ": " + msg
			}
		}
		_ = saveAgentTunnels(s)
		agentTunnelsMu.Unlock()
		log.Printf("agent tunnel %s: %s: %s", tunnelID, stage, msg)
	}

	if hubAddr == "" {
		fail("hub", fmt.Sprintf("hub agent %s has no usable public address (never connected, or enrolled from a private address; set one explicitly via POST /api/mesh/agents/%s/public-ip)", t.HubAgentID, t.HubAgentID))
		return
	}

	// 1. Hub desired — skipped when resuming a record whose hub stage
	// already completed before a controller restart (F3): re-applying to
	// the hub would restart a healthy tinc instance for nothing. The
	// agent may still re-ack the old revision; the ack observer keeps the
	// ack fields fresh and preserves main_host in that case.
	hubStageDone := job.HubAckOK && job.HubMainHost != ""
	if hubStageDone {
		log.Printf("agent tunnel %s: resuming after hub stage (instance %s)", tunnelID, job.Instance)
	} else {
		hubRev := agentNewRevision()
		hubCh := agentTunnelArmWait(job.HubAgentID, hubRev)
		defer agentTunnelDisarmWait(job.HubAgentID, hubRev)
		if err := deliverFn(job.HubAgentID, agentTunnelHubDesired(&job, hubAddr, hubRev)); err != nil {
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
	}

	// Orphan guard (F1): the tunnel may have been deleted or powered off
	// while the hub ack was in flight. A deleting tombstone is owned by
	// the delete flow (its removes are already in flight); a vanished
	// record gets a best-effort teardown so no instance is left running.
	gate, gateOK := agentTunnelStageGate(tunnelID, agentTunnelStateProvisioning)
	if !gateOK {
		if gate.ID == "" {
			log.Printf("agent tunnel %s: record vanished mid-provision; best-effort teardown", tunnelID)
			agentTunnelBestEffortRemove(job.HubAgentID, job.Instance)
			agentTunnelBestEffortRemove(job.NodeAgentID, job.Instance)
		} else {
			log.Printf("agent tunnel %s: provisioning aborted (%s owns the record now)", tunnelID, gate.State)
		}
		return
	}
	job = gate

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
	if job.HubRevision == "" {
		fail("node", "hub revision missing from the tunnel record; cannot build the node bundle")
		return
	}
	// The bundle's Revision must equal what the hub stored in its matrix
	// state (the hub-desired revision, persisted on the record so a resumed
	// pipeline rebuilds the identical bundle): matrixRegisterHub refuses a
	// node whose revision differs — a fresh revision here guaranteed a 409.
	bundle, err := agentTunnelNodeBundle(&job, hubAddr, meshPort, job.HubRevision)
	if err != nil {
		fail("node", err.Error())
		return
	}

	// 3. Node desired — gated atomically (F1): between the hub ack and
	// here the tunnel may have been deleted or powered off; the atomic
	// transition refuses to hand the registering state to a record that
	// no longer wants it, so no node bundle is ever delivered for a
	// tunnel the controller has stopped tracking.
	nodeRev := agentNewRevision()
	if !agentTunnelBeginRegistering(tunnelID, nodeRev) {
		log.Printf("agent tunnel %s: aborted before node stage (delete/power owns the record)", tunnelID)
		return
	}
	nodeCh := agentTunnelArmWait(job.NodeAgentID, nodeRev)
	defer agentTunnelDisarmWait(job.NodeAgentID, nodeRev)
	nodeDesired := agentDesired{Revision: nodeRev, Matrix: &agentDesiredMatrix{Instance: job.Instance, Action: "apply", Bundle: bundle}}
	if err := deliverFn(job.NodeAgentID, nodeDesired); err != nil {
		fail("node", err.Error())
		return
	}
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
			if s.Tunnels[i].State == agentTunnelStateDeleting {
				continue // never resurrect a tombstone
			}
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

// agentTunnelStageGate atomically re-checks a tunnel between provisioning
// stages (audit F1). gateOK is false when the record vanished, was deleted
// (state deleting — the delete flow owns teardown), lost desired power, or
// left the expected state. A vanished record is reported with an empty ID
// so the caller can send a best-effort teardown.
func agentTunnelStageGate(tunnelID, want string) (agentTunnel, bool) {
	agentTunnelsMu.Lock()
	defer agentTunnelsMu.Unlock()
	s := loadAgentTunnels()
	for i := range s.Tunnels {
		if s.Tunnels[i].ID != tunnelID {
			continue
		}
		t := s.Tunnels[i]
		if t.State != want || t.State == agentTunnelStateDeleting || !t.Active {
			return t, false
		}
		return t, true
	}
	return agentTunnel{}, false
}

// agentTunnelBeginRegistering atomically moves a provisioning tunnel to
// registering and arms its node revision. It refuses (and the caller must
// abort the node stage) when the record vanished, is a deleting tombstone,
// lost desired power, or left the provisioning state.
func agentTunnelBeginRegistering(tunnelID, nodeRev string) bool {
	agentTunnelsMu.Lock()
	defer agentTunnelsMu.Unlock()
	s := loadAgentTunnels()
	for i := range s.Tunnels {
		if s.Tunnels[i].ID != tunnelID {
			continue
		}
		if s.Tunnels[i].State != agentTunnelStateProvisioning || !s.Tunnels[i].Active {
			return false
		}
		s.Tunnels[i].State = agentTunnelStateRegistering
		s.Tunnels[i].NodeRevision = nodeRev
		_ = saveAgentTunnels(s)
		return true
	}
	return false
}

// agentTunnelBestEffortRemove delivers one remove desired without error
// handling; used by the orphan guard when a record vanished entirely.
func agentTunnelBestEffortRemove(agentID, instance string) {
	deliver := agentHubDeliverDesiredFn.get()
	if deliver == nil {
		return
	}
	d := agentDesired{Revision: agentNewRevision(), Matrix: &agentDesiredMatrix{Instance: instance, Action: "remove"}}
	_ = deliver(agentID, d)
}

// ---- startup reconciliation (audit F3) ----

// agentTunnelTombstoneTTL bounds how long a deleting tombstone may reserve
// its instance name while waiting for teardown acks from agents that may
// never reconnect. reconcileAgentTunnels drops older tombstones.
const agentTunnelTombstoneTTL = 24 * time.Hour

// reconcileAgentTunnels scans the persisted tunnel store when the
// controller starts and returns the work the previous process left
// unfinished. provisioning/registering records (and powered-on error
// records) are re-run through the resume-aware pipeline with FRESH
// revisions: agents refuse stale revisions and treat equal ones as
// no-ops, and every stage re-applies the same instance, so a reconcile can
// never duplicate a service, a port or a firewall rule. deleting
// tombstones get their teardown re-delivered once; tombstones older than
// the TTL are dropped, freeing the instance name. The returned lists are
// meant to be driven exactly once — there is no retry timer anywhere.
func reconcileAgentTunnels() (provision, teardown []string) {
	if !agentTunnelHubWired() {
		return nil, nil
	}
	agentTunnelsMu.Lock()
	s := loadAgentTunnels()
	changed := false
	// First pass: drop tombstones past their TTL (frees the instance name).
	kept := s.Tunnels[:0]
	for _, x := range s.Tunnels {
		if x.State == agentTunnelStateDeleting && time.Since(time.Unix(x.CreatedAt, 0)) > agentTunnelTombstoneTTL {
			log.Printf("agent tunnel %s: tombstone older than %s; dropping (instance %s freed)", x.ID, agentTunnelTombstoneTTL, x.Instance)
			changed = true
			continue
		}
		kept = append(kept, x)
	}
	s.Tunnels = kept
	// Second pass: collect the unfinished work.
	for i := range s.Tunnels {
		t := &s.Tunnels[i]
		switch t.State {
		case agentTunnelStateProvisioning, agentTunnelStateRegistering:
			// Resume: the pipeline skips the hub stage when the hub ack
			// and host file survived on the record.
			t.State = agentTunnelStateProvisioning
			provision = append(provision, t.ID)
			changed = true
		case agentTunnelStateError:
			if t.Active {
				t.State = agentTunnelStateProvisioning
				t.Error = ""
				provision = append(provision, t.ID)
				changed = true
			}
		case agentTunnelStateDeleting:
			t.HubRevision, t.NodeRevision = agentNewRevision(), agentNewRevision()
			t.HubTeardownOK, t.NodeTeardownOK = false, false
			teardown = append(teardown, t.ID)
			changed = true
		}
	}
	if changed {
		if err := saveAgentTunnels(s); err != nil {
			log.Printf("agent tunnel: reconcile save: %v", err)
		}
	}
	agentTunnelsMu.Unlock()
	return provision, teardown
}

// agentTunnelRedeliverTeardown re-delivers the remove desired for a
// deleting tombstone with the revisions reconcileAgentTunnels armed.
func agentTunnelRedeliverTeardown(tunnelID string) {
	deliverFn := agentHubDeliverDesiredFn.get()
	if deliverFn == nil {
		return
	}
	agentTunnelsMu.Lock()
	t, _, ok := findAgentTunnel(tunnelID)
	agentTunnelsMu.Unlock()
	if !ok || t.State != agentTunnelStateDeleting {
		return
	}
	for _, side := range []struct{ agentID, rev string }{{t.HubAgentID, t.HubRevision}, {t.NodeAgentID, t.NodeRevision}} {
		d := agentDesired{Revision: side.rev, Matrix: &agentDesiredMatrix{Instance: t.Instance, Action: "remove"}}
		if err := deliverFn(side.agentID, d); err != nil {
			log.Printf("agent tunnel %s: teardown redelivery to %s: %v", tunnelID, side.agentID, err)
		}
	}
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
	if nodeAddr == "" {
		// The node's address goes into the hub's matrix.register params;
		// an empty one would register an unusable node (Phase-4: the
		// loopback-enroll poison). Fail with the fix, not a mystery.
		return agentRegisterResult{OK: false, Error: fmt.Sprintf("node agent %s has no usable public address (enrolled from a private address; set one explicitly via POST /api/mesh/agents/%s/public-ip)", t.NodeAgentID, t.NodeAgentID)}
	}
	params := map[string]string{
		"instance":  t.Instance,
		"token":     reg.Token,
		"name":      reg.Name,
		"host_file": reg.HostFile,
		"addr":      nodeAddr,
		"revision":  reg.Revision,
	}
	res, err := agentHubCommandFn.get()(t.HubAgentID, agentCmdMatrixRegister, params, agentTunnelCmdTimeout)
	// A hub agent that dropped its WS for a moment fails fast; the node
	// only waits ~30s for this answer, so retry exactly once on a fast
	// failure and never on a slow (timeout) one.
	if err != nil && time.Since(start) < 5*time.Second {
		time.Sleep(7 * time.Second)
		res, err = agentHubCommandFn.get()(t.HubAgentID, agentCmdMatrixRegister, params, agentTunnelCmdTimeout)
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

// agentTrafficSummary sums one agent's rates by interface kind.
//
// billable_amplification is a SERVER-BASIS ESTIMATE, not a validated
// per-tunnel measurement: physical NIC RX is the billable direction and
// qmx TX is the payload pushed into the tunnel (downloads only — uploads
// ride qmx RX/NIC TX and are excluded by construction). The physical
// counter also carries SSH, panel and probe traffic, so this is a
// SERVER-WIDE ESTIMATE, not a Matrix-specific measurement and not a
// bound. It stays UNKNOWN (nil) whenever the counters are unavailable or
// unreliable (reset, stale heartbeat, unknown iface); Matrix-owned
// counters (the QMX_FWD iptables byte counts, read-only) would be needed
// for a per-tunnel figure and are deliberately not claimed yet.
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
		"basis": "server", // physical NIC covers more than Matrix
	}
	if nicRx > 0 && tunTx > 0 {
		out["billable_amplification"] = nicRx / tunTx
	} else {
		out["billable_amplification"] = nil // UNKNOWN, not zero
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
			if online := agentHubOnlineFn.get(); online != nil {
				v["online"] = online(agentID)
			}
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
	// Refuse a doomed create (audit F2): while an endpoint still holds an
	// unacked teardown desired, the delivery guard would refuse the new
	// work anyway — say so now instead of parking a tunnel in error.
	for _, agentID := range []string{q.HubAgentID, q.NodeAgentID} {
		if pd := agentPendingDesired(agentID); pd != nil && pd.Matrix != nil && pd.Matrix.Action == "remove" {
			writeAPIError(w, r, "E-AGENT-06", "agent "+agentID+" still has a pending teardown; create again once it completes")
			return
		}
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

	agentTunnelWG.Add(1)
	go func() { defer agentTunnelWG.Done(); agentTunnelProvision(t.ID) }()
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
	deleting := t.State == agentTunnelStateDeleting
	agentTunnelsMu.Unlock()
	if deleting {
		writeAPIError(w, r, "E-AGENT-05", "tunnel is being deleted")
		return
	}

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
		res, err := agentHubCommandFn.get()(agentID, op, map[string]string{"instance": t.Instance}, agentTunnelCmdTimeout)
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
	// Tombstone instead of removal (audit F2): the record survives with
	// state=deleting until both agents acked their remove desired. It
	// keeps reserving the matrix instance name (no reuse collision while
	// the old instance may still run) and carries the teardown revisions
	// so the ack observer can recognize the removes. reconcileAgentTunnels
	// re-delivers teardowns for tombstones at startup and drops them after
	// the TTL if an endpoint never returns.
	hubRev, nodeRev := agentNewRevision(), agentNewRevision()
	s := loadAgentTunnels()
	for i := range s.Tunnels {
		if s.Tunnels[i].ID != id {
			continue
		}
		s.Tunnels[i].State = agentTunnelStateDeleting
		s.Tunnels[i].HubRevision, s.Tunnels[i].NodeRevision = hubRev, nodeRev
		s.Tunnels[i].HubTeardownOK, s.Tunnels[i].NodeTeardownOK = false, false
		s.Tunnels[i].Error = ""
	}
	err := saveAgentTunnels(s)
	agentTunnelsMu.Unlock()
	if err != nil {
		writeAPIError(w, r, "E-AGENT-05", err.Error())
		return
	}
	// Teardown desired to both agents: each removes its local instance.
	// An offline agent is not an error — the hub keeps the desired queued
	// and delivers it on the next connect; the delivery guard keeps that
	// queued teardown from being clobbered by later work.
	removeDesired := func(agentID, rev string) {
		d := agentDesired{Revision: rev, Matrix: &agentDesiredMatrix{Instance: t.Instance, Action: "remove"}}
		if err := agentDeliverTeardownDesired(agentID, d); err != nil {
			log.Printf("agent tunnel %s: teardown desired to %s: %v", id, agentID, err)
		}
	}
	go removeDesired(t.HubAgentID, hubRev)
	go removeDesired(t.NodeAgentID, nodeRev)
	LogSecurityAudit("agent_tunnel_delete", cfg.Username, ClientIP(r), "id="+id)
	writeJSON(w, map[string]string{"status": "ok", "detail": "tunnel tombstoned; both endpoints are tearing down their local Matrix instances"})
}

// agentDeliverTeardownDesired delivers one teardown desired state. It is
// nil-safe by design: the delete handler checks agentTunnelHubWired() once,
// but these deliveries run in goroutines that may still be in flight when
// another test (or a future rewiring) rotates the seam back to nil —
// calling a nil func there would panic and kill the whole process
// (Session 4 gate finding, snapshot wt-s4-158).
func agentDeliverTeardownDesired(agentID string, d agentDesired) error {
	deliver := agentHubDeliverDesiredFn.get()
	if deliver == nil {
		return errors.New("agent hub seam unwired; teardown desired skipped")
	}
	return deliver(agentID, d)
}

func capsInclude(caps []string, want string) bool {
	for _, c := range caps {
		if c == want {
			return true
		}
	}
	return false
}
