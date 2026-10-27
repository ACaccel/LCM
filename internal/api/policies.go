package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/model"
)

type triggerDTO struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Props any    `json:"props"`
}

func triggersOf(plan *engine.Plan) []triggerDTO {
	out := []triggerDTO{}
	if plan == nil {
		return out
	}
	for _, t := range plan.Triggers() {
		out = append(out, triggerDTO{ID: t.ID, Type: t.Type, Props: t.Props})
	}
	return out
}

type lastRunDTO struct {
	ID        string           `json:"id"`
	Status    engine.RunStatus `json:"status"`
	Mode      engine.Mode      `json:"mode"`
	CreatedAt time.Time        `json:"createdAt"`
}

type policySummaryDTO struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	// LatestVersion is the newest stored version; DeployedVersion is null
	// when the policy is not deployed.
	LatestVersion   int          `json:"latestVersion"`
	DeployedVersion *int         `json:"deployedVersion"`
	Deployed        bool         `json:"deployed"`
	Paused          bool         `json:"paused"`
	DeployedAt      *time.Time   `json:"deployedAt"`
	UpdatedAt       time.Time    `json:"updatedAt"`
	Triggers        []triggerDTO `json:"triggers"`
	LastRun         *lastRunDTO  `json:"lastRun"`
}

func (s *Server) policySummary(r *http.Request, info engine.PolicyInfo) (policySummaryDTO, error) {
	out := policySummaryDTO{
		Name: info.Name, Labels: map[string]string{}, LatestVersion: info.LatestVersion, Paused: info.Paused,
		DeployedAt: optTime(info.DeployedAt), UpdatedAt: info.UpdatedAt, Triggers: []triggerDTO{},
	}
	if info.DeployedVersion > 0 {
		v := info.DeployedVersion
		out.DeployedVersion, out.Deployed = &v, true
		if plan, ok := s.engine.Plan(info.Name); ok {
			out.Triggers = triggersOf(plan)
			if l := plan.Labels(); l != nil {
				out.Labels = l
			}
		}
	}
	page, err := s.engine.ListRuns(r.Context(), engine.RunQuery{Policy: info.Name, Limit: 1})
	if err != nil {
		return out, err
	}
	if len(page.Runs) > 0 {
		run := page.Runs[0]
		out.LastRun = &lastRunDTO{ID: run.ID, Status: run.Status, Mode: run.Mode, CreatedAt: run.CreatedAt}
	}
	return out, nil
}

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	infos, err := s.engine.Policies(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, next, ok := pageSlice(w, p, infos)
	if !ok {
		return
	}
	items := make([]policySummaryDTO, 0, len(page))
	for _, info := range page {
		item, err := s.policySummary(r, info)
		if err != nil {
			s.internalError(w, r, err)
			return
		}
		items = append(items, item)
	}
	writeList(w, items, next)
}

type policyDocDTO struct {
	Document json.RawMessage `json:"document"`
	docMeta
}

type policyDetailDTO struct {
	policyDocDTO
	Name            string            `json:"name"`
	Labels          map[string]string `json:"labels"`
	LatestVersion   int               `json:"latestVersion"`
	DeployedVersion *int              `json:"deployedVersion"`
	Deployed        bool              `json:"deployed"`
	Paused          bool              `json:"paused"`
	DeployedAt      *time.Time        `json:"deployedAt"`
	Triggers        []triggerDTO      `json:"triggers"`
	LastRun         *lastRunDTO       `json:"lastRun"`
	// Warnings are non-fatal findings of the compiler, such as unreachable
	// nodes.
	Warnings []issueBody `json:"warnings"`
}

// policyInfo finds one policy among the stored ones.
func (s *Server) policyInfo(r *http.Request, name string) (engine.PolicyInfo, error) {
	infos, err := s.engine.Policies(r.Context())
	if err != nil {
		return engine.PolicyInfo{}, err
	}
	for _, i := range infos {
		if i.Name == name {
			return i, nil
		}
	}
	return engine.PolicyInfo{}, engine.ErrPolicyNotFound
}

func toPolicyDoc(v engine.PolicyVersion) policyDocDTO {
	return policyDocDTO{
		Document: json.RawMessage(v.Document),
		docMeta:  docMeta{Version: v.Version, UpdatedAt: v.CreatedAt, UpdatedBy: toActorDTO(v.CreatedBy)},
	}
}

