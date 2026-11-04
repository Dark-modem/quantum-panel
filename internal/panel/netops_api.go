package panel

// netops_api.go — the structured operation layer of the Network Optimization
// subsystem. Each op returns a JSON-tagged result with a stable shape so the
// Agent protocol (Session 3) can transport it verbatim; the wire contract is
// documented in docs/NETOPT-AGENT-MODE.md and MUST NOT change casually.
//
// Install roles (netInstallRole):
//   - "controller": <configDir>/panel.json present (full panel)
//   - "agent":      <configDir>/agent.json present, no panel.json (lightweight
//     endpoint — the panel.json check that Session 6's QA caught is gone;
//     the engine never requires the full-panel config here and never creates
//     a fake one)
//   - "none":       neither file — read-only ops (detect/plan/status) still
//     work (the installer may pre-flight a box), mutating ops are refused
//
// Privileges: detect/plan/status are read-only. apply/rollback/revert/
// apply-nic require root (uid 0) and are refused with a clear error
// otherwise; the engine's writes and sysctl calls all need it anyway.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
)

// netInstallRole is what this installation is, from the files on disk.
type netInstallRole string

const (
	netInstallNone       netInstallRole = "none"
	netInstallController netInstallRole = "controller"
	netInstallAgent      netInstallRole = "agent"
)

// netPrivileged is the privilege seam (tests stub it; default is uid 0).
var netPrivileged = func() bool { return os.Geteuid() == 0 }

var netErrNoInstallation = errors.New("netopt: no installation found (need panel.json for the controller or agent.json for an enrolled agent in the config directory)")

var netErrPrivilege = errors.New("netopt: this operation changes system state and requires root (uid 0)")

// netInstallRoleOf inspects the config directory. Read-only.
func netInstallRoleOf() netInstallRole {
	_, panelErr := os.Stat(cfgPath())
	_, agentErr := os.Stat(agentIdentityPath())
	switch {
	case panelErr == nil:
		return netInstallController
	case agentErr == nil:
		return netInstallAgent
	}
	return netInstallNone
}

// netRequireWritableInstall refuses mutating ops on machines without an
// installation identity and without root.
func netRequireWritableInstall() error {
	if netInstallRoleOf() == netInstallNone {
		return netErrNoInstallation
	}
	if !netPrivileged() {
		return netErrPrivilege
	}
	return nil
}

// ---------------------------------------------------------------------------
// Operation results — stable JSON shapes (the transport contract)
// ---------------------------------------------------------------------------

// NetopDetectResult is the netopt.detect payload (netHardwareReport itself
// is fully JSON-tagged; the envelope adds role and warnings count).
type NetopDetectResult struct {
	Role   string             `json:"role"`
	Report *netHardwareReport `json:"report"`
}

// NetopPlanResult is the netopt.plan payload.
type NetopPlanResult struct {
	Role     string             `json:"role"`
	Profile  string             `json:"profile"`
	Reasons  []string           `json:"reasons"`
	Summary  string             `json:"summary"`
	Changes  []netPlannedChange `json:"changes"`
	Registry *netHardwareReport `json:"registry"`
}

// NetopApplyResult is the netopt.apply / netopt.rollback / netopt.revert
// payload: the journal plus a human message.
type NetopApplyResult struct {
	Role    string      `json:"role"`
	Op      string      `json:"op"` // apply | rollback | revert
	Message string      `json:"message"`
	Ok      bool        `json:"ok"`
	Journal *netJournal `json:"journal,omitempty"`
}

// NetopStatusResult is the netopt.status payload.
type NetopStatusResult struct {
	Role       string       `json:"role"`
	HasJournal bool         `json:"has_journal"`
	Journal    *netJournal  `json:"journal,omitempty"`
	Drift      []netDrifted `json:"drift,omitempty"`
}

// netDrifted is one key whose live value no longer matches the journal.
type netDrifted struct {
	Key    string `json:"key"`
	Live   string `json:"live"`
	Wanted string `json:"wanted"`
}

// NetopDetect runs netopt.detect.
func NetopDetect() (*NetopDetectResult, error) {
	return &NetopDetectResult{Role: string(netInstallRoleOf()), Report: netDetectHardware()}, nil
}

// NetopPlan runs netopt.plan for one role ("" = auto) and optional forced
// profile. Read-only: nothing is applied.
func NetopPlan(role netRole, forcedProfile string) (*NetopPlanResult, error) {
	rep := netDetectHardware()
	resolved := netRoleFor(role)
	profile, why := netSelectProfile(rep, resolved, forcedProfile)
	changes := netPlanChanges(profile, rep, resolved)
	return &NetopPlanResult{
		Role:     string(resolved),
		Profile:  string(profile),
		Reasons:  why,
		Summary:  netProfileSummary(profile, changes),
		Changes:  changes,
		Registry: rep,
	}, nil
}

