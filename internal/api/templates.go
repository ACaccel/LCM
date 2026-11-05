package api

import (
	"errors"
	"net/http"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/templates"
)

func (s *Server) handleListTemplates(w http.ResponseWriter, _ *http.Request) {
	items := templates.List()
	if items == nil {
		items = []templates.Template{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type instantiateRequest struct {
	Name   string         `json:"name"`
	Params map[string]any `json:"params"`
	DryRun bool           `json:"dryRun"`
}

type instantiateDTO struct {
	Template string `json:"template"`
	Name     string `json:"name"`
	// YAML is the concrete policy document.
	YAML string `json:"yaml"`
	// Version is null for a dry run.
	Version  *int        `json:"version"`
	Deployed bool        `json:"deployed"`
	Warnings []issueBody `json:"warnings"`
}

func (s *Server) handleInstantiateTemplate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("t")
	tpl, ok := templates.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "no such template", map[string]any{"template": id})
		return
	}
	var req instantiateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name == "" {
		writeFieldError(w, "name", "the name of the new policy is required")
		return
	}
	doc, err := tpl.Render(req.Name, req.Params)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	// Only the resource parameter names something that must already exist,
	// and the templates act on services.
	if name, _ := req.Params[tpl.ResourceParam].(string); name != "" {
		rec, err := s.resources.Get(r.Context(), name)
		switch {
		case errors.Is(err, resource.ErrNotFound), err == nil && rec.Type != model.ResourceService:
			s.fail(w, r, model.Issues{{Path: "params." + tpl.ResourceParam, Message: "no service named " + name}}, nil)
			return
		case err != nil:
			s.internalError(w, r, err)
			return
		}
	}

	out := instantiateDTO{Template: id, Name: req.Name, YAML: string(doc), Warnings: []issueBody{}}
	switch _, err := s.engine.PolicyVersions(r.Context(), req.Name); {
	case err == nil:
		writeError(w, http.StatusConflict, codeAlreadyExists, "a policy with this name already exists", map[string]any{"name": req.Name})
		return
	case !errors.Is(err, engine.ErrPolicyNotFound):
		s.internalError(w, r, err)
		return
	}
	if req.DryRun {
		warnings, err := s.engine.Validate(r.Context(), doc)
		if err != nil {
			s.fail(w, r, err, nil)
			return
		}
		out.Warnings = issuesBody(warnings)
		writeJSON(w, http.StatusOK, out)
		return
	}
	// Expecting version 0 refuses a policy created since the check above.
	version, err := s.engine.Deploy(r.Context(), doc, 0)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	out.Version, out.Deployed = &version, true
	if plan, ok := s.engine.Plan(req.Name); ok {
		out.Warnings = issuesBody(plan.Warnings())
	}
	w.Header().Set("Location", "/api/v1/policies/"+req.Name)
	writeJSON(w, http.StatusCreated, out)
}
