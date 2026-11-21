package panel

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func matrixInstanceLab(t *testing.T) {
	t.Helper()
	oldDir, oldUnit, oldTinc, oldExec := configDir, matrixUnitDir, matrixTincPath, matrixExec
	configDir = t.TempDir()
	matrixUnitDir = t.TempDir()
	matrixTincPath = func() (string, error) { return "/usr/sbin/tincd", nil }
	matrixExec = func(name string, args ...string) (string, error) {
		s := strings.Join(args, " ")
		if name == "iptables" && (strings.Contains(s, " -C ") || strings.Contains(s, " -S ")) {
			return "", fmt.Errorf("chain absent")
		}
		if name == "ip" && strings.Contains(s, "route") {
			return "[]", nil
		}
		if name == "ip" && strings.Contains(s, "link") {
			return "", fmt.Errorf("interface does not exist")
		}
		return "", nil
	}
	t.Cleanup(func() {
		// No AgentOpsWait here: the agent runtime (and its drain helper)
		// was removed with the controller extraction; this tree has no
		// agent goroutines to drain before restoring the seams.
		configDir, matrixUnitDir, matrixTincPath, matrixExec = oldDir, oldUnit, oldTinc, oldExec
	})
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	// Verify BOTH transports are free: matrixSetup's preflight probes TCP as
	// well, and on loaded machines the OS can hand the same ephemeral port's
	// TCP side to another process between our check and tinc's bind.
	// Hardened like freeTCPPort (BACKEND-24): the probe/close sequence races
	// the Windows loopback stack — 50 attempts with a short settle pause
	// keeps it deterministic (20 occasionally ran out under load).
	for attempt := 0; attempt < 50; attempt++ {
		l, e := net.ListenPacket("udp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		port := l.LocalAddr().(*net.UDPAddr).Port
		ln, e := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if e != nil {
			l.Close()
			continue
		}
		l.Close()
		ln.Close()
		return port
	}
	t.Fatal("no dual-transport-free port found after 20 attempts")
	return 0
}

func TestMatrixInstanceDerivationIsUniquePerInstance(t *testing.T) {
	seen := map[string]string{}
	for _, in := range []matrixInstance{matrixLegacyInstance(), {ID: "matrix2"}, {ID: "matrix3"}, {ID: "matrix17"}} {
		if !in.valid() {
			t.Fatalf("instance %s should be valid", in.ID)
		}
		for _, v := range []string{
			"net:" + in.network(), "iface:" + in.iface(), "unit:" + in.unit(), "tag:" + in.tag(),
			"chain:" + in.chainPfx(), "hub:" + in.hubIP(), "peer:" + in.firstPeer(),
			"subnet:" + in.subnet(), "dir:" + matrixDirOf(in), "state:" + matrixStatePathOf(in),
			"unitpath:" + matrixUnitPathOf(in), "pid:" + in.pidFile(),
		} {
			if prev, dup := seen[v]; dup {
				t.Fatalf("%s collides between %s and %s", v, prev, in.ID)
			}
			seen[v] = in.ID
		}
	}
	legacy := matrixLegacyInstance()
	if legacy.network() != matrixNetname || legacy.iface() != matrixIfaceName || legacy.unit() != matrixUnit ||
		legacy.tag() != matrixTag || legacy.hubIP() != matrixHubIP || legacy.firstPeer() != matrixFirstPeer ||
		legacy.subnet() != matrixSubnet || legacy.chainPfx() != "QMX" {
		t.Fatal("legacy instance must keep every historical constant")
	}
	for _, bad := range []string{"matrix1", "matrixx", "matrix0", "matrix-3", "", "Matrix2", "matrix99999999999999999999"} {
		if (matrixInstance{ID: bad}).valid() {
			t.Fatalf("accepted invalid instance %q", bad)
		}
	}
	if _, ok := matrixInstanceOfNetwork("quantum_matrix2"); !ok {
		t.Fatal("network quantum_matrix2 must map to an instance")
	}
	if in, ok := matrixInstanceOfNetwork("quantum_matrix"); !ok || in.ID != "matrix" {
		t.Fatal("legacy network mapping broken")
	}
	if _, ok := matrixInstanceOfNetwork("quantum_matrix1"); ok {
		t.Fatal("matrix1 network must be refused")
	}
	if _, ok := matrixInstanceOfNetwork("quantum_matrix99999999999999999999"); ok {
		t.Fatal("over-cap instance network accepted")
	}
}

func TestMatrixInstanceRegistryScansStateFiles(t *testing.T) {
	matrixInstanceLab(t)
	if got := matrixInstances(); len(got) != 0 {
		t.Fatalf("empty config dir should list no instances, got %v", got)
	}
	if id := matrixNextInstanceID(); id != "matrix2" {
		t.Fatalf("next id = %q", id)
	}
	for _, name := range []string{"matrix.json", "matrix2.json", "matrix-local-services.json", "matrix-tinc", "topologies.json", "matrix10.json"} {
		if strings.HasSuffix(name, ".json") {
			if e := os.WriteFile(filepath.Join(configDir, name), []byte("{}"), 0600); e != nil {
				t.Fatal(e)
			}
		} else if e := os.MkdirAll(filepath.Join(configDir, name), 0700); e != nil {
			t.Fatal(e)
		}
	}
	got := matrixInstances()
	if len(got) != 3 || got[0].ID != "matrix" || got[1].ID != "matrix2" || got[2].ID != "matrix10" {
		t.Fatalf("registry = %+v", got)
	}
	if id := matrixNextInstanceID(); id != "matrix3" {
		t.Fatalf("next id after scan = %q", id)
	}
	if _, e := matrixResolveInstance("next"); e != nil {
		t.Fatal(e)
	}
	if _, e := matrixResolveInstance("bogus"); e == nil {
		t.Fatal("accepted bogus instance id")
	}
}

// The legacy first instance and an explicitly added second instance must
// coexist with fully disjoint files, service units, interfaces and subnets.
func TestMatrixSecondInstanceCoexistsWithLegacy(t *testing.T) {
	matrixInstanceLab(t)
	port1, port2 := freeUDPPort(t), freeUDPPort(t)
	legacy := matrixLegacyInstance()
	st1 := matrixState{Enabled: true, Role: "hub", Family: "ipv4", Port: port1, Addr: "185.226.119.74", Token: strings.Repeat("a", 64), Ports: []int{18080}, Name: "main", IP: legacy.hubIP()}
	if e := matrixSetup(legacy, st1, ""); e != nil {
		t.Fatal(e)
	}
	next, e := matrixResolveInstance("next")
	if e != nil || next.ID != "matrix2" {
		t.Fatalf("next instance = %+v err=%v", next, e)
	}
	st2 := matrixState{Enabled: true, Role: "hub", Family: "ipv4", Port: port2, Addr: "185.226.119.75", Token: strings.Repeat("b", 64), Ports: []int{18081}, Name: "main", IP: next.hubIP()}
	if e := matrixSetup(next, st2, ""); e != nil {
		t.Fatal(e)
	}
	// Both state files exist and the legacy one is untouched.
	var l1, l2 matrixState
	if b, e := os.ReadFile(matrixStatePath()); e != nil || json.Unmarshal(b, &l1) != nil || !l1.Enabled {
		t.Fatalf("legacy state: %v", e)
	}
	if b, e := os.ReadFile(matrixStatePathOf(next)); e != nil || json.Unmarshal(b, &l2) != nil || !l2.Enabled || l2.Network != "quantum_matrix2" {
		t.Fatalf("second state: %v", e)
	}
	if l1.Network != matrixNetname || l1.Port != port1 || l2.Port != port2 {
		t.Fatal("instance states share mutable fields")
	}
	conf1, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc.conf"))
	conf2, _ := os.ReadFile(filepath.Join(matrixDirOf(next), "tinc.conf"))
	if !strings.Contains(string(conf1), "Interface = qmx0") || strings.Contains(string(conf1), "qmx2") {
		t.Fatal("legacy config iface changed")
	}
	if !strings.Contains(string(conf2), "Interface = qmx2") || strings.Contains(string(conf2), "qmx0") {
		t.Fatal("second instance is not interface-isolated")
	}
	unit2, _ := os.ReadFile(matrixUnitPathOf(next))
	if !strings.Contains(string(unit2), "quantum-matrix2.pid") || !strings.Contains(string(unit2), "matrix2-tinc") || strings.Contains(string(unit2), matrixDir()) {
		t.Fatalf("second unit not isolated:\n%s", unit2)
	}
	if _, e := os.Stat(matrixUnitPath()); e != nil {
		t.Fatal("legacy unit removed")
	}
	// Second removal must not touch the legacy instance.
	if e := matrixRemove(next, l2); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(matrixStatePath()); e != nil {
		t.Fatal("legacy state removed with instance 2")
	}
	if _, e := os.Stat(matrixStatePathOf(next)); !os.IsNotExist(e) {
		t.Fatal("second state remains")
	}
	var still matrixState
	b, _ := os.ReadFile(matrixStatePath())
	_ = json.Unmarshal(b, &still)
	if !still.Enabled || still.Port != port1 {
		t.Fatal("legacy instance mutated by second instance removal")
	}
}

// A new Matrix instance may never take a mesh/user port already owned by
// another topology's tunnel or by another Matrix instance.
func TestMatrixSetupRefusesCrossTopologyAndCrossInstancePorts(t *testing.T) {
	matrixInstanceLab(t)
	// A topology binding a configured peer tunnel that serves port 18443.
	if e := os.WriteFile(filepath.Join(configDir, "peers.json"), []byte(`{"peers":[{"id":1,"name":"legacy","engine":"frp","ports":[18443]}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	topo := map[string]any{"topologies": []map[string]any{{
		"id": "topo_fixed", "name": "Iran pair", "role": "iran", "tunnel_ids": []string{"peer:1"}, "created_at": 1700000000,
	}}}
	b, _ := json.Marshal(topo)
	if e := os.WriteFile(topologyPath(), b, 0600); e != nil {
		t.Fatal(e)
	}
	if owner := matrixTopologyPortOwner([]int{18443}); owner != "Iran pair" {
		t.Fatalf("topology port owner = %q", owner)
	}
	in := matrixLegacyInstance()
	if owner := matrixInstancePortsBusy(in, []int{18443}); owner != "" {
		t.Fatalf("unexpected instance owner %q", owner)
	}
	// Instance-level ownership: legacy owns 21000, so instance 2 is refused.
	st := matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Family: "ipv4", Port: 21000, Addr: "185.226.119.74", Ports: []int{21001}, Name: "main", IP: matrixHubIP}
	if e := saveMatrix(st); e != nil {
		t.Fatal(e)
	}
	in2 := matrixInstance{ID: "matrix2"}
	if owner := matrixInstancePortsBusy(in2, []int{21001}); owner != "matrix" {
		t.Fatalf("cross-instance owner = %q", owner)
	}
	if owner := matrixInstancePortsBusy(in, []int{21000}); owner != "" {
		t.Fatal("instance flagged its own port")
	}
	if matrixPortClash([]int{21001}) == "" {
		t.Fatal("matrixPortClash missed a reserved port")
	}
	if matrixPortClash([]int{12345}) != "" {
		t.Fatal("matrixPortClash flagged a free port")
	}
}

// Two hub instances on one server: the registration token selects the instance.
func TestMatrixRegisterTokenSelectsInstance(t *testing.T) {
	matrixInstanceLab(t)
	legacy := matrixLegacyInstance()
	in2 := matrixInstance{ID: "matrix2"}
	st1 := matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Family: "ipv4", Port: 21010, Addr: "185.226.119.74", Token: strings.Repeat("a", 64), Ports: []int{18080}, Name: "main", IP: legacy.hubIP()}
	st2 := matrixState{Enabled: true, Network: in2.network(), Role: "hub", Family: "ipv4", Port: 21011, Addr: "185.226.119.75", Token: strings.Repeat("b", 64), Ports: []int{18081}, Name: "main", IP: in2.hubIP()}
	if e := saveMatrixInstance(legacy, st1); e != nil {
		t.Fatal(e)
	}
	if e := saveMatrixInstance(in2, st2); e != nil {
		t.Fatal(e)
	}
	for _, in := range []matrixInstance{legacy, in2} {
		if e := matrixWriteConfig(in, loadMatrixInstance(in), ""); e != nil {
			t.Fatal(e)
		}
	}
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}))
	hostFile := "Subnet = " + in2.firstPeer() + "/32\nCipher = aes-256-cbc\nDigest = sha256\n" + pub
	payload, _ := json.Marshal(map[string]string{"token": strings.Repeat("b", 64), "name": "node1", "host_file": hostFile})
	body := string(payload)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/matrix/register", strings.NewReader(body))
	handleMatrixRegister(rr, req)
	if rr.Code != 200 {
		t.Fatalf("register status %d body %s", rr.Code, rr.Body.String())
	}
	if _, e := os.Stat(filepath.Join(matrixDirOf(in2), "hosts", "node1")); e != nil {
		t.Fatal("node host file not written to instance 2")
	}
	if _, e := os.Stat(filepath.Join(matrixDir(), "hosts", "node1")); !os.IsNotExist(e) {
		t.Fatal("registration leaked into the legacy instance")
	}
}

// Multiple topologies on one server are allowed when their tunnels and remote
// endpoints differ; attaching one tunnel to a second topology is refused with
// 409 (exclusive ownership).
func TestTopologyMultiplePerServerAndExclusiveOwnership(t *testing.T) {
	topologyFixture(t)
	topologyRequest(http.MethodDelete, "topo_a", "")
	first := topologyRequest(http.MethodPost, "", `{"name":"Pair A","role":"iran","local_ip":"203.0.113.1","remote_ip":"203.0.113.2","tunnel_ids":["main"]}`)
	if first.Code != http.StatusCreated {
		t.Fatalf("first topology: %d %s", first.Code, first.Body.String())
	}
	second := topologyRequest(http.MethodPost, "", `{"name":"Pair B","role":"iran","local_ip":"203.0.113.1","remote_ip":"2001:db8::2","tunnel_ids":["matrix"]}`)
	if second.Code != http.StatusCreated {
		t.Fatalf("second topology on the same server must be allowed: %d %s", second.Code, second.Body.String())
	}
	attach := topologyRequest(http.MethodPatch, createdID(t, first), `{"tunnel_ids":["main","matrix"]}`)
	if attach.Code != http.StatusConflict {
		t.Fatalf("second attach must be 409, got %d %s", attach.Code, attach.Body.String())
	}
	steal := topologyRequest(http.MethodPatch, createdID(t, second), `{"tunnel_ids":["main"]}`)
	if steal.Code != http.StatusConflict {
		t.Fatalf("cross-topology steal must be 409, got %d %s", steal.Code, steal.Body.String())
	}
}

func createdID(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var resp struct {
		Topology Topology `json:"topology"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &resp); e != nil || resp.Topology.ID == "" {
		t.Fatalf("cannot read topology id: %v %s", e, w.Body.String())
	}
	return resp.Topology.ID
}

// The configured inventory lists every Matrix instance as its own tunnel.
func TestTopologyInventoryListsMatrixInstances(t *testing.T) {
	matrixInstanceLab(t)
	st1 := matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Family: "ipv4", Port: 21010, Addr: "185.226.119.74", Ports: []int{18080}, Name: "main", IP: matrixHubIP}
	st2 := matrixState{Enabled: true, Network: "quantum_matrix2", Role: "node", Family: "ipv6", Port: 21011, Addr: "2001:db8::2", Ports: []int{18081}, Name: "nodeabc", IP: matrixFirstPeer}
	if e := saveMatrixInstance(matrixLegacyInstance(), st1); e != nil {
		t.Fatal(e)
	}
	if e := saveMatrixInstance(matrixInstance{ID: "matrix2"}, st2); e != nil {
		t.Fatal(e)
	}
	snapPtr.Store(&HubSnapshot{})
	inv := readTopologyInventory()
	byID := map[string]topologyTunnel{}
	for _, tun := range inv {
		byID[tun.ID] = tun
	}
	if _, ok := byID["matrix"]; !ok {
		t.Fatal("legacy matrix missing from inventory")
	}
	m2, ok := byID["matrix2"]
	if !ok {
		t.Fatal("matrix2 missing from inventory")
	}
	if m2.Engine != "matrix" || m2.RemoteIP != "2001:db8::2" || len(m2.Ports) != 1 || m2.Ports[0] != 18081 {
		t.Fatalf("matrix2 inventory wrong: %+v", m2)
	}
	if !topologyTunnelRE.MatchString("matrix2") || !topologyTunnelRE.MatchString("matrix") || topologyTunnelRE.MatchString("matrix1") {
		t.Fatal("topology tunnel id regex misjudges matrix instances")
	}
}

// Every instance in the whole pool derives a disjoint mesh /30, hub IP and
// peer IP; the historic block (matrix2..matrix25) keeps its original
// addresses, and nothing ever lands on the pathtest probe nets (77, 78) or
// the legacy octet 231. A pool this size is what "no artificial tunnel
// limit" means for the Matrix engine.
func TestMatrixInstanceOctetsUniqueAcrossWholePool(t *testing.T) {
	seen := map[int]string{}
	check := func(in matrixInstance, wantSubnet string) {
		if !in.valid() {
			t.Fatalf("instance %s must be valid", in.ID)
		}
		if got := in.subnet(); got != wantSubnet {
			t.Fatalf("%s subnet = %s, want %s", in.ID, got, wantSubnet)
		}
		oct := in.octet()
		if oct == 77 || oct == 78 || oct == 231 {
			t.Fatalf("%s landed on reserved octet %d", in.ID, oct)
		}
		if prev, dup := seen[oct]; dup {
			t.Fatalf("octet %d collides between %s and %s", oct, prev, in.ID)
		}
		seen[oct] = in.ID
	}
	check(matrixLegacyInstance(), matrixSubnet)
	for n := 2; n <= matrixMaxInstances+1; n++ {
		in := matrixInstance{ID: "matrix" + strconv.Itoa(n)}
		want := ""
		switch {
		case n <= 25:
			want = fmt.Sprintf("10.254.%d.0/30", 230+n) // historic block, unchanged
		case n <= 102:
			want = fmt.Sprintf("10.254.%d.0/30", n-26)
		default:
			want = fmt.Sprintf("10.254.%d.0/30", n-24)
		}
		check(in, want)
	}
	if len(seen) != matrixMaxInstances+1 {
		t.Fatalf("pool covers %d octets, want %d", len(seen), matrixMaxInstances+1)
	}
	if !(matrixInstance{ID: "matrix" + strconv.Itoa(matrixMaxInstances+1)}).valid() {
		t.Fatal("top of the pool must be valid")
	}
	if (matrixInstance{ID: "matrix" + strconv.Itoa(matrixMaxInstances+2)}).valid() {
		t.Fatal("instance above the cap accepted")
	}
}
