package main

// Quantum firewall & port-rule engine (Session 1 — Firewall and Port
// Management). One backend is detected and MANAGED per host; the engine
// never assumes two tools can safely share a ruleset:
//
//	ufw       — active UFW: rules go through the ufw CLI with a
//	            "quantum:<owner>" comment; deletes target the exact rule.
//	iptables  — no UFW but iptables/ip6tables available (legacy OR the
//	            nft compatibility layer): rules live ONLY inside the
//	            Quantum-owned chain QUANTUM_FW, reached by one commented
//	            jump from INPUT. Deletes are by exact rule spec.
//	nftables  — native nftables with no usable iptables/ufw: DETECT and
//	            SNAPSHOT only. Quantum refuses to modify a foreign
//	            nftables ruleset (an accept rule in a foreign ruleset
//	            cannot be owned safely; co-managing it is how firewalls
//	            get broken). The refusal names the safe alternatives.
//
// Ownership model: every Quantum rule is identifiable — ufw rules carry a
// `quantum:<owner>` comment, iptables rules live in the QUANTUM_FW chain
// with a `quantum:<owner>` comment, and the INPUT jump carries the
// `quantum-fw` comment. Removing a tunnel removes only rules tagged with
// that owner. These operations are FORBIDDEN by construction and by a
// hard refusal layer (fwForbidden): ufw reset, global iptables -F/-X,
// NAT-table flushes, broad nftables table deletion, whole-ruleset
// replacement.
//
// Every apply records a before/after snapshot plus the executed commands
// in <configDir>/firewall_audit.jsonl (append-only), supports dry-run,
// and rolls back its OWN applied steps in reverse order when a step
// fails. All host execution goes through the fwRun seam so tests run
// hermetically; tests/test_firewall_lab.sh exercises the real backends
// inside a disposable network namespace on Linux.

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- execution seam ----

// fwRun executes one host command (combined output). Tests swap it.
var fwRun = func(name string, args ...string) (string, error) {
	out, err := runCmdTimeout(15*time.Second, name, args...)
	return string(out), err
}

// ---- model ----

// fwBackend is the detected active firewall backend.
type fwBackend string

const (
	fwBackendUfw      fwBackend = "ufw"
	fwBackendIptables fwBackend = "iptables"
	fwBackendNft      fwBackend = "nftables"
	fwBackendNone     fwBackend = "none"
)

const (
	// fwChain is the Quantum-owned filter chain (iptables backend). The
	// name follows the existing QUANTUM_* ownership convention (QUANTUM_ACCT_*,
	// HLB*, QMX*): nothing outside this engine writes into it, and this
	// engine never writes outside it.
	fwChain = "QUANTUM_FW"
	// fwJumpComment tags the single INPUT jump rule that reaches the chain.
	fwJumpComment = "quantum-fw"
	// fwCommentPrefix tags every Quantum-owned rule (ufw comments and
	// iptables rule comments) with its owner: "quantum:tunnel:at_x".
	fwCommentPrefix = "quantum:"
)

// fwPortRule is one allow rule the engine manages.
type fwPortRule struct {
	Port   int    `json:"port"`           // 1-65535
	Proto  string `json:"proto"`          // "tcp" | "udp"
	Family string `json:"family"`         // "ipv4" | "ipv6" | "both"
	Owner  string `json:"owner"`          // e.g. "tunnel:at_1a2b3c4d", "service:panel"
	Note   string `json:"note,omitempty"` // human description for the audit trail
}

func (r fwPortRule) validate() error {
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("port %d out of range", r.Port)
	}
	if r.Proto != "tcp" && r.Proto != "udp" {
		return fmt.Errorf("proto must be tcp or udp, got %q", r.Proto)
	}
	if r.Family != "ipv4" && r.Family != "ipv6" && r.Family != "both" {
		return fmt.Errorf("family must be ipv4, ipv6 or both, got %q", r.Family)
	}
	if strings.TrimSpace(r.Owner) == "" || strings.ContainsAny(r.Owner, "\"' \t") {
		// Whitespace is forbidden too: the owner becomes an iptables
		// comment token, and the normalized -S line is re-tokenized for
		// exact deletes.
		return fmt.Errorf("owner must be a non-empty string without quotes or whitespace")
	}
	return nil
}

