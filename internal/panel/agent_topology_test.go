package panel

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"
	"time"
)

// agentTunnelLab redirects the tunnel store at a temp dir and installs
// fake hub/registry seams. Returns a harness the tests drive.
type agentTunnelLab struct {
	t            *testing.T
	mu           sync.Mutex
	delivered    map[string][]agentDesired // agentID -> desired list
	commands     []agentCommandCall
	records      map[string]agentTunnelAgentInfo
	online       map[string]bool
	prevConfigMu sync.Mutex
}

type agentCommandCall struct {
	ID     string
	Op     string
	Params map[string]string
}

func newAgentTunnelLab(t *testing.T) *agentTunnelLab {
	t.Helper()
	dir := t.TempDir()
	oldDir := configDir
	configDir = dir
	setAgentTunnelsPath(filepath.Join(dir, "agent_tunnels.json"))
	t.Cleanup(func() {
		configDir = oldDir
		setAgentTunnelsPath("") // back to the configDir default
	})
	lab := &agentTunnelLab{
		t:         t,
		delivered: map[string][]agentDesired{},
		records:   map[string]agentTunnelAgentInfo{},
		online:    map[string]bool{},
	}
	oldDeliver, oldCmd, oldOnline, oldLookup := agentHubDeliverDesiredFn.get(), agentHubCommandFn.get(), agentHubOnlineFn.get(), agentRegistryLookupFn.get()
	oldAckWait, oldPortWait, oldCmdTimeout := agentTunnelAckWait, agentTunnelPortWait, agentTunnelCmdTimeout
	agentTunnelAckWait, agentTunnelPortWait, agentTunnelCmdTimeout = 2*time.Second, 200*time.Millisecond, 2*time.Second
	agentHubDeliverDesiredFn.set(func(id string, d agentDesired) error {
		lab.mu.Lock()
		lab.delivered[id] = append(lab.delivered[id], d)
		lab.mu.Unlock()
		return nil
	})
	agentHubCommandFn.set(func(id, op string, params map[string]string, timeout time.Duration) (agentCommandResult, error) {
		lab.mu.Lock()
		lab.commands = append(lab.commands, agentCommandCall{ID: id, Op: op, Params: params})
		lab.mu.Unlock()
		return agentCommandResult{OK: true}, nil
	})
	agentHubOnlineFn.set(func(id string) bool {
		lab.mu.Lock()
		defer lab.mu.Unlock()
		return lab.online[id]
	})
	agentRegistryLookupFn.set(func(id string) (agentTunnelAgentInfo, bool) {
		lab.mu.Lock()
		defer lab.mu.Unlock()
		rec, ok := lab.records[id]
		return rec, ok
	})
	t.Cleanup(func() {
		agentHubDrainAll()
		agentTunnelWG.Wait()
		AgentObserverWait() // observers still read the seams about to be restored
		agentHubDeliverDesiredFn.set(oldDeliver)
		agentHubCommandFn.set(oldCmd)
		agentHubOnlineFn.set(oldOnline)
		agentRegistryLookupFn.set(oldLookup)
		agentTunnelAckWait, agentTunnelPortWait, agentTunnelCmdTimeout = oldAckWait, oldPortWait, oldCmdTimeout
	})
	return lab
}

func (l *agentTunnelLab) addAgent(id, name, role, ip string, caps []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records[id] = agentTunnelAgentInfo{ID: id, Name: name, Role: role, Status: "offline", PublicIP: ip, Caps: caps}
}

func (l *agentTunnelLab) setLastState(agentID string, st *agentStateReport) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.records[agentID]
	rec.LastState = st
	l.records[agentID] = rec
}

func (l *agentTunnelLab) desiresFor(agentID string) []agentDesired {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]agentDesired(nil), l.delivered[agentID]...)
}

func (l *agentTunnelLab) commandCalls() []agentCommandCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]agentCommandCall(nil), l.commands...)
}

