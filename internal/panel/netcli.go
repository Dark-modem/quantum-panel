package panel

// netcli.go — the `gre-panel netopt` command line, agent-aware. Works on a
// full controller (panel.json) AND on a lightweight Agent-only endpoint
// (agent.json), never requiring — and never creating — a fake panel.json.
// All operations go through the structured layer in netops_api.go whose
// results have stable JSON shapes for the Agent protocol (Session 3 owns
// routes/protocol transport; this file is CLI only).
//
//	gre-panel netopt detect [--json]
//	gre-panel netopt plan [--role R] [--profile P] [--justify TEXT] [--json]
//	gre-panel netopt apply [--profile P] [--role R] [--justify TEXT] [--json]
//	gre-panel netopt status [--json]
//	gre-panel netopt rollback [--json]
//	gre-panel netopt revert [--json]
//	gre-panel netopt apply-nic              boot hook (owned unit)
//
// Privileges: apply/rollback/revert/apply-nic require root; detect/plan/
// status are read-only and work on an un-enrolled box too (installer
// pre-flight). This command never touches SSH, firewall or port rules and
// never changes MTU (tunnel subsystem owns it).

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
)

// netCliStart initializes the CLI without the full-panel requirement:
// GRE_PANEL_DIR overrides configDir; the installation role comes from the
// files on disk. Unlike cliStart it NEVER calls loadOrInit in agent mode
// (loadOrInit would create a default panel.json — the fake file Session 6's
// QA flagged). Read-only ops work on an uninstalled box; mutating ops are
// refused later by netRequireWritableInstall.
func netCliStart() bool {
	if v := os.Getenv("GRE_PANEL_DIR"); v != "" {
		configDir = v
	}
	cliMode = true
	log.SetOutput(io.Discard)
	switch netInstallRoleOf() {
	case netInstallController:
		// Full panel: load the real config so role/profile resolution sees
		// the same state the service sees.
		loadOrInit()
	case netInstallAgent, netInstallNone:
		// Agent-only (or not yet installed): no panel config is read or
		// created. netopt needs nothing from panel.json.
	}
	return true
}

func cliNetOpt(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, strings.TrimSpace(cliNetOptUsage))
		return 2
	}
	sub, rest := args[0], args[1:]
	netCliStart()

	asJSON := false
	for _, a := range rest {
		if a == "--json" {
			asJSON = true
		}
	}

	switch sub {
	case "apply-nic":
		// Boot hook from the owned unit.
		if !netPrivileged() {
			fmt.Fprintln(os.Stderr, "netopt apply-nic: requires root")
			return 1
		}
		if err := netApplyNicBoot(); err != nil {
			fmt.Fprintln(os.Stderr, "netopt apply-nic:", err)
			return 1
		}
		return 0
	case "detect":
		res, err := NetopDetect()
		return netCliOut(res, err, asJSON)
	case "plan":
		f := netCliFlagsParse(rest)
		res, err := NetopPlan(f.role, forcedProfileOf(f))
		return netCliPlanOut(res, err, asJSON)
	case "apply":
		f := netCliFlagsParse(rest)
		res, err := NetopApply(f.role, forcedProfileOf(f), f.justify, nil)
		return netCliApplyOut("apply", res, err, asJSON)
	case "status":
		res, err := NetopStatus()
		return netCliStatusOut(res, err, asJSON)
	case "rollback":
		res, err := NetopRollback()
		return netCliApplyOut("rollback", res, err, asJSON)
	case "revert":
		res, err := NetopRevert()
		return netCliApplyOut("revert", res, err, asJSON)
	default:
		fmt.Fprintln(os.Stderr, strings.TrimSpace(cliNetOptUsage))
		return 2
	}
}

