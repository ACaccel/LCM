package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/modules"
)

type moduleSummaryDTO struct {
	Name      string            `json:"name"`
	Labels    map[string]string `json:"labels"`
	Params    []model.Param     `json:"params"`
	Version   int               `json:"version"`
	UpdatedAt time.Time         `json:"updatedAt"`
	UpdatedBy actorDTO          `json:"updatedBy"`
}

func toModuleSummary(rec modules.Record) moduleSummaryDTO {
	labels, params := rec.Spec.Metadata.Labels, rec.Spec.Spec.Params
	if labels == nil {
		labels = map[string]string{}
	}
	if params == nil {
		params = []model.Param{}
	}
	return moduleSummaryDTO{
		Name: rec.Name, Labels: labels, Params: params, Version: rec.Version,
		UpdatedAt: rec.CreatedAt, UpdatedBy: toActorDTO(rec.CreatedBy),
	}
}

func (s *Server) handleListModules(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	recs, err := s.modules.List(r.Context())
	if err != nil {
		s.internalError(w, r, err)
		return
	}
	page, next, ok := pageSlice(w, p, recs)
	if !ok {
		return
	}
	items := make([]moduleSummaryDTO, 0, len(page))
	for _, rec := range page {
		items = append(items, toModuleSummary(rec))
	}
	writeList(w, items, next)
}

type moduleDocDTO struct {
	Document *model.Module `json:"document"`
	docMeta
}

func toModuleDoc(rec *modules.Record) moduleDocDTO {
	return moduleDocDTO{
		Document: rec.Spec,
		docMeta:  docMeta{Version: rec.Version, UpdatedAt: rec.CreatedAt, UpdatedBy: toActorDTO(rec.CreatedBy)},
	}
}

func (s *Server) handleGetModule(w http.ResponseWriter, r *http.Request) {
	rec, err := s.modules.Get(r.Context(), r.PathValue("m"))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	w.Header().Set("ETag", etag(rec.Version))
	writeJSON(w, http.StatusOK, toModuleDoc(rec))
}

func (s *Server) handlePutModule(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("m")
	im, ok := parseIfMatch(w, r)
	if !ok {
		return
	}
	body, ok := readDocument(w, r)
	if !ok {
		return
	}
	mod, err := model.DecodeModule(body)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	if mod.Metadata.Name != name {
		writeFieldError(w, "metadata.name", "must equal the module name in the URL \""+name+"\"")
		return
	}

	s.docMu.Lock()
	defer s.docMu.Unlock()
	current := 0
	switch cur, err := s.modules.Get(r.Context(), name); {
	case err == nil:
		current = cur.Version
	case !errors.Is(err, modules.ErrNotFound):
		s.internalError(w, r, err)
		return
	}
	if !checkIfMatch(w, im, current) {
		return
	}
	res, err := s.modules.Put(r.Context(), body, current)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	rec, err := s.modules.GetVersion(r.Context(), name, res.Version)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
		w.Header().Set("Location", "/api/v1/modules/"+name)
	}
	w.Header().Set("ETag", etag(res.Version))
	writeJSON(w, status, toModuleDoc(rec))
}

func (s *Server) handleModuleVersions(w http.ResponseWriter, r *http.Request) {
	p, ok := pageParams(w, r)
	if !ok {
		return
	}
	vs, err := s.modules.Versions(r.Context(), r.PathValue("m"))
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
