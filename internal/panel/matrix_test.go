package panel

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMatrixValidation(t *testing.T) {
	for _, family := range []string{"ipv4", "ipv6"} {
		addr := "185.226.119.74"
		if family == "ipv6" {
			addr = "2001:db8::1"
		}
		if e := validateMatrixSettings(family, addr, 21097, []int{443, 18080}); e != nil {
			t.Fatal(e)
		}
	}
	for _, v := range []struct {
		family, addr string
		ports        []int
	}{{"ipv4", "2001:db8::1", []int{18080}}, {"ipv6", "185.226.119.74", []int{18080}}, {"ipv4", "127.0.0.1", []int{18080}}, {"ipv4", "185.226.119.74", []int{22}}, {"ipv4", "185.226.119.74", []int{18080, 18080}}, {"ipv4", "185.226.119.74", nil}} {
		if validateMatrixSettings(v.family, v.addr, 21097, v.ports) == nil {
			t.Fatalf("accepted %+v", v)
		}
	}
}
func TestMatrixBundlePinsKeyAndRejectsBadPayloads(t *testing.T) {
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	pub := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)}))
	b := matrixBundlePayload{Network: matrixNetname, Panel: "http://185.226.119.74:7777/test", Addr: "185.226.119.74", Family: "ipv4", Port: 21097, FIP: matrixFirstPeer, Ports: []int{18080}, Token: strings.Repeat("a", 64), MainHost: "Cipher = none\nSubnet = 0.0.0.0/0\n" + pub}
	enc := func() string {
		raw, _ := json.Marshal(b)
		return matrixBundlePfx + base64.StdEncoding.EncodeToString(raw)
	}
	got, e := parseMatrixBundle(enc())
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(got.MainHost, "Cipher = none") || strings.Contains(got.MainHost, "0.0.0.0/0") {
		t.Fatal("accepted untrusted tinc directives")
	}
	b.Panel = "file:///etc/shadow"
	if _, e = parseMatrixBundle(enc()); e == nil {
		t.Fatal("accepted file URL")
	}
	b.Panel = "http://185.226.119.74:7777/test"
	b.FIP = "127.0.0.1"
	if _, e = parseMatrixBundle(enc()); e == nil {
		t.Fatal("accepted arbitrary mesh IP")
	}
}
func TestMatrixNamespacedConfigAndRoutes(t *testing.T) {
	oldDir, oldUnit, oldTinc := configDir, matrixUnitDir, matrixTincPath
	configDir = t.TempDir()
	matrixUnitDir = t.TempDir()
	matrixTincPath = func() (string, error) { return "/usr/sbin/tincd", nil }
	t.Cleanup(func() { configDir, matrixUnitDir, matrixTincPath = oldDir, oldUnit, oldTinc })
	st := matrixState{Role: "node", Name: "node123", Family: "ipv6", Port: 21097, IP: matrixFirstPeer, Network: matrixNetname}
	in := matrixLegacyInstance()
	if e := matrixWriteConfig(in, st, "public host file"); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"tinc.conf", "tinc-up", "hosts/node123", "rsa_key.priv"} {
		if _, e := os.Stat(filepath.Join(matrixDir(), name)); e != nil {
			t.Fatal(e)
		}
	}
	up, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc-up"))
	if !strings.Contains(string(up), matrixFirstPeer+"/30") {
		t.Fatal("node lacks return route")
	}
	conf, _ := os.ReadFile(filepath.Join(matrixDir(), "tinc.conf"))
	if !strings.Contains(string(conf), "Interface = qmx0") || !strings.Contains(string(conf), "AddressFamily = ipv6") {
		t.Fatal("unisolated config")
	}
	host, _ := os.ReadFile(filepath.Join(matrixDir(), "hosts/node123"))
	if !strings.Contains(string(host), "Subnet = "+matrixFirstPeer+"/32") {
		t.Fatal("missing own subnet")
	}
	if matrixNameRe.MatchString("node-with-dashes") || matrixNameRe.MatchString("../node") {
		t.Fatal("unsafe tinc name")
	}
}
func TestMatrixForwardingScopedAndReversible(t *testing.T) {
	st := matrixState{Addr: "185.226.119.74", Ports: []int{18080}}
	script := matrixForwardScript(matrixLegacyInstance(), st)
	for _, v := range []string{"-d 185.226.119.74", "--dport 18080", "-o qmx0", "--to-source " + matrixHubIP, "--comment quantum-matrix"} {
		if !strings.Contains(script, v) {
			t.Errorf("missing %s", v)
		}
	}
	if strings.Contains(script, "iptables -t nat -F\n") {
		t.Fatal("flushes host NAT")
	}
	old := matrixExec
	var calls []string
	matrixExec = func(name string, args ...string) (string, error) {
		cmd := name + " " + strings.Join(args, " ")
		calls = append(calls, cmd)
		if strings.Contains(cmd, " -C ") {
			return "", fmt.Errorf("not present")
		}
		return "", nil
	}
	t.Cleanup(func() { matrixExec = old })
	if e := matrixDNATClear(matrixLegacyInstance()); e != nil {
		t.Fatal(e)
	}
	for _, c := range calls {
		if strings.Contains(c, " -F ") && !strings.Contains(c, "QMX_") {
			t.Fatal("touches unrelated chain", c)
		}
	}
}
func TestMatrixRefusesUnownedRemoval(t *testing.T) {
	if e := matrixRemove(matrixLegacyInstance(), matrixState{Network: "matrix", Enabled: true}); e == nil {
		t.Fatal("would remove legacy network")
	}
}
func TestMatrixRegistrationUnauthenticatedDenied(t *testing.T) {
	old := configDir
	configDir = t.TempDir()
	t.Cleanup(func() { configDir = old })
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/matrix/register", strings.NewReader(`{"token":"wrong","name":"node"}`))
	handleMatrixRegister(rr, req)
	if rr.Code != 403 {
		t.Fatalf("status %d", rr.Code)
	}
}