func (s *Server) policyDetail(r *http.Request, name string) (policyDetailDTO, error) {
	info, err := s.policyInfo(r, name)
	if err != nil {
		return policyDetailDTO{}, err
	}
	version := info.LatestVersion
	if info.DeployedVersion > 0 {
		version = info.DeployedVersion
	}
	pv, err := s.engine.PolicyVersion(r.Context(), name, version)
	if err != nil {
		return policyDetailDTO{}, err
	}
	sum, err := s.policySummary(r, info)
	if err != nil {
		return policyDetailDTO{}, err
	}
	out := policyDetailDTO{
		policyDocDTO: toPolicyDoc(pv), Name: sum.Name, Labels: sum.Labels, LatestVersion: sum.LatestVersion,
		DeployedVersion: sum.DeployedVersion, Deployed: sum.Deployed, Paused: sum.Paused, DeployedAt: sum.DeployedAt,
		Triggers: sum.Triggers, LastRun: sum.LastRun, Warnings: []issueBody{},
	}
	if plan, ok := s.engine.Plan(name); ok && info.DeployedVersion > 0 {
		out.Warnings = issuesBody(plan.Warnings())
		return out, nil
	}
	// A policy that is not deployed has no plan; read the labels from the
	// stored document and compile it for its warnings.
	if pol, derr := model.DecodePolicy(pv.Document); derr == nil && pol.Metadata.Labels != nil {
		out.Labels = pol.Metadata.Labels
	}
	if warnings, verr := s.engine.Validate(r.Context(), pv.Document); verr == nil {
		out.Warnings = issuesBody(warnings)
	}
	return out, nil
}

func (s *Server) handleGetPolicy(w http.ResponseWriter, r *http.Request) {
	out, err := s.policyDetail(r, r.PathValue("p"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	writeJSON(w, http.StatusOK, out)
}

// checkIfMatch compares the If-Match header with the current version of a
// document that may not exist yet (current 0). It has written the response
// and returns false when the write must not proceed.
func checkIfMatch(w http.ResponseWriter, im ifMatch, current int) bool {
	switch {
	case current == 0 && im.present && !im.wildcard:
		writeError(w, http.StatusNotFound, codeNotFound, "document not found; create it without a version in If-Match", nil)
		return false
	case current == 0:
		return true
	case !im.present || im.wildcard:
		writeError(w, http.StatusBadRequest, codeBadRequest, "If-Match with the current version is required to update an existing document", nil)
		return false
	case im.version != current:
		writeError(w, http.StatusConflict, codeVersionConflict, "the stored version differs from If-Match",
			map[string]any{"currentVersion": current, "expectedVersion": im.version})
		return false
	}
	return true
}

func (s *Server) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("p")
	im, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	body, ok := readDocument(w, r)
	if !ok {
		return
	}
	pol, err := model.DecodePolicy(body)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	if pol.Metadata.Name != name {
		writeFieldError(w, "metadata.name", "must equal the policy name in the URL \""+name+"\"")
		return
	}

	s.docMu.Lock()
	defer s.docMu.Unlock()
	current := 0
	switch vs, err := s.engine.PolicyVersions(r.Context(), name); {
	case err == nil:
		current = vs[0].Version
	case !errors.Is(err, engine.ErrPolicyNotFound):
		s.internalError(w, r, err)
		return
	}
	if !checkIfMatch(w, im, current) {
		return
	}
	version, err := s.engine.Deploy(r.Context(), body)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	out, err := s.policyDetail(r, name)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	status := http.StatusOK
	if current == 0 {
		status = http.StatusCreated
		w.Header().Set("Location", "/api/v1/policies/"+name)
	}
	w.Header().Set("ETag", etag(version))
	writeJSON(w, status, out)
}

func (s *Server) handleUndeployPolicy(w http.ResponseWriter, r *http.Request) {
	if err := s.engine.Undeploy(r.Context(), r.PathValue("p")); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) setPaused(w http.ResponseWriter, r *http.Request, paused bool) {
	name := r.PathValue("p")
	if err := s.engine.SetPaused(r.Context(), name, paused); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "paused": paused})
}

func (s *Server) handlePausePolicy(w http.ResponseWriter, r *http.Request)  { s.setPaused(w, r, true) }
func (s *Server) handleResumePolicy(w http.ResponseWriter, r *http.Request) { s.setPaused(w, r, false) }

type policyVersionDTO struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy actorDTO  `json:"createdBy"`
}

