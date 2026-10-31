package main

// Typed firewall/port (Session 1) and netopt (Session 2) operations for the
// agent dispatcher. Each op wraps one delivered handler; params arrive as a
// single JSON "payload" field and results leave as JSON in Output. Mutating
// ops require payload.confirm == "yes" and are audit-logged; nothing here
// executes shell commands beyond the handlers' own fixed seams.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// agentOpsPayload extracts and validates the JSON payload for an op.
func agentOpsPayload(params map[string]string, out any) error {
	raw := strings.TrimSpace(params["payload"])
	if raw == "" {
		return fmt.Errorf("missing payload")
	}
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		return fmt.Errorf("invalid payload JSON: %w", err)
	}
	return nil
}

// agentOpsConfirm enforces the explicit confirmation gate for mutations.
func agentOpsConfirm(op string, params map[string]string) error {
	if !agentMutatingOps[op] {
		return nil
	}
	if params["confirm"] != "yes" {
		return fmt.Errorf("mutating operation requires confirm=yes (review the plan first)")
	}
	return nil
}

// agentRunTypedOp dispatches one typed operation to its delivered handler
// and returns the JSON-serializable result. Unknown ops never reach here
// (agentValidCommands gates the dispatcher).
func agentRunTypedOp(op string, params map[string]string) (any, error) {
	if err := agentOpsConfirm(op, params); err != nil {
		return nil, err
	}
	switch op {
	case agentCmdFwDetect:
		return AgentFirewallDetect(), nil
	case agentCmdFwSnapshot:
		return AgentFirewallSnapshot()
	case agentCmdFwPlan:
		var p fwAgentPlanParams
		if err := agentOpsPayload(params, &p); err != nil {
			return nil, err
		}
		return AgentFirewallPlan(p)
	case agentCmdFwApply:
		var p fwAgentApplyParams
		if err := agentOpsPayload(params, &p); err != nil {
			return nil, err
		}
		return AgentFirewallApply(p)
	case agentCmdFwTeardown:
		var p fwAgentTeardownParams
		if err := agentOpsPayload(params, &p); err != nil {
			return nil, err
		}
		return AgentFirewallTeardown(p)
	case agentCmdPortCheck:
		var p fwAgentPortCheckParams
		if err := agentOpsPayload(params, &p); err != nil {
			return nil, err
		}
		return AgentPortCheck(p)
	case agentCmdPortList:
		return AgentPortList()
	case agentCmdNetDetect:
		return NetopDetect()
	case agentCmdNetPlan:
		var p struct {
			Role          string `json:"role"`
			ForcedProfile string `json:"profile"`
		}
		if err := agentOpsPayload(params, &p); err != nil {
			return nil, err
		}
		return NetopPlan(netRole(strings.TrimSpace(p.Role)), strings.TrimSpace(p.ForcedProfile))
	case agentCmdNetApply:
		var p struct {
			Role    string   `json:"role"`
			Profile string   `json:"profile"`
			Justify string   `json:"justify"`
			Reasons []string `json:"reasons"`
		}
		if err := agentOpsPayload(params, &p); err != nil {
			return nil, err
		}
		justify := strings.TrimSpace(p.Justify)
		if justify == "" {
			return nil, fmt.Errorf("apply requires a justify string (audit trail)")
		}
		return NetopApply(netRole(strings.TrimSpace(p.Role)), strings.TrimSpace(p.Profile), justify, p.Reasons)
	case agentCmdNetStatus:
		return NetopStatus()
	case agentCmdNetRB:
		return NetopRollback()
	case agentCmdNetRevert:
		return NetopRevert()
	}
	return nil, fmt.Errorf("unknown typed operation %q", op)
}
