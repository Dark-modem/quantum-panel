package main

// Matrix tunnel (tinc mesh, netname "matrix"): a server-to-server encrypted
// private network between this panel's server and its peer, running beside
// the main tunnel. Two families: ipv4 (tinc transport over IPv4) and ipv6
// (transport over IPv6); user forwarding is IPv4 DNAT to the peer's mesh
// address either way, matching the upstream matrix-network tool.
//
// Hub (Iran)  = tinc "main" node: listens on the tinc port, holds every
//               peer's host file, owns the DNAT rules for forwarded ports.
// Node (foreign) = dials the hub (ConnectTo = main), gets a fixed mesh
//               address (172.16.1.2 for the first peer).
//
// Registration: the foreign panel parses the hmx1_ bundle, builds its node,
// then POSTs its host file (public key) to /api/matrix/register with the
// one-time token from the bundle; the hub answers with its own host file.
// Everything created is tagged: iptables rules carry the "hashem-matrix"
// comment, configs live in /etc/tinc/matrix, state in <configDir>/matrix.json.

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	matrixNetname   = "matrix"
	matrixConfDir   = "/etc/tinc/matrix"
	matrixTag       = "hashem-matrix"
	matrixHubIP     = "172.16.1.1"
	matrixFirstPeer = "172.16.1.2"
	matrixBundlePfx = "hmx1_"
)

type matrixPeer struct {
	Name     string `json:"name"`
	Addr     string `json:"addr"` // public address seen at registration
	HostFile string `json:"-"`    // hosts/<name> content
	IP       string `json:"ip"`   // mesh address (172.16.1.x)
}

// One state file for both roles: the hub (Iran) keeps the token, forwarded
// ports and the registered peers; a node (foreign) keeps its name and the
// hub's panel URL + transport address.
type matrixState struct {
	Enabled bool         `json:"enabled"`
	Role    string       `json:"role"` // "hub" | "node"
	Family  string       `json:"family"`
	Port    int          `json:"port"`
	Addr    string       `json:"addr"` // hub transport address (v4 or v6)
	Token   string       `json:"token"`
	Ports   []int        `json:"ports"`
	Peers   []matrixPeer `json:"peers"`
	Name    string       `json:"name"`  // node-side: local node name
	Panel   string       `json:"panel"` // node-side: hub panel base URL
	IP      string       `json:"ip"`    // node-side: local mesh address
}

var matrixMu sync.Mutex

func matrixStatePath() string { return filepath.Join(configDir, "matrix.json") }

