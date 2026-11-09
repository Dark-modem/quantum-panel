// ports.go — Phase 14 port inventory (migration 7): the panel's own port
// ledger (port_allocations) and the latest listening-port snapshot each
// agent reported (agent_listening).
//
// Why both tables exist: the forward-rule CRUD used to blind-book listen
// ports — a conflict was only discovered on the agent as a failed bind.
// port_allocations makes the panel's reservations explicit (and
// releasable), agent_listening makes the agent's reality visible, and the
// allocator (panel layer) unions both with the well-known ports before
// handing out anything.

package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPortTaken reports the (server_id, port) slot is already allocated.
// Callers match with errors.Is — the REST layer turns it into the
// structured E-ENGINE-PORT-CONFLICT envelope.
var ErrPortTaken = errors.New("store: port already allocated")

// PortAllocation is one reserved port on one server. OwnerKey names WHO
// holds it ("forward:<rule id>" today; other subsystems can book their
// own) so deleting the owner can release the reservation without the
// caller remembering the port number.
type PortAllocation struct {
	ID          int64  `json:"id"`
	ServerID    int64  `json:"server_id"`
	Port        int    `json:"port"`
	Proto       string `json:"proto"`
	Purpose     string `json:"purpose,omitempty"`
	OwnerKey    string `json:"owner_key,omitempty"`
	AllocatedAt int64  `json:"allocated_at"`
}

// ListenRow is one listening socket an agent observed on its host (the
// panel-side mirror of protocol.ListenPort; Addr is deliberately not
// persisted — 0.0.0.0 vs 127.0.0.1 does not change port-availability
// policy, and bind addresses are host-local detail).
type ListenRow struct {
	Proto string `json:"proto"`
	Port  int    `json:"port"`
	Proc  string `json:"proc,omitempty"`
}

// ---- port_allocations ----------------------------------------------------

// ListPortAllocations lists the panel's reservations for one server,
// lowest port first.
func (s *Store) ListPortAllocations(serverID int64) ([]PortAllocation, error) {
	rows, err := s.db.Query(`SELECT id, server_id, port, proto, purpose, owner_key, allocated_at
		FROM port_allocations WHERE server_id = ? ORDER BY port`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PortAllocation
	for rows.Next() {
		var a PortAllocation
		if err := rows.Scan(&a.ID, &a.ServerID, &a.Port, &a.Proto, &a.Purpose, &a.OwnerKey, &a.AllocatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AllocatePort records one reservation. The (server_id, port) UNIQUE
// constraint is the single source of truth for "taken": the INSERT is
// raced through on purpose so two concurrent allocators cannot both win.
func (s *Store) AllocatePort(serverID int64, port int, proto, purpose, ownerKey string) error {
	if serverID == 0 {
		return fmt.Errorf("store: allocate needs a server")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("store: port out of range")
	}
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("store: proto must be tcp or udp")
	}
	if len(purpose) > 128 || len(ownerKey) > 128 {
		return fmt.Errorf("store: purpose/owner_key too long")
	}
	_, err := s.db.Exec(`INSERT INTO port_allocations (server_id, port, proto, purpose, owner_key, allocated_at)
		VALUES (?,?,?,?,?,?)`, serverID, port, proto, purpose, ownerKey, time.Now().Unix())
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return ErrPortTaken
	}
	return err
}

// ReleasePort drops one reservation (no-op when free).
func (s *Store) ReleasePort(serverID int64, port int) error {
	_, err := s.db.Exec(`DELETE FROM port_allocations WHERE server_id = ? AND port = ?`, serverID, port)
	return err
}

// ReleasePortOwner drops every reservation held by one owner on one
// server (a forward rule's delete releases "forward:<id>" without the
// handler tracking which ports it had).
func (s *Store) ReleasePortOwner(serverID int64, ownerKey string) error {
	_, err := s.db.Exec(`DELETE FROM port_allocations WHERE server_id = ? AND owner_key = ?`, serverID, ownerKey)
	return err
}

// ---- agent_listening -------------------------------------------------------

// UpsertAgentListening replaces one agent's listening snapshot. REPLACE,
// not plain upsert, on purpose: the report is the agent's FULL inventory
// right now — a socket that closed must stop reserving its port, and an
// upsert-only write would keep a dead row (and a blocked port) forever.
// Best-effort callers may pass an empty rows slice to clear the snapshot.
func (s *Store) UpsertAgentListening(agentID string, rows []ListenRow, seenAt int64) error {
	if agentID == "" {
		return fmt.Errorf("store: agent id required")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM agent_listening WHERE agent_id = ?`, agentID); err != nil {
		return err
	}
	for _, r := range rows {
		if r.Port < 1 || r.Port > 65535 || (r.Proto != "tcp" && r.Proto != "udp") {
			continue // nonsense rows never enter the inventory
		}
		if len(r.Proc) > 128 {
			r.Proc = r.Proc[:128]
		}
		if _, err := tx.Exec(`INSERT INTO agent_listening (agent_id, proto, port, proc, seen_at) VALUES (?,?,?,?,?)
			ON CONFLICT(agent_id, proto, port) DO UPDATE SET proc = excluded.proc, seen_at = excluded.seen_at`,
			agentID, r.Proto, r.Port, r.Proc, seenAt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListAgentListening returns the latest reported listening ports for the
// agents linked to the given servers (agent_listening joins agents on
// server_id — an unlinked agent's rows are invisible by construction).
// Servers with no data are simply absent from the map.
func (s *Store) ListAgentListening(serverIDs []int64) (map[int64][]ListenRow, error) {
	out := map[int64][]ListenRow{}
	if len(serverIDs) == 0 {
		return out, nil
	}
	// Bounded IN-list: topology members are at most dozens.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(serverIDs)), ",")
	args := make([]any, len(serverIDs))
	for i, id := range serverIDs {
		args[i] = id
	}
	rows, err := s.db.Query(`SELECT a.server_id, l.proto, l.port, l.proc
		FROM agent_listening l JOIN agents a ON a.id = l.agent_id
		WHERE a.server_id IN (`+placeholders+`)
		ORDER BY a.server_id, l.port, l.proto`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var sid int64
		var r ListenRow
		if err := rows.Scan(&sid, &r.Proto, &r.Port, &r.Proc); err != nil {
			return nil, err
		}
		out[sid] = append(out[sid], r)
	}
	return out, rows.Err()
}

// AgentListeningForServer is the single-server convenience form used by
// the allocator's reservation set.
func (s *Store) AgentListeningForServer(serverID int64) ([]ListenRow, error) {
	m, err := s.ListAgentListening([]int64{serverID})
	if err != nil {
		return nil, err
	}
	return m[serverID], nil
}
