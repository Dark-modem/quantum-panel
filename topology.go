package main

// Topologies are panel-only grouping metadata. No handler here writes an engine
// configuration, runs a host command, probes a link, or adopts legacy services.
import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

// Topology describes the local and remote servers, not an engine configuration.
type Topology struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Role      string   `json:"role"`
	LocalIP   string   `json:"local_ip"`
	RemoteIP  string   `json:"remote_ip"`
	Country   string   `json:"country"`
	TunnelIDs []string `json:"tunnel_ids"`
	CreatedAt int64    `json:"created_at"`
}

type topologyTunnel struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Engine    string   `json:"engine"`
	RemoteIP  string   `json:"remote_ip"`
	State     string   `json:"state"`
	LatencyMs float64  `json:"latency_ms"`
	Ports     []int    `json:"ports"`
	Family    string   `json:"family,omitempty"`
	RemoteIPs []string `json:"remote_ips,omitempty"` // explicitly known aliases, never DNS guesses
	role      string
	localIP   string
}

const (
	topologyMaxCount = 100
	topologyMaxIDs   = 1000
	topologyMaxFile  = 1 << 20
)

var (
	topologyMu   sync.Mutex // independent of engine, peer and panel locks
	topologyIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	// "matrix" is the legacy first Matrix instance; matrix2, matrix3, ... are
	// additional independent instances hosted on the same server.
	topologyTunnelRE  = regexp.MustCompile(`^(main|matrix(?:[2-9][0-9]*)?|bagher|peer:[1-9][0-9]*|xray:[1-9][0-9]*|bagher:[1-9][0-9]*)$`)
	topologyCountryRE = regexp.MustCompile(`^[a-z]{2}$`)
	topologyInventory = readTopologyInventory // injectable, deterministic inventory for tests
)

func topologyPath() string { return filepath.Join(configDir, "topologies.json") }

func topologyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}

// loadTopologies never repairs or migrates a file. Bad data fails closed rather
// than silently replacing existing groupings on the next mutation. Caller locks.
func loadTopologies() ([]Topology, error) {
	f, err := os.Open(topologyPath())
	if os.IsNotExist(err) {
		return []Topology{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, topologyMaxFile+1))
	if err != nil || len(data) > topologyMaxFile {
		return nil, fmt.Errorf("cannot read topology collection")
	}
	var stored struct {
		Topologies []Topology `json:"topologies"`
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&stored); err != nil {
		return nil, fmt.Errorf("invalid topology collection: %w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing topology data")
	}
	if stored.Topologies == nil {
		return nil, fmt.Errorf("topology collection must contain an array")
	}
	if len(stored.Topologies) > topologyMaxCount {
		return nil, fmt.Errorf("too many topologies")
	}
	ids, names, owners := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for i := range stored.Topologies {
		t := &stored.Topologies[i]
		if !topologyIDRE.MatchString(t.ID) || ids[t.ID] || t.CreatedAt <= 0 {
			return nil, fmt.Errorf("invalid or duplicate topology identity")
		}
		if err := validateTopologyFields(t); err != nil {
			return nil, err
		}
		name := strings.ToLower(t.Name)
		if names[name] {
			return nil, fmt.Errorf("duplicate topology name")
		}
		ids[t.ID], names[name] = true, true
		for _, id := range t.TunnelIDs {
			if owners[id] {
				return nil, fmt.Errorf("tunnel has multiple topology owners")
			}
			owners[id] = true
		}
	}
	return stored.Topologies, nil
}

