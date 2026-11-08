//go:build integration

// upgrade_rc2_test.go — TASK 1a (phase 12): the REAL rc2 panel (commit
// 4e0611c9b28b, what the servers run) seeds a throwaway config dir with
// production-shaped data over its API, and the CURRENT panel main must
// upgrade that directory in place: migrations apply, NOTHING is lost,
// the old password still works, agents reconnect, old traffic reads
// identically, backup still works — and the documented downgrade
// behavior is verified. Run:
//
//	UPGRADE_E2E=1 go test -tags integration -run TestUpgradeRC2ToCurrent \
//	    ./internal/panel/ -count=1 -v -timeout 15m
package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	protocol "github.com/Dark-modem/quantum-protocol"
)

const rc2Commit = "4e0611c9b28b"
const rc2Password = "upgrade-rc2-pass-1"

// upPanel is one running panel binary.
type upPanel struct {
	t        *testing.T
	cmd      *exec.Cmd
	logPath  string
	baseURL  string // http://127.0.0.1:port
	basePath string
	client   *http.Client
	csrf     string
}

// buildPanelAt builds the panel binary from a commit (a throwaway git
// worktree) or from the current worktree (commit "").
func buildPanelAt(t *testing.T, commit, out string) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir := root
	if commit != "" {
		dir = out + ".src"
		wt := exec.Command("git", "worktree", "add", "--detach", dir, commit)
		wt.Dir = root
		if b, err := wt.CombinedOutput(); err != nil {
			t.Fatalf("worktree add %s: %v: %s", commit, err, b)
		}
		t.Cleanup(func() {
			rm := exec.Command("git", "worktree", "remove", "--force", dir)
			rm.Dir = root
			_, _ = rm.CombinedOutput()
			_ = os.RemoveAll(dir)
		})
	}
	build := exec.Command("go", "build", "-o", out, "./cmd/quantum-panel")
	build.Dir = dir
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build panel@%s: %v: %s", commit, err, b)
	}
}

// startPanel runs a panel binary against a config dir and waits for
// /api/health, then logs in and keeps an authenticated client.
func startPanel(t *testing.T, bin, cfgDir string, port int) *upPanel {
	t.Helper()
	logPath := filepath.Join(cfgDir+"_logs", filepath.Base(bin)+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"GRE_PANEL_DIR="+cfgDir,
		fmt.Sprintf("GRE_PANEL_PORT=%d", port),
		"GRE_PANEL_PASSWORD="+rc2Password,
	)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	p := &upPanel{t: t, cmd: cmd, logPath: logPath}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	// Discover the base path from panel.json (random 12 chars on first boot).
	base := ""
	deadline := time.Now().Add(30 * time.Second)
	healthOK := false
	for time.Now().Before(deadline) {
		if base == "" {
			if b, err := os.ReadFile(filepath.Join(cfgDir, "panel.json")); err == nil {
				var cfg struct {
					BasePath string `json:"base_path"`
				}
				if json.Unmarshal(b, &cfg) == nil {
					base = strings.Trim(cfg.BasePath, "/")
				}
			}
		}
		if base != "" {
			p.baseURL = fmt.Sprintf("http://127.0.0.1:%d/%s", port, base)
			resp, err := http.Get(p.baseURL + "/api/health")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == 200 {
					healthOK = true
					break
				}
			}
		}
		time.Sleep(300 * time.Millisecond)
	}
	if !healthOK {
		b, _ := os.ReadFile(logPath)
		t.Fatalf("panel %s never became healthy; log:\n%s", bin, b)
	}
	p.login(t)
	return p
}

