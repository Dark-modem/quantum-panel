// reconcile.go — the desired-state reconciler for PasarGuard nodes.
//
// Desired: one node per (foreign server × Iran datacenter) pair, named
// "quantum:<dc name>", address = the foreign server's address, usage
// coefficient = the datacenter's price coefficient, connection grpc.
//
// The diff NEVER deletes: nodes Quantum no longer desires are reported as
// drift (stale) and only a separate, explicit DeleteOrphans call removes
// them — and even that refuses anything without the quantum: prefix.
// Foreign (operator-created) nodes are listed for visibility and never
// touched.

package pasarguard

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// DesiredNode is one node the panel wants to exist.
type DesiredNode struct {
	Name        string  // "quantum:<dc>"
	Address     string  // foreign server address
	Coefficient float64 // datacenter price coefficient
	ServerCA    string  // the node's PEM cert (operator-supplied)
	APIKey      string  // the node's API_KEY UUID
	Port        int     // node gRPC port (default 62050)
}

// Action kinds.
const (
	ActionCreate    = "create"
	ActionUpdate    = "update"
	ActionReconnect = "reconnect"
	ActionOK        = "ok"
	ActionStale     = "stale"
	ActionForeign   = "foreign"
	ActionDelete    = "delete"
)

// PlanAction is one diff row (JSON-serializable for the API/UI).
type PlanAction struct {
	Action   string  `json:"action"` // create|update|reconnect|ok|stale|foreign|delete
	Name     string  `json:"name"`
	NodeID   int     `json:"node_id,omitempty"`
	Detail   string  `json:"detail,omitempty"`
	CoeffWas float64 `json:"coefficient_was,omitempty"`
	CoeffNew float64 `json:"coefficient_new,omitempty"`
}

// Plan is the full diff result.
type Plan struct {
	Actions    []PlanAction `json:"actions"`
	Creates    int          `json:"creates"`
	Updates    int          `json:"updates"`
	Reconnects int          `json:"reconnects"`
	Stale      int          `json:"stale"`
	Drift      bool         `json:"drift"`
}

// DesiredName builds the canonical node name for one (datacenter, foreign
// server) pair. The server part is REQUIRED: upstream node names are
// globally unique, and M foreign servers each host a node for the same
// datacenter — "quantum:<dc>-<server>" keeps them distinct and traceable.
func DesiredName(dcName, serverName string) string {
	return NamePrefix + slugPart(dcName) + "-" + slugPart(serverName)
}

// slugPart lowercases and replaces anything outside [a-z0-9-_] with '-'.
func slugPart(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "x"
	}
	if len(out) > 24 {
		out = out[:24]
	}
	return out
}

// Diff computes the plan for a desired set against the real panel.
// Pure function: no calls, fully testable.
func Diff(desired []DesiredNode, real []Node) *Plan {
	plan := &Plan{Actions: []PlanAction{}}
	realByName := map[string]Node{}
	for _, n := range real {
		realByName[n.Name] = n
	}
	desiredNames := map[string]bool{}

	for _, d := range desired {
		desiredNames[d.Name] = true
		existing, ok := realByName[d.Name]
		switch {
		case !ok:
			plan.Actions = append(plan.Actions, PlanAction{
				Action: ActionCreate, Name: d.Name,
				CoeffNew: d.Coefficient,
				Detail:   "node record does not exist",
			})
			plan.Creates++
			plan.Drift = true
		case existing.UsageCoefficient != d.Coefficient:
			plan.Actions = append(plan.Actions, PlanAction{
				Action: ActionUpdate, Name: d.Name, NodeID: existing.ID,
				CoeffWas: existing.UsageCoefficient, CoeffNew: d.Coefficient,
				Detail: "coefficient drifted",
			})
			plan.Updates++
			plan.Drift = true
		case existing.Status == StatusError || existing.Status == StatusDisabled:
			plan.Actions = append(plan.Actions, PlanAction{
				Action: ActionReconnect, Name: d.Name, NodeID: existing.ID,
				Detail: "node status " + existing.Status,
			})
			plan.Reconnects++
			plan.Drift = true
		default:
			plan.Actions = append(plan.Actions, PlanAction{
				Action: ActionOK, Name: d.Name, NodeID: existing.ID,
				Detail: fmt.Sprintf("coefficient %.3f, status %s", existing.UsageCoefficient, existing.Status),
			})
		}
	}

	// Real nodes we do not desire: stale (ours, previously created) or
	// foreign (the operator's) — both reported, neither touched here.
	for _, n := range real {
		if desiredNames[n.Name] {
			continue
		}
		if n.QuantumOwned() {
			plan.Actions = append(plan.Actions, PlanAction{
				Action: ActionStale, Name: n.Name, NodeID: n.ID,
				Detail: "created by Quantum but no (server, datacenter) pair desires it; use delete-orphans to remove",
			})
			plan.Stale++
			plan.Drift = true
		} else {
			plan.Actions = append(plan.Actions, PlanAction{
				Action: ActionForeign, Name: n.Name, NodeID: n.ID,
				Detail: "operator-created node — never touched by Quantum",
			})
		}
	}
	sortPlan(plan)
	return plan
}