func (s *Server) handlePolicyVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	vs, err := s.engine.PolicyVersions(r.Context(), r.PathValue("p"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	page, next, ok := pageSlice(w, p, vs)
	if !ok {
		return
	}
	items := make([]policyVersionDTO, 0, len(page))
	for _, v := range page {
		items = append(items, policyVersionDTO{Version: v.Version, CreatedAt: v.CreatedAt, CreatedBy: toActorDTO(v.CreatedBy)})
	}
	writeList(w, items, next)
}

func (s *Server) handleGetPolicyVersion(w http.ResponseWriter, r *http.Request) {
	v, ok := pathVersion(w, r, "v")
	if !ok {
		return
	}
	pv, err := s.engine.PolicyVersion(r.Context(), r.PathValue("p"), v)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, toPolicyDoc(pv))
}

type stateEntryDTO struct {
	Key       string    `json:"key"`
	Value     any       `json:"value"`
	Version   int64     `json:"version"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func (s *Server) handlePolicyState(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("p")
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	if _, err := s.engine.PolicyVersions(r.Context(), name); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	entries, err := s.state.List(r.Context(), name)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, next, ok := pageSlice(w, p, entries)
	if !ok {
		return
	}
	items := make([]stateEntryDTO, 0, len(page))
	for _, e := range page {
		items = append(items, stateEntryDTO{Key: e.Key, Value: e.Value, Version: e.Version, UpdatedAt: e.UpdatedAt})
	}
	writeList(w, items, next)
}

func (s *Server) handleDeletePolicyState(w http.ResponseWriter, r *http.Request) {
	name, key := r.PathValue("p"), r.PathValue("key")
	existed, err := s.state.Delete(r.Context(), name, key)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	if !existed {
		writeError(w, http.StatusNotFound, codeNotFound, "state key not found", nil)
		return
	}
	s.record(r.Context(), "policy.state.delete", "policy:"+name, audit.OutcomeOK, map[string]any{"key": key})
	w.WriteHeader(http.StatusNoContent)
}

// manualTrigger returns the id of the policy's only trigger.manual node.
func manualTrigger(plan *engine.Plan) (string, bool) {
	var ids []string
	for _, t := range plan.Triggers() {
		if t.Type == "trigger.manual" {
			ids = append(ids, t.ID)
		}
	}
	if len(ids) != 1 {
		return "", false
	}
	return ids[0], true
}

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("p")
	var req struct {
		Trigger string `json:"trigger"`
		Event   any    `json:"event"`
		Mode    string `json:"mode"`
	}
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	mode := engine.ModeLive
	switch req.Mode {
	case "", "live":
	case "dry":
		mode = engine.ModeDry
	default:
		writeFieldError(w, "mode", "must be live or dry")
		return
	}
	plan, ok := s.engine.Plan(name)
	if !ok {
		s.fail(w, r, engine.ErrPolicyNotDeployed, nil)
		return
	}
	triggerNode := req.Trigger
	if triggerNode == "" {
		if triggerNode, ok = manualTrigger(plan); !ok {
			writeFieldError(w, "trigger", "name the trigger node to start: the policy does not have exactly one trigger.manual node")
			return
		}
	}
	id, err := s.engine.Start(r.Context(), name, triggerNode, req.Event, mode)
	if errors.Is(err, engine.ErrDropped) {
		writeError(w, http.StatusConflict, codeConflict, "the run was dropped because another run of this policy is active",
			map[string]any{"runId": id, "status": string(engine.RunDropped)})
		return
	}
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("Location", "/api/v1/runs/"+id)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"runId": id, "policy": name, "trigger": triggerNode, "mode": string(mode),
	})
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	q := engine.RunQuery{Policy: r.URL.Query().Get("policy"), Limit: p.limit, Before: p.cursor}
	for _, raw := range r.URL.Query()["status"] {
		for st := range strings.SplitSeq(raw, ",") {
			st = strings.TrimSpace(st)
			if st == "" {
				continue
			}
			if !engine.RunStatus(st).Valid() {
				writeError(w, http.StatusBadRequest, codeBadRequest, "unknown run status", map[string]any{"param": "status", "value": st})
				return
			}
			q.Statuses = append(q.Statuses, engine.RunStatus(st))
		}
	}
	page, err := s.engine.ListRuns(r.Context(), q)
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	writeList(w, page.Runs, encodeCursor(page.Next))
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	view, err := s.engine.Run(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.engine.Cancel(r.Context(), id); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"runId": id})
}

func (s *Server) handleResumeNode(w http.ResponseWriter, r *http.Request) {
	id, nodeID := r.PathValue("id"), r.PathValue("node")
	var req struct {
		Key     string `json:"key"`
		Payload any    `json:"payload"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Key == "" {
		writeFieldError(w, "key", "the suspension key of the node is required")
		return
	}
	if err := s.engine.Resume(r.Context(), id, nodeID, req.Key, req.Payload); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"runId": id, "node": nodeID})
}
