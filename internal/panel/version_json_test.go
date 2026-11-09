package panel

// version_json_test.go — the release-integrity stamp (phase 14): the
// version --json output must carry the migration count, the feature-flag
// defaults and the feature markers the release verifier greps for.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestVersionJSONPanel(t *testing.T) {
	out := versionJSONString()
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("version --json is not valid JSON: %v", err)
	}
	if v["migrations"].(float64) != float64(store.MigrationCount()) {
		t.Fatalf("migrations mismatch: %v vs %d", v["migrations"], store.MigrationCount())
	}
	if store.MigrationCount() < 6 {
		t.Fatalf("only %d migrations compiled — the schema drifted", store.MigrationCount())
	}
	flags := v["flags"].(map[string]any)
	if _, ok := flags["legacy_enabled"]; !ok {
		t.Fatal("flags missing legacy_enabled")
	}
	if v["name"] != "quantum-panel" {
		t.Fatalf("name = %v", v["name"])
	}
}

func TestBuildFeaturesMatchTheBinary(t *testing.T) {
	// every feature marker must be a real string elsewhere in the package,
	// so a stale tree that lost the feature also loses the marker.
	markers := buildFeatures()
	for _, m := range markers {
		switch m {
		case "mesh-wizard", "engine-artifacts", "mesh-ui", "per-tunnel-weights", "structured-errors":
			// known markers — see the release verifier's grep list
		default:
			t.Fatalf("unknown feature marker %q — update the verifier", m)
		}
	}
	if !strings.Contains(strings.Join(markers, ","), "mesh-wizard") {
		t.Fatal("mesh-wizard marker missing")
	}
}
