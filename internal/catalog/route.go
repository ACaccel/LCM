package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/deployhelper"
	"github.com/ACaccel/lcm/internal/ingress"
	"github.com/ACaccel/lcm/internal/message"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	rt "github.com/ACaccel/lcm/internal/runtime"
)

// The route section of a service document describes how the host's nginx
// site presents the service (internal/ingress). Documents carry no
// privileges: LCM sends the options to the deploy helper (ingress.apply),
// and the host configuration decides whether the service has a route at
// all. A refusal therefore never rejects a document write; it is recorded
// as the outcome of the route.
//
// The outcome lives on the service timeline as entries of KindRoute. An
// entry with state "pending" holds the operation ID and is written before
// the helper is called, so that the helper's ledger can settle a call whose
// reply was lost; the newest entry is the route's status. Timeline
// retention may remove old entries. The route is then sent once more,
// which is harmless: ingress.apply declares the whole state of a service's
// options and an unchanged site is not touched.

// Route states, the "state" of a KindRoute entry.
const (
	// RoutePending: the request was recorded and may have reached the helper.
	RoutePending = "pending"
	// RouteApplied: the helper confirmed that the site was applied.
	RouteApplied = "applied"
	// RouteRefused: the host does not grant what the options need, or
	// refused the change (for example the site is not installed).
	RouteRefused = "refused"
	// RouteFailed: the helper tried and the site was put back.
	RouteFailed = "failed"
	// RouteUnknown: the helper may or may not have applied the options.
	RouteUnknown = "unknown"
	// RouteSkipped: nothing was sent; the helper or the ingress component
	// is not available on this host.
	RouteSkipped = "skipped"
)

// maxRouteMessage bounds the helper's message kept in a timeline entry.
const maxRouteMessage = 1 << 10

