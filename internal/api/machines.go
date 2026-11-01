package api

import (
	"net/http"
	"strconv"

	"github.com/ACaccel/lcm/internal/machine"
	"github.com/ACaccel/lcm/internal/model"
)

func (s *Server) machineRoutes(handle func(string, string, http.HandlerFunc)) {
	register := func(method, path string, h http.HandlerFunc) {
		handle(method, "/api/v1"+path, s.requireAdmin(func(w http.ResponseWriter, r *http.Request) {
			if s.machines == nil {
				writeError(w, http.StatusServiceUnavailable, codeUnavailable, "machine management is not available", nil)
				return
			}
			h(w, r)
		}))
	}
	register("GET", "/machines", s.handleListMachines)
	register("GET", "/machine-archives", s.handleMachineArchives)
	register("POST", "/machines", s.handleCreateMachine)
	register("GET", "/machines/{machine}", s.handleGetMachine)
	register("PUT", "/machines/{machine}", s.handlePutMachine)
	register("DELETE", "/machines/{machine}", s.handleUnmanageMachine)
	register("POST", "/machines/{machine}/phase", s.handleMachinePhase)
	register("GET", "/machines/{machine}/versions", s.handleMachineVersions)
	register("GET", "/machines/{machine}/versions/{v}", s.handleMachineVersion)
	register("POST", "/machines/{machine}/restore", s.handleMachineRestore)
	register("POST", "/machines/{machine}/observe", s.handleObserveMachine)
	register("GET", "/machines/{machine}/history", s.handleMachineHistory)
	register("GET", "/machines/{machine}/events", s.handleMachineEvents)
	register("GET", "/machines/{machine}/capabilities", s.handleMachineCapabilities)
	register("GET", "/machines/{machine}/maintenance", s.handleMachineMaintenance)
	register("GET", "/machines/{machine}/maintenance/preview", s.handleMachinePreview)
	register("POST", "/machines/{machine}/maintenance", s.handleEnterMachineMaintenance)
	register("POST", "/machines/{machine}/maintenance/exit", s.handleExitMachineMaintenance)
	register("GET", "/machines/{machine}/operations", s.handleMachineOperations)
	register("POST", "/machines/{machine}/operations", s.handleSubmitMachineOperation)
	register("GET", "/machines/{machine}/operations/{id}", s.handleMachineOperation)
	register("POST", "/machines/{machine}/operations/{id}/resolve", s.handleResolveMachineOperation)
}

func (s *Server) handleListMachines(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, err := s.machines.List(r.Context())
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	page, next, ok := pageSlice(w, p, items)
	if !ok {
		return
	}
	writeList(w, page, next)
}

func (s *Server) handleMachineArchives(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, err := s.machines.Archives(r.Context())
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	page, next, ok := pageSlice(w, p, items)
	if !ok {
		return
	}
	writeList(w, page, next)
}

func (s *Server) handleGetMachine(w http.ResponseWriter, r *http.Request) {
	v, err := s.machines.Get(r.Context(), r.PathValue("machine"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", etag(v.Version))
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) saveMachine(w http.ResponseWriter, r *http.Request, expected int) {
	body, ok := readDocument(w, r)
	if !ok {
		return
	}
	doc, err := model.DecodeMachine(body)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	if name := r.PathValue("machine"); name != "" && name != doc.Metadata.Name {
		writeFieldError(w, "metadata.name", "must equal the machine name in the URL")
		return
	}
	res, err := s.machines.Put(r.Context(), body, expected)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	v, err := s.machines.Get(r.Context(), doc.Metadata.Name)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", etag(res.Version))
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
		w.Header().Set("Location", "/api/v1/machines/"+doc.Metadata.Name)
	}
	writeJSON(w, status, v)
}

func (s *Server) handleCreateMachine(w http.ResponseWriter, r *http.Request) { s.saveMachine(w, r, 0) }
func (s *Server) handlePutMachine(w http.ResponseWriter, r *http.Request) {
	if version, ok := machineVersionMatch(w, r); ok {
		s.saveMachine(w, r, version)
	}
}
func machineVersionMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	im, ok := parseIfMatch(w, r)
	if !ok {
		return 0, false
	}
	if !im.present || im.wildcard {
		writeError(w, http.StatusBadRequest, codeBadRequest, "If-Match with the current version is required", nil)
		return 0, false
	}
	return im.version, true
}