// saveTopologies replaces one metadata file, with a fresh private temporary file
// in the same directory. A failed write/close/rename leaves the old file intact.
func saveTopologies(topologies []Topology) error {
	data, err := json.MarshalIndent(map[string]any{"topologies": topologies}, "", "  ")
	if err != nil || len(data) > topologyMaxFile {
		return fmt.Errorf("cannot encode topology collection")
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(configDir, ".topologies-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, topologyPath())
}

// decodeTopologyEdit accepts only editable fields and never accepts null,
// immutable IDs/timestamps, trailing JSON, or arbitrary engine configuration.
func decodeTopologyEdit(w http.ResponseWriter, r *http.Request, t *Topology) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return nil, fmt.Errorf("body must be a JSON object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("body must contain exactly one JSON object")
	}
	for key, raw := range fields {
		var dst any
		switch key {
		case "name":
			dst = &t.Name
		case "role":
			dst = &t.Role
		case "local_ip":
			dst = &t.LocalIP
		case "remote_ip":
			dst = &t.RemoteIP
		case "country":
			dst = &t.Country
		case "tunnel_ids":
			dst = &t.TunnelIDs
		default:
			return nil, fmt.Errorf("field %q is not editable", key)
		}
		if string(raw) == "null" || json.Unmarshal(raw, dst) != nil {
			return nil, fmt.Errorf("invalid type for %s", key)
		}
	}
	return fields, validateTopologyFields(t)
}

func validateTopologyFields(t *Topology) error {
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || !utf8.ValidString(t.Name) || utf8.RuneCountInString(t.Name) > 120 || strings.ContainsFunc(t.Name, unicode.IsControl) {
		return fmt.Errorf("name must contain 1-120 characters without control characters")
	}
	if t.Role != "iran" && t.Role != "foreign" {
		return fmt.Errorf("role must be iran or foreign")
	}
	for _, value := range []*string{&t.LocalIP, &t.RemoteIP} {
		*value = strings.TrimSpace(*value)
		if *value != "" {
			ip := net.ParseIP(*value)
			if ip == nil {
				return fmt.Errorf("local_ip and remote_ip must be IPv4/IPv6 literals or empty")
			}
			*value = ip.String()
		}
	}
	// Country labels the foreign endpoint (ISO 3166-1 alpha-2, lowercase) and is
	// optional: the zero value falls back to a generic globe flag in the UI.
	t.Country = strings.ToLower(strings.TrimSpace(t.Country))
	if t.Country != "" && !topologyCountryRE.MatchString(t.Country) {
		return fmt.Errorf("country must be a two-letter ISO 3166-1 alpha-2 code or empty")
	}
	if len(t.TunnelIDs) > topologyMaxIDs {
		return fmt.Errorf("too many tunnel_ids")
	}
	if t.TunnelIDs == nil {
		t.TunnelIDs = []string{}
	}
	seen := map[string]bool{}
	for _, id := range t.TunnelIDs {
		if !topologyTunnelRE.MatchString(id) || seen[id] {
			return fmt.Errorf("invalid or duplicate tunnel_id %q", id)
		}
		seen[id] = true
	}
	return nil
}

func topologyEndpoints(t topologyTunnel) []string {
	out := []string{}
	for _, value := range append([]string{t.RemoteIP}, t.RemoteIPs...) {
		if ip := net.ParseIP(value); ip != nil {
			out = append(out, ip.String())
		}
	}
	return out
}

func topologyHasIP(endpoints []string, ip string) bool {
	for _, endpoint := range endpoints {
		if endpoint == ip {
			return true
		}
	}
	return false
}

// validateTopologyBindings checks ownership even for temporarily absent tunnels.
// Renaming an existing group is allowed when inventory has become stale; adding
// IDs or editing its server identity must validate against current inventory.
func validateTopologyBindings(t Topology, all []Topology, inventory []topologyTunnel, checkInventory bool) (int, error) {
	for _, other := range all {
		if other.ID == t.ID {
			continue
		}
		if strings.EqualFold(other.Name, t.Name) {
			return http.StatusConflict, fmt.Errorf("a topology with this name already exists")
		}
		for _, id := range t.TunnelIDs {
			for _, owned := range other.TunnelIDs {
				if id == owned {
					return http.StatusConflict, fmt.Errorf("tunnel %s already belongs to another topology", id)
				}
			}
		}
	}
	if !checkInventory {
		return 0, nil
	}
	byID := map[string]topologyTunnel{}
	for _, tunnel := range inventory {
		byID[tunnel.ID] = tunnel
	}
	var common []string
	for _, id := range t.TunnelIDs {
		tunnel, ok := byID[id]
		if !ok {
			return http.StatusBadRequest, fmt.Errorf("tunnel %s is not in the current inventory", id)
		}
		if tunnel.role != "" && tunnel.role != t.Role {
			return http.StatusConflict, fmt.Errorf("role does not match assigned tunnel %s", id)
		}
		if t.LocalIP != "" && tunnel.localIP != "" && !net.ParseIP(t.LocalIP).Equal(net.ParseIP(tunnel.localIP)) {
			return http.StatusConflict, fmt.Errorf("local_ip does not match assigned tunnel %s", id)
		}
		endpoints := topologyEndpoints(tunnel)
		if len(endpoints) == 0 {
			continue // genuinely unknown remote endpoint: pending metadata is allowed
		}
		if t.RemoteIP != "" && !topologyHasIP(endpoints, t.RemoteIP) {
			return http.StatusConflict, fmt.Errorf("remote_ip does not match assigned tunnel %s", id)
		}
		if common == nil {
			common = endpoints
		} else {
			intersection := []string{}
			for _, ip := range common {
				if topologyHasIP(endpoints, ip) {
					intersection = append(intersection, ip)
				}
			}
			if len(intersection) == 0 {
				return http.StatusConflict, fmt.Errorf("assigned tunnels must connect the same remote server")
			}
			common = intersection
		}
	}
	return 0, nil
}

func handleTopologiesGet(w http.ResponseWriter, r *http.Request) {
	inventory := topologyInventory()
	if inventory == nil {
		inventory = []topologyTunnel{}
	}
	topologyMu.Lock()
	topologies, err := loadTopologies()
	topologyMu.Unlock()
	if err != nil {
		topologyError(w, http.StatusInternalServerError, "cannot read topology collection")
		return
	}
	writeJSON(w, map[string]any{"topologies": topologies, "tunnels": inventory})
}

func handleTopologiesPost(w http.ResponseWriter, r *http.Request) {
	t := Topology{Name: "New topology", Role: "iran", TunnelIDs: []string{}, CreatedAt: time.Now().Unix()}
	if _, err := decodeTopologyEdit(w, r, &t); err != nil {
		topologyError(w, http.StatusBadRequest, err.Error())
		return
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		topologyError(w, http.StatusInternalServerError, "cannot generate topology ID")
		return
	}
	t.ID = "topo_" + hex.EncodeToString(random[:])
	inventory := topologyInventory()
	topologyMu.Lock()
	defer topologyMu.Unlock()
	all, err := loadTopologies()
	if err != nil {
		topologyError(w, http.StatusInternalServerError, "cannot read topology collection")
		return
	}
	if len(all) >= topologyMaxCount {
		topologyError(w, http.StatusConflict, "at most 100 topologies are allowed")
		return
	}
	if status, err := validateTopologyBindings(t, all, inventory, true); err != nil {
		topologyError(w, status, err.Error())
		return
	}
	if err := saveTopologies(append(all, t)); err != nil {
		topologyError(w, http.StatusInternalServerError, "cannot save topology collection")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	writeJSON(w, map[string]any{"status": "ok", "topology": t})
}

func handleTopologyPatch(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !topologyIDRE.MatchString(id) {
		topologyError(w, http.StatusBadRequest, "invalid topology ID")
		return
	}
	inventory := topologyInventory()
	topologyMu.Lock()
	defer topologyMu.Unlock()
	all, err := loadTopologies()
	if err != nil {
		topologyError(w, http.StatusInternalServerError, "cannot read topology collection")
		return
	}
	for i, old := range all {
		if old.ID != id {
			continue
		}
		t := old
		fields, err := decodeTopologyEdit(w, r, &t)
		if err != nil {
			topologyError(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(fields) == 0 {
			topologyError(w, http.StatusBadRequest, "provide at least one editable field")
			return
		}
		identityChanged := t.Role != old.Role || t.LocalIP != old.LocalIP || t.RemoteIP != old.RemoteIP
		_, bindingsChanged := fields["tunnel_ids"]
		if identityChanged && t.Role != old.Role {
			// Unknown engine roles cannot justify reinterpreting a still-bound
			// real tunnel. Detach first; known roles are checked below as well.
			for _, retained := range t.TunnelIDs {
				for _, previous := range old.TunnelIDs {
					if retained == previous {
						topologyError(w, http.StatusConflict, "detach assigned tunnels before changing role")
						return
					}
				}
			}
		}
		if status, err := validateTopologyBindings(t, all, inventory, identityChanged || bindingsChanged); err != nil {
			topologyError(w, status, err.Error())
			return
		}
		all[i] = t
		if err := saveTopologies(all); err != nil {
			topologyError(w, http.StatusInternalServerError, "cannot save topology collection")
			return
		}
		writeJSON(w, map[string]any{"status": "ok", "topology": t})
		return
	}
	topologyError(w, http.StatusNotFound, "topology not found")
}

// DELETE removes only grouping metadata after its assigned tunnels have been
// deleted. Stopped/unhealthy tunnels still exist; missing stored IDs do not
// require a separate detach. No engine resource or service is changed here.
func handleTopologyDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !topologyIDRE.MatchString(id) {
		topologyError(w, http.StatusBadRequest, "invalid topology ID")
		return
	}
	// Read inventory before the metadata lock: inventory may take engine/cache
	// locks, but must never collect status, probe links or run host commands.
	inventory := topologyInventory()
	present := make(map[string]bool, len(inventory))
	for _, tunnel := range inventory {
		present[tunnel.ID] = true
	}
	topologyMu.Lock()
	defer topologyMu.Unlock()
	all, err := loadTopologies()
	if err != nil {
		topologyError(w, http.StatusInternalServerError, "cannot read topology collection")
		return
	}
	for i, t := range all {
		if t.ID != id {
			continue
		}
		if inventory == nil && len(t.TunnelIDs) > 0 {
			topologyError(w, http.StatusServiceUnavailable, "cannot verify tunnel inventory")
			return
		}
		remaining := []string{}
		for _, tunnelID := range t.TunnelIDs {
			if present[tunnelID] {
				remaining = append(remaining, tunnelID)
			}
		}
		if len(remaining) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":                "delete assigned tunnels before deleting this topology",
				"code":                 "topology_has_tunnels",
				"remaining_tunnel_ids": remaining,
			})
			return
		}
		all = append(all[:i], all[i+1:]...)
		if err := saveTopologies(all); err != nil {
			topologyError(w, http.StatusInternalServerError, "cannot save topology collection")
			return
		}
		// Deleting the grouping purges the usage records still keyed by its
		// listed tunnels (all of them are absent from the inventory here, so no
		// live tunnel's history is touched): nothing of a deleted topology may
		// keep rendering anywhere.
		purgeTunnelUsage(t.TunnelIDs...)
		writeJSON(w, map[string]any{"status": "ok", "id": id})
		return
	}
	topologyError(w, http.StatusNotFound, "topology not found")
}