func agentTestRSAPem(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}))
}

func TestAgentTunnelNextInstance(t *testing.T) {
	tunnels := []agentTunnel{{ID: "at_1", HubAgentID: "h1", Instance: "matrix"}}
	if got := agentTunnelNextInstance(tunnels, "h1"); got != "matrix2" {
		t.Fatalf("expected matrix2, got %q", got)
	}
	if got := agentTunnelNextInstance(tunnels, "h2"); got != "matrix" {
		t.Fatalf("expected matrix for other hub, got %q", got)
	}
}

func TestAgentTunnelNodeBundle(t *testing.T) {
	tun := agentTunnel{Instance: "matrix", Family: "ipv4", Ports: []int{8080}, Token: strings.Repeat("a", 32), HubMainHost: "Subnet = 10.254.231.1/32\n" + agentTestRSAPem(t)}
	b, err := agentTunnelNodeBundle(&tun, "185.10.10.10", 2097, "rev1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(b, matrixBundlePfx) {
		t.Fatalf("bundle must carry the %s prefix", matrixBundlePfx)
	}
	payload, in, err := parseMatrixBundleFor(b)
	if err != nil {
		t.Fatalf("controller-built bundle must parse: %v", err)
	}
	if payload.FIP != in.firstPeer() || payload.Token != tun.Token || payload.Port != 2097 || payload.Network != in.network() {
		t.Fatalf("bundle payload mismatch: %+v", payload)
	}
}

func TestAgentTunnelNodeBundleMissingHost(t *testing.T) {
	tun := agentTunnel{Instance: "matrix", Family: "ipv4", Token: strings.Repeat("a", 32)}
	if _, err := agentTunnelNodeBundle(&tun, "1.2.3.4", 2097, "r"); err == nil {
		t.Fatal("expected error without hub host file")
	}
}

func TestAgentTunnelProvisionEndToEnd(t *testing.T) {
	lab := newAgentTunnelLab(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	lab.online["hub1"] = true
	lab.online["node1"] = true

	// Store a tunnel directly, then run the provisioning job.
	s := loadAgentTunnels()
	tun := agentTunnel{ID: "at_x", Name: "first", Instance: "matrix", HubAgentID: "hub1", NodeAgentID: "node1",
		Family: "ipv4", MeshPort: 2097, Ports: []int{8080}, Token: strings.Repeat("t", 32), Active: true,
		State: agentTunnelStateProvisioning, CreatedAt: time.Now().Unix()}
	s.Tunnels = append(s.Tunnels, tun)
	if err := saveAgentTunnels(s); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Ack each side independently as soon as its desired lands: the
		// node desired is only delivered after the hub ack arrives.
		var hubAcked, nodeAcked bool
		for !(hubAcked && nodeAcked) {
			lab.mu.Lock()
			hubWant := len(lab.delivered["hub1"])
			nodeWant := len(lab.delivered["node1"])
			lab.mu.Unlock()
			if !hubAcked && hubWant >= 1 {
				hubAcked = true
				hubDesired := lab.desiresFor("hub1")[0]
				agentTunnelAckObserved("hub1", agentAck{Revision: hubDesired.Revision, OK: true,
					Detail: map[string]string{"main_host": "Subnet = 10.254.231.1/32\n" + agentTestRSAPem(t)}})
				AgentObserverWait()
				continue
			}
			if !nodeAcked && nodeWant >= 1 {
				nodeAcked = true
				nodeDesired := lab.desiresFor("node1")[0]
				agentTunnelAckObserved("node1", agentAck{Revision: nodeDesired.Revision, OK: true})
				continue
			}
			time.Sleep(10 * time.Millisecond)
			AgentObserverWait()
		}
	}()
	agentTunnelProvision("at_x")
	<-done

	s = loadAgentTunnels()
	if len(s.Tunnels) != 1 || s.Tunnels[0].State != agentTunnelStateActive {
		t.Fatalf("tunnel should end active, got %+v", s.Tunnels)
	}
	if !strings.Contains(s.Tunnels[0].HubMainHost, "RSA PUBLIC KEY") {
		t.Fatal("hub main host file must be stored from the ack detail")
	}
	if s.Tunnels[0].MeshPort != 2097 {
		t.Fatalf("mesh port must persist, got %d", s.Tunnels[0].MeshPort)
	}
	hubDesired := lab.desiresFor("hub1")[0]
	if hubDesired.Matrix == nil || hubDesired.Matrix.Hub == nil || hubDesired.Matrix.Hub.Addr != "185.1.1.1" || hubDesired.Matrix.Hub.Token != tun.Token {
		t.Fatalf("hub desired payload wrong: %+v", hubDesired.Matrix)
	}
	nodeDesired := lab.desiresFor("node1")[0]
	if nodeDesired.Matrix == nil || nodeDesired.Matrix.Bundle == "" {
		t.Fatal("node desired must carry the hmx1 bundle")
	}
	if _, _, err := parseMatrixBundleFor(nodeDesired.Matrix.Bundle); err != nil {
		t.Fatalf("node bundle must parse: %v", err)
	}
}

