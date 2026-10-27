package api

import (
	"net/http"
	"sort"
	"strings"
)

// Routes lists the registered method and path patterns, such as
// "GET /api/v1/services/{svc}". It exists so tests can compare the routes
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

	// Catalog.
	handle("GET", v1+"/hosts", admin(s.handleListHosts))
	handle("GET", v1+"/services", admin(s.handleListServices))
	handle("POST", v1+"/services", admin(s.handleCreateService))
	handle("GET", v1+"/services/{svc}", admin(s.handleGetService))
	handle("PUT", v1+"/services/{svc}", admin(s.handlePutService))
	handle("DELETE", v1+"/services/{svc}", admin(s.handleDeleteService))
	handle("GET", v1+"/services/{svc}/versions", admin(s.handleServiceVersions))
	handle("GET", v1+"/services/{svc}/versions/{v}", admin(s.handleGetServiceVersion))
	handle("POST", v1+"/services/{svc}/restore", admin(s.handleRestoreService))
	handle("POST", v1+"/services/{svc}/actions/{action}", admin(s.handleServiceAction))
	handle("GET", v1+"/actions/{id}", admin(s.handleGetAction))
	handle("GET", v1+"/services/{svc}/deployments", admin(s.handleListDeployments))
	handle("GET", v1+"/services/{svc}/timeline", admin(s.handleServiceTimeline))
	handle("GET", v1+"/timeline", admin(s.handleTimeline))

	// Monitoring.
	handle("GET", v1+"/services/{svc}/checks", admin(s.handleServiceChecks))
	handle("GET", v1+"/services/{svc}/checks/{check}/series", admin(s.handleCheckSeries))
	handle("GET", v1+"/services/{svc}/summary", admin(s.handleServiceSummary))
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
	handle("POST", v1+"/heartbeats/{svc}/{check}", ingest(s.handleHeartbeat))
	handle("GET", v1+"/heartbeats/{svc}/{check}", ingest(s.handleHeartbeat))
	handle("POST", v1+"/hooks/github", s.handleGitHub)
	handle("POST", v1+"/hooks/{name}", s.handleWebhook)

	// Live events.
	handle("GET", v1+"/events", admin(s.handleEvents))

	// A method-less pattern on the same path is less specific than the
	// method patterns above, so it only catches the remaining methods.
	// The two webhook patterns overlap in a way that forbids it for both
	// (a fixed name and a wildcard name); other methods on them get 404.
	delete(methods, v1+"/hooks/github")
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
