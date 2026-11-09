package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// simHelper simulates the deploy helper: builds, releases, the current
// link, the unit and the ledger, over the protocol fake that validates
// every request.
type simHelper struct {
	deployhelper.Fake
	notReady error

	mu       sync.Mutex
	n        int
	builds   map[string]string
	releases []deployhelper.Release
	current  string
	previous string
	active   bool
	ledger   map[string]deployhelper.Response
	// intercept, when set, sees every request other than query first; it
	// answers instead of the simulation when ok is set.
	intercept func(req deployhelper.Request) (resp deployhelper.Response, err error, ok bool)
	// lose performs the operations listed and then loses their reply.
	lose map[deployhelper.Op]bool
	// hang records the operations listed as still running and loses the
	// reply, as when the helper is still at work.
	hang map[deployhelper.Op]bool
	// refuse answers the operations listed with the code.
	refuse map[deployhelper.Op]deployhelper.Code
}

func newSimHelper() *simHelper {
	s := &simHelper{builds: map[string]string{}, ledger: map[string]deployhelper.Response{},
		lose: map[deployhelper.Op]bool{}, hang: map[deployhelper.Op]bool{}, refuse: map[deployhelper.Op]deployhelper.Code{}}
	s.Handle = s.handle
	return s
}

func (s *simHelper) Ready() error { return s.notReady }

func helperReply(req deployhelper.Request) deployhelper.Response {
	return deployhelper.Response{Version: deployhelper.Version, Op: req.Op, OperationID: req.OperationID}
}

func refusal(resp deployhelper.Response, code deployhelper.Code, msg string) (deployhelper.Response, error) {
	resp.Code, resp.Message = code, msg
	return resp, &deployhelper.Error{Code: code, Message: msg}
}

