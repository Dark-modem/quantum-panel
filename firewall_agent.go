package main

// Typed Agent execution interface for the firewall & port engine
// (Session 1). These handlers are the EXACT contract Session 3 can
// register in the agent dispatcher / controller command allowlist — this
// file deliberately does NOT touch agentproto.go, agent.go or the route
// tables.
//
// Wire proposal (agentproto v1 command ops → handlers below):
//
//	fw.detect      → AgentFirewallDetect()
//	fw.snapshot    → AgentFirewallSnapshot()
//	fw.plan        → AgentFirewallPlan(params)      — ALWAYS dry-run
//	fw.apply       → AgentFirewallApply(params)     — needs Confirmed
//	fw.teardown    → AgentFirewallTeardown(params)  — needs Confirmed
//	port.check     → AgentPortCheck(params)
//	port.list      → AgentPortList()
//
// Every parameter struct is JSON-typed and validated BEFORE any engine
// call; results are plain JSON structs. There is no shell/string command
// surface here: the only execution path is the engine's argv builders
// behind the fwForbidden refusal layer. Params/results carry no
// credentials, and error strings never embed rule secrets (they don't
// exist — rules are ports and comments).

import (
	"fmt"
	"strings"
)

// fwAgentSplitOwner validates and splits an "kind:id" owner tag.
func fwAgentSplitOwner(owner string) (kind, id string, err error) {
	idx := strings.Index(owner, ":")
	if idx <= 0 || idx == len(owner)-1 {
		return "", "", fmt.Errorf("owner must be kind:id (tunnel|service|external), got %q", owner)
	}
	kind, id = owner[:idx], owner[idx+1:]
	switch kind {
	case portOwnerTunnel, portOwnerService, portOwnerExternal:
	default:
		return "", "", fmt.Errorf("owner kind must be tunnel, service or external, got %q", kind)
	}
	if strings.ContainsAny(id, "\"'`$ \t") {
		return "", "", fmt.Errorf("owner id must not contain quotes, whitespace or shell metacharacters")
	}
	return kind, id, nil
}

// fwAgentValidateSpecs checks add/remove port specs.
func fwAgentValidateSpecs(specs []fwPortSpec) error {
	for _, s := range specs {
		if s.Port < 1 || s.Port > 65535 {
			return fmt.Errorf("port %d out of range", s.Port)
		}
		if s.Proto != portProtoTCP && s.Proto != portProtoUDP {
			return fmt.Errorf("proto must be tcp or udp, got %q", s.Proto)
		}
	}
	return nil
}

// ---- fw.detect ----

type fwAgentDetectResult struct {
	Backend fwBackend `json:"backend"`
	Detail  string    `json:"detail"`
}

// AgentFirewallDetect reports which single backend this host manages.
func AgentFirewallDetect() fwAgentDetectResult {
	backend, detail := fwDetect()
	return fwAgentDetectResult{Backend: backend, Detail: detail}
}

// ---- fw.snapshot ----

// AgentFirewallSnapshot captures the live ruleset (read-only).
func AgentFirewallSnapshot() (fwSnapshot, error) {
	return fwCaptureSnapshot()
}

// ---- fw.plan ----

// fwAgentPlanParams describes the wanted change. The agent NEVER mutates
// during a plan: it returns the exact commands for the controller (or the
// operator) to review.
type fwAgentPlanParams struct {
	Owner  string       `json:"owner"`            // "tunnel:at_…", "service:panel", …
	Family string       `json:"family,omitempty"` // ipv4 | ipv6 | both (default both)
	Note   string       `json:"note,omitempty"`
	Add    []fwPortSpec `json:"add,omitempty"`
	Remove []fwPortSpec `json:"remove,omitempty"`
}

type fwAgentPlanResult struct {
	Plan        fwPlan     `json:"plan"`
	PlannedCmds [][]string `json:"planned_cmds"`
	Refusal     string     `json:"refusal,omitempty"`
}

