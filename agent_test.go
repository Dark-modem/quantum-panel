package main

// Unit tests for the agent runtime: revision rules, desired-state
// application (hub/node), the command allowlist (injection-safe), lifecycle
// independence from the Matrix units, health/traffic parsing, node
// detection, TLS pinning, enrollment and the status CLI.
// The runtime/protocol tests live in agent_run_test.go.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// agentLab is matrixInstanceLab (temp configDir/unitDir, stubbed tinc and
// exec funnel) plus the agent-specific seams: no real docker and no real
// PasarGuard directories.
func agentLab(t *testing.T) {
	t.Helper()
	matrixInstanceLab(t)
	oldPS, oldDirs := agentDockerPS, agentPasarguardDirs
	agentDockerPS = func() (string, error) { return "", errors.New("agent lab: no docker") }
	agentPasarguardDirs = []string{filepath.Join(t.TempDir(), "absent")}
	t.Cleanup(func() { agentDockerPS, agentPasarguardDirs = oldPS, oldDirs })
}

// agentExecRecorder wraps matrixExec, counting and recording every call so
// tests can assert "no work happened" and inspect exact command lines. With
// a nil inner the PREVIOUS (lab) stub still handles the call — replacing it
// with a "always succeed" stub would hang matrixDNATClear's delete loop.
type agentExecRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *agentExecRecorder) wrap(t *testing.T, inner func(name string, args ...string) (string, error)) {
	t.Helper()
	r.mu.Lock()
	r.calls = nil
	r.mu.Unlock()
	old := matrixExec
	matrixExec = func(name string, args ...string) (string, error) {
		r.mu.Lock()
		r.calls = append(r.calls, name+" "+strings.Join(args, " "))
		r.mu.Unlock()
		if inner != nil {
			return inner(name, args...)
		}
		return old(name, args...)
	}
	t.Cleanup(func() { matrixExec = old })
}

func (r *agentExecRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (r *agentExecRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

// touchesUnit reports whether any recorded call mutates (not just reads) the
// named systemd unit.
func (r *agentExecRecorder) touchesUnit(unit string) bool {
	mutating := map[string]bool{"stop": true, "start": true, "restart": true, "disable": true,
		"enable": true, "mask": true, "kill": true, "try-restart": true, "reload-or-restart": true}
	for _, c := range r.all() {
		f := strings.Fields(c)
		if len(f) == 0 || f[0] != "systemctl" {
			continue
		}
		for _, a := range f[1:] {
			if mutating[a] && strings.Contains(c, unit) {
				return true
			}
		}
	}
	return false
}

func (r *agentExecRecorder) mentions(unit string) bool {
	for _, c := range r.all() {
		if strings.Contains(c, unit) {
			return true
		}
	}
	return false
}

// agentHubDesired builds a hub apply desired for the legacy instance.
func agentHubDesired(rev string, port int, ports []int) *agentDesired {
	return &agentDesired{Revision: rev, Matrix: &agentDesiredMatrix{
		Instance: "matrix", Action: "apply",
		Hub: &agentDesiredHub{Family: "ipv4", Port: port, Addr: "185.226.119.74", Ports: ports, Token: strings.Repeat("a", 48)},
	}}
}

// agentTestBundle builds a valid hmx1_ node pairing bundle for in.
func agentTestBundle(t *testing.T, in matrixInstance, meshPort, userPort int, revision string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}))
	payload := matrixBundlePayload{
		Panel:    "http://198.51.100.7:8080/panel",
		Token:    strings.Repeat("c", 48),
		Family:   "ipv4",
		Addr:     "185.226.119.75",
		Port:     meshPort,
		FIP:      in.firstPeer(),
		Ports:    []int{userPort},
		MainHost: pub,
		Network:  in.network(),
		Revision: revision,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return matrixBundlePfx + base64.StdEncoding.EncodeToString(b)
}

