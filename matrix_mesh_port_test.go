package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
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
	"sync"
	"testing"
	"time"
)

// occupyTCP holds the given ports for the whole test, so the panel's own
// wildcard TCP+UDP probe reports them as taken.
func occupyTCP(t *testing.T, ports ...int) {
	t.Helper()
	for _, p := range ports {
		l, e := net.Listen("tcp", ":"+strconv.Itoa(p))
		if e != nil {
			t.Fatalf("occupy tcp %d: %v", p, e)
		}
		t.Cleanup(func() { l.Close() })
	}
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	// The matrix preflight probes BOTH transports (matrixPortFree), so the
	// port must be free on TCP and UDP; on loaded machines the OS can hand
	// one side of an ephemeral port to another process in the check-to-use
	// window. Loop until both sides are free, like the agent tests' helper.
	// Hardened like freeMeshPort: the probe/close sequence races the
	// Windows loopback stack — 50 attempts with a short settle pause keeps
	// it deterministic (20 occasionally ran out under load).
	for attempt := 0; attempt < 50; attempt++ {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, e := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port))
		if e != nil {
			l.Close()
			time.Sleep(5 * time.Millisecond)
			continue
		}
		l.Close()
		u.Close()
		return port
	}
	t.Fatal("no dual-transport-free port found after 50 attempts")
	return 0
}

func matrixSetupBody(port int, ports []int) *httptest.ResponseRecorder {
	portsJSON, _ := json.Marshal(ports)
	body := fmt.Sprintf(`{"family":"ipv4","port":%d,"ports":%s,"addr":"185.226.119.74"}`, port, portsJSON)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/matrix/setup", strings.NewReader(body))
	handleMatrixSetupPost(rr, req)
	return rr
}

// A mesh port another program already holds must answer with its own code
// (E-MATRIX-10, 400), not the generic E-MATRIX-07 setup failure (500).
func TestMatrixSetupOccupiedMeshPortReturnsDedicatedCode(t *testing.T) {
	matrixInstanceLab(t)
	p := freeTCPPort(t)
	occupyTCP(t, p)
	user := freeUDPPort(t)
	rr := matrixSetupBody(p, []int{user})
	if rr.Code != 400 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Code  string `json:"error_code"`
		Error string `json:"error"`
		Hint  string `json:"hint"`
	}
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if out.Code != "E-MATRIX-10" {
		t.Fatalf("error_code = %q (%s)", out.Code, out.Error)
	}
	if !strings.Contains(out.Error, fmt.Sprintf("TCP port %d is occupied", p)) {
		t.Fatalf("detail does not name the port: %q", out.Error)
	}
	if !strings.Contains(out.Hint, "mesh port") {
		t.Fatalf("hint does not explain the mesh port: %q", out.Hint)
	}
	// The refusal happens in the preflight, before any state is persisted.
	if _, e := os.Stat(matrixStatePath()); !os.IsNotExist(e) {
		t.Fatalf("state persisted despite the port refusal: %v", e)
	}
}

// Auto-allocation: 2097 first, then 21000-21999, skipping instance-state
// ports, caller forwarded ports, topology-owned ports and occupied ports.
func TestMatrixAutoMeshPortSkipsReservedAndOccupied(t *testing.T) {
	matrixInstanceLab(t)
	if p, e := matrixAutoMeshPort(matrixLegacyInstance(), nil); e != nil || p != 2097 {
		t.Fatalf("free server should pick 2097, got %d err %v", p, e)
	}
	// Occupied 2097: the scan starts at 21000.
	occupyTCP(t, 2097)
	if p, e := matrixAutoMeshPort(matrixLegacyInstance(), nil); e != nil || p != 21000 {
		t.Fatalf("occupied 2097 should fall through to 21000, got %d err %v", p, e)
	}
	// Instance-state ownership: legacy reserves 21000 (mesh) and 21001 (user).
	occupyTCP(t, 21000, 21001)
	if e := saveMatrix(matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Port: 21000, Ports: []int{21001}}); e != nil {
		t.Fatal(e)
	}
	if p, e := matrixAutoMeshPort(matrixLegacyInstance(), nil); e != nil || p != 21002 {
		t.Fatalf("reserved 21000/21001 must be skipped, got %d err %v", p, e)
	}
	// The caller's own forwarded ports are never chosen as the mesh port.
	if p, e := matrixAutoMeshPort(matrixLegacyInstance(), []int{21002}); e != nil || p != 21003 {
		t.Fatalf("forwarded 21002 must be skipped, got %d err %v", p, e)
	}
	// A port bound to another topology's tunnel is skipped too: with 21002
	// occupied the scan must jump over topology-owned 21003 to 21004.
	if e := os.WriteFile(filepath.Join(configDir, "peers.json"), []byte(`{"peers":[{"id":1,"name":"legacy","engine":"frp","ports":[21003]}]}`), 0600); e != nil {
		t.Fatal(e)
	}
	topo := []byte(`{"topologies":[{"id":"topo_fixed","name":"Iran pair","role":"iran","tunnel_ids":["peer:1"],"created_at":1700000000}]}`)
	if e := os.WriteFile(topologyPath(), topo, 0600); e != nil {
		t.Fatal(e)
	}
	occupyTCP(t, 21002)
	if p, e := matrixAutoMeshPort(matrixLegacyInstance(), nil); e != nil || p != 21004 {
		t.Fatalf("topology-owned 21003 must be skipped, got %d err %v", p, e)
	}
}