func TestAgentTunnelProvisionHubAckFail(t *testing.T) {
	lab := newAgentTunnelLab(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})

	s := loadAgentTunnels()
	s.Tunnels = append(s.Tunnels, agentTunnel{ID: "at_y", Instance: "matrix", HubAgentID: "hub1", NodeAgentID: "node1",
		Family: "ipv4", MeshPort: 2097, Token: strings.Repeat("t", 32), State: agentTunnelStateProvisioning})
	_ = saveAgentTunnels(s)

	go func() {
		for {
			lab.mu.Lock()
			got := len(lab.delivered["hub1"])
			lab.mu.Unlock()
			if got >= 1 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		hubDesired := lab.desiresFor("hub1")[0]
		agentTunnelAckObserved("hub1", agentAck{Revision: hubDesired.Revision, OK: false, Error: "port busy"})
	}()
	AgentObserverWait()
	agentTunnelProvision("at_y")

	s = loadAgentTunnels()
	if s.Tunnels[0].State != agentTunnelStateError || !strings.Contains(s.Tunnels[0].Error, "port busy") {
		t.Fatalf("expected error state, got %+v", s.Tunnels[0])
	}
	if len(lab.desiresFor("node1")) != 0 {
		t.Fatal("node must not receive desired state after hub failure")
	}
}