// TestAgentDesiredRevisionRules covers the required revision semantics:
// newer → applied, equal → idempotent ok ack with zero work, older →
// "stale revision rejected" with no state change.
func TestAgentDesiredRevisionRules(t *testing.T) {
	agentLab(t)
	var rec agentExecRecorder
	rec.wrap(t, nil)
	port, userPort := freeUDPPort(t), freeUDPPort(t)

	// newer → applied
	ack := agentApplyDesired(nil, agentHubDesired("100", port, []int{userPort}))
	if !ack.OK || ack.Detail["main_host"] == "" {
		t.Fatalf("newer revision ack = %+v", ack)
	}
	agentRecordAck("100", ack)
	if st := agentLoadState(); st.AppliedRevision != "100" || st.DesiredStatus != "applied" {
		t.Fatalf("state after apply = applied:%q status:%q", st.AppliedRevision, st.DesiredStatus)
	}
	if !loadMatrixInstance(matrixLegacyInstance()).Enabled {
		t.Fatal("hub instance not enabled after apply")
	}
	if rec.count() == 0 {
		t.Fatal("apply did no work at all")
	}

	// equal → idempotent ok ack, zero exec work
	before := rec.count()
	ack = agentApplyDesired(nil, agentHubDesired("100", port, []int{userPort}))
	if !ack.OK {
		t.Fatalf("equal revision must re-ack ok: %+v", ack)
	}
	if rec.count() != before {
		t.Fatalf("equal revision did work: %d extra exec calls", rec.count()-before)
	}

	// older → rejected, applied unchanged, zero work
	before = rec.count()
	ack = agentApplyDesired(nil, agentHubDesired("99", port, []int{userPort}))
	if ack.OK || ack.Error != "stale revision rejected" {
		t.Fatalf("older revision ack = %+v", ack)
	}
	if rec.count() != before {
		t.Fatal("stale revision did work")
	}
	if st := agentLoadState(); st.AppliedRevision != "100" {
		t.Fatalf("stale rejection changed applied revision to %q", st.AppliedRevision)
	}

	// newer again → accepted (same config, only the bundle revision syncs)
	ack = agentApplyDesired(nil, agentHubDesired("101", port, []int{userPort}))
	if !ack.OK || ack.Detail["main_host"] == "" {
		t.Fatalf("newer revision ack = %+v", ack)
	}
	agentRecordAck("101", ack)
	if st := agentLoadState(); st.AppliedRevision != "101" {
		t.Fatalf("applied revision = %q", st.AppliedRevision)
	}
	if ms := loadMatrixInstance(matrixLegacyInstance()); ms.Revision != "101" {
		t.Fatalf("matrix state revision = %q, want 101 (node register validation depends on it)", ms.Revision)
	}
}

// TestAgentHubApply verifies the matrixSetup path runs and the ack detail
// carries the hub's hosts/main content for the controller's node bundle.
func TestAgentHubApply(t *testing.T) {
	agentLab(t)
	port, userPort := freeUDPPort(t), freeUDPPort(t)
	ack := agentApplyDesired(nil, agentHubDesired("10", port, []int{userPort}))
	if !ack.OK {
		t.Fatalf("hub apply failed: %+v", ack)
	}
	main, ok := ack.Detail["main_host"]
	if !ok || !strings.Contains(main, "-----BEGIN RSA PUBLIC KEY-----") {
		t.Fatalf("main_host detail missing or not a tinc host file: %q", main)
	}
	legacy := matrixLegacyInstance()
	if _, err := os.Stat(matrixUnitPathOf(legacy)); err != nil {
		t.Fatalf("unit file not written: %v", err)
	}
	st := loadMatrixInstance(legacy)
	if !st.Enabled || st.Role != "hub" || st.Token != strings.Repeat("a", 48) || st.Port != port || st.Revision != "10" {
		t.Fatalf("matrix state = %+v", st)
	}
	if _, err := os.Stat(filepath.Join(matrixDirOf(legacy), "hosts", "main")); err != nil {
		t.Fatalf("hosts/main missing: %v", err)
	}
}

// TestAgentHubApplyDriftRebuild verifies a changed hub configuration is
// repaired by remove + fresh setup, not silently ignored.
func TestAgentHubApplyDriftRebuild(t *testing.T) {
	agentLab(t)
	port1, port2, userPort := freeUDPPort(t), freeUDPPort(t), freeUDPPort(t)
	if ack := agentApplyDesired(nil, agentHubDesired("10", port1, []int{userPort})); !ack.OK {
		t.Fatalf("first apply: %+v", ack)
	}
	var rec agentExecRecorder
	rec.wrap(t, nil)
	ack := agentApplyDesired(nil, agentHubDesired("11", port2, []int{userPort}))
	if !ack.OK {
		t.Fatalf("drift apply: %+v", ack)
	}
	if st := loadMatrixInstance(matrixLegacyInstance()); st.Port != port2 {
		t.Fatalf("drift repair did not rebuild: port %d", st.Port)
	}
	if !rec.touchesUnit("quantum-matrix.service") {
		t.Fatalf("expected stop/restart traffic during rebuild, got %v", rec.all())
	}
}

