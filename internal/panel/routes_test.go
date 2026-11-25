package panel

// REFACTOR-S5 route-surface golden guard (REFACTOR-PLAN-PANEL.md §6.5).
//
// PURPOSE
//   The panel's HTTP surface is the product's security boundary: every route
//   must carry the auth/CSRF wrapper its family promises, and no handler may
//   silently strand without a route (the §4 dead-handler class that S1 had to
//   remove by hand: serveWizardPage/serveMeshPage). This file freezes the
//   surface so future stages cannot regress it unnoticed.
//
//   It asserts three things against the live registration code:
//     (a) EVERY handler-shaped package function is referenced by a mux
//         registration (dead-handler detector), modulo a documented
//         exceptions list;
//     (b) every registration's auth/CSRF/requireLegacy wrapper chain matches
//         a small golden manifest (route → wrapper class) — a new route
//         added without its family's wrapper FAILS here;
//     (c) the total registration count equals a documented constant that
//         points at the contract docs (00-ADMIN/UI-API-CONTRACT.md owns the
//         /api/ui families; the legacy/mesh/peer families are owned by the
//         registration sites themselves).
//
// APPROACH (least invasive, documented per the dispatch)
//   Go's http.ServeMux has no introspection API, and probing a built mux with
//   synthetic requests would execute real handlers against live state (not
//   hermetic, not deterministic). The registration code, however, is fully
//   static: buildPanelMux (main.go) plus eleven per-feature registerers and
//   three mount helpers. So this test enumerates the registration tables the
//   way a reviewer would: it parses THIS package's own non-test sources with
//   go/parser and extracts every mux.HandleFunc call — resolving the local
//   pattern variables (p := base + "/api/mesh" style), expanding peer.go's
//   route-table loop, and classifying each handler argument's wrapper chain.
//   Zero production changes; reads only local files; fully deterministic.
//
//   Out of scope (documented exceptions): the two INTERNAL protocol muxes,
//   pathtest.go hubMux() (/hpt/*) and wss_carrier.go (/tunnel-stream). They
//   are token-authed point-to-point servers on their own listeners, not part
//   of the panel HTTP surface; their handlers are anonymous closures with no
//   handleX/serveX symbols.
//
//   Wrapper classes use the package's own middleware names, outer→inner,
//   ">"-joined (e.g. requireAuth>requireCSRF). "bare" = no wrapper (public by
//   design: login, static assets, the agent WS/artifact endpoints that
//   authenticate inside the handler, the catch-all entry).

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

// ---- golden manifest ------------------------------------------------------

// expectedRouteRegistrationSites is the total number of runtime
// registrations on the main panel surface (assets, API families, peer
// table ×2 prefixes, cluster-mesh prefix, debug mount, catch-alls).
// Contract docs: 00-ADMIN/UI-API-CONTRACT.md (the §2.x /api/ui surface);
// the registration site of record is buildPanelMux in main.go. Update the
// constant ONLY together with the manifest and the contract.
// Breakdown at freeze (2026-11-19, panel da8c669): 127 requireAuth>requireCSRF
// (mutations), 116 requireAuth (reads incl. the debug pprof mount), 47 asset
// (static files), 22 requireAuth>requireLegacy>requireCSRF (frozen legacy
// mutations), 14 requirePeerAuth ({base} peer table) + 8 requirePeerSecretHidden
// + 6 requireTunnelSecretHidden (/_hp peer table + rescue pairs), 6 bare
// (login/logout, agent WS, metrics, index, root entry), 5 limitUnauth (health,
// enroll, agent artifact, engine manifest), 2 requireRescuePeerAuth +
// 2 requireRescuePeerHidden (peer rescue), 1 requireLegacy (matrix register —
// token-authed legacy surface), 1 notfound catch-all (/_hp/), 2 meshHandler
// (/_hc hello/beat). Contract docs: 00-ADMIN/UI-API-CONTRACT.md (§2.x /api/ui
// surface) and the registerer file headers for the legacy/mesh/peer families.
// BACKEND-38 (2026-11-21, panel §2.22): +1 requireAuth read —
// GET /api/cores/available (cores discovery). Breakdown now 117 requireAuth.
// Pair-benchmark wizard (2026-11-21, panel 82c9969): +1 requireAuth read
// (GET /api/benchmark/pair) and +3 requireAuth>requireCSRF mutations
// (pair-run / pair-stop / pair-clear) — manifested here after the freeze.
// BACKEND-41 (2026-11-21, panel §2.21.2): +1 requireAuth>requireCSRF
// mutation — POST /api/mesh/topologies/dc (DC-centric wizard create).
// Breakdown now 128 requireAuth>requireCSRF.
// BACKEND-42 (2026-11-22, panel §2.23): +2 requireAuth reads —
// GET /api/benchmark/method-board (method-probe board) and
// GET /api/optimization/kernel (kernel-tuning read surface).
// Breakdown now 120 requireAuth.
const expectedRouteRegistrationSites = 371

// unroutedHandlerExceptions lists handler-shaped package functions that are
// intentionally NOT mux-registered, each with the reason. Everything else
// with the http handler shape must appear in some registration (check a).
var unroutedHandlerExceptions = map[string]string{
	// Handler-shaped helpers invoked BY routed handlers (transitively
	// routed), never registered as routes themselves. No dead handler was
	// found at freeze time — all five have live non-test call sites.
	"uiOpen":           "shared /api/ui store-or-structured-error helper, invoked by the routed ui handlers",
	"meshStoreOr503":   "shared mesh-api store-or-503 helper, invoked by the routed mesh handlers",
	"termGuard":        "terminal session guard, invoked by the routed terminal handlers before every terminal action",
	"sshFwdActDisable": "sshfwd action sub-handler, invoked by the routed POST /api/sshfwd handler",
	"uiOverviewServe":  "S4 extraction body of handleUIOverview — the thin registered wrapper delegates to it",
}