const cliNetOptUsage = `
Usage: gre-panel netopt <command> [flags]
  detect [--json]                measure hardware/network (works agent-only)
  plan [--role R] [--profile P] [--justify TEXT] [--json]
  apply [--profile P] [--role R] [--justify TEXT] [--json]   (root)
  status [--json]
  rollback [--json]              (root)
  revert [--json]                (root)
  apply-nic                      boot hook: re-apply NIC actions (root)
Roles: R = controller | agent | auto (auto from panel.json / agent.json).
JSON output shapes are the stable netopt.* operation contracts
(docs/NETOPT-AGENT-MODE.md) — suitable for Agent protocol transport.
`

// netCliFlags is the small flag set plan/apply share.
type netCliFlags struct {
	role    netRole
	profile string
	justify string
}

func netCliFlagsParse(args []string) netCliFlags {
	f := netCliFlags{role: netRoleAuto}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--role":
			if i+1 < len(args) {
				i++
				f.role = netRole(args[i])
			}
		case "--profile":
			if i+1 < len(args) {
				i++
				f.profile = args[i]
			}
		case "--justify":
			if i+1 < len(args) {
				i++
				f.justify = strings.TrimSpace(args[i])
			}
		}
	}
	return f
}

func forcedProfileOf(f netCliFlags) string {
	if f.profile == "auto" {
		return ""
	}
	return f.profile
}

// netCliOut prints a result or an error; --json prints the stable contract.
func netCliOut(res any, err error, asJSON bool) int {
	if asJSON {
		fmt.Println(netopJSON(res))
		if err != nil {
			return 1
		}
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "netopt:", err)
		return 1
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(b))
	return 0
}

func netCliPlanOut(res *NetopPlanResult, err error, asJSON bool) int {
	if asJSON {
		fmt.Println(netopJSON(res))
		if err != nil {
			return 1
		}
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "netopt plan:", err)
		return 1
	}
	fmt.Println("detected:", netHardwareSummary(res.Registry))
	fmt.Println("profile:", res.Profile, res.Summary, "role:", res.Role)
	for _, r := range res.Reasons {
		fmt.Println("  reason:", r)
	}
	for _, c := range res.Changes {
		state := "APPLY "
		if !c.Apply {
			state = "skip  "
		}
		fmt.Printf("  %s %-42s %-18q -> %-18q %s\n", state, c.Key, c.Current, c.Proposed, c.SkipReason)
		if c.SkipReason == "" {
			fmt.Printf("          why: %s | risk: %s\n", c.Reason, c.Risk)
		}
	}
	return 0
}

func netCliApplyOut(op string, res *NetopApplyResult, err error, asJSON bool) int {
	if asJSON {
		fmt.Println(netopJSON(res))
		if err != nil {
			return 1
		}
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "netopt "+op+":", err)
		if res != nil && res.Journal != nil {
			fmt.Fprintf(os.Stderr, "journal: %d settings, rolled_back=%v, verified=%v\n",
				len(res.Journal.Applied), res.Journal.RolledBack, res.Journal.Verified)
		}
		return 1
	}
	fmt.Println(res.Message)
	for _, s := range res.Journal.Applied {
		fmt.Printf("  %-42s %q -> %q\n", s.Key, s.Before, s.After)
	}
	return 0
}

func netCliStatusOut(res *NetopStatusResult, err error, asJSON bool) int {
	if asJSON {
		fmt.Println(netopJSON(res))
		if err != nil {
			return 1
		}
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "netopt status:", err)
		return 1
	}
	if !res.HasJournal {
		fmt.Println("no netopt journal: the subsystem has not applied anything here")
		return 0
	}
	b, _ := json.MarshalIndent(res.Journal, "", "  ")
	fmt.Println(string(b))
	if len(res.Drift) == 0 {
		fmt.Println("live drift: none")
		return 0
	}
	fmt.Println("live drift:")
	for _, d := range res.Drift {
		fmt.Printf("  %-42s live=%q wanted=%q DRIFTED\n", d.Key, d.Live, d.Wanted)
	}
	return 0
}