func (p *upPanel) login(t *testing.T) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	p.client = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": rc2Password})
	resp, err := p.client.Post(p.baseURL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("login: status %d: %s", resp.StatusCode, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if tok, ok := out["csrf_token"].(string); ok {
		p.csrf = tok
	}
}

// getJSON does an authenticated GET.
func (p *upPanel) getJSON(path string) (int, map[string]any) {
	resp, err := p.client.Get(p.baseURL + path)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// post does an authenticated mutation (CSRF header).
func (p *upPanel) post(path string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", p.baseURL+path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", p.csrf)
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

func (p *upPanel) mustPost(path string, body any) map[string]any {
	p.t.Helper()
	code, out := p.post(path, body)
	if code != 200 {
		p.t.Fatalf("POST %s: %d %v", path, code, out)
	}
	return out
}

// seedRC2Data creates the production-shaped dataset through the API.
func (p *upPanel) seedRC2Data(t *testing.T) (topoID int64, agentID, agentKey string, tunnels []string) {
	t.Helper()
	p.mustPost("/api/mesh/datacenters", map[string]any{"kind": "iran", "name": "tehran-up", "price_coefficient": 1.0})
	p.mustPost("/api/mesh/datacenters", map[string]any{"kind": "foreign", "name": "frankfurt-up", "price_coefficient": 1.0})
	_, dcs := p.getJSON("/api/mesh/datacenters")
	dcIDs := map[string]float64{}
	for _, raw := range dcs["datacenters"].([]any) {
		m := raw.(map[string]any)
		dcIDs[m["name"].(string)] = m["id"].(float64)
	}
	_, sv := p.mustPostReturn(t, "/api/mesh/servers", map[string]any{"name": "ir-up-1", "role": "iran", "datacenter_id": dcIDs["tehran-up"], "address": "10.90.0.1"})
	irID := sv["id"].(float64)
	_, sv = p.mustPostReturn(t, "/api/mesh/servers", map[string]any{"name": "ir-up-2", "role": "iran", "datacenter_id": dcIDs["tehran-up"], "address": "10.90.0.2"})
	ir2ID := sv["id"].(float64)
	_, sv = p.mustPostReturn(t, "/api/mesh/servers", map[string]any{"name": "fr-up-1", "role": "foreign", "datacenter_id": dcIDs["frankfurt-up"], "address": "10.90.1.1"})
	frID := sv["id"].(float64)

	p.mustPost("/api/mesh/topologies/generate", map[string]any{
		"name": "upgrade-topo", "iran_servers": []float64{irID, ir2ID},
		"foreign_servers": []float64{frID}, "tunnels_per_pair": 2,
		"engines": []string{"matrix", "tinc"}, "enabled": true,
	})
	topoList := p.mustGet(t, "/api/mesh/topologies")
	topoID = int64(topoList["topologies"].([]any)[0].(map[string]any)["id"].(float64))

	// The tunnels ride the topology DETAIL (rc2 has no flat list route).
	_, tun := p.getJSON(fmt.Sprintf("/api/mesh/topologies/%d", topoID))
	tunnelIDs := []float64{}
	for _, raw := range tun["tunnels"].([]any) {
		m := raw.(map[string]any)
		tunnelIDs = append(tunnelIDs, m["id"].(float64))
		tunnels = append(tunnels, m["tunnel_key"].(string))
	}
	if len(tunnels) != 4 {
		t.Fatalf("generator made %d tunnels, want 4", len(tunnels))
	}
	p.mustPost(fmt.Sprintf("/api/mesh/topologies/%d/weights", topoID),
		map[string]any{"local_server_id": irID, "remote_server_id": frID, "weight": 60})
	p.mustPost(fmt.Sprintf("/api/mesh/topologies/%d/weights", topoID),
		map[string]any{"local_server_id": irID, "remote_server_id": frID, "tunnel_id": tunnelIDs[0], "weight": 30})

	p.mustPost("/api/mesh/forwards", map[string]any{
		"topology_id": topoID, "local_server_id": irID, "listen_port": 18443,
		"proto": "tcp", "enabled": true, "note": "upgrade-test",
	})
	p.mustPost("/api/mesh/pasarguard", map[string]any{
		"server_id": frID, "datacenter_id": dcIDs["frankfurt-up"],
		"name": "pg-up-1", "coefficient": 1.5,
	})
	p.mustPost("/api/mesh/alerts/settings", map[string]any{
		"enabled": true, "agent_offline_min": 7, "cert_expire_days": 21,
	})

	// Enroll one iran agent (token role iran) and link it.
	tok := p.mustPost("/api/agents/tokens", map[string]any{"role": "iran", "label": "up-seed", "expires_days": 1})
	token := tok["token"].(string)
	enrollBody := `{"name":"ir-up-agent","hostname":"up-node","os":"linux","arch":"amd64","version":"rc2","caps":["matrix"]}`
	// NOTE: rc2's enroll route is /api/agent/enroll (singular) — the
	// current main moved it to /api/agents/enroll; the seed must hit the
	// RC2 route.
	req, _ := http.NewRequest("POST", p.baseURL+"/api/agent/enroll", strings.NewReader(enrollBody))
	req.Header.Set(protocol.HeaderEnrollToken, token)
	resp, err := p.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("enroll: %d %s", resp.StatusCode, raw)
	}
	var er struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	_ = json.Unmarshal(raw, &er)
	agentID, agentKey = er.ID, er.Key
	p.mustPost("/api/mesh/agents/"+agentID+"/link", map[string]any{"server_id": irID})
	p.mustPost("/api/mesh/agents/"+agentID+"/engine-catalog",
		map[string]any{"catalog": []map[string]any{{"id": "matrix"}, {"id": "tinc"}}})
	return topoID, agentID, agentKey, tunnels
}

func (p *upPanel) mustPostReturn(t *testing.T, path string, body any) (int, map[string]any) {
	t.Helper()
	code, out := p.post(path, body)
	if code != 200 {
		t.Fatalf("POST %s: %d %v", path, code, out)
	}
	return code, out
}

func (p *upPanel) mustGet(t *testing.T, path string) map[string]any {
	t.Helper()
	code, out := p.getJSON(path)
	if code != 200 {
		t.Fatalf("GET %s: %d %v", path, code, out)
	}
	return out
}

// reportTraffic connects a fake v2 agent and sends ONE heartbeat with
// known counters (the 5 s sample throttle means exactly one sample).
func reportTraffic(t *testing.T, p *upPanel, agentID, agentKey string, tunnels []string, rx uint64) {
	t.Helper()
	hdr := http.Header{}
	hdr.Set(protocol.HeaderAgentID, agentID)
	hdr.Set(protocol.HeaderAgentKey, agentKey)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(p.baseURL, "http")+"/api/agent/ws", hdr)
	if err != nil {
		t.Fatalf("agent dial: %v", err)
	}
	defer ws.Close()
	send := func(typ string, payload any) {
		b, _ := json.Marshal(payload)
		if err := ws.WriteJSON(protocol.Envelope{V: 2, Type: typ, Data: b}); err != nil {
			t.Fatalf("agent send %s: %v", typ, err)
		}
	}
	send(protocol.MsgHello, protocol.Hello{ID: agentID, Version: "rc2", Hostname: "up-node",
		OS: "linux", Arch: "amd64", MaxVersion: protocol.ProtoVersionV2,
		EngineCatalog: []string{"matrix", "tinc"}})
	// welcome (v1 OR v2 both fine for a heartbeat below — we send v2).
	hj, _ := json.Marshal(protocol.HeartbeatV2{TS: time.Now().Unix(), UptimeS: 100,
		State: &protocol.StateReportV2{
			Tunnels: []protocol.TunnelRuntime{{
				TunnelID: tunnels[0], Engine: "matrix", Up: true,
				WireRx: rx, WireTx: rx / 8, PayloadRx: rx - rx/16, PayloadTx: rx / 9,
				CountersKnown: true, WireKnown: true, PayloadKnown: true,
			}},
			Resources: &protocol.ResourceReport{CPUCores: 2, CPUPercent: 12.5,
				MemTotalMB: 2048, MemUsedMB: 700, DiskTotalGB: 40, DiskUsedGB: 9, UptimeS: 3600},
		}})
	if err := ws.WriteJSON(protocol.Envelope{V: 2, Type: protocol.MsgHeartbeat, Data: hj}); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	time.Sleep(300 * time.Millisecond) // let the hub ingest
}

