// api.go — the typed node operations (paths verified in
// 00-ADMIN/PASARGUARD-INTEGRATION.md §4; note the singular/plural router
// prefix: /api/node — the LIST route is the literal "s" suffix
// (/api/nodes) while create/modify/delete/reconnect stay SINGULAR
// (/api/node, /api/node/{id}, ...). Verified against 5.2.1 sources and
// the live 5.6 panel (405 on POST /api/nodes).

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
	if err := c.do(ctx, http.MethodGet, "/api/node/"+strconv.Itoa(id), nil, &n); err != nil {
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
	if err := c.do(ctx, http.MethodPost, "/api/node", req, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// ModifyNode patches ONLY the fields set on the request (upstream
// NodeModify is fully optional; coefficient-only updates are the norm).
func (c *Client) ModifyNode(ctx context.Context, id int, req ModifyRequest) (*Node, error) {
	var n Node
	if err := c.do(ctx, http.MethodPut, "/api/node/"+strconv.Itoa(id), req, &n); err != nil {
		return nil, err
	}
	return &n, nil
}

// DeleteNode removes a node record (204). The reconciler gates this behind
// explicit confirmation and the quantum: name prefix.
func (c *Client) DeleteNode(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodDelete, "/api/node/"+strconv.Itoa(id), nil, nil)
}

// ReconnectNode asks the panel to redial the node.
func (c *Client) ReconnectNode(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodPost, "/api/node/"+strconv.Itoa(id)+"/reconnect",
		map[string]any{}, nil)
}

// ---- core config + hosts (per-DC inbounds, phase 14 task 2) -----------------
//
// Paths verified against the 5.2.1 sources: the Xray JSON lives INSIDE the
// core (app/routers/core.py — nodes reference it via core_config_id) and
// hosts are global records mapping what clients see onto an inbound tag
// (app/routers/host.py). Note the upstream quirks this client mirrors:
// GET /api/hosts is a BARE ARRAY and the create route is POST /api/host/
// (trailing slash — sent verbatim to dodge the 307 redirect).

// GetCore returns one core-config record; the Xray JSON is in "config"
// and the node-facing name/type/exclude/fallbacks ride along for the
// applier's PUT round-trip.
func (c *Client) GetCore(ctx context.Context, coreID int) (map[string]any, error) {
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/api/core/"+strconv.Itoa(coreID), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ModifyCore replaces the core config. restartNodes=true (always sent by
// the reconciler) makes upstream restart every node referencing the core,
// so the new inbounds go live without a separate reconnect round.
func (c *Client) ModifyCore(ctx context.Context, coreID int, restartNodes bool, body CoreModifyRequest) error {
	q := url.Values{}
	q.Set("restart_nodes", strconv.FormatBool(restartNodes))
	return c.do(ctx, http.MethodPut, "/api/core/"+strconv.Itoa(coreID)+"?"+q.Encode(), body, nil)
}

// CreateHost provisions one host record (upstream validates that
// inbound_tag exists in one of the configured cores).
func (c *Client) CreateHost(ctx context.Context, req HostCreateRequest) (*Host, error) {
	var h Host
	if err := c.do(ctx, http.MethodPost, "/api/host/", req, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// ListHosts returns every host record (the panel's client-facing map).
func (c *Client) ListHosts(ctx context.Context) ([]Host, error) {
	var out []Host
	if err := c.do(ctx, http.MethodGet, "/api/hosts", nil, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Host{}
	}
	return out, nil
}

// ModifyHost replaces one host record with the full desired document.
func (c *Client) ModifyHost(ctx context.Context, id int, req HostCreateRequest) (*Host, error) {
	var h Host
	if err := c.do(ctx, http.MethodPut, "/api/host/"+strconv.Itoa(id), req, &h); err != nil {
		return nil, err
	}
	return &h, nil
}

// DeleteHost removes one host record. NOT part of the reconciler's apply
// path (which never deletes); it exists for explicit operator cleanup.
func (c *Client) DeleteHost(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodDelete, "/api/host/"+strconv.Itoa(id), nil, nil)
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
	// NOTE: the quantum: ownership prefix is NOT enforced here — the client
	// is a transport for the upstream API and an operator may legitimately
	// manage bare-named nodes through it. The RECONCILER enforces the
	// prefix (DeleteOrphans refuses non-prefixed ids; Apply only ever sends
	// DesiredName()-shaped names).
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
	if !validUUID(r.APIKey) {
		return fmt.Errorf("pasarguard: api_key must be a UUID (Python uuid.UUID accepts the 32-hex undashed form too)")
	}
	if r.KeepAlive <= 0 || r.KeepAlive > 3600 {
		return fmt.Errorf("pasarguard: keep_alive out of range")
	}
	if r.CoreConfigID < 1 {
		return fmt.Errorf("pasarguard: core_config_id required (upstream default core is id 1)")
	}
	return nil
}

// validUUID mirrors python uuid.UUID: canonical 8-4-4-4-12 OR plain 32 hex.
func validUUID(s string) bool {
	hexOK := func(x string) bool {
		for _, c := range x {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
		return true
	}
	if len(s) == 36 {
		parts := strings.Split(s, "-")
		if len(parts) == 5 &&
			len(parts[0]) == 8 && len(parts[1]) == 4 && len(parts[2]) == 4 && len(parts[3]) == 4 && len(parts[4]) == 12 {
			for _, p := range parts {
				if !hexOK(p) {
					return false
				}
			}
			return true
		}
		return false
	}
	return len(s) == 32 && hexOK(s)
}