// AgentFirewallPlan builds and dry-runs a plan. It fails on invalid
// arguments and returns the backend's refusal (native nftables /
// no-backend) in Refusal without executing anything.
func AgentFirewallPlan(p fwAgentPlanParams) (fwAgentPlanResult, error) {
	out := fwAgentPlanResult{}
	if _, _, err := fwAgentSplitOwner(p.Owner); err != nil {
		return out, err
	}
	if err := fwAgentValidateSpecs(p.Add); err != nil {
		return out, fmt.Errorf("add: %v", err)
	}
	if err := fwAgentValidateSpecs(p.Remove); err != nil {
		return out, fmt.Errorf("remove: %v", err)
	}
	if len(p.Add) == 0 && len(p.Remove) == 0 {
		return out, fmt.Errorf("nothing to plan: add or remove at least one port")
	}
	family := p.Family
	if family == "" {
		family = "both"
	}
	var ops []fwOp
	for _, s := range p.Add {
		ops = append(ops, fwOp{Kind: "add", Rule: fwPortRule{
			Port: s.Port, Proto: s.Proto, Family: family, Owner: p.Owner, Note: p.Note}})
	}
	for _, s := range p.Remove {
		ops = append(ops, fwOp{Kind: "remove", Rule: fwPortRule{
			Port: s.Port, Proto: s.Proto, Family: family, Owner: p.Owner, Note: p.Note}})
	}
	plan, err := fwNewPlan(ops)
	if err != nil {
		return out, err
	}
	plan.DryRun = true
	res, err := fwApply(plan)
	if err != nil {
		out.Plan, out.Refusal = res.Plan, res.Error
		return out, err
	}
	out.Plan, out.PlannedCmds = res.Plan, res.PlannedCmds
	return out, nil
}

// ---- fw.apply ----

// fwAgentApplyParams re-submits ops that were previously planned (and
// reviewed). Confirmed MUST be true: it is the controller's signature
// that the dry-run was seen.
type fwAgentApplyParams struct {
	Ops       []fwOp `json:"ops"`
	Confirmed bool   `json:"confirmed"`
}

// AgentFirewallApply executes the confirmed ops with full engine safety:
// same validators, forbidden-command refusal, snapshots, rollback, audit.
func AgentFirewallApply(p fwAgentApplyParams) (fwApplyResult, error) {
	if !p.Confirmed {
		return fwApplyResult{}, fmt.Errorf("apply refused: the controller must confirm the dry-run first (confirmed=true)")
	}
	plan, err := fwNewPlan(p.Ops)
	if err != nil {
		return fwApplyResult{}, err
	}
	plan.DryRun = false
	return fwApply(plan)
}

// ---- fw.teardown ----

// fwAgentTeardownParams removes one owner's registry ports and firewall
// rules — and nothing else. Confirmed MUST be true.
type fwAgentTeardownParams struct {
	Owner     string `json:"owner"`
	Confirmed bool   `json:"confirmed"`
}

type fwAgentTeardownResult struct {
	ReleasedPorts int           `json:"released_ports"`
	Firewall      fwApplyResult `json:"firewall"`
}

// AgentFirewallTeardown removes only the firewall rules tagged with that
// owner (the registry is the plan's source of truth), and THEN releases
// the owner's registry entries — this order keeps a failed rule removal
// retriable, since the registry still says who owns what.
func AgentFirewallTeardown(p fwAgentTeardownParams) (fwAgentTeardownResult, error) {
	out := fwAgentTeardownResult{}
	if !p.Confirmed {
		return out, fmt.Errorf("teardown refused: the controller must confirm it first (confirmed=true)")
	}
	kind, id, err := fwAgentSplitOwner(p.Owner)
	if err != nil {
		return out, err
	}
	res, err := fwTeardownOwner(p.Owner)
	out.Firewall = res
	if err != nil {
		return out, err
	}
	released, err := portReleaseOwner(kind, id)
	if err != nil {
		return out, err
	}
	out.ReleasedPorts = released
	return out, nil
}

// ---- port.check / port.list ----

// fwAgentPortCheckParams asks for one port's state. With a proto
// (tcp|udp) the result is that transport's availability; without one the
// full both-transports report is returned.
type fwAgentPortCheckParams struct {
	Port  int    `json:"port"`
	Proto string `json:"proto,omitempty"`
}

func AgentPortCheck(p fwAgentPortCheckParams) (any, error) {
	if p.Port < 1 || p.Port > 65535 {
		return nil, fmt.Errorf("port %d out of range", p.Port)
	}
	switch p.Proto {
	case "":
		return portReport(p.Port), nil
	case portProtoTCP, portProtoUDP:
		return portAvailabilityCheck(p.Port, p.Proto), nil
	default:
		return nil, fmt.Errorf("proto must be tcp or udp, got %q", p.Proto)
	}
}

type fwAgentPortListResult struct {
	Backend   fwBackend        `json:"backend"`
	Detail    string           `json:"detail"`
	Entries   []portEntry      `json:"entries"`
	Listeners []portListener   `json:"listeners"`
	Conflicts []map[string]any `json:"conflicts"`
}

// AgentPortList is the one-call overview the controller UI needs.
func AgentPortList() (fwAgentPortListResult, error) {
	out := fwAgentPortListResult{}
	out.Backend, out.Detail = fwDetect()
	out.Entries = portEntries()
	if ls, err := portListeners(); err == nil {
		out.Listeners = ls
	}
	out.Conflicts = portConflicts()
	return out, nil
}
