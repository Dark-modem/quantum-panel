// agentmaintenance.go — phase 16: the maintenance-mode REST surface
// (phase-13 engine support existed; this is the API the UI's maintenance
// toggle rides — UI-API-CONTRACT.md §2.15, requested in
// UI-API-REQUESTS.md phase 14 item 3).
//
//	GET  /api/ui/agents/{id}/maintenance  — view the window
//	POST /api/ui/agents/{id}/maintenance  — {"until": epoch} set,
//	                                        0/empty body clear
//
// Semantics: rules keep evaluating and recording; only notification
// DELIVERY is silenced while a window is active (agent-scoped rules AND
// tunnel-scoped rules whose EITHER end's agent is in maintenance).
// Windows persist in the sealed alert-settings file (restart-safe).

package panel

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// maintenanceMaxAhead caps a window: no multi-month silences by accident.
const maintenanceMaxAhead = 30 * 24 * time.Hour

func registerAgentMaintenanceRoutes(mux *http.ServeMux, base string) {
	p := base + "/api/ui/agents/{id}/maintenance"
	mux.HandleFunc("GET "+p, requireAuth(handleUIAgentMaintenanceGet))
	mux.HandleFunc("POST "+p, requireAuth(requireCSRF(handleUIAgentMaintenanceSet)))
}

func handleUIAgentMaintenanceGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	if _, err := h.GetAgent(id); err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": id}, "")
		return
	}
	writeJSON(w, maintenanceView(id))
}

// maintenanceView is the shared shape for GET and the POST response.
func maintenanceView(id string) map[string]any {
	until, active := meshAlertEng.maintenanceUntil(id)
	return map[string]any{
		"agent":       id,
		"maintenance": map[string]any{"until": until, "active": active},
	}
}

func handleUIAgentMaintenanceSet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	h, ok := uiOpen(w, r)
	if !ok {
		return
	}
	if _, err := h.GetAgent(id); err != nil {
		uiErrWrite(w, r, "E-NOTFOUND", map[string]any{"agent": id}, "")
		return
	}
	// Empty body = clear (an operator toggle sends 0 explicitly, but an
	// unconditional UI "clear" button may POST nothing).
	var q struct {
		Until *int64 `json:"until"`
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err == nil {
		if len(b) > 0 {
			err = json.Unmarshal(b, &q)
		}
	}
	if err != nil {
		uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "until"},
			"body must be JSON like {\"until\": 1762450000} (0 or empty clears)")
		return
	}
	var until time.Time
	if q.Until != nil && *q.Until != 0 {
		until = time.Unix(*q.Until, 0)
		now := time.Now()
		if until.Before(now) {
			uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "until"},
				"until is in the past; send 0 to clear the window instead")
			return
		}
		if until.After(now.Add(maintenanceMaxAhead)) {
			uiErrWrite(w, r, "E-VALIDATION", map[string]any{"field": "until"},
				"until must be within 30 days of now")
			return
		}
	}
	if err := meshAlertEng.maintenanceSet(id, until); err != nil {
		uiErrWrite(w, r, "E-STORE", map[string]any{"op": "maintenance persist"}, err.Error())
		return
	}
	untilEpoch := int64(0)
	if !until.IsZero() {
		untilEpoch = until.Unix()
	}
	meshAudit(meshActor(r), "mesh.agent.maintenance",
		fmt.Sprintf(`{"agent":%q,"until":%d,"active":%t}`, id, untilEpoch, !until.IsZero()))
	writeJSON(w, map[string]any{"status": "ok", "agent": id,
		"maintenance": map[string]any{"until": untilEpoch, "active": !until.IsZero()}})
}