// goldenRouteManifest is the frozen route → wrapper-class parity table,
// asserted in BOTH directions against the extracted registrations: any
// registration missing from it, and any manifest row missing from the code,
// fails the test. Ordered slice (sorted by pattern) so the freeze diffs
// cleanly; the parity test builds the set and rejects duplicate rows.
var goldenRouteManifest = []routeRow{
	{"/", "bare"},
	{"/_hp/", "notfound"},
	{"DELETE {base}/api/agent-tunnels/{id}", "requireAuth>requireLegacy>requireCSRF"},
	{"DELETE {base}/api/mesh/datacenters/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/mesh/engines/artifacts/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/mesh/forwards/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/mesh/pasarguard/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/mesh/servers/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/mesh/topologies/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/mesh/tunnels/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/tls", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/topologies/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/ui/agents/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/ui/datacenters/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/ui/servers/{id}", "requireAuth>requireCSRF"},
	{"DELETE {base}/api/ui/topologies/{id}", "requireAuth>requireCSRF"},
	{"GET /_hp/api/peer/rescue-offer", "requireRescuePeerHidden"},
	{"GET /_hp/api/peer/status", "requirePeerSecretHidden"},
	{"GET /_hp/api/peer/swap-request", "requireTunnelSecretHidden"},
	{"GET /_hp/api/peer/swap-status", "requirePeerSecretHidden"},
	{"GET /_hp/api/peer/wrap-request", "requireTunnelSecretHidden"},
	{"GET {base}/", "bare"},
	{"GET {base}/api/2fa", "requireAuth"},
	{"GET {base}/api/access", "requireAuth"},
	{"GET {base}/api/agent-tunnels", "requireAuth"},
	{"GET {base}/api/agent/artifact/{name}", "limitUnauth"},
	{"GET {base}/api/agent/engine-artifacts", "limitUnauth"},
	{"GET {base}/api/agent/engine-artifacts/file/{name}", "limitUnauth"},
	{"GET {base}/api/agent/ws", "bare"},
	{"GET {base}/api/agents", "requireAuth"},
	{"GET {base}/api/agents/tokens", "requireAuth"},
	{"GET {base}/api/agents/updates", "requireAuth"},
	{"GET {base}/api/agents/{id}", "requireAuth"},
	{"GET {base}/api/alerts", "requireAuth"},
	{"GET {base}/api/audit", "requireAuth"},
	{"GET {base}/api/bagher", "requireAuth"},
	{"GET {base}/api/benchmark", "requireAuth"},
	{"GET {base}/api/benchmark/pair", "requireAuth"},
	{"GET {base}/api/carrier", "requireAuth"},
	{"GET {base}/api/carrier/advice", "requireAuth"},
	{"GET {base}/api/cluster/rules", "requireAuth"},
	{"GET {base}/api/cluster/status", "requireAuth"},
	{"GET {base}/api/cores/available", "requireAuth"},
	{"GET {base}/api/dashboard", "requireAuth"},
	{"GET {base}/api/dial", "requireAuth"},
	{"GET {base}/api/doctor", "requireAuth"},
	{"GET {base}/api/errors", "requireAuth"},
	{"GET {base}/api/fleet", "requireAuth"},
	{"GET {base}/api/health", "limitUnauth"},
	{"GET {base}/api/history", "requireAuth"},
	{"GET {base}/api/limits", "requireAuth"},
	{"GET {base}/api/logs", "requireAuth"},
	{"GET {base}/api/matrix/instances", "requireAuth"},
	{"GET {base}/api/matrix/local-service", "requireAuth"},
	{"GET {base}/api/matrix/status", "requireAuth"},
	{"GET {base}/api/mesh/agents", "requireAuth"},
	{"GET {base}/api/mesh/alerts", "requireAuth"},
	{"GET {base}/api/mesh/coefficient", "requireAuth"},
	{"GET {base}/api/mesh/datacenters", "requireAuth"},
	{"GET {base}/api/mesh/engines", "requireAuth"},
	{"GET {base}/api/mesh/engines/artifacts", "requireAuth"},
	{"GET {base}/api/mesh/engines/keysnippet", "requireAuth"},
	{"GET {base}/api/mesh/engines/templates", "requireAuth"},
	{"GET {base}/api/mesh/forwards", "requireAuth"},
	{"GET {base}/api/mesh/meta", "requireAuth"},
	{"GET {base}/api/mesh/overhead", "requireAuth"},
	{"GET {base}/api/mesh/overview", "requireAuth"},
	{"GET {base}/api/mesh/pasarguard", "requireAuth"},
	{"GET {base}/api/mesh/pasarguard/crosscheck", "requireAuth"},
	{"GET {base}/api/mesh/pasarguard/dc/plan", "requireAuth"},
	{"GET {base}/api/mesh/pasarguard/nodes", "requireAuth"},
	{"GET {base}/api/mesh/pasarguard/status", "requireAuth"},
	{"GET {base}/api/mesh/profiles/pending", "requireAuth"},
	{"GET {base}/api/mesh/servers", "requireAuth"},
	{"GET {base}/api/mesh/topologies", "requireAuth"},
	{"GET {base}/api/mesh/topologies/{id}", "requireAuth"},
	{"GET {base}/api/mesh/topologies/{id}/weights", "requireAuth"},
	{"GET {base}/api/mesh/usage", "requireAuth"},
	{"GET {base}/api/mesh/weights/check", "requireAuth"},
	{"GET {base}/api/metrics-token", "requireAuth"},
	{"GET {base}/api/optimization/kernel", "requireAuth"},
	{"GET {base}/api/overnight/checker", "requireAuth"}, // BACKEND-44: overnight completeness join (contract §2.24)
	{"GET {base}/api/pathtest", "requireAuth"},
	{"GET {base}/api/peer/config", "requireAuth"},
	{"GET {base}/api/peer/rescue-offer", "requireRescuePeerAuth"},
	{"GET {base}/api/peer/status", "requirePeerAuth"},
	{"GET {base}/api/peer/swap-request", "requirePeerAuth"},
	{"GET {base}/api/peer/swap-status", "requirePeerAuth"},
	{"GET {base}/api/peer/wrap-request", "requirePeerAuth"},
	{"GET {base}/api/peers", "requireAuth"},
	{"GET {base}/api/perf", "requireAuth"},
	{"GET {base}/api/releases", "requireAuth"},
	{"GET {base}/api/rescue", "requireAuth"},
	{"GET {base}/api/revpath", "requireAuth"},
	{"GET {base}/api/revpath/fixes", "requireAuth"},
	{"GET {base}/api/selfstats", "requireAuth"},
	{"GET {base}/api/session-settings", "requireAuth"},
	{"GET {base}/api/setup", "requireAuth"},
	{"GET {base}/api/spoof", "requireAuth"},
	{"GET {base}/api/spoof/tester", "requireAuth"},
	{"GET {base}/api/sshfwd", "requireAuth"},
	{"GET {base}/api/status", "requireAuth"},
	{"GET {base}/api/suggest", "requireAuth"},
	{"GET {base}/api/term/allowlist", "requireAuth"},
	{"GET {base}/api/term/status", "requireAuth"},
	{"GET {base}/api/term/ws", "requireAuth"},
	{"GET {base}/api/tls", "requireAuth"},
	{"GET {base}/api/tls/ip/ca", "requireAuth"},
	{"GET {base}/api/tls/proxy-config", "requireAuth"},
	{"GET {base}/api/topologies", "requireAuth"},
	{"GET {base}/api/traffic/usage", "requireAuth"},
	{"GET {base}/api/tunnel-health", "requireAuth"},
	{"GET {base}/api/tunnel/swap", "requireAuth"},
	{"GET {base}/api/ui/agents", "requireAuth"},
	{"GET {base}/api/ui/agents/{id}", "requireAuth"},
	{"GET {base}/api/ui/agents/{id}/dependencies", "requireAuth"},
	{"GET {base}/api/ui/agents/{id}/engines", "requireAuth"},
	{"GET {base}/api/ui/agents/{id}/maintenance", "requireAuth"},
	{"GET {base}/api/ui/agents/{id}/topologies", "requireAuth"},
	{"GET {base}/api/ui/alerts", "requireAuth"},
	{"GET {base}/api/ui/audit", "requireAuth"},
	{"GET {base}/api/ui/datacenters/{id}/client-config", "requireAuth"},
	{"GET {base}/api/ui/engines", "requireAuth"},
	{"GET {base}/api/ui/engines/artifacts", "requireAuth"},
	{"GET {base}/api/ui/engines/keysnippet", "requireAuth"},
	{"GET {base}/api/ui/engines/templates", "requireAuth"},
	{"GET {base}/api/ui/mesh/overview", "requireAuth"},
	{"GET {base}/api/ui/overview", "requireAuth"},
	{"GET {base}/api/ui/pasarguard", "requireAuth"},
	{"GET {base}/api/ui/pasarguard/coefficient", "requireAuth"},
	{"GET {base}/api/ui/pasarguard/crosscheck", "requireAuth"},
	{"GET {base}/api/ui/pasarguard/overhead", "requireAuth"},
	{"GET {base}/api/ui/pasarguard/status", "requireAuth"},
	{"GET {base}/api/ui/profiles/pending", "requireAuth"},
	{"GET {base}/api/ui/reports/billing", "requireAuth"},
	{"GET {base}/api/ui/selfcheck", "requireAuth"},
	{"GET {base}/api/ui/servers/{id}/dependencies", "requireAuth"},
	{"GET {base}/api/ui/session", "requireAuth"},
	{"GET {base}/api/ui/topologies", "requireAuth"},
	{"GET {base}/api/ui/topologies/{id}", "requireAuth"},
	{"GET {base}/api/ui/topologies/{id}/dependencies", "requireAuth"},
	{"GET {base}/api/ui/topologies/{id}/ports", "requireAuth"},
	{"GET {base}/api/ui/traffic-series", "requireAuth"},
	{"GET {base}/api/ui/tunnels/{key}", "requireAuth"},
	{"GET {base}/api/ui/tunnels/{key}/dependencies", "requireAuth"},
	{"GET {base}/api/update/available", "requireAuth"},
	{"GET {base}/api/update/channel", "requireAuth"},
	{"GET {base}/api/update/github-token", "requireAuth"},
	{"GET {base}/api/update/peer", "requireAuth"},
	{"GET {base}/api/version", "requireAuth"},
	{"GET {base}/api/watchdog", "requireAuth"},
	{"GET {base}/api/watchdog/backup-download", "requireAuth"},
	{"GET {base}/api/xray", "requireAuth"},
	{"GET {base}/app.js", "asset"},
	{"GET {base}/apple-touch-icon.png", "asset"},
	{"GET {base}/base.css", "asset"},
	{"GET {base}/debug/pprof/", "requireAuth"},
	{"GET {base}/enterprise.css", "asset"},
	{"GET {base}/favicon.ico", "asset"},
	{"GET {base}/favicon.png", "asset"},
	{"GET {base}/favicon.svg", "asset"},
	{"GET {base}/fonts.css", "asset"},
	{"GET {base}/fonts/vazirmatn.woff2", "asset"},
	{"GET {base}/i18n-fa.js", "asset"},
	{"GET {base}/img/engines/aestun.png", "asset"},
	{"GET {base}/img/engines/backhaul.png", "asset"},
	{"GET {base}/img/engines/backpack.png", "asset"},
	{"GET {base}/img/engines/chisel.png", "asset"},
	{"GET {base}/img/engines/dagger.png", "asset"},
	{"GET {base}/img/engines/easytier.png", "asset"},
	{"GET {base}/img/engines/frp.png", "asset"},
	{"GET {base}/img/engines/gost.png", "asset"},
	{"GET {base}/img/engines/hysteria.png", "asset"},
	{"GET {base}/img/engines/midos.png", "asset"},
	{"GET {base}/img/engines/narnia.png", "asset"},
	{"GET {base}/img/engines/paqet.png", "asset"},
	{"GET {base}/img/engines/pengo.png", "asset"},
	{"GET {base}/img/engines/rakhsh.png", "asset"},
	{"GET {base}/img/engines/rathole.png", "asset"},
	{"GET {base}/img/engines/singbox.png", "asset"},
	{"GET {base}/img/engines/singbox.svg", "asset"},
	{"GET {base}/img/engines/tinc.png", "asset"},
	{"GET {base}/img/engines/tinc.svg", "asset"},
	{"GET {base}/img/engines/udp2raw.png", "asset"},
	{"GET {base}/img/engines/waterwall.png", "asset"},
	{"GET {base}/img/engines/wattunnel.png", "asset"},
	{"GET {base}/img/engines/wireguard.png", "asset"},
	{"GET {base}/img/engines/wstunnel.png", "asset"},
	{"GET {base}/img/engines/xray.svg", "asset"},
	{"GET {base}/matrix_edit.js", "asset"},
	{"GET {base}/mesh.js", "asset"},
	{"GET {base}/meshtabs.js", "asset"},
	{"GET {base}/metrics", "bare"},
	{"GET {base}/quantum-mesh.css", "asset"},
	{"GET {base}/tokens.css", "asset"},
	{"GET {base}/topology.js", "asset"},
	{"GET {base}/tunnel_overview.js", "asset"},
	{"GET {base}/wizard.js", "asset"},
	{"GET {base}/xterm-fit.js", "asset"},
	{"GET {base}/xterm-search.js", "asset"},
	{"GET {base}/xterm.css", "asset"},
	{"GET {base}/xterm.js", "asset"},
	{"PATCH {base}/api/mesh/datacenters/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/mesh/forwards/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/mesh/pasarguard/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/mesh/servers/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/mesh/topologies/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/mesh/tunnels/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/peers", "requireAuth>requireLegacy>requireCSRF"},
	{"PATCH {base}/api/topologies/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/ui/datacenters/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/ui/servers/{id}", "requireAuth>requireCSRF"},
	{"PATCH {base}/api/ui/topologies/{id}", "requireAuth>requireCSRF"},
	{"POST /_hc/beat", "meshHandler(false)"},
	{"POST /_hc/hello", "meshHandler(true)"},
	{"POST /_hp/api/peer/apply-carrier", "requirePeerSecretHidden"},
	{"POST /_hp/api/peer/apply-engine", "requirePeerSecretHidden"},
	{"POST /_hp/api/peer/carrier-info", "requirePeerSecretHidden"},
	{"POST /_hp/api/peer/handshake", "requireTunnelSecretHidden"},
	{"POST /_hp/api/peer/ping", "requireTunnelSecretHidden"},
	{"POST /_hp/api/peer/rescue-ack", "requireRescuePeerHidden"},
	{"POST /_hp/api/peer/revpath-probe", "requirePeerSecretHidden"},
	{"POST /_hp/api/peer/sshfwd-install-key", "requirePeerSecretHidden"},
	{"POST /_hp/api/peer/swap-carrier", "requirePeerSecretHidden"},
	{"POST /_hp/api/peer/swap-request", "requireTunnelSecretHidden"},
	{"POST /_hp/api/peer/wrap-request", "requireTunnelSecretHidden"},
	{"POST {base}/api/2fa/disable", "requireAuth>requireCSRF"},
	{"POST {base}/api/2fa/enable", "requireAuth>requireCSRF"},
	{"POST {base}/api/2fa/setup", "requireAuth>requireCSRF"},
	{"POST {base}/api/access", "requireAuth>requireCSRF"},
	{"POST {base}/api/action", "requireAuth>requireCSRF"},
	{"POST {base}/api/agent-tunnels", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/agent-tunnels/{id}/power", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/agent-tunnels/{id}/restart", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/agent/enroll", "limitUnauth"},
	{"POST {base}/api/agents/tokens", "requireAuth>requireCSRF"},
	{"POST {base}/api/agents/tokens/{id}/revoke", "requireAuth>requireCSRF"},
	{"POST {base}/api/agents/update-outdated", "requireAuth>requireCSRF"},
	{"POST {base}/api/agents/{id}/command", "requireAuth>requireCSRF"},
	{"POST {base}/api/agents/{id}/desired", "requireAuth>requireCSRF"},
	{"POST {base}/api/agents/{id}/op", "requireAuth>requireCSRF"},
	{"POST {base}/api/agents/{id}/revoke", "requireAuth>requireCSRF"},
	{"POST {base}/api/agents/{id}/update", "requireAuth>requireCSRF"},
	{"POST {base}/api/alerts", "requireAuth>requireCSRF"},
	{"POST {base}/api/bagher", "requireAuth>requireCSRF"},
	{"POST {base}/api/benchmark/apply", "requireAuth>requireCSRF"},
	{"GET {base}/api/benchmark/method-board", "requireAuth"},
	{"POST {base}/api/benchmark/pair-clear", "requireAuth>requireCSRF"},
	{"POST {base}/api/benchmark/pair-run", "requireAuth>requireCSRF"},
	{"POST {base}/api/benchmark/pair-stop", "requireAuth>requireCSRF"},
	{"POST {base}/api/benchmark/autopilot", "requireAuth>requireCSRF"},
	{"POST {base}/api/benchmark/run", "requireAuth>requireCSRF"},
	{"POST {base}/api/carrier", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/cluster/dns", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/hubs/add", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/hubs/delete", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/join-code", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/join-hub", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/leave-hub", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/member", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/pool", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/pool/delete", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/pool/failback", "requireAuth>requireCSRF"},
	{"POST {base}/api/cluster/settings", "requireAuth>requireCSRF"},
	{"POST {base}/api/dial", "requireAuth>requireCSRF"},
	{"POST {base}/api/doctor", "requireAuth>requireCSRF"},
	{"POST {base}/api/limits", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/link-name", "requireAuth>requireCSRF"},
	{"POST {base}/api/login", "bare"},
	{"POST {base}/api/logout", "bare"},
	{"POST {base}/api/matrix/edit", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/matrix/foreign-remove", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/matrix/foreign-setup", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/matrix/local-service", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/matrix/register", "requireLegacy"},
	{"POST {base}/api/matrix/remove", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/matrix/restart", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/matrix/setup", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/mesh/agents/{id}/engine-catalog", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/agents/{id}/link", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/agents/{id}/profile/confirm", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/agents/{id}/profile/hardening", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/agents/{id}/profile/netopt", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/agents/{id}/profile/rollback", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/agents/{id}/public-ip", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/agents/{id}/push", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/alerts/settings", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/alerts/test", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/backup", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/datacenters", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/engines/artifacts", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/forwards", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/pasarguard", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/pasarguard/cert", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/pasarguard/connect", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/pasarguard/dc/apply", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/pasarguard/install-command", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/pasarguard/orphans", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/pasarguard/reconcile", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/servers", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/topologies/generate", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/topologies/dc", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/topologies/{id}/weights", "requireAuth>requireCSRF"},
	{"POST {base}/api/mesh/tunnels", "requireAuth>requireCSRF"},
	{"POST {base}/api/metrics-token", "requireAuth>requireCSRF"},
	{"POST {base}/api/password", "requireAuth>requireCSRF"},
	{"POST {base}/api/pathtest", "requireAuth>requireCSRF"},
	{"POST {base}/api/peer/apply-carrier", "requirePeerAuth"},
	{"POST {base}/api/peer/apply-engine", "requirePeerAuth"},
	{"POST {base}/api/peer/carrier-info", "requirePeerAuth"},
	{"POST {base}/api/peer/config", "requireAuth>requireCSRF"},
	{"POST {base}/api/peer/handshake", "requirePeerAuth"},
	{"POST {base}/api/peer/ping", "requirePeerAuth"},
	{"POST {base}/api/peer/rescue-ack", "requireRescuePeerAuth"},
	{"POST {base}/api/peer/revpath-probe", "requirePeerAuth"},
	{"POST {base}/api/peer/sshfwd-install-key", "requirePeerAuth"},
	{"POST {base}/api/peer/swap-carrier", "requirePeerAuth"},
	{"POST {base}/api/peer/swap-request", "requirePeerAuth"},
	{"POST {base}/api/peer/wrap-request", "requirePeerAuth"},
	{"POST {base}/api/peers", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/perf", "requireAuth>requireCSRF"},
	{"POST {base}/api/rescue", "requireAuth>requireCSRF"},
	{"POST {base}/api/revpath/fix", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/revpath/run", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/session-settings", "requireAuth>requireCSRF"},
	{"POST {base}/api/setup", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/spoof", "requireAuth>requireCSRF"},
	{"POST {base}/api/spoof/tester", "requireAuth>requireCSRF"},
	{"POST {base}/api/sshfwd", "requireAuth>requireCSRF"},
	{"POST {base}/api/term/allowlist", "requireAuth>requireCSRF"},
	{"POST {base}/api/term/enable", "requireAuth>requireCSRF"},
	{"POST {base}/api/term/kill", "requireAuth>requireCSRF"},
	{"POST {base}/api/term/ticket", "requireAuth>requireCSRF"},
	{"POST {base}/api/tls", "requireAuth>requireCSRF"},
	{"POST {base}/api/tls/check", "requireAuth>requireCSRF"},
	{"POST {base}/api/tls/custom", "requireAuth>requireCSRF"},
	{"POST {base}/api/tls/ip", "requireAuth>requireCSRF"},
	{"POST {base}/api/tls/renew", "requireAuth>requireCSRF"},
	{"POST {base}/api/topologies", "requireAuth>requireCSRF"},
	{"POST {base}/api/topologies/{id}/active", "requireAuth>requireCSRF"},
	{"POST {base}/api/tunnel-health/run", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/tunnel/swap", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/engines/{engine}/disable", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/engines/{engine}/enable", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/link", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/maintenance", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/profile/confirm", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/profile/hardening", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/profile/netopt", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/profile/rollback", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/public-ip", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/push", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/agents/{id}/rename", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/alerts/settings", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/alerts/test", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/backup", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/datacenters", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/engines/artifacts", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/pasarguard/connect", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/pasarguard/reconcile", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/servers", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/topologies/generate", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/topologies/{id}/tunnels/{key}/weight", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/topologies/{name}/members/add", "requireAuth>requireCSRF"},
	{"POST {base}/api/ui/topologies/{name}/members/remove", "requireAuth>requireCSRF"},
	{"POST {base}/api/update", "requireAuth>requireCSRF"},
	{"POST {base}/api/update/channel", "requireAuth>requireCSRF"},
	{"POST {base}/api/update/github-token", "requireAuth>requireCSRF"},
	{"POST {base}/api/watchdog", "requireAuth>requireLegacy>requireCSRF"},
	{"POST {base}/api/watchdog/backup-download", "requireAuth>requireCSRF"},
	{"POST {base}/api/xray", "requireAuth>requireLegacy>requireCSRF"},
}

type routeRow struct {
	pattern string
	class   string
}

// routeClassWrapperNames are the middleware factories recognized when
// classifying a registration's handler argument. Anything else in the
// argument tree contributes no class (a wrapped non-wrapper expression still
// gets the outer wrappers it has — e.g. mountDebug's requireAuth).
var routeClassWrapperNames = map[string]bool{
	"requireAuth":               true,
	"requireCSRF":               true,
	"requireLegacy":             true,
	"limitUnauth":               true,
	"requirePeerAuth":           true,
	"requirePeerSecretHidden":   true,
	"requireTunnelSecretHidden": true,
	"requireRescuePeerAuth":     true,
	"requireRescuePeerHidden":   true,
}

// ---- extraction -----------------------------------------------------------

type routeReg struct {
	pattern string
	class   string
	idents  map[string]bool
	file    string
}

// foldStringExpr statically folds a string expression (literals, paren,
// concatenation, the base parameter, the fixed route-prefix constants and
// already-resolved local variables). ok=false means "not statically
// known" — the guard treats that as a hard error so new registration
// shapes cannot slip past unclassified.
func foldStringExpr(e ast.Expr, locals map[string]string) (string, bool) {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind == token.STRING {
			return strings.Trim(v.Value, "`\""), true
		}
		return "", false
	case *ast.ParenExpr:
		return foldStringExpr(v.X, locals)
	case *ast.BinaryExpr:
		if v.Op != token.ADD {
			return "", false
		}
		a, ok1 := foldStringExpr(v.X, locals)
		b, ok2 := foldStringExpr(v.Y, locals)
		if !ok1 || !ok2 {
			return "", false
		}
		return a + b, true
	case *ast.Ident:
		switch v.Name {
		case "base":
			return "{base}", true
		case "peerRoutePrefix":
			return "/_hp", true
		case "meshRoutePrefix":
			return "/_hc", true
		}
		if s, ok := locals[v.Name]; ok {
			return s, true
		}
		return "", false
	}
	return "", false
}

// classOf returns the wrapper chain of a registration's handler argument:
// the outer→inner names of recognized middleware factories joined with ">".
// "" means no recognized wrapper found. The caller maps "" at top level to
// "bare" and drops it in nested positions.
func classOf(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.CallExpr:
		if id, ok := v.Fun.(*ast.Ident); ok {
			if id.Name == "serveAsset" {
				return "asset"
			}
			if id.Name == "meshHandler" && len(v.Args) == 1 {
				if lit, ok := v.Args[0].(*ast.Ident); ok {
					return "meshHandler(" + lit.Name + ")"
				}
				return "meshHandler(?)"
			}
			if routeClassWrapperNames[id.Name] && len(v.Args) == 1 {
				if inner := classOf(v.Args[0]); inner != "" {
					return id.Name + ">" + inner
				}
				return id.Name
			}
		}
		return ""
	case *ast.SelectorExpr:
		if x, ok := v.X.(*ast.Ident); ok && x.Name == "http" && v.Sel.Name == "NotFound" {
			return "notfound"
		}
		return ""
	}
	return ""
}

// collectIdents gathers every identifier in the subtree that names a
// package-level function (handlers, wrapper factories, asset factories).
func collectIdents(e ast.Node, into map[string]bool, pkgFuncs map[string]bool) {
	ast.Inspect(e, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if pkgFuncs[id.Name] {
			into[id.Name] = true
		}
		return true
	})
}

// handlerShaped reports whether a top-level FuncDecl has the http handler
// shape: no receiver and either exactly (http.ResponseWriter, *http.Request)
// parameters (any order) or a single http.HandlerFunc result.
func handlerShaped(fd *ast.FuncDecl) bool {
	if fd.Recv != nil {
		return false
	}
	hasW, hasR := false, false
	if fd.Type.Params != nil && len(fd.Type.Params.List) == 2 {
		for _, f := range fd.Type.Params.List {
			switch t := f.Type.(type) {
			case *ast.SelectorExpr:
				if t.Sel.Name == "ResponseWriter" {
					hasW = true
				}
			case *ast.StarExpr:
				if sel, ok := t.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "Request" {
					hasR = true
				}
			}
		}
	}
	if hasW && hasR {
		return true
	}
	if fd.Type.Results != nil && len(fd.Type.Results.List) == 1 {
		if sel, ok := fd.Type.Results.List[0].Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "HandlerFunc" {
			return true
		}
	}
	return false
}

// parsePanelSources parses every non-test .go file in the package directory
// (the test binary's working directory). Hermetic: local files only.
func parsePanelSources(t *testing.T) (*token.FileSet, map[string]*ast.File) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var names []string
	for _, en := range entries {
		name := en.Name()
		if en.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, name := range names {
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = f
	}
	return fset, files
}

// enclosingFuncName returns the declaration name containing pos.
func enclosingFuncName(f *ast.File, fset *token.FileSet, pos token.Pos) string {
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fd.Pos() <= pos && pos < fd.End() {
			return fd.Name.Name
		}
	}
	return ""
}

func exprDump(fset *token.FileSet, e ast.Expr) string {
	var sb strings.Builder
	if err := printer.Fprint(&sb, fset, e); err != nil {
		return "<dump failed>"
	}
	return sb.String()
}

// extractRouteSurface walks the parsed sources and returns every main-surface
// registration (pattern, wrapper class, referenced handler identifiers).
func extractRouteSurface(t *testing.T) []routeReg {
	t.Helper()
	fset, files := parsePanelSources(t)

	// All package-level function names (for ident collection) and all
	// handler-shaped declarations (for the dead-handler detector).
	pkgFuncs := map[string]bool{}
	for _, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				pkgFuncs[fd.Name.Name] = true
			}
		}
	}

	// Internal protocol muxes are out of scope (see file header).
	outOfScope := map[string]bool{"pathtest.go": true, "wss_carrier.go": true}

	var regs []routeReg
	for fname, f := range files {
		if outOfScope[fname] {
			continue
		}
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}

			// Resolve this function's local pattern variables
			// (p := base + "/api/mesh" and friends).
			locals := map[string]string{}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok || as.Tok != token.DEFINE || len(as.Lhs) != 1 || len(as.Rhs) != 1 {
					return true
				}
				lhs, ok := as.Lhs[0].(*ast.Ident)
				if !ok {
					return true
				}
				if s, ok := foldStringExpr(as.Rhs[0], locals); ok {
					locals[lhs.Name] = s
				}
				return true
			})

			if fd.Name.Name == "mountPeerRoutes" {
				regs = append(regs, extractPeerTable(t, fd, pkgFuncs)...)
			}

			// Direct HandleFunc sites; range-loop bodies are NOT descended
			// into here (the only loop-carried registrations in the package
			// are mountPeerRoutes' two sites, expanded from the table above
			// and counted in loopSites below — a new loop-carried site
			// changes the count and fails loudly).
			loopSites := 0
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if rs, ok := n.(*ast.RangeStmt); ok {
					ast.Inspect(rs.Body, func(m ast.Node) bool {
						if call, ok := m.(*ast.CallExpr); ok && isHandleFuncCall(call) {
							loopSites++
						}
						return true
					})
					return false // skip the loop body for direct extraction
				}
				call, ok := n.(*ast.CallExpr)
				if !ok || !isHandleFuncCall(call) {
					return true
				}
				pattern, ok := foldStringExpr(call.Args[0], locals)
				if !ok {
					t.Fatalf("%s/%s: registration pattern is not statically foldable (new registration shape? extend the S5 extractor): %s",
						fname, enclosingFuncName(f, fset, call.Pos()), exprDump(fset, call.Args[0]))
				}
				class := classOf(call.Args[1])
				if class == "" {
					class = "bare"
				}
				idents := map[string]bool{}
				collectIdents(call.Args[1], idents, pkgFuncs)
				regs = append(regs, routeReg{pattern: pattern, class: class, idents: idents, file: fname})
				return false // pruned: nested calls inside the args are not registrations
			})
			if fd.Name.Name == "mountPeerRoutes" {
				if loopSites != 2 {
					t.Fatalf("mountPeerRoutes expected exactly 2 loop-carried HandleFunc sites, found %d — update the S5 extractor", loopSites)
				}
			} else if loopSites != 0 {
				t.Fatalf("%s/%s: NEW loop-carried HandleFunc site (loop registrations are not enumerable generically) — extend the S5 extractor", fname, fd.Name.Name)
			}
		}
	}
	return regs
}

