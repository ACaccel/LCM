package catalog_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

const routeA = "  route:\n    proxy: [{ path: api/, upstream: /api/, bodyLimit: 3m }]\n"
const routeB = "  route:\n    proxy: [{ path: api/, upstream: /api/, bodyLimit: 8m }]\n"

func routeEnv(t *testing.T) (*env, *simHelper) {
	t.Helper()
	h := newSimHelper()
	return newEnv(t, withHelper(h)), h
}

func (h *simHelper) ingressCalls() []deployhelper.Request {
	var out []deployhelper.Request
	for _, r := range h.Requests() {
		if r.Op == deployhelper.OpIngressApply {
			out = append(out, r)
		}
	}
	return out
}

func routeStatus(t *testing.T, e *env, name string) *catalog.RouteStatus {
	t.Helper()
	rec, err := e.cat.Get(context.Background(), name)
	if err != nil {
		t.Fatal(err)
	}
	st, err := e.mgr.RouteStatus(context.Background(), rec)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func routeEntries(t *testing.T, e *env, name string) []catalog.Entry {
	t.Helper()
	page, err := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Resource: name, Kinds: []string{catalog.KindRoute}})
	if err != nil {
		t.Fatal(err)
	}
	return page.Entries
}

func TestRouteSyncAppliesChangedOptionsOnly(t *testing.T) {
	e, h := routeEnv(t)
	e.put(svcDoc("web", routeA), 0)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	calls := h.ingressCalls()
	if len(calls) != 1 || calls[0].Service != "web" || calls[0].Route == nil || calls[0].Route.Proxy[0].BodyLimit != "3m" {
		t.Fatalf("calls = %+v", calls)
	}
	st := routeStatus(t, e, "web")
	if st == nil || st.State != catalog.RouteApplied || !st.Current || !st.Configured || len(st.Changes) == 0 || st.OperationID != calls[0].OperationID {
		t.Fatalf("status = %+v", st)
	}
	// The pending record precedes the outcome.
	entries := routeEntries(t, e, "web")
	if len(entries) != 2 || entries[1].Data["state"] != catalog.RoutePending || entries[1].Data["operationId"] != calls[0].OperationID {
		t.Fatalf("entries = %+v", entries)
	}

	// Unchanged: nothing is sent, also when the document is saved again.
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	e.put(svcDoc("web", routeA+"  jobs: { x: { command: id } }\n"), 1)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	if n := len(h.ingressCalls()); n != 1 {
		t.Fatalf("calls = %d", n)
	}

	e.put(svcDoc("web", routeB), 2)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	calls = h.ingressCalls()
	if len(calls) != 2 || calls[1].Route.Proxy[0].BodyLimit != "8m" {
		t.Fatalf("calls = %+v", calls)
	}

	// Removing the section sends nil, so that the host drops the options.
	e.put(svcDoc("web", ""), 3)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	calls = h.ingressCalls()
	if len(calls) != 3 || calls[2].Route != nil {
		t.Fatalf("calls = %+v", calls)
	}
	if st := routeStatus(t, e, "web"); st == nil || st.Configured || !st.Current || st.State != catalog.RouteApplied {
		t.Fatalf("status = %+v", st)
	}
	if err := e.mgr.SyncRoute(e.ctx, "web", true); err != nil || len(h.ingressCalls()) != 3 {
		t.Fatalf("an applied removal is sent again: %v", err)
	}
}

func TestRouteSyncIgnoresServicesWithoutRoute(t *testing.T) {
	e, h := routeEnv(t)
	e.put(svcDoc("plain", ""), 0)
	if err := e.mgr.SyncRoute(e.ctx, "plain", true); err != nil {
		t.Fatal(err)
	}
	if len(h.Requests()) != 0 || routeStatus(t, e, "plain") != nil {
		t.Fatalf("requests = %v", h.ops())
	}
	if err := e.mgr.SyncRoute(e.ctx, "missing", true); err != nil {
		t.Fatal(err)
	}
}