func loadMatrix() matrixState {
	st := matrixState{}
	if data, err := os.ReadFile(matrixStatePath()); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func saveMatrix(st matrixState) error {
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(matrixStatePath(), append(data, '\n'), 0600)
}

func clearMatrixState() { _ = os.Remove(matrixStatePath()) }

func runMatrixCmd(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// matrixInstallTinc installs the tinc package when missing (both roles).
func matrixInstallTinc() error {
	if _, err := exec.LookPath("tincd"); err == nil {
		return nil
	}
	if _, err := exec.LookPath("apt-get"); err != nil {
		return fmt.Errorf("tinc is not installed and apt-get is unavailable")
	}
	if out, err := runMatrixCmd("apt-get", "update"); err != nil {
		return fmt.Errorf("apt-get update: %v: %s", err, trimLen(out, 200))
	}
	if out, err := runMatrixCmd("apt-get", "install", "-y", "tinc"); err != nil {
		return fmt.Errorf("apt-get install tinc: %v: %s", err, trimLen(out, 200))
	}
	return nil
}

// matrixWriteHub builds /etc/tinc/matrix for the hub (main node).
func matrixWriteHub(family, addr string, port int) error {
	if err := os.MkdirAll(filepath.Join(matrixConfDir, "hosts"), 0755); err != nil {
		return err
	}
	conf := strings.Join([]string{
		"Name = main",
		"Device = /dev/net/tun",
		"AddressFamily = " + family,
		"DirectOnly = yes",
		"AutoConnect = yes",
		"PingInterval = 10",
		"ProcessPriority = high",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(matrixConfDir, "tinc.conf"), []byte(conf), 0600); err != nil {
		return err
	}
	host := strings.Join([]string{
		"Address = " + addr,
		"Subnet = " + matrixHubIP + "/32",
		fmt.Sprintf("Port = %d", port),
		"Cipher = aes-128-cbc",
		"Digest = md5",
		"PTMU = 1514",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(matrixConfDir, "hosts", "main"), []byte(host), 0644); err != nil {
		return err
	}
	up := strings.Join([]string{
		"#!/bin/bash",
		"/sbin/ip link set $INTERFACE up",
		"/sbin/ip addr add " + matrixHubIP + "/32 dev $INTERFACE",
		"/sbin/ip route add 172.16.1.0/24 dev $INTERFACE",
		"",
	}, "\n")
	down := strings.Join([]string{
		"#!/bin/bash",
		"/sbin/ip route del 172.16.1.0/24 dev $INTERFACE 2>/dev/null || true",
		"/sbin/ip addr del " + matrixHubIP + "/32 dev $INTERFACE 2>/dev/null || true",
		"/sbin/ip link set $INTERFACE down",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(matrixConfDir, "tinc-up"), []byte(up), 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(matrixConfDir, "tinc-down"), []byte(down), 0755)
}

// matrixDNATAdd / matrixDNATClear manage the forwarding rules; every rule
// carries the hashem-matrix comment so removal is exact.
func matrixDNATAdd(ports []int, peerIP string) {
	iface := strings.TrimSpace(matrixIface())
	for _, p := range ports {
		dest := net.JoinHostPort(peerIP, fmt.Sprint(p))
		for _, proto := range []string{"tcp", "udp"} {
			_ = exec.Command("iptables", "-t", "nat", "-A", "PREROUTING", "-p", proto, "--dport",
				fmt.Sprint(p), "-j", "DNAT", "--to-destination", dest,
				"-m", "comment", "--comment", matrixTag).Run()
		}
	}
	if iface != "" {
		_ = exec.Command("iptables", "-t", "nat", "-A", "POSTROUTING", "-s", "172.16.1.0/24", "-o", iface,
			"-j", "MASQUERADE", "-m", "comment", "--comment", matrixTag).Run()
	}
	_ = os.WriteFile("/etc/sysctl.d/99-hashem-matrix.conf", []byte("net.ipv4.ip_forward=1\n"), 0644)
	_ = exec.Command("sysctl", "-w", "net.ipv4.ip_forward=1").Run()
}

func matrixIface() string {
	out, err := exec.Command("ip", "route", "show", "default").CombinedOutput()
	for _, f := range strings.Fields(string(out)) {
		if f != "default" && f != "via" && f != "dev" && !strings.Contains(f, ".") && !strings.Contains(f, ":") {
			return f
		}
	}
	if err != nil {
		return ""
	}
	return ""
}

func matrixDNATClear() {
	for _, c := range []string{"PREROUTING", "POSTROUTING"} {
		out, _ := exec.Command("iptables", "-t", "nat", "-S", c).CombinedOutput()
		var rules [][]string
		for _, line := range strings.Split(string(out), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "-A "+c+" ") && strings.Contains(line, matrixTag) {
				rules = append(rules, strings.Fields(line))
			}
		}
		// delete in reverse so the remaining -A positions stay valid
		for i := len(rules) - 1; i >= 0; i-- {
			f := rules[i]
			f[0] = "-D"
			_ = exec.Command("iptables", append([]string{"-t", "nat"}, f...)...).Run()
		}
	}
	_ = os.Remove("/etc/sysctl.d/99-hashem-matrix.conf")
}

func matrixServiceStart() error {
	_ = exec.Command("systemctl", "enable", "tinc@"+matrixNetname).Run()
	return exec.Command("systemctl", "restart", "tinc@"+matrixNetname).Run()
}

func matrixServiceActive() bool {
	out, err := exec.Command("systemctl", "is-active", "--quiet", "tinc@"+matrixNetname).CombinedOutput()
	_ = out
	return err == nil
}

func matrixMeshPing(ip string) bool {
	return exec.Command("ping", "-c", "2", "-i", "0.3", "-W", "2", ip).Run() == nil
}

// matrixDetectV6 returns this server's first global IPv6 address ("" if none).
func matrixDetectV6() string {
	out, err := exec.Command("ip", "-6", "addr", "show", "scope", "global").CombinedOutput()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "inet6 ") {
			continue
		}
		ip := strings.Fields(line)[1]
		if strings.HasPrefix(ip, "fe80") || strings.Contains(ip, "%") {
			continue
		}
		return strings.Split(ip, "/")[0]
	}
	return ""
}

// ---- handlers ----

var matrixNameRe = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func handleMatrixSetupPost(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Family string `json:"family"`
		Port   int    `json:"port"`
		Addr   string `json:"addr"`
		Ports  []int  `json:"ports"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&req); err != nil {
		writeAPIError(w, r, "E-ACTION-01", "invalid json")
		return
	}
	mu.Lock()
	defer mu.Unlock()
	st := loadMatrix()
	if st.Enabled {
		writeAPIError(w, r, "E-MATRIX-01", "matrix tunnel is already set up on this server — remove it first")
		return
	}
	family := strings.ToLower(strings.TrimSpace(req.Family))
	if family != "ipv4" && family != "ipv6" {
		writeAPIError(w, r, "E-MATRIX-02", "family must be ipv4 or ipv6")
		return
	}
	if req.Port < 22 || req.Port > 65535 {
		writeAPIError(w, r, "E-MATRIX-03", "tinc port must be 22-65535")
		return
	}
	ports := req.Ports
	if len(ports) > 20 {
		ports = ports[:20]
	}
	for _, p := range ports {
		if p < 1 || p > 65535 {
			writeAPIError(w, r, "E-MATRIX-04", "forwarded ports must be 1-65535")
			return
		}
	}
	addr := strings.TrimSpace(req.Addr)
	if family == "ipv6" {
		if addr == "" {
			addr = matrixDetectV6()
		}
		if addr == "" {
			writeAPIError(w, r, "E-MATRIX-05", "no global IPv6 address found on this server — enter one manually")
			return
		}
		if net.ParseIP(addr) == nil || !strings.Contains(addr, ":") {
			writeAPIError(w, r, "E-MATRIX-06", "the IPv6 address is not valid")
			return
		}
	} else if addr == "" {
		addr = getOutboundIP()
	}
	if err := matrixInstallTinc(); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", err.Error())
		return
	}
	if err := matrixWriteHub(family, addr, req.Port); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", err.Error())
		return
	}
	if out, err := runMatrixCmd("tincd", "-n", matrixNetname, "-K4096"); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", "tincd keygen: "+err.Error()+": "+trimLen(out, 200))
		return
	}
	if err := matrixServiceStart(); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", "tinc@matrix start: "+err.Error())
		return
	}
	token := randHex(24)
	st = matrixState{Enabled: true, Role: "hub", Family: family, Port: req.Port, Addr: addr, Token: token, Ports: ports}
	if err := saveMatrix(st); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", err.Error())
		return
	}
	LogSecurityAudit("matrix_setup", cfg.Username, ClientIP(r), "family="+family+" port="+fmt.Sprint(req.Port))
	writeJSON(w, map[string]any{"status": "ok", "bundle": matrixBundle(st, r), "state": matrixPublicState(st)})
}

// matrixBundle builds the hmx1_ payload the foreign panel pastes.
func matrixBundle(st matrixState, r *http.Request) string {
	scheme := "http"
	if r != nil && r.TLS != nil {
		scheme = "https"
	}
	host := ""
	if r != nil {
		host = r.Host
	}
	payload, _ := json.Marshal(map[string]any{
		"panel":  scheme + "://" + host + "/" + strings.Trim(cfg.BasePath, "/"),
		"token":  st.Token,
		"family": st.Family,
		"addr":   st.Addr,
		"port":   st.Port,
		"fip":    matrixFirstPeer,
		"ports":  st.Ports,
	})
	return matrixBundlePfx + base64.StdEncoding.EncodeToString(payload)
}

// handleMatrixRegister is called server-to-server by the foreign panel with
// the one-time token from the bundle: it exchanges host files and opens the
// DNAT rules for the forwarded ports.
func handleMatrixRegister(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		HostFile string `json:"host_file"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10)).Decode(&req); err != nil {
		writeAPIError(w, r, "E-ACTION-01", "invalid json")
		return
	}
	mu.Lock()
	defer mu.Unlock()
	st := loadMatrix()
	if !st.Enabled || st.Role != "hub" {
		writeJSON(w, map[string]string{"error": "matrix hub is not set up on this server"})
		return
	}
	if subtle.ConstantTimeCompare([]byte(strings.TrimSpace(req.Token)), []byte(st.Token)) != 1 {
		writeJSON(w, map[string]string{"error": "bad matrix token"})
		return
	}
	name := strings.ToLower(strings.TrimSpace(req.Name))
	if !matrixNameRe.MatchString(name) || name == "main" {
		writeJSON(w, map[string]string{"error": "invalid node name"})
		return
	}
	hf := strings.TrimSpace(req.HostFile)
	if len(hf) < 64 || len(hf) > 16<<10 || !strings.Contains(hf, "PUBLIC KEY") || strings.Contains(hf, "PRIVATE KEY") {
		writeJSON(w, map[string]string{"error": "host file must contain the tinc public key only"})
		return
	}
	peerAddr := ClientIP(r)
	if host, _, err := net.SplitHostPort(peerAddr); err == nil {
		peerAddr = host
	}
	hostFile := "Address = " + peerAddr + "\nSubnet = " + matrixFirstPeer + "/32\n" +
		fmt.Sprintf("Port = %d\n", st.Port) + hf + "\n"
	if err := os.WriteFile(filepath.Join(matrixConfDir, "hosts", name), []byte(hostFile), 0644); err != nil {
		writeJSON(w, map[string]string{"error": err.Error()})
		return
	}
	mainConf, _ := os.ReadFile(filepath.Join(matrixConfDir, "tinc.conf"))
	if !strings.Contains(string(mainConf), "ConnectTo = "+name) {
		_ = os.WriteFile(filepath.Join(matrixConfDir, "tinc.conf"),
			append(bytes.TrimRight(mainConf, "\n"), []byte("\nConnectTo = "+name+"\n")...), 0600)
	}
	_ = exec.Command("systemctl", "restart", "tinc@"+matrixNetname).Run()
	matrixDNATAdd(st.Ports, matrixFirstPeer)
	mainHost, _ := os.ReadFile(filepath.Join(matrixConfDir, "hosts", "main"))
	replaced := false
	for i := range st.Peers {
		if st.Peers[i].Name == name {
			st.Peers[i] = matrixPeer{Name: name, Addr: peerAddr, HostFile: hostFile, IP: matrixFirstPeer}
			replaced = true
		}
	}
	if !replaced {
		st.Peers = append(st.Peers, matrixPeer{Name: name, Addr: peerAddr, HostFile: hostFile, IP: matrixFirstPeer})
	}
	_ = saveMatrix(st)
	LogSecurityAudit("matrix_register", "matrix-node", peerAddr, "name="+name)
	writeJSON(w, map[string]string{"status": "ok", "main_host": string(mainHost)})
}

