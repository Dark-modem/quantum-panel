//go:build legacy

// LEGACY host-operator code — excluded from default builds.
//
// One-shot migration for units installed by the old separate
// "Modified Backhaul" script (backhaul-iranN / backhaul-kharejN systemd
// units on the panel host). This is panel-host operator machinery from
// before the controller/agent split: in the current architecture the
// panel must not operate host network state directly. Kept per the
// architect's rule (do not delete); build with `-tags legacy` to
// compile and run it (main.go still calls migrateLegacyBackhaul, which
// is a no-op stub in default builds — see legacy_disabled.go).
//
//nolint:unused // legacy path, only compiled under -tags legacy

package panel

import (
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// legacyBackhaulUnitDir is where the old separate Modified Backhaul script put
// its units; a var so tests can point it elsewhere.
var legacyBackhaulUnitDir = "/etc/systemd/system"

// legacyBackhaulUnits lists the backhaul-iranN / backhaul-kharejN units made by
// the old separate Modified Backhaul script (quantum-backhaul.sh).
func legacyBackhaulUnits() []string {
	var out []string
	for _, pat := range []string{"backhaul-iran[0-9]*.service", "backhaul-kharej[0-9]*.service"} {
		m, _ := filepath.Glob(filepath.Join(legacyBackhaulUnitDir, pat))
		out = append(out, m...)
	}
	return out
}

// legacyBackhaulMigrate is swappable for tests.
var legacyBackhaulMigrate = func() ([]byte, error) {
	script, err := greScriptPath()
	if err != nil {
		return nil, err
	}
	return runCmdTimeout(5*time.Minute, "bash", script, "backhaul-migrate")
}

// migrateLegacyBackhaul runs `quantum.sh backhaul-migrate` once at startup when
// such units exist, so those tunnels become ordinary Quantum Backhaul tunnels
// (backhaul-server / backhaul-client) that the panel, watchdog and backup see.
func migrateLegacyBackhaul() {
	if runtime.GOOS == "windows" || len(legacyBackhaulUnits()) == 0 {
		return
	}
	out, err := legacyBackhaulMigrate()
	for _, line := range strings.Split(strings.TrimSpace(stripANSI(string(out))), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			log.Printf("[backhaul-migrate] %s", line)
		}
	}
	if err != nil {
		log.Printf("[backhaul-migrate] failed: %v", err)
	}
}
