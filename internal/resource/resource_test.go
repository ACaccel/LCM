package resource_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/maintenance"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/resource"
	"github.com/ACaccel/lcm/internal/store"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

var alice = auth.Actor{Kind: auth.KindUser, ID: "1", Name: "alice"}

type harness struct {
	db    *store.DB
	store *resource.Store
	audit *audit.Log
	clk   *clock.Fake
	ctx   context.Context

	mu      sync.Mutex
	changes []resource.Change
}

// ownedGuard refuses to unmanage the names it lists, reading them from a
// table inside the phase change transaction.
type ownedGuard struct{}

func (ownedGuard) CheckPhase(ctx context.Context, tx *sql.Tx, rec resource.Record, to string) error {
	if to != resource.PhaseUnmanaged {
		return nil
	}
	var owned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM config_ownership WHERE kind = 'Resource' AND name = ?`, rec.Name).Scan(&owned); err != nil {
		return err
	}
	if owned > 0 {
		return fmt.Errorf("%w: %s is owned", resource.ErrPrecondition, rec.Name)
	}
	return nil
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{db: storetest.Open(t), clk: clock.NewFake(time.UnixMilli(1_700_000_000_000)), ctx: auth.WithActor(context.Background(), alice)}
	h.audit = audit.New(h.db)
	s, err := resource.New(resource.Config{
		DB: h.db, Gate: maintenance.New(h.db), Audit: h.audit, Clock: h.clk, Phases: ownedGuard{},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.Subscribe(func(c resource.Change) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.changes = append(h.changes, c)
	})
	t.Cleanup(s.Close)
	h.store = s
	return h
}

// flushed returns the changes delivered so far, after every change
// committed before the call.
func (h *harness) flushed(t *testing.T) []resource.Change {
	t.Helper()
	if err := h.store.Flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.changes)
}

func service(name, adapter, extra string) []byte {
	return fmt.Appendf(nil, "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: %s, labels: { tier: web } }\nspec:\n  type: service\n  adapter: %s\n%s", name, adapter, extra)
}

func machine(name string) []byte {
	return fmt.Appendf(nil, "apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: %s }\nspec: { type: machine, adapter: local }\n", name)
}

func dependsOn(targets ...string) string {
	var b strings.Builder
	b.WriteString("  relations:\n")
	for _, t := range targets {
		fmt.Fprintf(&b, "    - { type: dependsOn, target: %s }\n", t)
	}
	return b.String()
}

func (h *harness) put(t *testing.T, data []byte, expected int) resource.PutResult {
	t.Helper()
	res, err := h.store.Put(h.ctx, data, expected)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPutVersionsAndGet(t *testing.T) {
	h := newHarness(t)
	res := h.put(t, service("web", "external", ""), 0)
	if !res.Created || !res.Changed || res.Version != 1 {
		t.Fatalf("create = %+v", res)
	}
	if res := h.put(t, service("web", "external", ""), 1); res.Changed || res.Version != 1 {
		t.Fatalf("unchanged save = %+v", res)
	}
	h.clk.Advance(time.Second)
	res = h.put(t, service("web", "external", "  checks: [{ name: tcp, type: tcp }]\n"), 1)
	if res.Created || !res.Changed || res.Version != 2 {
		t.Fatalf("update = %+v", res)
	}

	rec, err := h.store.Get(h.ctx, "web")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Type != "service" || rec.Adapter != "external" || rec.Phase != resource.PhaseManaged || rec.Version != 2 || len(rec.ID) != 36 ||
		len(rec.Document.Spec.Checks) != 1 || !rec.UpdatedAt.After(rec.CreatedAt) {
		t.Fatalf("record = %+v", rec)
	}
	byID, err := h.store.Get(h.ctx, "@"+rec.ID)
	if err != nil || byID.Name != "web" {
		t.Fatalf("by id = %+v %v", byID, err)
	}

	vs, err := h.store.Versions(h.ctx, "web")
	if err != nil || len(vs) != 2 || vs[0].Version != 2 || vs[0].Actor != alice {
		t.Fatalf("versions = %+v %v", vs, err)
	}
	doc, v, err := h.store.GetVersion(h.ctx, "web", 1)
	if err != nil || len(doc.Spec.Checks) != 0 || v.Version != 1 {
		t.Fatalf("version 1 = %+v %+v %v", doc, v, err)
	}
	if _, _, err := h.store.GetVersion(h.ctx, "web", 9); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("missing version: %v", err)
	}

	res, err = h.store.Restore(h.ctx, "web", 1, 2)
	if err != nil || res.Version != 3 {
		t.Fatalf("restore = %+v %v", res, err)
	}
	vs, _ = h.store.Versions(h.ctx, "web")
	if vs[0].RestoredFrom != 1 {
		t.Fatalf("restored version = %+v", vs[0])
	}

	want := []string{"created", "updated", "restored"}
	var got []string
	for _, c := range h.flushed(t) {
		if c.Name != "web" || c.ID != rec.ID || c.Type != "service" {
			t.Fatalf("change = %+v", c)
		}
		got = append(got, c.Change)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("changes = %v", got)
	}
	entries, err := h.audit.List(h.ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, e := range entries {
		actions = append(actions, e.Action)
	}
	if !slices.Equal(actions, []string{"resource.restore", "resource.save", "resource.save"}) {
		t.Fatalf("audit = %v", actions)
	}
}

func TestPutErrors(t *testing.T) {
	h := newHarness(t)
	h.put(t, service("web", "external", ""), 0)
	cases := []struct {
		name     string
		data     []byte
		expected int
		want     error
	}{
		{"exists", service("web", "external", ""), 0, resource.ErrExists},
		{"missing", service("db", "external", ""), 1, resource.ErrNotFound},
		{"conflict", service("web", "external", "  checks: [{ name: a, type: tcp }]\n"), 5, resource.ErrConflict},
		{"adapter change", service("web", "command", "  config: { start: run }\n"), 1, resource.ErrPrecondition},
		{"invalid", service("web", "systemd", ""), 1, resource.ErrInvalid},
		{"v1", []byte("apiVersion: lcm/v1\nkind: Service\nmetadata: { name: web }\n"), 1, resource.ErrInvalid},
		{"negative", service("web", "external", ""), -1, resource.ErrPrecondition},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := h.store.Put(h.ctx, tt.data, tt.expected)
			if !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	_, err := h.store.Put(h.ctx, service("web", "systemd", ""), 1)
	var is model.Issues
	if !errors.As(err, &is) || is[0].Path != "spec.config.unit" {
		t.Fatalf("issues = %v", err)
	}
	if _, err := h.store.Put(context.Background(), service("db", "external", ""), 0); !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("no actor: %v", err)
	}
}

func TestRelationsUnresolvedAndCycles(t *testing.T) {
	h := newHarness(t)
	res := h.put(t, service("web", "external", dependsOn("db", "cache")), 0)
	if !slices.Equal(res.Unresolved, []string{"cache", "db"}) {
		t.Fatalf("unresolved = %v", res.Unresolved)
	}
	res = h.put(t, service("db", "external", dependsOn("disk")), 0)
	if !slices.Equal(res.Unresolved, []string{"disk"}) {
		t.Fatalf("unresolved = %v", res.Unresolved)
	}
	// disk -> web closes web -> db -> disk -> web.
	_, err := h.store.Put(h.ctx, service("disk", "external", dependsOn("web")), 0)
	var is model.Issues
	if !errors.Is(err, resource.ErrInvalid) || !errors.As(err, &is) || is[0].Path != "spec.relations[0]" ||
		!strings.Contains(is[0].Message, "disk -> web -> db -> disk") {
		t.Fatalf("cycle: %v", err)
	}
	// A control path may lead back.
	h.put(t, service("disk", "external", "  relations: [{ type: controlPath, target: web }]\n"), 0)
	rels, err := h.store.Relations(h.ctx)
	if err != nil || len(rels) != 3 || len(rels["web"]) != 2 || rels["disk"][0].Type != model.RelationControlPath {
		t.Fatalf("relations = %v %v", rels, err)
	}
	// runsOn takes part in cycle detection too.
	h.put(t, machine("host"), 0)
	h.put(t, service("cache", "external", "  relations: [{ type: runsOn, target: host }]\n"), 0)
	_, err = h.store.Put(h.ctx, []byte("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: host }\nspec: { type: machine, adapter: local, relations: [{ type: dependsOn, target: web }] }\n"), 1)
	if !errors.Is(err, resource.ErrInvalid) || !strings.Contains(err.Error(), "host -> web -> cache -> host") {
		t.Fatalf("runsOn cycle: %v", err)
	}
}

func TestListFiltersAndLocalMachine(t *testing.T) {
	h := newHarness(t)
	h.put(t, service("web", "external", ""), 0)
	h.put(t, []byte("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: api, labels: { tier: api } }\nspec: { type: service, adapter: external }\n"), 0)
	h.put(t, machine("ws"), 0)
	if _, err := h.store.Put(h.ctx, machine("ws2"), 0); !errors.Is(err, resource.ErrExists) || !strings.Contains(err.Error(), "already registered as ws") {
		t.Fatalf("second local machine: %v", err)
	}
	names := func(f resource.Filter) []string {
		t.Helper()
		recs, err := h.store.List(h.ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, r := range recs {
			out = append(out, r.Name)
		}
		return out
	}
	if got := names(resource.Filter{}); !slices.Equal(got, []string{"api", "web", "ws"}) {
		t.Fatalf("all = %v", got)
	}
	if got := names(resource.Filter{Type: "machine"}); !slices.Equal(got, []string{"ws"}) {
		t.Fatalf("machines = %v", got)
	}
	if got := names(resource.Filter{Labels: map[string]string{"tier": "web"}}); !slices.Equal(got, []string{"web"}) {
		t.Fatalf("labels = %v", got)
	}
	if err := h.store.SetPhase(h.ctx, "api", resource.PhaseRetired, 1, "replaced"); err != nil {
		t.Fatal(err)
	}
	if got := names(resource.Filter{}); !slices.Equal(got, []string{"web", "ws"}) {
		t.Fatalf("managed = %v", got)
	}
	if got := names(resource.Filter{IncludeRetired: true}); !slices.Equal(got, []string{"api", "web", "ws"}) {
		t.Fatalf("with retired = %v", got)
	}
}

func TestPhaseLifecycleAndIncarnations(t *testing.T) {
	h := newHarness(t)
	h.put(t, service("web", "external", ""), 0)
	h.put(t, service("web", "external", "  checks: [{ name: a, type: tcp }]\n"), 1)
	first, _ := h.store.Get(h.ctx, "web")

	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseUnmanaged, 2, ""); !errors.Is(err, resource.ErrPrecondition) {
		t.Fatalf("unmanage while managed: %v", err)
	}
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseRetired, 1, ""); !errors.Is(err, resource.ErrConflict) {
		t.Fatalf("stale version: %v", err)
	}
	if err := h.store.SetPhase(h.ctx, "web", "gone", 2, ""); !errors.Is(err, resource.ErrPrecondition) {
		t.Fatalf("unknown phase: %v", err)
	}
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseRetired, 2, "maintenance window"); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseManaged, 2, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseRetired, 2, ""); err != nil {
		t.Fatal(err)
	}

	// The phase guard can refuse a change inside its transaction.
	if err := h.db.Write(h.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO config_ownership (kind, name, source, path, commit_id, applied_version, document, applied_at) VALUES ('Resource', 'web', 'repo', 'web.yaml', 'c', 2, '{}', 1)`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseUnmanaged, 2, ""); !errors.Is(err, resource.ErrPrecondition) || !strings.Contains(err.Error(), "web is owned") {
		t.Fatalf("owned: %v", err)
	}
	if err := h.db.Write(h.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM config_ownership`)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseUnmanaged, 2, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Get(h.ctx, "web"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("unmanaged name still active: %v", err)
	}
	if _, err := h.store.Restore(h.ctx, "@"+first.ID, 1, 2); !errors.Is(err, resource.ErrPrecondition) {
		t.Fatalf("restore archive: %v", err)
	}

	// The name is free again: a new incarnation starts at version 1.
	res := h.put(t, service("web", "command", "  config: { start: run }\n"), 0)
	if !res.Created || res.Version != 1 {
		t.Fatalf("recreate = %+v", res)
	}
	second, _ := h.store.Get(h.ctx, "web")
	if second.ID == first.ID || second.Adapter != "command" {
		t.Fatalf("second incarnation = %+v", second)
	}
	archives, err := h.store.Archives(h.ctx, "service")
	if err != nil || len(archives) != 1 || archives[0].ID != first.ID || archives[0].Phase != resource.PhaseUnmanaged || archives[0].Version != 2 {
		t.Fatalf("archives = %+v %v", archives, err)
	}
	if vs, err := h.store.Versions(h.ctx, "@"+first.ID); err != nil || len(vs) != 2 {
		t.Fatalf("archived versions = %v %v", vs, err)
	}
	if a, _ := h.store.Archives(h.ctx, "machine"); len(a) != 0 {
		t.Fatalf("machine archives = %v", a)
	}

	var changes []string
	for _, c := range h.flushed(t) {
		changes = append(changes, c.Change)
	}
	if !slices.Equal(changes, []string{"created", "updated", "phase", "phase", "phase", "unmanaged", "created"}) {
		t.Fatalf("changes = %v", changes)
	}
	recent, _ := h.audit.List(h.ctx, 10, 0)
	var phaseEntries int
	for _, e := range recent {
		if e.Action == "resource.phase" {
			phaseEntries++
			if e.Details["id"] != first.ID {
				t.Fatalf("phase audit = %+v", e)
			}
		}
	}
	if phaseEntries != 4 {
		t.Fatalf("phase audit entries = %d", phaseEntries)
	}
}

type recordingHooks struct {
	saves  []string
	phases []string
	refuse error
}

func (r *recordingHooks) BeforeSave(_ context.Context, _ *sql.Tx, s resource.Save) error {
	oldVersion := 0
	if s.Old != nil {
		oldVersion = s.Old.Version
	}
	r.saves = append(r.saves, fmt.Sprintf("%s %d->%d from %d by %s", s.Next.Name, oldVersion, s.Next.Version, s.RestoredFrom, s.Actor.ID))
	return r.refuse
}

func (r *recordingHooks) BeforePhase(_ context.Context, _ *sql.Tx, p resource.PhaseChange) error {
	r.phases = append(r.phases, p.Record.Phase+"->"+p.To+" "+p.Reason)
	return r.refuse
}

func TestHooks(t *testing.T) {
	h := newHarness(t)
	hooks := &recordingHooks{}
	h.store.RegisterHooks("service", hooks)
	h.put(t, service("web", "external", ""), 0)
	h.put(t, service("web", "external", "  checks: [{ name: a, type: tcp }]\n"), 1)
	h.put(t, machine("ws"), 0)
	if _, err := h.store.Restore(h.ctx, "web", 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseRetired, 3, "done"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(hooks.saves, []string{"web 0->1 from 0 by 1", "web 1->2 from 0 by 1", "web 2->3 from 1 by 1"}) ||
		!slices.Equal(hooks.phases, []string{"managed->retired done"}) {
		t.Fatalf("hooks = %v %v", hooks.saves, hooks.phases)
	}

	hooks.refuse = errors.New("busy")
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseManaged, 3, ""); err == nil || err.Error() != "busy" {
		t.Fatalf("refused phase: %v", err)
	}
	if _, err := h.store.Put(h.ctx, service("web", "external", "  checks: [{ name: b, type: tcp }]\n"), 3); err == nil {
		t.Fatal("refused save was stored")
	}
	rec, _ := h.store.Get(h.ctx, "web")
	if rec.Version != 3 || rec.Phase != resource.PhaseRetired {
		t.Fatalf("record after refusals = %+v", rec)
	}
}

type rejectTCP struct{}

func (rejectTCP) ValidateChecks(checks []model.Check, path string) model.Issues {
	var is model.Issues
	for i, c := range checks {
		if c.Type == "tcp" {
			is = append(is, model.Issue{Path: fmt.Sprintf("%s[%d].type", path, i), Message: "unknown check type"})
		}
	}
	return is
}

func TestCheckValidator(t *testing.T) {
	db := storetest.Open(t)
	s, err := resource.New(resource.Config{DB: db, Audit: audit.New(db), Checks: rejectTCP{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithActor(context.Background(), alice)
	_, err = s.Put(ctx, machine("ws"), 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Put(ctx, service("web", "external", "  checks: [{ name: a, type: tcp }]\n"), 0)
	var is model.Issues
	if !errors.Is(err, resource.ErrInvalid) || !errors.As(err, &is) || is[0].Path != "spec.checks[0].type" {
		t.Fatalf("invalid check: %v", err)
	}
}

func TestMaintenanceGateBlocksWrites(t *testing.T) {
	h := newHarness(t)
	h.put(t, machine("ws"), 0)
	ws, _ := h.store.Get(h.ctx, "ws")
	if err := h.db.Write(h.ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO machine_maintenance (host, resource_id, state, entered_at, body) VALUES ('local', ?, 'active', 1, '{}')`, ws.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.Put(h.ctx, service("web", "external", ""), 0); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("put during maintenance: %v", err)
	}
	if err := h.store.SetPhase(h.ctx, "ws", resource.PhaseRetired, 1, ""); !errors.Is(err, maintenance.ErrActive) {
		t.Fatalf("phase during maintenance: %v", err)
	}
}

