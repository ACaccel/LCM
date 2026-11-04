package catalog_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/resource"
)

func groupDoc(name, relations string) string {
	spec := ""
	if relations != "" {
		spec = "  relations: [" + relations + "]\n"
	}
	return serviceDoc(name, "systemd", "    unit: "+name+".service\n", spec)
}

func stepNames(g catalog.Group) []string {
	var out []string
	for _, st := range g.Steps {
		out = append(out, st.Resource)
	}
	return out
}

func TestGroupOrderingAndStopOnFirstFailure(t *testing.T) {
	e := newEnv(t)
	e.put(groupDoc("web", "{ type: dependsOn, target: api }, { type: dependsOn, target: cache }"), 0)
	e.put(groupDoc("api", "{ type: dependsOn, target: db }"), 0)
	e.put(groupDoc("db", ""), 0)
	e.put(groupDoc("cache", ""), 0)
	e.put(strings.Replace(groupDoc("batch", ""), "tier: web", "tier: jobs", 1), 0)
	web := map[string]string{"tier": "web"}

	g, err := e.mgr.PlanGroup(e.ctx, catalog.GroupRequest{Labels: web, Action: catalog.ActionStart})
	if err != nil {
		t.Fatal(err)
	}
	if got := stepNames(g); !slices.Equal(got, []string{"cache", "db", "api", "web"}) {
		t.Fatalf("start order = %v", got)
	}
	if err := g.Check(); err != nil {
		t.Fatalf("start is not guarded: %v", err)
	}

	// The api fails to start: web is never attempted.
	e.runtime.adapter("api").set(func(a *fakeAdapter) { a.startErr = errors.New("unit failed") })
	var started, finished []string
	failed := e.mgr.RunGroup(e.ctx, g, catalog.GroupObserver{
		Started: func(i int) { started = append(started, g.Steps[i].Resource) },
		Finished: func(i int, _ catalog.ActionResult, err error) {
			finished = append(finished, fmt.Sprintf("%s %t", g.Steps[i].Resource, err != nil))
		},
	})
	if failed != 2 || !slices.Equal(started, []string{"cache", "db", "api"}) ||
		!slices.Equal(finished, []string{"cache false", "db false", "api true"}) {
		t.Fatalf("failed %d started %v finished %v", failed, started, finished)
	}
	if calls := e.runtime.adapter("web").Calls(); len(calls) != 0 {
		t.Fatalf("web was touched: %v", calls)
	}
	if e.state("cache") != catalog.StateRunning || e.state("web") != catalog.StateRegistered {
		t.Fatalf("states cache %s web %s", e.state("cache"), e.state("web"))
	}
	entries, _ := e.audit.List(e.ctx, 0, 0)
	if en := entries[0]; en.Action != "service.group-action" || en.Target != "services:tier=web" || en.Outcome != "error" ||
		en.Details["failed"] != "api" || fmt.Sprint(en.Details["notRun"]) != "[web]" || fmt.Sprint(en.Details["order"]) != "[cache db api web]" {
		t.Fatalf("group audit = %+v", en)
	}

	// Stop runs dependents first and needs the impact acknowledged.
	e.runtime.adapter("api").set(func(a *fakeAdapter) { a.startErr = nil })
	e.setState("api", catalog.StateRunning)
	e.setState("web", catalog.StateRunning)
	g, err = e.mgr.PlanGroup(e.ctx, catalog.GroupRequest{Labels: web, Action: catalog.ActionStop})
	if err != nil {
		t.Fatal(err)
	}
	if got := stepNames(g); !slices.Equal(got, []string{"web", "api", "db", "cache"}) {
		t.Fatalf("stop order = %v", got)
	}
	if g.Steps[0].AcknowledgementRequired || !g.Steps[1].AcknowledgementRequired || !g.Steps[3].AcknowledgementRequired {
		t.Fatalf("acknowledgement = %+v", g.Steps)
	}
	var ie *resource.ImpactError
	if err := g.Check(); !errors.As(err, &ie) || ie.Impact.Resource != "api" {
		t.Fatalf("unacknowledged stop: %v", err)
	}
	g.Request.AcknowledgeImpact = true
	if err := g.Check(); err != nil {
		t.Fatal(err)
	}
	if failed := e.mgr.RunGroup(e.ctx, g, catalog.GroupObserver{}); failed != -1 {
		t.Fatalf("stop failed at %d", failed)
	}
	entries, _ = e.audit.List(e.ctx, 0, 0)
	var stops []string
	for _, en := range slices.Backward(entries) {
		if en.Action == "service.stop" {
			stops = append(stops, en.Target)
			if en.Target == "api" {
				ack, _ := en.Details["acknowledgedImpact"].(map[string]any)
				if fmt.Sprint(ack["dependents"]) != "[web]" {
					t.Errorf("api stop audit = %+v", en)
				}
			}
		}
	}
	if !slices.Equal(stops, []string{"web", "api", "db", "cache"}) {
		t.Fatalf("stops = %v", stops)
	}
	if en := entries[0]; en.Action != "service.group-action" || en.Outcome != "ok" || en.Details["acknowledgedImpact"] == nil {
		t.Fatalf("group audit = %+v", en)
	}

	// A step that cannot run refuses the whole group before anything runs.
	g, err = e.mgr.PlanGroup(e.ctx, catalog.GroupRequest{Labels: web, Action: catalog.ActionStop, AcknowledgeImpact: true})
	if err != nil {
		t.Fatal(err)
	}
	var st *catalog.StateError
	var step *catalog.GroupStepError
	if err := g.Check(); !errors.As(err, &st) || !errors.As(err, &step) || step.Resource != "web" ||
		!strings.HasPrefix(err.Error(), "catalog: group step web cannot run: cannot stop") {
		t.Fatalf("stopped services: %v", err)
	}
}

