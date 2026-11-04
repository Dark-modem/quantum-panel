package panel

// topogen.go — the topology generator. Given Iran servers, foreign servers,
// tunnels-per-pair and one engine per slot, it generates EVERY tunnel of
// the topology deterministically: topology + members + tunnels with stable
// keys ("tun-<topo>-<local>-<remote>-<slot>"), plus per-slot engine and
// params. Numbers are variable by design (N Iran × M foreign × K tunnels);
// the brief's deployment shape (2×2×3 = 12 per topology, two topologies
// sharing the foreign servers) is just one instance of it.
//
// The generator is POLICY-stupid: it creates rows, it never touches agents
// (desired state is a separate compile+push step, so a generated topology
// goes live only when the panel pushes it).

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// TopoGenRequest is the mesh API's generate body.
type TopoGenRequest struct {
	Name           string            `json:"name"`              // unique topology name
	IranServers    []int64           `json:"iran_servers"`      // server IDs, role=iran
	ForeignServers []int64           `json:"foreign_servers"`   // server IDs, role=foreign
	TunnelsPerPair int               `json:"tunnels_per_pair"`  // 1..26
	Engines        []string          `json:"engines"`           // exactly TunnelsPerPair entries, one per slot
	EngineParams   []json.RawMessage `json:"engine_params"`     // optional: 0 or TunnelsPerPair entries
	Enabled        bool              `json:"enabled"`
	HealthCheck    json.RawMessage   `json:"health_check,omitempty"` // protocol.HealthCheckParams shape
	Failover       json.RawMessage   `json:"failover,omitempty"`     // protocol.FailoverRules shape
}

// topoGenResult is what the API answers with.
type topoGenResult struct {
	TopologyID int64          `json:"topology_id"`
	Tunnels    []store.Tunnel `json:"tunnels"`
	Warnings   []string       `json:"warnings,omitempty"`
}

// engineIDRe constrains engine ids coming from untrusted input (they end
// up in tunnel rows and desired state).
var engineIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// builtinEngines are the engines the panel knows exist even without an
// agent-reported catalog (ENGINE-GUIDE.md). An engine outside this list is
// allowed but warned about: 15+ engines are planned, the panel must not be
// the reason a new one cannot be declared.
var builtinEngines = map[string]bool{"matrix": true, "tinc": true}

// GenerateTopology validates the request and writes the full topology.
// It is transactional per entity but deliberately NOT one big SQL
// transaction: partial generation is visible and re-running the same
// request converges (unique keys) instead of doubling.
func GenerateTopology(req TopoGenRequest) (*topoGenResult, error) {
	if strings.TrimSpace(req.Name) == "" || len(req.Name) > 64 || !topoSlugRe.MatchString(req.Name) {
		return nil, fmt.Errorf("name must be 1-64 chars of [a-z0-9-_]")
	}
	if len(req.IranServers) == 0 || len(req.ForeignServers) == 0 {
		return nil, fmt.Errorf("need at least one iran and one foreign server")
	}
	if req.TunnelsPerPair < 1 || req.TunnelsPerPair > 26 {
		return nil, fmt.Errorf("tunnels_per_pair must be 1-26")
	}
	if len(req.Engines) != req.TunnelsPerPair {
		return nil, fmt.Errorf("engines must have exactly tunnels_per_pair=%d entries", req.TunnelsPerPair)
	}
	if len(req.EngineParams) != 0 && len(req.EngineParams) != req.TunnelsPerPair {
		return nil, fmt.Errorf("engine_params must be empty or have exactly %d entries", req.TunnelsPerPair)
	}
	for _, e := range req.Engines {
		if !engineIDRe.MatchString(e) {
			return nil, fmt.Errorf("invalid engine id %q", e)
		}
	}
	for i, p := range req.EngineParams {
		if len(p) > 16<<10 {
			return nil, fmt.Errorf("engine_params[%d] too large", i)
		}
		if !json.Valid(p) {
			return nil, fmt.Errorf("engine_params[%d] is not valid JSON", i)
		}
	}

	s, err := MeshStore()
	if err != nil {
		return nil, err
	}

	iran, err := resolveServers(s, req.IranServers, "iran")
	if err != nil {
		return nil, err
	}
	foreign, err := resolveServers(s, req.ForeignServers, "foreign")
	if err != nil {
		return nil, err
	}

	topo, err := s.UpsertTopology(store.Topology{
		Name: req.Name, Enabled: req.Enabled,
		HealthCheck: string(req.HealthCheck), Failover: string(req.Failover),
	})
	if err != nil {
		return nil, err
	}
	// UpsertTopology does not persist the balancing JSON (the v1 upsert
	// predates those columns); set them explicitly when provided.
	if len(req.HealthCheck) > 0 || len(req.Failover) > 0 {
		if err := s.SetTopologyBalancing(topo, string(req.HealthCheck), string(req.Failover)); err != nil {
			return nil, err
		}
	}
	for _, sv := range append(append([]store.Server{}, iran...), foreign...) {
		if err := s.AddTopologyMember(topo, sv.ID); err != nil {
			return nil, err
		}
	}

	res := &topoGenResult{TopologyID: topo}
	slug := topoSlug(req.Name)
	for _, local := range iran {
		for _, remote := range foreign {
			for slot := 0; slot < req.TunnelsPerPair; slot++ {
				key := fmt.Sprintf("tun-%s-%s-%s-%s", slug, serverSlugOf(local), serverSlugOf(remote), slotLetter(slot))
				params := "{}"
				if len(req.EngineParams) == req.TunnelsPerPair {
					params = string(req.EngineParams[slot])
				}
				tid, err := s.UpsertTunnel(store.Tunnel{
					TunnelKey:      key,
					TopologyID:     topo,
					LocalServerID:  local.ID,
					RemoteServerID: remote.ID,
					Engine:         req.Engines[slot],
					EngineParams:   params,
					Enabled:        req.Enabled,
				})
				if err != nil {
					return nil, fmt.Errorf("tunnel %s: %v", key, err)
				}
				if stored, err := s.GetTunnel(tid); err == nil {
					res.Tunnels = append(res.Tunnels, stored)
				}
				if !builtinEngines[req.Engines[slot]] {
					res.Warnings = append(res.Warnings,
						fmt.Sprintf("engine %q is not in the panel's builtin list; it is accepted because engine params are engine-validated (slot %s)", req.Engines[slot], key))
				}
			}
		}
	}
	meshAudit("panel", "mesh.topology.generate", fmt.Sprintf(`{"topology_id":%d,"tunnels":%d,"name":%q}`, topo, len(res.Tunnels), req.Name))
	return res, nil
}

