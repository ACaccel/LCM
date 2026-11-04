package api

import (
	"net/http"

	"github.com/ACaccel/lcm/internal/resource"
)

// codeImpactUnacknowledged: a disruptive action on a resource with
// dependents or control dependents was requested without
// acknowledgeImpact; details carry the impact.
const codeImpactUnacknowledged = "impact_unacknowledged"

type affectedDTO struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Relation string `json:"relation"`
	Via      string `json:"via"`
	Depth    int    `json:"depth"`
}

type impactDTO struct {
	Resource          string        `json:"resource"`
	Dependents        []affectedDTO `json:"dependents"`
	ControlDependents []affectedDTO `json:"controlDependents"`
	Unresolved        []string      `json:"unresolved"`
	// Action and AcknowledgementRequired describe the guard for one action
	// when the impact was computed for it.
	Action                  string `json:"action,omitempty"`
	AcknowledgementRequired bool   `json:"acknowledgementRequired"`
}

func toAffectedDTOs(as []resource.Affected) []affectedDTO {
	out := make([]affectedDTO, len(as))
	for i, a := range as {
		out[i] = affectedDTO{Name: a.Name, Type: a.Type, Relation: a.Relation, Via: a.Via, Depth: a.Depth}
	}
	return out
}

func toImpactDTO(imp resource.Impact) impactDTO {
	unresolved := imp.Unresolved
	if unresolved == nil {
		unresolved = []string{}
	}
	return impactDTO{
		Resource:          imp.Resource,
		Dependents:        toAffectedDTOs(imp.Dependents),
		ControlDependents: toAffectedDTOs(imp.ControlDependents),
		Unresolved:        unresolved,
	}
}

// actionImpactDTO is the impact as it applies to one action.
func actionImpactDTO(imp resource.Impact, a resource.Action) impactDTO {
	d := toImpactDTO(imp)
	d.Action = a.Name
	d.AcknowledgementRequired = resource.RequiresAcknowledgement(a, imp)
	return d
}

// typeAction finds an action of the resource's type by name and kind (any
// kind when kind is empty). Types without an action catalog offer none.
func typeAction(reg ResourceType, name, kind string) (resource.Action, bool) {
	c, ok := reg.Type.(ActionCatalog)
	if !ok {
		return resource.Action{}, false
	}
	for _, a := range c.ActionCatalog() {
		if a.Name == name && (kind == "" || a.Kind == kind) {
			return a, true
		}
	}
	return resource.Action{}, false
}

// guardImpact applies the impact guard to action a on rec and, when the
// caller acknowledged a non-empty impact, returns a request whose audit
// entries record it. It has written the response when ok is false.
func (s *Server) guardImpact(w http.ResponseWriter, r *http.Request, rec resource.Record, a resource.Action, acknowledged bool) (*http.Request, bool) {
	// Unguarded changes skip the graph: some of them (resume, unmanage)
	// address archives by ID, whose name may belong to another resource.
	if !resource.Guarded(a) {
		return r, true
	}
	imp, err := s.resources.CheckImpact(r.Context(), rec.Name, a, acknowledged)
	if err != nil {
		s.fail(w, r, err, nil)
		return nil, false
	}
	if acknowledged {
		r = r.WithContext(resource.WithAcknowledgedImpact(r.Context(), imp))
	}
	return r, true
}

func (s *Server) handleResourceImpact(w http.ResponseWriter, r *http.Request) {
	rec, reg, ok := s.resourceRef(w, r)
	if !ok {
		return
	}
	imp, err := s.resources.Impact(r.Context(), refOf(rec))
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	name := r.URL.Query().Get("action")
	if name == "" {
		writeJSON(w, http.StatusOK, toImpactDTO(imp))
		return
	}
	a, ok := typeAction(reg, name, "")
	if !ok {
		writeError(w, http.StatusBadRequest, codeBadRequest, rec.Type+" resources have no action "+name, map[string]any{"param": "action"})
		return
	}
	writeJSON(w, http.StatusOK, actionImpactDTO(imp, a))
}