func TestPutDocument(t *testing.T) {
	h := newHarness(t)
	doc := &model.Resource{
		Header: model.Header{APIVersion: model.APIVersion, Kind: model.KindResource, Metadata: model.Metadata{Name: "router"}},
		Spec:   model.ResourceSpec{Type: model.ResourceRouter, Adapter: model.AdapterXiaomi, Connection: "home", Config: &model.RouterConfig{}},
	}
	res, err := h.store.PutDocument(h.ctx, doc, 0)
	if err != nil || !res.Created {
		t.Fatalf("put = %+v %v", res, err)
	}
	rec, _ := h.store.Get(h.ctx, "router")
	cfg, ok := rec.Document.Router()
	if !ok || cfg.Interval.D() != time.Minute {
		t.Fatalf("stored router = %+v", rec.Document.Spec)
	}
	// The stored text is the canonical encoding, so an equal save is a no-op.
	if res, err := h.store.PutDocument(h.ctx, rec.Document, 1); err != nil || res.Changed {
		t.Fatalf("resave = %+v %v", res, err)
	}
}

func names(as []resource.Affected) []string {
	out := []string{}
	for _, a := range as {
		out = append(out, fmt.Sprintf("%s:%s:%s:%d", a.Name, a.Relation, a.Via, a.Depth))
	}
	return out
}

