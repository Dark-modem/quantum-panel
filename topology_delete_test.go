package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
)

func topologyDeleteStored(t *testing.T, ids []string) Topology {
	t.Helper()
	group := Topology{ID: "topo_delete", Name: "Delete guard", Role: "iran", TunnelIDs: ids, CreatedAt: 1}
	other := Topology{ID: "topo_other", Name: "Preserved", Role: "foreign", TunnelIDs: []string{}, CreatedAt: 2}
	if err := saveTopologies([]Topology{group, other}); err != nil {
		t.Fatal(err)
	}
	return group
}

func topologyDeleteFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(configDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			files[path] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func TestTopologyDeleteBlocksExistingRegardlessOfState(t *testing.T) {
	for _, state := range []string{"healthy", "degraded", "down", "off", "stopped", "unknown", ""} {
		t.Run(state, func(t *testing.T) {
			topologyFixture(t)
			group := topologyDeleteStored(t, []string{"peer:999", "main", "xray:4"})
			reads := 0
			topologyInventory = func() []topologyTunnel {
				reads++
				// Inventory must run before the metadata lock, on each request.
				if !topologyMu.TryLock() {
					t.Fatal("inventory read while metadata locked")
				}
				topologyMu.Unlock()
				return []topologyTunnel{{ID: "xray:4", State: state}, {ID: "main", State: state}, {ID: "peer:8"}}
			}
			for _, file := range []string{"peers.json", "matrix.json", "xray.json", "sshfwd.json", "frp/frps.toml", "units/frps.service"} {
				path := filepath.Join(configDir, file)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("untouched engine resource"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := topologyDeleteFiles(t)
			for attempt := 0; attempt < 2; attempt++ {
				w := topologyRequest(http.MethodDelete, group.ID, "")
				if w.Code != http.StatusConflict || w.Header().Get("Content-Type") != "application/json" {
					t.Fatalf("DELETE existing: %d %s", w.Code, w.Body.String())
				}
				var response struct {
					Error     string   `json:"error"`
					Code      string   `json:"code"`
					Remaining []string `json:"remaining_tunnel_ids"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Error == "" || response.Code != "topology_has_tunnels" || !reflect.DeepEqual(response.Remaining, []string{"main", "xray:4"}) {
					t.Fatalf("conflict contract: %s (%v)", w.Body.String(), err)
				}
				if !reflect.DeepEqual(before, topologyDeleteFiles(t)) {
					t.Fatal("409 changed metadata/engine files or created extra files")
				}
			}
			if reads != 2 {
				t.Fatalf("inventory reads=%d, want one fresh read per request", reads)
			}
		})
	}
}

func TestTopologyDeleteAllowsDeletedStaleIDsAndEmpty(t *testing.T) {
	for _, ids := range [][]string{{"main", "peer:3", "matrix", "bagher", "xray:4"}, {}} {
		t.Run(fmt.Sprint(ids), func(t *testing.T) {
			topologyFixture(t)
			group := topologyDeleteStored(t, ids)
			topologyInventory = func() []topologyTunnel { return []topologyTunnel{{ID: "peer:8", State: "healthy"}} }
			w := topologyRequest(http.MethodDelete, group.ID, "")
			if w.Code != http.StatusOK {
				t.Fatalf("DELETE removed tunnels/empty: %d %s", w.Code, w.Body.String())
			}
			all, err := loadTopologies()
			if err != nil || len(all) != 1 || all[0].ID != "topo_other" {
				t.Fatalf("DELETE persistence: %+v %v", all, err)
			}
		})
	}
}

func TestTopologyDeleteUnavailableInventoryFailsClosed(t *testing.T) {
	topologyFixture(t)
	group := topologyDeleteStored(t, []string{"main"})
	topologyInventory = func() []topologyTunnel { return nil }
	before := topologyDeleteFiles(t)
	w := topologyRequest(http.MethodDelete, group.ID, "")
	if w.Code != http.StatusServiceUnavailable || !reflect.DeepEqual(before, topologyDeleteFiles(t)) {
		t.Fatalf("unavailable inventory DELETE: %d %s", w.Code, w.Body.String())
	}
	// An empty topology needs no inventory proof; missing IDs still return 404.
	if w := topologyRequest(http.MethodDelete, "topo_other", ""); w.Code != http.StatusOK {
		t.Fatalf("empty topology with unavailable inventory: %d %s", w.Code, w.Body.String())
	}
	if w := topologyRequest(http.MethodDelete, "missing", ""); w.Code != http.StatusNotFound {
		t.Fatalf("missing topology with unavailable inventory: %d", w.Code)
	}
}

func TestTopologyDeleteColdInventoryRetainsStoppedConfigs(t *testing.T) {
	withTempConfig(t)
	oldInventory, oldSnap, oldFRP, oldBackhaul := topologyInventory, snapPtr.Load(), frpDir, backhaulDir
	oldRunner, oldSystemctl := runner, runSystemctl
	var commands atomic.Int32
	runner = func(context.Context, string, ...string) ([]byte, error) {
		commands.Add(1)
		return nil, fmt.Errorf("unexpected host command")
	}
	runSystemctl = func(...string) error {
		commands.Add(1)
		return fmt.Errorf("unexpected service action")
	}
	t.Cleanup(func() {
		topologyInventory, frpDir, backhaulDir = oldInventory, oldFRP, oldBackhaul
		snapPtr.Store(oldSnap)
		runner, runSystemctl = oldRunner, oldSystemctl
	})
	frpDir, backhaulDir = filepath.Join(configDir, "frp"), filepath.Join(configDir, "backhaul")
	topologyInventory = readTopologyInventory
	snapPtr.Store(nil)
	files := map[string]string{
		"peers.json":         `{"peers":[{"id":3,"name":"Stopped peer","engine":"frp"}]}`,
		"matrix.json":        `{"enabled":false,"role":"hub","addr":"203.0.113.2"}`,
		"sshfwd.json":        `{"enabled":false,"role":"client","remote_host":"203.0.113.2","ports":[443],"links":[{"id":2,"remote_host":"203.0.113.3","ports":[8443]}]}`,
		"xray.json":          `{"links":[{"id":4,"name":"Stopped Xray","disabled":true,"side":"iran"}]}`,
		"frp/frps.toml":      "bindPort = 7000\n",
		"units/frps.service": "ExecStart=frps\n",
	}
	for file, data := range files {
		path := filepath.Join(configDir, file)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"main", "peer:3", "matrix", "bagher", "bagher:2", "xray:4"}
	group := topologyDeleteStored(t, ids)
	before := topologyDeleteFiles(t)
	w := topologyRequest(http.MethodDelete, group.ID, "")
	var response struct {
		Remaining []string `json:"remaining_tunnel_ids"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusConflict || !reflect.DeepEqual(response.Remaining, ids) {
		t.Fatalf("cold configured inventory: %d %s (%v)", w.Code, w.Body.String(), err)
	}
	if !reflect.DeepEqual(before, topologyDeleteFiles(t)) || commands.Load() != 0 {
		t.Fatalf("read-only cold inventory changed files or ran commands: %d", commands.Load())
	}
	// Simulate actual tunnel deletion (not stopping or detaching) outside the
	// topology API. Stored IDs remain, and deletion now succeeds without PATCH.
	for _, file := range []string{"peers.json", "matrix.json", "sshfwd.json", "xray.json", "frp/frps.toml"} {
		if err := os.Remove(filepath.Join(configDir, file)); err != nil {
			t.Fatal(err)
		}
	}
	before = topologyDeleteFiles(t)
	w = topologyRequest(http.MethodDelete, group.ID, "")
	if w.Code != http.StatusOK || commands.Load() != 0 {
		t.Fatalf("deleted configured tunnels: %d %s commands=%d", w.Code, w.Body.String(), commands.Load())
	}
	after := topologyDeleteFiles(t)
	delete(before, topologyPath())
	delete(after, topologyPath())
	if !reflect.DeepEqual(before, after) {
		t.Fatal("successful DELETE touched engine resources or added files")
	}
}

func TestTopologyDeleteUnreadableConfigFailsClosed(t *testing.T) {
	for _, file := range []string{"peers.json", "matrix.json", "sshfwd.json", "xray.json"} {
		t.Run(file, func(t *testing.T) {
			withTempConfig(t)
			oldInventory, oldSnap := topologyInventory, snapPtr.Load()
			t.Cleanup(func() { topologyInventory = oldInventory; snapPtr.Store(oldSnap) })
			topologyInventory = readTopologyInventory
			snapPtr.Store(nil)
			group := topologyDeleteStored(t, []string{"peer:3"})
			if err := os.WriteFile(filepath.Join(configDir, file), []byte("invalid inventory"), 0600); err != nil {
				t.Fatal(err)
			}
			before := topologyDeleteFiles(t)
			w := topologyRequest(http.MethodDelete, group.ID, "")
			if w.Code != http.StatusServiceUnavailable || !reflect.DeepEqual(before, topologyDeleteFiles(t)) {
				t.Fatalf("bad config DELETE: %d %s", w.Code, w.Body.String())
			}
		})
	}
}