// routeDigest identifies the options of a route: empty for a service
// without a route section.
func routeDigest(o *ingress.Options) string {
	if o == nil {
		return ""
	}
	raw, err := json.Marshal(o)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// routeEntry is the data of a KindRoute timeline entry.
type routeEntry struct {
	State       string
	Digest      string
	Message     string
	OperationID string
	SiteSHA256  string
	Changes     []string
	Dropped     []string
	Trigger     string
	Actor       string
	At          time.Time
}

func (e routeEntry) data() map[string]any {
	d := map[string]any{"state": e.State, "digest": e.Digest, "trigger": e.Trigger, "actor": e.Actor}
	if e.Message != "" {
		d["message"] = e.Message
	}
	if e.OperationID != "" {
		d["operationId"] = e.OperationID
	}
	if e.SiteSHA256 != "" {
		d["siteSha256"] = e.SiteSHA256
	}
	if len(e.Changes) > 0 {
		d["changes"] = e.Changes
	}
	if len(e.Dropped) > 0 {
		d["dropped"] = e.Dropped
	}
	return d
}

func (e routeEntry) summary() string {
	s := "route " + e.State
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

func routeEntryOf(e Entry) routeEntry {
	str := func(k string) string { s, _ := e.Data[k].(string); return s }
	out := routeEntry{
		State: str("state"), Digest: str("digest"), Message: str("message"), OperationID: str("operationId"),
		SiteSHA256: str("siteSha256"), Trigger: str("trigger"), Actor: str("actor"), At: e.Time,
	}
	strs := func(k string) []string {
		var list []string
		raw, _ := e.Data[k].([]any)
		for _, c := range raw {
			if s, ok := c.(string); ok {
				list = append(list, s)
			}
		}
		return list
	}
	out.Changes, out.Dropped = strs("changes"), strs("dropped")
	return out
}

// latestRoute returns the newest route entry of a service.
func (c *Catalog) latestRoute(ctx context.Context, name string) (routeEntry, bool, error) {
	page, err := c.Timeline(ctx, TimelineQuery{Resource: name, Kinds: []string{KindRoute}, Limit: 1})
	if err != nil || len(page.Entries) == 0 {
		return routeEntry{}, false, err
	}
	return routeEntryOf(page.Entries[0]), true, nil
}

func (c *Catalog) appendRoute(ctx context.Context, name string, e routeEntry) error {
	e.Message = headCap(e.Message, maxRouteMessage)
	return c.AppendTimeline(ctx, Entry{Resource: name, Kind: KindRoute, Summary: e.summary(), Data: e.data(), Ref: e.OperationID})
}

// RouteStatus is what the console shows of a service's route.
type RouteStatus struct {
	// Configured reports that the document has a route section.
	Configured bool `json:"configured"`
	// State is the state of the newest recorded outcome; empty when the
	// route was never sent.
	State string `json:"state"`
	// Current reports that the outcome concerns the route as the document
	// says now.
	Current     bool       `json:"current"`
	Message     string     `json:"message"`
	At          *time.Time `json:"at"`
	OperationID string     `json:"operationId"`
	SiteSHA256  string     `json:"siteSha256"`
	Changes     []string   `json:"changes"`
	// Dropped describes stored route options of services that no longer
	// fit the host configuration and were left out of the site.
	Dropped []string `json:"dropped"`
}

// RouteStatus describes the route of rec. It is nil for a service that has
// no route section and never had a recorded outcome.
func (m *Manager) RouteStatus(ctx context.Context, rec *Record) (*RouteStatus, error) {
	latest, found, err := m.cat.latestRoute(ctx, rec.Name)
	if err != nil {
		return nil, err
	}
	configured := rec.Config.Route != nil
	if !configured && !found {
		return nil, nil
	}
	st := &RouteStatus{Configured: configured, Changes: []string{}, Dropped: []string{}}
	if found {
		at := latest.At
		st.State, st.Message, st.At, st.OperationID, st.SiteSHA256 = latest.State, latest.Message, &at, latest.OperationID, latest.SiteSHA256
		st.Current = latest.Digest == routeDigest(rec.Config.Route)
		st.Changes = append(st.Changes, latest.Changes...)
		st.Dropped = append(st.Dropped, latest.Dropped...)
	}
	return st, nil
}

// routeFromError maps the failure of ingress.apply to a state and message.
func routeFromError(err error) (state, msg string) {
	var he *rt.HelperError
	switch {
	case errors.As(err, &he) && he.Unknown:
		return RouteUnknown, he.Error()
	case errors.As(err, &he) && he.Code == deployhelper.CodeDisabled:
		return RouteSkipped, "the ingress component is not enabled on this host (components.ingress in host.json)"
	case errors.As(err, &he) && he.Code == deployhelper.CodeRefused:
		return RouteRefused, he.Message
	case errors.As(err, &he):
		return RouteFailed, he.Message
	case errors.Is(err, rt.ErrHelperUnavailable):
		return RouteSkipped, helperSkipMessage(err)
	}
	return RouteFailed, err.Error()
}

// helperSkipMessage explains why the helper cannot be called.
func helperSkipMessage(err error) string {
	var ue *rt.UnavailableError
	if errors.As(err, &ue) {
		return "the deploy helper is not available: " + ue.Detail
	}
	return "the deploy helper is not available on this host; it is installed with components.ingress or components.deploy"
}

// pushRoute sends the route options of a service to the helper and records
// the outcome on the timeline; it audits only when audited is set (actions
// are audited by their caller). The returned error is a failure to record;
// the outcome of the call itself is the entry.
func (m *Manager) pushRoute(ctx context.Context, name string, o *ingress.Options, trigger string, audited bool) (routeEntry, error) {
	m.routeMu.Lock()
	defer m.routeMu.Unlock()
	return m.pushRouteLocked(ctx, name, o, trigger, audited)
}

// pushRouteLocked is pushRoute for a caller that holds routeMu.
func (m *Manager) pushRouteLocked(ctx context.Context, name string, o *ingress.Options, trigger string, audited bool) (routeEntry, error) {
	actor, err := requireActor(ctx)
	if err != nil {
		return routeEntry{}, err
	}
	entry := routeEntry{Digest: routeDigest(o), Trigger: trigger, Actor: actor.Name}
	finish := func(e routeEntry) (routeEntry, error) {
		fctx, cancel := detached(ctx)
		defer cancel()
		e.At = m.cat.now()
		if e.State == RouteSkipped {
			if latest, found, err := m.cat.latestRoute(fctx, name); err == nil && found && latest.State == e.State && latest.Digest == e.Digest && latest.Message == e.Message {
				return e, nil
			}
		}
		err := m.cat.appendRoute(fctx, name, e)
		if err == nil && audited {
			outcome := audit.OutcomeOK
			if e.State != RouteApplied {
				outcome = audit.OutcomeError
			}
			details := map[string]any{"state": e.State, "trigger": trigger, "digest": e.Digest}
			if e.OperationID != "" {
				details["operationId"] = e.OperationID
			}
			if err := m.cat.audit.Record(fctx, "service.apply-route", name, outcome, details); err != nil {
				m.log.Error("write audit entry failed", "service", name, "action", ActionApplyRoute, "error", err)
			}
		}
		return e, err
	}

	if err := m.helperReady(); err != nil {
		entry.State, entry.Message = RouteSkipped, helperSkipMessage(err)
		return finish(entry)
	}
	req := deployhelper.Request{Op: deployhelper.OpIngressApply, Service: name, OperationID: rt.NewOperationID(), Route: o}
	pending := entry
	pending.State, pending.OperationID, pending.At = RoutePending, req.OperationID, m.cat.now()
	if err := m.cat.appendRoute(ctx, name, pending); err != nil {
		return routeEntry{}, fmt.Errorf("record the route request: %w", err)
	}
	resp, callErr := m.callRoute(ctx, name, &req)
	entry.OperationID = req.OperationID
	switch {
	case callErr != nil:
		entry.State, entry.Message = routeFromError(callErr)
	case resp.IngressApply == nil:
		entry.State, entry.Message = RouteUnknown, "the deploy helper's reply has no result"
	default:
		entry.State, entry.SiteSHA256, entry.Changes = RouteApplied, resp.IngressApply.SHA256, resp.IngressApply.Changes
		entry.Dropped = resp.IngressApply.Dropped
		entry.Message = "the site is up to date"
		if len(entry.Changes) > 0 {
			entry.Message = strings.Join(entry.Changes, "; ")
		}
	}
	return finish(entry)
}

// routeBusyBackoff are the waits before the retries of an ingress.apply the
// helper answered "busy" to: another change of the nginx site (an
// administrator's apply, another route) held the site's lock. The helper
// has already waited for the lock itself, so the waits are short and the
// whole sequence stays bounded.
var routeBusyBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second}

// callRoute calls the helper and, while it answers "busy", calls it again
// after a backoff on the catalog's clock. A busy answer means nothing was
// changed, and the helper's ledger would answer the same operation ID with
// the recorded refusal, so every attempt takes a new one; req holds the
// last one used. Another failure, a cancelled context or the end of the
// backoff returns the last answer.
func (m *Manager) callRoute(ctx context.Context, name string, req *deployhelper.Request) (deployhelper.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := rt.CallHelper(ctx, m.deploy, *req)
		var he *rt.HelperError
		if !errors.As(err, &he) || he.Code != deployhelper.CodeBusy || attempt >= len(routeBusyBackoff) {
			return resp, err
		}
		wait := routeBusyBackoff[attempt]
		m.log.Info("the nginx site is busy; trying the route again", "service", name, "attempt", attempt+1, "wait", wait)
		if !m.cat.sleep(ctx, wait) {
			return resp, err
		}
		req.OperationID = rt.NewOperationID()
	}
}

