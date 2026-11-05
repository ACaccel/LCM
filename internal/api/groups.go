package api

import (
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/model"
)

// maxTrackedGroups bounds the group actions kept in memory.
const maxTrackedGroups = 100

// maxGroupNames bounds the service names of a group selector.
const maxGroupNames = 200

// Statuses of a step of a group action, besides the action statuses.
const (
	stepPending = "pending"
	stepNotRun  = "notRun"
)

type groupSelectorDTO struct {
	Type   string            `json:"type"`
	Labels map[string]string `json:"labels"`
	Names  []string          `json:"names,omitempty"`
}

type groupRequest struct {
	Selector struct {
		Type   string            `json:"type"`
		Labels map[string]string `json:"labels"`
		Names  []string          `json:"names"`
	} `json:"selector"`
	Action string `json:"action"`
	Params struct {
		Version string `json:"version"`
		Job     string `json:"job"`
	} `json:"params"`
	DryRun            bool `json:"dryRun"`
	AcknowledgeImpact bool `json:"acknowledgeImpact"`
}

type groupPlanStepDTO struct {
	Resource string        `json:"resource"`
	Plan     *catalog.Plan `json:"plan"`
	// Error explains why the step could not run as things stand.
	Error  string    `json:"error"`
	Impact impactDTO `json:"impact"`
}

// groupPlanDTO is the dry run of a group action.
type groupPlanDTO struct {
	Action                  string             `json:"action"`
	Selector                groupSelectorDTO   `json:"selector"`
	Steps                   []groupPlanStepDTO `json:"steps"`
	AcknowledgementRequired bool               `json:"acknowledgementRequired"`
}

type groupStepDTO struct {
	Resource string `json:"resource"`
	// Status is pending, running, succeeded, failed or notRun.
	Status   string                `json:"status"`
	ActionID string                `json:"actionId"`
	Result   *catalog.ActionResult `json:"result"`
	Error    string                `json:"error"`
}

// groupDTO is the state of a running or finished group action.
type groupDTO struct {
	ID         string           `json:"id"`
	Action     string           `json:"action"`
	Status     string           `json:"status"`
	Actor      actorDTO         `json:"actor"`
	Selector   groupSelectorDTO `json:"selector"`
	Version    string           `json:"version,omitempty"`
	Job        string           `json:"job,omitempty"`
	StartedAt  time.Time        `json:"startedAt"`
	FinishedAt *time.Time       `json:"finishedAt"`
	Steps      []groupStepDTO   `json:"steps"`
}

// groupTracker remembers recent group actions so clients can poll them. It
// is bounded: when full, the oldest finished group is forgotten.
type groupTracker struct {
	mu    sync.Mutex
	byID  map[string]*groupDTO
	order []string
}

func newGroupTracker() *groupTracker { return &groupTracker{byID: map[string]*groupDTO{}} }

func (t *groupTracker) begin(g groupDTO) groupDTO {
	t.mu.Lock()
	defer t.mu.Unlock()
	g.ID, g.Status = "grp_"+strings.TrimPrefix(newActionID(), "act_"), actionRunning
	t.byID[g.ID] = &g
	t.order = append(t.order, g.ID)
	for len(t.order) > maxTrackedGroups {
		i := slices.IndexFunc(t.order, func(id string) bool { return t.byID[id].Status != actionRunning })
		if i < 0 {
			break
		}
		delete(t.byID, t.order[i])
		t.order = slices.Delete(t.order, i, i+1)
	}
	return clone(g)
}

func (t *groupTracker) update(id string, f func(g *groupDTO)) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if g, ok := t.byID[id]; ok {
		f(g)
	}
}

func (t *groupTracker) get(id string) (groupDTO, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	g, ok := t.byID[id]
	if !ok {
		return groupDTO{}, false
	}
	return clone(*g), true
}

// clone copies the steps, which the runner keeps changing.
func clone(g groupDTO) groupDTO {
	g.Steps = slices.Clone(g.Steps)
	return g
}

func toGroupPlanDTO(g catalog.Group, sel groupSelectorDTO) groupPlanDTO {
	out := groupPlanDTO{Action: g.Request.Action, Selector: sel, Steps: make([]groupPlanStepDTO, len(g.Steps))}
	info := catalog.ActionInfo(g.Request.Action)
	for i, st := range g.Steps {
		d := groupPlanStepDTO{Resource: st.Resource, Impact: actionImpactDTO(st.Impact, info)}
		if st.PlanErr != nil {
			d.Error = publicMessage(st.PlanErr)
		} else {
			p := st.Plan
			d.Plan = &p
		}
		out.Steps[i] = d
		out.AcknowledgementRequired = out.AcknowledgementRequired || st.AcknowledgementRequired
	}
	return out
}

