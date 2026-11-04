package panel

// Host network metrics have their own source, baselines and durable history.
// No tunnel/carrier counter or legacy traffic history enters this ledger.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type serverTrafficMetrics struct {
	Rx         *uint64  `json:"rx"`
	Tx         *uint64  `json:"tx"`
	RxRate     *float64 `json:"rx_rate"`
	TxRate     *float64 `json:"tx_rate"`
	Interfaces []string `json:"interfaces"`
	Known      bool     `json:"known"`
	CountersAt *int64   `json:"counters_at"`
}

type serverConnectionMetrics struct {
	Active *int `json:"active"`
	Known  bool `json:"known"`
}

func dashboardServerTraffic(v serverTrafficMetrics) serverTrafficMetrics {
	if v.Interfaces == nil {
		v.Interfaces = []string{}
	}
	return v
}

func serverConnections(v any) serverConnectionMetrics {
	if n, ok := v.(int); ok {
		return serverConnectionMetrics{Active: &n, Known: true}
	}
	return serverConnectionMetrics{}
}

// t/up/down/total/conns are compatible with the existing dashboard history.
// Explicit host aliases and nullable rates also let consumers avoid deriving
// a speed across missing samples. Counters are bytes observed since recording
// started, not fabricated boot totals or migrated tunnel history.
type serverHistoryPoint struct {
	trafficPoint
	Ts     int64    `json:"ts"`
	Rx     *uint64  `json:"rx"`
	Tx     *uint64  `json:"tx"`
	RxRate *float64 `json:"rx_rate"`
	TxRate *float64 `json:"tx_rate"`
	Known  bool     `json:"known"`
}

// Filesystem probes are injectable; Linux data is absent/unknown on Windows.
type serverInterfaceProbe struct {
	readFile func(string) ([]byte, error)
	readDir  func(string) ([]os.DirEntry, error)
	stat     func(string) (os.FileInfo, error)
	realPath func(string) (string, error)
}

var serverNetProbe = serverInterfaceProbe{os.ReadFile, os.ReadDir, os.Stat, filepath.EvalSymlinks}
var serverSysRoot = "/sys/class/net"

func serverDefaultInterfaces(p serverInterfaceProbe, root string) map[string]bool {
	m := map[string]bool{}
	if b, err := p.readFile(filepath.Join(root, "net/route")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 8 || f[1] != "00000000" || f[7] != "00000000" {
				continue
			}
			flags, err := strconv.ParseUint(f[3], 16, 32)
			if err == nil && flags&1 != 0 && flags&0x200 == 0 {
				m[f[0]] = true
			}
		}
	}
	if b, err := p.readFile(filepath.Join(root, "net/ipv6_route")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) < 10 || f[0] != strings.Repeat("0", 32) || f[1] != "00" {
				continue
			}
			flags, err := strconv.ParseUint(f[8], 16, 32)
			if err == nil && flags&1 != 0 && flags&0x200 == 0 {
				m[f[9]] = true
			}
		}
	}
	return m
}

func excludedServerInterface(name string) bool {
	n := strings.ToLower(name)
	for _, prefix := range []string{"lo", "gre", "gretap", "erspan", "ip6gre", "ip6tnl", "sit", "tun", "tap", "tinc", "veth", "docker", "br", "virbr", "dummy", "bond", "team", "vxlan", "geneve", "wg", "tailscale", "zt", "qmx", "qsp"} {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// Physical PCI/virtio NICs have a device link, irrespective of the interface
// name or current default route. Default-routed Ethernet is a fallback for
// cloud NICs whose device link is hidden, but never a bridge, veth, VLAN or
// tunnel. In particular qmx0 must not be counted again beside its transport.
func eligibleServerInterface(name string, routed bool, root string, p serverInterfaceProbe) bool {
	if excludedServerInterface(name) {
		return false
	}
	base := filepath.Join(root, name)
	b, err := p.readFile(filepath.Join(base, "type"))
	if err != nil {
		return false
	}
	typ := strings.TrimSpace(string(b))
	if typ != "1" && typ != "32" { // Ethernet/WiFi and physical InfiniBand
		return false
	}
	for _, marker := range []string{"bridge", "tun_flags", "bonding"} {
		if _, err := p.stat(filepath.Join(base, marker)); err == nil {
			return false
		}
	}
	entries, err := p.readDir(base)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "lower_") {
			return false // VLAN/macvlan/other stacked accounting
		}
	}
	if path, err := p.realPath(base); err == nil {
		if strings.Contains(filepath.ToSlash(path), "/devices/virtual/") {
			return false
		}
		if _, err := p.stat(filepath.Join(base, "device")); err == nil {
			return true
		}
	}
	if !routed || typ != "1" {
		return false
	}
	// veth may be renamed eth0. Its peer ifindex differs even when it is the
	// default interface; don't confuse a container network with host traffic.
	index, e1 := p.readFile(filepath.Join(base, "ifindex"))
	link, e2 := p.readFile(filepath.Join(base, "iflink"))
	return e1 == nil && e2 == nil && strings.TrimSpace(string(index)) != "" && strings.TrimSpace(string(index)) == strings.TrimSpace(string(link))
}

