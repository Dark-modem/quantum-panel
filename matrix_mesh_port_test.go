package main

import (
	"encoding/json"
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
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
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
