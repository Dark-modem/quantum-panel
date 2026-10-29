package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	matrixNetname   = "quantum_matrix"
	matrixIfaceName = "qmx0"
	matrixTag       = "quantum-matrix"
	matrixUnit      = "quantum-matrix.service"
	matrixHubIP     = "10.254.231.1"
	matrixFirstPeer = "10.254.231.2"
	matrixSubnet    = "10.254.231.0/30"
	matrixBundlePfx = "hmx1_"
)

var matrixMu sync.Mutex
var matrixNameRe = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)
var matrixExec = runMatrixCmd
var matrixUnitDir = "/etc/systemd/system"
var matrixTincPath = func() (string, error) { return exec.LookPath("tincd") }

// One owned network per server; a hub admits one node. Legacy tinc networks
// are deliberately not adopted or removed.
type matrixPeer struct {
	Name    string `json:"name"`
	Addr    string `json:"addr"`
	IP      string `json:"ip"`
	KeyHash string `json:"key_hash"`
}
type matrixState struct {
	Enabled          bool         `json:"enabled"`
	Network          string       `json:"network"`
	Role             string       `json:"role"`
	Family           string       `json:"family"`
	Port             int          `json:"port"`
	Addr             string       `json:"addr"`
	Token            string       `json:"token,omitempty"`
	Ports            []int        `json:"ports"`
	Peers            []matrixPeer `json:"peers"`
	Name             string       `json:"name"`
	Panel            string       `json:"panel,omitempty"`
	IP               string       `json:"ip"`
	ForwardBefore    string       `json:"forward_before,omitempty"`
	Revision         string       `json:"revision,omitempty"`
	PeerSyncRequired bool         `json:"peer_sync_required,omitempty"`
}
type matrixBundlePayload struct {
	Panel    string `json:"panel"`
	Token    string `json:"token"`
	Family   string `json:"family"`
	Addr     string `json:"addr"`
	Port     int    `json:"port"`
	FIP      string `json:"fip"`
	Ports    []int  `json:"ports"`
	MainHost string `json:"main_host"`
	Network  string `json:"network"`
	Revision string `json:"revision,omitempty"`
}

func matrixDir() string       { return filepath.Join(configDir, "matrix-tinc") }
func matrixStatePath() string { return filepath.Join(configDir, "matrix.json") }
func matrixUnitPath() string  { return filepath.Join(matrixUnitDir, matrixUnit) }
func loadMatrix() matrixState {
	var st matrixState
	b, e := os.ReadFile(matrixStatePath())
	if e == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}
func saveMatrix(st matrixState) error {
	if e := os.MkdirAll(configDir, 0700); e != nil {
		return e
	}
	b, e := json.MarshalIndent(st, "", "  ")
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(configDir, ".matrix-state-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(append(b, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), matrixStatePath())
}
func runMatrixCmd(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, name, args...)
	c.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	b, e := c.CombinedOutput()
	if e != nil {
		return string(b), fmt.Errorf("%s failed: %w: %s", name, e, trimLen(string(b), 400))
	}
	return string(b), nil
}

var errMatrixPackageBusy = errors.New("package manager is busy")
var matrixAptSleep = time.Sleep
var matrixAptNow = time.Now