// A setup request with port 0 auto-allocates, persists and returns the port.
func TestMatrixSetupAutoAllocatesMeshPort(t *testing.T) {
	matrixInstanceLab(t)
	user := freeUDPPort(t)
	rr := matrixSetupBody(0, []int{user})
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if out["port"].(float64) != 2097 {
		t.Fatalf("auto mesh port = %v, want 2097", out["port"])
	}
	st := loadMatrix()
	if !st.Enabled || st.Port != 2097 {
		t.Fatalf("chosen port not persisted: %+v", st)
	}
	// Occupied 2097: the setup still succeeds on the next free port.
	matrixInstanceLab(t)
	occupyTCP(t, 2097)
	user2 := freeUDPPort(t)
	rr2 := matrixSetupBody(0, []int{user2})
	if rr2.Code != 200 {
		t.Fatalf("status %d body %s", rr2.Code, rr2.Body.String())
	}
	var out2 map[string]any
	if e := json.Unmarshal(rr2.Body.Bytes(), &out2); e != nil {
		t.Fatal(e)
	}
	if out2["port"].(float64) != 21000 {
		t.Fatalf("auto mesh port after 2097 taken = %v, want 21000", out2["port"])
	}
}

// When nothing in the scan range is free the request fails with E-MATRIX-10
// and a message that says what to do, instead of a generic 500.
func TestMatrixSetupNoFreeMeshPortError(t *testing.T) {
	matrixInstanceLab(t)
	old := matrixMeshPortCandidates
	matrixMeshPortCandidates = func() []int { return []int{2097, 21000} }
	t.Cleanup(func() { matrixMeshPortCandidates = old })
	occupyTCP(t, 2097, 21000)
	user := freeUDPPort(t)
	rr := matrixSetupBody(0, []int{user})
	if rr.Code != 400 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Code  string `json:"error_code"`
		Error string `json:"error"`
	}
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if out.Code != "E-MATRIX-10" || !strings.Contains(out.Error, "no free mesh port") {
		t.Fatalf("code=%q error=%q", out.Code, out.Error)
	}
}

// Edit parity: an absent port keeps the current mesh port, an explicit 0
// auto-allocates, and a changed port is probed like in setup (E-MATRIX-10).
func TestMatrixEditMeshPortParity(t *testing.T) {
	matrixInstanceLab(t)
	base := matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Family: "ipv4",
		Port: 21010, Addr: "185.226.119.74", Token: strings.Repeat("a", 64), Ports: []int{18080},
		Name: "main", IP: matrixHubIP}
	if e := saveMatrix(base); e != nil {
		t.Fatal(e)
	}
	if e := matrixWriteConfig(matrixLegacyInstance(), base, ""); e != nil {
		t.Fatal(e)
	}
	// Absent port: nothing about the mesh port changes.
	next, e := matrixApplyEdit(base, matrixEditRequest{})
	if e != nil {
		t.Fatal(e)
	}
	if next.Port != 21010 {
		t.Fatalf("absent port must keep %d, got %d", 21010, next.Port)
	}
	// Explicit 0 auto-allocates (2097 is free here) and rewrites the config.
	next, e = matrixApplyEdit(base, matrixEditRequest{Port: intPtr(0)})
	if e != nil {
		t.Fatal(e)
	}
	if next.Port != 2097 {
		t.Fatalf("auto edit port = %d, want 2097", next.Port)
	}
	conf, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc.conf"))
	if !strings.Contains(string(conf), "Port = 2097") {
		t.Fatalf("tinc.conf not rewritten: %q", string(conf))
	}
	// Occupied target: the same dedicated code path as setup.
	occupyTCP(t, 21011)
	if _, e := matrixApplyEdit(base, matrixEditRequest{Port: intPtr(21011)}); e == nil {
		t.Fatal("occupied mesh port accepted")
	} else if _, ok := e.(*matrixPortBusyError); !ok {
		t.Fatalf("error type %T, want *matrixPortBusyError", e)
	}
	rr := httptest.NewRecorder()
	body := `{"port":21011}`
	req := httptest.NewRequest(http.MethodPost, "/api/matrix/edit", strings.NewReader(body))
	handleMatrixEdit(rr, req)
	if rr.Code != 400 {
		t.Fatalf("edit status %d body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Code string `json:"error_code"`
	}
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if out.Code != "E-MATRIX-10" {
		t.Fatalf("edit error_code = %q, want E-MATRIX-10", out.Code)
	}
	// A port change with a paired node requires a bundle re-apply.
	withPeer := base
	withPeer.Peers = []matrixPeer{{Name: "node1", Addr: "198.51.100.9", IP: matrixFirstPeer}}
	next, e = matrixApplyEdit(withPeer, matrixEditRequest{Port: intPtr(0)})
	if e != nil {
		t.Fatal(e)
	}
	if !next.PeerSyncRequired || next.Revision == "" {
		t.Fatalf("port change must raise PeerSyncRequired, got %+v", next)
	}
}