// topologyBoundTunnelPorts maps every port owned by a tunnel already bound to
// any topology onto that topology's name. Tunnels have exclusive topology
// ownership, so a new tunnel must never reuse one of these ports.
func topologyBoundTunnelPorts() map[int]string {
	out := map[int]string{}
	topologyMu.Lock()
	all, err := loadTopologies()
	topologyMu.Unlock()
	if err != nil {
		return out
	}
	bound := map[string]string{}
	for _, t := range all {
		for _, id := range t.TunnelIDs {
			bound[id] = t.Name
		}
	}
	if len(bound) == 0 {
		return out
	}
	inventory := topologyConfiguredInventory([]topologyTunnel{})
	for _, tunnel := range inventory {
		name, ok := bound[tunnel.ID]
		if !ok {
			continue
		}
		for _, p := range tunnel.Ports {
			out[p] = name
		}
	}
	return out
}

func topologyRole(role string) string {
	switch {
	case strings.HasPrefix(role, "iran"), role == "hub", role == "client":
		return "iran"
	case strings.HasPrefix(role, "foreign"), strings.HasPrefix(role, "kharej"), role == "node", role == "server":
		return "foreign"
	}
	return ""
}

func topologyIP(value string) string {
	if ip := net.ParseIP(strings.TrimSpace(value)); ip != nil {
		return ip.String()
	}
	return ""
}

