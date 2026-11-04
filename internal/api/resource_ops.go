package api

import (
	"net/http"
	"strconv"

	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
)

// maxObservedPage bounds history and event pages, which hold large items.
const maxObservedPage = 199

// ref resolves the path's resource for a read, or for a write when write is
// set, which refuses archives (see writableRef).
func (s *Server) ref(w http.ResponseWriter, r *http.Request, write bool) (resource.Record, ResourceType, bool) {
	if write {
		return s.writableRef(w, r)
	}
	return s.resourceRef(w, r)
}

// observations returns the observation backend of the path's resource. It
// has written the response on failure.
func (s *Server) observations(w http.ResponseWriter, r *http.Request, write bool) (resource.Record, Observations, bool) {
	rec, reg, ok := s.ref(w, r, write)
	if !ok {
		return resource.Record{}, nil, false
	}
	if reg.Observations == nil {
		s.fail(w, r, unsupported(rec, "native observation"), nil)
		return resource.Record{}, nil, false
	}
	return rec, reg.Observations, true
}

// operations returns the operation backend of the path's resource. It has
// written the response on failure.
func (s *Server) operations(w http.ResponseWriter, r *http.Request, write bool) (resource.Record, Operations, bool) {
	rec, reg, ok := s.ref(w, r, write)
	if !ok {
		return resource.Record{}, nil, false
	}
	if reg.Operations == nil {
		s.fail(w, r, unsupported(rec, "durable operations"), nil)
		return resource.Record{}, nil, false
	}
	return rec, reg.Operations, true
}

// maintained returns the path's machine for the maintenance routes, which
// only the local machine has. It has written the response on failure.
func (s *Server) maintained(w http.ResponseWriter, r *http.Request, write bool) (resource.Record, bool) {
	rec, _, ok := s.ref(w, r, write)
	if !ok {
		return resource.Record{}, false
	}
	if rec.Type != model.ResourceMachine {
		s.fail(w, r, unsupported(rec, "maintenance mode"), nil)
		return resource.Record{}, false
	}
	if s.machines == nil {
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "machine management is not available", nil)
		return resource.Record{}, false
	}
	return rec, true
}

func (s *Server) handleObserveResource(w http.ResponseWriter, r *http.Request) {
	rec, obs, ok := s.observations(w, r, true)
	if !ok {
		return
	}
	if err := obs.Observe(r.Context(), refOf(rec)); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	s.writeDetail(w, r, rec.ID, http.StatusOK)
}

// writeObserved pages items fetched with one extra element.
func writeObserved(w http.ResponseWriter, items []ObservedItem, limit int) {
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = encodeCursor(strconv.FormatInt(items[len(items)-1].ID, 10))
	}
	bodies := make([]any, 0, len(items))
	for _, it := range items {
		bodies = append(bodies, it.Body)
	}
	writeList(w, bodies, next)
}

func (s *Server) handleResourceHistory(w http.ResponseWriter, r *http.Request) {
	resolution := r.URL.Query().Get("resolution")
	if resolution != "" && resolution != "raw" && resolution != "minute" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "resolution must be raw or minute", map[string]any{"param": "resolution"})
		return
	}
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	after, ok := idCursor(w, p)
	if !ok {
		return
	}
	rec, obs, ok := s.observations(w, r, false)
	if !ok {
		return
	}
	limit := min(p.limit, maxObservedPage)
	items, err := obs.History(r.Context(), refOf(rec), after, limit+1, resolution == "minute")
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeObserved(w, items, limit)
}

func (s *Server) handleResourceEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	after, ok := idCursor(w, p)
	if !ok {
		return
	}
	rec, obs, ok := s.observations(w, r, false)
	if !ok {
		return
	}
	limit := min(p.limit, maxObservedPage)
	items, err := obs.Events(r.Context(), refOf(rec), after, limit+1)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeObserved(w, items, limit)
}

func (s *Server) handleResourceCapabilities(w http.ResponseWriter, r *http.Request) {
	rec, ops, ok := s.operations(w, r, false)
	if !ok {
		return
	}
	caps, err := ops.Capabilities(r.Context(), refOf(rec))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	if caps == nil {
		caps = []Capability{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": caps})
}

func (s *Server) handleResourceMaintenance(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.maintained(w, r, false)
	if !ok {
		return
	}
	v, err := s.machines.Maintenance(r.Context(), refOf(rec))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"maintenance": v})
}

func (s *Server) handleMaintenancePreview(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.maintained(w, r, false)
	if !ok {
		return
	}
	v, err := s.machines.Preview(r.Context(), refOf(rec))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleEnterMaintenance(w http.ResponseWriter, r *http.Request) {
	expected, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason       string `json:"reason"`
		Acknowledged bool   `json:"acknowledged"`
		PreviewHash  string `json:"previewHash"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if len(req.PreviewHash) != 64 {
		writeFieldError(w, "previewHash", "review the current maintenance preview before entering")
		return
	}
	rec, ok := s.maintained(w, r, true)
	if !ok {
		return
	}
	v, err := s.machines.EnterMaintenance(machine.DirectRequest(r.Context()), refOf(rec), req.Reason, req.Acknowledged, expected, req.PreviewHash)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"maintenance": v})
}

func (s *Server) handleExitMaintenance(w http.ResponseWriter, r *http.Request) {
	expected, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	rec, ok := s.maintained(w, r, true)
	if !ok {
		return
	}
	if err := s.machines.ExitMaintenance(machine.DirectRequest(r.Context()), refOf(rec), req.Reason, expected); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	rec, ops, ok := s.operations(w, r, false)
	if !ok {
		return
	}
	list, err := ops.List(r.Context(), refOf(rec))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	page, next, ok := pageSlice(w, p, list)
	if !ok {
		return
	}
	bodies := make([]any, 0, len(page))
	for _, o := range page {
		bodies = append(bodies, o.Body)
	}
	writeList(w, bodies, next)
}

func (s *Server) handleSubmitOperation(w http.ResponseWriter, r *http.Request) {
	expected, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Action            string `json:"action"`
		IdempotencyKey    string `json:"idempotencyKey"`
		AcknowledgeImpact bool   `json:"acknowledgeImpact"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	rec, ops, ok := s.operations(w, r, true)
	if !ok {
		return
	}
	// An operation the type does not list is left to Submit to refuse.
	if a, ok := typeAction(s.types[rec.Type], req.Action, resource.KindOperation); ok {
		if r, ok = s.guardImpact(w, r, rec, a, req.AcknowledgeImpact); !ok {
			return
		}
	}
	op, err := ops.Submit(machine.DirectRequest(r.Context()), refOf(rec), req.Action, req.IdempotencyKey, expected)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, op.Body)
}

func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	rec, ops, ok := s.operations(w, r, false)
	if !ok {
		return
	}
	op, err := ops.Get(r.Context(), refOf(rec), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, op.Body)
}

func (s *Server) handleResolveOperation(w http.ResponseWriter, r *http.Request) {
	expected, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	rec, ops, ok := s.operations(w, r, true)
	if !ok {
		return
	}
	op, err := ops.Resolve(machine.DirectRequest(r.Context()), refOf(rec), r.PathValue("id"), req.Reason, expected)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, op.Body)
}

// refOf names a resource for the components: by name while it is active,
// so that audit entries name it as people know it, and by ID once archived,
// because an archive has released its name.
func refOf(rec resource.Record) string {
	if rec.Phase == resource.PhaseUnmanaged {
		return "@" + rec.ID
	}
	return rec.Name
}
