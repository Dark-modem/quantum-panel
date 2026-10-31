package main

// gre-panel firewall … / gre-panel ports …: the terminal face of the
// firewall & port engine (Session 1). Design rules:
//
//   - plan is DRY-RUN by default and prints the exact commands that
//     would run; nothing mutates.
//   - apply/teardown refuse to run without an explicit --yes.
//   - output is structured JSON on stdout ({"error": …} + exit 1 on
//     failure, exit 2 on usage errors).
//   - no arbitrary shell execution exists on this path: every change
//     goes through the engine's validated plan/apply and the
//     forbidden-command refusal layer (fwForbidden).

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// firewallCLI is the main.go hook: `gre-panel firewall …` / `gre-panel ports …`.
func firewallCLI(top string, args []string) int {
	if !cliStart() {
		return 1
	}
	var exit int
	var payload any
	switch top {
	case "firewall":
		exit, payload = fwCLIFirewall(args)
	case "ports":
		exit, payload = fwCLIPorts(args)
	default:
		fmt.Fprintln(os.Stderr, "unknown command: "+top)
		return 2
	}
	if payload != nil {
		enc, err := json.MarshalIndent(payload, "", "  ")
		if err == nil {
			fmt.Println(string(enc))
		}
	}
	return exit
}

func fwCLIFirewall(args []string) (int, any) {
	sub := "detect"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "detect":
		return fwCLIDetect()
	case "snapshot":
		return fwCLISnapshot()
	case "plan":
		return fwCLIPlan(args, true)
	case "apply":
		return fwCLIPlan(args, false)
	case "teardown":
		return fwCLITeardown(args)
	}
	return 2, fwCLIError("usage: gre-panel firewall [detect | snapshot | plan | apply | teardown] — plan is dry-run; apply needs --yes")
}

func fwCLIPorts(args []string) (int, any) {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list":
		return fwCLIPortsList()
	case "check":
		return fwCLIPortsCheck(args)
	}
	return 2, fwCLIError("usage: gre-panel ports [list | check PORT[/tcp|/udp]]")
}

func fwCLIError(msg string) map[string]any { return map[string]any{"error": msg} }

// ---- detect / snapshot ----

func fwCLIDetect() (int, any) {
	backend, detail := fwDetect()
	return 0, map[string]any{"backend": backend, "detail": detail}
}

func fwCLISnapshot() (int, any) {
	snap, err := fwCaptureSnapshot()
	if err != nil {
		return 1, fwCLIError(err.Error())
	}
	return 0, snap
}

// ---- plan / apply ----

// fwSpecFlags collects repeatable --add/--remove values ("443/tcp").
type fwSpecFlags []fwPortSpec

func (f *fwSpecFlags) String() string {
	var parts []string
	for _, s := range *f {
		parts = append(parts, fmt.Sprintf("%d/%s", s.Port, s.Proto))
	}
	return strings.Join(parts, ",")
}

func (f *fwSpecFlags) Set(v string) error {
	idx := strings.LastIndex(v, "/")
	if idx <= 0 {
		return fmt.Errorf("use PORT/tcp or PORT/udp, got %q", v)
	}
	port, err := strconv.Atoi(v[:idx])
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid port in %q", v)
	}
	proto := strings.ToLower(v[idx+1:])
	if proto != "tcp" && proto != "udp" {
		return fmt.Errorf("proto must be tcp or udp, got %q", v[idx+1:])
	}
	*f = append(*f, fwPortSpec{Port: port, Proto: proto})
	return nil
}

// fwPlanFlags is the shared flag set of `firewall plan` and `firewall apply`.
type fwPlanFlags struct {
	owner    string
	family   string
	note     string
	adds     fwSpecFlags
	removes  fwSpecFlags
	yes      bool
	planFile string
}

func fwPlanFlagSet(fs *flag.FlagSet, pf *fwPlanFlags) {
	fs.StringVar(&pf.owner, "owner", "", "rule owner tag, e.g. tunnel:at_1a2b3c4d (required)")
	fs.StringVar(&pf.family, "family", "both", "address family: ipv4, ipv6 or both")
	fs.StringVar(&pf.note, "note", "", "human description stored in the audit trail")
	fs.Var(&pf.adds, "add", "allow rule to add, PORT/tcp or PORT/udp (repeatable)")
	fs.Var(&pf.removes, "remove", "allow rule to remove, PORT/tcp or PORT/udp (repeatable)")
	fs.BoolVar(&pf.yes, "yes", false, "confirm a destructive apply/teardown (plan never needs it)")
	fs.StringVar(&pf.planFile, "plan-file", "", "apply the exact JSON plan produced by `firewall plan`")
}

// fwCLIBuildOps turns the flags into validated ops (adds first, then removes).
func fwCLIBuildOps(pf *fwPlanFlags) ([]fwOp, error) {
	var ops []fwOp
	if pf.owner == "" && (len(pf.adds) > 0 || len(pf.removes) > 0) {
		return nil, fmt.Errorf("--owner is required for add/remove ops")
	}
	for _, s := range pf.adds {
		ops = append(ops, fwOp{Kind: "add", Rule: fwPortRule{
			Port: s.Port, Proto: s.Proto, Family: pf.family, Owner: pf.owner, Note: pf.note}})
	}
	for _, s := range pf.removes {
		ops = append(ops, fwOp{Kind: "remove", Rule: fwPortRule{
			Port: s.Port, Proto: s.Proto, Family: pf.family, Owner: pf.owner, Note: pf.note}})
	}
	return ops, nil
}

