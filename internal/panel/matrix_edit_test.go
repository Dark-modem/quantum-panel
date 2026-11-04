package panel

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func matrixEditLab(t *testing.T) matrixState {
	t.Helper()
	oldDir, oldUnit, oldTinc, oldExec := configDir, matrixUnitDir, matrixTincPath, matrixExec
	configDir = t.TempDir()
	matrixUnitDir = t.TempDir()
	matrixTincPath = func() (string, error) { return "/usr/sbin/tincd", nil }
	matrixExec = func(name string, args ...string) (string, error) {
		s := strings.Join(args, " ")
		if name == "iptables" && (strings.Contains(s, "-S ") || strings.Contains(s, "-C ")) {
			return "", fmt.Errorf("chain absent")
		}
		return "", nil
	}
	t.Cleanup(func() { configDir, matrixUnitDir, matrixTincPath, matrixExec = oldDir, oldUnit, oldTinc, oldExec })
	inst := matrixLegacyInstance()
	st := matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Family: "ipv4", Addr: "185.226.119.74", Port: 21097, Name: "main", IP: matrixHubIP, Token: strings.Repeat("a", 64), Ports: []int{18080}, Peers: []matrixPeer{{Name: "node", IP: matrixFirstPeer}}, ForwardBefore: "1"}
	if e := matrixWriteConfig(inst, st, ""); e != nil {
		t.Fatal(e)
	}
	if e := saveMatrixInstance(inst, st); e != nil {
		t.Fatal(e)
	}
	return st
}
func TestMatrixEditPortsPreservesKeyAndDoesNotRestart(t *testing.T) {
	st := matrixEditLab(t)
	before, _ := os.ReadFile(filepath.Join(matrixDir(), "rsa_key.priv"))
	old := matrixExec
	restarts := 0
	matrixExec = func(n string, a ...string) (string, error) {
		if n == "systemctl" && len(a) > 0 && a[0] == "restart" {
			restarts++
		}
		return old(n, a...)
	}
	ports := []int{18080}
	next, e := matrixApplyEdit(st, matrixEditRequest{Ports: &ports})
	if e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(matrixDir(), "rsa_key.priv"))
	if string(before) != string(after) || next.Token != st.Token || next.Name != st.Name {
		t.Fatal("edit regenerated identity")
	}
	if restarts != 0 {
		t.Fatal("port-only edit restarted live transport")
	}
	if !reflect.DeepEqual(loadMatrix().Ports, ports) {
		t.Fatal("state not saved")
	}
}
func TestMatrixEditFamilyMarksPeerSyncRequired(t *testing.T) {
	st := matrixEditLab(t)
	before, _ := os.ReadFile(filepath.Join(matrixDir(), "rsa_key.priv"))
	next, e := matrixApplyEdit(st, matrixEditRequest{Family: "ipv6", Addr: "2001:db8::1"})
	if e != nil {
		t.Fatal(e)
	}
	if !next.PeerSyncRequired || next.Revision == "" {
		t.Fatal("family change not marked pending")
	}
	conf, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc.conf"))
	if !strings.Contains(string(conf), "AddressFamily = ipv6") {
		t.Fatal("transport family unchanged")
	}
	after, _ := os.ReadFile(filepath.Join(matrixDir(), "rsa_key.priv"))
	if string(after) != string(before) {
		t.Fatal("key changed")
	}
}
func TestMatrixEditInvalidFamilyLeavesConfigIntact(t *testing.T) {
	st := matrixEditLab(t)
	before, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc.conf"))
	if _, e := matrixApplyEdit(st, matrixEditRequest{Family: "ipv6", Addr: "185.226.119.74"}); e == nil {
		t.Fatal("invalid edit accepted")
	}
	after, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc.conf"))
	if string(before) != string(after) {
		t.Fatal("invalid edit modified config")
	}
}
func TestMatrixEditFailureRestoresFilesAndState(t *testing.T) {
	st := matrixEditLab(t)
	before, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc.conf"))
	old := matrixExec
	failed := false
	matrixExec = func(n string, a ...string) (string, error) {
		if n == "sh" && !failed {
			failed = true
			return "", fmt.Errorf("injected failure")
		}
		return old(n, a...)
	}
	if _, e := matrixApplyEdit(st, matrixEditRequest{Family: "ipv6", Addr: "2001:db8::1"}); e == nil {
		t.Fatal("injected edit should fail")
	}
	after, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc.conf"))
	if string(before) != string(after) || loadMatrix().Family != "ipv4" {
		t.Fatal("rollback incomplete")
	}
}
func TestMatrixEditRejectsNewOccupiedPortButAllowsOwn(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows wildcard binds do not enforce Linux port exclusivity")
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	if e = matrixEditPortConflicts(nil, []int{port}); e == nil {
		t.Fatal("accepted occupied new port")
	}
	if e = matrixEditPortConflicts([]int{port}, []int{port}); e != nil {
		t.Fatal(e)
	}
}
func TestMatrixEditNodeRejectsHubKeyReplacement(t *testing.T) {
	st := matrixEditLab(t)
	host, _ := os.ReadFile(filepath.Join(matrixDir(), "hosts/main"))
	st.Role = "node"
	if e := os.WriteFile(filepath.Join(matrixDir(), "hosts/main"), host, 0600); e != nil {
		t.Fatal(e)
	}
	if e := matrixApplyNodeBundle(st, matrixBundlePayload{Panel: "http://different-hub", MainHost: string(host)}); e == nil {
		t.Fatal("changed hub accepted")
	}
}