// isHandleFuncCall reports whether the call is a mux.HandleFunc(pattern,
// handler) registration with the two-argument shape every site uses.
func isHandleFuncCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "HandleFunc" && len(call.Args) == 2
}

// extractPeerTable expands mountPeerRoutes' route table: one row per table
// entry under {base} (requirePeerAuth) and under /_hp (the per-path guard
// chosen by the switch in the loop body). It fails loudly if the table's
// shape changes so the extractor gets updated with it.
func extractPeerTable(t *testing.T, fd *ast.FuncDecl, pkgFuncs map[string]bool) []routeReg {
	t.Helper()
	var rows [][3]string // method, path, handler name
	guardFor := map[string]string{}
	defaultGuard := ""
	var currentCase *ast.CaseClause

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		// The composite-literal route table. The row struct groups names
		// (method, path string; h http.HandlerFunc) so count NAMES, not
		// field declarations.
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Rhs) == 1 {
			if cl, ok := as.Rhs[0].(*ast.CompositeLit); ok {
				if arr, ok := cl.Type.(*ast.ArrayType); ok {
					if st, ok := arr.Elt.(*ast.StructType); ok && st.Fields != nil && st.Fields.List != nil {
						nfields := 0
						for _, fl := range st.Fields.List {
							nfields += len(fl.Names)
						}
						if nfields == 3 {
							for _, el := range cl.Elts {
								row, ok := el.(*ast.CompositeLit)
								if !ok || len(row.Elts) != 3 {
									t.Fatal("mountPeerRoutes route table changed shape — update the S5 extractor (expected {method, path, handler} rows)")
								}
								m, ok1 := row.Elts[0].(*ast.BasicLit)
								p, ok2 := row.Elts[1].(*ast.BasicLit)
								h, ok3 := row.Elts[2].(*ast.Ident)
								if !ok1 || !ok2 || !ok3 {
									t.Fatal("mountPeerRoutes route table row is not {BasicLit, BasicLit, Ident} — update the S5 extractor")
								}
								rows = append(rows, [3]string{strings.Trim(m.Value, "\""), strings.Trim(p.Value, "\""), h.Name})
							}
						}
					}
				}
			}
		}
		// The per-path guard: the loop inits `guard := requirePeerSecretHidden`
		// before a switch on rt.path whose case clauses override to
		// requireTunnelSecretHidden for token-known routes.
		if as, ok := n.(*ast.AssignStmt); ok && (as.Tok == token.DEFINE || as.Tok == token.ASSIGN) && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			if lhs, ok := as.Lhs[0].(*ast.Ident); ok && lhs.Name == "guard" {
				if id, ok := as.Rhs[0].(*ast.Ident); ok && id.Name != "guard" {
					if currentCase == nil || len(currentCase.List) == 0 {
						defaultGuard = id.Name
					} else {
						for _, c := range currentCase.List {
							if lit, ok := c.(*ast.BasicLit); ok {
								guardFor[strings.Trim(lit.Value, "\"")] = id.Name
							}
						}
					}
				}
			}
		}
		if cc, ok := n.(*ast.CaseClause); ok {
			currentCase = cc
		}
		return true
	})

	if len(rows) == 0 || defaultGuard == "" {
		t.Fatal("mountPeerRoutes route table or guard switch not found — update the S5 extractor")
	}
	var regs []routeReg
	for _, r := range rows {
		g := guardFor[r[1]]
		if g == "" {
			g = defaultGuard
		}
		regs = append(regs, routeReg{
			pattern: r[0] + " {base}" + r[1],
			class:   "requirePeerAuth",
			idents:  map[string]bool{r[2]: true, "requirePeerAuth": true},
		})
		regs = append(regs, routeReg{
			pattern: r[0] + " /_hp" + r[1],
			class:   g,
			idents:  map[string]bool{r[2]: true, g: true},
		})
	}
	return regs
}