func intPtr(v int) *int { return &v }

// foreignTestBundle builds a valid pairing bundle targeting instance matrix2
// whose hub lives at hubURL; MainHost carries a real 2048-bit key so
// parseMatrixBundleFor accepts it.
func foreignTestBundle(t *testing.T, hubURL string, port int, ports []int) matrixBundlePayload {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}))
	in, _ := matrixInstanceOfNetwork("quantum_matrix2")
	return matrixBundlePayload{
		Panel: hubURL, Token: strings.Repeat("t", 64), Family: "ipv4", Addr: "185.226.119.74",
		Port: port, FIP: in.firstPeer(), Ports: ports, MainHost: pub, Network: "quantum_matrix2",
	}
}

// foreignSetupBody posts the bundle-encoded pairing request to
// handleMatrixForeignSetup.
func foreignSetupBody(t *testing.T, b matrixBundlePayload) *httptest.ResponseRecorder {
	t.Helper()
	raw, e := json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/matrix/foreign-setup",
		strings.NewReader(fmt.Sprintf(`{"bundle":%q}`, matrixBundlePfx+base64.StdEncoding.EncodeToString(raw))))
	handleMatrixForeignSetup(rr, req)
	return rr
}

// fakeMatrixHub accepts the node's registration POST, records its payload and
// answers like the real hub.
func fakeMatrixHub(t *testing.T) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var mu sync.Mutex
	payload := map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/matrix/register" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		payload = body
		mu.Unlock()
		w.Write([]byte(`{"status":"ok","main_host":"hub host file"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return payload
	}
}

// The reported regression: the node server still runs an ENABLED legacy Matrix
// instance (an old pairing) holding the bundle's mesh port. The node dials the
// hub at the port embedded in the hub host file, not at its own local port, so
// a taken local mesh port must be auto-shifted instead of failing E-MATRIX-02.
func TestMatrixForeignSetupShiftsBusyLocalMeshPort(t *testing.T) {
	matrixInstanceLab(t)
	// The legacy instance owns mesh port 21097 and forwarded 18080.
	if e := saveMatrix(matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Port: 21097, Ports: []int{18080}}); e != nil {
		t.Fatal(e)
	}
	// Shrink the auto-port scan and occupy its first candidate, so the picked
	// local mesh port is deterministic.
	oldCand := matrixMeshPortCandidates
	matrixMeshPortCandidates = func() []int { return []int{21000, 21001} }
	t.Cleanup(func() { matrixMeshPortCandidates = oldCand })
	occupyTCP(t, 21000)

	hub, reg := fakeMatrixHub(t)
	b := foreignTestBundle(t, hub.URL, 21097, []int{18081})
	rr := foreignSetupBody(t, b)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Status string `json:"status"`
		Port   int    `json:"port"`
	}
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if out.Status != "ok" || out.Port != 21001 {
		t.Fatalf("response = %+v, want ok with the shifted local port 21001", out)
	}
	in2 := matrixInstance{ID: "matrix2"}
	st := loadMatrixInstance(in2)
	if !st.Enabled || st.Role != "node" || st.Port != 21001 || st.Network != "quantum_matrix2" {
		t.Fatalf("node state = %+v, want an enabled matrix2 node on local mesh port 21001", st)
	}
	// The node listens on the shifted port but still dials the hub through the
	// host file's hub port: tinc.conf vs hosts/main.
	conf, e := os.ReadFile(filepath.Join(matrixDirOf(in2), "tinc.conf"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(conf), "Port = 21001") || !strings.Contains(string(conf), "ConnectTo = main") {
		t.Fatalf("tinc.conf not on the shifted local port: %q", string(conf))
	}
	main, e := os.ReadFile(filepath.Join(matrixDirOf(in2), "hosts", "main"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(main), "Port = 21097") || !strings.Contains(string(main), "Address = 185.226.119.74") {
		t.Fatalf("hosts/main must keep the hub dial port: %q", string(main))
	}
	// The registration payload still identifies the node, and the host file it
	// uploads carries the node's own (shifted) port.
	got := reg()
	if got["token"] != b.Token || got["revision"] != "" || !strings.HasPrefix(fmt.Sprint(got["name"]), "node") {
		t.Fatalf("register payload = %+v", got)
	}
	if hf := fmt.Sprint(got["host_file"]); !strings.Contains(hf, "Port = 21001") {
		t.Fatalf("register host_file must carry the node's local port: %q", hf)
	}
}

// When the bundle's mesh port is free locally nothing shifts and the response
// carries no port override.
func TestMatrixForeignSetupKeepsFreeBundlePort(t *testing.T) {
	matrixInstanceLab(t)
	hub, _ := fakeMatrixHub(t)
	p := freeTCPPort(t)
	b := foreignTestBundle(t, hub.URL, p, []int{18081})
	rr := foreignSetupBody(t, b)
	if rr.Code != 200 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if _, shifted := out["port"]; shifted {
		t.Fatalf("free bundle port must not be reported as shifted: %v", out)
	}
	if st := loadMatrixInstance(matrixInstance{ID: "matrix2"}); !st.Enabled || st.Port != p {
		t.Fatalf("node state = %+v, want the bundle port %d kept", st, p)
	}
}

// Forwarded ports are a genuine conflict: the hub DNATs exactly those port
// numbers into this node, where the real service listens. The refusal must
// name the port, the owning instance and the fix, and must not touch state.
func TestMatrixForeignSetupForwardedPortConflictFailsClearly(t *testing.T) {
	matrixInstanceLab(t)
	if e := saveMatrix(matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Port: 21097, Ports: []int{18080}}); e != nil {
		t.Fatal(e)
	}
	// The hub URL is never contacted: the conflict is refused in the preflight.
	b := foreignTestBundle(t, "http://127.0.0.1:9", 22000, []int{18080})
	rr := foreignSetupBody(t, b)
	if rr.Code != 400 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Code  string `json:"error_code"`
		Error string `json:"error"`
	}
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if out.Code != "E-MATRIX-02" {
		t.Fatalf("error_code = %q (%s)", out.Code, out.Error)
	}
	want := "forwarded port 18080 is already used on this server by Matrix instance matrix — free that port or remove that tunnel first"
	if !strings.Contains(out.Error, want) {
		t.Fatalf("error %q does not name the port, owner and fix", out.Error)
	}
	in2, _ := matrixInstanceOfNetwork("quantum_matrix2")
	if _, e := os.Stat(matrixStatePathOf(in2)); !os.IsNotExist(e) {
		t.Fatalf("state persisted despite the conflict: %v", e)
	}
	if _, e := os.Stat(matrixDirOf(in2)); !os.IsNotExist(e) {
		t.Fatalf("tinc dir created despite the conflict: %v", e)
	}
}

// Hub-side setup busy messages name the port and the owning instance: a taken
// mesh port points at the auto pick, a taken forwarded port names the fix.
func TestMatrixSetupBusyMessageNamesPortAndInstance(t *testing.T) {
	matrixInstanceLab(t)
	if e := saveMatrixInstance(matrixInstance{ID: "matrix2"}, matrixState{Enabled: true, Network: "quantum_matrix2", Role: "hub", Port: 21000, Ports: []int{21001}}); e != nil {
		t.Fatal(e)
	}
	rr := matrixSetupBody(21000, []int{18080})
	if rr.Code != 400 {
		t.Fatalf("status %d body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Code  string `json:"error_code"`
		Error string `json:"error"`
	}
	if e := json.Unmarshal(rr.Body.Bytes(), &out); e != nil {
		t.Fatal(e)
	}
	if out.Code != "E-MATRIX-02" || !strings.Contains(out.Error, "mesh port 21000 is already used by Matrix instance matrix2 — pick another port or leave it blank for auto") {
		t.Fatalf("mesh-port message = %q (%s)", out.Error, out.Code)
	}
	rr2 := matrixSetupBody(2097, []int{21001})
	if rr2.Code != 400 {
		t.Fatalf("status %d body %s", rr2.Code, rr2.Body.String())
	}
	var out2 struct {
		Error string `json:"error"`
	}
	if e := json.Unmarshal(rr2.Body.Bytes(), &out2); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(out2.Error, "forwarded port 21001 is already used by Matrix instance matrix2 — free that port or remove that tunnel first") {
		t.Fatalf("forwarded message = %q", out2.Error)
	}
}
