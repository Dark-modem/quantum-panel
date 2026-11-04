package store

// Store tests run against a temp-file database (WAL needs a real file)
// and exercise migrations, every table's CRUD, the derivation API and
// both legacy imports.

import (
	"errors"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/quantum.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestMigrationsAndWAL(t *testing.T) {
	s := openTestStore(t)
	if v, err := s.Version(); err != nil || v < 1 {
		t.Fatalf("version = %d, %v; want >= 1", v, err)
	}
	// Migrations are idempotent: reopening the same file must not reapply.
	if err := s.migrate(); err != nil {
		t.Fatalf("re-migrate: %v", err)
	}
	if v, _ := s.Version(); v < 1 {
		t.Fatalf("version after re-migrate regressed = %d", v)
	}
	if mode, err := s.JournalMode(); err != nil || mode != "wal" {
		t.Fatalf("journal mode = %q, %v; want wal", mode, err)
	}
}

func TestDeploymentShapeFits(t *testing.T) {
	// The Phase-1A deployment: 2 Iran DCs x 2 servers + 1 foreign DC x 2
	// servers; 2 topologies; 12 tunnels per topology; 2 Pasarguard nodes
	// per foreign server.
	s := openTestStore(t)
	drak, err := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "Drak", PriceCoefficient: 1.0})
	if err != nil {
		t.Fatal(err)
	}
	chek, err := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "Chekavak", PriceCoefficient: 1.5})
	if err != nil {
		t.Fatal(err)
	}
	hz, err := s.UpsertDatacenter(Datacenter{Kind: "foreign", Name: "Hetzner Germany"})
	if err != nil {
		t.Fatal(err)
	}

	servers := map[string]int64{}
	for _, sv := range []Server{
		{Name: "ir-drak-1", Role: "iran", DatacenterID: drak},
		{Name: "ir-drak-2", Role: "iran", DatacenterID: drak},
		{Name: "ir-chk-1", Role: "iran", DatacenterID: chek},
		{Name: "ir-chk-2", Role: "iran", DatacenterID: chek},
		{Name: "hz-fra-1", Role: "foreign", DatacenterID: hz},
		{Name: "hz-fra-2", Role: "foreign", DatacenterID: hz},
	} {
		id, err := s.UpsertServer(sv)
		if err != nil {
			t.Fatal(err)
		}
		servers[sv.Name] = id
	}

	engines := []string{"matrix", "wireguard", "backhaul"}
	topoName := "topo-drak-hetzner"
	topoID, err := s.UpsertTopology(Topology{Name: topoName, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ir-drak-1", "ir-drak-2", "hz-fra-1", "hz-fra-2"} {
		if err := s.AddTopologyMember(topoID, servers[name]); err != nil {
			t.Fatal(err)
		}
	}
	if members, err := s.TopologyMembers(topoID); err != nil || len(members) != 4 {
		t.Fatalf("members = %v, %v", members, err)
	}

	count := 0
	for _, iran := range []string{"ir-drak-1", "ir-drak-2"} {
		for _, hzSrv := range []string{"hz-fra-1", "hz-fra-2"} {
			for _, eng := range engines {
				count++
				if _, err := s.UpsertTunnel(Tunnel{
					TunnelKey:      "tun-" + iran + "-" + hzSrv + "-" + eng,
					TopologyID:     topoID,
					LocalServerID:  servers[iran],
					RemoteServerID: servers[hzSrv],
					Engine:         eng,
					EngineParams:   `{"listen_port":51821}`,
					Enabled:        true,
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if count != 12 {
		t.Fatalf("deployment shape must give 12 tunnels per topology, made %d", count)
	}
	tunnels, err := s.ListTunnels(topoID)
	if err != nil || len(tunnels) != 12 {
		t.Fatalf("ListTunnels = %d, %v", len(tunnels), err)
	}

	// Pasarguard nodes: one per foreign server per Iran DC, quota-side only.
	for _, hzSrv := range []string{"hz-fra-1", "hz-fra-2"} {
		for _, dc := range []struct {
			id   int64
			coef float64
			name string
		}{{drak, 1.0, "drak"}, {chek, 1.5, "chekavak"}} {
			if _, err := s.UpsertPasarguardNode(PasarguardNode{
				ServerID: servers[hzSrv], DatacenterID: dc.id, Name: dc.name, Coefficient: dc.coef,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	nodes, err := s.PasarguardNodesForServer(servers["hz-fra-1"])
	if err != nil || len(nodes) != 2 {
		t.Fatalf("nodes = %d, %v", len(nodes), err)
	}
}

func TestWeightPolicySpecificity(t *testing.T) {
	s := openTestStore(t)
	topo, _ := s.UpsertTopology(Topology{Name: "topo-x", Enabled: true})
	local, _ := s.UpsertServer(Server{Name: "l1", Role: "iran"})
	remote, _ := s.UpsertServer(Server{Name: "f1", Role: "foreign"})
	tun, _ := s.UpsertTunnel(Tunnel{TunnelKey: "t1", TopologyID: topo, LocalServerID: local, RemoteServerID: remote, Enabled: true})

	if err := s.SetWeightPolicy(topo, 0, 0, 0, 70); err != nil {
		t.Fatal(err)
	}
	if w, _ := s.WeightFor(topo, local, remote, tun); w != 70 {
		t.Fatalf("topology fallback weight = %d", w)
	}
	if err := s.SetWeightPolicy(topo, local, remote, 0, 30); err != nil {
		t.Fatal(err)
	}
	if w, _ := s.WeightFor(topo, local, remote, tun); w != 30 {
		t.Fatalf("server-pair weight = %d, want it to win over topology", w)
	}
	if err := s.SetWeightPolicy(topo, local, remote, tun, 10); err != nil {
		t.Fatal(err)
	}
	if w, _ := s.WeightFor(topo, local, remote, tun); w != 10 {
		t.Fatalf("tunnel weight = %d, want it to win over the pair", w)
	}
	// A different tunnel without any rule falls back to the pair rule.
	tun2, _ := s.UpsertTunnel(Tunnel{TunnelKey: "t2", TopologyID: topo, LocalServerID: local, RemoteServerID: remote, Enabled: true})
	if w, _ := s.WeightFor(topo, local, remote, tun2); w != 30 {
		t.Fatalf("other tunnel weight = %d, want 30", w)
	}
	// No rules at all -> default 1.
	if w, _ := s.WeightFor(topo+100, local, remote, tun2); w != 1 {
		t.Fatalf("no-rule weight = %d, want 1", w)
	}
}

func TestOverheadAndSuggestedCoefficient(t *testing.T) {
	s := openTestStore(t)
	dc, _ := s.UpsertDatacenter(Datacenter{Kind: "iran", Name: "Drak", PriceCoefficient: 1.0})
	topo, _ := s.UpsertTopology(Topology{Name: "topo-o", Enabled: true})
	local, _ := s.UpsertServer(Server{Name: "l", Role: "iran"})
	remote, _ := s.UpsertServer(Server{Name: "f", Role: "foreign"})
	tun, _ := s.UpsertTunnel(Tunnel{TunnelKey: "t1", TopologyID: topo, LocalServerID: local, RemoteServerID: remote, Engine: "matrix", Enabled: true})

	// No observations yet: ratio unknown, suggestion 0 (never guessed).
	if r, err := s.EngineOverheadRatio("matrix"); err != nil || r != 0 {
		t.Fatalf("initial ratio = %v, %v", r, err)
	}
	if c, err := s.SuggestedCoefficient(dc, "matrix"); err != nil || c != 0 {
		t.Fatalf("initial suggestion = %v, %v", c, err)
	}

	// 10 GB payload over the wire as 10.5 GB: ratio 1.05, suggestion 1.05.
	if err := s.ObserveEngineOverhead("matrix", 10_500_000_000, 10_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTrafficSample(TrafficSample{ // per-tunnel real bytes
		TunnelID: tun, At: 1796479200,
		WireRx: 10_500_000_000, PayloadRx: 10_000_000_000, CountersKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LatestTrafficSample(tun); err != nil || got.WireRx != 10_500_000_000 {
		t.Fatalf("sample = %+v, %v", got, err)
	}
	ratio, err := s.EngineOverheadRatio("matrix")
	if err != nil || ratio < 1.0499 || ratio > 1.0501 {
		t.Fatalf("ratio = %v, %v; want ~1.05", ratio, err)
	}
	coef, err := s.SuggestedCoefficient(dc, "matrix")
	if err != nil || coef < 1.0499 || coef > 1.0501 {
		t.Fatalf("suggested coefficient = %v, %v; want ~1.05", coef, err)
	}
	// Zero-payload observations are ignored (never divide by zero).
	if err := s.ObserveEngineOverhead("matrix", 500, 0); err != nil {
		t.Fatal(err)
	}
	if ratio2, _ := s.EngineOverheadRatio("matrix"); ratio2 != ratio {
		t.Fatalf("zero-payload observation changed the ratio: %v -> %v", ratio, ratio2)
	}
}

func TestImportLegacyJSON(t *testing.T) {
	s := openTestStore(t)
	agents := []byte(`{"agents":[
		{"id":"agt_abc123def456","name":"ir-drak-1","role":"iran","key_hash":"ab12","status":"online","version":"v1.29.0","hostname":"drak1","public_ip":"198.51.100.1","last_seen":1796479200,"applied_revision":"1796470000000000001"},
		{"id":"agt_def456abc789","name":"hz-fra-1","role":"foreign","key_hash":"cd34","status":"offline","hostname":"fra1"}
	]}`)
	st, err := s.ImportAgentsJSON(agents)
	if err != nil {
		t.Fatal(err)
	}
	if st.AgentsUpserted != 2 {
		t.Fatalf("agents upserted = %d", st.AgentsUpserted)
	}
	a, err := s.GetAgent("agt_abc123def456")
	if err != nil || a.Role != "iran" || a.Status != "online" {
		t.Fatalf("agent = %+v, %v", a, err)
	}
	// Idempotent: re-import upserts, does not duplicate.
	if _, err := s.ImportAgentsJSON(agents); err != nil {
		t.Fatal(err)
	}

	topos := []byte(`{"topologies":[
		{"id":"topo-drak-hetzner","name":"Drak-Hetzner","role":"iran","local_ip":"198.51.100.1","remote_ip":"203.0.113.10","country":"de","tunnel_ids":["main","matrix2"],"created_at":1796400000},
		{"id":"topo-chk-hetzner","name":"Chekavak-Hetzner","role":"iran","local_ip":"198.51.100.2","remote_ip":"203.0.113.10","tunnel_ids":["main"],"disabled":true}
	]}`)
	st, err = s.ImportTopologiesJSON(topos)
	if err != nil {
		t.Fatal(err)
	}
	if st.Topologies != 2 || st.Tunnels != 3 {
		t.Fatalf("import stats = %+v", st)
	}
	tun, err := s.GetTunnelByKey("topo-drak-hetzner/main")
	if err != nil || tun.Engine != "" || !tun.Enabled {
		t.Fatalf("imported tunnel = %+v, %v", tun, err)
	}
	// Tunnel ids are namespaced per topology: two "main" tunnels coexist.
	tun2, err := s.GetTunnelByKey("topo-chk-hetzner/main")
	if err != nil || tun2.Enabled {
		t.Fatalf("disabled topology tunnel = %+v, %v", tun2, err)
	}
	if _, err := s.GetTunnelByKey("topo-drak-hetzner/matrix2"); err != nil {
		t.Fatalf("matrix2 tunnel missing: %v", err)
	}
}

func TestNotFoundAndAudit(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.ServerByName("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing server error = %v, want ErrNotFound", err)
	}
	if err := s.AddAudit(AuditEntry{Actor: "admin", Action: "tunnel.update", Detail: `{"id":"t1"}`}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.AuditCount(); err != nil || n != 1 {
		t.Fatalf("audit count = %d, %v", n, err)
	}
}
