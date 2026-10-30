package main

// Agent-local persistence: the enrollment identity (agent.json) and the
// desired-state journal (agent-state.json). Both live in configDir, carry
// 0600 permissions and are written atomically (temp file + rename), so the
// agent is self-healing after a crash or reboot: the last received desired
// state, its progress and the last controller-connection status all survive.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// agentIdentity is the enrollment record persisted at <configDir>/agent.json.
// The key never leaves this file (and is never logged).
type agentIdentity struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Server     string `json:"server"`
	BasePath   string `json:"base_path,omitempty"`
	Name       string `json:"name,omitempty"`
	TLSPin     string `json:"tls_pin,omitempty"` // SHA-256 of the leaf cert SPKI (TOFU)
	EnrolledAt string `json:"enrolled_at,omitempty"`
}

func agentIdentityPath() string { return filepath.Join(configDir, "agent.json") }

// agentLoadIdentity reads and validates the enrollment record.
func agentLoadIdentity() (agentIdentity, bool) {
	var id agentIdentity
	b, err := os.ReadFile(agentIdentityPath())
	if err != nil || json.Unmarshal(b, &id) != nil || id.ID == "" || id.Key == "" {
		return agentIdentity{}, false
	}
	return id, true
}

func agentSaveIdentity(id agentIdentity) error {
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return agentAtomicWrite(agentIdentityPath(), append(b, '\n'), 0600)
}

// agentWSStatus mirrors the last controller connection the run loop saw, so
// `gre-panel agent status` (a separate process) can report it.
type agentWSStatus struct {
	Connected bool   `json:"connected"`
	LastError string `json:"last_error,omitempty"`
	TS        int64  `json:"ts,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"` // when `agent run` started
}

// agentPersistedState is the journal at <configDir>/agent-state.json.
// DesiredStatus is "pending" (received, apply in flight or deferred),
// "applied" (acknowledged ok) or "failed" (acknowledged with an error).
type agentPersistedState struct {
	Desired         *agentDesired  `json:"desired"`
	DesiredStatus   string         `json:"desired_status,omitempty"`
	AppliedRevision string         `json:"applied_revision,omitempty"`
	LastAck         *agentAck      `json:"last_ack,omitempty"`
	WS              *agentWSStatus `json:"ws,omitempty"`
	UpdatedAt       int64          `json:"updated_at,omitempty"`
}

func agentStatePath() string { return filepath.Join(configDir, "agent-state.json") }

// agentStateMu serializes load-modify-save cycles on agent-state.json.
var agentStateMu sync.Mutex

func agentLoadState() agentPersistedState {
	agentStateMu.Lock()
	defer agentStateMu.Unlock()
	return agentLoadStateLocked()
}

func agentLoadStateLocked() agentPersistedState {
	var st agentPersistedState
	b, err := os.ReadFile(agentStatePath())
	if err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// agentMutateState runs fn against the current persisted state and saves it
// atomically. Every state writer must go through it, so concurrent writers
// (run loop status, desired application) never clobber each other.
func agentMutateState(fn func(*agentPersistedState)) error {
	agentStateMu.Lock()
	defer agentStateMu.Unlock()
	st := agentLoadStateLocked()
	fn(&st)
	st.UpdatedAt = time.Now().Unix()
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return agentAtomicWrite(agentStatePath(), append(b, '\n'), 0600)
}

// agentAtomicWrite writes data to path via a temp file + rename, so a crash
// never leaves a truncated file behind.
func agentAtomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".agent-tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