// ---- surface views --------------------------------------------------------

// panelHandlerSymbols returns every handler-shaped top-level FuncDecl name
// declared in the package's non-test sources.
func panelHandlerSymbols(t *testing.T) map[string]string {
	t.Helper()
	_, files := parsePanelSources(t)
	out := map[string]string{} // name -> declaring file
	for fname, f := range files {
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && handlerShaped(fd) {
				if prev, dup := out[fd.Name.Name]; dup && prev != fname {
					t.Fatalf("handler symbol %q declared in two non-test files (%s, %s)", fd.Name.Name, prev, fname)
				}
				out[fd.Name.Name] = fname
			}
		}
	}
	return out
}

// ---- tests ----------------------------------------------------------------

// TestDumpRouteSurface is the manifest generator/auditor: it prints every
// extracted registration in a stable form. Keep it — it is how the frozen
// manifest gets regenerated and reviewed.
func TestDumpRouteSurface(t *testing.T) {
	regs := extractRouteSurface(t)
	sort.Slice(regs, func(i, j int) bool {
		if regs[i].pattern != regs[j].pattern {
			return regs[i].pattern < regs[j].pattern
		}
		return regs[i].class < regs[j].class
	})
	seen := map[string]bool{}
	for _, r := range regs {
		key := r.pattern + "|" + r.class
		if seen[key] {
			t.Errorf("duplicate registration %q (class %q) — ServeMux would panic at startup", r.pattern, r.class)
		}
		seen[key] = true
		t.Logf("%-72s %s", r.pattern, r.class)
	}
	t.Logf("TOTAL registrations: %d", len(regs))

	// Dead-handler report: handler-shaped symbols never referenced by any
	// registration argument.
	routed := map[string]bool{}
	for _, r := range regs {
		for id := range r.idents {
			routed[id] = true
		}
	}
	handlers := panelHandlerSymbols(t)
	var unrouted []string
	for name := range handlers {
		if !routed[name] {
			unrouted = append(unrouted, name)
		}
	}
	sort.Strings(unrouted)
	for _, name := range unrouted {
		t.Logf("UNROUTED handler: %s (declared in %s)", name, handlers[name])
	}
}

