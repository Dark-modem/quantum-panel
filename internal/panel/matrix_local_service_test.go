package panel

import (
	"bytes"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatrixLocalBridgeCopiesBytesUnchanged(t *testing.T) {
	target, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	go func() {
		c, e := target.Accept()
		if e != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
	bridge, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer bridge.Close()
	go func() {
		c, e := bridge.Accept()
		if e == nil {
			matrixCopyConnection(c, target.Addr().String())
		}
	}()
	c, e := net.Dial("tcp4", bridge.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	payload := bytes.Repeat([]byte("TLS and Reality payload pass through unchanged\x00\xff"), 2000)
	go func() { _, _ = c.Write(payload); _ = c.(*net.TCPConn).CloseWrite() }()
	got, e := io.ReadAll(c)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("bridge changed payload")
	}
}
func TestMatrixLocalBridgeRejectsWrongRoleAndProtectedPorts(t *testing.T) {
	old := configDir
	configDir = t.TempDir()
	t.Cleanup(func() { configDir = old })
	st := matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Ports: []int{443}}
	if e := saveMatrix(st); e != nil {
		t.Fatal(e)
	}
	inst := matrixLegacyInstance()
	if e := matrixConfigureLocalService(inst, matrixLocalService{443, 443}, false); e == nil {
		t.Fatal("hub bridge accepted")
	}
	st.Role = "node"
	if e := saveMatrix(st); e != nil {
		t.Fatal(e)
	}
	if e := matrixConfigureLocalService(inst, matrixLocalService{443, 22}, false); e == nil {
		t.Fatal("SSH target accepted")
	}
	if e := matrixConfigureLocalService(inst, matrixLocalService{8080, 443}, false); e == nil {
		t.Fatal("unforwarded port accepted")
	}
}
func TestMatrixLocalBridgeStateAndExactRemoval(t *testing.T) {
	oldDir, oldUnits, oldExec := configDir, matrixUnitDir, matrixExec
	configDir = t.TempDir()
	matrixUnitDir = t.TempDir()
	var calls []string
	matrixExec = func(n string, args ...string) (string, error) {
		calls = append(calls, n+" "+strings.Join(args, " "))
		return "", nil
	}
	t.Cleanup(func() { configDir, matrixUnitDir, matrixExec = oldDir, oldUnits, oldExec })
	inst := matrixLegacyInstance()
	if e := saveMatrixLocalServices(inst, []matrixLocalService{{443, 443}}); e != nil {
		t.Fatal(e)
	}
	ours := filepath.Join(matrixUnitDir, matrixLocalUnit(inst, 443))
	other := filepath.Join(matrixUnitDir, "xray.service")
	if e := os.WriteFile(ours, []byte("owned"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(other, []byte("unrelated"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := matrixRemoveLocalServices(inst); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(other); e != nil {
		t.Fatal("removed Xray unit")
	}
	if _, e := os.Stat(ours); !os.IsNotExist(e) {
		t.Fatal("owned unit remains")
	}
	if _, e := os.Stat(matrixLocalPath(inst)); !os.IsNotExist(e) {
		t.Fatal("bridge state remains")
	}
	for _, c := range calls {
		if strings.Contains(c, "xray") {
			t.Fatal("changed Xray service")
		}
	}
}
