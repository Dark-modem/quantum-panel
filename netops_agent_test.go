package main

// Agent-only acceptance tests for the Network Optimization subsystem: every
// operation must work on a lightweight endpoint that has ONLY agent.json
// (no panel.json, none created), with role detection from files on disk,
// privilege validation on mutating ops, and Conservative behavior when
// hardware facts are unavailable.

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// netAgentOnlyFixture: an enrolled lightweight agent — agent.json present,
// panel.json absent. Hardware: the Iran-like 1 vCPU / ~956 MB shape.
func netAgentOnlyFixture(t *testing.T) (map[string]string, map[string][]string, map[string]string) {
	t.Helper()
	withTempConfig(t)
	files, dirs, exec := netIranLikeFixture()
	// The enrolled agent identity is the ONLY installation marker: write it
	// to the real temp configDir (netInstallRoleOf stats the real FS).
	if err := os.WriteFile(agentIdentityPath(), []byte(`{"id":"agt_testagent001","controller":"wss://ctrl.example.test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	oldPriv := netPrivileged
	netPrivileged = func() bool { return true }
	t.Cleanup(func() { netPrivileged = oldPriv })
	return files, dirs, exec
}

// netAgentApplyFixture adds the apply machinery on top of the agent fixture.
func netAgentApplyFixture(t *testing.T) {
	t.Helper()
	files, dirs, exec := netAgentOnlyFixture(t)
	netApplyStubEnv(t, files, dirs, exec)
}

// TestNetopsAgentOnlyAllOpsNoFakePanel runs the six operations on an
// agent-only endpoint and asserts no panel.json is ever created.
func TestNetopsAgentOnlyAllOpsNoFakePanel(t *testing.T) {
	netAgentApplyFixture(t)

	// role detection
	if got := netInstallRoleOf(); got != netInstallAgent {
		t.Fatalf("install role = %q, want agent", got)
	}
	if got := netRoleFor(netRoleAuto); got != netRoleAgent {
		t.Fatalf("auto role = %q, want agent", got)
	}

	// detect
	d, err := NetopDetect()
	if err != nil || d.Role != "agent" || d.Report == nil {
		t.Fatalf("detect: %v %+v", err, d)
	}
	// plan: agent role, Conservative (956 MB)
	p, err := NetopPlan(netRoleAuto, "")
	if err != nil || p.Profile != string(netProfileConservative) || p.Role != "agent" {
		t.Fatalf("plan: %v profile=%s role=%s", err, p.Profile, p.Role)
	}
	// apply
	a, err := NetopApply(netRoleAuto, "", "", nil)
	if err != nil || !a.Ok || a.Role != "agent" || a.Journal == nil {
		t.Fatalf("apply: %v %+v", err, a)
	}
	// status
	s, err := NetopStatus()
	if err != nil || !s.HasJournal || s.Role != "agent" {
		t.Fatalf("status: %v %+v", err, s)
	}
	if len(s.Drift) != 0 {
		t.Fatalf("no drift expected right after apply: %+v", s.Drift)
	}
	// rollback
	r, err := NetopRollback()
	if err != nil || !r.Ok {
		t.Fatalf("rollback: %v %+v", err, r)
	}
	// revert (idempotent finisher; restores owned files)
	if _, err := NetopRevert(); err != nil {
		t.Fatalf("revert: %v", err)
	}

	// THE contract from Session 6's QA: no fake panel.json may exist.
	if _, err := os.Stat(cfgPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("panel.json must not be created on an agent-only endpoint (err=%v)", err)
	}
}

// TestNetopsUninstalledBoxReadOnly pins the "none" role behavior: read-only
// ops work (installer pre-flight), mutating ops are refused.
func TestNetopsUninstalledBoxReadOnly(t *testing.T) {
	withTempConfig(t)
	files, dirs, exec := netIfaceFixtures()
	netTestEnv(t, files, dirs, exec)
	oldPriv := netPrivileged
	netPrivileged = func() bool { return true }
	t.Cleanup(func() { netPrivileged = oldPriv })

	if got := netInstallRoleOf(); got != netInstallNone {
		t.Fatalf("install role = %q, want none", got)
	}
	if _, err := NetopDetect(); err != nil {
		t.Fatalf("detect on uninstalled box must work: %v", err)
	}
	if _, err := NetopPlan(netRoleAuto, ""); err != nil {
		t.Fatalf("plan on uninstalled box must work: %v", err)
	}
	if _, err := NetopStatus(); err != nil {
		t.Fatalf("status on uninstalled box must work: %v", err)
	}
	if _, err := NetopApply(netRoleAuto, "", "", nil); !errors.Is(err, netErrNoInstallation) {
		t.Fatalf("apply on uninstalled box must be refused, got %v", err)
	}
	if _, err := NetopRollback(); !errors.Is(err, netErrNoInstallation) {
		t.Fatalf("rollback on uninstalled box must be refused, got %v", err)
	}
	if _, err := NetopRevert(); !errors.Is(err, netErrNoInstallation) {
		t.Fatalf("revert on uninstalled box must be refused, got %v", err)
	}
}

// TestNetopsPrivilegeValidation pins the root requirement on mutating ops
// while read-only ops stay unprivileged. Scenario: enrolled agent, operator
// WITHOUT root — the installation identity must be present so the privilege
// refusal (not the installation refusal) is what the test observes.
func TestNetopsPrivilegeValidation(t *testing.T) {
	files, dirs, exec := netAgentOnlyFixture(t)
	netTestEnv(t, files, dirs, exec)
	old := netPrivileged
	netPrivileged = func() bool { return false } // simulate non-root
	t.Cleanup(func() { netPrivileged = old })

	if _, err := NetopDetect(); err != nil {
		t.Fatalf("detect must not require root: %v", err)
	}
	if _, err := NetopStatus(); err != nil {
		t.Fatalf("status must not require root: %v", err)
	}
	if _, err := NetopPlan(netRoleAuto, ""); err != nil {
		t.Fatalf("plan must not require root: %v", err)
	}
	for name, call := range map[string]func() (*NetopApplyResult, error){
		"apply":    func() (*NetopApplyResult, error) { return NetopApply(netRoleAuto, "", "", nil) },
		"rollback": func() (*NetopApplyResult, error) { return NetopRollback() },
		"revert":   func() (*NetopApplyResult, error) { return NetopRevert() },
	} {
		if _, err := call(); !errors.Is(err, netErrPrivilege) {
			t.Fatalf("%s without root must be refused with netErrPrivilege, got %v", name, err)
		}
	}
}

// TestNetopsAgentDetectionGapsStayConservative: unavailable hardware facts
// on an agent must cause Conservative, never guessed tuning.
func TestNetopsAgentDetectionGapsStayConservative(t *testing.T) {
	files, dirs, exec := netAgentOnlyFixture(t)
	delete(files, "/proc/cpuinfo")
	delete(exec, "uname")
	delete(exec, "systemd-detect-virt")
	netTestEnv(t, files, dirs, exec)

	p, err := NetopPlan(netRoleAuto, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Profile != string(netProfileConservative) {
		t.Fatalf("agent with detection gaps selected %s, want conservative", p.Profile)
	}
	for _, c := range p.Changes {
		if c.Apply && (strings.Contains(c.Key, "rmem") || strings.Contains(c.Key, "conntrack")) {
			t.Fatalf("guessed tuning proposed despite gaps: %s", c.Key)
		}
	}
	if len(p.Registry.Warnings) == 0 {
		t.Fatal("gaps must be recorded as warnings")
	}
}

// TestNetopsAgentLowResourcePlanShape pins the exact Conservative apply set
// through the operation layer (the JSON Session 3 will transport).
func TestNetopsAgentLowResourcePlanShape(t *testing.T) {
	files, dirs, exec := netAgentOnlyFixture(t)
	netTestEnv(t, files, dirs, exec)
	p, err := NetopPlan(netRoleAuto, "")
	if err != nil {
		t.Fatal(err)
	}
	var applied []string
	for _, c := range p.Changes {
		if c.Apply {
			applied = append(applied, c.Key)
		}
	}
	want := []string{
		"net.core.somaxconn",
		"net.ipv4.tcp_max_syn_backlog",
		"net.ipv4.tcp_mtu_probing",
		"vm.swappiness",
	}
	if strings.Join(applied, ",") != strings.Join(want, ",") {
		t.Fatalf("agent low-resource plan = %v, want %v", applied, want)
	}
	// JSON stability: the result must marshal cleanly with the documented keys
	b, err := json.Marshal(p)
	if err != nil || !strings.Contains(string(b), `"profile":"conservative"`) ||
		!strings.Contains(string(b), `"role":"agent"`) {
		t.Fatalf("plan JSON contract broken: %s", string(b)[:min(len(b), 200)])
	}
}

// TestNetopsJournalUnderAgentConfigDir proves the journal/baseline live
// under the agent's configDir (not /tmp) across operations.
func TestNetopsJournalUnderAgentConfigDir(t *testing.T) {
	netAgentApplyFixture(t)
	if _, err := NetopApply(netRoleAuto, "", "", nil); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{netJournalPath(), netBaselinePath()} {
		if !strings.HasPrefix(f, configDir) {
			t.Fatalf("journal path %s is not under configDir %s", f, configDir)
		}
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("journal file missing: %v", err)
		}
	}
	// and it must be next to agent.json, in the agent's own config dir
	if filepath.Dir(netJournalPath()) != filepath.Join(configDir, "netopt") {
		t.Fatalf("journal dir = %s", filepath.Dir(netJournalPath()))
	}
}

// TestNetopsDispatcherAllOps runs every operation through the single
// NetopDispatch entry point Session 3's agent dispatcher will call, on an
// agent-only installation, and verifies the result types and the no-fake-
// panel.json contract end to end.
func TestNetopsDispatcherAllOps(t *testing.T) {
	netAgentApplyFixture(t)

	seq := []struct {
		op    string
		check func(any) error
	}{
		{op: "detect", check: func(v any) error {
			r, ok := v.(*NetopDetectResult)
			if !ok || r.Role != "agent" || r.Report == nil {
				return errors.New("bad detect result")
			}
			return nil
		}},
		{op: "plan", check: func(v any) error {
			r, ok := v.(*NetopPlanResult)
			if !ok || r.Profile != string(netProfileConservative) || r.Role != "agent" {
				return errors.New("bad plan result")
			}
			return nil
		}},
		{op: "apply", check: func(v any) error {
			r, ok := v.(*NetopApplyResult)
			if !ok || !r.Ok || r.Op != "apply" || r.Journal == nil || !r.Journal.Verified {
				return errors.New("bad apply result")
			}
			return nil
		}},
		{op: "status", check: func(v any) error {
			r, ok := v.(*NetopStatusResult)
			if !ok || !r.HasJournal || r.Role != "agent" {
				return errors.New("bad status result")
			}
			return nil
		}},
		{op: "rollback", check: func(v any) error {
			r, ok := v.(*NetopApplyResult)
			if !ok || !r.Ok || r.Op != "rollback" {
				return errors.New("bad rollback result")
			}
			return nil
		}},
		{op: "revert", check: func(v any) error {
			r, ok := v.(*NetopApplyResult)
			if !ok || !r.Ok || r.Op != "revert" {
				return errors.New("bad revert result")
			}
			return nil
		}},
	}
	for _, step := range seq {
		res, err := NetopDispatch(NetopRequest{Op: step.op, Role: "auto"})
		if err != nil {
			t.Fatalf("dispatch %s: %v", step.op, err)
		}
		if err := step.check(res); err != nil {
			t.Fatalf("dispatch %s: %v", step.op, err)
		}
		// the transport contract: every result must marshal with its shape
		if b, merr := json.Marshal(res); merr != nil || len(b) < 2 {
			t.Fatalf("dispatch %s: result not marshalable (%v)", step.op, merr)
		}
	}
	if _, err := os.Stat(cfgPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dispatcher must not create panel.json (err=%v)", err)
	}
}

// TestNetopsDispatcherGuards pins the dispatcher's refusals: unknown op,
// privilege, and performance-justify requirements.
func TestNetopsDispatcherGuards(t *testing.T) {
	netAgentApplyFixture(t)
	if _, err := NetopDispatch(NetopRequest{Op: "tune"}); err == nil ||
		!strings.Contains(err.Error(), "unknown op") {
		t.Fatalf("unknown op must be rejected, got %v", err)
	}
	old := netPrivileged
	netPrivileged = func() bool { return false }
	t.Cleanup(func() { netPrivileged = old })
	if _, err := NetopDispatch(NetopRequest{Op: "apply"}); !errors.Is(err, netErrPrivilege) {
		t.Fatalf("dispatch apply without root must refuse, got %v", err)
	}
	netPrivileged = func() bool { return true }
	if _, err := NetopDispatch(NetopRequest{Op: "apply", Profile: "performance"}); err == nil ||
		!strings.Contains(err.Error(), "--justify") {
		t.Fatalf("performance without justify must be refused, got %v", err)
	}
}

// TestNetopsStatusNoFalseDriftFromFormatting pins the whitespace fix: a
// vector sysctl the kernel reports with tabs must not read as drift against
// the journal's space-separated form.
func TestNetopsStatusNoFalseDriftFromFormatting(t *testing.T) {
	files, dirs, exec := netAgentOnlyFixture(t)
	// The kernel reports vector values with TABS; the journal (written by the
	// profile) stores the space-separated form. Same values, same setting.
	files["/proc/sys/net/ipv4/tcp_rmem"] = "4096\t131072\t16777216\n"
	netTestEnv(t, files, dirs, exec)

	j := &netJournal{
		AppliedAt: time.Now().Unix(), Profile: "balanced", Role: "agent",
		Applied: []netAppliedSetting{
			{Key: "net.ipv4.tcp_rmem", Kind: "sysctl", Before: "4096 65536 4194304", After: "4096 131072 16777216"},
		},
		OwnedFiles: []string{"/etc/sysctl.d/99-quantum-netopt.conf"},
	}
	b, _ := json.MarshalIndent(j, "", "  ")
	if err := os.MkdirAll(netOptDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(netJournalPath(), b, 0600); err != nil {
		t.Fatal(err)
	}
	res, err := NetopStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drift) != 0 {
		t.Fatalf("formatting-only difference must not be drift: %+v", res.Drift)
	}
	// and a genuinely different live value IS drift
	files["/proc/sys/net/ipv4/tcp_rmem"] = "4096 131072 4194304\n"
	res, err = NetopStatus()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Drift) != 1 || res.Drift[0].Key != "net.ipv4.tcp_rmem" {
		t.Fatalf("real drift not detected: %+v", res.Drift)
	}
}