type serverInterfaceSelector struct {
	mu        sync.Mutex
	at        time.Time
	proc, sys string
	names     []string
}

var serverIfSelector serverInterfaceSelector

func (s *serverInterfaceSelector) counters(dev map[string]devCounters, at time.Time, proc, sys string, p serverInterfaceProbe) (map[string][2]uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Discover interfaces once a minute, not once per browser or per socket.
	if s.proc != proc || s.sys != sys || s.at.IsZero() || at.Sub(s.at) >= time.Minute {
		s.proc, s.sys, s.at = proc, sys, at
		s.names = nil
		routed := serverDefaultInterfaces(p, proc)
		if entries, err := p.readDir(sys); err == nil {
			for _, entry := range entries {
				if eligibleServerInterface(entry.Name(), routed[entry.Name()], sys, p) {
					s.names = append(s.names, entry.Name())
				}
			}
		}
		sort.Strings(s.names)
	}
	out := make(map[string][2]uint64, len(s.names))
	known := len(s.names) > 0 && dev != nil
	for _, name := range s.names {
		if c, ok := dev[name]; ok {
			out[name] = [2]uint64{c.rx, c.tx}
		} else {
			known = false // partial access must not report a fabricated aggregate
		}
	}
	return out, known
}

type serverMetricsDisk struct {
	Version int                  `json:"v"`
	Rx      uint64               `json:"rx"`
	Tx      uint64               `json:"tx"`
	LastIf  map[string][2]uint64 `json:"last_if"`
	BootID  string               `json:"boot_id"`
	Points  []serverHistoryPoint `json:"points"`
}

type serverMetricsStore struct {
	mu        sync.Mutex
	path      string
	disk      serverMetricsDisk
	lastAt    time.Time
	lastKnown bool
	lastSave  time.Time
	dirty     bool
}

var serverMetrics serverMetricsStore

func serverMetricsFile() string { return filepath.Join(configDir, "server_metrics.json") }

func (s *serverMetricsStore) loadLocked(path string, now time.Time) {
	if s.path == path {
		return
	}
	s.path, s.disk = path, serverMetricsDisk{Version: 1}
	s.lastAt, s.lastSave, s.lastKnown, s.dirty = time.Time{}, time.Time{}, false, false
	if b, err := os.ReadFile(path); err == nil {
		var d serverMetricsDisk
		if json.Unmarshal(b, &d) == nil && d.Version == 1 {
			s.disk = d
		}
	}
	s.compactLocked(now.Unix())
}

