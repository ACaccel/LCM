package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/events"
)

// maxTrackedActions bounds the action history kept in memory.
const maxTrackedActions = 500

// maxActionError bounds the error text kept per tracked action.
const maxActionError = 1024

// Action statuses.
const (
	actionRunning   = "running"
	actionSucceeded = "succeeded"
	actionFailed    = "failed"
)

// serviceActions are the actions accepted under /services/{svc}/actions.
var serviceActions = []string{
	catalog.ActionDeploy, catalog.ActionRollback, catalog.ActionStart, catalog.ActionStop,
	catalog.ActionRestart, catalog.ActionBackup, catalog.ActionRunJob, catalog.ActionRetire,
}

// actionDTO is the state of an API-started service action. It is the
// response of GET /actions/{id} and the payload of action events.
type actionDTO struct {
	ID      string   `json:"id"`
	Service string   `json:"service"`
	Action  string   `json:"action"`
	Status  string   `json:"status"`
	Actor   actorDTO `json:"actor"`
	// Version and Job echo the request options.
	Version    string                `json:"version,omitempty"`
	Job        string                `json:"job,omitempty"`
	StartedAt  time.Time             `json:"startedAt"`
	FinishedAt *time.Time            `json:"finishedAt"`
	Result     *catalog.ActionResult `json:"result"`
	Error      string                `json:"error"`
}

// actionTracker remembers recent actions so clients can poll their outcome.
// It is bounded: when full, the oldest finished action is forgotten.
type actionTracker struct {
	max int
	wg  sync.WaitGroup

	mu    sync.Mutex
	byID  map[string]*actionDTO
	order []string
}

func newActionTracker(max int) *actionTracker {
	return &actionTracker{max: max, byID: map[string]*actionDTO{}}
}

func newActionID() string {
	var b [8]byte
	// crypto/rand.Read does not fail on supported platforms.
	_, _ = rand.Read(b[:])
	return "act_" + hex.EncodeToString(b[:])
}

// begin registers a running action and returns its snapshot.
func (t *actionTracker) begin(a actionDTO) actionDTO {
	t.mu.Lock()
	defer t.mu.Unlock()
	a.ID, a.Status = newActionID(), actionRunning
	t.byID[a.ID] = &a
	t.order = append(t.order, a.ID)
	for len(t.order) > t.max {
		i := slices.IndexFunc(t.order, func(id string) bool { return t.byID[id].Status != actionRunning })
		if i < 0 {
			break
		}
		delete(t.byID, t.order[i])
		t.order = slices.Delete(t.order, i, i+1)
	}
	return a
}

func (t *actionTracker) finish(id string, now time.Time, res *catalog.ActionResult, err error) actionDTO {
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.byID[id]
	a.FinishedAt = &now
	a.Result = res
	if err != nil {
		a.Status, a.Error = actionFailed, truncate(publicMessage(err), maxActionError)
	} else {
		a.Status = actionSucceeded
	}
	return *a
}

func (t *actionTracker) get(id string) (actionDTO, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	a, ok := t.byID[id]
	if !ok {
		return actionDTO{}, false
	}
	return *a, true
}

// runningOn reports whether an action on the service is still running.
func (t *actionTracker) runningOn(service string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, a := range t.byID {
		if a.Service == service && a.Status == actionRunning {
			return true
		}
	}
	return false
}

// Close waits for running actions. They stop on their own when the
// application context ends, so callers cancel that first.
func (s *Server) Close() { s.actions.wg.Wait() }

func (s *Server) publishAction(a actionDTO) {
	if s.hub != nil {
		s.hub.Publish(events.Event{Type: events.TypeAction, Service: a.Service, Data: a})
	}
}

type actionRequest struct {
	Version string `json:"version"`
	Job     string `json:"job"`
}

