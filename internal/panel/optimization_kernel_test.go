package panel

// optimization_kernel_test.go — BACKEND-42 hermetic tests for the
// kernel-tuning READ surface (§2.23.2): the netopt.detect command never
// leaves the test (agentHubCommandFn seam), the mapping is checked field by
// field against a hand-built report and the honest declarations
// (tcp_retries2 always null, the profile's AGENT-31 flag) are asserted.

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// kernelTestReset clears the kernel cache/seam state (global across tests).
func kernelTestReset(t *testing.T) {
	t.Helper()
	kernelMu.Lock()
	kernelCache = map[string]*kernelDiagnostics{}
	kernelRunning = map[string]bool{}
	kernelMu.Unlock()
}

// kernelNetReportFixture is the documented netopt.detect report shape the
// agent answers (netops_api.go NetopDetectResult{role, report}).
const kernelNetReportFixture = `{"role":"iran","report":{"cc":"cubic","cc_available":["cubic","bbr"],
"default_qdisc":"fq","sysctls":{"net.ipv4.tcp_mtu_probing":"1","net.core.rmem_max":"16777216",
"net.core.wmem_max":"4194304","net.ipv4.tcp_rmem":"4096 131072 6291456","net.ipv4.tcp_wmem":"4096 16384 4194304"},
"ifaces":[{"name":"eth0","primary":true,"offloads":{"generic-segmentation-offload":"on","generic-receive-offload":"off"}}]}}`

func kernelFakeCmd(t *testing.T, output string, err error) {
	t.Helper()
	old := agentHubCommandFn.get()
	agentHubCommandFn.set(func(id, op string, params map[string]string, timeout time.Duration) (agentCommandResult, error) {
		if err != nil {
			return agentCommandResult{}, err
		}
		return agentCommandResult{OK: true, Output: output}, nil
	})
	t.Cleanup(func() { agentHubCommandFn.set(old) })
}

func TestKernelProfileDeclaration(t *testing.T) {
	p := kernelProfile()
	if p["id"] != "kernel-net-tuning" || p["family"] != "hardening" || p["status"] != "needs-agent-fields" {
		t.Fatalf("profile identity: %v", p)
	}
	if p["flag"] != "AGENT-31" || p["apply"] != "later-task" {
		t.Fatalf("profile declaration: %v", p)
	}
	missing, _ := p["missing_fields"].([]string)
	if len(missing) != 2 {
		t.Fatalf("missing_fields: %v", p["missing_fields"])
	}
}

func TestKernelDetectOneMapsTheReport(t *testing.T) {
	kernelTestReset(t)
	kernelFakeCmd(t, kernelNetReportFixture, nil)
	d := kernelDetectOne("agt_1a2b3c4d5e6f", "iran-a", true)
	if d.Status != "ok" || d.Error != "" {
		t.Fatalf("status %q err %q", d.Status, d.Error)
	}
	if d.CongestionControl != "cubic" || d.BBRAvailable != true || d.Qdisc != "fq" {
		t.Fatalf("cc/bbr/qdisc: %v %v %v", d.CongestionControl, d.BBRAvailable, d.Qdisc)
	}
	if d.TCPMTUProbing != 1 || d.RmemMax != 16777216 || d.WmemMax != 4194304 {
		t.Fatalf("sysctl ints: %v %v %v", d.TCPMTUProbing, d.RmemMax, d.WmemMax)
	}
	if d.TCPRmem != "4096 131072 6291456" || d.TCPWmem != "4096 16384 4194304" {
		t.Fatalf("tcp rmem/wmem: %v %v", d.TCPRmem, d.TCPWmem)
	}
	if d.GSO != "on" || d.GRO != "off" {
		t.Fatalf("primary-iface offloads: %v %v", d.GSO, d.GRO)
	}
	// the honest hole: tcp_retries2 is ALWAYS null (AGENT-31), never guessed
	if d.TCPRetries2 != nil {
		t.Fatalf("tcp_retries2 must stay null: %v", d.TCPRetries2)
	}
}