// settlePending resolves a request that was recorded but whose outcome was
// never: LCM stopped between the call and its record. The helper's ledger
// decides; an unknown answer stays unknown and is never taken as success.
func (m *Manager) settlePending(ctx context.Context, name string, p routeEntry) (routeEntry, error) {
	done := p
	done.At = m.cat.now()
	done.Trigger = "recovery"
	resp, err := rt.CallHelper(ctx, m.deploy, deployhelper.Request{Op: deployhelper.OpQuery, OperationID: p.OperationID})
	var he *rt.HelperError
	switch {
	case m.deploy == nil || errors.Is(err, rt.ErrHelperUnavailable):
		done.State, done.Message = RouteUnknown, "LCM stopped while the route was being applied, and the deploy helper cannot be asked about operation "+p.OperationID
	case errors.As(err, &he) && he.Code == deployhelper.CodeNotFound:
		done.State, done.Message = RouteFailed, "LCM stopped before the route was sent: the helper has no record of operation "+p.OperationID
	case err != nil:
		done.State, done.Message = RouteUnknown, fmt.Sprintf("LCM stopped while the route was being applied; operation %s: %v", p.OperationID, err)
	case resp.State == deployhelper.StateSucceeded:
		done.State, done.Message = RouteApplied, "confirmed from the helper ledger after a restart of LCM"
		if r := resp.IngressApply; r != nil {
			done.SiteSHA256, done.Changes, done.Dropped = r.SHA256, r.Changes, r.Dropped
		}
	case resp.State == deployhelper.StateFailed:
		done.State, done.Message = RouteFailed, resp.Message
	default:
		done.State, done.Message = RouteUnknown, fmt.Sprintf("operation %s is %s in the helper ledger", p.OperationID, resp.State)
	}
	if err := m.cat.appendRoute(ctx, name, done); err != nil {
		return p, err
	}
	return done, nil
}

