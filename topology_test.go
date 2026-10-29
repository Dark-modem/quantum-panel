package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func topologyFixture(t *testing.T) {
	t.Helper()
	withTempConfig(t)
	old := topologyInventory
	topologyInventory = func() []topologyTunnel {
		return []topologyTunnel{
			{ID: "main", Name: "Legacy FRP", Engine: "frp", RemoteIP: "203.0.113.2", State: "healthy", LatencyMs: 12, Ports: []int{443}, role: "iran", localIP: "203.0.113.1"},
			{ID: "matrix", Name: "Matrix", Engine: "matrix", Family: "v6", RemoteIP: "2001:db8::2", RemoteIPs: []string{"203.0.113.2"}, State: "healthy", LatencyMs: 9, Ports: []int{443}, role: "iran"},
			{ID: "peer:3", Name: "Other server", Engine: "backhaul", RemoteIP: "203.0.113.3", State: "down", LatencyMs: -1, Ports: []int{8443}, role: "iran"},
			{ID: "bagher", Name: "Bagher", Engine: "bagher", State: "unknown", LatencyMs: -1, Ports: []int{}},
			{ID: "xray:4", Name: "Xray", Engine: "xray", RemoteIP: "203.0.113.2", State: "healthy", LatencyMs: 10, Ports: []int{443}, role: "iran"},
		}
	}
	t.Cleanup(func() { topologyInventory = old })
}

func topologyRequest(method, id, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "/api/topologies/"+id, strings.NewReader(body))
	r.SetPathValue("id", id)
	w := httptest.NewRecorder()
	switch method {
	case http.MethodGet:
		handleTopologiesGet(w, r)
	case http.MethodPost:
		handleTopologiesPost(w, r)
	case http.MethodPatch:
		handleTopologyPatch(w, r)
	case http.MethodDelete:
		handleTopologyDelete(w, r)
	}
	return w
}

func topologyCreated(t *testing.T, body string) Topology {
	t.Helper()
	w := topologyRequest(http.MethodPost, "", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Status   string   `json:"status"`
		Topology Topology `json:"topology"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Status != "ok" {
		t.Fatalf("create response: %s (%v)", w.Body.String(), err)
	}
	return response.Topology
}

func TestTopologyCRUDPersistenceAndGroupingOnly(t *testing.T) {
	topologyFixture(t)
	// Creating/deleting a topology must not touch any legacy engine resource.
	files := map[string]string{
		"peers.json":         "legacy peer token",
		"matrix.json":        "legacy Matrix token",
		"xray.json":          "legacy Xray private key",
		"ssh_fwd.json":       "legacy Bagher key",
		"frp/frps.toml":      "token = secret",
		"units/frps.service": "ExecStart=frps",
	}
	for file, data := range files {
		path := filepath.Join(configDir, file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := os.ReadDir(configDir)
	get := topologyRequest(http.MethodGet, "", "")
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"topologies":[]`) {
		t.Fatalf("initial GET: %d %s", get.Code, get.Body.String())
	}
	if _, err := os.Stat(topologyPath()); !os.IsNotExist(err) {
		t.Fatalf("GET created a file: %v", err)
	}
	group := topologyCreated(t, `{"name":"  Iran ↔ Foreign  ","role":"iran","local_ip":"203.0.113.1","remote_ip":"203.0.113.2","tunnel_ids":["main","matrix","xray:4"]}`)
	if group.Name != "Iran ↔ Foreign" || !topologyIDRE.MatchString(group.ID) || group.CreatedAt <= 0 {
		t.Fatalf("bad topology: %+v", group)
	}
	stored, err := loadTopologies()
	if err != nil || len(stored) != 1 || !reflect.DeepEqual(stored[0], group) {
		t.Fatalf("persistence: %+v %v", stored, err)
	}
	get = topologyRequest(http.MethodGet, "", "")
	var response struct {
		Topologies []Topology       `json:"topologies"`
		Tunnels    []topologyTunnel `json:"tunnels"`
	}
	if err := json.Unmarshal(get.Body.Bytes(), &response); err != nil || len(response.Topologies) != 1 || len(response.Tunnels) != 5 {
		t.Fatalf("GET: %s %v", get.Body.String(), err)
	}
	patch := topologyRequest(http.MethodPatch, group.ID, `{"name":"Renamed"}`)
	if patch.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", patch.Code, patch.Body.String())
	}
	stored, _ = loadTopologies()
	if stored[0].Name != "Renamed" || stored[0].CreatedAt != group.CreatedAt || stored[0].ID != group.ID || len(stored[0].TunnelIDs) != 3 {
		t.Fatalf("PATCH changed immutable/unmentioned fields: %+v", stored[0])
	}
	deleted := topologyRequest(http.MethodDelete, group.ID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("DELETE bound group: %d %s", deleted.Code, deleted.Body.String())
	}
	stored, err = loadTopologies()
	if err != nil || len(stored) != 0 {
		t.Fatalf("DELETE persistence: %+v %v", stored, err)
	}
	for file, data := range files {
		got, err := os.ReadFile(filepath.Join(configDir, file))
		if err != nil || string(got) != data {
			t.Fatalf("engine file %s changed: %q %v", file, got, err)
		}
	}
	after, _ := os.ReadDir(configDir)
	if len(after) != len(before)+1 {
		t.Fatalf("unexpected files after CRUD: before=%d after=%d", len(before), len(after))
	}
	if w := topologyRequest(http.MethodDelete, group.ID, ""); w.Code != http.StatusNotFound {
		t.Fatalf("second DELETE: %d", w.Code)
	}
	// A deleted grouping releases ownership but leaves its tunnel assignable.
	topologyCreated(t, `{"name":"Reassigned","remote_ip":"203.0.113.2","tunnel_ids":["main"]}`)
}

