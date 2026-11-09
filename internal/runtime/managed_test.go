package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/execx"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// fakeHelper is a deploy helper for tests: a protocol fake that is ready
// unless notReady is set.
type fakeHelper struct {
	deployhelper.Fake
	notReady error
}

func (f *fakeHelper) Ready() error { return f.notReady }

var managedRuntime = model.Runtime{Type: model.RuntimeManaged, Start: "server/main.mjs", Service: "web"}

func managedAdapter(t *testing.T, h rt.DeployHelper) rt.Adapter {
	t.Helper()
	a, err := rt.NewRegistry(&execx.Fake{}, rt.Options{Deploy: h}).For(managedRuntime)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func reply(req deployhelper.Request) deployhelper.Response {
	return deployhelper.Response{Version: deployhelper.Version, Op: req.Op, OperationID: req.OperationID}
}

func TestManagedCapabilities(t *testing.T) {
	if got := rt.CapabilitiesOf(managedRuntime); got != (rt.Capabilities{Start: true, Stop: true, Restart: true}) {
		t.Fatalf("capabilities = %+v", got)
	}
}

func TestManagedStatus(t *testing.T) {
	ctx := context.Background()
	unit := func(active string) func(deployhelper.Request) (deployhelper.Response, error) {
		return func(req deployhelper.Request) (deployhelper.Response, error) {
			resp := reply(req)
			resp.Unit = &deployhelper.UnitStatus{LoadState: "loaded", ActiveState: active, SubState: "running", MainPID: 42, Release: "20261101T120000Z-0000abcd"}
			return resp, nil
		}
	}
	for active, want := range map[string]rt.State{
		"active": rt.StateActive, "reloading": rt.StateActive, "inactive": rt.StateInactive,
		"failed": rt.StateFailed, "activating": rt.StateUnknown,
	} {
		h := &fakeHelper{Fake: deployhelper.Fake{Handle: unit(active)}}
		st, err := managedAdapter(t, h).Status(ctx, managedRuntime)
		if err != nil || st.State != want || st.Detail["release"] != "20261101T120000Z-0000abcd" || st.Detail["pid"] != "42" {
			t.Fatalf("%s: %+v, %v", active, st, err)
		}
		reqs := h.Requests()
		if len(reqs) != 1 || reqs[0].Op != deployhelper.OpStatus || reqs[0].Service != "web" {
			t.Fatalf("requests = %+v", reqs)
		}
	}

	refuse := func(code deployhelper.Code, msg string) *fakeHelper {
		return &fakeHelper{Fake: deployhelper.Fake{Handle: func(req deployhelper.Request) (deployhelper.Response, error) {
			resp := reply(req)
			resp.Code, resp.Message = code, msg
			return resp, &deployhelper.Error{Code: code, Message: msg}
		}}}
	}
	// No unit yet: nothing of the service runs.
	st, err := managedAdapter(t, refuse(deployhelper.CodeNotFound, "web has no unit yet")).Status(ctx, managedRuntime)
	if err != nil || st.State != rt.StateInactive || st.Detail["reason"] != "web has no unit yet" {
		t.Fatalf("not found: %+v, %v", st, err)
	}
	st, err = managedAdapter(t, refuse(deployhelper.CodeRefused, "web is not in the services")).Status(ctx, managedRuntime)
	if err != nil || st.State != rt.StateUnknown || !strings.Contains(st.Detail["reason"], "not in the services") {
		t.Fatalf("refused: %+v, %v", st, err)
	}
	broken := &fakeHelper{Fake: deployhelper.Fake{Handle: func(deployhelper.Request) (deployhelper.Response, error) {
		return deployhelper.Response{}, errors.New("sudo: a password is required")
	}}}
	if st, err := managedAdapter(t, broken).Status(ctx, managedRuntime); err == nil || st.State != rt.StateUnknown {
		t.Fatalf("broken: %+v, %v", st, err)
	}

	// Without the helper nothing is called and the status is unknown.
	for _, h := range []rt.DeployHelper{nil, &fakeHelper{notReady: rt.ErrHelperUnavailable}} {
		st, err := managedAdapter(t, h).Status(ctx, managedRuntime)
		if err != nil || st.State != rt.StateUnknown || !strings.Contains(st.Detail["reason"], "not installed") {
			t.Fatalf("unavailable: %+v, %v", st, err)
		}
	}
}

func TestManagedActions(t *testing.T) {
	ctx := context.Background()
	h := &fakeHelper{}
	a := managedAdapter(t, h)
	for _, act := range []func(context.Context, model.Runtime) error{a.Start, a.Stop, a.Restart} {
		if err := act(ctx, managedRuntime); err != nil {
			t.Fatal(err)
		}
	}
	reqs := h.Requests()
	if len(reqs) != 3 || reqs[0].Op != deployhelper.OpStart || reqs[1].Op != deployhelper.OpStop || reqs[2].Op != deployhelper.OpRestart {
		t.Fatalf("requests = %+v", reqs)
	}
	if reqs[0].OperationID == reqs[1].OperationID || reqs[0].Service != "web" {
		t.Fatalf("operation IDs must be fresh and name the service: %+v", reqs)
	}

	failing := &fakeHelper{Fake: deployhelper.Fake{Handle: func(req deployhelper.Request) (deployhelper.Response, error) {
		resp := reply(req)
		resp.State, resp.Code, resp.Message = deployhelper.StateFailed, deployhelper.CodeFailed, "systemctl stop failed"
		return resp, &deployhelper.Error{Code: resp.Code, Message: resp.Message}
	}}}
	var he *rt.HelperError
	if err := managedAdapter(t, failing).Stop(ctx, managedRuntime); !errors.As(err, &he) || he.Code != deployhelper.CodeFailed || he.Unknown {
		t.Fatalf("failed stop = %v", err)
	}
	if err := managedAdapter(t, nil).Restart(ctx, managedRuntime); !errors.Is(err, rt.ErrHelperUnavailable) {
		t.Fatalf("restart without helper = %v", err)
	}
	notReady := &fakeHelper{notReady: rt.ErrHelperUnavailable}
	if err := managedAdapter(t, notReady).Start(ctx, managedRuntime); !errors.Is(err, rt.ErrHelperUnavailable) || len(notReady.Requests()) != 0 {
		t.Fatalf("start without helper = %v", err)
	}
}

// TestCallHelperQueriesUnknownOutcomes: after a lost reply the ledger
// decides, and nothing it cannot confirm counts as done.
func TestCallHelperQueriesUnknownOutcomes(t *testing.T) {
	ctx := context.Background()
	lost := func(query func(orig deployhelper.Request, q deployhelper.Request) (deployhelper.Response, error)) *fakeHelper {
		var orig deployhelper.Request
		return &fakeHelper{Fake: deployhelper.Fake{Handle: func(req deployhelper.Request) (deployhelper.Response, error) {
			if req.Op != deployhelper.OpQuery {
				orig = req
				return deployhelper.Response{}, deployhelper.ErrOutcomeUnknown
			}
			if req.OperationID != orig.OperationID {
				return deployhelper.Response{}, errors.New("query for another operation")
			}
			return query(orig, req)
		}}}
	}
	recorded := func(state deployhelper.State, code deployhelper.Code) func(orig, q deployhelper.Request) (deployhelper.Response, error) {
		return func(orig, _ deployhelper.Request) (deployhelper.Response, error) {
			resp := reply(orig)
			resp.State, resp.Code, resp.Message = state, code, "recorded"
			if state == deployhelper.StateSucceeded {
				resp.Install = &deployhelper.InstallResult{ReleaseID: "20261101T120000Z-0000abcd"}
			}
			return resp, nil
		}
	}
	install := deployhelper.Request{Op: deployhelper.OpInstall, Service: "web", BuildID: "20261101T115900Z-0000ffff", Artifact: "dist"}

	call := func(h *fakeHelper) (deployhelper.Response, error) {
		req := install
		req.OperationID = rt.NewOperationID()
		return rt.CallHelper(ctx, h, req)
	}
	resp, err := call(lost(recorded(deployhelper.StateSucceeded, "")))
	if err != nil || resp.Install == nil || resp.Install.ReleaseID != "20261101T120000Z-0000abcd" {
		t.Fatalf("recorded success: %+v, %v", resp, err)
	}

	var he *rt.HelperError
	if _, err := call(lost(recorded(deployhelper.StateFailed, deployhelper.CodeFailed))); !errors.As(err, &he) || he.Unknown || he.Code != deployhelper.CodeFailed {
		t.Fatalf("recorded failure: %v", err)
	}
	for name, h := range map[string]*fakeHelper{
		"running":     lost(recorded(deployhelper.StateRunning, "")),
		"interrupted": lost(recorded(deployhelper.StateInterrupted, deployhelper.CodeFailed)),
		"query fails": lost(func(_, _ deployhelper.Request) (deployhelper.Response, error) {
			return deployhelper.Response{}, deployhelper.ErrOutcomeUnknown
		}),
		"another op": lost(func(_, q deployhelper.Request) (deployhelper.Response, error) {
			resp := reply(q)
			resp.Op, resp.State = deployhelper.OpBuild, deployhelper.StateSucceeded
			return resp, nil
		}),
	} {
		_, err := call(h)
		if !errors.As(err, &he) || !he.Unknown || !strings.Contains(err.Error(), "outcome unknown, query "+he.OperationID) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// No record: the request never reached the helper.
	none := lost(func(_, q deployhelper.Request) (deployhelper.Response, error) {
		resp := reply(q)
		resp.Code, resp.Message = deployhelper.CodeNotFound, "no operation is recorded"
		return resp, &deployhelper.Error{Code: resp.Code, Message: resp.Message}
	})
	if _, err := call(none); !errors.As(err, &he) || he.Unknown || !strings.Contains(err.Error(), "not carried out") {
		t.Fatalf("not recorded: %v", err)
	}

	// An invalid request is refused before it is sent.
	h := &fakeHelper{}
	bad := install
	bad.OperationID, bad.Artifact = rt.NewOperationID(), "../etc"
	if _, err := rt.CallHelper(ctx, h, bad); err == nil || len(h.Requests()) != 0 {
		t.Fatalf("invalid request: %v, %d requests", err, len(h.Requests()))
	}
}
