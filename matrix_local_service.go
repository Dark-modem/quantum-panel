package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type matrixLocalService struct {
	Port       int `json:"port"`
	TargetPort int `json:"target_port"`
}

// Each Matrix instance owns its own local-service registry file so instances
// never share mutable state.
func matrixLocalPath(in matrixInstance) string {
	return filepath.Join(configDir, "matrix"+in.suffix()+"-local-services.json")
}
func matrixLocalServices(in matrixInstance) ([]matrixLocalService, error) {
	var services []matrixLocalService
	b, e := os.ReadFile(matrixLocalPath(in))
	if os.IsNotExist(e) {
		return services, nil
	}
	if e != nil {
		return nil, e
	}
	e = json.Unmarshal(b, &services)
	return services, e
}
func saveMatrixLocalServices(in matrixInstance, services []matrixLocalService) error {
	if len(services) == 0 {
		e := os.Remove(matrixLocalPath(in))
		if os.IsNotExist(e) {
			return nil
		}
		return e
	}
	b, e := json.Marshal(services)
	if e != nil {
		return e
	}
	f, e := os.CreateTemp(configDir, ".matrix-local-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), matrixLocalPath(in))
}
func matrixLocalUnit(in matrixInstance, port int) string {
	return "quantum-matrix" + in.suffix() + "-local-" + strconv.Itoa(port) + ".service"
}
func matrixRemoveLocalService(in matrixInstance, port int) error {
	unit := matrixLocalUnit(in, port)
	path := filepath.Join(matrixUnitDir, unit)
	if _, e := os.Stat(path); os.IsNotExist(e) {
		return nil
	} else if e != nil {
		return e
	}
	if _, e := matrixExec("systemctl", "stop", unit); e != nil {
		return e
	}
	if _, e := matrixExec("systemctl", "disable", unit); e != nil {
		return e
	}
	return os.Remove(path)
}
func matrixRemoveLocalServices(in matrixInstance) error {
	services, e := matrixLocalServices(in)
	if e != nil {
		return e
	}
	for _, s := range services {
		if e = matrixRemoveLocalService(in, s.Port); e != nil {
			return e
		}
	}
	if e = saveMatrixLocalServices(in, nil); e != nil {
		return e
	}
	_, e = matrixExec("systemctl", "daemon-reload")
	return e
}
func matrixConfigureLocalService(in matrixInstance, q matrixLocalService, remove bool) error {
	st := loadMatrixInstance(in)
	if !st.Enabled || st.Network != in.network() || st.Role != "node" {
		return fmt.Errorf("connect a local service on the foreign Matrix node, not the Iran hub")
	}
	if q.Port < 1 || q.Port > 65535 || q.TargetPort < 1 || q.TargetPort > 65535 || q.Port == 22 || q.TargetPort == 22 || q.Port == cfg.Port || q.TargetPort == cfg.Port {
		return fmt.Errorf("valid service ports are required; SSH and panel ports are excluded")
	}
	found := false
	for _, p := range st.Ports {
		if p == q.Port {
			found = true
		}
	}
	if !found && !remove {
		return fmt.Errorf("the user port must be present in the Matrix forwarded-port list")
	}
	services, e := matrixLocalServices(in)
	if e != nil {
		return e
	}
	idx := -1
	for i, s := range services {
		if s.Port == q.Port {
			idx = i
		}
	}
	if remove {
		if idx < 0 {
			return nil
		}
		if e = matrixRemoveLocalService(in, q.Port); e != nil {
			return e
		}
		services = append(services[:idx], services[idx+1:]...)
		if e = saveMatrixLocalServices(in, services); e != nil {
			return e
		}
		_, e = matrixExec("systemctl", "daemon-reload")
		return e
	}
	// Fail before any service changes when the destination does not listen.
	conn, e := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(q.TargetPort)), 2*time.Second)
	if e != nil {
		return fmt.Errorf("local destination 127.0.0.1:%d is not accepting TCP connections", q.TargetPort)
	}
	conn.Close()
	if idx < 0 {
		unitPath := filepath.Join(matrixUnitDir, matrixLocalUnit(in, q.Port))
		if _, e = os.Lstat(unitPath); e == nil {
			return fmt.Errorf("service unit already exists; refusing to overwrite")
		}
		ln, e := net.Listen("tcp4", net.JoinHostPort(in.firstPeer(), strconv.Itoa(q.Port)))
		if e != nil {
			return fmt.Errorf("Matrix mesh port %d is occupied; service may already listen there", q.Port)
		}
		ln.Close()
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	if strings.ContainsAny(exe, "\n\r\"") {
		return fmt.Errorf("invalid executable path")
	}
	unit := matrixLocalUnit(in, q.Port)
	unitBody := "[Unit]\nDescription=Quantum Matrix local TCP service bridge\nRequires=" + in.unit() + "\nAfter=" + in.unit() + "\nPartOf=" + in.unit() + "\n[Service]\nType=simple\nExecStart=\"" + exe + "\" matrix-local-proxy " + strconv.Itoa(q.Port) + " " + strconv.Itoa(q.TargetPort) + "\nRestart=on-failure\nRestartSec=2\nNoNewPrivileges=true\n[Install]\nWantedBy=" + in.unit() + "\n"
	if idx < 0 {
		services = append(services, q)
	} else {
		services[idx] = q
	}
	if e = saveMatrixLocalServices(in, services); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(matrixUnitDir, unit), []byte(unitBody), 0644); e != nil {
		return e
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", unit}, {"restart", unit}} {
		if _, e = matrixExec("systemctl", args...); e != nil {
			return e
		}
	}
	for i := 0; i < 15; i++ {
		conn, e = net.DialTimeout("tcp", net.JoinHostPort(in.firstPeer(), strconv.Itoa(q.Port)), time.Second)
		if e == nil {
			conn.Close()
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("local bridge did not start; inspect its service log")
}
func handleMatrixLocalService(w http.ResponseWriter, r *http.Request) {
	matrixMu.Lock()
	defer matrixMu.Unlock()
	in, e := matrixResolveInstance(strings.TrimSpace(r.URL.Query().Get("instance")))
	if e != nil {
		writeAPIError(w, r, "E-MATRIX-02", e.Error())
		return
	}
	if r.Method == http.MethodGet {
		services, e := matrixLocalServices(in)
		if e != nil {
			writeAPIError(w, r, "E-MATRIX-07", e.Error())
			return
		}
		if services == nil {
			services = []matrixLocalService{}
		}
		writeJSON(w, map[string]any{"services": services})
		return
	}
	var q struct {
		Port       int  `json:"port"`
		TargetPort int  `json:"target_port"`
		Remove     bool `json:"remove"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&q); e != nil {
		writeAPIError(w, r, "E-MATRIX-02", "invalid JSON")
		return
	}
	if q.Remove && q.TargetPort == 0 {
		q.TargetPort = q.Port
	}
	if e := matrixConfigureLocalService(in, matrixLocalService{Port: q.Port, TargetPort: q.TargetPort}, q.Remove); e != nil {
		writeAPIError(w, r, "E-MATRIX-07", e.Error())
		return
	}
	LogSecurityAudit("matrix_local_service", cfg.Username, ClientIP(r), fmt.Sprintf("port=%d target_port=%d removed=%t", q.Port, q.TargetPort, q.Remove))
	writeJSON(w, map[string]any{"status": "ok", "protocol": "tcp", "port": q.Port, "target": "127.0.0.1:" + strconv.Itoa(q.TargetPort)})
}

// The bridge passes bytes unchanged; TLS/Reality authentication stays in Xray.
func matrixCopyConnection(client net.Conn, target string) {
	defer client.Close()
	remote, e := net.DialTimeout("tcp", target, 5*time.Second)
	if e != nil {
		return
	}
	defer remote.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	cp := func(dst, src net.Conn) {
		defer wg.Done()
		_, _ = io.Copy(dst, src)
		if t, ok := dst.(*net.TCPConn); ok {
			_ = t.CloseWrite()
		}
	}
	go cp(remote, client)
	go cp(client, remote)
	wg.Wait()
}
func matrixLocalProxyCmd(args []string) int {
	if len(args) != 2 {
		return 2
	}
	port, e := strconv.Atoi(args[0])
	if e != nil || port < 1 || port > 65535 {
		return 2
	}
	target, e := strconv.Atoi(args[1])
	if e != nil || target < 1 || target > 65535 {
		return 2
	}
	ln, e := net.Listen("tcp4", net.JoinHostPort(matrixFirstPeer, strconv.Itoa(port)))
	if e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	defer ln.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		client, e := ln.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return 0
			}
			return 1
		}
		go matrixCopyConnection(client, net.JoinHostPort("127.0.0.1", strconv.Itoa(target)))
	}
}