func TestGroupRefusals(t *testing.T) {
	e := newEnv(t)
	e.put(groupDoc("web", ""), 0)
	if _, err := e.mgr.PlanGroup(e.ctx, catalog.GroupRequest{Labels: map[string]string{"tier": "web"}, Action: "reboot"}); !errors.Is(err, catalog.ErrPrecondition) {
		t.Errorf("machine operation: %v", err)
	}
	if _, err := e.mgr.PlanGroup(e.ctx, catalog.GroupRequest{Labels: map[string]string{"tier": "none"}, Action: catalog.ActionStart}); !errors.Is(err, catalog.ErrEmptyGroup) {
		t.Errorf("empty selection: %v", err)
	}
}

// A relation added after the group was checked stops the group at the step
// it now guards, before that step runs.
func TestGroupRechecksImpactBeforeEachStep(t *testing.T) {
	e := newEnv(t)
	e.put(groupDoc("api", ""), 0)
	e.put(groupDoc("web", ""), 0)
	e.setState("api", catalog.StateRunning)
	e.setState("web", catalog.StateRunning)
	g, err := e.mgr.PlanGroup(e.ctx, catalog.GroupRequest{Labels: map[string]string{"tier": "web"}, Action: catalog.ActionRestart})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Check(); err != nil || !slices.Equal(stepNames(g), []string{"api", "web"}) {
		t.Fatalf("check = %v, steps %v", err, stepNames(g))
	}
	var errs []error
	failed := e.mgr.RunGroup(e.ctx, g, catalog.GroupObserver{
		Started: func(i int) {
			if i == 1 {
				e.put(strings.Replace(groupDoc("batch", "{ type: dependsOn, target: web }"), "tier: web", "tier: jobs", 1), 0)
			}
		},
		Finished: func(_ int, _ catalog.ActionResult, err error) { errs = append(errs, err) },
	})
	var ie *resource.ImpactError
	if failed != 1 || len(errs) != 2 || errs[0] != nil || !errors.As(errs[1], &ie) || !errors.Is(errs[1], resource.ErrImpactUnacknowledged) {
		t.Fatalf("failed %d errs %v", failed, errs)
	}
	if calls := e.runtime.adapter("web").Calls(); len(calls) != 0 {
		t.Fatalf("web was restarted: %v", calls)
	}
	if calls := e.runtime.adapter("api").Calls(); len(calls) == 0 {
		t.Fatal("api was not restarted")
	}
}
