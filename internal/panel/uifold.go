// uifold.go — the /api/ui fold of the REMAINING /api/mesh endpoints the
// rebuilt UI still rides on (UI-API-REQUESTS.md phase-11 addendum: "If
// the goal is a pure /api/ui surface, please fold these into the
// contract"). ADDITIVE only: every legacy /api/mesh route keeps working
// unchanged; these are ALIASES to the SAME handlers (same payloads, same
// CSRF rules, same mesh-shaped errors), so the UI can migrate call by
// call and nothing breaks meanwhile.
//
// Phase 17 folded the READS (contract §2.16):
//
//	GET /api/ui/topologies              (mesh topology list — the UI's
//	                                     /api/ui/topologies/{id} detail
//	                                     is a DIFFERENT, richer view)
//	GET /api/ui/mesh/overview           (the mesh page snapshot: dcs +
//	                                     servers + topologies + agents;
//	                                     named mesh/overview so it does
//	                                     NOT collide with the dashboard
//	                                     /api/ui/overview)
//	GET /api/ui/engines                 (Engines tab composite view)
//	GET /api/ui/engines/artifacts       (artifact rows)
//	GET /api/ui/engines/keysnippet      (signing key snippet)
//	GET /api/ui/engines/templates       (engine parameter templates)
//	GET /api/ui/alerts                  (open + history + settings view;
//	                                     includes the phase-16 additive
//	                                     maintenance array)
//	GET /api/ui/pasarguard              (PasarGuard nodes)
//	GET /api/ui/pasarguard/coefficient  (per-DC coefficient suggestions)
//	GET /api/ui/pasarguard/overhead     (measured engine overheads)
//	GET /api/ui/profiles/pending        (safe-apply pending confirms)
//
// Phase 18 folds the MUTATIONS the addendum lists and the UI calls
// (contract §2.17) — method+path mirror each twin exactly; the handler
// sees the real request path, so audit lines stay truthful:
//
//	POST   /api/ui/backup                          (encrypted backup download)
//	POST   /api/ui/agents/{id}/link                (attach to a server)
//	POST   /api/ui/agents/{id}/push                (compile + deliver DesiredV2)
//	POST   /api/ui/agents/{id}/profile/{hardening,netopt,confirm,rollback}
//	                                               (safe-apply, phase-13 semantics)
//	POST   /api/ui/alerts/settings + /test         (alert rules surface, §2.15)
//	GET    /api/ui/pasarguard/status + /crosscheck (PG page reads)
//	POST   /api/ui/pasarguard/connect + /reconcile (PG connection writes)
//	POST   /api/ui/engines/artifacts               (artifact upload)
//	POST/PATCH/DELETE /api/ui/datacenters…         (wizard + management forms)
//	POST/PATCH/DELETE /api/ui/servers…
//	POST   /api/ui/topologies/generate             (the wizard's generator)
//	PATCH/DELETE /api/ui/topologies/{id}           (rename/retire a topology)

package panel

import "net/http"