func matrixPublicState(st matrixState) map[string]any {
	peers := []map[string]any{}
	for _, p := range st.Peers {
		peers = append(peers, map[string]any{"name": p.Name, "addr": p.Addr, "ip": p.IP,
			"ping_ok": matrixMeshPing(p.IP)})
	}
	out := map[string]any{
		"enabled": st.Enabled, "role": st.Role, "family": st.Family, "port": st.Port,
		"addr": st.Addr, "ports": st.Ports, "service": matrixServiceActive(), "peers": peers,
		"name": st.Name, "ip": st.IP, "bundle": "",
	}
	if st.Role == "node" && st.IP != "" {
		out["hub_ping_ok"] = matrixMeshPing(matrixHubIP)
	}
	return out
}

func handleMatrixStatus(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	st := loadMatrix()
	out := matrixPublicState(st)
	if st.Enabled && st.Role == "hub" {
		out["bundle"] = matrixBundle(st, r)
	}
	writeJSON(w, out)
}

func handleMatrixRemovePost(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	st := loadMatrix()
	_ = exec.Command("systemctl", "stop", "tinc@"+matrixNetname).Run()
	_ = exec.Command("systemctl", "disable", "tinc@"+matrixNetname).Run()
	matrixDNATClear()
	_ = os.RemoveAll(matrixConfDir)
	clearMatrixState()
	LogSecurityAudit("matrix_remove", cfg.Username, ClientIP(r), "role="+st.Role)
	writeJSON(w, map[string]string{"status": "ok", "detail": "matrix removed — configs, service and forwarding rules cleared"})
}

func handleMatrixRestartPost(w http.ResponseWriter, r *http.Request) {
	if err := matrixServiceStart(); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", err.Error())
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}