// TestAgentNodeApply covers the node bundle flow: provision, register
// message over the link, register_result ok → ok ack.
func TestAgentNodeApply(t *testing.T) {
	agentLab(t)
	in := matrixInstance{ID: "matrix2"}
	bundle := agentTestBundle(t, in, freeUDPPort(t), freeUDPPort(t), "77")
	link := &fakeAgentLink{results: map[string]json.RawMessage{
		agentMsgRegisterResult: []byte(`{"ok":true,"main_host":"HUBMAIN"}`),
	}}
	ack := agentApplyDesired(link, &agentDesired{Revision: "77", Matrix: &agentDesiredMatrix{
		Instance: in.ID, Action: "apply", Bundle: bundle,
	}})
	if !ack.OK || ack.Detail["hub_main_host"] != "HUBMAIN" {
		t.Fatalf("node apply ack = %+v", ack)
	}
	regs := link.sentOf(agentMsgRegister)
	if len(regs) != 1 {
		t.Fatalf("expected exactly one register message, got %d", len(regs))
	}
	var env agentEnvelope
	if err := json.Unmarshal(regs[0], &env); err != nil || env.Type != agentMsgRegister {
		t.Fatalf("register envelope = %s (%v)", regs[0], err)
	}
	var reg agentRegisterMsg
	if err := json.Unmarshal(env.Data, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Instance != in.ID || reg.Token != strings.Repeat("c", 48) || reg.Revision != "77" ||
		!strings.HasPrefix(reg.Name, "node") || !strings.Contains(reg.HostFile, "BEGIN RSA PUBLIC KEY") {
		t.Fatalf("register message = %+v", reg)
	}
	st := loadMatrixInstance(in)
	if !st.Enabled || st.Role != "node" || st.Network != in.network() || st.Panel != "http://198.51.100.7:8080/panel" {
		t.Fatalf("node state = %+v", st)
	}
}

// TestAgentNodeApplyRegisterFailureRollback verifies a failed (or timed out)
// hub registration rolls the provisioning back like the manual HTTP flow.
func TestAgentNodeApplyRegisterFailureRollback(t *testing.T) {
	agentLab(t)
	in := matrixInstance{ID: "matrix2"}
	for name, link := range map[string]*fakeAgentLink{
		"refused": {results: map[string]json.RawMessage{agentMsgRegisterResult: []byte(`{"ok":false,"error":"denied"}`)}},
		"timeout": {},
	} {
		t.Run(name, func(t *testing.T) {
			bundle := agentTestBundle(t, in, freeUDPPort(t), freeUDPPort(t), "80")
			ack := agentApplyDesired(link, &agentDesired{Revision: "80", Matrix: &agentDesiredMatrix{
				Instance: in.ID, Action: "apply", Bundle: bundle,
			}})
			if ack.OK {
				t.Fatalf("register %s must fail the ack: %+v", name, ack)
			}
			if !strings.Contains(ack.Error, "hub registration") {
				t.Fatalf("error %q must name the hub registration", ack.Error)
			}
			if _, err := os.Stat(matrixStatePathOf(in)); !os.IsNotExist(err) {
				t.Fatalf("rollback left state file: %v", err)
			}
			if _, err := os.Stat(matrixUnitPathOf(in)); !os.IsNotExist(err) {
				t.Fatalf("rollback left unit file: %v", err)
			}
			if _, err := os.Stat(matrixDirOf(in)); !os.IsNotExist(err) {
				t.Fatalf("rollback left tinc dir: %v", err)
			}
		})
	}
}

// TestAgentCommandsAllowlistAndInjection: unknown ops never execute, and
// injected/invalid parameters are rejected before any exec funnel call.
func TestAgentCommandsAllowlistAndInjection(t *testing.T) {
	agentLab(t)
	var rec agentExecRecorder
	rec.wrap(t, nil)

	cases := []struct {
		name string
		cmd  agentCommand
	}{
		{"shell.exec", agentCommand{ID: "x1", Op: "shell.exec", Params: map[string]string{"instance": "matrix"}}},
		{"rm -rf", agentCommand{ID: "x2", Op: "rm -rf /"}},
		{"injection semicolon", agentCommand{ID: "x3", Op: agentCmdMatrixRestart, Params: map[string]string{"instance": "matrix; reboot"}}},
		{"injection substitution", agentCommand{ID: "x4", Op: agentCmdMatrixStop, Params: map[string]string{"instance": "matrix$(x)"}}},
		{"empty instance", agentCommand{ID: "x5", Op: agentCmdMatrixStop}},
		{"ping injection", agentCommand{ID: "x6", Op: agentCmdDiagPing, Params: map[string]string{"instance": "matrix", "target": "10.254.231.2; rm -rf /"}}},
		{"ping flags", agentCommand{ID: "x7", Op: agentCmdDiagPing, Params: map[string]string{"instance": "matrix", "target": "10.254.231.2 -c 100000"}}},
	}
	for _, tc := range cases {
		res := agentRunCommand(tc.cmd)
		if res.OK || res.Error == "" {
			t.Fatalf("%s: expected rejection, got %+v", tc.name, res)
		}
		if res.ID != tc.cmd.ID {
			t.Fatalf("%s: result must echo the command ID", tc.name)
		}
		if rec.count() != 0 {
			t.Fatalf("%s: exec funnel reached: %v", tc.name, rec.all())
		}
	}
	// Nil params (no instance at all) must be rejected by validation.
	if res := agentRunCommand(agentCommand{ID: "x8", Op: agentCmdMatrixStop}); res.OK || res.Error == "" {
		t.Fatalf("nil params handled wrong: %+v", res)
	}

	// A valid diag.ping goes through exactly once, with the fixed arg shape.
	res := agentRunCommand(agentCommand{ID: "x9", Op: agentCmdDiagPing, Params: map[string]string{"instance": "matrix", "target": "10.254.231.2"}})
	if !res.OK || res.Output != "ping ok" {
		t.Fatalf("diag.ping = %+v", res)
	}
	calls := rec.all()
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "ping -I qmx0 -c 1 -W 1 10.254.231.2") {
		t.Fatalf("diag.ping exec = %v", calls)
	}
}

