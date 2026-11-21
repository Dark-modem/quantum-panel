// cores_live_test.go — BACKEND-39 live wiring proof. SKIPPED BY DEFAULT:
// QUANTUM_LIVE_CORES=1 runs it against the REAL public quantum-cores repo
// (read-only api.github.com GETs — no downloads, no tokens, no writes).
// This is an integration test, NOT part of the hermetic battery; the
// hermetic coverage lives in cores_discovery_test.go. The production
// pointer under test is coresReleaseAPI itself — the same constant the
// running panel uses, so a pass here is the discovery half of the live
// wiring (the full M2 store chain is proven separately by the documented
// manual run: 00-ADMIN/tools/backend39-live-proof.sh).

package panel

import (
	"net/url"
	"os"
	"strings"
	"testing"
)

// publishedCores pins the cores published in Dark-modem/quantum-cores as
// observed 2026-11-21 (CORES-1; matrix is exempt — in-process, no
// releases by design). A core published later is logged, not failed: the
// invariant under test is that NOTHING published is missed, not that
// nothing new appears.
var publishedCores = []string{
	"backhaul", "chisel", "ctunnel", "frp", "gost",
	"rathole", "udp2raw", "wstunnel", "xray",
}

func TestLiveCoresDiscoveryIntegration(t *testing.T) {
	if os.Getenv("QUANTUM_LIVE_CORES") != "1" {
		t.Skip("live integration test against the real quantum-cores repo; run with QUANTUM_LIVE_CORES=1")
	}
	rels, err := fetchCoresReleasesUncached()
	if err != nil {
		t.Fatalf("live cores feed unreachable: %v", err)
	}
	if len(rels) == 0 {
		t.Fatal("live feed returned zero releases")
	}
	rows := discoverCores(rels)
	byCore := map[string]coreRow{}
	for _, r := range rows {
		byCore[r.Core] = r
	}
	for _, c := range publishedCores {
		row, ok := byCore[c]
		if !ok {
			t.Errorf("live discovery: core %q missing", c)
			continue
		}
		if row.Error != "" {
			t.Errorf("live discovery: core %q unusable: %s", c, row.Error)
		}
		if !strings.HasPrefix(row.LatestTag, c+"-v") {
			t.Errorf("live discovery: core %q latest_tag = %q, want the <core>-v<ver> shape", c, row.LatestTag)
		}
		if row.LatestVersion != strings.TrimPrefix(row.LatestTag, c+"-v") {
			t.Errorf("live discovery: core %q latest_version %q does not match its tag %q", c, row.LatestVersion, row.LatestTag)
		}
		if len(row.Assets) == 0 {
			t.Errorf("live discovery: core %q lists no .tar.gz assets", c)
		}
		for _, a := range row.Assets {
			u, perr := url.Parse(a.URL)
			if perr != nil || u.Host != "github.com" {
				t.Errorf("live discovery: %s asset URL = %q (err %v): want a github.com release URL", a.Name, a.URL, perr)
			}
			if a.Size <= 0 {
				t.Errorf("live discovery: %s asset size = %d", a.Name, a.Size)
			}
			if a.Os == nil || *a.Os != "linux" {
				t.Errorf("live discovery: %s parsed os = %v, want linux", a.Name, a.Os)
			}
		}
	}
	for c := range byCore {
		known := false
		for _, k := range publishedCores {
			if c == k {
				known = true
				break
			}
		}
		if !known {
			t.Logf("live discovery: core %q published since 2026-11-21 (not in the pinned list yet)", c)
		}
	}

	// The calver shape the repo actually publishes for udp2raw — the reason
	// BACKEND-38 widened the tag regex to 2-or-3 numeric segments — must
	// keep parsing off the REAL feed.
	if u, ok := byCore["udp2raw"]; ok {
		segs := strings.Split(u.LatestVersion, ".")
		if len(segs) != 2 && len(segs) != 3 {
			t.Errorf("udp2raw latest_version = %q: want 2-or-3 numeric segments (calver)", u.LatestVersion)
		}
		for _, s := range segs {
			for _, r := range s {
				if r < '0' || r > '9' {
					t.Errorf("udp2raw latest_version = %q: non-numeric segment %q", u.LatestVersion, s)
				}
			}
		}
	}

	// frp must resolve the exact asset the M2 repo-resolve chain consumes
	// (backend39-live-proof.sh: engine=frp goos=linux goarch=amd64).
	if f, ok := byCore["frp"]; ok {
		found := false
		for _, a := range f.Assets {
			if a.Name == "frp-linux-amd64.tar.gz" {
				found = true
			}
		}
		if !found {
			t.Errorf("frp latest release %s lists no frp-linux-amd64.tar.gz asset — the M2 repo-resolve chain would 404", f.LatestTag)
		}
	}
}
