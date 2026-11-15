// uifold.go — phase 17: fold the REMAINING /api/mesh READ endpoints the
// rebuilt UI still rides on into the /api/ui surface
// (UI-API-REQUESTS.md phase-11 addendum: "If the goal is a pure /api/ui
// surface, please fold these into the contract"). ADDITIVE only: every
// legacy /api/mesh route keeps working unchanged; these are ALIASES to
// the SAME handlers (same payloads, same mesh-shaped errors), so the UI
// can migrate call by call and nothing breaks meanwhile. Mutations
// (link, backup, safe-apply, wizard writes) stay /api/mesh-only this
// phase — reads first, per the dispatch.
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

package panel

import "net/http"

func registerUIFoldRoutes(mux *http.ServeMux, base string) {
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
}
