package runtime_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/model"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

var instancesRuntime = model.Runtime{Type: model.RuntimeManaged, Service: "web", Instances: []string{"a", "b"}}

func TestManagedInstances(t *testing.T) {
	ctx := context.Background()
	if got := rt.CapabilitiesOf(instancesRuntime); got != (rt.Capabilities{Start: true, Stop: true, Restart: true}) {
		t.Fatalf("capabilities = %+v", got)
	}
	h := &fakeHelper{Fake: deployhelper.Fake{Handle: func(req deployhelper.Request) (deployhelper.Response, error) {
		resp := reply(req)
		if req.Op == deployhelper.OpStatus {
			resp.Unit = &deployhelper.UnitStatus{LoadState: "loaded", ActiveState: "failed", Release: "20261101T120000Z-0000abcd"}
			resp.Instances = []deployhelper.InstanceStatus{
				{Name: "a", Port: 4000, ActiveState: "active", SubState: "running", UnitFileState: "enabled", MainPID: 7, Granted: true},
				{Name: "b", ActiveState: "failed", UnitFileState: "disabled", Granted: true},
				{Name: "old", ActiveState: "activating", UnitFileState: "enabled"},
			}
			return resp, nil
		}
		resp.State = deployhelper.StateSucceeded
		return resp, nil
	}}}
	a := managedAdapter(t, h)
	st, err := a.Status(ctx, instancesRuntime)
	if err != nil || st.State != rt.StateFailed || len(st.Instances) != 3 {
		t.Fatalf("status = %+v, %v", st, err)
	}
	want := []rt.InstanceStatus{
		{Name: "a", State: rt.StateActive, Enabled: true, SubState: "running", PID: 7, Port: 4000, Granted: true},
		{Name: "b", State: rt.StateFailed, Disabled: true, Granted: true},
		{Name: "old", State: rt.StateUnknown, Enabled: true},
	}
	for i := range want {
		if st.Instances[i] != want[i] {
			t.Errorf("instance %d = %+v, want %+v", i, st.Instances[i], want[i])
		}
	}

	ic, ok := a.(rt.InstanceController)
	if !ok {
		t.Fatal("the managed adapter controls no instances")
	}
	if err := ic.InstanceAction(ctx, instancesRuntime, rt.InstanceStop, "b"); err != nil {
		t.Fatal(err)
	}
	reqs := h.Requests()
	if last := reqs[len(reqs)-1]; last.Op != deployhelper.OpStop || last.Instance != "b" || last.Service != "web" {
		t.Fatalf("request = %+v", last)
	}
	if err := ic.InstanceAction(ctx, managedRuntime, rt.InstanceStop, "b"); !errors.Is(err, rt.ErrUnsupported) {
		t.Fatalf("instance action on a single service = %v", err)
	}
}

func TestOlderHelperHint(t *testing.T) {
	h := &fakeHelper{Fake: deployhelper.Fake{Handle: func(req deployhelper.Request) (deployhelper.Response, error) {
		resp := reply(req)
		resp.Code, resp.Message = deployhelper.CodeRefused, `unknown key "instances"`
		return resp, &deployhelper.Error{Code: resp.Code, Message: resp.Message}
	}}}
	_, err := rt.CallHelper(context.Background(), h, deployhelper.Request{Op: deployhelper.OpStatus, OperationID: rt.NewOperationID(), Service: "web", Instance: "a"})
	var he *rt.HelperError
	if !errors.As(err, &he) || !strings.Contains(he.Message, "older than this LCM: run sudo bin/lcm host apply") {
		t.Fatalf("err = %v", err)
	}
}