// readTopologyInventory reads only published status. Unlike currentSnapshot's
// cold-start path, it never kicks off collection/probes from an HTTP handler.
func readTopologyInventory() []topologyTunnel {
	s := snapPtr.Load()
	if s == nil {
		s = &HubSnapshot{}
	}
	out := []topologyTunnel{}
	addPeer := func(id string, p peerLive, role string) {
		state := p.HealthState
		if state == "" {
			state = "unknown"
		}
		localIP := topologyIP(p.LocalPub)
		if id == "main" && localIP == "" {
			localIP = topologyIP(s.Local.LocalPub)
		}
		out = append(out, topologyTunnel{ID: id, Name: p.Name, Engine: p.Engine,
			RemoteIP: topologyIP(p.RemotePub), State: state, LatencyMs: p.LatencyMs,
			Ports: append([]int{}, p.Ports...), role: role, localIP: localIP})
	}
	local := s.Local
	if s.Main != nil && s.Main.Engine != "matrix" {
		addPeer("main", *s.Main, topologyRole(local.Role))
	} else if local.Engine != "matrix" && (local.Gre.Exists || local.FrpSvc != "" || local.FrpUp || len(local.ProxyPorts) > 0) {
		state, latency := "down", -1.0
		if local.FrpUp || local.Gre.Exists {
			state = "degraded"
		}
		if s.LocalSession || local.PingOK {
			state = "healthy"
			if s.LocalRTT > 0 {
				latency = s.LocalRTT
			}
		}
		out = append(out, topologyTunnel{ID: "main", Name: "Tunnel", Engine: local.Engine,
			RemoteIP: topologyIP(local.RemotePub), State: state, LatencyMs: latency,
			Ports: append([]int{}, local.ProxyPorts...), role: topologyRole(local.Role), localIP: topologyIP(local.LocalPub)})
	}
	for _, p := range s.Peers {
		if p.ID > 0 {
			addPeer("peer:"+strconv.Itoa(p.ID), p, "iran")
		}
	}
	if mx := local.Matrix; mx != nil && mx.Enabled {
		t := topologyTunnel{ID: "matrix", Name: "Matrix", Engine: "matrix", Family: mx.Family,
			RemoteIP: topologyIP(mx.RemotePub), State: mx.HealthState, LatencyMs: mx.LatencyMs,
			Ports: append([]int{}, mx.Ports...), role: topologyRole(mx.Role)}
		if t.State == "" {
			t.State = "unknown"
		}
		if mx.Role == "node" {
			if t.RemoteIP == "" {
				t.RemoteIP = topologyIP(mx.Addr)
			}
			// A registration bundle's Panel URL and transport Addr explicitly
			// identify the same hub, including a v4 panel + v6 transport pair.
			st := loadMatrix() // read-only; no matrixPublicState/live ping helpers
			if st.Role == "node" && st.Enabled && st.Addr == mx.Addr {
				if u, err := url.Parse(st.Panel); err == nil {
					if ip := topologyIP(u.Hostname()); ip != "" && ip != t.RemoteIP {
						t.RemoteIPs = []string{ip}
					}
				}
			}
		} else {
			// Addr is the Matrix outer transport, not necessarily this server's
			// public v4 identity. Do not force a v6 transport onto local_ip.
			if t.RemoteIP == "" && len(mx.Peers) == 1 {
				t.RemoteIP = topologyIP(mx.Peers[0].Addr)
			}
		}
		out = append(out, t)
	}
	if bagher := s.Bagher; bagher != nil && bagher["has_config"] == true {
		addBagher := func(id string, b map[string]any) {
			name, _ := b["name"].(string)
			remote, _ := b["remote_host"].(string)
			role, _ := bagher["role"].(string)
			ports, _ := b["ports"].([]int)
			latency, ok := b["latency_ms"].(float64)
			if !ok || latency <= 0 {
				latency = -1
			}
			state := "down"
			if b["is_active"] == true {
				state = "degraded"
			}
			if b["connected"] == true {
				state = "healthy"
			}
			out = append(out, topologyTunnel{ID: id, Name: name, Engine: "bagher", RemoteIP: topologyIP(remote),
				State: state, LatencyMs: latency, Ports: append([]int{}, ports...), role: topologyRole(role)})
		}
		links, _ := bagher["links"].([]map[string]any)
		if len(links) == 0 {
			addBagher("bagher", bagher)
		} else {
			for _, link := range links {
				id, _ := link["id"].(int)
				if id > 0 {
					canonical := "bagher"
					if id != 1 {
						canonical += ":" + strconv.Itoa(id)
					}
					addBagher(canonical, link)
				}
			}
		}
	}
	// Config reads and already cached health are safe. xrayCachedHealth and
	// xrayLinkView would refresh probes, so deliberately do not call them.
	xrayMu.Lock()
	links := loadXrayState().Links
	xrayMu.Unlock()
	xrayHealthCache.Lock()
	for _, link := range links {
		if link.ID <= 0 {
			continue
		}
		state, latency := "unknown", -1.0
		if h, ok := xrayHealthCache.m[link.ID]; ok {
			switch h.Link {
			case "up":
				state = "healthy"
			case "listening", "checking":
				state = "degraded"
			case "down", "off":
				state = "down"
			}
			if h.ProbeMS > 0 {
				latency = h.ProbeMS
			} else if h.PingMS > 0 {
				latency = h.PingMS
			}
		}
		if link.Disabled {
			state, latency = "down", -1
		}
		out = append(out, topologyTunnel{ID: "xray:" + strconv.Itoa(link.ID), Name: link.Name,
			Engine: "xray", RemoteIP: topologyIP(link.Remote), State: state, LatencyMs: latency,
			Ports: append([]int{}, link.Ports...), role: topologyRole(link.Side)})
	}
	xrayHealthCache.Unlock()
	return topologyConfiguredInventory(out)
}