// TestAgentCommandPowerAndRegister executes the allowlisted ops against a
// stubbed funnel and checks the exec argument shapes, including the
// matrix.register relay with replayed command IDs.
func TestAgentCommandPowerAndRegister(t *testing.T) {
	agentLab(t)
	port, userPort := freeUDPPort(t), freeUDPPort(t)
	if ack := agentApplyDesired(nil, agentHubDesired("1", port, []int{userPort})); !ack.OK {
		t.Fatalf("hub setup: %+v", ack)
	}
	var rec agentExecRecorder
	rec.wrap(t, nil)

	// stop / start / restart shapes
	res := agentRunCommand(agentCommand{ID: "p1", Op: agentCmdMatrixStop, Params: map[string]string{"instance": "matrix"}})
	if !res.OK {
		t.Fatalf("stop: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(configDir, "matrix.json")); err != nil {
		t.Fatal("stop removed the instance state (stop must be stop-only)")
	}
	res = agentRunCommand(agentCommand{ID: "p2", Op: agentCmdMatrixStart, Params: map[string]string{"instance": "matrix"}})
	if !res.OK {
		t.Fatalf("start: %+v", res)
	}
	res = agentRunCommand(agentCommand{ID: "p3", Op: agentCmdMatrixRestart, Params: map[string]string{"instance": "matrix"}})
	if !res.OK {
		t.Fatalf("restart: %+v", res)
	}
	joined := strings.Join(rec.all(), "\n")
	for _, want := range []string{
		"systemctl stop quantum-matrix.service",
		"systemctl enable --now quantum-matrix.service",
		"systemctl restart quantum-matrix.service",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing exec %q in:\n%s", want, joined)
		}
	}

	// matrix.register relay: the pairing token selects the hub instance.
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}))
	st := loadMatrixInstance(matrixLegacyInstance())
	res = agentRunCommand(agentCommand{ID: "r1", Op: agentCmdMatrixRegister, Params: map[string]string{
		"token": st.Token, "name": "node1", "host_file": pub, "addr": "203.0.113.9", "revision": "1",
	}})
	if !res.OK || res.Output == "" {
		t.Fatalf("register: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(matrixDirOf(matrixLegacyInstance()), "hosts", "node1")); err != nil {
		t.Fatalf("node host file not written: %v", err)
	}
	main, _ := os.ReadFile(filepath.Join(matrixDirOf(matrixLegacyInstance()), "hosts", "main"))
	if res.Output != string(main) {
		t.Fatal("register output must be the hub hosts/main content")
	}

	// Replay with the SAME command ID and the same payload: idempotent
	// re-pairing, state not corrupted (still exactly one peer, node1).
	res = agentRunCommand(agentCommand{ID: "r1", Op: agentCmdMatrixRegister, Params: map[string]string{
		"token": st.Token, "name": "node1", "host_file": pub, "addr": "203.0.113.9", "revision": "1",
	}})
	if !res.OK {
		t.Fatalf("identical register replay: %+v", res)
	}
	if peers := loadMatrixInstance(matrixLegacyInstance()).Peers; len(peers) != 1 || peers[0].Name != "node1" {
		t.Fatalf("hub peers corrupted by replay: %+v", peers)
	}

	// Replay with the same command ID but a DIFFERENT node: matrixRegisterHub
	// refuses (hub already paired) instead of overwriting the pairing.
	key2, _ := rsa.GenerateKey(rand.Reader, 2048)
	pub2 := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key2.PublicKey)}))
	res = agentRunCommand(agentCommand{ID: "r1", Op: agentCmdMatrixRegister, Params: map[string]string{
		"token": st.Token, "name": "node2", "host_file": pub2, "addr": "203.0.113.10", "revision": "1",
	}})
	if res.OK || !strings.Contains(res.Error, "already paired") {
		t.Fatalf("second-node replay must be refused: %+v", res)
	}
	if peers := loadMatrixInstance(matrixLegacyInstance()).Peers; len(peers) != 1 || peers[0].Name != "node1" {
		t.Fatalf("hub peers corrupted by second-node replay: %+v", peers)
	}

	// Wrong token and mismatched instance are denied.
	res = agentRunCommand(agentCommand{ID: "r2", Op: agentCmdMatrixRegister, Params: map[string]string{
		"token": "nope", "name": "node3", "host_file": pub, "revision": "1",
	}})
	if res.OK || res.Error != "registration denied" {
		t.Fatalf("wrong token: %+v", res)
	}
	res = agentRunCommand(agentCommand{ID: "r3", Op: agentCmdMatrixRegister, Params: map[string]string{
		"token": st.Token, "instance": "matrix2", "name": "node3", "host_file": pub, "revision": "1",
	}})
	if res.OK || !strings.Contains(res.Error, "does not belong") {
		t.Fatalf("instance mismatch: %+v", res)
	}

	// No matrix lifecycle exec ever touches the gre-agent unit.
	if rec.mentions(agentServiceUnit) {
		t.Fatalf("matrix lifecycle touched the agent unit:\n%s", strings.Join(rec.all(), "\n"))
	}
}

