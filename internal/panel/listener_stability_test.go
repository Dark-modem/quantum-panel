// listener_stability_test.go — phase 6 regression tests for the panel
// listener fix (PANEL-LISTENER-NOTES.md): after the first successful
// bind the HTTP port, TLS port and base path are FROZEN — a busy port
// at a later start is a fatal error, never a silent rebind, because the
// rebind permanently changed the URL every enrolled agent uses.

package panel

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func listenerTestCfg(t *testing.T) {
	t.Helper()
	withTempConfig(t)
	cfg = panelConfig{Username: "op", Port: 18777, BasePath: "panel"}
	t.Cleanup(func() { cfg = panelConfig{} })
}

func writeSquatter(t *testing.T, port int) (net.Listener, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:"+itoaListener(port))
	if err != nil {
		t.Fatalf("squatter bind %d: %v", port, err)
	}
	return ln, func() { _ = ln.Close() }
}

func itoaListener(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func readPanelJSON(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(configDir, "panel.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestListenerFirstStartAutoSwitchPersists(t *testing.T) {
	listenerTestCfg(t)
	// Squat the preferred port: FIRST start must move once + save.
	_, done := writeSquatter(t, 18777)
	defer done()

	cfg.PortsFinalized = false
	ensureFreeHTTPPort()
	if cfg.Port == 18777 {
		t.Fatal("first start with a busy preferred port must auto-switch")
	}
	if !cfg.PortsFinalized {
		t.Fatal("ports must finalize after the first successful resolution")
	}
	m := readPanelJSON(t)
	if got, _ := m["port"].(float64); int(got) != cfg.Port {
		t.Fatalf("panel.json port = %v, want %d", m["port"], cfg.Port)
	}
	if m["ports_finalized"] != true {
		t.Fatal("ports_finalized must be persisted")
	}
}

func TestListenerLaterStartRefusesToMove(t *testing.T) {
	listenerTestCfg(t)
	// Simulate an ALREADY-finalized panel whose port is now squatted.
	cfg.PortsFinalized = true
	_, done := writeSquatter(t, 18777)
	defer done()

	// The fix turns the rebind into a fatal log; the process exits —
	// run the resolution in a child test binary? Too heavy. Instead call
	// the guard decision directly via the exported seam below.
	if listenerMoveAllowed() {
		t.Fatal("a finalized panel must NEVER be allowed to move its port")
	}
	// ensureFreeHTTPPort would log.Fatal here; assert the decision path
	// without dying by checking the guard used inside it.
}

func TestListenerFreePortKeepsEverythingStable(t *testing.T) {
	listenerTestCfg(t)
	cfg.PortsFinalized = true
	before := cfg.Port
	ensureFreeHTTPPort() // 18777 free (no squatter)
	if cfg.Port != before {
		t.Fatalf("free port must not change: %d → %d", before, cfg.Port)
	}
	if !cfg.PortsFinalized {
		t.Fatal("stays finalized")
	}
}

func TestListenerBasePathNeverAutoChanges(t *testing.T) {
	listenerTestCfg(t)
	cfg.BasePath = "secret-base"
	// The only writer of BasePath is the operator setup form; ensure the
	// port machinery does not touch it (it never marshals BasePath).
	cfg.PortsFinalized = false
	ensureFreeHTTPPort()
	if cfg.BasePath != "secret-base" {
		t.Fatalf("base path changed by port machinery: %q", cfg.BasePath)
	}
}

func TestEffectiveTLSPortEnvOverride(t *testing.T) {
	listenerTestCfg(t)
	t.Setenv("GRE_PANEL_TLS_PORT", "18443")
	if got := effectiveTLSPort(); got != 18443 {
		t.Fatalf("GRE_PANEL_TLS_PORT override = %d, want 18443", got)
	}
	t.Setenv("GRE_PANEL_TLS_PORT", "bogus")
	if got := effectiveTLSPort(); got != 7443 {
		t.Fatalf("invalid override must fall back to 7443, got %d", got)
	}
}

func TestPanelJSONPortsFinalizedRoundTrip(t *testing.T) {
	listenerTestCfg(t)
	cfg.PortsFinalized = true
	saveCfg()
	m := readPanelJSON(t)
	if m["ports_finalized"] != true {
		t.Fatalf("ports_finalized not persisted: %v", m)
	}
	// And loading it back yields a frozen panel.
	cfg2 := panelConfig{}
	raw, _ := os.ReadFile(filepath.Join(configDir, "panel.json"))
	_ = json.Unmarshal(raw, &cfg2)
	if !cfg2.PortsFinalized {
		t.Fatal("loaded config lost PortsFinalized")
	}
	if !strings.Contains(string(raw), "ports_finalized") {
		t.Fatal("marker missing from file")
	}
}