func TestImpactTransitiveAndControlPath(t *testing.T) {
	h := newHarness(t)
	// router <- host (controlPath); db runsOn host; api dependsOn db;
	// web dependsOn api and db; tool controls through api; old (retired)
	// depends on db and is ignored.
	h.put(t, []byte("apiVersion: lcm/v2\nkind: Resource\nmetadata: { name: router }\nspec: { type: machine, adapter: local }\n"), 0)
	h.put(t, service("host", "external", "  relations: [{ type: controlPath, target: router }]\n"), 0)
	h.put(t, service("db", "external", "  relations: [{ type: runsOn, target: host }, { type: dependsOn, target: ghost }]\n"), 0)
	h.put(t, service("api", "external", dependsOn("db")), 0)
	h.put(t, service("web", "external", dependsOn("api", "db")), 0)
	h.put(t, service("tool", "external", "  relations: [{ type: controlPath, target: api }]\n"), 0)
	h.put(t, service("old", "external", dependsOn("db")), 0)
	if err := h.store.SetPhase(h.ctx, "old", resource.PhaseRetired, 1, ""); err != nil {
		t.Fatal(err)
	}

	imp, err := h.store.Impact(h.ctx, "host")
	if err != nil {
		t.Fatal(err)
	}
	if got := names(imp.Dependents); !slices.Equal(got, []string{"db:runsOn:host:1", "api:dependsOn:db:2", "web:dependsOn:db:2"}) {
		t.Errorf("dependents = %v", got)
	}
	if got := names(imp.ControlDependents); !slices.Equal(got, []string{"tool:controlPath:api:3"}) {
		t.Errorf("control dependents = %v", got)
	}
	if imp.Dependents[0].Type != model.ResourceService {
		t.Errorf("type = %q", imp.Dependents[0].Type)
	}

	imp, err = h.store.Impact(h.ctx, "router")
	if err != nil || imp.Empty() || len(imp.Dependents) != 0 || names(imp.ControlDependents)[0] != "host:controlPath:router:1" {
		t.Fatalf("router impact = %+v %v", imp, err)
	}
	imp, err = h.store.Impact(h.ctx, "db")
	if err != nil || !slices.Equal(imp.Unresolved, []string{"ghost"}) {
		t.Fatalf("unresolved = %+v %v", imp, err)
	}
	if imp, _ := h.store.Impact(h.ctx, "web"); !imp.Empty() {
		t.Errorf("leaf impact = %+v", imp)
	}
	if _, err := h.store.Impact(h.ctx, "missing"); !errors.Is(err, resource.ErrNotFound) {
		t.Errorf("missing: %v", err)
	}

	order, err := h.store.DependencyOrder(h.ctx, []string{"web", "tool", "api", "host"})
	if err != nil || !slices.Equal(order, []string{"host", "api", "tool", "web"}) {
		t.Errorf("order = %v %v", order, err)
	}
}