// TestRouteSurfaceGoldenParity is check (b): the frozen manifest against the
// live registrations, in BOTH directions. A new route without a manifest row
// fails; a manifest row whose registration disappears fails; a wrapper-chain
// change on an existing route fails. This is what keeps a future route added
// without its family's auth/CSRF wrapper out of the tree.
func TestRouteSurfaceGoldenParity(t *testing.T) {
	regs := extractRouteSurface(t)
	got := map[routeRow]string{}
	want := map[routeRow]bool{}
	for _, row := range goldenRouteManifest {
		if want[row] {
			t.Errorf("duplicate manifest row %q (class %q) — regenerate the manifest", row.pattern, row.class)
		}
		want[row] = true
	}
	for _, r := range regs {
		row := routeRow{r.pattern, r.class}
		if _, dup := got[row]; dup {
			t.Errorf("duplicate registration %q (class %q) — ServeMux would panic at startup", r.pattern, r.class)
		}
		got[row] = r.file
	}
	for _, r := range regs {
		row := routeRow{r.pattern, r.class}
		if !want[row] {
			t.Errorf("UNMANIFESTED registration %q (class %q, %s) — add it to goldenRouteManifest with the right wrapper class, or fix the wrapper chain", r.pattern, r.class, r.file)
		}
	}
	for row := range want {
		if _, ok := got[row]; !ok {
			t.Errorf("STALE manifest row %q (class %q) — registration gone from the code; remove the row or restore the route", row.pattern, row.class)
		}
	}
}

