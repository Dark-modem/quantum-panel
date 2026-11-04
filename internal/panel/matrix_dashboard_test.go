package panel

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func matrixDashboardFixture(t *testing.T, role string, service, connected bool) (*hostView, matrixState) {
	t.Helper()
	oldConfig, oldFRP, oldBH := configDir, frpDir, backhaulDir
	dir := t.TempDir()
	configDir, frpDir, backhaulDir = dir, filepath.Join(dir, "frp"), filepath.Join(dir, "backhaul")
	t.Cleanup(func() { configDir, frpDir, backhaulDir = oldConfig, oldFRP, oldBH })
	st := matrixState{Enabled: true, Network: matrixNetname, Role: role, Family: "ipv6", Port: 2097, Addr: "2001:db8::1", IP: matrixHubIP, Ports: []int{443, 8080}}
	if role == "hub" {
		st.Peers = []matrixPeer{{Name: "foreign", Addr: "2001:db8::2", IP: matrixFirstPeer}}
	} else {
		st.IP = matrixFirstPeer
	}
	if err := os.WriteFile(matrixStatePath(), mustJSON(st), 0600); err != nil {
		t.Fatal(err)
	}
	v := &hostView{inner: map[string]string{matrixIfaceName: st.IP + "/30"}, active: map[string]bool{matrixUnit: service}, dev: map[string]devCounters{matrixIfaceName: {1000, 2000}}, icmp: func(target string) icmpResult {
		if !strings.HasPrefix(target, matrixIfaceName+"|") {
			t.Fatalf("mesh probe not bound to qmx0: %q", target)
		}
		return icmpResult{OK: connected, Known: true, Ms: 12}
	}}
	return v, st
}

func TestMatrixOnlyDashboardStatus(t *testing.T) {
	for _, role := range []string{"hub", "node"} {
		t.Run(role, func(t *testing.T) {
			v, _ := matrixDashboardFixture(t, role, true, true)
			local := localStatusFrom(v)
			if local.Engine != "matrix" || local.Matrix == nil || !local.Matrix.Connected {
				t.Fatalf("Matrix missing: %+v", local)
			}
			if local.FrpSvc != "" || local.FrpUp || local.Gre.Exists || len(local.Proxies) != 0 {
				t.Fatalf("Matrix mislabeled GRE/FRP: %+v", local)
			}
			if local.RemotePub == "" || len(local.Ports) != 2 {
				t.Fatalf("endpoint/ports missing: %+v", local)
			}
			if main := mainTunnelFrom(local, v); main != nil {
				t.Fatalf("Matrix invented legacy main: %+v", main)
			}
			prev := snapPtr.Load()
			t.Cleanup(func() { snapPtr.Store(prev) })
			snapPtr.Store(&HubSnapshot{At: time.Now(), Local: local, Traffic: map[string]any{}, Bagher: map[string]any{}})
			rr := httptest.NewRecorder()
			handleDashboard(rr, httptest.NewRequest("GET", "/api/dashboard", nil))
			var d map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
				t.Fatal(err)
			}
			if d["active_engine"] != "matrix" || d["health"] != "HEALTHY" || d["matrix"] == nil || d["online"] != true {
				t.Fatalf("dashboard lost Matrix: %s", rr.Body.String())
			}
			rr = httptest.NewRecorder()
			handleStatus(rr, httptest.NewRequest("GET", "/api/status", nil))
			if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
				t.Fatal(err)
			}
			lv := d["local"].(map[string]any)
			if lv["health_state"] != "healthy" || lv["matrix"] == nil || lv["rx"] != float64(1000) {
				t.Fatalf("status lost Matrix health/traffic: %s", rr.Body.String())
			}
		})
	}
}

func TestMatrixServiceIsNotMeshConnection(t *testing.T) {
	v, st := matrixDashboardFixture(t, "hub", true, false)
	mx := matrixStatusFrom(st, v)
	if mx.Connected || mx.HealthState != stateDegraded {
		t.Fatalf("running service claimed connected: %+v", mx)
	}
	v.active[matrixUnit] = false
	mx = matrixStatusFrom(st, v)
	if mx.HealthState != stateDown || mx.Service || mx.Connected {
		t.Fatalf("stopped service: %+v", mx)
	}
	v.active[matrixUnit] = true
	v.icmp = func(string) icmpResult { return icmpResult{OK: true, Known: true, Ms: 10} }
	st.PeerSyncRequired = true
	mx = matrixStatusFrom(st, v)
	if mx.Connected || mx.HealthState != stateDegraded {
		t.Fatalf("pending edit claimed connected: %+v", mx)
	}
}

func TestMatrixKeepsHealthyFRPPrimary(t *testing.T) {
	v, _ := matrixDashboardFixture(t, "hub", true, true)
	v.active["frpc"] = true
	local := localStatusFrom(v)
	if local.Engine != "frp" || local.FrpSvc != "frpc" || local.Role != "foreign (client)" || local.Matrix == nil {
		t.Fatalf("Matrix replaced FRP primary: %+v", local)
	}
	prev := snapPtr.Load()
	t.Cleanup(func() { snapPtr.Store(prev) })
	for _, tc := range []struct {
		session  bool
		matrixUp bool
		health   string
	}{
		{true, true, "HEALTHY"}, {true, false, "DEGRADED"}, {false, true, "DEGRADED"},
	} {
		local.Matrix.Service = tc.matrixUp
		local.Matrix.Connected = tc.matrixUp
		snapPtr.Store(&HubSnapshot{At: time.Now(), Local: local, LocalSession: tc.session, LocalRTT: 10, Traffic: map[string]any{}, Bagher: map[string]any{}})
		rr := httptest.NewRecorder()
		handleDashboard(rr, httptest.NewRequest("GET", "/api/dashboard", nil))
		var d map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &d); err != nil {
			t.Fatal(err)
		}
		if d["active_engine"] != "frp" || d["health"] != tc.health {
			t.Fatalf("mixed rollup lost legacy health (%+v): %s", tc, rr.Body.String())
		}
	}
}

func TestMatrixTrafficAndFleetIsolation(t *testing.T) {
	resetTrafficState(t)
	data := devLines(map[string][2]uint64{"qmx0": {100, 200}, "ens5": {999999, 999999}})
	cur := trafficTunnelIfs(data)
	if len(cur) != 1 || cur["qmx0"] != [2]uint64{100, 200} {
		t.Fatalf("qmx0 real counters missing or NIC counted: %+v", cur)
	}
	if cur := trafficTunnelIfs(devLines(map[string][2]uint64{"ens5": {999, 999}})); len(cur) != 0 {
		t.Fatalf("invented Matrix traffic: %+v", cur)
	}
	v, st := matrixDashboardFixture(t, "node", true, true)
	snap := &HubSnapshot{At: time.Now(), Local: tunnelStatus{Matrix: matrixStatusFrom(st, v)}}
	links := snap.MatrixLinks()
	if len(links) != 1 || links[0].Engine != "matrix" || links[0].GreUp || links[0].FrpUp || !links[0].Linked || links[0].Rx == nil {
		t.Fatalf("fleet mislabeled Matrix: %+v", links)
	}
	if len(snap.All()) != 0 {
		t.Fatal("Matrix polluted legacy tunnel records")
	}
	targets := icmpTargets(snap)
	if _, ok := targets[matrixProbeKey(matrixHubIP)]; !ok {
		t.Fatalf("no qmx0-bound background mesh probe: %+v", targets)
	}
}
