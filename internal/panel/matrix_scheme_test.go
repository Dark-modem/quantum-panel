// matrix_scheme_test.go — BACKEND-40 tests: the panel's Matrix instance
// pool mirrored from quantum-agent @ 2f240d2 (AGENT-27). Two legs:
// (1) GOLDEN ALIGNMENT — the agent's scheme values pinned as literals with
// provenance, so any drift between the repos fails HERE first and the
// panel re-aligns to the agent (the executor), never the other way round;
// (2) validation boundaries at the lifted cap (the whole-pool sweep lives
// on matrix_instances_test.go, the ui/live leg that pins every subnet).
// Historical invariant: for n <= 25 the derived addresses are
// byte-identical to the old 230+N scheme, so no address any panel could
// previously emit has changed.

package panel

import (
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestPanelMatrixSchemeMirrorsAgent2f240d2 pins the agent's scheme values
// as literals. PROVENANCE: quantum-agent @ 2f240d2 (AGENT-27 adjudication
// of 98d19070), internal/agent/matrix.go — matrixMaxInstances = 250 (the
// const block) and octet() (the n<=25 → 230+n / n<=102 → n-26 / else
// n-24 carve). If this test ever fails against an updated agent, re-read
// the agent source and update BOTH sides in one change — the agent wins
// every boundary disagreement.
func TestPanelMatrixSchemeMirrorsAgent2f240d2(t *testing.T) {
	if matrixMaxInstances != 250 {
		t.Fatalf("panel matrixMaxInstances = %d, agent 2f240d2 pins 250", matrixMaxInstances)
	}
	if matrixMaxInstances+1 != 251 {
		t.Fatalf("per-server instance total = %d, agent scheme pins 251", matrixMaxInstances+1)
	}
	// Boundary table: instance number → third octet, straight from the
	// agent's octet() ( historic block / below pathtest / above pathtest).
	cases := []struct{ n, octet int }{
		{2, 232},   // first of the historic block 232..255
		{17, 247},  // the OLD 16-cap's top instance — unchanged
		{25, 255},  // last of the historic block
		{26, 0},    // first past the historic block (old scheme: invalid 10.254.256.x)
		{102, 76},  // last below the pathtest nets 77/78
		{103, 79},  // first above the pathtest nets
		{251, 227}, // top of the pool
	}
	for _, c := range cases {
		in := matrixInstance{ID: "matrix" + strconv.Itoa(c.n)}
		if got := in.octet(); got != c.octet {
			t.Errorf("matrix%d octet() = %d, agent scheme pins %d", c.n, got, c.octet)
		}
	}
	// Historic byte-identity over the WHOLE historic block: the panel must
	// derive exactly the old 230+N addresses — no deployed address changed.
	for n := 2; n <= 25; n++ {
		in := matrixInstance{ID: "matrix" + strconv.Itoa(n)}
		o := strconv.Itoa(230 + n)
		if got := in.hubIP(); got != "10.254."+o+".1" {
			t.Errorf("historic hub address changed: matrix%d hubIP = %s, want 10.254.%s.1", n, got, o)
		}
		if got := in.firstPeer(); got != "10.254."+o+".2" {
			t.Errorf("historic peer address changed: matrix%d firstPeer = %s, want 10.254.%s.2", n, got, o)
		}
		if got := in.subnet(); got != "10.254."+o+".0/30" {
			t.Errorf("historic subnet changed: matrix%d subnet = %s, want 10.254.%s.0/30", n, got, o)
		}
	}
}

// TestMatrixCapValidationBoundaries — the 16→250 cap lift accepts exactly
// n = 2..251 and refuses beyond, in valid() and the API-facing resolver.
func TestMatrixCapValidationBoundaries(t *testing.T) {
	accept := []int{2, 16, 17, 18, 25, 26, 100, 251}
	reject := []int{1, 252, 300, 100000}
	for _, n := range accept {
		in := matrixInstance{ID: "matrix" + strconv.Itoa(n)}
		if !in.valid() {
			t.Errorf("matrix%d must be valid (17 = the old cap's top; 18 = newly valid)", n)
		}
		if _, err := matrixResolveInstance(in.ID); err != nil {
			t.Errorf("resolver refuses matrix%d: %v", n, err)
		}
	}
	for _, n := range reject {
		in := matrixInstance{ID: "matrix" + strconv.Itoa(n)}
		if in.valid() {
			t.Errorf("matrix%d must be refused", n)
		}
		if _, err := matrixResolveInstance(in.ID); err == nil {
			t.Errorf("resolver accepted matrix%d", n)
		}
	}
	// Above-cap via the resolver must name the agent-matching cap total.
	_, err := matrixResolveInstance("matrix252")
	if err == nil || !strings.Contains(err.Error(), "use matrix, matrix2, ... or next") {
		t.Errorf("above-cap resolver error = %v, want the standard invalid-instance guidance", err)
	}
}

// TestMatrixNextInstanceIDReachesNewCap — with the whole pool resident,
// "next" refuses with the 251-cap message; freeing one slot hands out the
// lowest free id (same behavior as before, now over the 250-slot pool).
// Runs in the matrix lab so matrixInstances() scans a real config dir.
func TestMatrixNextInstanceIDReachesNewCap(t *testing.T) {
	matrixInstanceLab(t)
	for n := 2; n <= matrixMaxInstances+1; n++ {
		in := matrixInstance{ID: "matrix" + strconv.Itoa(n)}
		if err := saveMatrixInstance(in, matrixState{Enabled: true, Network: in.network()}); err != nil {
			t.Fatal(err)
		}
	}
	if id := matrixNextInstanceID(); id != "" {
		t.Fatalf("pool full: next instance id = %q, want empty", id)
	}
	_, err := matrixResolveInstance("next")
	if err == nil || !strings.Contains(err.Error(), "maximum of 251 Matrix instances") {
		t.Fatalf("pool-full resolver error = %v, want the 251-cap message", err)
	}
	if err := os.Remove(matrixStatePathOf(matrixInstance{ID: "matrix26"})); err != nil {
		t.Fatal(err)
	}
	if id := matrixNextInstanceID(); id != "matrix26" {
		t.Fatalf("after freeing matrix26: next = %q, want matrix26", id)
	}
}