func (s *simHelper) handle(req deployhelper.Request) (deployhelper.Response, error) {
	if req.Op == deployhelper.OpQuery {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r, ok := s.ledger[req.OperationID]; ok {
			return r, nil
		}
		return refusal(helperReply(req), deployhelper.CodeNotFound, "no operation "+req.OperationID+" is recorded")
	}
	if s.intercept != nil {
		if resp, err, ok := s.intercept(req); ok {
			return resp, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if code, ok := s.refuse[req.Op]; ok {
		resp := helperReply(req)
		if req.Op.Changes() {
			resp.State = deployhelper.StateFailed
			resp.Log = "helper log line"
		}
		resp, err := refusal(resp, code, string(req.Op)+" was refused by the simulation")
		if req.Op.Changes() {
			s.ledger[req.OperationID] = resp
		}
		return resp, err
	}
	if s.hang[req.Op] {
		s.ledger[req.OperationID] = deployhelper.Response{Version: deployhelper.Version, Op: req.Op, OperationID: req.OperationID, State: deployhelper.StateRunning}
		return deployhelper.Response{}, fmt.Errorf("%w: no reply", deployhelper.ErrOutcomeUnknown)
	}
	resp := s.perform(req)
	if req.Op.Changes() {
		resp.State = deployhelper.StateSucceeded
		if resp.Code != "" {
			resp.State = deployhelper.StateFailed
		}
		s.ledger[req.OperationID] = resp
	}
	if s.lose[req.Op] {
		return deployhelper.Response{}, fmt.Errorf("%w: no reply", deployhelper.ErrOutcomeUnknown)
	}
	if resp.Code != "" {
		return resp, &deployhelper.Error{Code: resp.Code, Message: resp.Message}
	}
	return resp, nil
}

func (s *simHelper) id() string {
	s.n++
	return fmt.Sprintf("20261101T1200%02dZ-%08x", s.n, s.n)
}

func (s *simHelper) perform(req deployhelper.Request) deployhelper.Response {
	resp := helperReply(req)
	fail := func(code deployhelper.Code, msg string) deployhelper.Response {
		resp.Code, resp.Message = code, msg
		return resp
	}
	switch req.Op {
	case deployhelper.OpBuild:
		id := s.id()
		s.builds[id] = req.Commit
		resp.Build, resp.Log = &deployhelper.BuildResult{BuildID: id, Commit: req.Commit}, "built "+req.Commit
	case deployhelper.OpInstall:
		commit, ok := s.builds[req.BuildID]
		if !ok {
			return fail(deployhelper.CodeNotFound, "no build "+req.BuildID)
		}
		id := s.id()
		m := deployhelper.Manifest{Version: 1, Service: req.Service, ReleaseID: id, BuildID: req.BuildID, Commit: commit, Artifact: req.Artifact}
		s.releases = append([]deployhelper.Release{{ReleaseID: id, Manifest: &m}}, s.releases...)
		resp.Install = &deployhelper.InstallResult{ReleaseID: id, Manifest: m}
	case deployhelper.OpActivate:
		if !slices.ContainsFunc(s.releases, func(r deployhelper.Release) bool { return r.ReleaseID == req.ReleaseID }) {
			return fail(deployhelper.CodeNotFound, "there is no release "+req.ReleaseID)
		}
		resp.Activate = &deployhelper.ActivateResult{ReleaseID: req.ReleaseID, Previous: s.current, Switched: true}
		s.previous, s.current, s.active = s.current, req.ReleaseID, true
	case deployhelper.OpStart, deployhelper.OpRestart, deployhelper.OpStop:
		if s.current == "" {
			return fail(deployhelper.CodeNotFound, req.Service+" has no unit yet")
		}
		s.active = req.Op != deployhelper.OpStop
	case deployhelper.OpStatus:
		if s.current == "" {
			return fail(deployhelper.CodeNotFound, req.Service+" has no unit yet")
		}
		state := "inactive"
		if s.active {
			state = "active"
		}
		resp.Unit = &deployhelper.UnitStatus{LoadState: "loaded", ActiveState: state, SubState: "running", Release: s.current}
	case deployhelper.OpReleases:
		for _, r := range s.releases {
			r.Current, r.Previous = r.ReleaseID == s.current, r.ReleaseID == s.previous
			resp.Releases = append(resp.Releases, r)
		}
		if resp.Releases == nil {
			resp.Releases = []deployhelper.Release{}
		}
	}
	return resp
}

// prune forgets a release, as keepReleases would.
func (s *simHelper) prune(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases = slices.DeleteFunc(s.releases, func(r deployhelper.Release) bool { return r.ReleaseID == id })
}

func (s *simHelper) ops() []deployhelper.Op {
	var out []deployhelper.Op
	for _, r := range s.Requests() {
		out = append(out, r.Op)
	}
	return out
}

// managedRuntimes serves managed services with the real adapter over the
// helper and everything else with the fakes.
type managedRuntimes struct {
	fake    *fakeRuntimes
	managed *rt.Registry
}

func (r managedRuntimes) For(x model.Runtime) (rt.Adapter, error) {
	if x.Type == model.RuntimeManaged {
		return r.managed.For(x)
	}
	return r.fake.For(x)
}

func withHelper(h rt.DeployHelper) envOption {
	return func(e *env, cfg *catalog.ManagerConfig) {
		cfg.Deploy = h
		cfg.Runtimes = managedRuntimes{fake: e.runtime, managed: rt.NewRegistry(&execx.Fake{}, rt.Options{Deploy: h})}
	}
}

const managedBuild = "touch built-by-lcm; npm run build"

func managedDoc(url string) string {
	return serviceDoc("app", "managed", fmt.Sprintf(
		"    source: { url: %s, branch: main, build: '%s', timeout: 20m }\n"+
			"    artifact: dist/\n    runtime: node24\n    start: server/main.mjs\n    startArgs: [--quiet]\n"+
			"    migrate: [node, scripts/migrate.mjs]\n", url, managedBuild), "")
}

// managedEnv is a managed service app whose source is a real git fixture
// and whose helper is simulated.
func managedEnv(t *testing.T) (*env, *gitFixture, *simHelper) {
	t.Helper()
	g := newGitFixture(t)
	h := newSimHelper()
	e := newEnv(t, realGit(g), withHelper(h))
	e.put(managedDoc(g.remote), 0)
	return e, g, h
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func TestManagedDeploy(t *testing.T) {
	e, g, h := managedEnv(t)
	v1 := g.tip()

	// The operation ID is stored before each call.
	h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
		if req.Op.Changes() {
			deps := deployments(t, e, "app")
			if len(deps) == 0 || !strings.Contains(deps[0].LogExcerpt, fmt.Sprintf("deploy helper %s operation %s", req.Op, req.OperationID)) {
				t.Errorf("%s sent before its operation ID was recorded: %+v", req.Op, deps)
			}
		}
		return deployhelper.Response{}, nil, false
	}
	res, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != v1 || res.To != catalog.StateRunning || e.state("app") != catalog.StateRunning {
		t.Fatalf("deploy = %+v", res)
	}
	reqs := h.Requests()
	if got := h.ops(); !slices.Equal(got, []deployhelper.Op{deployhelper.OpBuild, deployhelper.OpInstall, deployhelper.OpActivate}) {
		t.Fatalf("ops = %v", got)
	}
	b, in, act := reqs[0], reqs[1], reqs[2]
	if b.Service != "app" || b.Commit != v1 || b.Command != managedBuild || b.TimeoutSec != 1200 || !slices.Equal(b.Runtimes, []string{"node24"}) {
		t.Fatalf("build = %+v", b)
	}
	if in.Artifact != "dist" || in.BuildID == "" {
		t.Fatalf("install = %+v", in)
	}
	if !slices.Equal(act.Start, []string{"server/main.mjs", "--quiet"}) || act.Runtime != "node24" ||
		!slices.Equal(act.Migrate, []string{"node", "scripts/migrate.mjs"}) || act.KeepReleases != 0 {
		t.Fatalf("activate = %+v", act)
	}
	if b.OperationID == in.OperationID || in.OperationID == act.OperationID {
		t.Fatal("operation IDs must be fresh")
	}
	// LCM checked the commit out; the build ran in the helper only.
	if g.head() != v1 {
		t.Fatalf("checkout at %s", g.head())
	}
	if _, err := os.Stat(filepath.Join(g.repo, "built-by-lcm")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the build command ran as lcm: %v", err)
	}
	d := deployments(t, e, "app")[0]
	if d.Outcome != catalog.OutcomeSucceeded || lastLine(d.LogExcerpt) != "installed release "+act.ReleaseID ||
		!strings.Contains(d.LogExcerpt, "  | built "+v1) {
		t.Fatalf("deployment = %+v", d)
	}

	// The status comes from the helper.
	rec, _ := e.cat.Get(context.Background(), "app")
	ad, _ := rt.NewRegistry(&execx.Fake{}, rt.Options{Deploy: h}).For(rec.Runtime())
	if st, err := ad.Status(context.Background(), rec.Runtime()); err != nil || st.State != rt.StateActive || st.Detail["release"] != act.ReleaseID {
		t.Fatalf("status = %+v, %v", st, err)
	}

	v2 := g.commit("v2")
	if res, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil || res.Version != v2 || res.PreviousVersion != v1 {
		t.Fatalf("update = %+v, %v", res, err)
	}
}

func TestManagedDeployPlan(t *testing.T) {
	e, _, _ := managedEnv(t)
	p, err := e.mgr.Plan(e.ctx, "app", catalog.ActionDeploy, catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, m := range p.StepMessages {
		codes = append(codes, m.Code)
	}
	want := []string{"step.lock", "step.recordDeployment", "step.clone", "step.resolveHead", "step.checkout",
		"step.helperBuild", "step.helperInstall", "step.helperActivate", "step.setState"}
	if !slices.Equal(codes, want) {
		t.Fatalf("steps = %v\n%v", codes, p.Steps)
	}
	if act := p.StepMessages[7]; act.Params["migrate"] != "node scripts/migrate.mjs" || act.Params["start"] != "server/main.mjs --quiet" {
		t.Fatalf("activate step = %+v", act)
	}

	p, err = e.mgr.Plan(e.ctx, "app", catalog.ActionStop, catalog.ActionOptions{})
	if err != nil || p.StepMessages[1].Code != "step.stop" || p.StepMessages[1].Params["runtime"] != "managed" || p.StepMessages[1].Params["name"] != "app" {
		t.Fatalf("stop plan = %+v, %v", p, err)
	}
}

func TestManagedDeployFailures(t *testing.T) {
	cases := []struct {
		name  string
		setup func(h *simHelper)
		ops   []deployhelper.Op
		err   string
	}{
		{"build refused", func(h *simHelper) { h.refuse[deployhelper.OpBuild] = deployhelper.CodeRefused },
			[]deployhelper.Op{deployhelper.OpBuild}, "refused: build was refused"},
		{"install failed", func(h *simHelper) { h.refuse[deployhelper.OpInstall] = deployhelper.CodeFailed },
			[]deployhelper.Op{deployhelper.OpBuild, deployhelper.OpInstall}, "failed: install was refused"},
		{"activate busy", func(h *simHelper) { h.refuse[deployhelper.OpActivate] = deployhelper.CodeBusy },
			[]deployhelper.Op{deployhelper.OpBuild, deployhelper.OpInstall, deployhelper.OpActivate}, "busy"},
		{"activate still running", func(h *simHelper) { h.hang[deployhelper.OpActivate] = true },
			[]deployhelper.Op{deployhelper.OpBuild, deployhelper.OpInstall, deployhelper.OpActivate, deployhelper.OpQuery},
			"outcome unknown, query "},
		{"build reply lost and never recorded", func(h *simHelper) {
			h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
				if req.Op == deployhelper.OpBuild {
					return deployhelper.Response{}, deployhelper.ErrOutcomeUnknown, true
				}
				return deployhelper.Response{}, nil, false
			}
		}, []deployhelper.Op{deployhelper.OpBuild, deployhelper.OpQuery}, "not carried out"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			e, _, h := managedEnv(t)
			tt.setup(h)
			_, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{})
			if !errors.Is(err, catalog.ErrDeployFailed) || !strings.Contains(err.Error(), tt.err) {
				t.Fatalf("err = %v", err)
			}
			if got := h.ops(); !slices.Equal(got, tt.ops) {
				t.Fatalf("ops = %v", got)
			}
			d := deployments(t, e, "app")[0]
			if d.Outcome != catalog.OutcomeFailed || !strings.Contains(d.Error, tt.err) || strings.Contains(d.LogExcerpt, "installed release") {
				t.Fatalf("deployment = %+v", d)
			}
			if e.state("app") != catalog.StateFailed {
				t.Fatalf("state = %s", e.state("app"))
			}
			if tt.name == "activate still running" {
				id := h.Requests()[2].OperationID
				if !strings.Contains(d.Error, "query "+id) {
					t.Fatalf("the error must name the operation: %s", d.Error)
				}
			}
		})
	}
}

