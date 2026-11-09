package panel

// portalloc_test.go — Phase 14 port allocator + the wired endpoints:
// deterministic lowest-free allocation over the configured ranges, the
// reservation set's sources (well-known / ssh_ports setting / observed
// listening / ledger / forward rules), the structured 409 the mesh
// forward-create returns on a taken port, target_port's round trip
// store → compile → wire DesiredV2, the forward delete's ledger release,
// the topology port view, and pgDesired dropping the coefficient from
// the data path (phase 14 decision).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	protocol "github.com/Dark-modem/quantum-protocol"
)

// portAllocSeed builds one foreign server (+ linked agent) on a private
// in-memory store and pins the panel listeners for the duration.
func portAllocSeed(t *testing.T) (*store.Store, int64, string) {
	t.Helper()
	s := withMeshStore(t)
	oldPort, oldTLS := cfg.Port, cfg.TLSPort
	cfg.Port, cfg.TLSPort = 7777, 7443 // the panel's own listeners count as reserved
	t.Cleanup(func() { cfg.Port, cfg.TLSPort = oldPort, oldTLS })
	sv, err := s.UpsertServer(store.Server{Name: "pa-node", Role: "foreign"})
	if err != nil {
		t.Fatal(err)
	}
	ag := "agt_pa_alloc"
	if err := s.UpsertAgent(store.Agent{ID: ag, ServerID: sv, Name: "pa-node", Role: "foreign", LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	return s, sv, ag
}

func TestPortAllocatorDeterministicLowest(t *testing.T) {
	s, sv, _ := portAllocSeed(t)

	// Preferred 0 = allocator's choice: the lowest free port of the
	// default pool. The well-known set (22/80/443/7777/7443/62050/62051)
	// sits outside 20000-29999, so 20000 is the winner — twice in a row,
	// deterministically.
	p1, err := AllocateServerPort(s, sv, "tcp", "test", "o1", 0)
	if err != nil || p1 != 20000 {
		t.Fatalf("first allocate = %d, %v (want 20000, nil)", p1, err)
	}
	if p2, _ := AllocateServerPort(s, sv, "tcp", "test", "o2", 0); p2 != 20001 {
		t.Fatalf("second allocate = %d (want 20001 — the booked port must not repeat)", p2)
	}

	// A free preferred port wins as-is.
	if p, _ := AllocateServerPort(s, sv, "tcp", "test", "o3", 25000); p != 25000 {
		t.Fatalf("preferred 25000 = %d (want 25000)", p)
	}
	// Reserved preferred ports fall through to the lowest free scan:
	// SSH and the Pasarguard node port are never handed out.
	if p, _ := AllocateServerPort(s, sv, "tcp", "test", "o4", SSH_PORT); p != 20002 {
		t.Fatalf("preferred 22 = %d (want 20002 — SSH must not be allocated)", p)
	}
	if p, _ := AllocateServerPort(s, sv, "tcp", "test", "o5", 62050); p != 20003 {
		t.Fatalf("preferred 62050 = %d (want 20003)", p)
	}

	// Alternatives are deterministic and free: three ports, none inside
	// the reservation set, same answer on every call.
	alt1 := freePortSuggestions(s, sv, 3)
	alt2 := freePortSuggestions(s, sv, 3)
	if !reflect.DeepEqual(alt1, alt2) || len(alt1) != 3 {
		t.Fatalf("alternatives not deterministic/3: %v vs %v", alt1, alt2)
	}
	reserved, err := reservedPortsFor(s, sv)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range alt1 {
		if reserved[p] {
			t.Fatalf("alternative %d is reserved: %v", p, alt1)
		}
	}
	if !reflect.DeepEqual(alt1, []int{20004, 20005, 20006}) {
		t.Fatalf("alternatives = %v (want 20004-20006)", alt1)
	}
}

func TestReservedPortsUnionSources(t *testing.T) {
	s, sv, ag := portAllocSeed(t)

	// Observed listening (the agent's reality)...
	if err := s.UpsertAgentListening(ag, []store.ListenRow{
		{Proto: "tcp", Port: 5353, Proc: "mdns"},
		{Proto: "udp", Port: 51820, Proc: "wg"},
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	// ...the ledger...
	if err := s.AllocatePort(sv, 21000, "tcp", "test", "o-x"); err != nil {
		t.Fatal(err)
	}
	// ...declared forward rules...
	topo, err := s.UpsertTopology(store.Topology{Name: "pa-topo"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateForwardRule(store.ForwardRule{
		TopologyID: &topo, LocalServerID: sv, ListenPort: 25000, Proto: "tcp", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// ...and the operator's extra SSH port.
	if err := s.SetSetting("ssh_ports", "[2222, 2223]"); err != nil {
		t.Fatal(err)
	}

	reserved, err := reservedPortsFor(s, sv)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []int{
		22, 80, 443, // well-known
		7777, 7443, // the panel's own HTTP/TLS listeners
		62050, 62051, // Pasarguard node + API
		2222, 2223, // ssh_ports setting
		5353, 51820, // observed listening
		21000, // ledger
		25000, // existing forward rule
	} {
		if !reserved[p] {
			t.Fatalf("port %d must be reserved; set: %v", p, reserved)
		}
	}

	// A broken setting must degrade to the safe default, never to "less
	// reserved": junk JSON adds nothing and removes nothing.
	if err := s.SetSetting("ssh_ports", "not json at all"); err != nil {
		t.Fatal(err)
	}
	reserved, err = reservedPortsFor(s, sv)
	if err != nil {
		t.Fatal(err)
	}
	if reserved[2222] || reserved[2223] {
		t.Fatal("junk ssh_ports must be ignored, not invented")
	}
	if !reserved[22] || !reserved[80] || !reserved[21000] || !reserved[25000] {
		t.Fatal("junk ssh_ports must not shrink the base reservation set")
	}
}

func TestPortRangesSetting(t *testing.T) {
	s, sv, _ := portAllocSeed(t)
	// Operator-configured pool: allocation follows it exactly.
	if err := s.SetSetting("port_ranges", `[{"from":30000,"to":30002}]`); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{30000, 30001, 30002} {
		p, err := AllocateServerPort(s, sv, "tcp", "test", fmt.Sprintf("o-%d", i), 0)
		if err != nil || p != want {
			t.Fatalf("allocate %d = %d, %v (want %d)", i, p, err, want)
		}
	}
	// Pool exhausted: honest failure, not a silent out-of-range pick.
	if _, err := AllocateServerPort(s, sv, "tcp", "test", "o-4", 0); err == nil {
		t.Fatal("exhausted pool must fail, not wrap")
	}
	// Junk range JSON falls back to the default pool (a typo in settings
	// must never leave the allocator without a pool).
	if err := s.SetSetting("port_ranges", "{{{"); err != nil {
		t.Fatal(err)
	}
	if p, err := AllocateServerPort(s, sv, "tcp", "test", "o-5", 0); err != nil || p != 20000 {
		t.Fatalf("fallback allocate = %d, %v (want 20000)", p, err)
	}
}

func TestForwardCreatePortConflict409(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	irServer, _, agIr, _ := uiSeedWorld(t, s)
	c := meshSession(t)

	create := func(port int) (*http.Response, string) {
		return meshPutRaw(t, c, srv, "POST", "/api/mesh/forwards", map[string]any{
			"local_server_id": irServer, "listen_port": port, "proto": "tcp", "enabled": true,
		})
	}
	conflict := func(port int, why string) {
		t.Helper()
		resp, body := create(port)
		if resp.StatusCode != http.StatusConflict {
			t.Fatalf("%s: port %d must be 409, got %d: %s", why, port, resp.StatusCode, body)
		}
		var env struct {
			ErrorCode string `json:"error_code"`
			UI        struct {
				Details struct {
					Port         int    `json:"port"`
					Alternatives []int  `json:"alternatives"`
					Raw          string `json:"raw"`
				} `json:"details"`
			} `json:"ui"`
		}
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatalf("%s: envelope: %v (%s)", why, err, body)
		}
		if env.ErrorCode != "E-ENGINE-PORT-CONFLICT" {
			t.Fatalf("%s: error_code = %s", why, env.ErrorCode)
		}
		if env.UI.Details.Port != port {
			t.Fatalf("%s: details.port = %d (want %d)", why, env.UI.Details.Port, port)
		}
		if len(env.UI.Details.Alternatives) != 3 {
			t.Fatalf("%s: alternatives = %v (want exactly 3)", why, env.UI.Details.Alternatives)
		}
		for _, alt := range env.UI.Details.Alternatives {
			if alt == port {
				t.Fatalf("%s: alternative repeats the taken port: %v", why, env.UI.Details.Alternatives)
			}
		}
	}

	// Well-known: SSH is never bookable.
	conflict(22, "well-known")
	// Ledger: a booked port conflicts even though no rule holds it yet.
	if err := s.AllocatePort(irServer, 21000, "tcp", "test", "o-test"); err != nil {
		t.Fatal(err)
	}
	conflict(21000, "ledger")
	// Observed: the agent reported the port listening on its host.
	if err := s.UpsertAgentListening(agIr, []store.ListenRow{{Proto: "tcp", Port: 21001, Proc: "app"}}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	conflict(21001, "observed")

	// A genuinely free port goes through and lands in the ledger under
	// the rule's owner key.
	resp, body := create(25000)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("free port must create: %d %s", resp.StatusCode, body)
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.ID == 0 {
		t.Fatalf("create response: %v (%s)", err, body)
	}
	allocs, err := s.ListPortAllocations(irServer)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range allocs {
		if a.Port == 25000 && a.OwnerKey == fmt.Sprintf("forward:%d", out.ID) && a.Purpose == "forward_rule" {
			found = true
		}
	}
	if !found {
		t.Fatalf("port 25000 must be booked for forward:%d: %+v", out.ID, allocs)
	}

	// Delete releases the booking (a freed port is reusable, not stuck).
	resp, _ = meshPutRaw(t, c, srv, "DELETE", fmt.Sprintf("/api/mesh/forwards/%d", out.ID), nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	allocs, _ = s.ListPortAllocations(irServer)
	for _, a := range allocs {
		if a.OwnerKey == fmt.Sprintf("forward:%d", out.ID) {
			t.Fatalf("delete must release the owner's booking: %+v", allocs)
		}
	}
	// And the freed port can be booked again by a new rule.
	resp, body = create(25000)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("freed port must be reusable: %d %s", resp.StatusCode, body)
	}
}

func TestForwardTargetPortStoreToWire(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	irServer, _, agIr, _ := uiSeedWorld(t, s)
	c := meshSession(t)

	topos, _ := s.ListTopologies()
	if len(topos) == 0 {
		t.Fatal("seed topology missing")
	}

	// Explicit target_port: stored, then compiled into the wire rule.
	resp, body := meshPutRaw(t, c, srv, "POST", "/api/mesh/forwards", map[string]any{
		"topology_id": topos[0].ID, "local_server_id": irServer,
		"listen_port": 25010, "target_port": 12345, "proto": "tcp", "enabled": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create with target_port: %d %s", resp.StatusCode, body)
	}
	var created struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &created)

	f, err := s.GetForwardRule(created.ID)
	if err != nil || f.TargetPort != 12345 {
		t.Fatalf("store round trip: %+v err=%v", f, err)
	}
	ag, err := s.GetAgent(agIr)
	if err != nil {
		t.Fatal(err)
	}
	desired, skip, _, err := compileDesiredV2(s, ag)
	if err != nil || skip != "" {
		t.Fatalf("compile: %v skip=%q", err, skip)
	}
	var wire *struct {
		ID         string
		ListenPort int
		TargetPort int
	}
	for i := range desired.Forwards {
		if desired.Forwards[i].ID == fmt.Sprintf("fw-%d", created.ID) {
			fw := desired.Forwards[i]
			wire = &struct {
				ID         string
				ListenPort int
				TargetPort int
			}{fw.ID, fw.ListenPort, fw.TargetPort}
		}
	}
	if wire == nil {
		t.Fatalf("compiled forwards lack fw-%d: %+v", created.ID, desired.Forwards)
	}
	if wire.ListenPort != 25010 || wire.TargetPort != 12345 {
		t.Fatalf("wire rule: listen=%d target=%d (want 25010/12345)", wire.ListenPort, wire.TargetPort)
	}

	// target_port 0 keeps the historical default: the foreign side serves
	// the SAME port as the listen side.
	resp, body = meshPutRaw(t, c, srv, "POST", "/api/mesh/forwards", map[string]any{
		"topology_id": topos[0].ID, "local_server_id": irServer,
		"listen_port": 25011, "proto": "tcp", "enabled": true,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create without target_port: %d %s", resp.StatusCode, body)
	}
	var created2 struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal([]byte(body), &created2)
	if f, _ := s.GetForwardRule(created2.ID); f.TargetPort != 0 {
		t.Fatalf("omitted target_port must store 0: %+v", f)
	}
	desired, _, _, err = compileDesiredV2(s, ag)
	if err != nil {
		t.Fatal(err)
	}
	for i := range desired.Forwards {
		if desired.Forwards[i].ID == fmt.Sprintf("fw-%d", created2.ID) && desired.Forwards[i].TargetPort != 25011 {
			t.Fatalf("default wire target = %d (want listen port 25011)", desired.Forwards[i].TargetPort)
		}
	}
}

func TestPGDesiredDropsCoefficient(t *testing.T) {
	s := withMeshStore(t)
	// Phase 14 decision: the per-datacenter price coefficient left the
	// product's DATA path — pgDesired must not carry it into the
	// reconciler's desired set (the store column and the billing report's
	// price hint display are display-only and stay).
	dcIr, err := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "pa-tehran"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDatacenterPriceCoefficient(dcIr, 2.5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertServer(store.Server{Name: "pa-fx", Role: "foreign", Address: "198.51.100.9", DatacenterID: dcIr}); err != nil {
		t.Fatal(err)
	}
	desired, _, err := pgDesired(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(desired) == 0 {
		t.Fatal("pgDesired produced no nodes")
	}
	for _, d := range desired {
		if d.Coefficient != 0 {
			t.Fatalf("desired node %s still carries a coefficient (%v)", d.Name, d.Coefficient)
		}
	}
}

func TestUITopologyPortsEndpoint(t *testing.T) {
	srv := uiServer(t)
	s := meshStoreMust(t)
	irServer, frServer, _, agFr := uiSeedWorld(t, s)
	c := meshSession(t)

	// Seed all three sources on the FOREIGN member: a booked port, the
	// agent's observed inventory; well-known ports arrive implicitly.
	if err := s.AllocatePort(frServer, 21000, "tcp", "forward_rule", "forward:9"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgentListening(agFr, []store.ListenRow{
		{Proto: "tcp", Port: 22, Proc: "sshd"},
		{Proto: "udp", Port: 53, Proc: "dnsmasq"},
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	topos, _ := s.ListTopologies()
	path := fmt.Sprintf("/api/ui/topologies/%d/ports", topos[0].ID)
	out := meshGetMap(t, c, srv, path)
	if int(out["topology_id"].(float64)) != int(topos[0].ID) {
		t.Fatalf("topology_id: %v", out["topology_id"])
	}
	servers, ok := out["servers"].([]any)
	if !ok || len(servers) != 2 {
		t.Fatalf("servers rows: %v", out["servers"])
	}
	byServer := map[int64]map[string]any{}
	for _, raw := range servers {
		row := raw.(map[string]any)
		byServer[int64(row["server_id"].(float64))] = row
	}
	frRow := byServer[frServer]
	if frRow == nil {
		t.Fatalf("foreign member missing: %v", byServer)
	}
	if frRow["name"] != "fra-1" {
		t.Fatalf("member name: %v", frRow["name"])
	}
	alloc, _ := frRow["allocated"].([]any)
	if len(alloc) != 1 {
		t.Fatalf("allocated rows: %v", frRow["allocated"])
	}
	a0 := alloc[0].(map[string]any)
	if a0["port"].(float64) != 21000 || a0["owner_key"] != "forward:9" || a0["purpose"] != "forward_rule" || a0["proto"] != "tcp" {
		t.Fatalf("allocated row: %v", a0)
	}
	var reserved []float64
	for _, r := range frRow["reserved"].([]any) {
		reserved = append(reserved, r.(float64))
	}
	for _, want := range []float64{22, 80, 443, 21000, 62050, 62051} {
		found := false
		for _, got := range reserved {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("reserved set lacks %v: %v", want, reserved)
		}
	}
	observed, _ := frRow["observed"].([]any)
	if len(observed) != 2 {
		t.Fatalf("observed rows: %v", frRow["observed"])
	}
	o0 := observed[0].(map[string]any)
	if o0["port"].(float64) != 22 || o0["proto"] != "tcp" || o0["proc"] != "sshd" {
		t.Fatalf("observed row 0: %v", o0)
	}
	// The iran member has no agent inventory seeded: empty lists, not an
	// error, and never the foreign member's data.
	irRow := byServer[irServer]
	if irRow == nil {
		t.Fatalf("iran member missing: %v", byServer)
	}
	if obs, _ := irRow["observed"].([]any); len(obs) != 0 {
		t.Fatalf("iran member must show an empty inventory: %v", obs)
	}
	if obs, _ := irRow["allocated"].([]any); len(obs) != 0 {
		t.Fatalf("iran member must show an empty ledger: %v", obs)
	}

	// Unknown topology: structured E-NOTFOUND, not a panic.
	resp, body := meshPutRaw(t, c, srv, "GET", "/api/ui/topologies/999999/ports", nil)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "E-NOTFOUND") {
		t.Fatalf("unknown topology: %d %s", resp.StatusCode, body)
	}
}

func TestIngestStoresListeningInventory(t *testing.T) {
	s := withMeshStore(t)
	dc, _ := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "pa-dc"})
	sv, _ := s.UpsertServer(store.Server{Role: "iran", Name: "pa-ir", DatacenterID: dc})
	ag := "agt_pa_ingest"
	if err := s.UpsertAgent(store.Agent{ID: ag, ServerID: sv, Name: "pa-ir", Role: "iran"}); err != nil {
		t.Fatal(err)
	}

	meshSampleMinGap = 0 // test: every heartbeat writes
	t.Cleanup(func() { meshSampleMinGap = 5 * time.Second })

	// The heartbeat's ResourceReport.Listening used to be received and
	// discarded; it must land in agent_listening (sanitized).
	ingestStateReportV2(ag, &protocol.StateReportV2{
		Resources: &protocol.ResourceReport{
			CPUCores: 4,
			Listening: []protocol.ListenPort{
				{Proto: "TCP", Port: 22, Proc: "sshd"},
				{Proto: "udp", Port: 53, Proc: "dnsmasq"},
				{Proto: "icmp", Port: 7}, // junk proto dropped
				{Proto: "tcp", Port: 0},  // junk port dropped
				{Proto: "tcp", Port: 22}, // duplicate (proto+port) collapses
			},
		},
	})
	time.Sleep(50 * time.Millisecond)
	rows, err := s.AgentListeningForServer(sv)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Proto != "tcp" || rows[0].Port != 22 || rows[1].Proto != "udp" {
		t.Fatalf("listening inventory after ingest: %+v", rows)
	}

	// The NEXT heartbeat is the full inventory: a closed socket (22 gone)
	// must stop reserving its port — replace, not accumulate.
	ingestStateReportV2(ag, &protocol.StateReportV2{
		Resources: &protocol.ResourceReport{
			Listening: []protocol.ListenPort{{Proto: "udp", Port: 53, Proc: "dnsmasq"}},
		},
	})
	time.Sleep(50 * time.Millisecond)
	rows, _ = s.AgentListeningForServer(sv)
	if len(rows) != 1 || rows[0].Port != 53 {
		t.Fatalf("snapshot must replace, not accumulate: %+v", rows)
	}
}
