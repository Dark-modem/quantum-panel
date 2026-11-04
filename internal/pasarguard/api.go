// api.go — the typed node operations (paths verified in
// 00-ADMIN/PASARGUARD-INTEGRATION.md §4; note the singular/plural router
// prefix: /api/node with literal "s" suffixes for the collection routes).

package pasarguard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ListNodes returns the panel's node records (optionally filtered by
// search substring). Quantum-owned filtering happens in the reconciler.
func (c *Client) ListNodes(ctx context.Context, search string) ([]Node, error) {
	q := url.Values{}
	q.Set("limit", strconv.Itoa(500)) // upstream default page sizes are small; mesh shapes are ≤ 2×N
	if strings.TrimSpace(search) != "" {
		q.Set("search", search)
	}
	var out nodesList
	if err := c.do(ctx, http.MethodGet, "/api/nodes?"+q.Encode(), nil, &out); err != nil {
		return nil, err
	}
	if out.Nodes == nil {
		out.Nodes = []Node{}
	}
	return out.Nodes, nil
}

// GetNode returns one node by id.
func (c *Client) GetNode(ctx context.Context, id int) (*Node, error) {
	var n Node
	if err := c.do(ctx, http.MethodGet, "/api/nodes/"+strconv.Itoa(id), nil, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// CreateNode provisions a node record. 409 (duplicate name) surfaces as an
// APIError for the reconciler to translate into a plan note.
func (c *Client) CreateNode(ctx context.Context, req CreateRequest) (*Node, error) {
	if err := req.validate(); err != nil {
		return nil, err
	}
	var n Node
	if err := c.do(ctx, http.MethodPost, "/api/nodes", req, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// ModifyNode patches ONLY the fields set on the request (upstream
// NodeModify is fully optional; coefficient-only updates are the norm).
func (c *Client) ModifyNode(ctx context.Context, id int, req ModifyRequest) (*Node, error) {
	var n Node
	if err := c.do(ctx, http.MethodPut, "/api/nodes/"+strconv.Itoa(id), req, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// DeleteNode removes a node record (204). The reconciler gates this behind
// explicit confirmation and the quantum: name prefix.
func (c *Client) DeleteNode(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodDelete, "/api/nodes/"+strconv.Itoa(id), nil, nil)
}

// ReconnectNode asks the panel to redial the node.
func (c *Client) ReconnectNode(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodPost, "/api/nodes/"+strconv.Itoa(id)+"/reconnect",
		map[string]any{}, nil)
}

// UsageRow is one node's aggregated uplink/downlink from the usage
// endpoint (GET /api/node/usage with group_by_node=true).
type UsageRow struct {
	NodeID   int   `json:"node_id"`
	Uplink   int64 `json:"uplink"`
	Downlink int64 `json:"downlink"`
}

// NodeUsage returns the node's aggregated usage in [start, end). The
// response shape is upstream-version dependent, so the client accepts the
// two known envelopes: a bare array of rows or {"rows": [...]} — and
// falls back to the LIFETIME counters from the node record when the usage
// endpoint's shape does not parse (documented in the integration doc).
func (c *Client) NodeUsage(ctx context.Context, nodeID int, start, end time.Time) (uplink, downlink int64, degraded bool, err error) {
	q := url.Values{}
	q.Set("start", start.Format(time.RFC3339))
	q.Set("end", end.Format(time.RFC3339))
	q.Set("node_id", strconv.Itoa(nodeID))
	q.Set("group_by_node", "true")
	var raw json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/api/node/usage?"+q.Encode(), nil, &raw); err != nil {
		return 0, 0, false, err
	}
	// Envelope A: array of rows.
	var rows []UsageRow
	if err := decodeJSON(raw, &rows); err == nil {
		for _, r := range rows {
			if r.NodeID == nodeID {
				return r.Uplink, r.Downlink, false, nil
			}
		}
		return 0, 0, false, nil // node present, no rows yet = zero usage
	}
	// Envelope B: {"usages": [...]}/{"rows": [...]} wrappers.
	var wrapped struct {
		Usages []UsageRow `json:"usages"`
		Rows   []UsageRow `json:"rows"`
	}
	if err := decodeJSON(raw, &wrapped); err == nil {
		all := append(wrapped.Usages, wrapped.Rows...)
		for _, r := range all {
			if r.NodeID == nodeID {
				return r.Uplink, r.Downlink, false, nil
			}
		}
		return 0, 0, false, nil
	}
	// Unknown shape: degrade to lifetime counters from the node record —
	// the cross-check marks the row "degraded" instead of guessing.
	n, err := c.GetNode(ctx, nodeID)
	if err != nil {
		return 0, 0, false, err
	}
	return n.Uplink, n.Downlink, true, nil
}

func (r CreateRequest) validate() error {
	if strings.TrimSpace(r.Name) == "" || len(r.Name) > 64 {
		return fmt.Errorf("pasarguard: node name must be 1-64 chars")
	}
	if strings.HasPrefix(r.Name, NamePrefix) == false || r.Name == NamePrefix {
		return fmt.Errorf("pasarguard: node name must carry the %q ownership prefix", NamePrefix)
	}
	if strings.TrimSpace(r.Address) == "" || len(r.Address) > 255 {
		return fmt.Errorf("pasarguard: node address required")
	}
	if r.Port < 1 || r.Port > 65535 || r.APIPort < 1 || r.APIPort > 65535 {
		return fmt.Errorf("pasarguard: ports out of range")
	}
	if r.UsageCoefficient <= 0 || r.UsageCoefficient > 1e6 {
		return fmt.Errorf("pasarguard: usage_coefficient must be > 0")
	}
	if r.ConnectionType != ConnectionTypeGRPC {
		return fmt.Errorf("pasarguard: only %q connection_type is provisioned", ConnectionTypeGRPC)
	}
	if !strings.Contains(r.ServerCA, "BEGIN CERTIFICATE") || len(r.ServerCA) > 2048 {
		return fmt.Errorf("pasarguard: server_ca must be a PEM certificate ≤ 2048 chars (public part only)")
	}
	if len(r.APIKey) != 36 || strings.Count(r.APIKey, "-") != 4 {
		return fmt.Errorf("pasarguard: api_key must be a UUID")
	}
	if r.KeepAlive <= 0 || r.KeepAlive > 3600 {
		return fmt.Errorf("pasarguard: keep_alive out of range")
	}
	if r.CoreConfigID < 1 {
		return fmt.Errorf("pasarguard: core_config_id required (upstream default core is id 1)")
	}
	return nil
}
