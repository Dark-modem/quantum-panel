package main

// Controller proxy for typed agent operations (firewall/port/netopt). The
// UI calls POST /api/agents/{id}/op with {op, payload, confirm}; the
// controller forwards the typed command to the agent and relays the JSON
// result. Mutating ops require the operator's explicit confirm=yes (the UI
// shows the agent's plan first) and are audit-logged here as well — the
// agent logs its own execution.

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

var agentOpsLongTimeout = 60 * time.Second // apply/teardown can sweep iptables

func agentOpsTimeoutFor(op string) time.Duration {
	if agentMutatingOps[op] {
		return agentOpsLongTimeout
	}
	return agentCommandTimeout
}

func handleAgentOpPost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var q struct {
		Op      string          `json:"op"`
		Payload json.RawMessage `json:"payload"`
		Confirm bool            `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&q); err != nil {
		agentAPIError(w, r, "E-AGENT-05", http.StatusBadRequest, "body must be {\"op\",\"payload\",\"confirm\"}")
		return
	}
	op := strings.TrimSpace(q.Op)
	if !agentValidCommands[op] {
		agentAPIError(w, r, "E-AGENT-02", http.StatusBadRequest, "unknown operation "+op)
		return
	}
	isMutating := agentMutatingOps[op]
	params := map[string]string{}
	if len(q.Payload) > 0 && string(q.Payload) != "null" {
		params["payload"] = string(q.Payload)
	}
	if isMutating {
		if !q.Confirm {
			agentAPIError(w, r, "E-AGENT-08", http.StatusPreconditionRequired, "mutating operation: fetch the plan, review it, then resend with confirm=true")
			return
		}
		params["confirm"] = "yes"
	}
	res, err := agentHubCommand(id, op, params, agentOpsTimeoutFor(op))
	if err != nil {
		LogSecurityAudit("agent_op_failed", cfg.Username, ClientIP(r), "agent="+id+" op="+op+" err="+err.Error())
		agentAPIError(w, r, "E-AGENT-01", http.StatusBadGateway, err.Error())
		return
	}
	if isMutating {
		LogSecurityAudit("agent_op_applied", cfg.Username, ClientIP(r), "agent="+id+" op="+op+" ok="+boolToStr(res.OK))
	} else {
		LogSecurityAudit("agent_op_read", cfg.Username, ClientIP(r), "agent="+id+" op="+op)
	}
	writeJSON(w, map[string]any{"op": op, "ok": res.OK, "error": res.Error, "result": json.RawMessage(res.Output)})
}

func boolToStr(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