// fwRuleComment is the identifying comment for one rule.
func fwRuleComment(r fwPortRule) string {
	return fwCommentPrefix + r.Owner
}

// fwSnapshot captures the backend ruleset text at one point in time.
type fwSnapshot struct {
	Backend fwBackend `json:"backend"`
	At      int64     `json:"at"`
	Raw     string    `json:"raw"`
}

// fwOp is one planned change.
type fwOp struct {
	Kind string     `json:"kind"` // "add" | "remove"
	Rule fwPortRule `json:"rule"`
}

// fwPlan is an ordered set of changes for one backend.
type fwPlan struct {
	ID      string    `json:"id"`
	Backend fwBackend `json:"backend"`
	DryRun  bool      `json:"dry_run,omitempty"`
	Ops     []fwOp    `json:"ops"`
}

// fwAppliedOp records one executed step and how to undo it.
type fwAppliedOp struct {
	Op      fwOp       `json:"op"`
	Cmds    [][]string `json:"cmds"`
	Undo    [][]string `json:"undo"`
	Skipped bool       `json:"skipped,omitempty"` // idempotent no-op
}

// fwApplyResult is the outcome of one plan execution.
type fwApplyResult struct {
	Plan        fwPlan        `json:"plan"`
	Before      fwSnapshot    `json:"before"`
	After       *fwSnapshot   `json:"after,omitempty"`
	Applied     []fwAppliedOp `json:"applied"`
	PlannedCmds [][]string    `json:"planned_cmds,omitempty"` // dry-run only
	RolledBack  int           `json:"rolled_back,omitempty"`
	Error       string        `json:"error,omitempty"`
}

// fwErrNftForeign marks the safe refusal to modify a foreign nftables
// ruleset.
var fwErrNftForeign = errors.New("native nftables backend: Quantum does not modify a foreign nftables ruleset; manage opens via UFW, or add the allow rules to your own nftables ruleset yourself")

// ---- detection ----

// fwDetect identifies the ONE active backend this engine will talk to.
// Order matters: an active UFW owns the whole ruleset (its iptables/
// nftables output is ufw's), so ufw wins whenever it is active.
func fwDetect() (fwBackend, string) {
	if out, err := fwRun("ufw", "status"); err == nil && strings.Contains(out, "Status: active") {
		return fwBackendUfw, "ufw status: active"
	}
	if _, err := fwRun("iptables", "-S"); err == nil {
		detail := "iptables available"
		if v, verr := fwRun("iptables", "--version"); verr == nil {
			detail = strings.TrimSpace(v)
		}
		return fwBackendIptables, detail
	}
	if _, err := fwRun("nft", "list", "tables"); err == nil {
		return fwBackendNft, "nft list tables ok"
	}
	return fwBackendNone, "no firewall tool answered"
}

// ---- snapshots ----

// fwCaptureSnapshot captures the current ruleset of the detected backend.
func fwCaptureSnapshot() (fwSnapshot, error) {
	backend, _ := fwDetect()
	snap := fwSnapshot{Backend: backend, At: time.Now().Unix()}
	switch backend {
	case fwBackendUfw:
		out, err := fwRun("ufw", "status", "verbose")
		if err != nil {
			return snap, err
		}
		snap.Raw = out
	case fwBackendIptables:
		var b strings.Builder
		for _, tool := range []string{"iptables", "ip6tables"} {
			out, err := fwRun(tool, "-S")
			if err != nil {
				continue // ip6tables may legitimately be absent
			}
			b.WriteString("# " + tool + "\n" + out)
		}
		snap.Raw = b.String()
	case fwBackendNft:
		out, err := fwRun("nft", "list", "ruleset")
		if err != nil {
			return snap, err
		}
		snap.Raw = out
	default:
		snap.Raw = ""
	}
	return snap, nil
}

// ---- hard safety layer ----