// Supplement published health with fresh config reads: a cold/old snapshot must
// not make a stopped configured tunnel look deleted. Snapshot-only legacy
// resources remain conservatively present until the collector drops them.
// nil means a source could not be read/decoded, not a confirmed empty inventory.
func topologyConfiguredInventory(out []topologyTunnel) []topologyTunnel {
	seen := make(map[string]bool, len(out))
	for _, tunnel := range out {
		seen[tunnel.ID] = true
	}
	add := func(t topologyTunnel) {
		if !seen[t.ID] {
			seen[t.ID] = true
			t.State, t.LatencyMs = "unknown", -1
			if t.Ports == nil {
				t.Ports = []int{}
			}
			out = append(out, t)
		}
	}
	read := func(path string, dst any) error {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(data)) == "null" {
			return fmt.Errorf("invalid inventory source")
		}
		return json.Unmarshal(data, dst)
	}
	var peers struct {
		Peers []peerRecord `json:"peers"`
	}
	if err := read(peersFile(), &peers); err != nil {
		return nil
	}
	for _, p := range peers.Peers {
		if p.ID > 0 {
			add(topologyTunnel{ID: "peer:" + strconv.Itoa(p.ID), Name: p.Name, Engine: p.Engine,
				RemoteIP: topologyIP(p.RemotePub), Ports: append([]int{}, p.Ports...), role: "iran", localIP: topologyIP(p.LocalPub)})
		}
	}
	// Every Matrix instance is a distinct inventory tunnel: "matrix" (legacy)
	// plus matrix2, matrix3, ... State files are read directly (no matrixMu:
	// saves rename atomically, so readers never see partial writes) so this
	// helper is safe to call while matrixMu is held elsewhere.
	for _, in := range matrixInstances() {
		var mx matrixState
		if err := read(matrixStatePathOf(in), &mx); err != nil {
			return nil
		}
		if mx.Enabled || mx.Role != "" {
			name := "Matrix"
			if in.suffix() != "" {
				name += " " + in.suffix()
			}
			add(topologyTunnel{ID: in.ID, Name: name, Engine: "matrix", Family: mx.Family,
				RemoteIP: topologyIP(mx.Addr), Ports: append([]int{}, mx.Ports...), role: topologyRole(mx.Role)})
		}
	}
	var bagher SSHFwdConfig
	sshFwdMu.Lock()
	bagherErr := read(sshFwdConfigFile(), &bagher)
	sshFwdMu.Unlock()
	if bagherErr != nil {
		return nil
	}
	if bagher.Enabled || strings.TrimSpace(bagher.RemoteHost) != "" || len(bagher.Ports) > 0 {
		add(topologyTunnel{ID: "bagher", Name: "Bagher", Engine: "bagher", RemoteIP: topologyIP(bagher.RemoteHost),
			Ports: append([]int{}, bagher.Ports...), role: topologyRole(bagher.Role)})
	}
	for _, link := range bagher.Links {
		if link.ID > 0 {
			id := "bagher"
			if link.ID != 1 {
				id += ":" + strconv.Itoa(link.ID)
			}
			add(topologyTunnel{ID: id, Name: "Bagher", Engine: "bagher", RemoteIP: topologyIP(link.RemoteHost),
				Ports: append([]int{}, link.Ports...), role: topologyRole(bagher.Role)})
		}
	}
	// Validate Xray's source too: loadXrayState otherwise silently maps a read
	// or decode failure to zero links. Disabled links are deliberately retained.
	var xray xrayState
	xrayMu.Lock()
	xrayErr := read(xrayStatePath(), &xray)
	xrayMu.Unlock()
	if xrayErr != nil {
		return nil
	}
	for _, link := range xray.Links {
		if link.ID > 0 {
			add(topologyTunnel{ID: "xray:" + strconv.Itoa(link.ID), Name: link.Name, Engine: "xray",
				RemoteIP: topologyIP(link.Remote), Ports: append([]int{}, link.Ports...), role: topologyRole(link.Side)})
		}
	}
	for _, c := range staleConfigCandidates() {
		if _, err := os.Stat(c.path); err == nil {
			engine := "frp"
			if c.engine == "backhaul" {
				engine = "backhaul"
			}
			add(topologyTunnel{ID: "main", Name: "Tunnel", Engine: engine, role: topologyRole(c.role)})
			break
		} else if !os.IsNotExist(err) {
			return nil
		}
	}
	return out
}