func TestRouteRefusalIsRecordedNotRaised(t *testing.T) {
	e, h := routeEnv(t)
	h.refuse[deployhelper.OpIngressApply] = deployhelper.CodeRefused
	// The write succeeds: documents carry no privileges.
	e.put(svcDoc("web", routeA), 0)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	st := routeStatus(t, e, "web")
	if st.State != catalog.RouteRefused || !st.Current || !strings.Contains(st.Message, "refused by the simulation") {
		t.Fatalf("status = %+v", st)
	}
	// The same options are not sent again by a document change, but are at
	// startup and by the action.
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil || len(h.ingressCalls()) != 1 {
		t.Fatalf("calls = %d, %v", len(h.ingressCalls()), err)
	}
	delete(h.refuse, deployhelper.OpIngressApply)
	if err := e.mgr.SyncRoute(e.ctx, "web", true); err != nil {
		t.Fatal(err)
	}
	if st := routeStatus(t, e, "web"); st.State != catalog.RouteApplied || len(h.ingressCalls()) != 2 {
		t.Fatalf("status = %+v", st)
	}
	entries, _ := e.audit.List(context.Background(), 0, 0)
	if !slices.ContainsFunc(entries, func(a audit.Entry) bool { return a.Action == "service.apply-route" && a.Target == "web" }) {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestRouteFailureStates(t *testing.T) {
	cases := []struct {
		name    string
		code    deployhelper.Code
		message string
		state   string
	}{
		{"failed", deployhelper.CodeFailed, "", catalog.RouteFailed},
		{"component off", deployhelper.CodeDisabled, "components.ingress is not configured in /etc/lcm/host.json", catalog.RouteSkipped},
		{"refused with the same text", deployhelper.CodeRefused, "components.ingress is not configured in /etc/lcm/host.json", catalog.RouteRefused},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			e, h := routeEnv(t)
			h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
				if req.Op != deployhelper.OpIngressApply {
					return deployhelper.Response{}, nil, false
				}
				resp := helperReply(req)
				resp.State = deployhelper.StateFailed
				resp, err := refusal(resp, tt.code, tt.message+"x")
				if tt.message != "" {
					resp.Message, err = tt.message, &deployhelper.Error{Code: tt.code, Message: tt.message}
				}
				return resp, err, true
			}
			e.put(svcDoc("web", routeA), 0)
			if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
				t.Fatal(err)
			}
			if st := routeStatus(t, e, "web"); st.State != tt.state || st.Message == "" {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

// syncBusy runs SyncRoute while the helper answers "busy" to the first
// busyFor ingress.apply calls and returns the waits the manager asked for.
func syncBusy(t *testing.T, e *env, h *simHelper, busyFor int) []time.Duration {
	t.Helper()
	var calls atomic.Int32
	h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
		if req.Op != deployhelper.OpIngressApply || int(calls.Add(1)) > busyFor {
			return deployhelper.Response{}, nil, false
		}
		resp := helperReply(req)
		resp.State = deployhelper.StateFailed
		resp, err := refusal(resp, deployhelper.CodeBusy, "another change of the nginx site is running")
		return resp, err, true
	}
	done := make(chan error, 1)
	go func() { done <- e.mgr.SyncRoute(e.ctx, "web", false) }()
	var waits []time.Duration
	for {
		armed := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { armed <- e.clk.WaitForTimers(ctx, 1) }()
		select {
		case err := <-done:
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			return waits
		case err := <-armed:
			cancel()
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			cancel()
			t.Fatal("neither a backoff timer nor a result")
		}
		// Step until the backoff timer fires to learn how long it was. The
		// retry it wakes may arm the next timer before Pending is read, so
		// the attempt it starts also ends the step.
		start, attempts := e.clk.Now(), calls.Load()
		for e.clk.Pending() > 0 && calls.Load() == attempts {
			e.clk.Advance(time.Second)
		}
		waits = append(waits, e.clk.Now().Sub(start))
	}
}

func TestRouteRetriesWhileTheHelperIsBusy(t *testing.T) {
	e, h := routeEnv(t)
	e.put(svcDoc("web", routeA), 0)
	waits := syncBusy(t, e, h, 2)
	if !slices.Equal(waits, []time.Duration{5 * time.Second, 15 * time.Second}) {
		t.Errorf("waits = %v", waits)
	}
	st := routeStatus(t, e, "web")
	if st.State != catalog.RouteApplied || !st.Current {
		t.Fatalf("status = %+v", st)
	}
	calls := h.ingressCalls()
	if len(calls) != 3 {
		t.Fatalf("%d ingress.apply calls", len(calls))
	}
	ids := map[string]bool{}
	for _, c := range calls {
		ids[c.OperationID] = true
	}
	if len(ids) != 3 || st.OperationID != calls[2].OperationID {
		t.Errorf("each attempt needs its own operation ID: %v, status %s", ids, st.OperationID)
	}
	for _, en := range routeEntries(t, e, "web") {
		if en.Data["state"] == catalog.RouteFailed {
			t.Errorf("a busy attempt was recorded as a failure: %+v", en)
		}
	}
}

func TestRouteFailsOnlyAfterTheBusyRetries(t *testing.T) {
	e, h := routeEnv(t)
	e.put(svcDoc("web", routeA), 0)
	waits := syncBusy(t, e, h, 100)
	if !slices.Equal(waits, []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}) {
		t.Errorf("waits = %v", waits)
	}
	if n := len(h.ingressCalls()); n != 4 {
		t.Errorf("%d ingress.apply calls, want 4", n)
	}
	st := routeStatus(t, e, "web")
	if st.State != catalog.RouteFailed || !strings.Contains(st.Message, "another change of the nginx site") {
		t.Fatalf("status = %+v", st)
	}
}

func TestRouteRecordsDroppedOptions(t *testing.T) {
	e, h := routeEnv(t)
	dropped := "services.api: the stored route options no longer fit (no port); the route has its defaults until they are sent again"
	h.intercept = func(req deployhelper.Request) (deployhelper.Response, error, bool) {
		if req.Op != deployhelper.OpIngressApply {
			return deployhelper.Response{}, nil, false
		}
		resp := helperReply(req)
		resp.State = deployhelper.StateSucceeded
		resp.IngressApply = &deployhelper.IngressApplyResult{SHA256: strings.Repeat("a", 64), Changes: []string{}, Dropped: []string{dropped}}
		return resp, nil, true
	}
	e.put(svcDoc("web", routeA), 0)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	if st := routeStatus(t, e, "web"); st.State != catalog.RouteApplied || !slices.Equal(st.Dropped, []string{dropped}) {
		t.Fatalf("status = %+v", st)
	}
}

func TestRouteSkippedWithoutHelper(t *testing.T) {
	e, h := routeEnv(t)
	h.notReady = &rt.UnavailableError{Cause: rt.CauseNotInstalled, Detail: "/usr/local/libexec/lcm-deploy is missing"}
	e.put(svcDoc("web", routeA), 0)
	for range 3 {
		if err := e.mgr.SyncRoute(e.ctx, "web", true); err != nil {
			t.Fatal(err)
		}
	}
	st := routeStatus(t, e, "web")
	if st.State != catalog.RouteSkipped || !strings.Contains(st.Message, "lcm-deploy is missing") || len(h.Requests()) != 0 {
		t.Fatalf("status = %+v, requests %v", st, h.ops())
	}
	// A skip is written once, not at every attempt.
	if n := len(routeEntries(t, e, "web")); n != 1 {
		t.Fatalf("entries = %d", n)
	}
	// Once the helper exists the startup pass sends the route.
	h.notReady = nil
	if err := e.mgr.SyncRoute(e.ctx, "web", true); err != nil {
		t.Fatal(err)
	}
	if st := routeStatus(t, e, "web"); st.State != catalog.RouteApplied {
		t.Fatalf("status = %+v", st)
	}
}

func TestRouteSkippedWithoutHelperConfigured(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("web", routeA), 0)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	if st := routeStatus(t, e, "web"); st.State != catalog.RouteSkipped {
		t.Fatalf("status = %+v", st)
	}
}