// fwForbidden reports whether an argv is one of the operations Quantum
// must never run. It is checked before EVERY execution, so even a bug in
// the command builders cannot flush a foreign ruleset.
func fwForbidden(name string, args []string) bool {
	joined := strings.Join(args, " ")
	switch name {
	case "ufw":
		if len(args) > 0 && args[0] == "reset" {
			return true
		}
	case "iptables", "ip6tables":
		for i, a := range args {
			switch a {
			case "-F", "--flush":
				// Flushing is only allowed scoped to the Quantum chain.
				if i+1 >= len(args) || args[i+1] != fwChain {
					return true
				}
			case "-X", "--delete-chain":
				if i+1 >= len(args) || args[i+1] != fwChain {
					return true
				}
			case "-t", "--table":
				if i+1 < len(args) && args[i+1] == "nat" && strings.Contains(joined, " -F") {
					return true
				}
			}
		}
	case "nft":
		if strings.Contains(joined, "delete table") || strings.Contains(joined, "flush ruleset") ||
			strings.Contains(joined, "delete ruleset") {
			return true
		}
	}
	return false
}

// ---- command builders ----

// fwUfwCmds builds the ufw argv list for one op. ufw add is naturally
// idempotent ("Skipping adding existing rule"); removes are guarded by a
// presence check in the executor so a non-existent rule never errors.
func fwUfwCmds(op fwOp) ([][]string, [][]string) {
	spec := fmt.Sprintf("%d/%s", op.Rule.Port, op.Rule.Proto)
	comment := fwRuleComment(op.Rule)
	if op.Kind == "add" {
		return [][]string{{"ufw", "allow", spec, "comment", comment}},
			[][]string{{"ufw", "delete", "allow", spec}}
	}
	return [][]string{{"ufw", "delete", "allow", spec}},
		[][]string{{"ufw", "allow", spec, "comment", comment}}
}

// fwIptablesSpec is the rule spec the engine appends (without the -A/-D
// and chain). NOTE: `iptables -S` normalizes rules (adds implicit `-m
// tcp`, quotes the comment) and — on the nft compatibility layer — a
// missing chain makes `-S <chain>` exit 0 with EMPTY output. Therefore
// presence checks match on tokens (fwIptablesMatchLines), never on exact
// text, and removes delete the REAL normalized line from -S.
func fwIptablesSpec(r fwPortRule) string {
	return fmt.Sprintf("-p %s --dport %d -m comment --comment %s -j ACCEPT", r.Proto, r.Port, fwRuleComment(r))
}

// fwIptablesFamilies expands the rule family into the tools it touches.
func fwIptablesFamilies(r fwPortRule) []string {
	switch r.Family {
	case "ipv6":
		return []string{"ip6tables"}
	case "both":
		return []string{"iptables", "ip6tables"}
	default:
		return []string{"iptables"}
	}
}