// TestAgentLifecycleIndependence renders the exact systemd unit the install
// subcommand writes and proves the agent's lifecycle is independent from the
// Matrix units: no dependency directives on other units, no destructive
// systemctl call against quantum-matrix* from shutdown or uninstall, and no
// matrix exec touching gre-agent.
func TestAgentLifecycleIndependence(t *testing.T) {
	agentLab(t)

	// 1. The unit body itself.
	unit := agentUnitContent("/usr/local/bin/gre-panel")
	if strings.Contains(unit, "quantum-matrix") {
		t.Fatalf("unit body references a Matrix unit:\n%s", unit)
	}
	var after, wants []string
	for _, line := range strings.Split(unit, "\n") {
		key, val, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		val = strings.TrimSpace(val)
		switch key {
		case "Requires", "PartOf", "BindsTo", "Requisite", "Upholds":
			t.Fatalf("unit must not declare %s=, got %s=%s", key, key, val)
		case "After":
			after = append(after, val)
		case "Wants":
			wants = append(wants, val)
		case "Restart":
			if val != "always" {
				t.Fatalf("Restart=%s, want always", val)
			}
		case "WantedBy":
			if val != "multi-user.target" {
				t.Fatalf("WantedBy=%s, want multi-user.target", val)
			}
		case "ExecStart":
			if !strings.HasSuffix(val, " agent run") {
				t.Fatalf("ExecStart=%q must run `agent run`", val)
			}
		}
	}
	for _, v := range append(append([]string{}, after...), wants...) {
		if v != "network-online.target" {
			t.Fatalf("After=/Wants= must only reference network-online.target, got %q", v)
		}
	}
	if len(after) == 0 || len(wants) == 0 {
		t.Fatal("unit should keep After=/Wants=network-online.target")
	}

	// 2. Uninstall: recorded systemctl calls, none targeting quantum-matrix*.
	unitDir := t.TempDir()
	oldUnitDir, oldSystemctl := agentUnitDir, agentSystemctl
	agentUnitDir = unitDir
	var sysCalls [][]string
	agentSystemctl = func(args ...string) error {
		sysCalls = append(sysCalls, args)
		return nil
	}
	t.Cleanup(func() { agentUnitDir, agentSystemctl = oldUnitDir, oldSystemctl })
	if err := os.WriteFile(filepath.Join(unitDir, agentServiceUnit), []byte(unit), 0644); err != nil {
		t.Fatal(err)
	}
	if code := agentUninstall(); code != 0 {
		t.Fatalf("uninstall exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(unitDir, agentServiceUnit)); !os.IsNotExist(err) {
		t.Fatal("uninstall left the unit file")
	}
	for _, args := range sysCalls {
		for _, a := range args {
			if strings.Contains(a, "quantum-matrix") {
				t.Fatalf("uninstall called systemctl with a Matrix unit: %v", args)
			}
		}
	}
	if len(sysCalls) != 2 || strings.Join(sysCalls[0], " ") != "disable --now gre-agent" || strings.Join(sysCalls[1], " ") != "daemon-reload" {
		t.Fatalf("unexpected systemctl traffic during uninstall: %v", sysCalls)
	}

	// 3. Agent shutdown: a full connect→graceful-shutdown cycle must issue
	// zero destructive systemctl calls against quantum-matrix* (and none
	// against gre-agent either).
	ident := agentIdentity{ID: "agt_lc", Key: "k", Server: "http://127.0.0.1:1"}
	if err := agentSaveIdentity(ident); err != nil {
		t.Fatal(err)
	}
	var rec agentExecRecorder
	rec.wrap(t, nil)
	oldDialer := agentDialer
	t.Cleanup(func() { agentDialer = oldDialer })
	agentDialer = func(agentIdentity) (agentWS, error) { return newFakeBlockingWS(), nil }
	rt := &agentRuntime{ident: ident, start: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- rt.loop(ctx) }()
	time.Sleep(150 * time.Millisecond) // let it connect, hello, heartbeat once
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("graceful shutdown exit %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not shut down")
	}
	if rec.touchesUnit("quantum-matrix") {
		t.Fatalf("shutdown mutated a Matrix unit:\n%s", strings.Join(rec.all(), "\n"))
	}
	if rec.mentions(agentServiceUnit) {
		t.Fatalf("shutdown touched the agent unit via the matrix funnel:\n%s", strings.Join(rec.all(), "\n"))
	}
	if st := agentLoadState(); st.WS == nil || st.WS.Connected {
		t.Fatalf("shutdown must journal disconnected state, got %+v", st.WS)
	}
}

