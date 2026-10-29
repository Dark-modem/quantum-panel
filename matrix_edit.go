package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type matrixEditRequest struct {
	Ports  *[]int `json:"ports,omitempty"`
	Family string `json:"family,omitempty"`
	Addr   string `json:"addr,omitempty"`
	Bundle string `json:"bundle,omitempty"`
}

func matrixSetDirective(data []byte, key, value string) []byte {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := make([]string, 0, len(lines)+1)
	found := false
	for _, line := range lines {
		k, _, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(k) == key {
			if !found {
				out = append(out, key+" = "+value)
				found = true
			}
			continue
		}
		out = append(out, line)
	}
	if !found {
		out = append(out, key+" = "+value)
	}
	return []byte(strings.Join(out, "\n") + "\n")
}

func matrixEditPortConflicts(old, next []int) error {
	existing := map[int]bool{}
	for _, p := range old {
		existing[p] = true
	}
	for _, p := range next {
		if existing[p] {
			continue
		}
		port := net.JoinHostPort("", strconv.Itoa(p))
		ln, e := net.Listen("tcp4", port)
		if e != nil {
			return fmt.Errorf("new TCP user port %d is occupied", p)
		}
		ln.Close()
		udp, e := net.ListenPacket("udp4", port)
		if e != nil {
			return fmt.Errorf("new UDP user port %d is occupied", p)
		}
		udp.Close()
	}
	return nil
}