// fwIptablesMatchLines returns the live `-S fwChain` lines (normalized
// form) that belong to one rule: same proto, same dport, same owner
// comment. Quote characters are stripped on both sides because iptables
// quotes comments in -S output.
func fwIptablesMatchLines(tool string, r fwPortRule) ([]string, error) {
	out, err := fwRun(tool, "-S", fwChain)
	if err != nil {
		// A missing chain also lands here on legacy iptables; treat as
		// "no rules yet".
		return nil, nil
	}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.ReplaceAll(line, "\"", ""))
		if !strings.HasPrefix(line, "-A "+fwChain+" ") {
			continue
		}
		if strings.Contains(line, "-p "+r.Proto+" ") &&
			strings.Contains(line, fmt.Sprintf("--dport %d ", r.Port)) &&
			strings.Contains(line, "--comment "+fwRuleComment(r)+" ") {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// fwIptablesPresent checks whether the rule already exists (token match —
// see fwIptablesMatchLines for why exact text cannot work).
func fwIptablesPresent(tool string, r fwPortRule) bool {
	lines, _ := fwIptablesMatchLines(tool, r)
	return len(lines) > 0
}

// fwEnsureIptablesChain creates the Quantum chain and its INPUT jump once.
// Both steps are idempotent and neither is part of plan rollback (the
// chain is reusable infrastructure, not a per-rule change).
func fwEnsureIptablesChain(tool string) error {
	// `-S <chain>` cannot be trusted for existence (nft-compat exits 0 on
	// a missing chain): create the chain unconditionally and tolerate the
	// "already exists" answer.
	out, err := fwRun(tool, "-N", fwChain)
	if err != nil && !strings.Contains(strings.ToLower(out), "already exist") {
		return fmt.Errorf("cannot create chain %s: %v", fwChain, err)
	}
	out, err = fwRun(tool, "-S", "INPUT")
	if err != nil {
		return fmt.Errorf("cannot read INPUT rules: %v", err)
	}
	if strings.Contains(out, "-j "+fwChain) {
		return nil
	}
	if _, err := fwRun(tool, "-I", "INPUT", "1", "-m", "comment", "--comment", fwJumpComment, "-j", fwChain); err != nil {
		return fmt.Errorf("cannot hook %s into INPUT: %v", fwChain, err)
	}
	return nil
}

// fwIptablesRuleArgv builds one iptables command as PROPER argv elements
// (a whole rule must never travel as a single argument — iptables would
// read "-p tcp --dport …" as one option value). action is "-A" or "-D".
func fwIptablesRuleArgv(tool, action string, r fwPortRule) []string {
	return []string{
		tool, action, fwChain,
		"-p", r.Proto, "--dport", strconv.Itoa(r.Port),
		"-m", "comment", "--comment", fwRuleComment(r),
		"-j", "ACCEPT",
	}
}

// fwIptablesCmds builds the argv list for one op (one per family), plus
// the inverse commands for rollback. Removes target the REAL normalized
// -S lines (tokenized), so a delete can never touch a different rule. In
// dry-run the chain-creation step is skipped entirely: a plan must never
// touch the host.
func fwIptablesCmds(op fwOp, dry bool) ([][]string, [][]string, error) {
	var cmds, undo [][]string
	for _, tool := range fwIptablesFamilies(op.Rule) {
		if op.Kind == "add" {
			if fwIptablesPresent(tool, op.Rule) {
				continue // idempotent no-op
			}
			if !dry {
				if err := fwEnsureIptablesChain(tool); err != nil {
					return nil, nil, err
				}
			}
			cmds = append(cmds, fwIptablesRuleArgv(tool, "-A", op.Rule))
			undo = append(undo, fwIptablesRuleArgv(tool, "-D", op.Rule))
		} else {
			lines, err := fwIptablesMatchLines(tool, op.Rule)
			if err != nil {
				return nil, nil, err
			}
			if len(lines) == 0 {
				continue // idempotent no-op
			}
			for _, line := range lines {
				cmds = append(cmds, append([]string{tool, "-D", fwChain}, strings.Fields(strings.TrimPrefix(line, "-A "+fwChain+" "))...))
				undo = append(undo, fwIptablesRuleArgv(tool, "-A", op.Rule))
			}
		}
	}
	return cmds, undo, nil
}

// fwUfwRulePresent checks `ufw status` for the exact port/proto rule.
func fwUfwRulePresent(spec string) bool {
	out, err := fwRun("ufw", "status")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == spec {
			return true
		}
	}
	return false
}

// ---- planning ----

// fwPortSpec is one port/transport pair a caller wants a rule for.
type fwPortSpec struct {
	Port  int
	Proto string
}

// fwNewPlan validates rules and returns a plan for the detected backend.
func fwNewPlan(ops []fwOp) (fwPlan, error) {
	backend, _ := fwDetect()
	p := fwPlan{ID: "fw_" + randHex(6), Backend: backend}
	for _, op := range ops {
		if op.Kind != "add" && op.Kind != "remove" {
			return p, fmt.Errorf("op kind must be add or remove, got %q", op.Kind)
		}
		if err := op.Rule.validate(); err != nil {
			return p, fmt.Errorf("op %s: %v", op.Kind, err)
		}
		p.Ops = append(p.Ops, op)
	}
	return p, nil
}