// TestManagedDeployLostReplyRecorded: a reply lost after the helper
// finished is recovered from the ledger, so the deployment succeeds.
func TestManagedDeployLostReplyRecorded(t *testing.T) {
	e, _, h := managedEnv(t)
	h.lose[deployhelper.OpActivate] = true
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	reqs := h.Requests()
	if got := h.ops(); !slices.Equal(got, []deployhelper.Op{deployhelper.OpBuild, deployhelper.OpInstall, deployhelper.OpActivate, deployhelper.OpQuery}) ||
		reqs[3].OperationID != reqs[2].OperationID {
		t.Fatalf("requests = %+v", reqs)
	}
	if d := deployments(t, e, "app")[0]; d.Outcome != catalog.OutcomeSucceeded || lastLine(d.LogExcerpt) != "installed release "+reqs[2].ReleaseID {
		t.Fatalf("deployment = %+v", d)
	}
}

func TestManagedActivateFailureAfterSwitch(t *testing.T) {
	e, _, h := managedEnv(t)
	h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
		if req.Op != deployhelper.OpActivate {
			return deployhelper.Response{}, nil, false
		}
		resp := helperReply(req)
		resp.State, resp.Log = deployhelper.StateFailed, "Job for app.service failed"
		resp.Activate = &deployhelper.ActivateResult{ReleaseID: req.ReleaseID, Previous: "20261001T000000Z-00000001", Switched: true}
		resp, err := refusal(resp, deployhelper.CodeFailed, "systemctl restart failed")
		return resp, err, true
	}
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrDeployFailed) {
		t.Fatalf("err = %v", err)
	}
	d := deployments(t, e, "app")[0]
	if d.Outcome != catalog.OutcomeFailed || !strings.Contains(d.LogExcerpt, "is current although the activation failed (previous: 20261001T000000Z-00000001)") ||
		!strings.Contains(d.LogExcerpt, "  | Job for app.service failed") {
		t.Fatalf("deployment = %+v", d)
	}
}