func TestTopologyPendingDefaultsAndPartialEdit(t *testing.T) {
	topologyFixture(t)
	pending := topologyCreated(t, `{}`)
	if pending.Name != "New topology" || pending.Role != "iran" || pending.LocalIP != "" || pending.RemoteIP != "" || pending.TunnelIDs == nil || len(pending.TunnelIDs) != 0 {
		t.Fatalf("defaults: %+v", pending)
	}
	w := topologyRequest(http.MethodPatch, pending.ID, `{"name":"Pending foreign","role":"foreign","local_ip":"2001:0db8:0:0::1","remote_ip":"2001:db8::2"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("edit pending: %d %s", w.Code, w.Body.String())
	}
	stored, _ := loadTopologies()
	if stored[0].LocalIP != "2001:db8::1" || stored[0].Role != "foreign" {
		t.Fatalf("canonical IP/editable pending role: %+v", stored[0])
	}
	w = topologyRequest(http.MethodPatch, pending.ID, `{"remote_ip":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("clear pending IP: %d %s", w.Code, w.Body.String())
	}
}

func TestTopologyRejectsInvalidEdits(t *testing.T) {
	cases := []string{
		`null`, `[]`, `"text"`, `{`, `{} {}`, `{"name":""}`, `{"name":"   "}`,
		`{"name":"a\nb"}`, `{"role":"hub"}`, `{"role":""}`, `{"role":null}`,
		`{"local_ip":"domain.example"}`, `{"remote_ip":"1.2.3.4:443"}`,
		`{"local_ip":"2001:db8::1/64"}`, `{"remote_ip":"fe80::1%eth0"}`,
		`{"remote_ip":12}`, `{"local_ip":null}`, `{"tunnel_ids":null}`,
		`{"tunnel_ids":[1]}`, `{"tunnel_ids":"main"}`, `{"tunnel_ids":["../main"]}`,
		`{"tunnel_ids":["peer:0"]}`, `{"tunnel_ids":["xray:-1"]}`, `{"tunnel_ids":["peer:01"]}`,
		`{"tunnel_ids":["main","main"]}`, `{"tunnel_ids":["peer:999"]}`,
		`{"id":"chosen"}`, `{"created_at":1}`, `{"engine":"matrix"}`, `{"detach":true}`,
		fmt.Sprintf(`{"name":%q}`, strings.Repeat("x", 121)),
		fmt.Sprintf(`{"name":%q}`, strings.Repeat("x", 70000)),
	}
	for _, body := range cases {
		t.Run(body[:min(len(body), 60)], func(t *testing.T) {
			topologyFixture(t)
			w := topologyRequest(http.MethodPost, "", body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("POST invalid %s: %d %s", body, w.Code, w.Body.String())
			}
			if _, err := os.Stat(topologyPath()); !os.IsNotExist(err) {
				t.Fatalf("invalid POST wrote metadata: %v", err)
			}
			group := topologyCreated(t, `{"name":"Original"}`)
			before, _ := os.ReadFile(topologyPath())
			w = topologyRequest(http.MethodPatch, group.ID, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("PATCH invalid %s: %d %s", body, w.Code, w.Body.String())
			}
			after, _ := os.ReadFile(topologyPath())
			if string(before) != string(after) {
				t.Fatal("failed PATCH changed metadata")
			}
		})
	}
}

func TestTopologyOwnershipAndSamePeerValidation(t *testing.T) {
	topologyFixture(t)
	group := topologyCreated(t, `{"name":"Pair","remote_ip":"203.0.113.2","tunnel_ids":["main","matrix"]}`)
	for _, body := range []string{
		`{"name":" pair "}`,
		`{"name":"Other","tunnel_ids":["main"]}`,
		`{"name":"Other","remote_ip":"203.0.113.2","tunnel_ids":["peer:3"]}`,
		`{"name":"Other","tunnel_ids":["peer:3","xray:4"]}`,
		`{"name":"Other","role":"foreign","tunnel_ids":["xray:4"]}`,
	} {
		w := topologyRequest(http.MethodPost, "", body)
		if w.Code != http.StatusConflict {
			t.Fatalf("conflict POST %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	for _, body := range []string{
		`{"role":"foreign"}`, `{"remote_ip":"203.0.113.3"}`, `{"local_ip":"203.0.113.9"}`,
		`{"tunnel_ids":["main","peer:3"]}`,
	} {
		w := topologyRequest(http.MethodPatch, group.ID, body)
		if w.Code != http.StatusConflict {
			t.Fatalf("conflict PATCH %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	// Matrix's explicitly paired public v4 and v6 are valid, not arbitrary IPs.
	w := topologyRequest(http.MethodPatch, group.ID, `{"tunnel_ids":["matrix"],"remote_ip":"2001:db8::2"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("Matrix transport IP: %d %s", w.Code, w.Body.String())
	}
	w = topologyRequest(http.MethodPatch, group.ID, `{"tunnel_ids":[],"role":"foreign","local_ip":"","remote_ip":""}`)
	if w.Code != http.StatusOK {
		t.Fatalf("detach then edit: %d %s", w.Code, w.Body.String())
	}
}

func TestTopologyReadOnlyStaleInventoryAndMissingIDs(t *testing.T) {
	topologyFixture(t)
	group := topologyCreated(t, `{"name":"Saved","tunnel_ids":["main"]}`)
	before, _ := os.ReadFile(topologyPath())
	topologyInventory = func() []topologyTunnel { return nil }
	w := topologyRequest(http.MethodGet, "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tunnels":[]`) || !strings.Contains(w.Body.String(), `"main"`) {
		t.Fatalf("stale GET: %d %s", w.Code, w.Body.String())
	}
	after, _ := os.ReadFile(topologyPath())
	if string(before) != string(after) {
		t.Fatal("GET mutated stale bindings")
	}
	if w := topologyRequest(http.MethodPatch, group.ID, `{"name":"Still saved"}`); w.Code != http.StatusOK {
		t.Fatalf("rename stale topology: %d %s", w.Code, w.Body.String())
	}
	if w := topologyRequest(http.MethodPatch, group.ID, `{"remote_ip":"203.0.113.2"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("cannot verify stale identity change: %d %s", w.Code, w.Body.String())
	}
	if w := topologyRequest(http.MethodPatch, group.ID, `{"tunnel_ids":[]}`); w.Code != http.StatusOK {
		t.Fatalf("detach stale: %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{http.MethodPatch, http.MethodDelete} {
		if w := topologyRequest(method, "missing", `{"name":"Missing"}`); w.Code != http.StatusNotFound {
			t.Fatalf("missing %s: %d", method, w.Code)
		}
		if w := topologyRequest(method, "../escape", `{}`); w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe ID %s: %d", method, w.Code)
		}
	}
	if w := topologyRequest(http.MethodPatch, group.ID, `{}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty PATCH: %d", w.Code)
	}
}

func TestTopologyCountLimitAndConcurrentOwnership(t *testing.T) {
	topologyFixture(t)
	var wg sync.WaitGroup
	var successes, conflicts atomic.Int32
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			w := topologyRequest(http.MethodPost, "", fmt.Sprintf(`{"name":"Concurrent %d","tunnel_ids":["main"]}`, i))
			switch w.Code {
			case http.StatusCreated:
				successes.Add(1)
			case http.StatusConflict:
				conflicts.Add(1)
			default:
				t.Errorf("concurrent status: %d %s", w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	if successes.Load() != 1 || conflicts.Load() != 15 {
		t.Fatalf("ownership race: successes=%d conflicts=%d", successes.Load(), conflicts.Load())
	}
	all := []Topology{}
	for i := 0; i < topologyMaxCount; i++ {
		all = append(all, Topology{ID: fmt.Sprintf("topo_%d", i), Name: fmt.Sprintf("Pair %d", i), Role: "iran", TunnelIDs: []string{}, CreatedAt: 1})
	}
	if err := saveTopologies(all); err != nil {
		t.Fatal(err)
	}
	if w := topologyRequest(http.MethodPost, "", `{"name":"Over limit"}`); w.Code != http.StatusConflict {
		t.Fatalf("limit status: %d %s", w.Code, w.Body.String())
	}
	if w := topologyRequest(http.MethodGet, "", ""); w.Code != http.StatusOK {
		t.Fatalf("100 topologies GET: %d", w.Code)
	}
}

func TestTopologyAtomicPrivatePersistence(t *testing.T) {
	topologyFixture(t)
	topologyCreated(t, `{"name":"First"}`)
	// Atomic replacement must reset even an existing file's loose permissions.
	if err := os.Chmod(topologyPath(), 0644); err != nil {
		t.Fatal(err)
	}
	topologyCreated(t, `{"name":"Second"}`)
	info, err := os.Stat(topologyPath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o, want 0600", info.Mode().Perm())
	}
	leftovers, _ := filepath.Glob(filepath.Join(configDir, ".topologies-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files: %v", leftovers)
	}
	if err := os.Remove(topologyPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(topologyPath(), 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(topologyPath(), "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveTopologies([]Topology{}); err == nil {
		t.Fatal("rename over nonempty directory unexpectedly succeeded")
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "preserve" {
		t.Fatalf("failed replacement damaged destination: %s %v", got, err)
	}
	leftovers, _ = filepath.Glob(filepath.Join(configDir, ".topologies-*"))
	if len(leftovers) != 0 {
		t.Fatalf("failed write leaked temporary files: %v", leftovers)
	}
}

func TestTopologyCorruptPersistenceFailsClosed(t *testing.T) {
	for _, data := range []string{
		`garbage`, `null`, `{}`, `{"topologies":null}`, `{"topologies":[]} {}`,
		`{"topologies":[{"id":"../bad","name":"bad","role":"iran","created_at":1}]}`,
		`{"topologies":[{"id":"safe","name":"bad","role":"iran","created_at":0}]}`,
		`{"topologies":[{"id":"a","name":"A","role":"iran","created_at":1,"tunnel_ids":["main"]},{"id":"b","name":"B","role":"iran","created_at":1,"tunnel_ids":["main"]}]}`,
		strings.Repeat("x", topologyMaxFile+1),
	} {
		t.Run(data[:min(len(data), 40)], func(t *testing.T) {
			topologyFixture(t)
			if err := os.WriteFile(topologyPath(), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete} {
				w := topologyRequest(method, "safe", `{"name":"Do not overwrite"}`)
				if w.Code != http.StatusInternalServerError {
					t.Fatalf("corrupt %s: %d %s", method, w.Code, w.Body.String())
				}
			}
			got, _ := os.ReadFile(topologyPath())
			if string(got) != data {
				t.Fatal("corrupt collection automatically rewritten")
			}
		})
	}
}

func TestTopologyInventorySnapshotOnlyAndNoSecrets(t *testing.T) {
	withTempConfig(t)
	oldSnap, oldRunner := snapPtr.Load(), runner
	var commands atomic.Int32
	runner = func(context.Context, string, ...string) ([]byte, error) {
		commands.Add(1)
		return nil, fmt.Errorf("no commands expected")
	}
	t.Cleanup(func() { snapPtr.Store(oldSnap); runner = oldRunner })
	snapPtr.Store(nil)
	if got := readTopologyInventory(); len(got) != 0 || commands.Load() != 0 {
		t.Fatalf("cold inventory starts host collection: %+v commands=%d", got, commands.Load())
	}
	snap := &HubSnapshot{
		Local:  tunnelStatus{Role: "iran", Engine: "frp", LocalPub: "203.0.113.1", Matrix: &matrixStatus{Enabled: true, Role: "hub", Family: "v6", Addr: "2001:db8::1", RemotePub: "203.0.113.2", HealthState: "healthy", Ports: []int{443}}},
		Main:   &peerLive{peerRecord: peerRecord{ID: mainTunnelID, Name: "Legacy", Engine: "frp", Token: "secret-peer", RemotePub: "203.0.113.2", Ports: []int{443}}, HealthState: "healthy", LatencyMs: 12},
		Peers:  []peerLive{{peerRecord: peerRecord{ID: 7, Name: "Peer", Engine: "backhaul", RemotePub: "203.0.113.2", Token: "secret-other"}, HealthState: "degraded", LatencyMs: -1}},
		Bagher: map[string]any{"has_config": true, "name": "Bagher", "remote_host": "203.0.113.2", "role": "client", "ports": []int{8443}, "connected": true, "latency_ms": 4.0, "private_key": "secret-bagher"},
	}
	snapPtr.Store(snap)
	if err := saveXrayState(xrayState{Links: []XrayLink{{ID: 9, Name: "Xray", Side: "iran", Remote: "203.0.113.2", Ports: []int{2083}, Keys: XrayKeys{UUID: "secret-uuid", PrivateKey: "secret-xray"}}}}); err != nil {
		t.Fatal(err)
	}
	xrayHealthCache.Lock()
	oldHealth, hadHealth := xrayHealthCache.m[9]
	xrayHealthCache.m[9] = xrayHealth{Link: "up", ProbeMS: 7, Error: "secret-error"}
	xrayHealthCache.Unlock()
	t.Cleanup(func() {
		xrayHealthCache.Lock()
		defer xrayHealthCache.Unlock()
		if hadHealth {
			xrayHealthCache.m[9] = oldHealth
		} else {
			delete(xrayHealthCache.m, 9)
		}
	})
	got := readTopologyInventory()
	ids := []string{}
	for _, tunnel := range got {
		ids = append(ids, tunnel.ID)
		if tunnel.Ports == nil {
			t.Errorf("nil ports: %s", tunnel.ID)
		}
	}
	if !reflect.DeepEqual(ids, []string{"main", "peer:7", "matrix", "bagher", "xray:9"}) {
		t.Fatalf("canonical inventory: %v", ids)
	}
	if got[len(got)-1].LatencyMs != 7 || got[len(got)-1].State != "healthy" || commands.Load() != 0 {
		t.Fatalf("cached health: %+v commands=%d", got, commands.Load())
	}
	encoded, _ := json.Marshal(got)
	for _, secret := range []string{"secret-", "private_key", "token", "uuid", "remote_user", "last_error"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("inventory exposes secret: %s", encoded)
		}
	}
	// The inventory copies mutable slices, rather than modifying a snapshot.
	got[0].Ports[0] = 80
	if snap.Main.Ports[0] != 443 {
		t.Fatal("inventory mutates snapshot ports")
	}
}

func TestTopologyInventoryMatrixOnlyAndRegistrationAlias(t *testing.T) {
	withTempConfig(t)
	old := snapPtr.Load()
	t.Cleanup(func() { snapPtr.Store(old) })
	st := matrixState{Enabled: true, Role: "node", Addr: "2001:db8::1", Panel: "https://203.0.113.1/panel", Network: matrixNetname}
	data, _ := json.Marshal(st)
	if err := os.WriteFile(matrixStatePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	snapPtr.Store(&HubSnapshot{Local: tunnelStatus{Engine: "matrix", Role: "foreign (matrix node)", RemotePub: st.Addr,
		Matrix: &matrixStatus{Enabled: true, Role: "node", Family: "v6", Addr: st.Addr, RemotePub: st.Addr, HealthState: "healthy", LatencyMs: 10, Ports: []int{443}}}})
	got := readTopologyInventory()
	if len(got) != 1 || got[0].ID != "matrix" || got[0].Family != "v6" || !reflect.DeepEqual(got[0].RemoteIPs, []string{"203.0.113.1"}) {
		t.Fatalf("Matrix-only inventory or v4 alias: %+v", got)
	}
	for _, remote := range []string{"203.0.113.1", "2001:db8::1"} {
		group := Topology{ID: "new", Name: "Pair", Role: "foreign", RemoteIP: remote, TunnelIDs: []string{"matrix"}}
		if _, err := validateTopologyBindings(group, nil, got, true); err != nil {
			t.Fatalf("known Matrix endpoint %s rejected: %v", remote, err)
		}
	}
	group := Topology{ID: "new", Name: "Pair", Role: "foreign", RemoteIP: "203.0.113.9", TunnelIDs: []string{"matrix"}}
	if status, err := validateTopologyBindings(group, nil, got, true); err == nil || status != http.StatusConflict {
		t.Fatal("arbitrary Matrix v4 incorrectly accepted")
	}
	st.Panel = "https://hub.example/panel"
	data, _ = json.Marshal(st)
	if err := os.WriteFile(matrixStatePath(), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := readTopologyInventory(); len(got[0].RemoteIPs) != 0 {
		t.Fatalf("inventory guessed DNS alias: %+v", got)
	}
}