func TestKernelDetectOneHonestFailures(t *testing.T) {
	kernelTestReset(t)
	// offline → status offline, no command attempted
	d := kernelDetectOne("agt_1a2b3c4d5e6f", "iran-a", false)
	if d.Status != "offline" || d.Error == "" {
		t.Fatalf("offline: %+v", d)
	}
	// command failure → honest error, nulls
	kernelFakeCmd(t, "", errors.New("test command failure"))
	d = kernelDetectOne("agt_1a2b3c4d5e6f", "iran-a", true)
	if d.Status != "error" || d.Error == "" || d.CongestionControl != nil || d.TCPRetries2 != nil {
		t.Fatalf("command failure: %+v", d)
	}
	// refused (OK=false) → honest error
	kernelFakeCmd(t, "", nil)
	old := agentHubCommandFn.get()
	agentHubCommandFn.set(func(id, op string, params map[string]string, timeout time.Duration) (agentCommandResult, error) {
		return agentCommandResult{OK: false, Error: "refused by test"}, nil
	})
	t.Cleanup(func() { agentHubCommandFn.set(old) })
	d = kernelDetectOne("agt_1a2b3c4d5e6f", "iran-a", true)
	if d.Status != "error" || !strings.Contains(d.Error, "refused") {
		t.Fatalf("refused: %+v", d)
	}
	// wrong output shape → honest error, never invented fields
	kernelFakeCmd(t, `{"unexpected":true}`, nil)
	d = kernelDetectOne("agt_1a2b3c4d5e6f", "iran-a", true)
	if d.Status != "error" || d.Qdisc != nil {
		t.Fatalf("wrong shape: %+v", d)
	}
}

func TestOptimizationKernelHandler(t *testing.T) {
	withTempConfig(t)
	s := withMeshStore(t)
	kernelTestReset(t)
	if err := s.UpsertAgent(store.Agent{ID: "agt_77aa88bb99cc", Name: "k-a", Status: "online"}); err != nil {
		t.Fatal(err)
	}
	// uiAgentOnline = status online + a fresh runtime report: seed the
	// runtime view so the read path reaches the (seamed) detect command
	meshRuntimeMu.Lock()
	meshRuntimeSeen["agt_77aa88bb99cc"] = time.Now().Unix()
	meshRuntimeMu.Unlock()
	kernelFakeCmd(t, kernelNetReportFixture, nil)

	// hostile input: a bad agent id is E-IN-06 (never reaches the store)
	r := httptest.NewRequest("GET", "/api/optimization/kernel?agent=../../etc/passwd", nil)
	w := httptest.NewRecorder()
	handleOptimizationKernel(w, r)
	if !strings.Contains(w.Body.String(), "E-IN-06") {
		t.Fatalf("bad agent id: %s", w.Body.String())
	}

	// the single-agent read probes through the seam and answers the mapped row
	r = httptest.NewRequest("GET", "/api/optimization/kernel?agent=agt_77aa88bb99cc", nil)
	w = httptest.NewRecorder()
	handleOptimizationKernel(w, r)
	if w.Code != 200 {
		t.Fatalf("agent read: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Profile map[string]any       `json:"profile"`
		Agents  []*kernelDiagnostics `json:"agents"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v — %s", err, w.Body.String())
	}
	if resp.Profile["flag"] != "AGENT-31" {
		t.Fatalf("profile flag: %v", resp.Profile["flag"])
	}
	if len(resp.Agents) != 1 || resp.Agents[0].Status != "ok" {
		t.Fatalf("agents: %+v", resp.Agents)
	}
	if resp.Agents[0].BBRAvailable != true || resp.Agents[0].TCPRetries2 != nil {
		t.Fatalf("row mapping: %+v", resp.Agents[0])
	}

	// run=1 refreshes single-flight and answers immediately (cache rows)
	r = httptest.NewRequest("GET", "/api/optimization/kernel?agent=agt_77aa88bb99cc&run=1", nil)
	w = httptest.NewRecorder()
	handleOptimizationKernel(w, r)
	if w.Code != 200 {
		t.Fatalf("run read: %d %s", w.Code, w.Body.String())
	}

	// the all-agents read answers every enrolled agent from cache
	// (never-probed agents are honest pending rows)
	if err := s.UpsertAgent(store.Agent{ID: "agt_00ff11ee22dd", Name: "k-b"}); err != nil {
		t.Fatal(err)
	}
	r = httptest.NewRequest("GET", "/api/optimization/kernel", nil)
	w = httptest.NewRecorder()
	handleOptimizationKernel(w, r)
	if w.Code != 200 {
		t.Fatalf("all read: %d %s", w.Code, w.Body.String())
	}
	resp = struct {
		Profile map[string]any       `json:"profile"`
		Agents  []*kernelDiagnostics `json:"agents"`
	}{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode all: %v", err)
	}
	if len(resp.Agents) != 2 {
		t.Fatalf("all agents: %d rows", len(resp.Agents))
	}
	byID := map[string]*kernelDiagnostics{}
	for _, a := range resp.Agents {
		byID[a.Agent] = a
	}
	if byID["agt_77aa88bb99cc"].Status != "ok" {
		t.Fatalf("cached row: %+v", byID["agt_77aa88bb99cc"])
	}
	if byID["agt_00ff11ee22dd"].Status != "pending" {
		t.Fatalf("never-probed row must be pending: %+v", byID["agt_00ff11ee22dd"])
	}
	if byID["agt_00ff11ee22dd"].TCPRetries2 != nil {
		t.Fatalf("pending row tcp_retries2 must stay null: %+v", byID["agt_00ff11ee22dd"])
	}
}
