package panel

// optimization_kernel.go — BACKEND-42 PART B (UI-API-CONTRACT §2.23.2): the
// kernel-tuning READ surface. GET /api/optimization/kernel answers
// per-agent diagnostics from what the agents ALREADY report: the panel
// issues the agents' existing read-only netopt.detect op (protocol
// CmdNetDetect — a ValidCommands entry, no confirm, no mutation) and maps
// the agent's netHardwareReport into the read view.
//
// NOTHING IS INVENTED. Fields the current agent surface does not carry
// (verified against quantum-agent 2f240d2: netSysctlKeys has no
// tcp_retries2; no qdisc-availability list) render null and are declared
// here: the tuning PROFILE "kernel-net-tuning" (hardening family) is
// needs-agent-fields and the agent-side work is FLAGGED AGENT-31. The
// APPLY path is a later task — this endpoint is read-only.

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	protocol "github.com/Dark-modem/quantum-protocol"
)

// kernelProfile is the honest declaration riding every response (§2.23.2).
// status "needs-agent-fields" = the read surface works TODAY for the fields
// the agent carries; the missing ones are named, flagged AGENT-31, and the
// apply path is a later task.
func kernelProfile() map[string]any {
	return map[string]any{
		"id":             "kernel-net-tuning",
		"family":         "hardening",
		"status":         "needs-agent-fields",
		"missing_fields": []string{"tcp_retries2", "qdisc_availability"},
		"apply":          "later-task",
		"flag":           "AGENT-31",
		"note": "read-only diagnostics from the agent's netopt.detect report; " +
			"tcp_retries2 and the available-qdisc list are not in the current agent " +
			"report (flagged AGENT-31 — declared null, never invented); applying a " +
			"profile is a later task",
	}
}

// kernelDiagnostics is one agent's read view (§2.23.2). Every field is
// present; unmeasured fields are null (checked_at 0 = never probed).
type kernelDiagnostics struct {
	Agent             string `json:"agent"`
	Name              string `json:"name"`
	Online            bool   `json:"online"`
	Status            string `json:"status"` // ok | pending | error | offline
	CheckedAt         int64  `json:"checked_at"`
	CongestionControl any    `json:"congestion_control"`
	BBRAvailable      any    `json:"bbr_available"`
	Qdisc             any    `json:"qdisc"`
	TCPMTUProbing     any    `json:"tcp_mtu_probing"`
	RmemMax           any    `json:"rmem_max"`
	WmemMax           any    `json:"wmem_max"`
	TCPRmem           any    `json:"tcp_rmem"`
	TCPWmem           any    `json:"tcp_wmem"`
	GSO               any    `json:"gso"` // primary iface ethtool offload: "on"/"off", null when not reported
	GRO               any    `json:"gro"`
	TCPRetries2       any    `json:"tcp_retries2"` // ALWAYS null today — missing agent field (AGENT-31), never invented
	Error             string `json:"error,omitempty"`
}

// kernelNetReport mirrors the agent-side netHardwareReport fields this read
// surface consumes (the netopt wire contract: docs/NETOPT-AGENT-MODE.md —
// the same JSON both the panel's and the agent's netops_api generate).
type kernelNetReport struct {
	CC           string            `json:"cc"`
	CCAvailable  []string          `json:"cc_available"`
	DefaultQdisc string            `json:"default_qdisc"`
	Sysctls      map[string]string `json:"sysctls"`
	Ifaces       []struct {
		Name     string            `json:"name"`
		Primary  bool              `json:"primary,omitempty"`
		Offloads map[string]string `json:"offloads,omitempty"`
	} `json:"ifaces"`
}

// kernelTTL: cached diagnostics stay fresh for 10 minutes, failures for
// 1 minute (the §2.20/§2.22 feed discipline); the detect command itself
// gets 60s (ethtool runs per interface).
const (
	kernelOKTTL  = 10 * time.Minute
	kernelErrTTL = 1 * time.Minute
	kernelCmdTTL = 60 * time.Second
)

var (
	kernelMu      sync.Mutex
	kernelCache   = map[string]*kernelDiagnostics{}
	kernelRunning = map[string]bool{}
)

