package panel

// selfcheck_fa_test.go — phase 16: every finding with a catalog entry
// carries the Persian mirror (title_fa/what_fa/fix_fa) with the SAME
// dynamic values rendered in, and no broken format verbs. The English
// fields keep their exact shape (the CLI reads them).

import (
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestSelfcheckFindingsCarryFaMirror(t *testing.T) {
	withTempConfig(t)
	withMeshStore(t)
	s := meshStoreMust(t)
	_, _, agIr, _ := uiSeedWorld(t, s)

	// The iran agent reports chisel as NOT enabled → tunnel.engine-disabled
	// (critical) with interpolated values, plus the static rollup.never-ran.
	if err := s.UpsertAgentEngine(store.AgentEngine{AgentID: agIr, Engine: "chisel",
		Installed: true, Desired: false, Instances: 0, UpdatedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}

	rep := runSelfcheck(s)
	byID := map[string]*checkFinding{}
	for i := range rep.Findings {
		byID[rep.Findings[i].ID] = &rep.Findings[i]
	}

	dis, ok := byID["tunnel.engine-disabled"]
	if !ok {
		t.Fatalf("engine-disabled finding missing: %+v", rep.Findings)
	}
	for _, field := range []string{dis.TitleFa, dis.WhatFa, dis.FixFa} {
		if field == "" {
			t.Fatalf("fa mirror incomplete: %+v", dis)
		}
		if strings.Contains(field, "%!") || strings.Contains(field, "%[") {
			t.Fatalf("fa render broken: %q", field)
		}
	}
	// The values must appear in BOTH languages (the engine and the
	// finding's own tunnel object).
	if dis.Objects == "" {
		t.Fatalf("objects must carry the tunnel key: %+v", dis)
	}
	if !strings.Contains(dis.WhatFa, "chisel") || !strings.Contains(dis.WhatFa, dis.Objects) {
		t.Fatalf("fa what lost the values: %q (objects %q)", dis.WhatFa, dis.Objects)
	}
	if !strings.Contains(dis.What, "NOT enabled") {
		t.Fatalf("en what changed shape: %q", dis.What)
	}

	// Static path (no args): the catalog texts land VERBATIM — no store
	// dependency (a migrated store pre-seeds the rollup watermark, so
	// rollup.never-ran does not fire here).
	rep2 := &selfcheckReport{At: time.Now().UTC(), OK: true}
	rep2.add("time.sync", sevWarning, "Time not NTP-synchronized",
		"The host clock is not NTP-synchronized — billing windows and certificate checks depend on it.",
		"Enable the NTP client: timedatectl set-ntp true.", "")
	st := rep2.Findings[0]
	fa := selfcheckFa["time.sync"]
	if st.TitleFa != fa.Title || st.WhatFa != fa.What || st.FixFa != fa.Fix {
		t.Fatalf("static fa mirror must land verbatim: %+v (want %v)", st, fa)
	}
}