func TestCheckImpactGuard(t *testing.T) {
	h := newHarness(t)
	h.put(t, service("db", "external", ""), 0)
	h.put(t, service("web", "external", dependsOn("db")), 0)
	stop := resource.Action{Name: "stop", Risk: resource.RiskDisruptive, Kind: resource.KindAction}
	start := resource.Action{Name: "start", Risk: resource.RiskReversible, Kind: resource.KindAction}
	retire := resource.Action{Name: "retire", Risk: resource.RiskDisruptive, Kind: resource.KindPhase}
	unmanage := resource.Action{Name: "unmanage", Risk: resource.RiskDestructive, Kind: resource.KindPhase}
	shutdown := resource.Action{Name: "graceful-shutdown", Risk: resource.RiskDestructive, Kind: resource.KindOperation}

	for _, a := range []resource.Action{stop, retire, shutdown} {
		imp, err := h.store.CheckImpact(h.ctx, "db", a, false)
		var ie *resource.ImpactError
		if !errors.As(err, &ie) || !errors.Is(err, resource.ErrImpactUnacknowledged) || ie.Action != a.Name || len(imp.Dependents) != 1 {
			t.Errorf("%s unacknowledged: %v %+v", a.Name, err, imp)
		}
		if _, err := h.store.CheckImpact(h.ctx, "db", a, true); err != nil {
			t.Errorf("%s acknowledged: %v", a.Name, err)
		}
	}
	for _, a := range []resource.Action{start, unmanage} {
		if imp, err := h.store.CheckImpact(h.ctx, "db", a, false); err != nil || len(imp.Dependents) != 1 {
			t.Errorf("%s is not guarded: %v", a.Name, err)
		}
	}
	if _, err := h.store.CheckImpact(h.ctx, "web", stop, false); err != nil {
		t.Errorf("no impact needs no acknowledgement: %v", err)
	}

	imp, _ := h.store.Impact(h.ctx, "db")
	ctx := resource.WithAcknowledgedImpact(h.ctx, imp)
	if err := h.audit.Record(ctx, "service.stop", "db", audit.OutcomeOK, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := h.audit.List(h.ctx, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	ack, ok := entries[0].Details["acknowledgedImpact"].(map[string]any)
	if !ok || fmt.Sprint(ack["dependents"]) != "[web]" || fmt.Sprint(ack["controlDependents"]) != "[]" {
		t.Errorf("audit details = %v", entries[0].Details)
	}
}

func TestWritesRollBackWithoutAudit(t *testing.T) {
	h := newHarness(t)
	if _, err := h.store.Put(h.ctx, service("web", "command", "  config: { start: a, stop: b, restart: c, status: d }\n"), 0); err != nil {
		t.Fatal(err)
	}
	restore := storetest.FailInserts(t, h.db, "audit_log")
	if _, err := h.store.Put(h.ctx, service("api", "command", "  config: { start: a, stop: b, restart: c, status: d }\n"), 0); err == nil {
		t.Fatal("create succeeded without its audit entry")
	}
	if _, err := h.store.Get(h.ctx, "api"); !errors.Is(err, resource.ErrNotFound) {
		t.Fatalf("resource created without an audit entry: %v", err)
	}
	if _, err := h.store.Put(h.ctx, service("web", "command", "  config: { start: x, stop: b, restart: c, status: d }\n"), 1); err == nil {
		t.Fatal("update succeeded without its audit entry")
	}
	if err := h.store.SetPhase(h.ctx, "web", resource.PhaseRetired, 1, "gone"); err == nil {
		t.Fatal("phase change succeeded without its audit entry")
	}
	restore()
	rec, err := h.store.Get(h.ctx, "web")
	if err != nil || rec.Version != 1 || rec.Phase != resource.PhaseManaged {
		t.Fatalf("web = v%d %s, %v", rec.Version, rec.Phase, err)
	}
	if changes := h.flushed(t); len(changes) != 1 {
		t.Fatalf("changes published for rolled back writes: %+v", changes)
	}
}