func sortPlan(p *Plan) {
	rank := map[string]int{ActionCreate: 0, ActionUpdate: 1, ActionReconnect: 2, ActionOK: 3, ActionStale: 4, ActionForeign: 5, ActionDelete: 6}
	sort.SliceStable(p.Actions, func(i, j int) bool {
		if rank[p.Actions[i].Action] != rank[p.Actions[j].Action] {
			return rank[p.Actions[i].Action] < rank[p.Actions[j].Action]
		}
		return p.Actions[i].Name < p.Actions[j].Name
	})
}

// Applier is the client surface Apply needs (the real *Client or a fake).
type Applier interface {
	CreateNode(ctx context.Context, req CreateRequest) (*Node, error)
	ModifyNode(ctx context.Context, id int, req ModifyRequest) (*Node, error)
	ReconnectNode(ctx context.Context, id int) error
}

// Apply executes a plan's create/update/reconnect rows against applier.
// Stale/foreign/delete rows are NEVER executed here; deletion is its own
// explicit call. Returns per-action outcomes (same order).
func Apply(ctx context.Context, plan *Plan, desired []DesiredNode, ap Applier) []PlanAction {
	byName := map[string]DesiredNode{}
	for _, d := range desired {
		byName[d.Name] = d
	}
	out := make([]PlanAction, 0, len(plan.Actions))
	for _, a := range plan.Actions {
		switch a.Action {
		case ActionCreate:
			d, ok := byName[a.Name]
			if !ok {
				out = append(out, PlanAction{Action: a.Action, Name: a.Name, Detail: "desired row vanished mid-apply; skipped"})
				continue
			}
			if _, err := ap.CreateNode(ctx, createRequestFor(d)); err != nil {
				out = append(out, PlanAction{Action: a.Action, Name: a.Name, Detail: "FAILED: " + err.Error()})
				continue
			}
			out = append(out, PlanAction{Action: a.Action, Name: a.Name, Detail: "created"})
		case ActionUpdate:
			d, ok := byName[a.Name]
			if !ok || a.NodeID == 0 {
				out = append(out, PlanAction{Action: a.Action, Name: a.Name, Detail: "skipped (no desired row / no node id)"})
				continue
			}
			coef := d.Coefficient
			if _, err := ap.ModifyNode(ctx, a.NodeID, ModifyRequest{UsageCoefficient: &coef}); err != nil {
				out = append(out, PlanAction{Action: a.Action, Name: a.Name, NodeID: a.NodeID, Detail: "FAILED: " + err.Error()})
				continue
			}
			out = append(out, PlanAction{Action: a.Action, Name: a.Name, NodeID: a.NodeID, Detail: fmt.Sprintf("coefficient -> %.3f", coef)})
		case ActionReconnect:
			if a.NodeID == 0 {
				continue
			}
			if err := ap.ReconnectNode(ctx, a.NodeID); err != nil {
				out = append(out, PlanAction{Action: a.Action, Name: a.Name, NodeID: a.NodeID, Detail: "FAILED: " + err.Error()})
				continue
			}
			out = append(out, PlanAction{Action: a.Action, Name: a.Name, NodeID: a.NodeID, Detail: "reconnect requested"})
		default:
			out = append(out, a) // ok/stale/foreign pass through untouched
		}
	}
	return out
}

// DeleteOrphans removes STALE quantum-prefixed nodes by id. It refuses
// anything not created by Quantum (name prefix check against the real
// node list) — a mistyped id can never delete an operator node.
func DeleteOrphans(ctx context.Context, ids []int, cl *Client) ([]PlanAction, error) {
	real, err := cl.ListNodes(ctx, "")
	if err != nil {
		return nil, err
	}
	byID := map[int]Node{}
	for _, n := range real {
		byID[n.ID] = n
	}
	out := []PlanAction{}
	for _, id := range ids {
		n, ok := byID[id]
		if !ok {
			out = append(out, PlanAction{Action: ActionDelete, NodeID: id, Detail: "not found; skipped"})
			continue
		}
		if !n.QuantumOwned() {
			out = append(out, PlanAction{Action: ActionDelete, NodeID: id, Name: n.Name,
				Detail: "REFUSED: node is not Quantum-owned (missing quantum: prefix)"})
			continue
		}
		if err := cl.DeleteNode(ctx, id); err != nil {
			out = append(out, PlanAction{Action: ActionDelete, NodeID: id, Name: n.Name, Detail: "FAILED: " + err.Error()})
			continue
		}
		out = append(out, PlanAction{Action: ActionDelete, NodeID: id, Name: n.Name, Detail: "deleted"})
	}
	return out, nil
}

func createRequestFor(d DesiredNode) CreateRequest {
	port := d.Port
	if port == 0 {
		port = DefaultNodePort
	}
	return CreateRequest{
		Name:             d.Name,
		Address:          d.Address,
		Port:             port,
		APIPort:          DefaultAPIPort,
		UsageCoefficient: d.Coefficient,
		ConnectionType:   ConnectionTypeGRPC,
		ServerCA:         d.ServerCA,
		KeepAlive:        DefaultKeepAlive,
		CoreConfigID:     1,
		APIKey:           d.APIKey,
	}
}