// NetopApply applies a profile ("" = measured selection). Requires an
// installation identity and root.
func NetopApply(role netRole, forcedProfile, justify string, reasons []string) (*NetopApplyResult, error) {
	if err := netRequireWritableInstall(); err != nil {
		return nil, err
	}
	if forcedProfile == string(netProfilePerformance) && justify == "" {
		return nil, fmt.Errorf("netopt: --profile performance requires --justify (recorded in the journal)")
	}
	resolved := netRoleFor(role)
	forced := ""
	if forcedProfile != "" && forcedProfile != "auto" {
		forced = forcedProfile
	}
	rep := netDetectHardware()
	profile, why := netSelectProfile(rep, resolved, forced)
	if justify != "" {
		why = append(why, "operator justification: "+justify)
	}
	j, err := netApply(profile, resolved, why)
	if err != nil {
		return &NetopApplyResult{Role: string(resolved), Op: "apply", Ok: false, Message: err.Error(), Journal: j}, err
	}
	return &NetopApplyResult{
		Role: string(resolved), Op: "apply", Ok: true,
		Message: fmt.Sprintf("applied profile=%s settings=%d owned_files=%d", j.Profile, len(j.Applied), len(j.OwnedFiles)),
		Journal: j,
	}, nil
}

// NetopRollback undoes the last apply.
func NetopRollback() (*NetopApplyResult, error) {
	if err := netRequireWritableInstall(); err != nil {
		return nil, err
	}
	msg, err := netRollback()
	res := &NetopApplyResult{Op: "rollback", Ok: err == nil, Message: msg}
	if j := netLoadJournal(); j != nil {
		res.Role = j.Role
		res.Journal = j
	}
	if err != nil {
		if msg == "" {
			res.Message = err.Error()
		}
		return res, err
	}
	return res, nil
}

// NetopRevert removes owned files and restores the earliest baseline.
func NetopRevert() (*NetopApplyResult, error) {
	if err := netRequireWritableInstall(); err != nil {
		return nil, err
	}
	msg, err := netRevert()
	res := &NetopApplyResult{Op: "revert", Ok: err == nil, Message: msg}
	if j := netLoadJournal(); j != nil {
		res.Role = j.Role
		res.Journal = j
	}
	if err != nil {
		if msg == "" {
			res.Message = err.Error()
		}
		return res, err
	}
	return res, nil
}

// NetopStatus reports the last journal and any live drift.
func NetopStatus() (*NetopStatusResult, error) {
	role := netInstallRoleOf()
	res := &NetopStatusResult{Role: string(role)}
	j := netLoadJournal()
	if j == nil {
		return res, nil
	}
	res.HasJournal = true
	res.Journal = j
	rep := netDetectHardware()
	for _, s := range j.Applied {
		if s.Kind != "sysctl" {
			continue
		}
		// whitespace-normalized compare: the kernel prints vector values with
		// tabs while the journal stores the profile's space-separated form —
		// a formatting difference is not drift (live-acceptance finding).
		if live := rep.Sysctls[s.Key]; !netSameSysctlValue(live, s.After) {
			res.Drift = append(res.Drift, netDrifted{Key: s.Key, Live: live, Wanted: s.After})
		}
	}
	return res, nil
}

// netopJSON renders any op result as the stable wire JSON.
func netopJSON(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{\"error\": " + strconv.Quote(err.Error()) + "}"
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Dispatcher — the single handler entry point for Session 3's Agent
// protocol. One typed request, one function, the stable result types above.
// ---------------------------------------------------------------------------

// NetopRequest is the typed request for every netopt operation.
type NetopRequest struct {
	Op      string `json:"op"`                // detect|plan|apply|status|rollback|revert
	Role    string `json:"role,omitempty"`    // controller|agent|auto ("" = auto)
	Profile string `json:"profile,omitempty"` // conservative|balanced|performance|auto
	Justify string `json:"justify,omitempty"` // required for profile=performance
}

// netopValidOps is the dispatcher's operation table.
var netopValidOps = map[string]bool{
	"detect": true, "plan": true, "apply": true,
	"status": true, "rollback": true, "revert": true,
}

// NetopDispatch routes one typed request to its handler. The returned value
// is one of the stable result types (marshal directly); the error is a Go
// error for the transport to wrap. Mutating ops enforce the installation and
// privilege rules internally.
func NetopDispatch(req NetopRequest) (any, error) {
	if !netopValidOps[req.Op] {
		return nil, fmt.Errorf("netop: unknown op %q (valid: detect, plan, apply, status, rollback, revert)", req.Op)
	}
	role := netRole(req.Role)
	forced := req.Profile
	if forced == "auto" {
		forced = ""
	}
	switch req.Op {
	case "detect":
		return NetopDetect()
	case "plan":
		return NetopPlan(role, forced)
	case "apply":
		return NetopApply(role, forced, req.Justify, nil)
	case "status":
		return NetopStatus()
	case "rollback":
		return NetopRollback()
	case "revert":
		return NetopRevert()
	}
	return nil, fmt.Errorf("netop: unreachable op %q", req.Op) // guarded by netopValidOps
}
