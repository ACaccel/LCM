package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/model"
)

func TestPutCreatesAndVersions(t *testing.T) {
	e := newEnv(t)
	res := e.put(svcDoc("app", ""), 0)
	if res.Version != 1 || !res.Created || !res.Changed {
		t.Fatalf("create = %+v", res)
	}
	rec, err := e.cat.Get(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != catalog.StateRegistered || rec.Version != 1 || rec.Host != "local" || rec.Spec.Spec.Runtime.Unit != "app.service" ||
		!rec.CreatedAt.Equal(epoch) {
		t.Fatalf("record = %+v", rec)
	}

	// An identical document, however it is spelled, changes nothing.
	res = e.put(svcDoc("app", "  host: local\n  configSource: gui\n"), 1)
	if res.Changed || res.Version != 1 {
		t.Fatalf("unchanged save = %+v", res)
	}

	e.clk.Advance(time.Minute)
	res = e.put(svcDoc("app", "  dependsOn: [db]\n"), 1)
	if res.Version != 2 || res.Created || !res.Changed {
		t.Fatalf("update = %+v", res)
	}
	rec, _ = e.cat.Get(context.Background(), "app")
	if rec.Version != 2 || !rec.UpdatedAt.Equal(epoch.Add(time.Minute)) || rec.Spec.Spec.DependsOn[0] != "db" {
		t.Fatalf("record after update = %+v", rec)
	}
}

func TestPutConflictsAndErrors(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	e.put(svcDoc("app", "  dependsOn: [a]\n"), 1)

	tests := []struct {
		name     string
		doc      string
		expected int
		want     error
	}{
		{"stale version", svcDoc("app", "  dependsOn: [b]\n"), 1, catalog.ErrConflict},
		{"future version", svcDoc("app", "  dependsOn: [b]\n"), 5, catalog.ErrConflict},
		{"exists", svcDoc("app", "  dependsOn: [b]\n"), 0, catalog.ErrExists},
		{"update missing", svcDoc("other", ""), 1, catalog.ErrNotFound},
	}
	for _, tt := range tests {
		_, err := e.cat.Put(e.ctx, []byte(tt.doc), tt.expected)
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}
	if _, err := e.cat.Put(e.ctx, []byte(svcDoc("app", "")), -1); err == nil {
		t.Error("negative expected version accepted")
	}
	// A failed save writes nothing.
	vs, _ := e.cat.Versions(context.Background(), "app")
	if len(vs) != 2 {
		t.Fatalf("versions = %+v", vs)
	}

	var is model.Issues
	_, err := e.cat.Put(e.ctx, []byte("apiVersion: lcm/v1\nkind: Service\nmetadata: { name: x }\nspec: { runtime: { type: bogus } }\n"), 0)
	if !errors.As(err, &is) || is[0].Path != "spec.runtime.type" {
		t.Fatalf("invalid document: %v", err)
	}
	if _, err := e.cat.Put(e.ctx, []byte(validPolicy), 0); err == nil {
		t.Error("policy accepted as a service")
	}
	if _, err := e.cat.Put(context.Background(), []byte(svcDoc("noactor", "")), 0); !errors.Is(err, audit.ErrNoActor) {
		t.Errorf("no actor: %v", err)
	}
}

const validPolicy = "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: p }\nspec:\n  nodes:\n    - { id: a, type: trigger.manual }\n"

func TestOptimisticLockIsAtomic(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	const writers = 8
	results := make(chan error, writers)
	for i := range writers {
		go func() {
			doc := svcDoc("app", fmt.Sprintf("  jobs: { j%d: { command: x } }\n", i))
			_, err := e.cat.Put(e.ctx, []byte(doc), 1)
			results <- err
		}()
	}
	wins, conflicts := 0, 0
	for range writers {
		switch err := <-results; {
		case err == nil:
			wins++
		case errors.Is(err, catalog.ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 || conflicts != writers-1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
}

func TestVersionsHistoryAndRestore(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	e.clk.Advance(time.Minute)
	e.put(svcDoc("app", "  dependsOn: [db]\n"), 1)
	e.clk.Advance(time.Minute)
	e.put(svcDoc("app", "  dependsOn: [db, cache]\n"), 2)

	ctx := auth.WithActor(context.Background(), bob)
	res, err := e.cat.Restore(ctx, "app", 1, 3)
	if err != nil || res.Version != 4 || !res.Changed {
		t.Fatalf("restore = %+v %v", res, err)
	}
	rec, _ := e.cat.Get(ctx, "app")
	if len(rec.Spec.Spec.DependsOn) != 0 || rec.Version != 4 {
		t.Fatalf("restored record = %+v", rec)
	}

	vs, err := e.cat.Versions(ctx, "app")
	if err != nil || len(vs) != 4 {
		t.Fatalf("versions = %+v %v", vs, err)
	}
	if vs[0].Version != 4 || vs[0].RestoredFrom != 1 || vs[0].CreatedBy != bob || vs[3].CreatedBy != alice || vs[3].RestoredFrom != 0 {
		t.Fatalf("version info = %+v", vs)
	}
	old, info, err := e.cat.GetVersion(ctx, "app", 2)
	if err != nil || old.Spec.DependsOn[0] != "db" || info.Version != 2 || !info.CreatedAt.Equal(epoch.Add(time.Minute)) {
		t.Fatalf("GetVersion = %+v %+v %v", old, info, err)
	}

	if _, err := e.cat.Restore(ctx, "app", 9, 4); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("restore unknown version: %v", err)
	}
	if _, err := e.cat.Restore(ctx, "app", 2, 3); !errors.Is(err, catalog.ErrConflict) {
		t.Errorf("restore with stale base: %v", err)
	}
	if _, err := e.cat.Restore(ctx, "app", 2, 0); err == nil {
		t.Error("restore without a base version accepted")
	}
	if _, err := e.cat.Versions(ctx, "ghost"); !errors.Is(err, catalog.ErrNotFound) {
		t.Errorf("versions of unknown service: %v", err)
	}
}

func TestAuditRecordsActors(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	if _, err := e.cat.Put(auth.WithActor(context.Background(), bob), []byte(svcDoc("app", "  dependsOn: [db]\n")), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cat.Restore(e.ctx, "app", 1, 2); err != nil {
		t.Fatal(err)
	}
	if err := e.cat.Delete(auth.WithActor(context.Background(), auth.CLI), "app"); err != nil {
		t.Fatal(err)
	}
	// An unchanged save is not an event.
	e.put(svcDoc("again", ""), 0)
	e.put(svcDoc("again", ""), 1)

	entries, err := e.audit.List(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := len(entries) - 1; i >= 0; i-- {
		en := entries[i]
		got = append(got, en.Action+"|"+en.Target+"|"+en.Actor.Name)
		if en.Outcome != audit.OutcomeOK {
			t.Errorf("outcome of %s = %s", en.Action, en.Outcome)
		}
	}
	want := []string{
		"service.create|app|alice", "service.update|app|ci-token", "service.restore|app|alice",
		"service.delete|app|cli", "service.create|again|alice",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("audit = %v\nwant    %v", got, want)
	}
}

func TestDelete(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	ctx := context.Background()

	e.setState("app", catalog.StateRunning)
	if err := e.cat.Delete(e.ctx, "app"); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("delete while running: %v", err)
	}
	e.setState("app", catalog.StateUpdating)
	if err := e.cat.Delete(e.ctx, "app"); !errors.Is(err, catalog.ErrInvalidTransition) {
		t.Fatalf("delete while updating: %v", err)
	}
	e.setState("app", catalog.StateRunning)
	e.setState("app", catalog.StateStopped)
	if err := e.cat.Delete(e.ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cat.Get(ctx, "app"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("deleted service still found: %v", err)
	}
	if err := e.cat.Delete(e.ctx, "app"); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := e.cat.Delete(ctx, "app"); err == nil {
		t.Fatal("delete without an actor accepted")
	}

	// History survives and numbering continues when the name is reused.
	if vs, err := e.cat.Versions(ctx, "app"); err != nil || len(vs) != 1 {
		t.Fatalf("history after delete: %+v %v", vs, err)
	}
	res := e.put(svcDoc("app", "  dependsOn: [x]\n"), 0)
	if res.Version != 2 || !res.Created {
		t.Fatalf("re-create = %+v", res)
	}
	if e.state("app") != catalog.StateRegistered {
		t.Fatal("re-created service must start registered")
	}
}

func TestListAndHosts(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("zeta", ""), 0)
	e.put(svcDoc("alpha", ""), 0)
	recs, err := e.cat.List(context.Background())
	if err != nil || len(recs) != 2 || recs[0].Name != "alpha" || recs[1].Name != "zeta" {
		t.Fatalf("list = %+v %v", recs, err)
	}
	hosts, err := e.cat.Hosts(context.Background())
	if err != nil || len(hosts) != 1 || hosts[0].Name != "local" || hosts[0].Kind != "local" {
		t.Fatalf("hosts = %+v %v", hosts, err)
	}
}