// fwPlanAddOwner plans allow rules for one owner's ports.
func fwPlanAddOwner(owner, family, note string, ports []fwPortSpec) (fwPlan, error) {
	var ops []fwOp
	for _, p := range ports {
		ops = append(ops, fwOp{Kind: "add", Rule: fwPortRule{
			Port: p.Port, Proto: p.Proto, Family: family, Owner: owner, Note: note,
		}})
	}
	return fwNewPlan(ops)
}

// fwPlanRemoveOwner plans the removal of every rule tagged with the owner.
// The registry (portregistry.go) is the source of truth for what the owner
// holds, so tunnel teardown never guesses.
func fwPlanRemoveOwner(owner string) (fwPlan, error) {
	var ops []fwOp
	for _, e := range portEntriesForOwner(owner) {
		ops = append(ops, fwOp{Kind: "remove", Rule: fwPortRule{
			Port: e.Port, Proto: e.Proto, Family: e.Family, Owner: owner,
		}})
	}
	return fwNewPlan(ops)
}

// ---- execution ----

// fwAuditPath is the append-only audit trail.
func fwAuditPath() string { return filepath.Join(configDir, "firewall_audit.jsonl") }

type fwAuditEntry struct {
	At         int64      `json:"at"`
	PlanID     string     `json:"plan_id"`
	Backend    fwBackend  `json:"backend"`
	DryRun     bool       `json:"dry_run,omitempty"`
	Ops        []fwOp     `json:"ops"`
	Applied    int        `json:"applied"`
	Skipped    int        `json:"skipped,omitempty"`
	RolledBack int        `json:"rolled_back,omitempty"`
	Error      string     `json:"error,omitempty"`
	Before     string     `json:"before"`
	After      string     `json:"after,omitempty"`
	Cmds       [][]string `json:"cmds,omitempty"`
}

var fwAuditMu sync.Mutex