// TestRouteSurfaceCount is check (c): the total registration count against
// the documented constant.
func TestRouteSurfaceCount(t *testing.T) {
	regs := extractRouteSurface(t)
	if len(regs) != expectedRouteRegistrationSites {
		t.Fatalf("route surface changed: %d registrations, expected %d — if deliberate, regenerate the manifest (go test -run TestDumpRouteSurface -v), update the contract docs, and re-freeze the constant", len(regs), expectedRouteRegistrationSites)
	}
}

// TestEveryHandlerRouted is check (a), the dead-handler detector: every
// handler-shaped package function must appear in some registration argument,
// or be documented in unroutedHandlerExceptions with a reason.
func TestEveryHandlerRouted(t *testing.T) {
	regs := extractRouteSurface(t)
	routed := map[string]bool{}
	for _, r := range regs {
		for id := range r.idents {
			routed[id] = true
		}
	}
	handlers := panelHandlerSymbols(t)
	var missing []string
	for name := range handlers {
		if !routed[name] {
			if _, ok := unroutedHandlerExceptions[name]; !ok {
				missing = append(missing, name)
			}
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("UNROUTED handler %s (declared in %s) — register it, delete it, or document it in unroutedHandlerExceptions with a reason", name, handlers[name])
	}
	// Stale exceptions fail too: an exception for a symbol that no longer
	// exists (or is routed again) must be removed with the change that made
	// it stale.
	for name := range unroutedHandlerExceptions {
		if _, ok := handlers[name]; !ok {
			t.Errorf("stale unroutedHandlerExceptions entry %q — no such handler symbol anymore; remove the exception", name)
		}
	}
}
