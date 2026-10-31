package main

// netcli.go — the `gre-panel netopt` command line: measure, plan, apply,
// verify, roll back. See docs/NETOPT-INVENTORY.md for the profile inventory
// and docs/NETOPT-PERF.md for the measurement protocol.
//
//	gre-panel netopt detect                    print the hardware report (JSON)
//	gre-panel netopt plan [--role R] [--profile P] [--justify TEXT]
//	                                           select + print the change plan
//	gre-panel netopt apply  [--profile P] [--role R] [--justify TEXT]
//	                                           apply with baseline + rollback guard
//	gre-panel netopt status                    last journal vs live drift
//	gre-panel netopt rollback                  undo the last apply
//	gre-panel netopt revert                    remove owned files, restore baseline
//	gre-panel netopt apply-nic                 boot hook: re-apply NIC actions (unit)
//
// This command never touches SSH, firewall or port rules (Sessions 1/6 own
// those) and never rewrites MTU (the tunnel code owns it).

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func cliNetOpt(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, strings.TrimSpace(cliNetOptUsage))
		return 2
	}
	sub := args[0]
	rest := args[1:]
	if sub == "apply-nic" {
		// Boot hook from the owned unit: no cliStart dependency beyond config.
		if !cliStart() {
			return 1
		}
		if err := netApplyNicBoot(); err != nil {
			fmt.Fprintln(os.Stderr, "netopt apply-nic:", err)
			return 1
		}
		return 0
	}
	if !cliStart() {
		return 1
	}
	switch sub {
	case "detect":
		return netCliDetect()
	case "plan":
		return netCliPlan(rest)
	case "apply":
		return netCliApply(rest)
	case "status":
		return netCliStatus()
	case "rollback":
		msg, err := netRollback()
		if err != nil {
			fmt.Fprintln(os.Stderr, "netopt rollback:", err)
			return 1
		}
		fmt.Println(msg)
		return 0
	case "revert":
		msg, err := netRevert()
		if err != nil {
			fmt.Fprintln(os.Stderr, "netopt revert:", err)
			return 1
		}
		fmt.Println(msg)
		return 0
	default:
		fmt.Fprintln(os.Stderr, strings.TrimSpace(cliNetOptUsage))
		return 2
	}
}

const cliNetOptUsage = `
Usage: gre-panel netopt <command> [flags]
  detect                    measure hardware/network and print JSON
  plan [--role controller|agent|auto] [--profile conservative|balanced|performance] [--justify TEXT]
  apply [--profile P] [--role R] [--justify TEXT]
  status                    last journal vs live drift
  rollback                  undo the last apply (restores captured baseline)
  revert                    remove owned files and restore the baseline
  apply-nic                 boot hook: re-apply NIC actions (owned unit)
Flags: --profile, --role, --justify (required for --profile performance overrides)
`

// netCliFlags parses the small flag set the plan/apply commands share.
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

func netCliDetect() int {
	rep := netDetectHardware()
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		return 1
	}
	return 0
}

func netCliPlan(args []string) int {
	f := netCliFlagsParse(args)
	rep := netDetectHardware()
	forced := ""
	if f.profile != "" && f.profile != "auto" {
		forced = f.profile
	}
	profile, why := netSelectProfile(rep, f.role, forced)
	role := netRoleFor(f.role)
	changes := netPlanChanges(profile, rep, role)
	fmt.Println("detected:", netHardwareSummary(rep))
	fmt.Println("profile:", string(profile), netProfileSummary(profile, changes))
	for _, r := range why {
		fmt.Println("  reason:", r)
	}
	for _, c := range changes {
		state := "APPLY "
		if !c.Apply {
			state = "skip  "
		}
		fmt.Printf("  %s %-42s %-18q -> %-18q %s\n", state, c.Key, c.Current, c.Proposed, c.SkipReason)
		if c.SkipReason == "" {
			fmt.Printf("          why: %s | risk: %s\n", c.Reason, c.Risk)
		}
	}
	if forced == string(netProfilePerformance) {
		fmt.Println("note: performance profile is operator-justified; record the reason with --justify")
	}
	return 0
}

func netCliApply(args []string) int {
	f := netCliFlagsParse(args)
	if f.profile == string(netProfilePerformance) && f.justify == "" {
		fmt.Fprintln(os.Stderr, "netopt apply: --profile performance requires --justify (recorded in the journal)")
		return 2
	}
	profile := netProfileBalanced
	forced := ""
	if f.profile != "" && f.profile != "auto" {
		forced = f.profile
		profile = netProfileName(f.profile)
	}
	rep := netDetectHardware()
	_, why := netSelectProfile(rep, f.role, forced)
	if f.justify != "" {
		why = append(why, "operator justification: "+f.justify)
	}
	j, err := netApply(profile, f.role, why)
	if err != nil {
		fmt.Fprintln(os.Stderr, "netopt apply:", err)
		if j != nil {
			fmt.Fprintf(os.Stderr, "journal: %d settings, rolled_back=%v, verified=%v\n", len(j.Applied), j.RolledBack, j.Verified)
		}
		return 1
	}
	fmt.Printf("applied profile=%s role=%s settings=%d owned_files=%d\n",
		j.Profile, j.Role, len(j.Applied), len(j.OwnedFiles))
	for _, s := range j.Applied {
		fmt.Printf("  %-42s %q -> %q\n", s.Key, s.Before, s.After)
	}
	return 0
}

func netCliStatus() int {
	j := netLoadJournal()
	if j == nil {
		fmt.Println("no netopt journal: the subsystem has not applied anything here")
		return 0
	}
	out, _ := json.MarshalIndent(j, "", "  ")
	fmt.Println(string(out))
	fmt.Println("live drift:")
	rep := netDetectHardware()
	for _, s := range j.Applied {
		if s.Kind != "sysctl" {
			continue
		}
		live := rep.Sysctls[s.Key]
		mark := "ok"
		if live != s.After {
			mark = "DRIFTED"
		}
		fmt.Printf("  %-42s live=%q journal_after=%q %s\n", s.Key, live, s.After, mark)
	}
	return 0
}
