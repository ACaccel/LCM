package api

import (
	"net/http"
	"sort"
	"strings"
)

// Routes lists the registered method and path patterns, such as
// "GET /api/v1/resources/{resource}". It exists so tests can compare the routes
// with the OpenAPI document.
func (s *Server) Routes() []string {
	s.routes()
	out := append([]string(nil), s.patterns...)
	sort.Strings(out)
	return out
}

// routes builds the mux. Every route is registered through handle, which
// also records the pattern.
//
// Authorization levels: public routes need nothing; session routes need a
// logged-in user; admin routes accept sessions and admin-scoped tokens;
// ingest routes accept any authenticated caller, which includes
// ingest-scoped tokens.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	methods := map[string][]string{}
	s.patterns = s.patterns[:0]
	handle := func(method, path string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+path, h)
		methods[path] = append(methods[path], method)
		s.patterns = append(s.patterns, method+" "+path)
	}
	admin, session, ingest := s.requireAdmin, s.requireSession, s.requireAny
	const v1 = "/api/v1"

	// Public.
	handle("GET", v1+"/health", s.handleHealth)
	handle("GET", v1+"/openapi.yaml", s.handleOpenAPI)
	handle("POST", v1+"/auth/login", s.handleLogin)

	// Identity and administration.
	handle("POST", v1+"/auth/logout", session("", s.handleLogout))
	handle("GET", v1+"/auth/me", ingest(s.handleMe))
	handle("GET", v1+"/tokens", session("", s.handleListTokens))
	handle("POST", v1+"/tokens", session("token.create", s.handleCreateToken))
	handle("DELETE", v1+"/tokens/{id}", session("token.revoke", s.handleRevokeToken))
	handle("GET", v1+"/audit", admin(s.handleListAudit))

	// Resources of every type.
	handle("GET", v1+"/resource-types", admin(s.handleResourceTypes))
	handle("GET", v1+"/resources", admin(s.handleListResources))
	handle("POST", v1+"/resources", admin(s.handleCreateResource))
	handle("GET", v1+"/resource-archives", admin(s.handleResourceArchives))
	handle("GET", v1+"/resources/{resource}", admin(s.handleGetResource))
	handle("PUT", v1+"/resources/{resource}", admin(s.handlePutResource))
	handle("GET", v1+"/resources/{resource}/versions", admin(s.handleResourceVersions))
	handle("GET", v1+"/resources/{resource}/versions/{v}", admin(s.handleGetResourceVersion))
	handle("POST", v1+"/resources/{resource}/restore", admin(s.handleRestoreResource))
	handle("POST", v1+"/resources/{resource}/phase", admin(s.handleResourcePhase))
	handle("POST", v1+"/resources/{resource}/actions/{action}", admin(s.handleResourceAction))
	handle("GET", v1+"/actions/{id}", admin(s.handleGetAction))
	handle("GET", v1+"/resources/{resource}/impact", admin(s.handleResourceImpact))
	handle("POST", v1+"/resource-groups/actions", admin(s.handleGroupAction))
	handle("GET", v1+"/resource-groups/actions/{id}", admin(s.handleGetGroupAction))
	handle("GET", v1+"/resources/{resource}/deployments", admin(s.handleListDeployments))
	handle("GET", v1+"/resources/{resource}/timeline", admin(s.handleResourceTimeline))
	handle("GET", v1+"/timeline", admin(s.handleTimeline))

	// Native observation, maintenance and durable operations (machines,
	// routers). Writes are direct administrator requests: the components
	// refuse them from policies.
	handle("POST", v1+"/resources/{resource}/observe", admin(s.handleObserveResource))
	handle("GET", v1+"/resources/{resource}/history", admin(s.handleResourceHistory))
	handle("GET", v1+"/resources/{resource}/events", admin(s.handleResourceEvents))
	handle("GET", v1+"/resources/{resource}/capabilities", admin(s.handleResourceCapabilities))
	handle("GET", v1+"/resources/{resource}/maintenance", admin(s.handleResourceMaintenance))
	handle("POST", v1+"/resources/{resource}/maintenance", admin(s.handleEnterMaintenance))
	handle("GET", v1+"/resources/{resource}/maintenance/preview", admin(s.handleMaintenancePreview))
	handle("POST", v1+"/resources/{resource}/maintenance/exit", admin(s.handleExitMaintenance))
	handle("GET", v1+"/resources/{resource}/operations", admin(s.handleListOperations))
	handle("POST", v1+"/resources/{resource}/operations", admin(s.handleSubmitOperation))
	handle("GET", v1+"/resources/{resource}/operations/{id}", admin(s.handleGetOperation))
	handle("POST", v1+"/resources/{resource}/operations/{id}/resolve", admin(s.handleResolveOperation))
	handle("GET", v1+"/connections", admin(s.handleListConnections))

	// Security incident state is admin-only; collectors can only submit.
	handle("POST", v1+"/hooks/security", ingest(s.handleSecurityHook))
	handle("POST", v1+"/hooks/security/traffic", ingest(s.handleSecurityTrafficHook))
	handle("GET", v1+"/security/traffic", admin(s.handleSecurityTraffic))
	handle("GET", v1+"/security/status", admin(s.handleSecurityStatus))
	handle("GET", v1+"/security/incidents", admin(s.handleSecurityIncidents))
	handle("GET", v1+"/security/incidents/{id}", admin(s.handleSecurityIncident))
	handle("PUT", v1+"/security/incidents/{id}/handling", admin(s.handleSecurityHandling))
	handle("GET", v1+"/security/traffic/series", admin(s.handleSecuritySeries))
	handle("GET", v1+"/security/settings", admin(s.handleSecuritySettings))
	handle("GET", v1+"/security/settings/history", admin(s.handleSecuritySettingsHistory))
	handle("PUT", v1+"/security/settings", admin(s.handleSecurityPutSettings))
	handle("POST", v1+"/security/test-notification", admin(s.handleSecurityTest))
	handle("GET", v1+"/security/tests/{id}", admin(s.handleSecurityGetTest))

	handle("GET", v1+"/security/host", admin(s.handleSecurityHost))
	handle("POST", v1+"/security/host/preview", admin(s.handleSecurityHostPreview))
	handle("POST", v1+"/security/host/apply", admin(s.handleSecurityHostApply))
	handle("GET", v1+"/security/host/operations", admin(s.handleSecurityHostOperations))
	handle("GET", v1+"/security/host/operations/{id}", admin(s.handleSecurityHostOperation))
	// Monitoring.
	handle("GET", v1+"/resources/{resource}/checks", admin(s.handleResourceChecks))
	handle("GET", v1+"/resources/{resource}/checks/{check}/series", admin(s.handleCheckSeries))
	handle("GET", v1+"/resources/{resource}/summary", admin(s.handleResourceSummary))
	handle("GET", v1+"/check-types", admin(s.handleCheckTypes))
	handle("GET", v1+"/alerts", admin(s.handleListAlerts))

	// Policies and modules.
	handle("GET", v1+"/policies", admin(s.handleListPolicies))
	handle("GET", v1+"/policies/{p}", admin(s.handleGetPolicy))
	handle("PUT", v1+"/policies/{p}", admin(s.handlePutPolicy))
	handle("DELETE", v1+"/policies/{p}/deployment", admin(s.handleUndeployPolicy))
	handle("POST", v1+"/policies/{p}/pause", admin(s.handlePausePolicy))
	handle("POST", v1+"/policies/{p}/resume", admin(s.handleResumePolicy))
	handle("GET", v1+"/policies/{p}/versions", admin(s.handlePolicyVersions))
	handle("GET", v1+"/policies/{p}/versions/{v}", admin(s.handleGetPolicyVersion))
	handle("GET", v1+"/policies/{p}/state", admin(s.handlePolicyState))
	handle("DELETE", v1+"/policies/{p}/state/{key}", admin(s.handleDeletePolicyState))
	handle("POST", v1+"/policies/{p}/runs", admin(s.handleStartRun))
	handle("POST", v1+"/validate", admin(s.handleValidate))
	handle("GET", v1+"/modules", admin(s.handleListModules))
	handle("GET", v1+"/modules/{m}", admin(s.handleGetModule))
	handle("PUT", v1+"/modules/{m}", admin(s.handlePutModule))
	handle("GET", v1+"/modules/{m}/versions", admin(s.handleModuleVersions))
	handle("GET", v1+"/node-types", admin(s.handleNodeTypes))
	handle("GET", v1+"/templates", admin(s.handleListTemplates))
	handle("POST", v1+"/templates/{t}/instantiate", admin(s.handleInstantiateTemplate))

	// Policy-as-code.
	handle("POST", v1+"/apply", admin(s.handleApply))
	handle("GET", v1+"/export", admin(s.handleExport))
	handle("GET", v1+"/config/sources", admin(s.handleListSources))
	handle("POST", v1+"/config/sources", admin(s.handleCreateSource))
	handle("GET", v1+"/config/sources/{name}", admin(s.handleGetSource))
	handle("PUT", v1+"/config/sources/{name}", admin(s.handlePutSource))
	handle("DELETE", v1+"/config/sources/{name}", admin(s.handleDeleteSource))
	handle("POST", v1+"/config/sources/{name}/sync", admin(s.handleSyncSource))
	handle("GET", v1+"/config/ownership", admin(s.handleListOwnership))
	handle("POST", v1+"/config/ownership/{kind}/{name}/resolve", admin(s.handleResolveOwnership))
	handle("DELETE", v1+"/config/ownership/{kind}/{name}", admin(s.handleReleaseOwnership))

	// Runs and locks.
	handle("GET", v1+"/runs", admin(s.handleListRuns))
	handle("GET", v1+"/runs/{id}", admin(s.handleGetRun))
	handle("POST", v1+"/runs/{id}/cancel", admin(s.handleCancelRun))
	handle("POST", v1+"/runs/{id}/nodes/{node}/resume", admin(s.handleResumeNode))
	handle("GET", v1+"/locks", admin(s.handleListLocks))
	handle("DELETE", v1+"/locks/{name}", admin(s.handleForceReleaseLock))

	// Ingest. The webhook routes decide authentication themselves because
	// a valid HMAC signature can stand in for a token.
	handle("POST", v1+"/hooks/alerts/{source}", ingest(s.handleAlertHook))
	handle("POST", v1+"/heartbeats/{resource}/{check}", ingest(s.handleHeartbeat))
	handle("GET", v1+"/heartbeats/{resource}/{check}", ingest(s.handleHeartbeat))
	handle("POST", v1+"/hooks/github", s.handleGitHub)
	handle("POST", v1+"/hooks/{name}", s.handleWebhook)

	// Live events.
	handle("GET", v1+"/events", admin(s.handleEvents))

	// A method-less pattern on the same path is less specific than the
	// method patterns above, so it only catches the remaining methods.
	// The two webhook patterns overlap in a way that forbids it for both
	// (a fixed name and a wildcard name); other methods on them get 404.
	delete(methods, v1+"/hooks/github")
	delete(methods, v1+"/hooks/security")
	delete(methods, v1+"/hooks/{name}")
	for path, ms := range methods {
		if contains(ms, "GET") {
			ms = append(ms, "HEAD")
		}
		sort.Strings(ms)
		mux.HandleFunc(path, methodNotAllowedHandler(strings.Join(ms, ", ")))
	}
	mux.HandleFunc(v1+"/", notFoundHandler)
	return mux
}
