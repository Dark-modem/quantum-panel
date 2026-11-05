// pasarguard.go — store surface for the PasarGuard integration (migration
// 3): connection settings (URL plain; credentials sealed by the caller
// with the per-install key) and the pg_nodes mapping table.

package store

import (
	"database/sql"
	"errors"
	"time"
)

// ---- settings KV -----------------------------------------------------------

// SetSetting stores one settings value (upsert).
func (s *Store) SetSetting(key, value string) error {
	if key == "" || len(key) > 64 || len(value) > 64<<10 {
		return errors.New("store: invalid setting key/value")
	}
	_, err := s.db.Exec(`INSERT INTO pasarguard_settings (key, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, time.Now().Unix())
	return err
}

// Setting reads one settings value ("" = unset).
func (s *Store) Setting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM pasarguard_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// ---- pg_nodes mapping -------------------------------------------------------

// PGNode is one (foreign server × iran datacenter) → PasarGuard node
// mapping. APIKeyEnc and the credentials are sealed by the panel layer
// (AES-256-GCM with the per-install key file); the store never sees
// plaintext secrets.
type PGNode struct {
	ID           int64  `json:"id"`
	ServerID     int64  `json:"server_id"`
	DatacenterID int64  `json:"datacenter_id"`
	PGNodeID     int64  `json:"pg_node_id"`
	APIKeyEnc    string `json:"-"`
	ServerCA     string `json:"server_ca,omitempty"`
	NodePort     int    `json:"node_port"`
	Note         string `json:"note,omitempty"`
	CreatedAt    int64  `json:"created_at"`
}

// UpsertPGNode inserts or updates the mapping for a pair.
func (s *Store) UpsertPGNode(n PGNode) (int64, error) {
	if n.NodePort == 0 {
		n.NodePort = 62050
	}
	if n.CreatedAt == 0 {
		n.CreatedAt = time.Now().Unix()
	}
	_, err := s.db.Exec(`INSERT INTO pg_nodes (server_id, datacenter_id, pg_node_id, api_key_enc, server_ca, node_port, note, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(server_id, datacenter_id) DO UPDATE SET pg_node_id = excluded.pg_node_id,
		  api_key_enc = excluded.api_key_enc, server_ca = excluded.server_ca,
		  node_port = excluded.node_port, note = excluded.note`,
		n.ServerID, n.DatacenterID, n.PGNodeID, n.APIKeyEnc, n.ServerCA, n.NodePort, n.Note, n.CreatedAt)
	if err != nil {
		return 0, err
	}
	return s.lookupID(`SELECT id FROM pg_nodes WHERE server_id = ? AND datacenter_id = ?`,
		n.ServerID, n.DatacenterID)
}

// ListPGNodes returns every mapping.
func (s *Store) ListPGNodes() ([]PGNode, error) {
	rows, err := s.db.Query(`SELECT id, server_id, datacenter_id, pg_node_id, api_key_enc, server_ca, node_port, note, created_at
		FROM pg_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PGNode
	for rows.Next() {
		var n PGNode
		if err := rows.Scan(&n.ID, &n.ServerID, &n.DatacenterID, &n.PGNodeID, &n.APIKeyEnc,
			&n.ServerCA, &n.NodePort, &n.Note, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// GetPGNodeByPair resolves the mapping for one (server, datacenter) pair.
func (s *Store) GetPGNodeByPair(serverID, datacenterID int64) (PGNode, error) {
	var n PGNode
	err := s.db.QueryRow(`SELECT id, server_id, datacenter_id, pg_node_id, api_key_enc, server_ca, node_port, note, created_at
		FROM pg_nodes WHERE server_id = ? AND datacenter_id = ?`, serverID, datacenterID).
		Scan(&n.ID, &n.ServerID, &n.DatacenterID, &n.PGNodeID, &n.APIKeyEnc,
			&n.ServerCA, &n.NodePort, &n.Note, &n.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// DeletePGNode removes a mapping row (not the PasarGuard node itself).
func (s *Store) DeletePGNode(id int64) error {
	res, err := s.db.Exec(`DELETE FROM pg_nodes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	return requireRow(res)
}
