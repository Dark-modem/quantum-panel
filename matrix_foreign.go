package main

// Matrix node side (a foreign server): parses the hmx1_ bundle from the hub's
// panel, builds the local tinc node, and registers its host file with the hub
// over the one-time token. Removal clears everything this side created.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type matrixBundlePayload struct {
	Panel  string `json:"panel"`
	Token  string `json:"token"`
	Family string `json:"family"`
	Addr   string `json:"addr"`
	Port   int    `json:"port"`
	FIP    string `json:"fip"`
	Ports  []int  `json:"ports"`
}

func parseMatrixBundle(s string) (matrixBundlePayload, error) {
	var b matrixBundlePayload
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, matrixBundlePfx) {
		return b, fmt.Errorf("the bundle must start with %s", matrixBundlePfx)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, matrixBundlePfx))
	if err != nil {
		return b, fmt.Errorf("the bundle is not valid base64")
	}
	if err := json.Unmarshal(raw, &b); err != nil {
		return b, fmt.Errorf("the bundle payload is not valid")
	}
	if b.Panel == "" || b.Token == "" || (b.Family != "ipv4" && b.Family != "ipv6") || b.FIP == "" || b.Port < 22 || b.Port > 65535 {
		return b, fmt.Errorf("the bundle payload is incomplete")
	}
	return b, nil
}

func handleMatrixForeignSetup(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Bundle string `json:"bundle"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeAPIError(w, r, "E-ACTION-01", "invalid json")
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if loadMatrix().Enabled {
		writeAPIError(w, r, "E-MATRIX-01", "matrix tunnel is already set up on this server — remove it first")
		return
	}
	b, err := parseMatrixBundle(req.Bundle)
	if err != nil {
		writeAPIError(w, r, "E-MATRIX-02", err.Error())
		return
	}
	if err := matrixInstallTinc(); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", err.Error())
		return
	}
	name, err := matrixWriteNode(b.Family, b.Port)
	if err != nil {
		writeAPIError(w, r, "E-MATRIX-07", err.Error())
		return
	}
	if out, err := runMatrixCmd("tincd", "-n", matrixNetname, "-K4096"); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", "tincd keygen: "+err.Error()+": "+trimLen(out, 200))
		return
	}
	// the node's own host file now carries its public key
	self, err := os.ReadFile(matrixConfDir + "/hosts/" + name)
	if err != nil {
		writeAPIError(w, r, "E-MATRIX-07", "read own host file: "+err.Error())
		return
	}
	// register with the hub: host file in, hub host file out
	body, _ := json.Marshal(map[string]string{"token": b.Token, "name": name, "host_file": string(self)})
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(strings.TrimRight(b.Panel, "/")+"/api/matrix/register", "application/json", bytes.NewReader(body))
	if err != nil {
		writeAPIError(w, r, "E-MATRIX-08", "cannot reach the hub panel: "+err.Error())
		return
	}
	defer resp.Body.Close()
	var reg struct {
		Status   string `json:"status"`
		MainHost string `json:"main_host"`
		Error    string `json:"error"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err := json.Unmarshal(data, &reg); err != nil || reg.Error != "" || reg.MainHost == "" {
		writeAPIError(w, r, "E-MATRIX-08", "hub registration failed: "+reg.Error)
		return
	}
	if err := os.WriteFile(matrixConfDir+"/hosts/main", []byte(reg.MainHost), 0644); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", err.Error())
		return
	}
	if err := matrixServiceStart(); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", "tinc@matrix start: "+err.Error())
		return
	}
	st := matrixState{Enabled: true, Role: "node", Family: b.Family, Name: name,
		Panel: b.Panel, Token: b.Token, Addr: b.Addr, Port: b.Port, IP: b.FIP, Ports: b.Ports}
	if err := saveMatrix(st); err != nil {
		writeAPIError(w, r, "E-MATRIX-07", err.Error())
		return
	}
	LogSecurityAudit("matrix_node_setup", cfg.Username, ClientIP(r), "hub="+b.Addr+" family="+b.Family)
	writeJSON(w, map[string]string{"status": "ok", "detail": "matrix node registered with the hub and started"})
}

func handleMatrixForeignRemove(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	_ = exec.Command("systemctl", "stop", "tinc@"+matrixNetname).Run()
	_ = exec.Command("systemctl", "disable", "tinc@"+matrixNetname).Run()
	_ = os.RemoveAll(matrixConfDir)
	clearMatrixState()
	LogSecurityAudit("matrix_node_remove", cfg.Username, ClientIP(r), "removed")
	writeJSON(w, map[string]string{"status": "ok", "detail": "matrix node removed — configs and service cleared"})
}

// matrixWriteNode builds /etc/tinc/matrix for a foreign node: tinc.conf with
// ConnectTo = main, and the up/down scripts for the node's mesh address. The
// host files are filled around key generation (own key locally, the hub's
// host file arrives with the registration answer).
func matrixWriteNode(family string, port int) (string, error) {
	name := "matrix-f" + randHex(4)
	if err := os.MkdirAll(filepath.Join(matrixConfDir, "hosts"), 0755); err != nil {
		return "", err
	}
	conf := strings.Join([]string{
		"Name = " + name,
		"Device = /dev/net/tun",
		"AddressFamily = " + family,
		"DirectOnly = yes",
		"ConnectTo = main",
		"AutoConnect = yes",
		"PingInterval = 10",
		"ProcessPriority = high",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(matrixConfDir, "tinc.conf"), []byte(conf), 0600); err != nil {
		return "", err
	}
	up := strings.Join([]string{
		"#!/bin/bash",
		"/sbin/ip link set $INTERFACE up",
		"/sbin/ip addr add " + matrixFirstPeer + "/32 dev $INTERFACE",
		"",
	}, "\n")
	down := strings.Join([]string{
		"#!/bin/bash",
		"/sbin/ip addr del " + matrixFirstPeer + "/32 dev $INTERFACE 2>/dev/null || true",
		"/sbin/ip link set $INTERFACE down",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(matrixConfDir, "tinc-up"), []byte(up), 0755); err != nil {
		return "", err
	}
	return name, os.WriteFile(filepath.Join(matrixConfDir, "tinc-down"), []byte(down), 0755)
}