func (s *Server) handleServiceAction(w http.ResponseWriter, r *http.Request) {
	name, action := r.PathValue("svc"), r.PathValue("action")
	if !slices.Contains(serviceActions, action) {
		writeError(w, http.StatusNotFound, codeNotFound, "unknown action",
			map[string]any{"action": action, "actions": serviceActions})
		return
	}
	dry, ok := boolParam(w, r, "dryRun")
	if !ok {
		return
	}
	var req actionRequest
	if !decodeOptionalJSON(w, r, &req) {
		return
	}
	switch {
	case req.Version != "" && action != catalog.ActionDeploy:
		writeFieldError(w, "version", "only deploy accepts a version")
		return
	case req.Job != "" && action != catalog.ActionRunJob:
		writeFieldError(w, "job", "only run-job accepts a job")
		return
	case req.Job == "" && action == catalog.ActionRunJob:
		writeFieldError(w, "job", "run-job needs the name of a job")
		return
	}
	opts := catalog.ActionOptions{Version: req.Version, Job: req.Job}

	// Plan is the precondition check: it fails for an unknown service, a
	// state the action does not allow and missing configuration, all of
	// which are better reported now than in the tracker.
	plan, err := s.manager.Plan(r.Context(), name, action, opts)
	if err != nil {
		s.fail(w, r, err, nil)
		return
	}
	if dry {
		writeJSON(w, http.StatusOK, plan)
		return
	}
	if holder, held, err := s.locks.Holder(r.Context(), catalog.LockName(name)); err != nil {
		s.internalError(w, r, err)
		return
	} else if held {
		writeError(w, http.StatusConflict, codeLockHeld, fmt.Sprintf("service %s is busy: lock held by %s", name, holder.Owner),
			lockDetails(holder))
		return
	}
	if s.actions.runningOn(name) {
		writeError(w, http.StatusConflict, codeLockHeld, "another action is running on service "+name, nil)
		return
	}

	actor := actorOf(r)
	started := s.actions.begin(actionDTO{
		Service: name, Action: action, Actor: toActorDTO(actor), Version: req.Version, Job: req.Job, StartedAt: s.now().UTC(),
	})
	s.publishAction(started)
	s.actions.wg.Add(1)
	go s.runAction(auth.WithActor(s.ctx, actor), started.ID, name, action, opts)

	w.Header().Set("Location", "/api/v1/actions/"+started.ID)
	writeJSON(w, http.StatusAccepted, map[string]any{"actionId": started.ID, "status": actionRunning})
}

// runAction executes the action and records its outcome. It never panics
// out of the goroutine: a panic in a runtime adapter is recorded as a
// failure of the action.
func (s *Server) runAction(ctx context.Context, id, name, action string, opts catalog.ActionOptions) {
	defer s.actions.wg.Done()
	var (
		res catalog.ActionResult
		err error
	)
	func() {
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("panic in service action", "action", action, "service", name, "panic", p)
				err = fmt.Errorf("internal error while running %s", action)
			}
		}()
		res, err = s.dispatchAction(ctx, name, action, opts)
	}()
	if err != nil {
		s.log.Warn("service action failed", "action", action, "service", name, "action_id", id, "error", err)
	}
	var out *catalog.ActionResult
	if res.Action != "" {
		out = &res
	}
	s.publishAction(s.actions.finish(id, s.now().UTC(), out, err))
}

func (s *Server) dispatchAction(ctx context.Context, name, action string, opts catalog.ActionOptions) (catalog.ActionResult, error) {
	switch action {
	case catalog.ActionDeploy:
		return s.manager.Deploy(ctx, name, opts)
	case catalog.ActionRollback:
		return s.manager.Rollback(ctx, name, opts)
	case catalog.ActionStart:
		return s.manager.Start(ctx, name, opts)
	case catalog.ActionStop:
		return s.manager.Stop(ctx, name, opts)
	case catalog.ActionRestart:
		return s.manager.Restart(ctx, name, opts)
	case catalog.ActionBackup:
		return s.manager.Backup(ctx, name, opts)
	case catalog.ActionRunJob:
		return s.manager.RunJob(ctx, name, opts)
	case catalog.ActionRetire:
		return s.manager.Retire(ctx, name, opts)
	}
	return catalog.ActionResult{}, fmt.Errorf("unknown action %q", action)
}

func (s *Server) handleGetAction(w http.ResponseWriter, r *http.Request) {
	a, ok := s.actions.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, codeNotFound, "action not found", nil)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// truncate shortens s to at most n bytes without splitting a rune.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}