// kernelDetectOne issues the agent's read-only netopt.detect and maps the
// report. It never invents: any missing field stays null with the error
// note naming the cause.
func kernelDetectOne(agentID, name string, online bool) *kernelDiagnostics {
	d := &kernelDiagnostics{Agent: agentID, Name: name, Online: online,
		Status: "error", CheckedAt: time.Now().Unix(),
		TCPRetries2: nil, // declared missing (AGENT-31) — never a guessed value
	}
	if !online {
		d.Status = "offline"
		d.Error = "the agent is not connected"
		return d
	}
	cmdFn := agentHubCommandFn.get()
	if cmdFn == nil {
		d.Error = "the agent hub is not wired up on this panel"
		return d
	}
	res, err := cmdFn(agentID, protocol.CmdNetDetect, nil, kernelCmdTTL)
	if err != nil {
		d.Error = "netopt.detect failed: " + err.Error()
		return d
	}
	if !res.OK {
		d.Error = "netopt.detect refused: " + res.Error
		return d
	}
	var op struct {
		Role   string           `json:"role"`
		Report *kernelNetReport `json:"report"`
	}
	if err := json.Unmarshal([]byte(res.Output), &op); err != nil || op.Report == nil {
		d.Error = "the agent's netopt.detect output was not the documented report shape"
		return d
	}
	rep := op.Report
	d.Status = "ok"
	if rep.CC != "" {
		d.CongestionControl = rep.CC
	}
	for _, cc := range rep.CCAvailable {
		if cc == "bbr" {
			d.BBRAvailable = true
			break
		}
	}
	if d.BBRAvailable == nil {
		d.BBRAvailable = false // the list was reported and bbr is not in it
	}
	if rep.DefaultQdisc != "" {
		d.Qdisc = rep.DefaultQdisc
	}
	if v := kernelSysctlInt(rep, "net.ipv4.tcp_mtu_probing"); v != nil {
		d.TCPMTUProbing = *v
	}
	if v := kernelSysctlInt(rep, "net.core.rmem_max"); v != nil {
		d.RmemMax = *v
	}
	if v := kernelSysctlInt(rep, "net.core.wmem_max"); v != nil {
		d.WmemMax = *v
	}
	if v := kernelSysctlStr(rep, "net.ipv4.tcp_rmem"); v != "" {
		d.TCPRmem = v
	}
	if v := kernelSysctlStr(rep, "net.ipv4.tcp_wmem"); v != "" {
		d.TCPWmem = v
	}
	// GSO/GRO: the primary interface's ethtool -k offloads (netdetect.go
	// carries generic-segmentation-offload / generic-receive-offload).
	for _, iff := range rep.Ifaces {
		if !iff.Primary {
			continue
		}
		if v, ok := iff.Offloads["generic-segmentation-offload"]; ok && v != "" {
			d.GSO = v
		}
		if v, ok := iff.Offloads["generic-receive-offload"]; ok && v != "" {
			d.GRO = v
		}
		break
	}
	return d
}