func (s *Server) handleUnmanageMachine(w http.ResponseWriter, r *http.Request) {
	expected, ok := machineVersionMatch(w, r)
	if !ok {
		return
	}
	if err := s.machines.SetPhase(r.Context(), r.PathValue("machine"), "unmanaged", expected); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleMachinePhase(w http.ResponseWriter, r *http.Request) {
	expected, ok := machineVersionMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Phase string `json:"phase"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Phase != "managed" && req.Phase != "retired" {
		writeFieldError(w, "phase", "must be managed or retired; use DELETE to unmanage")
		return
	}
	if err := s.machines.SetPhase(r.Context(), r.PathValue("machine"), req.Phase, expected); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	s.handleGetMachine(w, r)
}
func (s *Server) handleMachineVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	vs, err := s.machines.Versions(r.Context(), r.PathValue("machine"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	page, next, ok := pageSlice(w, p, vs)
	if !ok {
		return
	}
	writeList(w, page, next)
}
func (s *Server) handleMachineVersion(w http.ResponseWriter, r *http.Request) {
	v, ok := pathVersion(w, r, "v")
	if !ok {
		return
	}
	vs, err := s.machines.Versions(r.Context(), r.PathValue("machine"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	for _, item := range vs {
		if item.Version == v {
			writeJSON(w, http.StatusOK, item)
			return
		}
	}
	s.fail(w, r, machine.ErrNotFound, nil)
}
func (s *Server) handleMachineRestore(w http.ResponseWriter, r *http.Request) {
	expected, ok := machineVersionMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Version int `json:"version"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if _, err := s.machines.Restore(r.Context(), r.PathValue("machine"), req.Version, expected); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	s.handleGetMachine(w, r)
}
func (s *Server) handleObserveMachine(w http.ResponseWriter, r *http.Request) {
	v, err := s.machines.Observe(r.Context(), r.PathValue("machine"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) handleMachineHistory(w http.ResponseWriter, r *http.Request) {
	resolution := r.URL.Query().Get("resolution")
	if resolution != "" && resolution != "raw" && resolution != "minute" {
		writeError(w, http.StatusBadRequest, codeBadRequest, "resolution must be raw or minute", nil)
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
	items, err := s.machines.HistoryResolution(r.Context(), r.PathValue("machine"), after, min(p.limit, 199)+1, resolution == "minute")
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	var next *string
	if len(items) > min(p.limit, 199) {
		items = items[:min(p.limit, 199)]
		next = encodeCursor(strconv.FormatInt(items[len(items)-1].ID, 10))
	}
	writeList(w, items, next)
}
func (s *Server) handleMachineEvents(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	after, ok := idCursor(w, p)
	if !ok {
		return
	}
	items, err := s.machines.Events(r.Context(), r.PathValue("machine"), after, min(p.limit, 199)+1)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	var next *string
	if len(items) > min(p.limit, 199) {
		items = items[:min(p.limit, 199)]
		next = encodeCursor(strconv.FormatInt(items[len(items)-1].ID, 10))
	}
	writeList(w, items, next)
}
func (s *Server) handleMachineCapabilities(w http.ResponseWriter, r *http.Request) {
	v, err := s.machines.Capabilities(r.Context(), r.PathValue("machine"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": v})
}
func (s *Server) handleMachineMaintenance(w http.ResponseWriter, r *http.Request) {
	v, err := s.machines.Maintenance(r.Context(), r.PathValue("machine"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"maintenance": v})
}
func (s *Server) handleEnterMachineMaintenance(w http.ResponseWriter, r *http.Request) {
	expected, ok := machineVersionMatch(w, r)
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
	v, err := s.machines.EnterMaintenance(machine.DirectRequest(r.Context()), r.PathValue("machine"), req.Reason, req.Acknowledged, expected, req.PreviewHash)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"maintenance": v})
}

func (s *Server) handleMachinePreview(w http.ResponseWriter, r *http.Request) {
	v, err := s.machines.Preview(r.Context(), r.PathValue("machine"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) handleExitMachineMaintenance(w http.ResponseWriter, r *http.Request) {
	expected, ok := machineVersionMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := s.machines.ExitMaintenance(machine.DirectRequest(r.Context()), r.PathValue("machine"), req.Reason, expected); err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleMachineOperations(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	items, err := s.machines.Operations(r.Context(), r.PathValue("machine"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	page, next, ok := pageSlice(w, p, items)
	if !ok {
		return
	}
	writeList(w, page, next)
}
func (s *Server) handleSubmitMachineOperation(w http.ResponseWriter, r *http.Request) {
	expected, ok := machineVersionMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Action         string `json:"action"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	v, err := s.machines.Submit(machine.DirectRequest(r.Context()), r.PathValue("machine"), req.Action, req.IdempotencyKey, expected)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}
func (s *Server) handleMachineOperation(w http.ResponseWriter, r *http.Request) {
	v, err := s.machines.Operation(r.Context(), r.PathValue("machine"), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (s *Server) handleResolveMachineOperation(w http.ResponseWriter, r *http.Request) {
	expected, ok := machineVersionMatch(w, r)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	v, err := s.machines.Resolve(machine.DirectRequest(r.Context()), r.PathValue("machine"), r.PathValue("id"), req.Reason, expected)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