// Back up only mutable Matrix-owned files. Keys and interface addressing are
// never rewritten by an edit, including when changing the outer IP family.
func matrixEditCommit(old, next matrixState, mainHost []byte) error {
	names := []string{"tinc.conf", "hosts/" + old.Name, "forward-up"}
	if old.Role == "node" {
		names = append(names, "hosts/main")
	}
	backup := map[string][]byte{}
	absent := map[string]bool{}
	for _, name := range names {
		b, e := os.ReadFile(filepath.Join(matrixDir(), name))
		if os.IsNotExist(e) {
			absent[name] = true
			continue
		}
		if e != nil {
			return e
		}
		backup[name] = b
	}
	if len(backup["tinc.conf"]) == 0 {
		return fmt.Errorf("Matrix config is missing")
	}
	rollback := func(err error) error {
		for _, name := range names {
			path := filepath.Join(matrixDir(), name)
			if absent[name] {
				_ = os.Remove(path)
			} else {
				_ = os.WriteFile(path, backup[name], 0600)
			}
		}
		_ = saveMatrix(old)
		if old.Role == "hub" && len(old.Peers) > 0 {
			_ = matrixActivateForward(&old)
		}
		_, _ = matrixExec("systemctl", "restart", matrixUnit)
		return fmt.Errorf("Matrix edit failed; previous configuration restored: %w", err)
	}
	conf := matrixSetDirective(backup["tinc.conf"], "AddressFamily", next.Family)
	conf = matrixSetDirective(conf, "Port", strconv.Itoa(next.Port))
	if e := os.WriteFile(filepath.Join(matrixDir(), "tinc.conf"), conf, 0600); e != nil {
		return rollback(e)
	}
	self := backup["hosts/"+old.Name]
	if old.Role == "hub" {
		self = matrixSetDirective(self, "Address", next.Addr)
	}
	if e := os.WriteFile(filepath.Join(matrixDir(), "hosts", old.Name), self, 0600); e != nil {
		return rollback(e)
	}
	if old.Role == "node" {
		if e := os.WriteFile(filepath.Join(matrixDir(), "hosts/main"), mainHost, 0600); e != nil {
			return rollback(e)
		}
	}
	if old.Role == "hub" && len(next.Peers) > 0 {
		if e := matrixActivateForward(&next); e != nil {
			return rollback(e)
		}
	}
	if e := saveMatrix(next); e != nil {
		return rollback(e)
	}
	// User port-only edits do not restart the transport or disconnect sessions.
	if old.Family != next.Family || old.Addr != next.Addr || old.Port != next.Port {
		if _, e := matrixExec("systemctl", "restart", matrixUnit); e != nil {
			return rollback(e)
		}
		started := false
		for attempt := 0; attempt < 15; attempt++ {
			if matrixServiceActive() {
				started = true
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if !started {
			return rollback(fmt.Errorf("service did not start"))
		}
	}
	return nil
}

func matrixApplyEdit(old matrixState, q matrixEditRequest) (matrixState, error) {
	next := old
	if !old.Enabled || old.Network != matrixNetname {
		return old, fmt.Errorf("no owned Matrix tunnel to edit")
	}
	if old.Role != "hub" {
		return old, fmt.Errorf("edit on the Iran hub, then apply its updated bundle on this node")
	}
	if q.Family != "" {
		next.Family = strings.TrimSpace(q.Family)
	}
	if q.Ports != nil {
		next.Ports = append([]int(nil), (*q.Ports)...)
	}
	if q.Addr != "" {
		next.Addr = strings.TrimSpace(q.Addr)
	} else if next.Family != old.Family {
		if next.Family == "ipv6" {
			next.Addr = matrixDetectV6()
		} else {
			next.Addr = getOutboundIP()
		}
	}
	if e := validateMatrixSettings(next.Family, next.Addr, next.Port, next.Ports); e != nil {
		return old, e
	}
	if e := matrixEditPortConflicts(old.Ports, next.Ports); e != nil {
		return old, e
	}
	if next.Family != old.Family || next.Addr != old.Addr {
		next.Revision = randHex(16)
		next.PeerSyncRequired = len(old.Peers) > 0
	}
	if e := matrixEditCommit(old, next, nil); e != nil {
		return old, e
	}
	return next, nil
}

func matrixApplyNodeBundle(old matrixState, b matrixBundlePayload) error {
	if old.Role != "node" || old.Network != matrixNetname {
		return fmt.Errorf("only an owned Matrix node may apply an edited bundle")
	}
	if strings.TrimRight(old.Panel, "/") != strings.TrimRight(b.Panel, "/") {
		return fmt.Errorf("updated bundle is from a different hub")
	}
	current, e := os.ReadFile(filepath.Join(matrixDir(), "hosts/main"))
	if e != nil {
		return e
	}
	key, e := matrixPublicKey(string(current))
	if e != nil {
		return e
	}
	newKey, e := matrixPublicKey(b.MainHost)
	if e != nil {
		return e
	}
	if key != newKey {
		return fmt.Errorf("hub key changed; use a new setup instead of editing")
	}
	next := old
	next.Family = b.Family
	next.Addr = b.Addr
	next.Port = b.Port
	next.Ports = b.Ports
	next.Revision = b.Revision
	next.PeerSyncRequired = old.Family != b.Family || old.Addr != b.Addr || old.Port != b.Port || old.Revision != b.Revision || old.PeerSyncRequired
	if e = matrixEditCommit(old, next, []byte(b.MainHost)); e != nil {
		return e
	}
	// Port-only node refreshes merely update displayed settings; no transport
	// acknowledgement or restart is needed while the family/address is unchanged.
	if old.Family == b.Family && old.Addr == b.Addr && old.Port == b.Port && old.Revision == b.Revision && !old.PeerSyncRequired {
		return nil
	}
	// Register the same key/identity to acknowledge this revision on the hub.
	self, e := os.ReadFile(filepath.Join(matrixDir(), "hosts", old.Name))
	if e != nil {
		return e
	}
	data, _ := json.Marshal(map[string]string{"token": b.Token, "revision": b.Revision, "name": old.Name, "host_file": string(self)})
	client := &http.Client{Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Post(b.Panel+"/api/matrix/register", "application/json", strings.NewReader(string(data)))
	if e != nil {
		return fmt.Errorf("node settings saved, but hub acknowledgement failed; retry this bundle")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node settings saved, but hub acknowledgement returned HTTP %d; retry this bundle", resp.StatusCode)
	}
	next.PeerSyncRequired = false
	return saveMatrix(next)
}

func handleMatrixEdit(w http.ResponseWriter, r *http.Request) {
	var q matrixEditRequest
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 40<<10)).Decode(&q); e != nil {
		writeAPIError(w, r, "E-MATRIX-02", "invalid JSON")
		return
	}
	matrixMu.Lock()
	defer matrixMu.Unlock()
	old := loadMatrix()
	if q.Bundle != "" {
		b, e := parseMatrixBundle(q.Bundle)
		if e == nil {
			e = matrixApplyNodeBundle(old, b)
		}
		if e != nil {
			writeAPIError(w, r, "E-MATRIX-07", e.Error())
			return
		}
		LogSecurityAudit("matrix_edit", cfg.Username, ClientIP(r), "node applied hub bundle")
		writeJSON(w, map[string]any{"status": "ok", "peer_sync_required": false})
		return
	}
	st, e := matrixApplyEdit(old, q)
	if e != nil {
		writeAPIError(w, r, "E-MATRIX-07", e.Error())
		return
	}
	LogSecurityAudit("matrix_edit", cfg.Username, ClientIP(r), "hub settings saved")
	writeJSON(w, map[string]any{"status": "ok", "bundle": matrixBundle(st, r), "peer_sync_required": st.PeerSyncRequired})
}