func matrixPackageLockError(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{"could not get lock", "unable to acquire", "unable to lock", "is held by process", "waiting for cache lock"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func matrixAptGet(args ...string) error {
	// dpkg's native wait protects an installation already in progress. The
	// apt lists lock does not honor this option, so retry that lock separately.
	deadline := matrixAptNow().Add(90 * time.Second)
	for {
		out, err := matrixExec("apt-get", append([]string{"-o", "DPkg::Lock::Timeout=90"}, args...)...)
		if err == nil {
			return nil
		}
		if !matrixPackageLockError(out + " " + err.Error()) {
			return err
		}
		if !matrixAptNow().Before(deadline) {
			return fmt.Errorf("%w: another apt/dpkg process is still running after waiting; let it finish and retry Matrix setup", errMatrixPackageBusy)
		}
		matrixAptSleep(3 * time.Second)
	}
}

func matrixInstallTinc() error {
	if _, e := matrixTincPath(); e == nil {
		return nil
	}
	if err := matrixAptGet("update"); err != nil {
		return err
	}
	return matrixAptGet("install", "-y", "--no-install-recommends", "tinc")
}

func writeMatrixSetupError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errMatrixPackageBusy) {
		writeAPIError(w, r, "E-MATRIX-09", "")
		return
	}
	writeAPIError(w, r, "E-MATRIX-07", err.Error())
}
func validateMatrixSettings(family, addr string, port int, ports []int) error {
	ip := net.ParseIP(addr)
	if (family != "ipv4" && family != "ipv6") || ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return fmt.Errorf("valid transport address and family ipv4 or ipv6 required")
	}
	if (family == "ipv4") != (ip.To4() != nil) {
		return fmt.Errorf("address does not match selected family")
	}
	if port < 1024 || port > 65535 {
		return fmt.Errorf("tinc port must be 1024-65535")
	}
	if len(ports) == 0 || len(ports) > 20 {
		return fmt.Errorf("choose 1-20 forwarded ports")
	}
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 1 || p > 65535 || p == port || p == cfg.Port || p == 22 || seen[p] {
			return fmt.Errorf("forwarded ports must be unique, 1-65535, and exclude SSH, panel and tinc ports")
		}
		seen[p] = true
	}
	return nil
}
func matrixDetectV6() string {
	b, e := matrixExec("ip", "-j", "-6", "addr", "show", "scope", "global")
	if e != nil {
		return ""
	}
	var rows []struct {
		Info []struct {
			Local string `json:"local"`
		} `json:"addr_info"`
	}
	if json.Unmarshal([]byte(b), &rows) != nil {
		return ""
	}
	for _, r := range rows {
		for _, a := range r.Info {
			p := net.ParseIP(a.Local)
			if p != nil && p.IsGlobalUnicast() {
				return a.Local
			}
		}
	}
	return ""
}
func matrixCheckResources(st matrixState) error {
	for _, p := range []string{matrixDir(), matrixUnitPath()} {
		if _, e := os.Lstat(p); e == nil {
			return fmt.Errorf("resource already exists, refusing to overwrite %s", p)
		} else if !os.IsNotExist(e) {
			return e
		}
	}
	routes, e := matrixExec("ip", "-j", "-4", "route", "show", "table", "all")
	if e != nil {
		return e
	}
	var rows []struct {
		Dst string `json:"dst"`
	}
	if json.Unmarshal([]byte(routes), &rows) != nil {
		return fmt.Errorf("cannot read routes")
	}
	_, own, _ := net.ParseCIDR(matrixSubnet)
	for _, r := range rows {
		if r.Dst == "default" || r.Dst == "" {
			continue
		}
		ip, n, e := net.ParseCIDR(r.Dst)
		if e != nil {
			ip = net.ParseIP(r.Dst)
			if ip != nil && own.Contains(ip) {
				return fmt.Errorf("mesh IP conflicts with an existing route")
			}
			continue
		}
		if own.Contains(ip) || n.Contains(net.ParseIP(matrixHubIP)) {
			return fmt.Errorf("mesh subnet conflicts with existing route %s", r.Dst)
		}
	}
	if _, e = matrixExec("ip", "link", "show", matrixIfaceName); e == nil {
		return fmt.Errorf("interface %s already exists", matrixIfaceName)
	}
	for _, c := range []struct{ table, chain string }{{"nat", "QMX_IN"}, {"nat", "QMX_OUT"}, {"filter", "QMX_FWD"}, {"mangle", "QMX_MSS"}} {
		if _, err := matrixExec("iptables", "-t", c.table, "-S", c.chain); err == nil {
			return fmt.Errorf("firewall chain %s already exists; refusing to adopt it", c.chain)
		}
	}
	checkPorts := []int{st.Port}
	if st.Role == "hub" {
		checkPorts = append(checkPorts, st.Ports...)
	}
	for _, p := range checkPorts {
		l, e := net.Listen("tcp", net.JoinHostPort("", strconv.Itoa(p)))
		if e != nil {
			return fmt.Errorf("TCP port %d is occupied", p)
		}
		l.Close()
		u, e := net.ListenPacket("udp", net.JoinHostPort("", strconv.Itoa(p)))
		if e != nil {
			return fmt.Errorf("UDP port %d is occupied", p)
		}
		u.Close()
	}
	return nil
}
func matrixPublicKey(host string) (string, error) {
	start := strings.Index(host, "-----BEGIN RSA PUBLIC KEY-----")
	if start < 0 {
		return "", fmt.Errorf("RSA public key missing")
	}
	block, rest := pem.Decode([]byte(host[start:]))
	if block == nil || block.Type != "RSA PUBLIC KEY" || len(strings.TrimSpace(string(rest))) != 0 {
		return "", fmt.Errorf("invalid public key payload")
	}
	k, e := x509.ParsePKCS1PublicKey(block.Bytes)
	if e != nil || k.N.BitLen() < 2048 {
		return "", fmt.Errorf("RSA key must have at least 2048 bits")
	}
	return string(pem.EncodeToMemory(block)), nil
}
func matrixWriteConfig(st matrixState, mainHost string) error {
	if e := os.MkdirAll(filepath.Join(matrixDir(), "hosts"), 0700); e != nil {
		return e
	}
	key, e := rsa.GenerateKey(rand.Reader, 3072)
	if e != nil {
		return e
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	pub := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	conf := "Name = " + st.Name + "\nInterface = " + matrixIfaceName + "\nDevice = /dev/net/tun\nAddressFamily = " + st.Family + "\nPort = " + strconv.Itoa(st.Port) + "\nDirectOnly = yes\nPingInterval = 10\nCipher = aes-256-cbc\nDigest = sha256\n"
	if st.Role == "node" {
		conf += "ConnectTo = main\n"
	}
	self := "Subnet = " + st.IP + "/32\nPort = " + strconv.Itoa(st.Port) + "\nCipher = aes-256-cbc\nDigest = sha256\n"
	if st.Role == "hub" {
		self = "Address = " + st.Addr + "\n" + self
	}
	files := map[string][]byte{"tinc.conf": []byte(conf), "rsa_key.priv": priv, "hosts/" + st.Name: append([]byte(self), pub...)}
	if st.Role == "node" {
		files["hosts/main"] = []byte(mainHost)
	}
	// A /30 creates an on-link route on both ends without touching the default route.
	files["tinc-up"] = []byte("#!/bin/sh\nset -eu\nip address add " + st.IP + "/30 dev \"$INTERFACE\"\nip link set \"$INTERFACE\" mtu 1380 up\n")
	files["tinc-down"] = []byte("#!/bin/sh\nip link set \"$INTERFACE\" down 2>/dev/null || true\n")
	for name, data := range files {
		mode := os.FileMode(0600)
		if name == "tinc-up" || name == "tinc-down" {
			mode = 0700
		}
		if e = os.WriteFile(filepath.Join(matrixDir(), name), data, mode); e != nil {
			return e
		}
	}
	tinc, e := matrixTincPath()
	if e != nil {
		return e
	}
	if strings.ContainsAny(matrixDir(), "\n\r\"") {
		return fmt.Errorf("unsupported configuration directory")
	}
	unit := "[Unit]\nDescription=Quantum Matrix tinc tunnel\nAfter=network-online.target\nWants=network-online.target\n[Service]\nType=simple\nExecStart=" + tinc + " -D -c \"" + matrixDir() + "\" --pidfile=/run/quantum-matrix.pid\nExecStartPost=/bin/sh -c 'if [ -f \"" + matrixDir() + "/forward-up\" ]; then /bin/sh \"" + matrixDir() + "/forward-up\"; fi'\nRestart=on-failure\nRestartSec=3\n[Install]\nWantedBy=multi-user.target\n"
	return os.WriteFile(matrixUnitPath(), []byte(unit), 0644)
}
func matrixServiceStart() error {
	for _, args := range [][]string{{"daemon-reload"}, {"enable", "--now", matrixUnit}} {
		if _, e := matrixExec("systemctl", args...); e != nil {
			return e
		}
	}
	for i := 0; i < 15; i++ {
		if matrixServiceActive() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("Matrix service did not start")
}
func matrixServiceActive() bool {
	_, e := matrixExec("systemctl", "is-active", "--quiet", matrixUnit)
	return e == nil
}
func matrixMeshPing(ip string) bool {
	_, e := matrixExec("ping", "-I", matrixIfaceName, "-c", "1", "-W", "1", ip)
	return e == nil
}
func matrixForwardScript(st matrixState) string {
	// Dedicated chains make deletion exact, even on machines with unrelated NAT rules.
	s := "#!/bin/sh\nset -eu\n" +
		"iptables -t nat -N QMX_IN 2>/dev/null || iptables -t nat -F QMX_IN\n" +
		"iptables -t nat -N QMX_OUT 2>/dev/null || iptables -t nat -F QMX_OUT\n" +
		"iptables -N QMX_FWD 2>/dev/null || iptables -F QMX_FWD\n" +
		"iptables -t mangle -N QMX_MSS 2>/dev/null || iptables -t mangle -F QMX_MSS\n" +
		"iptables -t mangle -A QMX_MSS -o qmx0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1340\n" +
		"iptables -t mangle -A QMX_MSS -i qmx0 -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --set-mss 1340\n" +
		"iptables -t mangle -C FORWARD -m comment --comment quantum-matrix -j QMX_MSS 2>/dev/null || iptables -t mangle -A FORWARD -m comment --comment quantum-matrix -j QMX_MSS\n" +
		"sysctl -w net.ipv4.ip_forward=1 >/dev/null\n"
	for _, p := range st.Ports {
		for _, proto := range []string{"tcp", "udp"} {
			s += fmt.Sprintf("iptables -t nat -A QMX_IN -d %s -p %s --dport %d -j DNAT --to-destination %s\n", st.Addr, proto, p, net.JoinHostPort(matrixFirstPeer, strconv.Itoa(p)))
		}
	}
	s += "iptables -t nat -A QMX_OUT -o " + matrixIfaceName + " -d " + matrixFirstPeer + " -j SNAT --to-source " + matrixHubIP + "\n"
	s += "iptables -A QMX_FWD -i " + matrixIfaceName + " -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT\n"
	for _, p := range st.Ports {
		for _, proto := range []string{"tcp", "udp"} {
			s += fmt.Sprintf("iptables -A QMX_FWD -o %s -d %s -p %s --dport %d -j ACCEPT\n", matrixIfaceName, matrixFirstPeer, proto, p)
		}
	}
	s += "iptables -t nat -C PREROUTING -m comment --comment " + matrixTag + " -j QMX_IN 2>/dev/null || iptables -t nat -A PREROUTING -m comment --comment " + matrixTag + " -j QMX_IN\n" +
		"iptables -t nat -C POSTROUTING -m comment --comment " + matrixTag + " -j QMX_OUT 2>/dev/null || iptables -t nat -A POSTROUTING -m comment --comment " + matrixTag + " -j QMX_OUT\n" +
		"iptables -C FORWARD -m comment --comment " + matrixTag + " -j QMX_FWD 2>/dev/null || iptables -I FORWARD 1 -m comment --comment " + matrixTag + " -j QMX_FWD\n"
	return s
}
func matrixDNATClear() error {
	var errs []string
	for _, r := range []struct{ table, parent, chain string }{{"nat", "PREROUTING", "QMX_IN"}, {"nat", "POSTROUTING", "QMX_OUT"}, {"filter", "FORWARD", "QMX_FWD"}, {"mangle", "FORWARD", "QMX_MSS"}} {
		if _, e := matrixExec("iptables", "-t", r.table, "-S", r.chain); e != nil {
			continue
		}
		for {
			if _, e := matrixExec("iptables", "-t", r.table, "-C", r.parent, "-m", "comment", "--comment", matrixTag, "-j", r.chain); e != nil {
				break
			}
			if _, e := matrixExec("iptables", "-t", r.table, "-D", r.parent, "-m", "comment", "--comment", matrixTag, "-j", r.chain); e != nil {
				errs = append(errs, e.Error())
				break
			}
		}
		for _, op := range []string{"-F", "-X"} {
			if _, e := matrixExec("iptables", "-t", r.table, op, r.chain); e != nil {
				errs = append(errs, e.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("forwarding cleanup failed: %s", strings.Join(errs, "; "))
	}
	return nil
}
func matrixActivateForward(st *matrixState) error {
	if st.Role != "hub" {
		return nil
	}
	// A v6 outer transport still forwards IPv4 user ports using the hub's public IPv4.
	fwd := *st
	if st.Family == "ipv6" {
		fwd.Addr = getOutboundIP()
	}
	if e := matrixDNATClear(); e != nil {
		return e
	}
	if st.ForwardBefore == "" {
		v, e := matrixExec("sysctl", "-n", "net.ipv4.ip_forward")
		if e != nil {
			return e
		}
		st.ForwardBefore = strings.TrimSpace(v)
	}
	if _, e := matrixExec("sysctl", "-w", "net.ipv4.ip_forward=1"); e != nil {
		return e
	}
	script := filepath.Join(matrixDir(), "forward-up")
	if e := os.WriteFile(script, []byte(matrixForwardScript(fwd)), 0700); e != nil {
		return e
	}
	_, e := matrixExec("sh", script)
	if e != nil {
		_ = matrixDNATClear()
	}
	return e
}
func matrixRemove(st matrixState) error {
	if st.Network != matrixNetname {
		return fmt.Errorf("refusing to remove an unowned legacy Matrix network")
	}
	if _, err := os.Stat(matrixUnitPath()); err == nil {
		if _, e := matrixExec("systemctl", "stop", matrixUnit); e != nil {
			return e
		}
		if _, e := matrixExec("systemctl", "disable", matrixUnit); e != nil {
			return e
		}
	}
	if e := matrixDNATClear(); e != nil {
		return e
	}
	if st.ForwardBefore == "0" {
		_, _ = matrixExec("sysctl", "-w", "net.ipv4.ip_forward=0")
	}
	if e := os.Remove(matrixUnitPath()); e != nil && !os.IsNotExist(e) {
		return e
	}
	_, _ = matrixExec("systemctl", "daemon-reload")
	if e := os.RemoveAll(matrixDir()); e != nil {
		return e
	}
	if e := os.Remove(matrixStatePath()); e != nil && !os.IsNotExist(e) {
		return e
	}
	return nil
}
func matrixSetup(st matrixState, mainHost string) error {
	if e := matrixCheckResources(st); e != nil {
		return e
	}
	if e := matrixInstallTinc(); e != nil {
		return e
	}
	// Save ownership before creating resources, so failed setup remains removable.
	st.Network = matrixNetname
	if e := saveMatrix(st); e != nil {
		return e
	}
	if e := matrixWriteConfig(st, mainHost); e != nil {
		_ = matrixRemove(st)
		return e
	}
	if e := matrixServiceStart(); e != nil {
		_ = matrixRemove(st)
		return e
	}
	return nil
}
func parseMatrixBundle(s string) (matrixBundlePayload, error) {
	var b matrixBundlePayload
	if len(s) > 32<<10 || !strings.HasPrefix(strings.TrimSpace(s), matrixBundlePfx) {
		return b, fmt.Errorf("invalid Matrix bundle")
	}
	data, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), matrixBundlePfx))
	if e != nil {
		return b, e
	}
	if e = json.Unmarshal(data, &b); e != nil {
		return b, e
	}
	if b.Network != matrixNetname || b.FIP != matrixFirstPeer || len(b.Token) < 32 {
		return b, fmt.Errorf("unsupported or incomplete Matrix bundle")
	}
	if e = validateMatrixSettings(b.Family, b.Addr, b.Port, b.Ports); e != nil {
		return b, e
	}
	u, e := url.Parse(b.Panel)
	if e != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") || net.ParseIP(u.Hostname()) == nil {
		return b, fmt.Errorf("panel URL must be an explicit HTTP(S) IP endpoint")
	}
	key, e := matrixPublicKey(b.MainHost)
	if e != nil {
		return b, e
	}
	b.MainHost = "Address = " + b.Addr + "\nSubnet = " + matrixHubIP + "/32\nPort = " + strconv.Itoa(b.Port) + "\nCipher = aes-256-cbc\nDigest = sha256\n" + key
	return b, nil
}
func matrixBundle(st matrixState, r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host, _ := os.ReadFile(filepath.Join(matrixDir(), "hosts/main"))
	panelHost := r.Host
	if h, _, err := net.SplitHostPort(panelHost); err == nil {
		if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
			panelHost = net.JoinHostPort(getOutboundIP(), strconv.Itoa(cfg.Port))
		}
	}
	b, _ := json.Marshal(matrixBundlePayload{Panel: scheme + "://" + panelHost + "/" + strings.Trim(cfg.BasePath, "/"), Token: st.Token, Family: st.Family, Addr: st.Addr, Port: st.Port, FIP: matrixFirstPeer, Ports: st.Ports, MainHost: string(host), Network: matrixNetname, Revision: st.Revision})
	return matrixBundlePfx + base64.StdEncoding.EncodeToString(b)
}
func handleMatrixSetupPost(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Family string `json:"family"`
		Port   int    `json:"port"`
		Addr   string `json:"addr"`
		Ports  []int  `json:"ports"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&q); e != nil {
		writeAPIError(w, r, "E-MATRIX-02", "invalid JSON")
		return
	}
	matrixMu.Lock()
	defer matrixMu.Unlock()
	if loadMatrix().Enabled {
		writeAPIError(w, r, "E-MATRIX-01", "")
		return
	}
	q.Family = strings.TrimSpace(q.Family)
	q.Addr = strings.TrimSpace(q.Addr)
	if q.Addr == "" {
		if q.Family == "ipv6" {
			q.Addr = matrixDetectV6()
		} else {
			q.Addr = getOutboundIP()
		}
	}
	if e := validateMatrixSettings(q.Family, q.Addr, q.Port, q.Ports); e != nil {
		writeAPIError(w, r, "E-MATRIX-02", e.Error())
		return
	}
	st := matrixState{Enabled: true, Network: matrixNetname, Role: "hub", Family: q.Family, Port: q.Port, Addr: q.Addr, Token: randHex(32), Ports: q.Ports, Name: "main", IP: matrixHubIP}
	if e := matrixSetup(st, ""); e != nil {
		writeMatrixSetupError(w, r, e)
		return
	}
	LogSecurityAudit("matrix_setup", cfg.Username, ClientIP(r), "family="+st.Family)
	writeJSON(w, map[string]any{"status": "ok", "bundle": matrixBundle(st, r), "state": matrixPublicState(st)})
}
func handleMatrixRegister(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		HostFile string `json:"host_file"`
		Revision string `json:"revision,omitempty"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&q); e != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	matrixMu.Lock()
	defer matrixMu.Unlock()
	st := loadMatrix()
	if !st.Enabled || st.Network != matrixNetname || st.Role != "hub" || st.Token == "" || subtle.ConstantTimeCompare([]byte(q.Token), []byte(st.Token)) != 1 {
		http.Error(w, "registration denied", 403)
		return
	}
	if !matrixNameRe.MatchString(q.Name) || q.Name == "main" {
		http.Error(w, "invalid node name", 400)
		return
	}
	if st.Revision != q.Revision {
		http.Error(w, "bundle revision changed; copy the current hub bundle", http.StatusConflict)
		return
	}
	key, e := matrixPublicKey(q.HostFile)
	if e != nil {
		http.Error(w, "invalid public key", 400)
		return
	}
	hash := sha256.Sum256([]byte(key))
	fingerprint := hex.EncodeToString(hash[:])
	if len(st.Peers) > 0 && (st.Peers[0].Name != q.Name || st.Peers[0].KeyHash != fingerprint) {
		http.Error(w, "hub already paired; remove and recreate to pair another node", 409)
		return
	}
	hf := "Subnet = " + matrixFirstPeer + "/32\nPort = " + strconv.Itoa(st.Port) + "\nCipher = aes-256-cbc\nDigest = sha256\n" + key
	if e = os.WriteFile(filepath.Join(matrixDir(), "hosts", q.Name), []byte(hf), 0600); e != nil {
		writeAPIError(w, r, "E-MATRIX-07", e.Error())
		return
	}
	st.Peers = []matrixPeer{{Name: q.Name, Addr: ClientIP(r), IP: matrixFirstPeer, KeyHash: fingerprint}}
	if e = matrixActivateForward(&st); e != nil {
		writeAPIError(w, r, "E-MATRIX-07", e.Error())
		return
	}
	st.PeerSyncRequired = false
	if e = saveMatrix(st); e != nil {
		writeAPIError(w, r, "E-MATRIX-07", e.Error())
		return
	}
	if _, e = matrixExec("systemctl", "restart", matrixUnit); e != nil {
		writeAPIError(w, r, "E-MATRIX-07", e.Error())
		return
	}
	host, _ := os.ReadFile(filepath.Join(matrixDir(), "hosts/main"))
	writeJSON(w, map[string]string{"status": "ok", "main_host": string(host)})
}
func handleMatrixForeignSetup(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Bundle string `json:"bundle"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 40<<10)).Decode(&q); e != nil {
		writeAPIError(w, r, "E-MATRIX-02", "invalid JSON")
		return
	}
	b, e := parseMatrixBundle(q.Bundle)
	if e != nil {
		writeAPIError(w, r, "E-MATRIX-02", e.Error())
		return
	}
	matrixMu.Lock()
	defer matrixMu.Unlock()
	if loadMatrix().Enabled {
		writeAPIError(w, r, "E-MATRIX-01", "")
		return
	}
	st := matrixState{Enabled: true, Network: matrixNetname, Role: "node", Family: b.Family, Port: b.Port, Addr: b.Addr, Ports: b.Ports, Name: "node" + randHex(8), IP: matrixFirstPeer, Panel: b.Panel}
	if e = matrixSetup(st, b.MainHost); e != nil {
		writeMatrixSetupError(w, r, e)
		return
	}
	self, _ := os.ReadFile(filepath.Join(matrixDir(), "hosts", st.Name))
	payload, _ := json.Marshal(map[string]string{"token": b.Token, "name": st.Name, "host_file": string(self), "revision": b.Revision})
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Post(b.Panel+"/api/matrix/register", "application/json", strings.NewReader(string(payload)))
	if e != nil {
		_ = matrixRemove(st)
		writeAPIError(w, r, "E-MATRIX-08", "hub registration unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		_ = matrixRemove(st)
		writeAPIError(w, r, "E-MATRIX-08", fmt.Sprintf("hub registration returned HTTP %d", resp.StatusCode))
		return
	}
	LogSecurityAudit("matrix_node_setup", cfg.Username, ClientIP(r), "family="+st.Family)
	writeJSON(w, map[string]string{"status": "ok", "detail": "matrix node registered with the hub and started"})
}
func matrixPublicState(st matrixState) map[string]any {
	peers := []map[string]any{}
	active := st.Enabled && st.Network == matrixNetname && matrixServiceActive()
	for _, p := range st.Peers {
		peers = append(peers, map[string]any{"name": p.Name, "addr": p.Addr, "ip": p.IP, "ping_ok": active && matrixMeshPing(p.IP)})
	}
	return map[string]any{"enabled": st.Enabled, "role": st.Role, "family": st.Family, "port": st.Port, "addr": st.Addr, "ports": st.Ports, "service": active, "peers": peers, "name": st.Name, "ip": st.IP, "hub_ping_ok": active && st.Role == "node" && matrixMeshPing(matrixHubIP), "bundle": ""}
}
func handleMatrixStatus(w http.ResponseWriter, r *http.Request) {
	matrixMu.Lock()
	st := loadMatrix()
	bundle := ""
	if st.Enabled && st.Role == "hub" && st.Network == matrixNetname {
		bundle = matrixBundle(st, r)
	}
	matrixMu.Unlock()
	v := matrixPublicState(st)
	v["bundle"] = bundle
	v["peer_sync_required"] = st.PeerSyncRequired
	writeJSON(w, v)
}
func handleMatrixRemovePost(w http.ResponseWriter, r *http.Request) {
	matrixMu.Lock()
	defer matrixMu.Unlock()
	st := loadMatrix()
	if st.Enabled {
		if e := matrixRemove(st); e != nil {
			writeAPIError(w, r, "E-MATRIX-07", e.Error())
			return
		}
	}
	LogSecurityAudit("matrix_remove", cfg.Username, ClientIP(r), "removed owned network")
	writeJSON(w, map[string]string{"status": "ok", "detail": "Matrix removed; unrelated tinc networks and firewall rules preserved"})
}
func handleMatrixRestartPost(w http.ResponseWriter, r *http.Request) {
	matrixMu.Lock()
	defer matrixMu.Unlock()
	st := loadMatrix()
	if !st.Enabled || st.Network != matrixNetname {
		writeAPIError(w, r, "E-MATRIX-01", "no owned Matrix network")
		return
	}
	if _, e := matrixExec("systemctl", "restart", matrixUnit); e != nil {
		writeAPIError(w, r, "E-MATRIX-07", e.Error())
		return
	}
	if st.Role == "hub" && len(st.Peers) > 0 {
		if e := matrixActivateForward(&st); e != nil {
			writeAPIError(w, r, "E-MATRIX-07", e.Error())
			return
		}
	}
	writeJSON(w, map[string]string{"status": "ok"})
}