// resolveServers loads and role-checks the requested server IDs.
func resolveServers(s *store.Store, ids []int64, role string) ([]store.Server, error) {
	seen := map[int64]bool{}
	out := make([]store.Server, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			return nil, fmt.Errorf("server %d listed twice", id)
		}
		seen[id] = true
		sv, err := s.GetServer(id)
		if err != nil {
			return nil, fmt.Errorf("server %d: %v", id, err)
		}
		if sv.Role != role {
			return nil, fmt.Errorf("server %s (%d) has role %q, want %q", sv.Name, sv.ID, sv.Role, role)
		}
		out = append(out, sv)
	}
	return out, nil
}

var topoSlugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// topoSlug reduces a topology name to its tunnel-key form.
func topoSlug(name string) string {
	return strings.Trim(strings.ToLower(name), "-_")
}

// serverSlugOf picks a short stable token for the tunnel key: the server
// name sanitized, trimmed to 12 chars.
func serverSlugOf(sv store.Server) string {
	n := strings.ToLower(sv.Name)
	var b strings.Builder
	for _, r := range n {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
		if b.Len() >= 12 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return fmt.Sprintf("srv%d", sv.ID)
	}
	return out
}

// slotLetter renders slot 0..25 as a..z (deterministic, readable keys).
func slotLetter(slot int) string {
	if slot >= 0 && slot < 26 {
		return string(rune('a' + slot))
	}
	return fmt.Sprintf("s%d", slot)
}

// validateEngineAgainstCatalog checks an engine id against the catalog the
// server's linked agent reported. Policy:
//   - no linked agent / empty catalog → builtin list decides; unknown ids
//     return a WARNING string (accepted — the engine validates its params);
//   - a catalog exists → the engine MUST be in it (hard error).
// The catalog entries are {"id": "...", ...}; only the id field matters.
func validateEngineAgainstCatalog(s *store.Store, serverID int64, engine string) (warning string, err error) {
	if !engineIDRe.MatchString(engine) {
		return "", fmt.Errorf("invalid engine id %q", engine)
	}
	agents, err := s.ListAgents()
	if err != nil {
		return "", err
	}
	var catalog string
	for _, a := range agents {
		if a.ServerID == serverID {
			v2, err := s.AgentV2(a.ID)
			if err == nil && strings.TrimSpace(v2.EngineCatalog) != "" {
				catalog = v2.EngineCatalog
				break
			}
		}
	}
	if catalog == "" {
		if builtinEngines[engine] {
			return "", nil
		}
		return "engine " + engine + " is not in the panel builtin list and this server has no agent-reported catalog; accepted (engine params are engine-validated)", nil
	}
	var entries []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(catalog), &entries); err != nil {
		return "", fmt.Errorf("stored engine catalog is not a JSON array of objects: %v", err)
	}
	for _, e := range entries {
		if e.ID == engine {
			return "", nil
		}
	}
	return "", fmt.Errorf("engine %q is not in the catalog reported by this server's agent", engine)
}