func kernelSysctlInt(rep *kernelNetReport, key string) *int {
	v := kernelSysctlStr(rep, key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

func kernelSysctlStr(rep *kernelNetReport, key string) string {
	if rep == nil || rep.Sysctls == nil {
		return ""
	}
	return strings.TrimSpace(rep.Sysctls[key])
}

// kernelPendingRow is the honest never-probed row.
func kernelPendingRow(a kernelAgentRef) *kernelDiagnostics {
	status := "pending"
	if !a.Online {
		status = "offline"
	}
	return &kernelDiagnostics{Agent: a.ID, Name: a.Name, Online: a.Online,
		Status: status, TCPRetries2: nil}
}

// kernelRowFor answers one agent: cached rows inside their TTL, otherwise a
// blocking probe (single-flight — a concurrent caller gets the row the
// in-flight probe will store, or an honest pending row until it lands).
func kernelRowFor(a kernelAgentRef) *kernelDiagnostics {
	kernelMu.Lock()
	if c := kernelCache[a.ID]; c != nil {
		ttl := kernelErrTTL
		if c.Status == "ok" {
			ttl = kernelOKTTL
		}
		if time.Since(time.Unix(c.CheckedAt, 0)) < ttl {
			kernelMu.Unlock()
			return c
		}
	}
	if kernelRunning[a.ID] {
		kernelMu.Unlock()
		return kernelPendingRow(a) // the in-flight probe has not stored yet
	}
	kernelRunning[a.ID] = true
	kernelMu.Unlock()

	d := kernelDetectOne(a.ID, a.Name, a.Online)

	kernelMu.Lock()
	kernelRunning[a.ID] = false
	kernelCache[a.ID] = d
	kernelMu.Unlock()
	return d
}

// kernelDetectAndStore runs one background probe (single-flight). Returns
// false when another probe for this agent is already running.
func kernelDetectAndStore(a kernelAgentRef) bool {
	kernelMu.Lock()
	if kernelRunning[a.ID] {
		kernelMu.Unlock()
		return false
	}
	kernelRunning[a.ID] = true
	kernelMu.Unlock()
	d := kernelDetectOne(a.ID, a.Name, a.Online)
	kernelMu.Lock()
	kernelRunning[a.ID] = false
	kernelCache[a.ID] = d
	kernelMu.Unlock()
	return true
}

// kernelFreshRows serves the rows from cache only (no probing) — the
// all-agents read and the run=1 immediate answer.
func kernelFreshRows(agents []kernelAgentRef) []*kernelDiagnostics {
	now := time.Now()
	out := make([]*kernelDiagnostics, 0, len(agents))
	kernelMu.Lock()
	for _, a := range agents {
		c := kernelCache[a.ID]
		if c != nil && time.Since(time.Unix(c.CheckedAt, 0)) < kernelOKTTL {
			out = append(out, c)
			continue
		}
		status, errNote := "pending", ""
		if c != nil && now.Sub(time.Unix(c.CheckedAt, 0)) < kernelErrTTL {
			status, errNote = c.Status, c.Error // a fresh failure stays honest
		}
		row := kernelPendingRow(a)
		row.Status = status
		row.Error = errNote
		out = append(out, row)
	}
	kernelMu.Unlock()
	return out
}

type kernelAgentRef struct {
	ID     string
	Name   string
	Online bool
}

// handleOptimizationKernel serves GET /api/optimization/kernel
// [?agent=<id>][&run=1] — the read-only kernel-tuning surface (§2.23.2).
// Without ?agent it answers every enrolled agent from cache (never probed
// = pending); ?agent=<id> answers that agent (blocking probe past the
// cache TTL, single-flight); run=1 refreshes in the background
// (single-flight per agent) and answers immediately.
func handleOptimizationKernel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	run := q.Get("run") == "1" // any other value is inert (§2.22 refresh discipline)
	agentID := strings.TrimSpace(q.Get("agent"))
	s := meshStoreOr503(w, r)
	if s == nil {
		return
	}
	if agentID != "" {
		if !validAgentID(agentID) {
			writeAPIError(w, r, "E-IN-06", "bad agent id")
			return
		}
		ag, err := s.GetAgent(agentID)
		if err != nil {
			writeAPIError(w, r, "E-IN-04", "no such agent")
			return
		}
		a := kernelAgentRef{ID: ag.ID, Name: ag.Name, Online: uiAgentOnline(ag)}
		if run {
			go kernelDetectAndStore(a) // background single-flight refresh
			writeJSON(w, map[string]any{"profile": kernelProfile(), "agents": kernelFreshRows([]kernelAgentRef{a})})
			return
		}
		writeJSON(w, map[string]any{"profile": kernelProfile(), "agents": []*kernelDiagnostics{kernelRowFor(a)}})
		return
	}
	ags, err := s.ListAgents()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "mesh store unavailable (details in the panel error journal)")
		return
	}
	refs := make([]kernelAgentRef, 0, len(ags))
	for _, a := range ags {
		refs = append(refs, kernelAgentRef{ID: a.ID, Name: a.Name, Online: uiAgentOnline(a)})
	}
	if run {
		// background refresh for every agent whose cache is missing/stale —
		// single-flight per agent, the GET answers immediately
		for _, a := range refs {
			stale := true
			kernelMu.Lock()
			if c := kernelCache[a.ID]; c != nil && time.Since(time.Unix(c.CheckedAt, 0)) < kernelOKTTL {
				stale = false
			}
			if stale && !kernelRunning[a.ID] {
				kernelRunning[a.ID] = true
				go func(ref kernelAgentRef) {
					d := kernelDetectOne(ref.ID, ref.Name, ref.Online)
					kernelMu.Lock()
					kernelRunning[ref.ID] = false
					kernelCache[ref.ID] = d
					kernelMu.Unlock()
				}(a)
			}
			kernelMu.Unlock()
		}
	}
	rows := kernelFreshRows(refs)
	sort.Slice(rows, func(i, j int) bool { return rows[i].Agent < rows[j].Agent })
	writeJSON(w, map[string]any{"profile": kernelProfile(), "agents": rows})
}
