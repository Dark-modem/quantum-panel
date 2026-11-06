// legacygate.go — TASK 3 M3 (SECURITY-AUDIT-PANEL): the legacy
// host-operator surface (matrix/tunnel/peers/watchdog/carrier/xray/
// limits/revpath/tunnel-health/swap/bagher/setup/terminal commands that
// exec on THIS host) is DISABLED BY DEFAULT. The panel now primarily
// manages REMOTE agents over the mesh API; a panel facing the internet
// must not carry an always-on host-command web surface.
//
// Enable deliberately: panel.json "legacy_enabled": true, or env
// GRE_PANEL_LEGACY=1 (containers/units). Disabled mutations answer 403
// E-LEGACY-01 with the fix in the message; reads stay available (the UI
// hides the legacy pages via /api/status legacy_enabled=false).

package panel

import (
	"net/http"
	"os"
	"strings"
)

// legacyOpsEnabled reports whether the legacy host-command surface is on.
func legacyOpsEnabled() bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("GRE_PANEL_LEGACY")), "1") {
		return true
	}
	return cfg.LegacyEnabled
}

// requireLegacy gates legacy host-command MUTATIONS. Reads are not gated
// (they expose no host actions); the UI hides the pages anyway.
func requireLegacy(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !legacyOpsEnabled() {
			writeAPIError(w, r, "E-LEGACY-01",
				"legacy host-operator commands are disabled by default — set legacy_enabled=true in panel.json (or GRE_PANEL_LEGACY=1) to use them")
			return
		}
		next(w, r)
	}
}