func TestAgentTunnelRegisterRelay(t *testing.T) {
	lab := newAgentTunnelLab(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	s := loadAgentTunnels()
	s.Tunnels = append(s.Tunnels, agentTunnel{ID: "at_z", Instance: "matrix", HubAgentID: "hub1", NodeAgentID: "node1",
		Token: strings.Repeat("k", 32), State: agentTunnelStateRegistering})
	_ = saveAgentTunnels(s)

	res := agentTunnelRegisterRelay(agentRegisterMsg{Token: strings.Repeat("k", 32), Name: "nodeabcd12", HostFile: "hostfile", Revision: "rev"}, "node1")
	if !res.OK {
		t.Fatalf("relay should succeed: %+v", res)
	}
	calls := lab.commandCalls()
	if len(calls) != 1 || calls[0].Op != agentCmdMatrixRegister || calls[0].ID != "hub1" {
		t.Fatalf("expected one matrix.register command to the hub, got %+v", calls)
	}
	if calls[0].Params["host_file"] != "hostfile" || calls[0].Params["instance"] != "matrix" {
		t.Fatalf("relay params wrong: %+v", calls[0].Params)
	}

	// Wrong agent must be refused.
	res = agentTunnelRegisterRelay(agentRegisterMsg{Token: strings.Repeat("k", 32), Name: "x", HostFile: "h", Revision: "r"}, "hub1")
	if res.OK {
		t.Fatal("registration from the hub agent itself must be refused")
	}
	// Unknown token must be refused.
	res = agentTunnelRegisterRelay(agentRegisterMsg{Token: strings.Repeat("z", 32), Name: "x", HostFile: "h", Revision: "r"}, "node1")
	if res.OK {
		t.Fatal("unknown token must be refused")
	}
}

func TestAgentTunnelsPostValidation(t *testing.T) {
	lab := newAgentTunnelLab(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.addAgent("node1", "foreign", "foreign", "5.6.7.8", []string{agentCapMatrix})
	lab.addAgent("nocap", "no-cap", "iran", "1.1.1.1", []string{agentCapTraffic})

	post := func(body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/api/agent-tunnels", strings.NewReader(body))
		handleAgentTunnelsPost(rr, req)
		return rr
	}
	if rr := post(`{"name":"t","hub_agent_id":"hub1","node_agent_id":"hub1","family":"ipv4"}`); rr.Code != 400 {
		t.Fatalf("same agents must be rejected, got %d", rr.Code)
	}
	if rr := post(`{"name":"t","hub_agent_id":"hub1","node_agent_id":"node1","family":"gre"}`); rr.Code != 400 {
		t.Fatalf("bad family must be rejected, got %d", rr.Code)
	}
	if rr := post(`{"name":"t","hub_agent_id":"hub1","node_agent_id":"nocap","family":"ipv4"}`); rr.Code != 400 {
		t.Fatalf("agent without matrix cap must be rejected, got %d", rr.Code)
	}
	if rr := post(`{"name":"t","hub_agent_id":"hub1","node_agent_id":"node1","family":"ipv4","ports":[22]}`); rr.Code != 400 {
		t.Fatalf("port 22 must be rejected, got %d", rr.Code)
	}
	if rr := post(`{"name":"t","hub_agent_id":"hub1","node_agent_id":"node1","family":"ipv4","ports":[]}`); rr.Code != 400 {
		t.Fatalf("empty ports must be rejected (Matrix DNATs user ports), got %d", rr.Code)
	}
	rr := post(`{"name":"t","hub_agent_id":"hub1","node_agent_id":"node1","family":"ipv4","port":2097,"ports":[8080]}`)
	if rr.Code != 200 {
		t.Fatalf("valid create must pass, got %d: %s", rr.Code, rr.Body.String())
	}
	s := loadAgentTunnels()
	if len(s.Tunnels) != 1 || s.Tunnels[0].Instance != "matrix" || len(s.Tunnels[0].Token) < 32 {
		t.Fatalf("tunnel not stored correctly: %+v", s.Tunnels)
	}
}

func TestAgentTunnelHealthViewFromHubState(t *testing.T) {
	lab := newAgentTunnelLab(t)
	lab.addAgent("hub1", "iran", "iran", "185.1.1.1", []string{agentCapMatrix})
	lab.setLastState("hub1", &agentStateReport{Health: []agentTunnelHealth{{Instance: "matrix", LatencyMs: 42, LossPct: 0, Quality: "good"}}})
	tun := agentTunnel{Instance: "matrix", HubAgentID: "hub1"}
	h := agentTunnelHealthView(tun)
	if h == nil || h.LatencyMs != 42 || h.Quality != "good" {
		t.Fatalf("health view wrong: %+v", h)
	}
}

func TestAgentTunnelsFileRoundTrip(t *testing.T) {
	newAgentTunnelLab(t)
	s := agentTunnelStore{Version: 1, Tunnels: []agentTunnel{{ID: "at_1", Token: "sekrit"}}}
	if err := saveAgentTunnels(s); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(agentTunnelsFile())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0600 {
		t.Fatalf("agent_tunnels.json must be 0600, got %v", fi.Mode().Perm())
	}
	back := loadAgentTunnels()
	if len(back.Tunnels) != 1 || back.Tunnels[0].Token != "sekrit" {
		t.Fatalf("round trip failed: %+v", back)
	}
}

// ---- full controller-loop E2E: real hub server, real WebSocket clients,
// real orchestration and relay. Only the agents' matrix exec is absent
// (agent-side apply is covered by the agent package tests). ----

type agentE2EConn struct {
	t  *testing.T
	ws *websocket.Conn
	mu sync.Mutex
}

func (c *agentE2EConn) send(typ string, payload any) error {
	b, err := agentEncode(typ, payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteMessage(websocket.TextMessage, b)
}

func (c *agentE2EConn) read() (string, json.RawMessage, error) {
	var env agentEnvelope
	if err := c.ws.ReadJSON(&env); err != nil {
		return "", nil, err
	}
	return env.Type, env.Data, nil
}

func TestAgentTunnelControllerLoopRealHub(t *testing.T) {
	dir := t.TempDir()
	oldDir := configDir
	oldAck, oldPort, oldCmd := agentTunnelAckWait, agentTunnelPortWait, agentTunnelCmdTimeout
	configDir = dir
	setAgentTunnelsPath(filepath.Join(dir, "agent_tunnels.json"))
	agentTunnelAckWait, agentTunnelPortWait, agentTunnelCmdTimeout = 15*time.Second, 3*time.Second, 10*time.Second
	t.Cleanup(func() {
		configDir = oldDir
		agentTunnelAckWait, agentTunnelPortWait, agentTunnelCmdTimeout = oldAck, oldPort, oldCmd
	})

	// Wire the REAL hub functions (the production wiring).
	oldDeliver, oldCmdFn, oldOnline, oldLookup := agentHubDeliverDesiredFn.get(), agentHubCommandFn.get(), agentHubOnlineFn.get(), agentRegistryLookupFn.get()
	wireAgentSubsystem()
	t.Cleanup(func() {
		agentHubDeliverDesiredFn.set(oldDeliver)
		agentHubCommandFn.set(oldCmdFn)
		agentHubOnlineFn.set(oldOnline)
		agentRegistryLookupFn.set(oldLookup)
	})

	srv := agentHubTestServer(t)
	hubID, hubKey := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	nodeID, nodeKey := agentHubEnrollAgent(t, srv, []string{agentCapMatrix})
	hubWS := agentHubHandshakeCaps(t, srv.URL, hubID, hubKey, []string{agentCapMatrix})
	nodeWS := agentHubHandshakeCaps(t, srv.URL, nodeID, nodeKey, []string{agentCapMatrix})
	t.Cleanup(func() {
		hubWS.Close()
		nodeWS.Close()
		// The server-side session goroutines write the registry on their way
		// down; wait for both agents to read offline before the seams rotate.
		for i := 0; i < 40; i++ {
			_, ok1 := agentTunnelRegistryLookup(hubID)
			_, ok2 := agentTunnelRegistryLookup(nodeID)
			h, _ := agentTunnelRegistryLookup(hubID)
			n, _ := agentTunnelRegistryLookup(nodeID)
			_ = ok1
			_ = ok2
			if h.Status == "offline" && n.Status == "offline" {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	hub, node := &agentE2EConn{t: t, ws: hubWS}, &agentE2EConn{t: t, ws: nodeWS}

	pem := agentTestRSAPem(t)
	hubSaw := make(chan agentCommand, 4)
	// Hub brain: ack hub desired with the host file; answer register relay.
	go func() {
		for {
			typ, data, err := hub.read()
			if err != nil {
				return
			}
			switch typ {
			case agentMsgDesired:
				var d agentDesired
				if json.Unmarshal(data, &d) != nil {
					continue
				}
				_ = hub.send(agentMsgAck, agentAck{Revision: d.Revision, OK: true,
					Detail: map[string]string{"main_host": "Subnet = 10.254.231.1/32\n" + pem}})
			case agentMsgCommand:
				var cmd agentCommand
				if json.Unmarshal(data, &cmd) != nil {
					continue
				}
				hubSaw <- cmd
				_ = hub.send(agentMsgResult, agentCommandResult{ID: cmd.ID, OK: true, Output: "paired"})
			}
		}
	}()
	// Node brain: ack node desired and send the pairing register.
	nodeGotRegisterResult := make(chan agentRegisterResult, 1)
	go func() {
		for {
			typ, data, err := node.read()
			if err != nil {
				return
			}
			switch typ {
			case agentMsgDesired:
				var d agentDesired
				if json.Unmarshal(data, &d) != nil {
					continue
				}
				_ = node.send(agentMsgAck, agentAck{Revision: d.Revision, OK: true})
				_ = node.send(agentMsgRegister, agentRegisterMsg{Instance: "matrix",
					Token: strings.Repeat("e", 32), Name: "nodee2e1234", HostFile: "hostfile", Revision: "rev1"})
			case agentMsgRegisterResult:
				var r agentRegisterResult
				_ = json.Unmarshal(data, &r)
				nodeGotRegisterResult <- r
			}
		}
	}()

	s := loadAgentTunnels()
	s.Tunnels = append(s.Tunnels, agentTunnel{ID: "at_e2e", Name: "e2e", Instance: "matrix",
		HubAgentID: hubID, NodeAgentID: nodeID, Family: "ipv4", MeshPort: 2097,
		Ports: []int{8080}, Token: strings.Repeat("e", 32), Active: true,
		State: agentTunnelStateProvisioning, CreatedAt: time.Now().Unix()})
	if err := saveAgentTunnels(s); err != nil {
		t.Fatal(err)
	}

	agentTunnelProvision("at_e2e")

	s = loadAgentTunnels()
	if len(s.Tunnels) != 1 {
		t.Fatalf("tunnel record lost")
	}
	got := s.Tunnels[0]
	if got.State != agentTunnelStateActive {
		t.Fatalf("tunnel must end active over the real hub, got state=%q error=%q hubAck=%v nodeAck=%v", got.State, got.Error, got.HubAckOK, got.NodeAckOK)
	}
	if !strings.Contains(got.HubMainHost, "RSA PUBLIC KEY") {
		t.Fatalf("hub host file missing from the ack detail: %q", got.HubMainHost)
	}

	select {
	case cmd := <-hubSaw:
		if cmd.Op != agentCmdMatrixRegister || cmd.Params["host_file"] != "hostfile" || cmd.Params["instance"] != "matrix" {
			t.Fatalf("register relay envelope wrong: %+v", cmd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hub never received the matrix.register relay")
	}
	select {
	case r := <-nodeGotRegisterResult:
		if !r.OK {
			t.Fatalf("node register_result not ok: %+v", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("node never received the register_result")
	}
}

// Regression (Session 4 gate finding, snapshot wt-s4-158): the teardown
// delivery runs in goroutines that may outlive the seam wiring — a nil
// seam must produce an error, never a nil-func panic that kills the
// process.
func TestAgentTeardownDesiredDeliverNilSeamNoPanic(t *testing.T) {
	old := agentHubDeliverDesiredFn.get()
	t.Cleanup(func() { agentHubDeliverDesiredFn.set(old) })

	agentHubDeliverDesiredFn.set(nil)
	err := agentDeliverTeardownDesired("hub1", agentDesired{Revision: "r",
		Matrix: &agentDesiredMatrix{Instance: "matrix", Action: "remove"}})
	if err == nil {
		t.Fatal("nil seam must return an error, not panic")
	}

	delivered := false
	agentHubDeliverDesiredFn.set(func(id string, d agentDesired) error {
		delivered = true
		return nil
	})
	if err := agentDeliverTeardownDesired("hub1", agentDesired{Revision: "r"}); err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("wired seam must deliver")
	}
}
