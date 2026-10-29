package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testServerNIC(t *testing.T, root, name, typ string, physical bool, index, link string) {
	t.Helper()
	base := filepath.Join(root, name)
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	for file, value := range map[string]string{"type": typ, "ifindex": index, "iflink": link} {
		if err := os.WriteFile(filepath.Join(base, file), []byte(value+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if physical {
		if err := os.Mkdir(filepath.Join(base, "device"), 0700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestServerPhysicalSelection(t *testing.T) {
	root, proc := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(proc, "net"), 0700); err != nil {
		t.Fatal(err)
	}
	names := []string{"ens5", "enp7s0", "cloud0", "qmx0", "gre-main", "tinc0", "tun0", "vethABC", "br0", "lo", "renamedGRE", "renamedBridge", "renamedVeth", "vlan100"}
	dev := map[string]devCounters{}
	for _, name := range names {
		physical := name == "ens5" || name == "enp7s0"
		typ := "1"
		if name == "renamedGRE" {
			typ = "778"
		}
		if name == "lo" {
			typ = "772"
		}
		link := "5"
		if name == "renamedVeth" {
			link = "8"
		}
		testServerNIC(t, root, name, typ, physical, "5", link)
		dev[name] = devCounters{100, 200}
	}
	for _, item := range [][2]string{{"renamedBridge", "bridge"}, {"vlan100", "lower_ens5"}} {
		if err := os.Mkdir(filepath.Join(root, item[0], item[1]), 0700); err != nil {
			t.Fatal(err)
		}
	}
	routes := "Iface Destination Gateway Flags RefCnt Use Metric Mask MTU Window IRTT\n"
	for _, name := range names {
		routes += fmt.Sprintf("%s 00000000 01000000 0003 0 0 100 00000000 0 0 0\n", name)
	}
	if err := os.WriteFile(filepath.Join(proc, "net/route"), []byte(routes), 0600); err != nil {
		t.Fatal(err)
	}
	var selector serverInterfaceSelector
	cur, known := selector.counters(dev, time.Now(), proc, root, serverNetProbe)
	want := map[string][2]uint64{"ens5": {100, 200}, "enp7s0": {100, 200}, "cloud0": {100, 200}}
	if !known || !reflect.DeepEqual(cur, want) {
		t.Fatalf("selected=%v known=%v want=%v", cur, known, want)
	}
	delete(dev, "ens5")
	_, known = selector.counters(dev, time.Now(), proc, root, serverNetProbe)
	if known {
		t.Fatal("a partial read must not be presented as whole-host totals")
	}
}

func TestServerRouteScopeAndVirtualDevice(t *testing.T) {
	root, proc := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(proc, "net"), 0700)
	testServerNIC(t, root, "physical0", "1", true, "1", "1")
	testServerNIC(t, root, "cloud6", "1", false, "2", "2")
	testServerNIC(t, root, "unrouted0", "1", false, "3", "3")
	testServerNIC(t, root, "rejected0", "1", false, "4", "4")
	_ = os.WriteFile(filepath.Join(proc, "net/route"), []byte("rejected0 00000000 00000000 0201 0 0 0 00000000 0 0 0\n"), 0600)
	v6 := strings.Repeat("0", 32) + " 00 " + strings.Repeat("0", 32) + " 00 " + strings.Repeat("0", 32) + " 00000001 00000000 00000000 00000001 cloud6\n"
	_ = os.WriteFile(filepath.Join(proc, "net/ipv6_route"), []byte(v6), 0600)
	var selector serverInterfaceSelector
	cur, known := selector.counters(map[string]devCounters{"physical0": {1, 2}, "cloud6": {3, 4}, "unrouted0": {5, 6}, "rejected0": {7, 8}}, time.Now(), proc, root, serverNetProbe)
	if !known || len(cur) != 2 || cur["physical0"] != ([2]uint64{1, 2}) || cur["cloud6"] != ([2]uint64{3, 4}) {
		t.Fatalf("route selection=%v known=%v", cur, known)
	}
	probe := serverNetProbe
	probe.realPath = func(path string) (string, error) { return "/sys/devices/virtual/net/" + filepath.Base(path), nil }
	if eligibleServerInterface("physical0", false, root, probe) {
		t.Fatal("device marker on virtual interface must not make it physical")
	}
}

func TestServerPhysicalRatesResetsAndUnknown(t *testing.T) {
	var store serverMetricsStore
	path := filepath.Join(t.TempDir(), "server_metrics.json")
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	first := store.sample(path, map[string][2]uint64{"ens5": {1000, 2000}, "enp7s0": {500, 500}}, true, "boot-a", at, 17)
	if !first.Known || *first.Rx != 0 || *first.Tx != 0 || first.RxRate != nil {
		t.Fatalf("first baseline=%+v", first)
	}
	next := store.sample(path, map[string][2]uint64{"ens5": {1500, 2100}, "enp7s0": {700, 600}}, true, "boot-a", at.Add(5*time.Second), 17)
	if *next.Rx != 700 || *next.Tx != 200 || *next.RxRate != 140 || *next.TxRate != 40 {
		t.Fatalf("whole host rates=%+v", next)
	}
	reset := store.sample(path, map[string][2]uint64{"ens5": {4, 8}, "enp7s0": {750, 650}}, true, "boot-a", at.Add(10*time.Second), nil)
	if *reset.Rx != 750 || *reset.Tx != 250 || reset.RxRate != nil {
		t.Fatalf("reset must only add unchanged physical NIC deltas=%+v", reset)
	}
	unknown := store.sample(path, nil, false, "boot-a", at.Add(15*time.Second), nil)
	if unknown.Known || unknown.Rx != nil || unknown.Tx != nil || unknown.RxRate != nil {
		t.Fatalf("unreadable counters=%+v", unknown)
	}
	back := store.sample(path, map[string][2]uint64{"ens5": {14, 18}, "enp7s0": {760, 660}}, true, "boot-a", at.Add(20*time.Second), 17)
	if *back.Rx != 770 || *back.Tx != 270 || back.RxRate != nil {
		t.Fatalf("unknown preserves baseline without false short-window speed=%+v", back)
	}
	dropped := store.sample(path, map[string][2]uint64{"ens5": {24, 28}}, true, "boot-a", at.Add(25*time.Second), 17)
	if *dropped.Rx != 780 || dropped.RxRate != nil {
		t.Fatalf("NIC drop must not fabricate reset=%+v", dropped)
	}
	stale := store.sample(path, map[string][2]uint64{"ens5": {999999, 999999}}, true, "boot-a", at.Add(24*time.Second), 17)
	if stale.Known || store.disk.Rx != 780 {
		t.Fatal("stale sample was counted")
	}
	history := store.history(path, "1h", at.Add(time.Minute))
	if len(history) != 6 || history[3].Up != nil || history[3].Down != nil || history[3].Known {
		t.Fatalf("history gap=%+v", history)
	}
	if *history[1].Up != 200 || *history[1].Down != 700 || history[1].T != history[1].Ts {
		t.Fatal("history must use host counters with compatible directions")
	}
}

func TestServerHistoryPersistenceSeparateFromTunnel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server_metrics.json")
	at := time.Now().Add(-time.Minute)
	_ = os.WriteFile(filepath.Join(dir, "traffic.json"), []byte(`[{"t":1,"up":999999,"down":999999}]`), 0600)
	var store serverMetricsStore
	if got := store.history(path, "7d", at); len(got) != 0 {
		t.Fatalf("old tunnel history migrated as server data: %v", got)
	}
	store.sample(path, map[string][2]uint64{"ens5": {100, 200}}, true, "boot-a", at, 1)
	store.sample(path, map[string][2]uint64{"ens5": {200, 400}}, true, "boot-a", at.Add(5*time.Second), 2)
	store.flush()
	var restored serverMetricsStore
	if h := restored.history(path, "7d", time.Now()); len(h) != 2 || *h[1].Rx != 100 || *h[1].Tx != 200 {
		t.Fatalf("persisted server history=%v", h)
	}
	got := restored.sample(path, map[string][2]uint64{"ens5": {250, 450}}, true, "boot-a", at.Add(10*time.Second), 2)
	if *got.Rx != 150 || *got.Tx != 250 || got.RxRate != nil {
		t.Fatalf("same boot restart counters=%+v", got)
	}
	reboot := restored.sample(path, map[string][2]uint64{"ens5": {999999, 999999}}, true, "boot-b", at.Add(15*time.Second), 2)
	if *reboot.Rx != 150 || *reboot.Tx != 250 || reboot.RxRate != nil {
		t.Fatal("different boot must rebaseline without boot totals")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "traffic.json"))
	if !strings.Contains(string(b), "999999") {
		t.Fatal("legacy tunnel file was changed")
	}
}

func TestServerHistoryRangesAndBound(t *testing.T) {
	var store serverMetricsStore
	path := filepath.Join(t.TempDir(), "server_metrics.json")
	now := time.Now().Truncate(time.Second)
	store.loadLocked(path, now)
	for i := 0; i < 1000; i++ {
		timestamp := now.Add(-time.Duration(999-i) * time.Second).Unix()
		store.disk.Points = append(store.disk.Points, serverHistoryPoint{trafficPoint: trafficPoint{T: timestamp}, Ts: timestamp, Known: true})
	}
	for _, hours := range []int{2, 25, 100, 200} {
		timestamp := now.Add(-time.Duration(hours) * time.Hour).Unix()
		store.disk.Points = append([]serverHistoryPoint{{trafficPoint: trafficPoint{T: timestamp}, Ts: timestamp, Known: true}}, store.disk.Points...)
	}
	store.compactLocked(now.Unix())
	for _, rng := range []string{"1h", "24h", "7d"} {
		h := store.history(path, rng, now)
		if len(h) != 240 || h[len(h)-1].T != now.Unix() {
			t.Fatalf("range %s count=%d", rng, len(h))
		}
		span := int64(86400)
		if rng == "1h" {
			span = 3600
		}
		if rng == "7d" {
			span = 7 * 86400
		}
		for _, p := range h {
			if p.T < now.Unix()-span {
				t.Fatalf("range %s out of range", rng)
			}
		}
	}
}

func TestServerDashboardAdditiveJSON(t *testing.T) {
	oldDir, oldSnap := configDir, snapPtr.Load()
	configDir = t.TempDir()
	t.Cleanup(func() { configDir = oldDir; snapPtr.Store(oldSnap) })
	rx, tx, rate, stamp := uint64(123), uint64(456), float64(12), time.Now().UnixMilli()
	snapPtr.Store(&HubSnapshot{At: time.Now(), Traffic: map[string]any{"up": uint64(9), "down": uint64(8)}, ActiveConns: 73, Bagher: map[string]any{}, ServerTraffic: serverTrafficMetrics{Rx: &rx, Tx: &tx, RxRate: &rate, Interfaces: []string{"ens5"}, Known: true, CountersAt: &stamp}})
	w := httptest.NewRecorder()
	handleDashboard(w, httptest.NewRequest("GET", "/api/dashboard?range=7d", nil))
	var data map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if data["traffic"].(map[string]any)["up"] != float64(9) || data["server_traffic"].(map[string]any)["rx"] != float64(123) || data["server_connections"].(map[string]any)["active"] != float64(73) {
		t.Fatalf("dashboard fields=%s", w.Body.String())
	}
	if _, ok := data["server_history"].([]any); !ok {
		t.Fatal("empty server history must be [] not null")
	}
	unknown, _ := json.Marshal(dashboardServerTraffic(serverTrafficMetrics{}))
	if !strings.Contains(string(unknown), `"rx":null`) || !strings.Contains(string(unknown), `"interfaces":[]`) {
		t.Fatalf("unknown shape=%s", unknown)
	}
}

func TestServerNoPhysicalCountersAreUnknown(t *testing.T) {
	var selector serverInterfaceSelector
	cur, known := selector.counters(nil, time.Now(), t.TempDir(), t.TempDir(), serverNetProbe)
	if known || len(cur) != 0 {
		t.Fatal("missing proc/sys must be unknown on Windows or restricted hosts")
	}
	if c := serverConnections(nil); c.Known || c.Active != nil {
		t.Fatal("unknown connections must not report zero")
	}
	if c := serverConnections(0); !c.Known || c.Active == nil || *c.Active != 0 {
		t.Fatal("known idle connections should report zero")
	}
}
