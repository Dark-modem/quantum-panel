// Package pasarguard is a small typed client for the PasarGuard panel
// admin API (upstream studied at pasargurad-panel-5.2.1 and
// pasargurad-node-0.5.3; see 00-ADMIN/PASARGUARD-INTEGRATION.md for the
// endpoint contract). Scope: node records — create/modify/delete/list/
// reconnect and usage — plus the phase 14 per-datacenter inbounds
// (dcinbound.go): core config reads/writes, host records and REALITY key
// generation. Quantum never manages PasarGuard users, groups, or the
// nodes' runtime statistics.
//
// Ground rules: the admin token is transparently re-acquired on 401
// (upstream has no refresh endpoint); retries are exponential-backoff and
// idempotent-call-only; credentials and tokens never appear in logs;
// statistics pass through untouched (coefficients are never applied by
// this package — real bytes in, real bytes out).
package pasarguard

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Node status values (upstream app/db/models.py NodeStatus).
const (
	StatusConnected  = "connected"
	StatusConnecting = "connecting"
	StatusError      = "error"
	StatusDisabled   = "disabled"
	StatusLimited    = "limited"
)

// ConnectionTypeGRPC is the only connection type Quantum provisions.
const ConnectionTypeGRPC = "grpc"

// NamePrefix marks every node record Quantum owns. The reconciler only
// ever mutates or deletes nodes whose name starts with this prefix;
// everything else is reported as foreign and left alone.
const NamePrefix = "quantum:"

// Node is the subset of upstream NodeResponse the panel needs.
type Node struct {
	ID               int     `json:"id"`
	Name             string  `json:"name"`
	Address          string  `json:"address"`
	Port             int     `json:"port"`
	APIPort          int     `json:"api_port"`
	UsageCoefficient float64 `json:"usage_coefficient"`
	ConnectionType   string  `json:"connection_type"`
	ServerCA         string  `json:"server_ca"`
	KeepAlive        int     `json:"keep_alive"`
	CoreConfigID     int     `json:"core_config_id"`
	APIKey           string  `json:"api_key"`
	Status           string  `json:"status,omitempty"`
	Message          string  `json:"message,omitempty"`
	NodeVersion      string  `json:"node_version,omitempty"`
	Uplink           int64   `json:"uplink"`
	Downlink         int64   `json:"downlink"`
	LifetimeUplink   int64   `json:"lifetime_uplink,omitempty"`
	LifetimeDownlink int64   `json:"lifetime_downlink,omitempty"`
}

// QuantumOwned reports whether this node record belongs to Quantum.
func (n Node) QuantumOwned() bool { return strings.HasPrefix(n.Name, NamePrefix) }

// CreateRequest mirrors upstream NodeCreate (all required-by-upstream
// fields set; Quantum always sends a complete document).
type CreateRequest struct {
	Name             string  `json:"name"`
	Address          string  `json:"address"`
	Port             int     `json:"port"`
	APIPort          int     `json:"api_port"`
	UsageCoefficient float64 `json:"usage_coefficient"`
	ConnectionType   string  `json:"connection_type"`
	ServerCA         string  `json:"server_ca"`
	KeepAlive        int     `json:"keep_alive"`
	CoreConfigID     int     `json:"core_config_id"`
	APIKey           string  `json:"api_key"`
}

// ModifyRequest mirrors upstream NodeModify: every field optional, so the
// reconciler sends ONLY what drifted (coefficient-only updates are the
// common case and never reconnect-worthy on the Quantum side; the panel
// handles its own reconnect).
type ModifyRequest struct {
	Name             *string  `json:"name,omitempty"`
	Address          *string  `json:"address,omitempty"`
	Port             *int     `json:"port,omitempty"`
	UsageCoefficient *float64 `json:"usage_coefficient,omitempty"`
	ServerCA         *string  `json:"server_ca,omitempty"`
	APIKey           *string  `json:"api_key,omitempty"`
}

// UsageSample is one entry of the aggregated usage endpoint
// (GET /api/node/usage). The exact row shape is upstream-version dependent;
// the client keeps the fields the cross-check needs and ignores the rest.
type UsageSample struct {
	NodeID   int    `json:"node_id"`
	NodeName string `json:"node_name,omitempty"`
	Uplink   int64  `json:"uplink"`
	Downlink int64  `json:"downlink"`
}

// nodesList is the upstream {"nodes": [...], "total": N} envelope.
type nodesList struct {
	Nodes []Node `json:"nodes"`
	Total int    `json:"total"`
}

// decodeJSON is a strict helper that caps the decoded payload.
func decodeJSON(data []byte, into any) error {
	if len(data) > 4<<20 {
		return fmt.Errorf("pasarguard: response too large (%d bytes)", len(data))
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("pasarguard: bad JSON: %v", err)
	}
	return nil
}
