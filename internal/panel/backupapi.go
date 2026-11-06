// backupapi.go — TASK 2 UI glue: "download encrypted backup" behind
// RE-AUTH. POST /api/mesh/backup with {password, passphrase}: password
// is verified against the operator hash (a stolen session tab cannot
// mint backups without knowing the panel password), then the bundle is
// built with the given passphrase and streamed. The passphrase and the
// bundle bytes are never logged.

package panel

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func registerBackupRoutes(mux *http.ServeMux, base string) {
	mux.HandleFunc("POST "+base+"/api/mesh/backup", requireAuth(requireCSRF(handleMeshBackupDownload)))
}

func handleMeshBackupDownload(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Password   string `json:"password"`
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&q); err != nil {
		writeAPIError(w, r, "E-IN-03", "bad JSON body")
		return
	}
	// Re-auth: the current operator password (NOT stored, NOT logged).
	if passOK, _ := verifyPassword(cfg.PassHash, q.Password); !passOK {
		LogSecurityAudit("backup_download_denied", cfg.Username, ClientIP(r), "wrong password")
		writeAPIError(w, r, "E-AUTH-02", "panel password does not match")
		return
	}
	if len(q.Passphrase) < 12 {
		writeAPIError(w, r, "E-IN-04", "backup passphrase too short (min 12 characters)")
		return
	}
	tarBytes, man, err := buildBackupTar()
	if err != nil {
		writeAPIError(w, r, "E-SYS-01", "backup build failed: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\"quantum-panel-backup.qpbak\"")
	if err := backupEncrypt(w, []byte(q.Passphrase), tarBytes); err != nil {
		return // headers already sent; nothing safe to report
	}
	LogSecurityAudit("backup_downloaded", cfg.Username, ClientIP(r),
		"members="+strconv.Itoa(len(man.Members)))
}