// TestAgentParsePing parses fixed Linux iputils ping outputs.
func TestAgentParsePing(t *testing.T) {
	full := "PING 10.254.231.2 (10.254.231.2) from 10.254.231.1 qmx0: 56(84) bytes of data.\n" +
		"64 bytes from 10.254.231.2: icmp_seq=1 ttl=64 time=0.512 ms\n" +
		"64 bytes from 10.254.231.2: icmp_seq=2 ttl=64 time=0.487 ms\n" +
		"64 bytes from 10.254.231.2: icmp_seq=3 ttl=64 time=0.530 ms\n" +
		"\n--- 10.254.231.2 ping statistics ---\n" +
		"3 packets transmitted, 3 received, 0% packet loss, time 2003ms\n" +
		"rtt min/avg/max/mdev = 0.487/0.509/0.530/0.017 ms\n"
	samples, loss := agentParsePing(full)
	if len(samples) != 3 || loss != 0 {
		t.Fatalf("full parse = %v loss %v", samples, loss)
	}
	if got := agentMeanJitter(samples); got < 0.0335 || got > 0.0345 {
		t.Fatalf("jitter = %v, want ~0.034", got)
	}

	lossy := "2 packets transmitted, 1 received, 50% packet loss, time 1001ms\n" +
		"64 bytes from 10.254.231.2: icmp_seq=1 ttl=64 time=12.5 ms\n"
	samples, loss = agentParsePing(lossy)
	if len(samples) != 1 || samples[0] != 12.5 || loss != 50 {
		t.Fatalf("lossy parse = %v loss %v", samples, loss)
	}

	dead := "3 packets transmitted, 0 received, 100% packet loss, time 2003ms\n"
	samples, loss = agentParsePing(dead)
	if len(samples) != 0 || loss != 100 {
		t.Fatalf("dead parse = %v loss %v", samples, loss)
	}

	// No summary line → loss -1 (caller decides), timestamps still extracted.
	samples, loss = agentParsePing("64 bytes from 10.254.231.2: icmp_seq=1 ttl=64 time=1.5 ms\n")
	if len(samples) != 1 || loss != -1 {
		t.Fatalf("summary-less parse = %v loss %v", samples, loss)
	}
}

// TestAgentQualityBuckets covers the good/fair/poor/down thresholds.
func TestAgentQualityBuckets(t *testing.T) {
	base := agentTunnelHealth{Service: true, IfaceUp: true, Reachable: true}
	cases := []struct {
		mut  func(*agentTunnelHealth)
		want string
	}{
		{func(h *agentTunnelHealth) { h.LossPct = 0; h.JitterMs = 1 }, "good"},
		{func(h *agentTunnelHealth) { h.LossPct = 2; h.JitterMs = 1 }, "fair"},
		{func(h *agentTunnelHealth) { h.LossPct = 0; h.JitterMs = 30 }, "fair"},
		{func(h *agentTunnelHealth) { h.LossPct = 10 }, "poor"},
		{func(h *agentTunnelHealth) { h.Reachable = false }, "poor"},
		{func(h *agentTunnelHealth) { h.Service = false }, "down"},
		{func(h *agentTunnelHealth) { h.IfaceUp = false }, "down"},
		{func(h *agentTunnelHealth) { h.LossPct = 100 }, "down"},
	}
	for i, tc := range cases {
		h := base
		tc.mut(&h)
		if got := agentQualityBucket(h); got != tc.want {
			t.Fatalf("case %d: quality = %s, want %s", i, got, tc.want)
		}
	}
}

// TestAgentProbeInstance runs one probe cycle with stubbed ping/link.
func TestAgentProbeInstance(t *testing.T) {
	agentLab(t)
	oldPing, oldLink := agentPingExec, agentIPLinkShow
	pings := 0
	agentPingExec = func(iface, target string) (string, error) {
		pings++
		return "time=10 ms\ntime=12 ms\ntime=14 ms\n0% packet loss\n", nil
	}
	agentIPLinkShow = func(string) (string, error) { return `[{"ifname":"qmx0","operstate":"UP"}]`, nil }
	t.Cleanup(func() { agentPingExec, agentIPLinkShow = oldPing, oldLink })

	legacy := matrixLegacyInstance()
	st := matrixState{Enabled: true, Network: matrixNetname, Role: "node", Family: "ipv4", Port: 21010, Addr: "185.226.119.75", Name: "nodeabc", IP: matrixFirstPeer}
	if err := saveMatrixInstance(legacy, st); err != nil {
		t.Fatal(err)
	}
	h := agentProbeInstance(legacy)
	if pings != 1 {
		t.Fatalf("node probes its hub only, got %d ping rounds", pings)
	}
	if !h.Service || !h.IfaceUp || !h.Reachable {
		t.Fatalf("health flags = %+v", h)
	}
	if h.LatencyMs != 12 || h.JitterMs != 2 || h.LossPct != 0 {
		t.Fatalf("latency %v jitter %v loss %v", h.LatencyMs, h.JitterMs, h.LossPct)
	}
	if h.WindowS != 60 || h.Samples != 3 || h.Quality != "good" {
		t.Fatalf("window/samples/quality = %d/%d/%s", h.WindowS, h.Samples, h.Quality)
	}
	if snap := agentHealthSnapshot(); len(snap) != 1 || snap[0].Instance != "matrix" {
		t.Fatalf("snapshot = %+v", snap)
	}

	// A disabled instance is "down" and must not ping at all.
	if err := os.Remove(matrixStatePathOf(legacy)); err != nil {
		t.Fatal(err)
	}
	pings = 0
	h = agentProbeInstance(legacy)
	if pings != 0 || h.Quality != "down" {
		t.Fatalf("disabled instance = pings %d quality %s", pings, h.Quality)
	}
}