// fwCLIPlan implements BOTH `firewall plan` (dry is true) and
// `firewall apply` (dry is false, --yes required).
func fwCLIPlan(args []string, dry bool) (int, any) {
	fs := flag.NewFlagSet("firewall", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var pf fwPlanFlags
	fwPlanFlagSet(fs, &pf)
	if err := fs.Parse(args); err != nil {
		return 2, fwCLIError(err.Error())
	}

	var ops []fwOp
	if pf.planFile != "" {
		if dry {
			return 2, fwCLIError("--plan-file belongs to apply; plan is already dry-run")
		}
		data, err := os.ReadFile(pf.planFile)
		if err != nil {
			return 1, fwCLIError(err.Error())
		}
		var parsed struct {
			Ops  []fwOp  `json:"ops"`
			Plan *fwPlan `json:"plan"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return 1, fwCLIError("unreadable plan file: " + err.Error())
		}
		switch {
		case parsed.Plan != nil:
			ops = parsed.Plan.Ops
		case parsed.Ops != nil:
			ops = parsed.Ops
		default:
			return 1, fwCLIError("plan file carries no ops")
		}
	} else {
		var err error
		if ops, err = fwCLIBuildOps(&pf); err != nil {
			return 2, fwCLIError(err.Error())
		}
	}
	if len(ops) == 0 {
		return 2, fwCLIError("nothing to do: give --add/--remove (or --plan-file)")
	}

	// Re-validate through the engine: the CLI can never bypass the same
	// rules the API and the agent handlers go through.
	plan, err := fwNewPlan(ops)
	if err != nil {
		return 2, fwCLIError(err.Error())
	}
	plan.DryRun = dry
	if !dry && !pf.yes {
		return 2, fwCLIError("apply changes the live firewall; re-run with --yes (or without it to keep the dry-run preview)")
	}
	res, err := fwApply(plan)
	if err != nil {
		return 1, res
	}
	return 0, res
}

// ---- teardown ----

func fwCLITeardown(args []string) (int, any) {
	fs := flag.NewFlagSet("teardown", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var pf fwPlanFlags
	fwPlanFlagSet(fs, &pf)
	if err := fs.Parse(args); err != nil {
		return 2, fwCLIError(err.Error())
	}
	if pf.owner == "" {
		return 2, fwCLIError("--owner is required (e.g. --owner tunnel:at_1a2b3c4d)")
	}
	if !pf.yes {
		return 2, fwCLIError("teardown removes the owner's ports and firewall rules; re-run with --yes")
	}
	kind, id, err := fwAgentSplitOwner(pf.owner)
	if err != nil {
		return 2, fwCLIError(err.Error())
	}
	// Firewall first (the registry drives the plan), release second: a
	// failed rule removal stays retriable because the registry still
	// records the ownership.
	res, err := fwTeardownOwner(pf.owner)
	if err != nil {
		return 1, map[string]any{"firewall": res, "error": err.Error()}
	}
	released, err := portReleaseOwner(kind, id)
	if err != nil {
		return 1, map[string]any{"firewall": res, "error": err.Error()}
	}
	return 0, map[string]any{"released_ports": released, "firewall": res}
}

// ---- ports ----

func fwCLIPortsList() (int, any) {
	backend, detail := fwDetect()
	var listeners []portListener
	if ls, err := portListeners(); err == nil {
		listeners = ls
	}
	return 0, map[string]any{
		"backend":   backend,
		"detail":    detail,
		"entries":   portEntries(),
		"listeners": listeners,
		"conflicts": portConflicts(),
	}
}

func fwCLIPortsCheck(args []string) (int, any) {
	spec := ""
	if len(args) > 0 {
		spec = strings.TrimSpace(args[0])
	}
	if spec == "" {
		return 2, fwCLIError("usage: gre-panel ports check PORT[/tcp|/udp]")
	}
	port, proto, err := fwCLIParsePortSpec(spec)
	if err != nil {
		return 2, fwCLIError(err.Error())
	}
	if proto == "" {
		return 0, portReport(port)
	}
	return 0, map[string]any{"port": port, proto: portAvailabilityCheck(port, proto)}
}

// fwCLIParsePortSpec parses "443", "443/tcp" or "443/udp".
func fwCLIParsePortSpec(v string) (int, string, error) {
	idx := strings.Index(v, "/")
	if idx < 0 {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return 0, "", fmt.Errorf("invalid port %q", v)
		}
		return port, "", nil
	}
	port, err := strconv.Atoi(v[:idx])
	if err != nil || port < 1 || port > 65535 {
		return 0, "", fmt.Errorf("invalid port in %q", v)
	}
	proto := strings.ToLower(v[idx+1:])
	if proto != "tcp" && proto != "udp" {
		return 0, "", fmt.Errorf("proto must be tcp or udp, got %q", v[idx+1:])
	}
	return port, proto, nil
}
