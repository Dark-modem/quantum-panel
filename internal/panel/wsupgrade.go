package panel

// wsupgrade.go — the ONE shared websocket.Upgrader (REFACTOR-S2, refactor
// plan §6.2). The three WS surfaces used to re-declare their own upgrader
// config; they now derive from this one per use and keep their
// per-connection read limits at the accept sites (agent channel
// agentWSReadLimit, terminal 64 KiB, carrier 65535) — those are per-use
// overrides, not upgrader fields, and S2 does not touch them.

import (
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

// wsUpgrader is the shared package-level WebSocket upgrader: 4 KiB
// buffers (gorilla's own defaults, spelled out so every derivation starts
// from the same base) and the common origin rule. CheckOrigin accepts
// non-browser clients (agents, carriers, terminals never send an Origin
// header) and rejects any browser origin whose cleaned host does not
// match the request host — the cross-site WebSocket hijacking guard every
// surface keeps.
var wsUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     wsOriginNonBrowser,
}

// wsOriginNonBrowser is the shared origin rule: empty Origin = non-browser
// client; a present Origin must clean-match the request host.
func wsOriginNonBrowser(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	return strings.EqualFold(CleanHost(origin), CleanHost(r.Host))
}