func TestRouteLostReplyIsSettledFromTheLedger(t *testing.T) {
	e, h := routeEnv(t)
	h.lose[deployhelper.OpIngressApply] = true
	e.put(svcDoc("web", routeA), 0)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	if st := routeStatus(t, e, "web"); st.State != catalog.RouteApplied || len(st.Changes) == 0 {
		t.Fatalf("status = %+v", st)
	}

	// A helper that is still at work leaves the outcome unknown, never a success.
	h.lose = map[deployhelper.Op]bool{}
	h.hang[deployhelper.OpIngressApply] = true
	e.put(svcDoc("web", routeB), 1)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	st := routeStatus(t, e, "web")
	if st.State != catalog.RouteUnknown || !st.Current {
		t.Fatalf("status = %+v", st)
	}
}

func TestRoutePendingAfterRestartIsSettled(t *testing.T) {
	tests := []struct {
		name   string
		ledger func(h *simHelper, id string)
		state  string
		calls  int
	}{
		{"succeeded", func(h *simHelper, id string) {
			h.ledger[id] = deployhelper.Response{Version: deployhelper.Version, Op: deployhelper.OpIngressApply, OperationID: id,
				State: deployhelper.StateSucceeded, IngressApply: &deployhelper.IngressApplyResult{SHA256: strings.Repeat("a", 64), Changes: []string{"reload nginx"}}}
		}, catalog.RouteApplied, 0},
		{"failed", func(h *simHelper, id string) {
			h.ledger[id] = deployhelper.Response{Version: deployhelper.Version, Op: deployhelper.OpIngressApply, OperationID: id,
				State: deployhelper.StateFailed, Code: deployhelper.CodeFailed, Message: "nginx -t failed"}
		}, catalog.RouteFailed, 1},
		{"unrecorded", func(*simHelper, string) {}, catalog.RouteFailed, 1},
		{"running", func(h *simHelper, id string) {
			h.ledger[id] = deployhelper.Response{Version: deployhelper.Version, Op: deployhelper.OpIngressApply, OperationID: id, State: deployhelper.StateRunning}
		}, catalog.RouteUnknown, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, h := routeEnv(t)
			e.put(svcDoc("web", routeA), 0)
			// The first pass records the digest; then LCM stops after writing
			// another request, before its outcome.
			if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
				t.Fatal(err)
			}
			id := rt.NewOperationID()
			digest := routeEntries(t, e, "web")[0].Data["digest"]
			if err := e.cat.AppendTimeline(e.ctx, catalog.Entry{Resource: "web", Kind: catalog.KindRoute, Summary: "route pending",
				Data: map[string]any{"state": catalog.RoutePending, "digest": digest, "operationId": id}}); err != nil {
				t.Fatal(err)
			}
			before := len(h.ingressCalls())
			tt.ledger(h, id)
			if err := e.mgr.SyncRoute(e.ctx, "web", true); err != nil {
				t.Fatal(err)
			}
			if got := len(h.ingressCalls()) - before; got != tt.calls {
				t.Fatalf("calls = %d, want %d", got, tt.calls)
			}
			var settled string
			for _, en := range routeEntries(t, e, "web") {
				if en.Data["trigger"] == "recovery" {
					settled, _ = en.Data["state"].(string)
				}
			}
			if settled != tt.state {
				t.Fatalf("settled = %q, want %q", settled, tt.state)
			}
			// Whatever the ledger said, the route ends up applied.
			if st := routeStatus(t, e, "web"); st.State != catalog.RouteApplied && tt.state != catalog.RouteUnknown {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

func TestApplyRouteAction(t *testing.T) {
	e, h := routeEnv(t)
	e.put(svcDoc("web", routeA), 0)
	e.put(svcDoc("plain", ""), 0)

	availability := func(name string) catalog.Availability {
		rec, err := e.cat.Get(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range catalog.AvailableActions(rec, nil) {
			if a.Action == catalog.ActionApplyRoute {
				return a
			}
		}
		t.Fatal("apply-route not listed")
		return catalog.Availability{}
	}
	if a := availability("web"); !a.Allowed {
		t.Fatalf("web: %+v", a)
	}
	if a := availability("plain"); a.Allowed || a.Reason != catalog.ReasonNotConfigured {
		t.Fatalf("plain: %+v", a)
	}

	plan, err := e.mgr.Plan(e.ctx, "web", catalog.ActionApplyRoute, catalog.ActionOptions{})
	if err != nil || len(plan.Steps) != 2 {
		t.Fatalf("plan = %+v, %v", plan, err)
	}

	res, err := e.mgr.Do(e.ctx, "web", catalog.ActionApplyRoute, catalog.ActionOptions{})
	if err != nil || res.Action != catalog.ActionApplyRoute || res.Output == "" {
		t.Fatalf("result = %+v, %v", res, err)
	}
	// Explicit requests always send.
	if _, err := e.mgr.Do(e.ctx, "web", catalog.ActionApplyRoute, catalog.ActionOptions{}); err != nil || len(h.ingressCalls()) != 2 {
		t.Fatalf("calls = %d, %v", len(h.ingressCalls()), err)
	}

	h.refuse[deployhelper.OpIngressApply] = deployhelper.CodeRefused
	_, err = e.mgr.Do(e.ctx, "web", catalog.ActionApplyRoute, catalog.ActionOptions{})
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("err = %v", err)
	}
	if st := routeStatus(t, e, "web"); st.State != catalog.RouteRefused {
		t.Fatalf("status = %+v", st)
	}

	// Without a helper the action is not offered and is refused.
	h.notReady = &rt.UnavailableError{Cause: rt.CauseNotInstalled, Detail: "missing"}
	rec, _ := e.cat.Get(context.Background(), "web")
	for _, a := range catalog.AvailableActions(rec, h.notReady) {
		if a.Action == catalog.ActionApplyRoute && (a.Allowed || a.Reason != catalog.ReasonHelperUnavailable) {
			t.Fatalf("availability = %+v", a)
		}
	}
	if _, err := e.mgr.Do(e.ctx, "web", catalog.ActionApplyRoute, catalog.ActionOptions{}); !errors.Is(err, catalog.ErrHelperUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestRouteSyncQueue(t *testing.T) {
	e, h := routeEnv(t)
	sync := catalog.NewRouteSync(e.mgr)
	e.res.Subscribe(sync.Changed)
	e.put(svcDoc("web", routeA), 0)
	e.put(svcDoc("plain", ""), 0)
	if err := e.res.Flush(e.ctx); err != nil {
		t.Fatal(err)
	}
	sync.Drain(e.ctx)
	if calls := h.ingressCalls(); len(calls) != 1 || calls[0].Service != "web" {
		t.Fatalf("calls = %+v", calls)
	}
	// Nothing is queued any more.
	sync.Drain(e.ctx)
	if len(h.ingressCalls()) != 1 {
		t.Fatal("drained twice")
	}

	// Startup retries the services whose last outcome was not applied.
	h.refuse[deployhelper.OpIngressApply] = deployhelper.CodeRefused
	e.put(svcDoc("web", routeB), 1)
	if err := e.res.Flush(e.ctx); err != nil {
		t.Fatal(err)
	}
	sync.Drain(e.ctx)
	delete(h.refuse, deployhelper.OpIngressApply)
	if err := sync.EnqueueAll(e.ctx); err != nil {
		t.Fatal(err)
	}
	sync.Drain(e.ctx)
	if st := routeStatus(t, e, "web"); st.State != catalog.RouteApplied {
		t.Fatalf("status = %+v", st)
	}
}

// TestRouteSyncRemovesTheOptionsOfAnUnmanagedService: unmanaging a service
// removes the route options it last sent from the host, once.
func TestRouteSyncRemovesTheOptionsOfAnUnmanagedService(t *testing.T) {
	e, h := routeEnv(t)
	e.put(svcDoc("web", routeA), 0)
	e.put(svcDoc("plain", ""), 0)
	if err := e.mgr.SyncRoute(e.ctx, "web", false); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"web", "plain"} {
		for _, to := range []string{resource.PhaseRetired, resource.PhaseUnmanaged} {
			if err := e.phase(e.ctx, name, to); err != nil {
				t.Fatal(err)
			}
		}
	}
	sync := catalog.NewRouteSync(e.mgr)
	for _, name := range []string{"web", "plain"} {
		sync.Changed(resource.Change{Name: name, Type: "service", Change: resource.ChangeUnmanaged})
	}
	sync.Drain(e.ctx)
	calls := h.ingressCalls()
	if len(calls) != 2 || calls[1].Service != "web" || calls[1].Route != nil {
		t.Fatalf("calls = %+v", calls)
	}
	entries := routeEntries(t, e, "web")
	if entries[0].Data["state"] != catalog.RouteApplied || entries[0].Data["trigger"] != "unmanaged" || entries[0].Data["digest"] != "" {
		t.Fatalf("entry = %+v", entries[0])
	}
	// Once removed, nothing more is sent.
	if err := e.mgr.SyncRoute(e.ctx, "web", true); err != nil || len(h.ingressCalls()) != 2 {
		t.Fatalf("sent again: %v %d", err, len(h.ingressCalls()))
	}
}
