package panel

// meshstore_503_hygiene_test.go — BACKEND-36 regression (PHASE35 leftover):
// a forced REAL store-open failure through meshStoreOr503 keeps the
// filesystem/driver specifics OFF the wire (B-AG2 redaction discipline —
// the client and the client-served error journal see only the masked
// string, L1) while the durable log (panel.log) carries the full raw
// detail for diagnosis.

import (
	"bytes"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestMeshStoreOr503MaskedDetail(t *testing.T) {
	// Forget any process-wide store so the next MeshStore() really opens.
	meshStoreSetForTest(nil)
	t.Cleanup(func() {
		// Restore the seams pristine: original db path, no failed-open
		// memo (a leftover meshStoreErr would poison the next lazy open
		// through the 10s retry gate), no sweep goroutine.
		meshStoreMu.Lock()
		meshStoreErr = nil
		meshStoreMu.Unlock()
		meshStoreCloseForTest()
	})

	// Forced open failure: a DIRECTORY sits at the db path — sqlite
	// refuses to open it and the raw error names the path, which is
	// exactly the L1-gray text that must stay off the wire.
	tmp := t.TempDir()
	badPath := filepath.Join(tmp, "not-a-database")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatalf("cannot stage the forced-failure path: %v", err)
	}
	oldPath := meshDBPath
	meshDBPath = func() string { return badPath }
	t.Cleanup(func() { meshDBPath = oldPath })

	// The raw detail needle: MeshStore wraps store.Open's error as
	// "open store: %v", so probe the same open once to capture the full
	// text the durable log must carry.
	_, openErr := store.Open(badPath)
	if openErr == nil {
		t.Fatal("forced store-open failure did not fail — the fixture is broken")
	}
	fullDetail := capReportString("open store: "+openErr.Error(), maxReportString)

	var buf bytes.Buffer
	oldW := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(oldW) })

	req := httptest.NewRequest("GET", "/api/mesh/overview", nil)
	w := httptest.NewRecorder()
	if s := meshStoreOr503(w, req); s != nil {
		t.Fatalf("meshStoreOr503 returned a store on a forced open failure")
	}

	// The endpoint shape is unchanged: E-SYS-01 family, catalog status,
	// only the detail text differs from the pre-fix raw string.
	if w.Code != 500 {
		t.Fatalf("E-SYS-01 status changed: HTTP %d (want the catalog 500)", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"error_code":"E-SYS-01"`) {
		t.Fatalf("client body lost the E-SYS-01 code: %s", body)
	}
	if !strings.Contains(body, "mesh store unavailable (details in the panel error journal)") {
		t.Fatalf("client body lost the masked detail: %s", body)
	}
	// No filesystem/SQL specifics may reach the API client. The base name
	// survives JSON encoding, so it is the reliable path needle.
	for _, needle := range []string{"not-a-database", "unable to open database file", "sqlite", "open store:"} {
		if strings.Contains(body, needle) {
			t.Fatalf("raw store-open detail leaked to the API client (L1 violation, needle %q): %s", needle, body)
		}
	}

	// The durable log carries the full raw detail, tagged as masked-facing,
	// with the request context.
	durable := buf.String()
	if !strings.Contains(durable, fullDetail) {
		t.Fatalf("durable log lost the full raw open detail (want %q): %s", fullDetail, durable)
	}
	if !strings.Contains(durable, "masked in the API response") {
		t.Fatalf("durable log line missing the masking tag: %s", durable)
	}
	if !strings.Contains(durable, "GET /api/mesh/overview") {
		t.Fatalf("durable log line missing the request context: %s", durable)
	}
}