func registerUIFoldRoutes(mux *http.ServeMux, base string) {
	// reads (phase 17, §2.16)
	mux.HandleFunc("GET "+base+"/api/ui/topologies", requireAuth(handleMeshTopoList))
	mux.HandleFunc("GET "+base+"/api/ui/mesh/overview", requireAuth(handleMeshOverview))
	mux.HandleFunc("GET "+base+"/api/ui/engines", requireAuth(handleEngineTab))
	mux.HandleFunc("GET "+base+"/api/ui/engines/artifacts", requireAuth(handleEngineArtifactList))
	mux.HandleFunc("GET "+base+"/api/ui/engines/keysnippet", requireAuth(handleEngineKeySnippet))
	mux.HandleFunc("GET "+base+"/api/ui/engines/templates", requireAuth(handleEngineTemplates))
	mux.HandleFunc("GET "+base+"/api/ui/alerts", requireAuth(handleMeshAlertsView))
	mux.HandleFunc("GET "+base+"/api/ui/pasarguard", requireAuth(handleMeshPGList))
	mux.HandleFunc("GET "+base+"/api/ui/pasarguard/coefficient", requireAuth(handleMeshCoefficient))
	mux.HandleFunc("GET "+base+"/api/ui/pasarguard/overhead", requireAuth(handleMeshOverhead))
	mux.HandleFunc("GET "+base+"/api/ui/profiles/pending", requireAuth(handleMeshProfilesPending))
	// mutations (phase 18, §2.17)
	mux.HandleFunc("POST "+base+"/api/ui/backup", requireAuth(requireCSRF(handleMeshBackupDownload)))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/link", requireAuth(requireCSRF(handleMeshAgentLink)))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/push", requireAuth(requireCSRF(handleMeshAgentPush)))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/profile/hardening", requireAuth(requireCSRF(handleMeshProfileHardening)))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/profile/netopt", requireAuth(requireCSRF(handleMeshProfileNetopt)))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/profile/confirm", requireAuth(requireCSRF(handleMeshProfileConfirm)))
	mux.HandleFunc("POST "+base+"/api/ui/agents/{id}/profile/rollback", requireAuth(requireCSRF(handleMeshProfileRollback)))
	mux.HandleFunc("POST "+base+"/api/ui/alerts/settings", requireAuth(requireCSRF(handleMeshAlertsSettings)))
	mux.HandleFunc("POST "+base+"/api/ui/alerts/test", requireAuth(requireCSRF(handleMeshAlertsTest)))
	mux.HandleFunc("GET "+base+"/api/ui/pasarguard/status", requireAuth(handlePGStatus))
	mux.HandleFunc("GET "+base+"/api/ui/pasarguard/crosscheck", requireAuth(handlePGCrossCheck))
	mux.HandleFunc("POST "+base+"/api/ui/pasarguard/connect", requireAuth(requireCSRF(handlePGConnect)))
	mux.HandleFunc("POST "+base+"/api/ui/pasarguard/reconcile", requireAuth(requireCSRF(handlePGReconcile)))
	mux.HandleFunc("POST "+base+"/api/ui/engines/artifacts", requireAuth(requireCSRF(handleEngineArtifactUpload)))
	mux.HandleFunc("POST "+base+"/api/ui/datacenters", requireAuth(requireCSRF(handleMeshDCCreate)))
	mux.HandleFunc("PATCH "+base+"/api/ui/datacenters/{id}", requireAuth(requireCSRF(handleMeshDCPatch)))
	mux.HandleFunc("DELETE "+base+"/api/ui/datacenters/{id}", requireAuth(requireCSRF(handleMeshDCDelete)))
	mux.HandleFunc("POST "+base+"/api/ui/servers", requireAuth(requireCSRF(handleMeshServerCreate)))
	mux.HandleFunc("PATCH "+base+"/api/ui/servers/{id}", requireAuth(requireCSRF(handleMeshServerPatch)))
	mux.HandleFunc("DELETE "+base+"/api/ui/servers/{id}", requireAuth(requireCSRF(handleMeshServerDelete)))
	mux.HandleFunc("POST "+base+"/api/ui/topologies/generate", requireAuth(requireCSRF(handleMeshTopoGenerate)))
	mux.HandleFunc("POST "+base+"/api/ui/topologies/{name}/members/add", requireAuth(requireCSRF(handleUITopoMemberAdd)))
	mux.HandleFunc("POST "+base+"/api/ui/topologies/{name}/members/remove", requireAuth(requireCSRF(handleUITopoMemberRemove)))
	mux.HandleFunc("PATCH "+base+"/api/ui/topologies/{id}", requireAuth(requireCSRF(handleMeshTopoPatch)))
	mux.HandleFunc("DELETE "+base+"/api/ui/topologies/{id}", requireAuth(requireCSRF(handleMeshTopoDelete)))
}