func (s *Server) handleGroupAction(w http.ResponseWriter, r *http.Request) {
	var req groupRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	action := req.Action
	switch {
	case req.Selector.Type != "" && req.Selector.Type != model.ResourceService:
		writeFieldError(w, "selector.type", "group actions run service lifecycle actions; machine and router operations are not run in groups")
		return
	case len(req.Selector.Labels) == 0 && len(req.Selector.Names) == 0:
		writeFieldError(w, "selector.labels", "at least one label or service name is required, so a group never selects every service by accident")
		return
	case len(req.Selector.Names) > maxGroupNames:
		writeFieldError(w, "selector.names", "too many service names")
		return
	case slices.Contains(req.Selector.Names, ""):
		writeFieldError(w, "selector.names", "service names must not be empty")
		return
	case !slices.Contains(catalog.Actions, action):
		writeFieldError(w, "action", "must be a service lifecycle action: "+strings.Join(catalog.Actions, ", "))
		return
	case req.Params.Version != "" && action != catalog.ActionDeploy:
		writeFieldError(w, "params.version", "only deploy accepts a version")
		return
	case req.Params.Job != "" && action != catalog.ActionRunJob:
		writeFieldError(w, "params.job", "only run-job accepts a job")
		return
	case req.Params.Job == "" && action == catalog.ActionRunJob:
		writeFieldError(w, "params.job", "run-job needs the name of a job")
		return
	}
	if req.Selector.Labels == nil {
		req.Selector.Labels = map[string]string{}
	}
	sel := groupSelectorDTO{Type: model.ResourceService, Labels: req.Selector.Labels, Names: req.Selector.Names}
	g, err := s.manager.PlanGroup(r.Context(), catalog.GroupRequest{
		Labels: req.Selector.Labels, Names: req.Selector.Names, Action: action, AcknowledgeImpact: req.AcknowledgeImpact,
		Options: catalog.ActionOptions{Version: req.Params.Version, Job: req.Params.Job},
	})
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	if req.DryRun {
		writeJSON(w, http.StatusOK, toGroupPlanDTO(g, sel))
		return
	}
	if err := g.Check(); err != nil {
		s.fail(w, r, err, nil)
		return
	}

	actor := actorOf(r)
	steps := make([]groupStepDTO, len(g.Steps))
	for i, st := range g.Steps {
		steps[i] = groupStepDTO{Resource: st.Resource, Status: stepPending}
	}
	started := s.groups.begin(groupDTO{
		Action: action, Actor: toActorDTO(actor), Selector: sel, Version: req.Params.Version, Job: req.Params.Job,
		StartedAt: s.now().UTC(), Steps: steps,
	})
	id := started.ID
	actionIDs := make([]string, len(g.Steps))
	obs := catalog.GroupObserver{
		Started: func(i int) {
			a := s.actions.begin(actionDTO{
				Resource: g.Steps[i].Resource, Action: action, Actor: toActorDTO(actor),
				Version: req.Params.Version, Job: req.Params.Job, StartedAt: s.now().UTC(),
			})
			actionIDs[i] = a.ID
			s.publishAction(a)
			s.groups.update(id, func(gd *groupDTO) { gd.Steps[i].Status, gd.Steps[i].ActionID = actionRunning, a.ID })
		},
		Finished: func(i int, res catalog.ActionResult, err error) {
			var out *catalog.ActionResult
			if res.Action != "" {
				out = &res
			}
			a := s.actions.finish(actionIDs[i], s.now().UTC(), out, err)
			s.publishAction(a)
			s.groups.update(id, func(gd *groupDTO) {
				gd.Steps[i].Status, gd.Steps[i].Result, gd.Steps[i].Error = a.Status, out, a.Error
			})
		},
	}
	s.actions.wg.Add(1)
	go func() {
		defer s.actions.wg.Done()
		failed := s.manager.RunGroup(auth.WithActor(s.ctx, actor), g, obs)
		now := s.now().UTC()
		s.groups.update(id, func(gd *groupDTO) {
			gd.FinishedAt, gd.Status = &now, actionSucceeded
			if failed >= 0 {
				gd.Status = actionFailed
				for i := failed + 1; i < len(gd.Steps); i++ {
					gd.Steps[i].Status = stepNotRun
				}
			}
		})
	}()

	w.Header().Set("Location", "/api/v1/resource-groups/actions/"+id)
	writeJSON(w, http.StatusAccepted, started)
}

func (s *Server) handleGetGroupAction(w http.ResponseWriter, r *http.Request) {
	g, ok := s.groups.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "group action not found", nil)
		return
	}
	writeJSON(w, http.StatusOK, g)
}