// SyncRoute sends the route of a service to the helper when the document's
// options differ from the last recorded outcome. retry also sends options
// whose last outcome was not "applied" (at startup, when the helper may
// have been installed since). A service that is no longer managed
// (unmanaged) has no options: those it last sent are removed from the
// host. It returns ErrActive-like gate errors unrecorded, for the caller
// to try again.
func (m *Manager) SyncRoute(ctx context.Context, name string, retry bool) error {
	ctx = withActor(ctx)
	ctx, release, err := m.cat.gate.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	// One apply at a time, also against the pending entry of an action
	// that is still waiting for the helper.
	m.routeMu.Lock()
	defer m.routeMu.Unlock()
	var route *ingress.Options
	rec, err := m.cat.Get(ctx, name)
	switch {
	case errors.Is(err, ErrNotFound):
		rec = nil
	case err != nil:
		return err
	default:
		route = rec.Config.Route
	}
	latest, found, err := m.cat.latestRoute(ctx, name)
	if err != nil {
		return err
	}
	if rec == nil && !found {
		return nil
	}
	if found && latest.State == RoutePending {
		if latest, err = m.settlePending(ctx, name, latest); err != nil {
			return err
		}
	}
	if rec == nil {
		// Only options the host may hold are removed, once.
		if latest.Digest == "" {
			return nil
		}
		_, err = m.pushRouteLocked(ctx, name, nil, "unmanaged", true)
		return err
	}
	want := routeDigest(route)
	have := ""
	if found {
		have = latest.Digest
	}
	switch {
	case have == want && (!found || latest.State == RouteApplied):
		return nil
	case have == want && !retry:
		return nil
	}
	trigger := "document"
	if retry {
		trigger = "startup"
	}
	_, err = m.pushRouteLocked(ctx, name, route, trigger, true)
	return err
}

// ApplyRoute sends the route of the service again, whatever was recorded.
// It fails when the route was not applied.
func (m *Manager) ApplyRoute(ctx context.Context, name string, opts ActionOptions) (ActionResult, error) {
	return m.exec(ctx, name, ActionApplyRoute, opts, func(ctx context.Context, s *session, res *ActionResult) error {
		e, err := m.pushRoute(ctx, s.name, s.cfg.Route, "action", false)
		if err != nil {
			return err
		}
		res.Output = e.Message
		if e.State != RouteApplied {
			return fmt.Errorf("the route was not applied (%s): %s", e.State, e.Message)
		}
		return nil
	})
}

// applyRouteStep is the dry-run step of apply-route.
func applyRouteStep(name string) message.Message {
	return message.New("step.applyRoute",
		"send the route options of "+name+" to the deploy helper, which renders and applies the nginx site", "service", name)
}

// RouteSync applies the routes of changed service documents in the
// background: the resource store's change callback only queues a name, and
// a loop (Drain) does the work, because an apply calls the root helper.
type RouteSync struct {
	m    *Manager
	log  *slog.Logger
	kick chan struct{}

	mu    sync.Mutex
	queue map[string]bool
}

// NewRouteSync returns a RouteSync over m.
func NewRouteSync(m *Manager) *RouteSync {
	return &RouteSync{m: m, log: m.log, kick: make(chan struct{}, 1), queue: map[string]bool{}}
}

// Kick is signalled when names are queued.
func (s *RouteSync) Kick() <-chan struct{} { return s.kick }

func (s *RouteSync) enqueue(name string, retry bool) {
	s.mu.Lock()
	s.queue[name] = s.queue[name] || retry
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// Changed queues a service whose document was created, saved or restored,
// or that was unmanaged. It implements the callback of
// resource.Store.Subscribe.
func (s *RouteSync) Changed(c resource.Change) {
	if c.Type != model.ResourceService {
		return
	}
	switch c.Change {
	case resource.ChangeCreated, resource.ChangeUpdated, resource.ChangeRestored, resource.ChangeUnmanaged:
		s.enqueue(c.Name, false)
	}
}

// EnqueueAll queues every service, retrying routes whose last outcome was
// not "applied".
func (s *RouteSync) EnqueueAll(ctx context.Context) error {
	recs, err := s.m.cat.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range recs {
		s.enqueue(r.Name, true)
	}
	return nil
}

// Drain syncs the queued services. A service that could not be synced now
// (maintenance is active) stays queued.
func (s *RouteSync) Drain(ctx context.Context) {
	s.mu.Lock()
	names := make([]string, 0, len(s.queue))
	for n := range s.queue {
		names = append(names, n)
	}
	retry := map[string]bool{}
	for _, n := range names {
		retry[n] = s.queue[n]
		delete(s.queue, n)
	}
	s.mu.Unlock()
	slices.Sort(names)
	for _, n := range names {
		if ctx.Err() != nil {
			s.requeue(names, retry, n)
			return
		}
		err := s.m.SyncRoute(ctx, n, retry[n])
		switch {
		case err == nil:
		case errors.Is(err, context.Canceled):
			s.requeue(names, retry, n)
			return
		default:
			s.log.Warn("apply route failed", "service", n, "error", err)
			s.mu.Lock()
			s.queue[n] = s.queue[n] || retry[n]
			s.mu.Unlock()
		}
	}
}

// requeue puts the services from name on back in the queue.
func (s *RouteSync) requeue(names []string, retry map[string]bool, from string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.Index(names, from)
	for _, n := range names[i:] {
		s.queue[n] = s.queue[n] || retry[n]
	}
}
