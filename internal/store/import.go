package store

// Legacy JSON import: agents.json and topologies.json from the panel's
// current state files. The store parses both with its own local structs
// (no imports from internal/panel) and maps them per
// 00-ADMIN/DATA-MODEL.md. Import is idempotent (upserts) and never
// deletes rows the legacy files no longer mention.

import (
	"encoding/json"
	"fmt"
)

// legacyAgentRecord mirrors the panel's agentRecord (agentreg.go) — only
// the fields the store keeps.
type legacyAgentRecord struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	KeyHash         string `json:"key_hash"`
	Role            string `json:"role"`
	Version         string `json:"version"`
	Hostname        string `json:"hostname"`
	PublicIP        string `json:"public_ip"`
	Status          string `json:"status"`
	LastSeen        int64  `json:"last_seen"`
	AppliedRevision string `json:"applied_revision"`
}

type legacyAgentRegistry struct {
	Agents []legacyAgentRecord `json:"agents"`
}

// legacyTopology mirrors the panel's Topology (topology.go): a PAIR of
// endpoints (local/remote IP), with the tunnels it owns.
type legacyTopology struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Role      string   `json:"role"` // side of the panel host ("iran"/"foreign")
	LocalIP   string   `json:"local_ip"`
	RemoteIP  string   `json:"remote_ip"`
	TunnelIDs []string `json:"tunnel_ids"`
	Disabled  bool     `json:"disabled,omitempty"`
}

type legacyTopologyStore struct {
	Topologies []legacyTopology `json:"topologies"`
}

// ImportStats reports what one import pass did.
type ImportStats struct {
	AgentsUpserted int
	Topologies     int
	Servers        int
	Tunnels        int
}

// ImportAgentsJSON imports the legacy agents.json bytes.
func (s *Store) ImportAgentsJSON(data []byte) (ImportStats, error) {
	var reg legacyAgentRegistry
	if err := json.Unmarshal(data, &reg); err != nil {
		return ImportStats{}, fmt.Errorf("agents.json: %w", err)
	}
	var st ImportStats
	for _, a := range reg.Agents {
		if a.ID == "" {
			continue
		}
		err := s.UpsertAgent(Agent{
			ID:              a.ID,
			Name:            a.Name,
			Role:            a.Role,
			KeyHash:         a.KeyHash,
			Status:          a.Status,
			Version:         a.Version,
			Hostname:        a.Hostname,
			PublicIP:        a.PublicIP,
			LastSeen:        a.LastSeen,
			AppliedRevision: a.AppliedRevision,
		})
		if err != nil {
			return st, fmt.Errorf("agent %s: %w", a.ID, err)
		}
		st.AgentsUpserted++
	}
	return st, nil
}

// ImportTopologiesJSON imports the legacy topologies.json bytes. Servers
// are resolved/created by IP; the legacy topology role marks the local
// side ("iran" → local=iran/remote=foreign, else the reverse).
func (s *Store) ImportTopologiesJSON(data []byte) (ImportStats, error) {
	var store legacyTopologyStore
	if err := json.Unmarshal(data, &store); err != nil {
		return ImportStats{}, fmt.Errorf("topologies.json: %w", err)
	}
	var st ImportStats
	for _, lt := range store.Topologies {
		if lt.ID == "" {
			continue
		}
		// Legacy topology names are unique in the file; key the new
		// topology by its legacy id so re-imports upsert.
		topoName := lt.ID
		topoID, err := s.UpsertTopology(Topology{Name: topoName, Enabled: !lt.Disabled})
		if err != nil {
			return st, fmt.Errorf("topology %s: %w", lt.ID, err)
		}
		st.Topologies++

		localRole, remoteRole := "iran", "foreign"
		if lt.Role == "foreign" {
			localRole, remoteRole = "foreign", "iran"
		}
		localID, err := s.upsertLegacyServer("legacy-local-"+lt.LocalIP, localRole, lt.LocalIP)
		if err != nil {
			return st, err
		}
		remoteID, err := s.upsertLegacyServer("legacy-remote-"+lt.RemoteIP, remoteRole, lt.RemoteIP)
		if err != nil {
			return st, err
		}
		st.Servers += 2 // both upserts counted cheaply; idempotent
		for _, sid := range []int64{localID, remoteID} {
			if err := s.AddTopologyMember(topoID, sid); err != nil {
				return st, err
			}
		}
		for _, tid := range lt.TunnelIDs {
			if tid == "" {
				continue
			}
			// Legacy tunnel ids are namespaced per topology ("main" can
			// exist in several topologies); the imported key makes that
			// explicit. Legacy tunnels carry their engine elsewhere; the
			// store keeps the topology linkage with an empty engine.
			if _, err := s.UpsertTunnel(Tunnel{
				TunnelKey:      lt.ID + "/" + tid,
				TopologyID:     topoID,
				LocalServerID:  localID,
				RemoteServerID: remoteID,
				Engine:         "",
				Enabled:        !lt.Disabled,
			}); err != nil {
				return st, fmt.Errorf("tunnel %s: %w", tid, err)
			}
			st.Tunnels++
		}
	}
	return st, nil
}

// upsertLegacyServer resolves a legacy endpoint by name; the name is
// stable ("legacy-<side>-<ip>") so re-imports upsert.
func (s *Store) upsertLegacyServer(name, role, addr string) (int64, error) {
	return s.UpsertServer(Server{Name: name, Role: role, Address: addr})
}