func TestManagedRollback(t *testing.T) {
	e, g, h := managedEnv(t)
	v1 := g.tip()
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	r1 := h.Requests()[2].ReleaseID
	g.commit("v2")
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}

	p, err := e.mgr.Plan(e.ctx, "app", catalog.ActionRollback, catalog.ActionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if last := p.StepMessages[len(p.StepMessages)-1]; last.Code != "step.helperActivatePrevious" || last.Params["release"] != r1 {
		t.Fatalf("plan = %v", p.Steps)
	}

	before := len(h.Requests())
	res, err := e.mgr.Rollback(e.ctx, "app", catalog.ActionOptions{})
	if err != nil || res.Version != v1 {
		t.Fatalf("rollback = %+v, %v", res, err)
	}
	reqs := h.Requests()[before:]
	if len(reqs) != 2 || reqs[0].Op != deployhelper.OpReleases || reqs[1].Op != deployhelper.OpActivate ||
		reqs[1].ReleaseID != r1 || reqs[1].Migrate != nil {
		t.Fatalf("rollback requests = %+v", reqs)
	}
	d := deployments(t, e, "app")[0]
	if d.Action != catalog.ActionRollback || d.Outcome != catalog.OutcomeSucceeded || lastLine(d.LogExcerpt) != "installed release "+r1 ||
		!strings.Contains(d.LogExcerpt, "the migration is not run") {
		t.Fatalf("deployment = %+v", d)
	}
}