func (s *serverMetricsStore) sample(path string, cur map[string][2]uint64, known bool, bootID string, at time.Time, conns any) serverTrafficMetrics {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked(path, at)
	result := serverTrafficMetrics{Interfaces: []string{}}
	if at.IsZero() || (!s.lastAt.IsZero() && !at.After(s.lastAt)) {
		return result
	}
	names := make([]string, 0, len(cur))
	for name := range cur {
		names = append(names, name)
	}
	sort.Strings(names)
	result.Interfaces = names
	stamp := at.UnixMilli()
	result.CountersAt = &stamp
	known = known && len(cur) > 0
	point := serverHistoryPoint{trafficPoint: trafficPoint{T: at.Unix()}, Ts: at.Unix(), Known: known}
	if n, ok := conns.(int); ok {
		point.Conns = &n
	}
	if known {
		var dr, dt uint64
		comparable := len(cur) == len(s.disk.LastIf)
		sameBoot := bootID != "" && s.disk.BootID == bootID
		// Without boot identity, only in-process baselines are trustworthy.
		if !sameBoot && (bootID != s.disk.BootID || s.lastAt.IsZero()) {
			s.disk.LastIf = nil
			comparable = false
		}
		for name, c := range cur {
			prev, exists := s.disk.LastIf[name]
			if !exists || c[0] < prev[0] || c[1] < prev[1] {
				comparable = false // reset/source change: fresh baseline, never spike
				continue
			}
			dr += c[0] - prev[0]
			dt += c[1] - prev[1]
		}
		s.disk.Rx += dr
		s.disk.Tx += dt
		s.disk.LastIf = make(map[string][2]uint64, len(cur))
		for name, c := range cur {
			s.disk.LastIf[name] = c
		}
		s.disk.BootID = bootID
		rx, tx, total := s.disk.Rx, s.disk.Tx, s.disk.Rx+s.disk.Tx
		result.Rx, result.Tx, result.Known = &rx, &tx, true
		point.Rx, point.Tx, point.Up, point.Down, point.Total = &rx, &tx, &tx, &rx, &total
		if comparable && s.lastKnown && !s.lastAt.IsZero() {
			seconds := at.Sub(s.lastAt).Seconds()
			rr, tr := float64(dr)/seconds, float64(dt)/seconds
			result.RxRate, result.TxRate, point.RxRate, point.TxRate = &rr, &tr, &rr, &tr
		}
	}
	// Unknown reads preserve baselines/totals, but history honestly shows gaps.
	s.lastAt, s.lastKnown = at, known
	n := len(s.disk.Points)
	if n == 0 || point.T-s.disk.Points[n-1].T >= 4 {
		s.disk.Points = append(s.disk.Points, point)
	}
	s.dirty = true
	if s.lastSave.IsZero() || at.Sub(s.lastSave) >= 30*time.Second {
		_ = s.saveLocked(at)
	}
	return result
}

// Keep 5-second observations for a day and minute observations through 7d.
// The strict 7-day window also bounds memory and persistence on busy hosts.
func (s *serverMetricsStore) compactLocked(now int64) {
	out := make([]serverHistoryPoint, 0, len(s.disk.Points))
	var last, bucket int64 = -1, -1
	for _, p := range s.disk.Points {
		if p.T < now-7*86400 || p.T > now+60 || p.T <= last {
			continue
		}
		last = p.T
		if p.T < now-86400 {
			b := p.T / 60
			if b == bucket {
				continue
			}
			bucket = b
		}
		out = append(out, p)
	}
	s.disk.Points = out
}

func (s *serverMetricsStore) saveLocked(now time.Time) error {
	s.compactLocked(now.Unix())
	b, err := json.Marshal(s.disk)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	if err = os.WriteFile(s.path+".tmp", b, 0600); err != nil {
		return err
	}
	if err = os.Rename(s.path+".tmp", s.path); err != nil {
		return err
	}
	s.lastSave, s.dirty = now, false
	return nil
}

func (s *serverMetricsStore) flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirty && s.path != "" {
		_ = s.saveLocked(time.Now())
	}
}

func (s *serverMetricsStore) history(path, rng string, now time.Time) []serverHistoryPoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked(path, now)
	span := int64(86400)
	switch rng {
	case "1h":
		span = 3600
	case "7d":
		span = 7 * 86400
	}
	out := []serverHistoryPoint{}
	for _, p := range s.disk.Points {
		if p.T >= now.Unix()-span && p.T <= now.Unix()+60 {
			out = append(out, p)
		}
	}
	if len(out) <= 240 {
		return out
	}
	// Preserve unavailable observations rather than silently filling a gap.
	reduced := make([]serverHistoryPoint, 0, 240)
	for i := 0; i < 239; i++ {
		start := i * (len(out) - 1) / 239
		end := (i + 1) * (len(out) - 1) / 239
		p := out[start]
		for j := start; j < end; j++ {
			if !out[j].Known {
				p = out[j]
				break
			}
		}
		reduced = append(reduced, p)
	}
	return append(reduced, out[len(out)-1])
}

func serverTrafficHistory(rng string) []serverHistoryPoint {
	return serverMetrics.history(serverMetricsFile(), rng, time.Now())
}
