package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/message"
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

// maintained returns the path's machine for the routes only the local
// machine has (maintenance, identity); feature names the route in a
// refusal. It has written the response on failure.
func (s *Server) maintained(w http.ResponseWriter, r *http.Request, write bool, feature string) (resource.Record, bool) {
	rec, _, ok := s.ref(w, r, write)
	if !ok {
		return resource.Record{}, false
	}
	if rec.Type != model.ResourceMachine {
		s.fail(w, r, unsupported(rec, feature), nil)
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

// writeObserved pages items fetched with one extra element. cursor encodes
// the position after an item.
func writeObserved(w http.ResponseWriter, items []ObservedItem, limit int, cursor func(ObservedItem) string) {
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = encodeCursor(cursor(items[len(items)-1]))
	}
	bodies := make([]any, 0, len(items))
	for _, it := range items {
		bodies = append(bodies, it.Body)
	}
	writeList(w, bodies, next)
}

// idMarker is the cursor of items paged by ID alone.
func idMarker(it ObservedItem) string { return strconv.FormatInt(it.ID, 10) }

// minHistoryStep is the finest thinning of a history range; the stored
// points are a minute apart at best.
const minHistoryStep = time.Second

// historyQuery reads the history parameters of a request. It has written a
// 400 response when it returns false.
func historyQuery(w http.ResponseWriter, r *http.Request) (resource.HistoryQuery, bool) {
	var q resource.HistoryQuery
	switch r.URL.Query().Get("resolution") {
	case "", "raw":
	case "minute":
		q.Minute = true
	default:
		writeError(w, http.StatusBadRequest, codeBadRequest, "resolution must be raw or minute", map[string]any{"param": "resolution"})
		return q, false
	}
	p, ok := pageParams(w, r)
	if !ok {
		return q, false
	}
	if q.Cursor, ok = idCursor(w, p); !ok {
		return q, false
	}
	q.Limit = min(p.limit, maxObservedPage)
	if q.From, ok = timeParam(w, r, "from"); !ok {
		return q, false
	}
	if q.To, ok = timeParam(w, r, "to"); !ok {
		return q, false
	}
	if !q.From.IsZero() && !q.To.IsZero() && !q.From.Before(q.To) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "from must be before to", map[string]any{"param": "from"})
		return q, false
	}
	if raw := r.URL.Query().Get("step"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d < minHistoryStep {
			writeError(w, http.StatusBadRequest, codeBadRequest, "invalid query parameter",
				map[string]any{"param": "step", "expected": "a duration of at least 1s, such as 5m or 1h"})
			return q, false
		}
		q.Step = d
	}
	return q, true
}

func (s *Server) handleResourceHistory(w http.ResponseWriter, r *http.Request) {
	q, ok := historyQuery(w, r)
	if !ok {
		return
	}
	rec, obs, ok := s.observations(w, r, false)
	if !ok {
		return
	}
	limit := q.Limit
	q.Limit++
	items, err := obs.History(r.Context(), refOf(rec), q)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeObserved(w, items, limit, idMarker)
}

// eventsQuery reads the event parameters of a request. It has written a 400
// response when it returns false.
func eventsQuery(w http.ResponseWriter, r *http.Request) (resource.EventQuery, bool) {
	var q resource.EventQuery
	switch r.URL.Query().Get("order") {
	case "", "asc":
	case "desc":
		q.Newest = true
	default:
		writeError(w, http.StatusBadRequest, codeBadRequest, "order must be asc or desc", map[string]any{"param": "order"})
		return q, false
	}
	p, ok := pageParams(w, r)
	if !ok {
		return q, false
	}
	q.Limit = min(p.limit, maxObservedPage)
	if q.Newest {
		q.CursorAt, q.Cursor, ok = eventCursor(w, p)
	} else {
		q.Cursor, ok = idCursor(w, p)
	}
	if !ok {
		return q, false
	}
	if q.From, ok = timeParam(w, r, "from"); !ok {
		return q, false
	}
	if q.To, ok = timeParam(w, r, "to"); !ok {
		return q, false
	}
	if !q.From.IsZero() && !q.To.IsZero() && !q.From.Before(q.To) {
		writeError(w, http.StatusBadRequest, codeBadRequest, "from must be before to", map[string]any{"param": "from"})
		return q, false
	}
	return q, true
}