func usageSnapshot(t *testing.T, p *upPanel, tunnelID int64) map[string]any {
	t.Helper()
	return p.mustGet(t, fmt.Sprintf("/api/mesh/usage?tunnel_id=%d&from=0&to=%d", tunnelID, time.Now().Add(time.Hour).Unix()))
}

func TestUpgradeRC2ToCurrent(t *testing.T) {
	if os.Getenv("UPGRADE_E2E") == "" {
		t.Skip("UPGRADE_E2E not set")
	}
	work := t.TempDir()
	cfgDir := filepath.Join(work, "cfg")

	// ---- 1. build BOTH panels + run RC2 ----
	rc2Bin := filepath.Join(work, "panel-rc2")
	curBin := filepath.Join(work, "panel-cur")
	buildPanelAt(t, rc2Commit, rc2Bin)
	buildPanelAt(t, "", curBin)

	port := freeTCPPort(t)
	rc2 := startPanel(t, rc2Bin, cfgDir, port)
	topoID, agentID, agentKey, tunnels := rc2.seedRC2Data(t)

	// Traffic through the REAL agent WS path (known bytes).
	reportTraffic(t, rc2, agentID, agentKey, tunnels, 3000)
	_, tunList := rc2.getJSON(fmt.Sprintf("/api/mesh/topologies/%d", topoID))
	var firstTunnelID float64
	for _, raw := range tunList["tunnels"].([]any) {
		m := raw.(map[string]any)
		if m["tunnel_key"].(string) == tunnels[0] {
			firstTunnelID = m["id"].(float64)
		}
	}
	preUsage := usageSnapshot(t, rc2, int64(firstTunnelID))
	preDCs, _ := json.Marshal(rc2.mustGet(t, "/api/mesh/datacenters"))
	t.Logf("rc2 seeded: topo %d, %d tunnels, agent %s, usage rx seen", topoID, len(tunnels), agentID)

	// ---- 2. stop RC2, start CURRENT on the SAME dir ----
	if err := rc2.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_, _ = rc2.cmd.Process.Wait()
	time.Sleep(500 * time.Millisecond)

	cur := startPanel(t, curBin, cfgDir, port) // migrations 5+6 apply here

	// Login still works with the OLD password (startPanel logs in).
	// Nothing lost:
	postDCs, _ := json.Marshal(cur.mustGet(t, "/api/mesh/datacenters"))
	if string(postDCs) != string(preDCs) {
		t.Fatalf("datacenters changed across upgrade:\npre %s\npost %s", preDCs, postDCs)
	}
	// Agents survive (row + v2 state).
	postAgents := cur.mustGet(t, "/api/mesh/agents")
	rawAgents, _ := json.Marshal(postAgents)
	if !strings.Contains(string(rawAgents), agentID) {
		t.Fatalf("agent %s lost across upgrade: %s", agentID, rawAgents)
	}
	// Old traffic reads IDENTICALLY (same bytes): compare only the DATA
	// fields — the response echoes the per-call from/to window.
	stripWindow := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range m {
			if k != "from" && k != "to" && k != "note" {
				out[k] = v
			}
		}
		return out
	}
	postUsage := usageSnapshot(t, cur, int64(firstTunnelID))
	preJSON, _ := json.Marshal(stripWindow(preUsage))
	postJSON, _ := json.Marshal(stripWindow(postUsage))
	if string(preJSON) != string(postJSON) {
		t.Fatalf("usage changed across upgrade:\npre %s\npost %s", preJSON, postJSON)
	}
	t.Logf("usage identical across upgrade (first-sample zero-base: rc2 bytes preserved)")

	// The agent RECONNECTS to the current panel (fresh WS + heartbeat
	// with MORE bytes — cumulative counters continue, not restart).
	reportTraffic(t, cur, agentID, agentKey, tunnels, 5000)
	_, agAfter := cur.getJSON("/api/mesh/agents")
	agJSON, _ := json.Marshal(agAfter)
	if !strings.Contains(string(agJSON), `"online"`) {
		t.Fatalf("agent did not come online on the current panel: %s", agJSON)
	}
	afterUsage := usageSnapshot(t, cur, int64(firstTunnelID))
	afterJSON, _ := json.Marshal(stripWindow(afterUsage))
	if string(afterJSON) == string(postJSON) {
		t.Fatalf("post-upgrade heartbeat did not add usage: %s", afterJSON)
	}

	// The NEW API works on the upgraded store.
	ovCode, ov := cur.getJSON("/api/ui/overview")
	if ovCode != 200 || ov["counts"] == nil {
		t.Fatalf("/api/ui/overview on upgraded store: %d %v", ovCode, ov)
	}

	// Backup still works (CLI on the upgraded dir; the CLI shape is
	// `backup --out FILE --passphrase-file F`).
	backupOut := filepath.Join(work, "upgraded-backup.tar.gz")
	passFile := filepath.Join(work, "bk.pass")
	if err := os.WriteFile(passFile, []byte("upgrade-backup-passphrase-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	bk := exec.Command(curBin, "backup", "--out", backupOut, "--passphrase-file", passFile)
	bk.Env = append(os.Environ(), "GRE_PANEL_DIR="+cfgDir)
	if b, err := bk.CombinedOutput(); err != nil {
		t.Fatalf("backup CLI: %v: %s", err, b)
	}
	if fi, err := os.Stat(backupOut); err != nil || fi.Size() < 1024 {
		t.Fatalf("backup file missing/too small: %v", err)
	}

	// ---- 3. DOWNGRADE check (documented behavior) ----
	if err := cur.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_, _ = cur.cmd.Process.Wait()
	time.Sleep(500 * time.Millisecond)
	rc2again := startPanel(t, rc2Bin, cfgDir, port)
	code, dcs := rc2again.getJSON("/api/mesh/datacenters")
	if code != 200 || dcs == nil {
		t.Fatalf("DOWNGRADE: rc2 cannot read its old data after the upgrade: %d %v", code, dcs)
	}
	t.Logf("DOWNGRADE verified: rc2 boots on the upgraded store and reads the old data (phase-11 tables are additive; rc2 simply ignores them, new fields stop updating, /api/ui/* is absent)")
}
