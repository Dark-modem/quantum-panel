package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Exercise the real metadata handlers with isolated files and counters. The
// active Matrix bridge is deliberately represented by immutable test data;
// nothing in this contract may collect, probe, restart, or adopt a real tunnel.
func TestTopologyDashboardNameContract(t *testing.T) {
	topologyFixture(t)
	oldSnap, oldUsage := snapPtr.Load(), trafficUsage
	oldRunner, oldDial, oldSystemctl := runner, dialFn, runSystemctl
	runner = func(context.Context, string, ...string) ([]byte, error) {
		t.Error("metadata handler attempted a host command")
		return nil, fmt.Errorf("metadata contract forbids commands")
	}
	dialFn = func(string, string, time.Duration) (net.Conn, error) {
		t.Error("metadata handler attempted a network probe")
		return nil, fmt.Errorf("metadata contract forbids probes")
	}
	runSystemctl = func(...string) error {
		t.Error("metadata handler attempted service mutation")
		return fmt.Errorf("metadata contract forbids service mutation")
	}
	t.Cleanup(func() {
		snapPtr.Store(oldSnap)
		trafficUsage = oldUsage
		runner, dialFn, runSystemctl = oldRunner, oldDial, oldSystemctl
	})
	snap := &HubSnapshot{
		At: t0, CountersAt: t0, Seq: 443,
		Local: tunnelStatus{Engine: "matrix", Role: "iran", Matrix: &matrixStatus{
			Enabled: true, Service: true, Connected: true, Role: "hub", Ports: []int{443},
			Rx: up64(12000), Tx: up64(34000), HealthState: "healthy", LatencyMs: 9,
		}},
		Traffic: map[string]any{"up_bytes": uint64(77000), "down_bytes": uint64(88000)},
		ServerTraffic: serverTrafficMetrics{
			Rx: up64(91000), Tx: up64(92000), Interfaces: []string{"ens5"}, Known: true,
		},
		ActiveConns: 73,
		DevData:     devLines(map[string][2]uint64{"ens5": {91000, 92000}, "qmx0": {12000, 34000}}),
		CarrierTags: map[string][2]uint64{"bagher:1": {40, 60}},
	}
	snapPtr.Store(snap)
	trafficUsage = newUsage()
	trafficUsage.observe("main", "Original engine label", t0, up64(100), up64(200))
	trafficUsage.observe("main", "Original engine label", t0.Add(time.Minute), up64(140), up64(260))
	trafficUsage.observeIfs(usageHostKey, "Existing tunnel total", t0, map[string][2]uint64{"qmx0": {1000, 2000}})
	trafficUsage.observeIfs(usageHostKey, "Existing tunnel total", t0.Add(time.Minute), map[string][2]uint64{"qmx0": {1040, 2060}})
	files := map[string]string{
		"matrix.json":                  "active Matrix443 engine identity and secret",
		"peers.json":                   "original peer identity and token",
		"link_names.json":              "original engine labels",
		"traffic_state.json":           "original lifetime tunnel counters",
		"traffic_peers.json":           "original persisted link history",
		"frp/frpc.toml":                "original engine configuration",
		"units/quantum-matrix.service": "original unit definition",
	}
	for name, data := range files {
		path := filepath.Join(configDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	beforeSnapshot := topologyContractJSON(t, snap)
	beforeLedger := topologyContractJSON(t, trafficUsage.links)
	beforeInventory := topologyContractJSON(t, topologyInventory())
	assertPhysicalUnchanged := func() {
		t.Helper()
		if snapPtr.Load() != snap || topologyContractJSON(t, snap) != beforeSnapshot {
			t.Fatal("grouping edit changed physical snapshot, engine identity, or counters")
		}
		if topologyContractJSON(t, trafficUsage.links) != beforeLedger {
			t.Fatal("grouping edit reset or relabeled existing link history")
		}
		if topologyContractJSON(t, topologyInventory()) != beforeInventory {
			t.Fatal("grouping edit changed physical inventory")
		}
		for name, want := range files {
			got, err := os.ReadFile(filepath.Join(configDir, name))
			if err != nil || string(got) != want {
				t.Fatalf("grouping edit changed %s: %q (%v)", name, got, err)
			}
		}
		if err := filepath.WalkDir(configDir, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			name, err := filepath.Rel(configDir, path)
			if err == nil && filepath.ToSlash(name) != "topologies.json" {
				if _, ok := files[filepath.ToSlash(name)]; !ok {
					t.Errorf("metadata handler wrote an unexpected file: %s", name)
				}
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	group := topologyCreated(t, `{"name":"  مسیر Matrix443  ","role":"iran","local_ip":"203.0.113.1","remote_ip":"203.0.113.2","tunnel_ids":["matrix","main"]}`)
	if group.Name != "مسیر Matrix443" {
		t.Fatalf("POST did not return canonical topology name: %+v", group)
	}
	assertPhysicalUnchanged()
	assertGet := func(want Topology) {
		t.Helper()
		w := topologyRequest(http.MethodGet, "", "")
		var response struct {
			Topologies []Topology       `json:"topologies"`
			Tunnels    []topologyTunnel `json:"tunnels"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || len(response.Topologies) != 1 || !reflect.DeepEqual(response.Topologies[0], want) {
			t.Fatalf("GET lost topology name or identity: %d %s", w.Code, w.Body.String())
		}
		if topologyContractJSON(t, response.Tunnels) != beforeInventory {
			t.Fatal("GET relabeled engine inventory using the topology name")
		}
	}
	assertGet(group)
	w := topologyRequest(http.MethodPatch, group.ID, `{"name":"  Renamed <Matrix443> & مسیر  "}`)
	var response struct {
		Topology Topology `json:"topology"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil {
		t.Fatalf("name PATCH failed: %d %s", w.Code, w.Body.String())
	}
	want := group
	want.Name = "Renamed <Matrix443> & مسیر"
	if !reflect.DeepEqual(response.Topology, want) {
		t.Fatalf("name PATCH changed identity, timestamps, endpoints or assignments: got %+v want %+v", response.Topology, want)
	}
	assertGet(want)
	assertPhysicalUnchanged()
	beforeMetadata, err := os.ReadFile(topologyPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"name":""}`, `{"name":"a\nb"}`, `{"name":"Oops","id":"new"}`, `{"name":"Oops","engine":"matrix"}`, `{"name":"Oops","created_at":1}`} {
		w := topologyRequest(http.MethodPatch, group.ID, body)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("unsafe name PATCH accepted: %s => %d %s", body, w.Code, w.Body.String())
		}
		after, err := os.ReadFile(topologyPath())
		if err != nil || string(after) != string(beforeMetadata) {
			t.Fatal("rejected rename changed persisted grouping")
		}
		assertPhysicalUnchanged()
	}
	w = topologyRequest(http.MethodDelete, group.ID, "")
	if w.Code != http.StatusConflict {
		t.Fatalf("assigned tunnels must block deletion: %d %s", w.Code, w.Body.String())
	}
	assertPhysicalUnchanged()
	originalInventory := topologyInventory
	topologyInventory = func() []topologyTunnel { return []topologyTunnel{} }
	w = topologyRequest(http.MethodDelete, group.ID, "")
	if w.Code != http.StatusOK {
		t.Fatalf("group DELETE after engine removal failed: %d %s", w.Code, w.Body.String())
	}
	topologyInventory = originalInventory
	// Deleting the topology purges the usage records still keyed by its listed
	// tunnels; the physical snapshot and unrelated live series stay untouched.
	if snapPtr.Load() != snap || topologyContractJSON(t, snap) != beforeSnapshot {
		t.Fatal("group delete changed physical snapshot, engine identity, or counters")
	}
	if trafficUsage.links["main"] != nil {
		t.Fatal("group delete must purge the usage records of its listed tunnels")
	}
	if l := trafficUsage.links[usageHostKey]; l == nil || len(l.Points) != 1 || l.CumulativeRx != 40 {
		t.Fatalf("group delete must not purge unrelated live series: %+v", l)
	}
	for name, want := range files {
		if name == "traffic_peers.json" {
			continue // the deletion purge rewrites the ledger file
		}
		got, err := os.ReadFile(filepath.Join(configDir, name))
		if err != nil || string(got) != want {
			t.Fatalf("grouping delete changed %s: %q (%v)", name, got, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(configDir, "traffic_peers.json")); err != nil || !json.Valid(data) {
		t.Fatalf("purge must leave a valid persisted ledger: %q (%v)", data, err)
	}
}

// Current-assignment usage is keyed by stable topology identity, never its
// display name. Renaming must preserve the requested-window sum without adding
// the already-aggregated host series or changing the underlying ledger.
func TestTopologyDashboardRenameUsageContract(t *testing.T) {
	topologyFixture(t)
	s := newUsage()
	for _, row := range []struct {
		key    string
		rx, tx uint64
	}{
		{"main", 50, 100}, {"matrix", 500, 400}, {"p3", 7, 11},
		{usageHostKey, 999000, 888000},
	} {
		s.observe(row.key, "Original engine "+row.key, t0, up64(1000), up64(2000))
		s.observe(row.key, "Original engine "+row.key, t0.Add(time.Minute), up64(1000+row.rx), up64(2000+row.tx))
		// Old recorded traffic must not leak into the one-hour group total.
		l := s.links[row.key]
		l.Points = append([]usagePoint{{T: minuteOf(t0.Add(-2 * time.Hour)), Rx: 70000, Tx: 80000}}, l.Points...)
	}
	snapshot := &HubSnapshot{
		Local: tunnelStatus{Engine: "frp", Role: "iran", Matrix: &matrixStatus{Enabled: true}},
		Main:  &peerLive{peerRecord: peerRecord{ID: mainTunnelID, GreIf: "gre-tunnel", Engine: "frp"}},
		Peers: []peerLive{{peerRecord: peerRecord{ID: 3, GreIf: "gre-t3", Engine: "backhaul"}}},
	}
	group := topologyCreated(t, `{"name":"Original group","role":"iran","remote_ip":"203.0.113.2","tunnel_ids":["main","matrix"]}`)
	now := t0.Add(2 * time.Minute)
	beforeLedger := topologyContractJSON(t, s.links)
	beforeSnapshot := topologyContractJSON(t, snapshot)
	getTotals := func() ([]map[string]any, map[string]any) {
		t.Helper()
		groups, err := loadTopologies()
		if err != nil {
			t.Fatal(err)
		}
		rows, unassigned := s.topologyTotals(now, time.Hour, 60, groups, topologyInventory(), snapshot)
		var result []map[string]any
		var rest map[string]any
		if err := json.Unmarshal([]byte(topologyContractJSON(t, rows)), &result); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(topologyContractJSON(t, unassigned)), &rest); err != nil {
			t.Fatal(err)
		}
		return result, rest
	}
	assertRow := func(row map[string]any, id, name string, rx, tx, count float64) {
		t.Helper()
		if row["id"] != id || row["name"] != name || row["rx"] != rx || row["tx"] != tx || row["total"] != rx+tx || row["known"] != true || row["tunnel_count"] != count {
			t.Fatalf("group row did not use only current assigned window counters: %+v", row)
		}
	}
	rows, unassigned := getTotals()
	if len(rows) != 1 {
		t.Fatalf("expected one topology row, got %+v", rows)
	}
	assertRow(rows[0], group.ID, group.Name, 550, 500, 2)
	if unassigned["rx"] != float64(7) || unassigned["tx"] != float64(11) || unassigned["total"] != float64(18) {
		t.Fatalf("unassigned must be tunnel counters, not physical-minus-topology or host duplication: %+v", unassigned)
	}
	w := topologyRequest(http.MethodPatch, group.ID, `{"name":"Renamed grouping"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rename failed: %d %s", w.Code, w.Body.String())
	}
	renamed, stillUnassigned := getTotals()
	if len(renamed) != 1 {
		t.Fatalf("rename changed row cardinality: %+v", renamed)
	}
	assertRow(renamed[0], group.ID, "Renamed grouping", 550, 500, 2)
	expected := rows[0]
	expected["name"] = "Renamed grouping"
	if !reflect.DeepEqual(renamed[0], expected) || !reflect.DeepEqual(unassigned, stillUnassigned) {
		t.Fatal("rename changed group data beyond its display name")
	}
	// The same historical ledger is viewed through current assignments. Detach
	// changes the group sum, not any physical counter or recorded link history.
	w = topologyRequest(http.MethodPatch, group.ID, `{"tunnel_ids":["matrix"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("detach failed: %d %s", w.Code, w.Body.String())
	}
	rows, unassigned = getTotals()
	assertRow(rows[0], group.ID, "Renamed grouping", 500, 400, 1)
	if unassigned["rx"] != float64(57) || unassigned["tx"] != float64(111) || unassigned["total"] != float64(168) {
		t.Fatalf("detached link history was not reflected in current unassigned view: %+v", unassigned)
	}
	if topologyContractJSON(t, s.links) != beforeLedger || topologyContractJSON(t, snapshot) != beforeSnapshot {
		t.Fatal("usage aggregation or grouping edits mutated measurements/history")
	}
}

func topologyContractJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Inspect actual call nodes, not comments or token-shaped strings. Follow local
// helpers from the metadata handlers, including the injected inventory's real
// default, to guard against accidentally moving host actions into polling paths.
func TestTopologyDashboardMetadataSourceNoCommands(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "topology.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range file.Imports {
		if strings.Contains(imp.Path.Value, "os/exec") {
			t.Fatal("topology metadata imports command execution")
		}
	}
	funcs := map[string]*ast.FuncDecl{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok {
			funcs[fn.Name.Name] = fn
		}
	}
	queue := []string{"handleTopologiesGet", "handleTopologiesPost", "handleTopologyPatch", "handleTopologyDelete", "readTopologyInventory"}
	seen := map[string]bool{}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		fn := funcs[name]
		if fn == nil {
			t.Fatalf("metadata function %s missing", name)
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch callee := call.Fun.(type) {
			case *ast.Ident:
				if _, local := funcs[callee.Name]; local {
					queue = append(queue, callee.Name)
				}
				if strings.HasPrefix(callee.Name, "runCmd") || strings.HasPrefix(callee.Name, "runSystemctl") || strings.HasPrefix(callee.Name, "saveMatrix") || callee.Name == "currentSnapshot" || callee.Name == "localStatus" || callee.Name == "livePeers" || callee.Name == "dialFn" || callee.Name == "xrayCachedHealth" || callee.Name == "xrayLinkView" {
					t.Errorf("metadata helper %s invokes host action/collection %s", name, callee.Name)
				}
			case *ast.SelectorExpr:
				if pkg, ok := callee.X.(*ast.Ident); ok && ((pkg.Name == "exec") || (pkg.Name == "http" && (callee.Sel.Name == "Get" || callee.Sel.Name == "Post" || callee.Sel.Name == "Do")) || (pkg.Name == "net" && strings.HasPrefix(callee.Sel.Name, "Dial"))) {
					t.Errorf("metadata helper %s invokes %s.%s", name, pkg.Name, callee.Sel.Name)
				}
			}
			return true
		})
	}
}