// eventCursor reads the "<occurrence ms>:<id>" cursor of newest-first
// events; no cursor is the start.
func eventCursor(w http.ResponseWriter, p pageReq) (at, id int64, ok bool) {
	if p.cursor == "" {
		return 0, 0, true
	}
	atText, idText, found := strings.Cut(p.cursor, ":")
	at, errAt := strconv.ParseInt(atText, 10, 64)
	id, errID := strconv.ParseInt(idText, 10, 64)
	if !found || errAt != nil || errID != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid cursor", map[string]any{"param": "cursor"})
		return 0, 0, false
	}
	return at, id, true
}

func (s *Server) handleResourceEvents(w http.ResponseWriter, r *http.Request) {
	q, ok := eventsQuery(w, r)
	if !ok {
		return
	}
	rec, obs, ok := s.observations(w, r, false)
	if !ok {
		return
	}
	limit := q.Limit
	q.Limit++
	items, err := obs.Events(r.Context(), refOf(rec), q)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	for i, it := range items {
		if e, ok := it.Body.(resource.Event); ok {
			items[i].Body = eventDTO{Event: e, ReasonMessage: resource.EventReasonMessage(e)}
		}
	}
	cursor := idMarker
	if q.Newest {
		cursor = func(it ObservedItem) string { return strconv.FormatInt(it.At, 10) + ":" + strconv.FormatInt(it.ID, 10) }
	}
	writeObserved(w, items, limit, cursor)
}

// eventDTO is a stored observation event with its reason for translation.
type eventDTO struct {
	resource.Event
	ReasonMessage *message.Message `json:"reasonMessage,omitempty"`
}

// capabilityDTO is a capability with a translatable message for its reason.
type capabilityDTO struct {
	Capability
	ReasonMessage message.Message `json:"reasonMessage"`
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
	items := make([]capabilityDTO, 0, len(caps))
	for _, c := range caps {
		items = append(items, capabilityDTO{Capability: c, ReasonMessage: resource.ReasonMessage(c.Reason)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleResourceMaintenance(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.maintained(w, r, false, "maintenance mode")
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
	rec, ok := s.maintained(w, r, false, "maintenance mode")
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
		Reason      string `json:"reason"`
		PreviewHash string `json:"previewHash"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.PreviewHash != "" && len(req.PreviewHash) != 64 {
		writeFieldError(w, "previewHash", "send the hash of a maintenance preview or leave it out")
		return
	}
	rec, ok := s.maintained(w, r, true, "maintenance mode")
	if !ok {
		return
	}
	v, err := s.machines.EnterMaintenance(machine.DirectRequest(r.Context()), refOf(rec), req.Reason, expected, req.PreviewHash)
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
	rec, ok := s.maintained(w, r, true, "maintenance mode")
	if !ok {
		return
	}
	if err := s.machines.ExitMaintenance(machine.DirectRequest(r.Context()), refOf(rec), req.Reason, expected); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRebindIdentity(w http.ResponseWriter, r *http.Request) {
	expected, ok := requiredIfMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason  string `json:"reason"`
		Confirm string `json:"confirm"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	rec, ok := s.maintained(w, r, true, "device identity")
	if !ok {
		return
	}
	if _, err := s.machines.RebindIdentity(machine.DirectRequest(r.Context()), refOf(rec), expected, req.Reason, req.Confirm); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	s.writeDetail(w, r, rec.ID, http.StatusOK)
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
		Reason            string `json:"reason"`
		Confirm           string `json:"confirm"`
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
	op, err := ops.Submit(machine.DirectRequest(r.Context()), refOf(rec), resource.OperationRequest{
		Action: req.Action, Key: req.IdempotencyKey, Reason: req.Reason, Confirm: req.Confirm, Version: expected,
	})
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
