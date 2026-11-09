package panel

// version_json.go — `quantum-panel version --json`: the release-integrity
// stamp (phase 14). The release build pins ALL of these via -ldflags:
//   -X github.com/Dark-modem/quantum-panel/internal/panel.panelVersion=<ver>
//   -X github.com/Dark-modem/quantum-panel/internal/panel.buildSHA=<sha>
//   -X github.com/Dark-modem/quantum-panel/internal/panel.buildTime=<rfc3339>
// (goVersion is runtime.Version(); migrations come from the store package).
// build-release.sh asserts these against MANIFEST.json after every build —
// a binary that cannot state its own provenance must never ship. The
// PHASE8 acceptance proved a stale tree built "successfully" while
// claiming the wrong SHA; this makes that impossible to miss.

import (
	"encoding/json"
	"runtime"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

// buildSHA / buildTime are set at release build time via -ldflags. Empty
// in dev builds.
var (
	buildSHA   string
	buildTime  string
	binaryName = "quantum-panel"
)

// buildFeatureFlags lists the panel's feature flags as compiled in, with
// their DEFAULT values (the operator's panel.json overrides them at
// runtime). The release manifest records this set so a bundle whose
// defaults drifted from the announced SHA is caught by the verifier.
func buildFeatureFlags() map[string]any {
	c := panelConfig{}
	return map[string]any{
		"legacy_enabled":   c.LegacyEnabled,
		"terminal_enabled": c.TerminalEnabled,
		"debug_enabled":    c.DebugEnabled,
		"revpath_auto":     c.RevPathAuto,
		"ports_finalized":  c.PortsFinalized,
	}
}

type versionJSON struct {
	Name       string         `json:"name"`
	Version    string         `json:"version"`
	SHA        string         `json:"sha"`
	BuildTime  string         `json:"build_time"`
	GoVersion  string         `json:"go_version"`
	GOOS       string         `json:"goos"`
	GOARCH     string         `json:"goarch"`
	Migrations int            `json:"migrations"`
	Flags      map[string]any `json:"flags"`
	Features   []string       `json:"features"`
}

func versionJSONString() string {
	v := versionJSON{
		Name:       binaryName,
		Version:    panelVersion,
		SHA:        buildSHA,
		BuildTime:  buildTime,
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		Migrations: store.MigrationCount(),
		Flags:      buildFeatureFlags(),
		Features:   buildFeatures(),
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return panelVersion
	}
	return string(b)
}

// buildFeatures lists the compile-time feature markers the release
// verifier greps for in the binary (mesh wizard, R5 artifacts, the mesh
// UI). Keep in sync with the actual features; the verifier also checks
// real strings from the binary, so a stale tree fails both ways.
func buildFeatures() []string {
	return []string{
		"mesh-wizard",
		"engine-artifacts",
		"mesh-ui",
		"per-tunnel-weights",
		"structured-errors",
	}
}