// TestAgentTrafficClassification covers the classifier and /proc/net/dev
// parsing, including the UNKNOWN fallback.
func TestAgentTrafficClassification(t *testing.T) {
	cases := map[string]string{
		"eth0": agentIfacePhysical, "ens18": agentIfacePhysical, "eno1": agentIfacePhysical,
		"wlan0": agentIfacePhysical, "enp3s0": agentIfacePhysical,
		"qmx0": agentIfaceTunnel, "qmx2": agentIfaceTunnel,
		"lo": agentIfaceSkip, "": agentIfaceSkip,
		"gre0": agentIfaceOther, "gretap1": agentIfaceOther, "tun0": agentIfaceOther,
		"veth9f3abc": agentIfaceOther, "br-abc123": agentIfaceOther, "docker0": agentIfaceOther,
		"vti0": agentIfaceOther, "sit0": agentIfaceOther, "wg0": agentIfaceOther, "eth0-if": agentIfaceOther,
	}
	for name, want := range cases {
		if got := agentClassifyIface(name); got != want {
			t.Fatalf("classify(%q) = %q, want %q", name, got, want)
		}
	}

	data := "Inter-|   Receive                                                |  Transmit\n" +
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n" +
		"    lo: 100 1 0 0 0 0 0 0 200 2 0 0 0 0 0 0\n" +
		"  eth0: 1000 10 0 0 0 0 0 0 2000 20 0 0 0 0 0 0\n" +
		"  qmx0: 10 1 0 0 0 0 0 0 20 2 0 0 0 0 0 0\n" +
		"docker0: 1 1 0 0 0 0 0 0 1 1 0 0 0 0 0 0\n"
	oldRead := agentReadNetDev
	agentReadNetDev = func() (string, error) { return data, nil }
	t.Cleanup(func() { agentReadNetDev = oldRead })

	rep := agentCollectTraffic()
	if len(rep.Ifaces) != 2 {
		t.Fatalf("ifaces = %+v", rep.Ifaces)
	}
	if rep.Ifaces[0].Name != "eth0" || rep.Ifaces[0].Kind != agentIfacePhysical || rep.Ifaces[0].Rx != 1000 || rep.Ifaces[0].Tx != 2000 || !rep.Ifaces[0].Known {
		t.Fatalf("eth0 entry = %+v", rep.Ifaces[0])
	}
	if rep.Ifaces[1].Name != "qmx0" || rep.Ifaces[1].Kind != agentIfaceTunnel || !rep.Ifaces[1].Known {
		t.Fatalf("qmx0 entry = %+v", rep.Ifaces[1])
	}

	agentReadNetDev = func() (string, error) { return "", errAgentNoNetDev }
	rep = agentCollectTraffic()
	if len(rep.Ifaces) != 1 || rep.Ifaces[0].Name != "_all" || rep.Ifaces[0].Known {
		t.Fatalf("unreadable fallback = %+v", rep.Ifaces)
	}
}

// TestAgentNodeDetect covers install-dir and docker detection plus silent
// failure.
func TestAgentNodeDetect(t *testing.T) {
	agentLab(t)
	dir := t.TempDir()
	oldDirs, oldPS := agentPasarguardDirs, agentDockerPS
	t.Cleanup(func() { agentPasarguardDirs, agentDockerPS = oldDirs, oldPS })

	agentPasarguardDirs = []string{dir}
	if d := agentDetectNode(); !d.Detected || d.Kind != "install-dir" || d.Detail != dir {
		t.Fatalf("dir detect = %+v", d)
	}

	agentPasarguardDirs = oldDirs
	agentDockerPS = func() (string, error) { return "pasarguard-node\nredis\n", nil }
	if d := agentDetectNode(); !d.Detected || d.Kind != "docker-container" || d.Detail != "pasarguard-node" {
		t.Fatalf("docker detect = %+v", d)
	}
	agentDockerPS = func() (string, error) { return "", errors.New("docker gone") }
	if d := agentDetectNode(); d.Detected {
		t.Fatalf("errors must mean detected=false: %+v", d)
	}
}

// TestAgentTLSPinTOFU covers the trust-on-first-use pin store and mismatch.
func TestAgentTLSPinTOFU(t *testing.T) {
	agentLab(t)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "controller"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pin := agentSPKIPin(cert)
	if len(pin) != 64 {
		t.Fatalf("pin %q is not a sha256 hex", pin)
	}
	ident := agentIdentity{ID: "agt_pin", Key: "k"}
	if err := agentCheckTLSPin(&ident, pin); err != nil {
		t.Fatal(err)
	}
	if ident.TLSPin != pin {
		t.Fatal("first connect must store the pin")
	}
	saved, ok := agentLoadIdentity()
	if !ok || saved.TLSPin != pin {
		t.Fatalf("pin not persisted: %+v ok=%v", saved, ok)
	}
	err = agentCheckTLSPin(&ident, strings.Repeat("f", 64))
	if err == nil || !strings.Contains(err.Error(), "MITM") {
		t.Fatalf("pin mismatch must abort loudly, got %v", err)
	}
}