// TestManagedRollbackRebuilds: without the release, the rollback builds
// the commit again, still without the migration.
func TestManagedRollbackRebuilds(t *testing.T) {
	e, g, h := managedEnv(t)
	v1 := g.tip()
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	h.prune(h.Requests()[2].ReleaseID)
	g.commit("v2")
	if _, err := e.mgr.Deploy(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}

	p, err := e.mgr.Plan(e.ctx, "app", catalog.ActionRollback, catalog.ActionOptions{})
	if err != nil || !slices.ContainsFunc(p.StepMessages, func(m message.Message) bool { return m.Code == "step.helperBuild" }) {
		t.Fatalf("plan = %v, %v", p.Steps, err)
	}

	before := len(h.Requests())
	if _, err := e.mgr.Rollback(e.ctx, "app", catalog.ActionOptions{}); err != nil {
		t.Fatal(err)
	}
	reqs := h.Requests()[before:]
	ops := make([]deployhelper.Op, len(reqs))
	for i, r := range reqs {
		ops[i] = r.Op
	}
	if !slices.Equal(ops, []deployhelper.Op{deployhelper.OpReleases, deployhelper.OpBuild, deployhelper.OpInstall, deployhelper.OpActivate}) ||
		reqs[1].Commit != v1 || reqs[3].Migrate != nil {
		t.Fatalf("rollback requests = %+v", reqs)
	}
}