func fwAuditAppend(e fwAuditEntry) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	fwAuditMu.Lock()
	defer fwAuditMu.Unlock()
	f, err := os.OpenFile(fwAuditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

// fwApply executes a plan. With DryRun it only reports the commands that
// would run. Real runs snapshot before/after, execute through the
// forbidden-command refusal layer, roll back THEIR OWN applied steps in
// reverse on the first failure, and append one audit entry either way.
func fwApply(plan fwPlan) (fwApplyResult, error) {
	res := fwApplyResult{Plan: plan, RolledBack: 0}
	before, err := fwCaptureSnapshot()
	if err != nil {
		return res, fmt.Errorf("cannot snapshot the ruleset: %v", err)
	}
	res.Before = before

	switch plan.Backend {
	case fwBackendUfw:
	case fwBackendIptables:
	case fwBackendNft:
		// Safe refusal: Quantum never edits a foreign nftables ruleset.
		res.Error = fwErrNftForeign.Error()
		fwAuditAppend(fwAuditEntry{At: time.Now().Unix(), PlanID: plan.ID, Backend: plan.Backend,
			DryRun: plan.DryRun, Ops: plan.Ops, Before: before.Raw, Error: res.Error})
		return res, fwErrNftForeign
	case fwBackendNone:
		res.Error = "no firewall backend detected; nothing to manage (ports remain registry-only)"
		fwAuditAppend(fwAuditEntry{At: time.Now().Unix(), PlanID: plan.ID, Backend: plan.Backend,
			DryRun: plan.DryRun, Ops: plan.Ops, Before: before.Raw, Error: res.Error})
		return res, errors.New(res.Error)
	}

	rollback := func(reason error) {
		for i := len(res.Applied) - 1; i >= 0; i-- {
			a := res.Applied[i]
			if a.Skipped {
				continue
			}
			for _, cmd := range a.Undo {
				if len(cmd) == 0 {
					continue
				}
				if fwForbidden(cmd[0], cmd[1:]) {
					log.Printf("[FW] rollback refused a forbidden command (bug): %v", cmd)
					continue
				}
				_, _ = fwRun(cmd[0], cmd[1:]...)
				res.RolledBack++
			}
		}
		res.Error = reason.Error()
	}

	for _, op := range plan.Ops {
		var cmds, undo [][]string
		switch plan.Backend {
		case fwBackendUfw:
			// ufw add dedupes itself; a guarded remove needs presence.
			cmds, undo = fwUfwCmds(op)
			if op.Kind == "remove" && !fwUfwRulePresent(fmt.Sprintf("%d/%s", op.Rule.Port, op.Rule.Proto)) {
				res.Applied = append(res.Applied, fwAppliedOp{Op: op, Skipped: true})
				continue
			}
		case fwBackendIptables:
			var err error
			cmds, undo, err = fwIptablesCmds(op, plan.DryRun)
			if err != nil {
				rollback(err)
				fwAuditAppend(res.auditEntry(before, nil))
				return res, err
			}
			if len(cmds) == 0 {
				res.Applied = append(res.Applied, fwAppliedOp{Op: op, Skipped: true})
				continue
			}
		}
		if plan.DryRun {
			res.PlannedCmds = append(res.PlannedCmds, cmds...)
			res.Applied = append(res.Applied, fwAppliedOp{Op: op, Skipped: true})
			continue
		}
		applied := fwAppliedOp{Op: op, Cmds: cmds, Undo: undo}
		for _, cmd := range cmds {
			if fwForbidden(cmd[0], cmd[1:]) {
				err := fmt.Errorf("refused forbidden command: %v", cmd)
				rollback(err)
				fwAuditAppend(res.auditEntry(before, nil))
				return res, err
			}
			if _, err := fwRun(cmd[0], cmd[1:]...); err != nil {
				err = fmt.Errorf("%v: %v", cmd, err)
				rollback(err)
				fwAuditAppend(res.auditEntry(before, nil))
				return res, err
			}
		}
		// Ownership recording: every applied add registers the port in the
		// registry (the plan already validated it), so a later
		// `firewall teardown --owner …` can find and remove exactly what
		// this apply opened. Every remove — executed OR skipped — drops
		// the record: a skipped remove means the rule is already absent
		// from the host, so the ownership record is stale. Without this,
		// an installer-only apply (no separate ports reserve call) would
		// open a port the engine could never clean up.
		if op.Kind == "add" {
			if err := portRegistryRecord(op.Rule); err != nil {
				log.Printf("[FW] apply: cannot record ownership for %d/%s: %v", op.Rule.Port, op.Rule.Proto, err)
			}
		} else {
			if err := portRegistryForget(op.Rule.Proto, op.Rule.Port); err != nil {
				log.Printf("[FW] apply: cannot drop ownership for %d/%s: %v", op.Rule.Port, op.Rule.Proto, err)
			}
		}
		res.Applied = append(res.Applied, applied)
	}

	if plan.DryRun {
		fwAuditAppend(res.auditEntry(before, nil))
		return res, nil
	}
	after, err := fwCaptureSnapshot()
	if err == nil {
		res.After = &after
	}
	fwAuditAppend(res.auditEntry(before, res.After))
	return res, nil
}

func (r fwApplyResult) auditEntry(before fwSnapshot, after *fwSnapshot) fwAuditEntry {
	e := fwAuditEntry{
		At: time.Now().Unix(), PlanID: r.Plan.ID, Backend: r.Plan.Backend,
		DryRun: r.Plan.DryRun, Ops: r.Plan.Ops, Before: before.Raw,
		Error: r.Error, RolledBack: r.RolledBack,
	}
	if after != nil {
		e.After = after.Raw
	}
	for _, a := range r.Applied {
		if !a.Skipped {
			e.Applied++
			e.Cmds = append(e.Cmds, a.Cmds...)
		} else {
			e.Skipped++
		}
	}
	return e
}

// ---- tunnel teardown integration helper ----

// fwTeardownOwner plans and applies the removal of every firewall rule
// tagged with the owner (e.g. "tunnel:at_1a2b3c4d"). Only Quantum-owned
// rules can match, so a tunnel deletion can never touch administrator or
// third-party rules.
func fwTeardownOwner(owner string) (fwApplyResult, error) {
	plan, err := fwPlanRemoveOwner(owner)
	if err != nil {
		return fwApplyResult{}, err
	}
	if len(plan.Ops) == 0 {
		return fwApplyResult{Plan: plan}, nil
	}
	return fwApply(plan)
}
