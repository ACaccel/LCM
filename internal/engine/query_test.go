package engine_test

import (
	"errors"
	"testing"

	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
)

func TestListRuns(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	h.Deploy(e, policy("alpha", `
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a] } }
    - { id: a, type: test.echo, props: { value: 1 } }
`))
	h.Deploy(e, policy("beta", `
  nodes:
    - { id: start, type: trigger.manual }
`))
	var alpha []string
	for range 3 {
		id := h.Start(e, "alpha", nil)
		h.Wait(e, id)
		alpha = append(alpha, id)
	}
	betaID := h.Start(e, "beta", nil)
	h.Wait(e, betaID)

	page, err := e.ListRuns(h.Ctx, engine.RunQuery{Policy: "alpha", Limit: 2})
	if err != nil || len(page.Runs) != 2 || page.Next != alpha[1] {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if page.Runs[0].ID != alpha[2] || page.Runs[0].Status != engine.RunSucceeded || page.Runs[0].FinishedAt == nil {
		t.Fatalf("newest run = %+v", page.Runs[0])
	}
	page, err = e.ListRuns(h.Ctx, engine.RunQuery{Policy: "alpha", Limit: 2, Before: page.Next})
	if err != nil || len(page.Runs) != 1 || page.Runs[0].ID != alpha[0] || page.Next != "" {
		t.Fatalf("second page = %+v, %v", page, err)
	}

	all, err := e.ListRuns(h.Ctx, engine.RunQuery{})
	if err != nil || len(all.Runs) != 4 || all.Runs[0].ID != betaID {
		t.Fatalf("all runs = %+v, %v", all, err)
	}
	none, err := e.ListRuns(h.Ctx, engine.RunQuery{Statuses: []engine.RunStatus{engine.RunFailed, engine.RunCancelled}})
	if err != nil || len(none.Runs) != 0 {
		t.Fatalf("failed runs = %+v, %v", none, err)
	}
	if _, err := e.ListRuns(h.Ctx, engine.RunQuery{Statuses: []engine.RunStatus{"bogus"}}); err == nil {
		t.Fatal("unknown status accepted")
	}
}

func TestPolicyQueries(t *testing.T) {
	h := enginetest.New(t)
	e := h.Open()
	if _, err := e.PolicyVersions(h.Ctx, "ghost"); !errors.Is(err, engine.ErrPolicyNotFound) {
		t.Fatalf("versions of unknown policy: %v", err)
	}
	if _, err := e.PolicyVersion(h.Ctx, "ghost", 0); !errors.Is(err, engine.ErrPolicyNotFound) {
		t.Fatalf("version of unknown policy: %v", err)
	}

	h.Deploy(e, policy("alpha", `
  nodes:
    - { id: start, type: trigger.manual }
`))
	h.Deploy(e, policy("alpha", `
  params: { x: 1 }
  nodes:
    - { id: start, type: trigger.manual }
`))
	h.Deploy(e, policy("beta", `
  nodes:
    - { id: start, type: trigger.manual }
`))
	if err := e.SetPaused(h.Ctx, "beta", true); err != nil {
		t.Fatal(err)
	}
	if err := e.Undeploy(h.Ctx, "alpha"); err != nil {
		t.Fatal(err)
	}

	list, err := e.Policies(h.Ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("policies = %+v, %v", list, err)
	}
	alpha, beta := list[0], list[1]
	if alpha.Name != "alpha" || alpha.LatestVersion != 2 || alpha.DeployedVersion != 0 || !alpha.DeployedAt.IsZero() {
		t.Errorf("alpha = %+v", alpha)
	}
	if beta.Name != "beta" || beta.LatestVersion != 1 || beta.DeployedVersion != 1 || !beta.Paused || beta.DeployedAt.IsZero() {
		t.Errorf("beta = %+v", beta)
	}

	vs, err := e.PolicyVersions(h.Ctx, "alpha")
	if err != nil || len(vs) != 2 || vs[0].Version != 2 || vs[1].Version != 1 || vs[0].CreatedBy.Name != "cli" {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	latest, err := e.PolicyVersion(h.Ctx, "alpha", 0)
	if err != nil || latest.Version != 2 || len(latest.Document) == 0 {
		t.Fatalf("latest = %+v, %v", latest, err)
	}
	first, err := e.PolicyVersion(h.Ctx, "alpha", 1)
	if err != nil || first.Version != 1 || string(first.Document) == string(latest.Document) {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if _, err := e.PolicyVersion(h.Ctx, "alpha", 9); !errors.Is(err, engine.ErrPolicyNotFound) {
		t.Fatalf("missing version: %v", err)
	}
}