func TestManagedWithoutHelper(t *testing.T) {
	g := newGitFixture(t)
	h := newSimHelper()
	h.notReady = fmt.Errorf("%w: /usr/local/libexec/lcm-deploy is missing", rt.ErrHelperUnavailable)
	e := newEnv(t, realGit(g), withHelper(h))
	e.put(managedDoc(g.remote), 0)
	e.put(svcDoc("plain", ""), 0)

	rec, _ := e.cat.Get(context.Background(), "app")
	for _, a := range catalog.AvailableActions(rec, h.Ready()) {
		want := catalog.ReasonHelperUnavailable
		switch a.Action {
		case catalog.ActionBackup, catalog.ActionRunJob:
			want = catalog.ReasonNotConfigured
		}
		if a.Reason != want {
			t.Errorf("%s: %+v, want %s", a.Action, a, want)
		}
	}
	plain, _ := e.cat.Get(context.Background(), "plain")
	if a := catalog.AvailableActions(plain, h.Ready())[0]; a.Action != catalog.ActionStart || !a.Allowed {
		t.Fatalf("other runtimes do not need the helper: %+v", a)
	}

	for _, act := range []func(context.Context, string, catalog.ActionOptions) (catalog.ActionResult, error){e.mgr.Deploy, e.mgr.Start, e.mgr.Stop} {
		if _, err := act(e.ctx, "app", catalog.ActionOptions{}); !errors.Is(err, catalog.ErrHelperUnavailable) || !strings.Contains(err.Error(), "lcm-deploy is missing") {
			t.Fatalf("err = %v", err)
		}
	}
	if _, err := e.mgr.Plan(e.ctx, "app", catalog.ActionDeploy, catalog.ActionOptions{}); !errors.Is(err, catalog.ErrHelperUnavailable) {
		t.Fatalf("plan err = %v", err)
	}
	if len(h.Requests()) != 0 || len(deployments(t, e, "app")) != 0 || e.state("app") != catalog.StateRegistered {
		t.Fatal("a refused action changed something")
	}
}

// TestManagedInterruptedDeployment: after a restart the deployment is
// closed as interrupted with what the ledger says about its last helper
// operation.
func TestManagedInterruptedDeployment(t *testing.T) {
	e, _, h := managedEnv(t)
	ctx := context.Background()
	opID := rt.NewOperationID()
	h.ledger[opID] = deployhelper.Response{Version: deployhelper.Version, Op: deployhelper.OpActivate, OperationID: opID,
		State: deployhelper.StateSucceeded, Activate: &deployhelper.ActivateResult{ReleaseID: "20261101T120000Z-0000abcd", Switched: true}}
	running := rt.NewOperationID()
	h.ledger[running] = deployhelper.Response{Version: deployhelper.Version, Op: deployhelper.OpBuild, OperationID: running, State: deployhelper.StateRunning}

	begin := func(log string) int64 {
		id, err := e.cat.BeginDeployment(ctx, catalog.Deployment{Resource: "app", Action: "deploy", Actor: alice})
		if err != nil {
			t.Fatal(err)
		}
		if err := e.cat.NoteDeployment(ctx, id, log); err != nil {
			t.Fatal(err)
		}
		return id
	}
	begin("deploy helper build operation " + rt.NewOperationID() + "\nbuilt x\ndeploy helper activate operation " + opID + "\n")
	begin("deploy helper build operation " + running + "\n")
	if _, err := e.mgr.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	deps := deployments(t, e, "app")
	if deps[1].Outcome != catalog.OutcomeInterrupted ||
		!strings.Contains(deps[1].Error, "deploy helper activate operation "+opID+" succeeded: release 20261101T120000Z-0000abcd is current") {
		t.Fatalf("deployment = %+v", deps[1])
	}
	if deps[0].Outcome != catalog.OutcomeInterrupted || !strings.Contains(deps[0].Error, "outcome unknown (running), query "+running) {
		t.Fatalf("deployment = %+v", deps[0])
	}
}
