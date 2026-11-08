package panel

// backup_restore_full_test.go — TASK 5 (phase 12): backup → wipe →
// restore on a CLEAN config dir must bring back EVERYTHING, including
// the phase-11/12 data (migration 5 engine artifacts, migration 6
// engine status / proc / events) and the hourly rollups — verified by
// comparing store reads before and after.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
	protocol "github.com/Dark-modem/quantum-protocol"
)

func TestBackupRestoreCoversAllTables(t *testing.T) {
	withTempConfig(t)
	// FILE-backed (the backup snapshots <configDir>/quantum.db — the
	// in-memory test store has no file to snapshot).
	fs, err := store.Open(filepath.Join(configDir, "quantum.db"))
	if err != nil {
		t.Fatal(err)
	}
	MeshStoreSetForTest(fs)
	t.Cleanup(MeshStoreCloseForTest)
	s := meshStoreMust(t)

	// ---- seed EVERY data class ----
	dc, _ := s.UpsertDatacenter(store.Datacenter{Kind: "iran", Name: "bk-tehran", PriceCoefficient: 3.0})
	sv, _ := s.UpsertServer(store.Server{Role: "iran", Name: "bk-ir", DatacenterID: dc})
	if err := s.UpsertAgent(store.Agent{ID: "agt_bk", ServerID: sv, Role: "iran",
		Name: "bk", Arch: "amd64", OS: "linux", LastSeen: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	topo, _ := s.UpsertTopology(store.Topology{Name: "bk-topo", Enabled: true})
	tun, err := s.UpsertTunnel(store.Tunnel{TunnelKey: "bk-t1", TopologyID: topo,
		LocalServerID: sv, RemoteServerID: sv, Engine: "chisel", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetWeightPolicy(topo, sv, sv, tun, 40); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	// Traffic samples + the rollup + engine artifacts + engine status +
	// proc + events.
	for i := int64(0); i < 3; i++ {
		_ = s.AddTrafficSample(store.TrafficSample{TunnelID: tun, At: now - 7200 + i*1800,
			WireRx: uint64(1000 + i*1000), WireTx: 100, PayloadRx: uint64(900 + i*900), PayloadTx: 90,
			CountersKnown: true, PayloadKnown: true, AgentID: "agt_bk"})
	}
	if n, err := s.RollupTrafficHourly(now); err != nil || n == 0 {
		t.Fatalf("fold: n=%d err=%v", n, err)
	}
	if _, err := s.UpsertEngineArtifact(store.EngineArtifact{
		Engine: "chisel", Version: "v9", Goos: "linux", Goarch: "amd64",
		Artifact: "chisel-bk", SHA256: "abc", Size: 5, Signature: "c2ln",
		UploadedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: "agt_bk", Engine: "chisel",
		Installed: true, Version: "v1.10.1", BytesOnDisk: 123, Desired: true, Instances: 1,
		UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertTunnelProc(store.TunnelProcNow{TunnelID: tun, AgentID: "agt_bk",
		CPUPct: 4.5, RSSBytes: 9 << 20, Processes: 1, At: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTunnelEvent(store.TunnelEvent{TunnelID: tun, At: now, Kind: "state",
		FromState: "down", ToState: "up"}); err != nil {
		t.Fatal(err)
	}

	snapshot := func(st *store.Store) map[string]any {
		out := map[string]any{}
		var wireUsage store.Usage
		all, _, err := st.RangeUsageDetailed(nil, 0, now+3600)
		if err == nil {
			wireUsage = all
		}
		out["usage_wire_rx"] = wireUsage.WireRx
		{ // watermark: the fold is idempotent
			var folded int64
			_ = st.DB().QueryRow(`SELECT COUNT(*) FROM traffic_hourly`).Scan(&folded)
			out["hourly_rows"] = folded
		}
		arts, _ := st.ListEngineArtifacts()
		out["artifacts"] = len(arts)
		engs, _ := st.AgentEngines("agt_bk")
		out["agent_engines"] = len(engs)
		if len(engs) == 1 {
			out["engine_version"] = engs[0].Version
		}
		procs, _ := st.TunnelProcFor([]int64{tun})
		out["proc_rows"] = len(procs)
		if p, ok := procs[tun]; ok {
			out["proc_rss"] = p.RSSBytes
		}
		evs, _ := st.TunnelEvents(tun, 10)
		out["events"] = len(evs)
		w, _ := st.WeightFor(topo, sv, sv, tun)
		out["weight"] = w
		ags, _ := st.ListAgents()
		var arch string
		for _, a := range ags {
			if a.ID == "agt_bk" {
				arch = a.Arch
			}
		}
		out["agent_arch"] = arch
		return out
	}
	before := snapshot(s)

	// ---- backup (encrypted) ----
	bakFile := filepath.Join(t.TempDir(), "full.bak")
	passFile := filepath.Join(t.TempDir(), "pass")
	if err := os.WriteFile(passFile, []byte("full-restore-pass-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	tarBytes, _, err := buildBackupTar()
	if err != nil {
		t.Fatalf("buildBackupTar: %v", err)
	}
	if err := backupEncryptToFile(bakFile, []byte("full-restore-pass-1"), tarBytes); err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if fi, err := os.Stat(bakFile); err != nil || fi.Size() < 2048 {
		t.Fatalf("backup too small: %v", err)
	}

	// ---- wipe + restore on a CLEAN config dir ----
	clean := filepath.Join(t.TempDir(), "clean-cfg")
	if err := os.MkdirAll(clean, 0o700); err != nil {
		t.Fatal(err)
	}
	oldCfg := configDir
	configDir = clean
	t.Cleanup(func() { configDir = oldCfg })
	MeshStoreCloseForTest()
	if rc := RestoreCmd([]string{"--in", bakFile, "--passphrase-file", passFile, "--force"}); rc != 0 {
		t.Fatalf("RestoreCmd rc=%d", rc)
	}
	// Reopen the restored store.
	rs, err := store.Open(filepath.Join(clean, "quantum.db"))
	if err != nil {
		t.Fatalf("reopen restored store: %v", err)
	}
	MeshStoreSetForTest(rs)
	t.Cleanup(MeshStoreCloseForTest)

	after := snapshot(rs)
	for k, want := range before {
		got := after[k]
		if got != want {
			t.Fatalf("restored %s = %v, want %v (before/after mismatch)", k, got, want)
		}
	}
	// The migration-5/6 tables really carried data (not vacuously equal).
	if before["artifacts"].(int) != 1 || before["agent_engines"].(int) != 1 ||
		before["proc_rows"].(int) != 1 || before["events"].(int) != 1 ||
		before["hourly_rows"].(int64) < 1 || before["usage_wire_rx"].(uint64) != 3000 {
		t.Fatalf("seed did not cover the new tables: %v", before)
	}
	_ = protocol.TunnelRuntime{} // protocol anchor (samples ride the v2 path)
	t.Logf("backup→wipe→restore preserved: usage=%v hourly=%v artifacts=%v engines=%v proc=%v events=%v",
		before["usage_wire_rx"], before["hourly_rows"], before["artifacts"],
		before["agent_engines"], before["proc_rows"], before["events"])
}
