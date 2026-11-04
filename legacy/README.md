# legacy/ — quarantined host-operator code

Per the Phase-0 rule: dead/legacy host-operator code is **quarantined,
never deleted**. This directory and the `//go:build legacy` Go files
hold the panel-host operator machinery that predates the
controller/agent split. The panel is management-only; it must not
operate host network state directly. New work must not depend on
anything in here.

## What lives where

| Artifact | Location | Status |
|---|---|---|
| `grepanel` bash control script (status/logs/restart/url/password/uninstall of the old panel host) | `legacy/grepanel` (this dir) | Moved out of the tree root; not referenced by any Go code. Was installed by the old `quantum.sh` monolith installer. |
| Legacy "Modified Backhaul" unit migration (`migrateLegacyBackhaul` + helpers + tests) | `internal/panel/backhaul_legacy.go` + `_test.go`, `//go:build legacy` | Compiled **only** with `-tags legacy`. Default builds get a no-op stub (`internal/panel/legacy_disabled.go`). |

## Reachable legacy — scheduled, not yet movable (Phase 1+)

The following is legacy in *spirit* (panel-host operator paths that the
agent architecture replaces) but still has live callers inside
`internal/panel`, so it cannot be tagged out without functional changes:

- `sshfwd.go` — FRP/GRE/Bagher bundle builders + `/api/sshfwd`,
  `/api/bagher` routes; called from topology, dashboard, peer and
  carrier_traffic code. Contains the hardcoded frp token
  `quantum-bagher-sec` and dashboard password `bagher-dash-local`
  (documented security finding; externalize before Phase 1).
- `bagher_links.go` — Bagher systemd unit helpers; used by topology,
  carrier.go, xray*.go, main.go startup migrations.
- `bagher_carrier.go` — only live entry is the `bagher-carrier` CLI
  subcommand (main.go); everything else is referenced only by the two
  files above and its tests.

A Phase-1 task should move the Bagher/FRP host-operator surface behind
the agent desired-state API and then extend the `legacy` build tag to
cover these files. Evidence for each live reference was measured during
Phase 0 and is recorded in `00-ADMIN/PHASE0-REPORT.md`.

## Building the legacy variant

```
go build -tags legacy ./...
go test  -tags legacy ./...
```