// TestAgentEnrollCLI runs enroll against an httptest controller: success
// persists agent.json, rejection fails cleanly.
func TestAgentEnrollCLI(t *testing.T) {
	oldCfg := configDir
	t.Cleanup(func() { configDir = oldCfg })

	var gotToken, gotPath string
	var gotBody agentEnrollRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotToken = r.Header.Get(agentEnrollHeader)
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if gotToken != "tok123" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte(`{"id":"agt_enr","key":"aabbcc","base_path":"panel"}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	t.Setenv("GRE_PANEL_DIR", dir)
	if code := agentCmd([]string{"enroll", "--server", srv.URL, "--token", "tok123", "--name", "edge-1"}); code != 0 {
		t.Fatalf("enroll exit %d", code)
	}
	if gotPath != agentEnrollPath || gotToken != "tok123" {
		t.Fatalf("request = %s token %q", gotPath, gotToken)
	}
	if gotBody.OS != runtime.GOOS || gotBody.Arch != runtime.GOARCH || gotBody.Version != panelVersion ||
		gotBody.Name != "edge-1" || len(gotBody.Caps) != 4 {
		t.Fatalf("enroll body = %+v", gotBody)
	}
	ident, ok := agentLoadIdentity()
	if !ok || ident.ID != "agt_enr" || ident.Key != "aabbcc" || ident.BasePath != "panel" || ident.Name != "edge-1" || ident.Server != srv.URL {
		t.Fatalf("identity = %+v ok=%v", ident, ok)
	}
	if want := strings.Replace(srv.URL, "http://", "ws://", 1) + "/panel" + agentWSPath; agentWSURL(ident) != want {
		t.Fatalf("ws url = %s, want %s", agentWSURL(ident), want)
	}
	if fi, err := os.Stat(agentIdentityPath()); err != nil || fi.Size() == 0 {
		t.Fatalf("agent.json missing: %v", err)
	}

	// Rejection: exit 1, no identity written.
	dir2 := t.TempDir()
	t.Setenv("GRE_PANEL_DIR", dir2)
	if code := agentCmd([]string{"enroll", "--server", srv.URL, "--token", "wrong"}); code != 1 {
		t.Fatalf("rejected enroll exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir2, "agent.json")); !os.IsNotExist(err) {
		t.Fatal("rejected enroll must not write agent.json")
	}
}

// TestAgentStatusCLI prints JSON without ever leaking the agent key.
func TestAgentStatusCLI(t *testing.T) {
	agentLab(t)
	ident := agentIdentity{ID: "agt_s", Key: "supersecretkey", Server: "https://ctrl.example:7443", BasePath: "panel", Name: "n1"}
	if err := agentSaveIdentity(ident); err != nil {
		t.Fatal(err)
	}
	if err := agentMutateState(func(st *agentPersistedState) {
		st.AppliedRevision = "42"
		st.WS = &agentWSStatus{Connected: true, StartedAt: time.Now().Unix() - 120}
	}); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(t, func() {
		if code := agentCmd([]string{"status"}); code != 0 {
			t.Errorf("status exit %d", code)
		}
	})
	if !strings.Contains(out, `"applied_revision": "42"`) || !strings.Contains(out, `"id": "agt_s"`) {
		t.Fatalf("status output:\n%s", out)
	}
	if strings.Contains(out, ident.Key) {
		t.Fatal("status leaked the agent key")
	}
	// Unknown subcommand and usage exit codes.
	if code := agentCmd([]string{"bogus"}); code != 1 {
		t.Fatalf("bogus subcommand exit %d", code)
	}
	if code := agentCmd([]string{"--help"}); code != 0 {
		t.Fatalf("help exit %d", code)
	}
}

// captureStdout runs fn while capturing everything it prints to stdout.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

func TestAgentWSURLBasePathNotDoubled(t *testing.T) {
	cases := []struct {
		server, basePath, want string
	}{
		{"https://ctrl.example:7443/panel", "panel", "wss://ctrl.example:7443/panel/api/agent/ws"},
		{"https://ctrl.example:7443", "panel", "wss://ctrl.example:7443/panel/api/agent/ws"},
		{"https://ctrl.example:7443/", "panel", "wss://ctrl.example:7443/panel/api/agent/ws"},
		{"https://ctrl.example:7443", "", "wss://ctrl.example:7443/api/agent/ws"},
		{"http://127.0.0.1:17778/4UCf3vAbDv5I", "4UCf3vAbDv5I", "ws://127.0.0.1:17778/4UCf3vAbDv5I/api/agent/ws"},
	}
	for _, tc := range cases {
		got := agentWSURL(agentIdentity{Server: tc.server, BasePath: tc.basePath})
		if got != tc.want {
			t.Fatalf("agentWSURL(%q, base %q) = %q, want %q", tc.server, tc.basePath, got, tc.want)
		}
	}
}
