package panel

import (
	"os/exec"
	"testing"
)

func TestDashboardServerRenderer(t *testing.T) {
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
