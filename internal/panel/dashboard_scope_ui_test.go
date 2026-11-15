package panel

import (
	"os"
	"os/exec"
	"testing"
)

func TestDashboardServerRenderer(t *testing.T) {
	// BACKEND-24: these VM tests exercise the UI session's rendering code
	// (app.js) through a cjs harness that must be kept CURRENT with app.js.
	// They need node AND a maintained harness, so they are env-gated like
	// the load lab: QUANTUM_UI_RENDERER=1 (the UI session runs them when
	// refreshing app.js; the standard gates skip them).
	if os.Getenv("QUANTUM_UI_RENDERER") != "1" {
		t.Skip("UI-renderer VM tests are env-gated: set QUANTUM_UI_RENDERER=1 (needs node + a harness current with app.js)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node unavailable")
	}
	for _, args := range [][]string{{"dashboard_server_ui.test.cjs"}, {"--test", "tunnel_overview.test.cjs"}} {
		out, err := exec.Command(node, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("renderer test failed: %v\n%s", err, out)
		}
	}
}
