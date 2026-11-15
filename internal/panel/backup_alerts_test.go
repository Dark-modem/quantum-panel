package panel

// backup_alerts_test.go — BACKEND-19 TASK D / finding F3: the sealed
// mesh-alerts settings file (thresholds + notifier secrets + the phase-16
// maintenance windows) must be part of the backup bundle and survive a
// restore round trip.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dark-modem/quantum-panel/internal/store"
)

func TestBackupCoversMeshAlertsState(t *testing.T) {
	newTestEngine(t, time.Now().Add(-time.Hour)) // temp config + in-memory store
	// buildBackupTar snapshots quantum.db from DISK: create (and close) a
	// file-backed store so the snapshot has a database to read
	db, err := store.Open(meshDBPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// plant the phase-16 state: one maintenance window + notifier config
	until := time.Now().Add(2 * time.Hour)
	if err := meshAlertEng.maintenanceSet("agt_b18", until); err != nil {
		t.Fatal(err)
	}
	meshAlertEng.mu.Lock()
	meshAlertEng.settings.Enabled = true
	meshAlertEng.settings.AgentOfflineMin = 3
	meshAlertEng.settings.WebhookURL = "http://10.9.9.9/hook"
	if err := meshAlertEng.persistMeshSettings(); err != nil {
		meshAlertEng.mu.Unlock()
		t.Fatal(err)
	}
	meshAlertEng.mu.Unlock()

	// the file exists on disk and is a backup member
	orig, err := os.ReadFile(meshAlertSettingsPath())
	if err != nil {
		t.Fatalf("mesh-alerts.json not on disk: %v", err)
	}
	tarBytes, man, err := buildBackupTar()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range man.Members {
		if m.Name == "mesh-alerts.json" {
			found = true
			if m.Mode != 0o600 {
				t.Fatalf("mesh-alerts.json must be backed up as 0600, got %o", m.Mode)
			}
		}
	}
	if !found {
		t.Fatalf("mesh-alerts.json missing from the backup manifest: %+v", man.Members)
	}

	// restore into a FRESH dir: the file must come back byte-identical
	_, files, err := untarBackup(tarBytes)
	if err != nil {
		t.Fatal(err)
	}
	fresh := t.TempDir()
	if rc := restoreMembers(&man, files, fresh); rc != 0 {
		t.Fatalf("restore rc=%d", rc)
	}
	got, err := os.ReadFile(filepath.Join(fresh, "mesh-alerts.json"))
	if err != nil {
		t.Fatalf("mesh-alerts.json not restored: %v", err)
	}
	if string(got) != string(orig) {
		t.Fatal("restored mesh-alerts.json differs from the original")
	}
	if !containsAll(string(got), `"agt_b18"`, `"webhook_url_sealed"`) {
		t.Fatal("restored mesh-alerts.json lost the maintenance window or sealed notifier config")
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
