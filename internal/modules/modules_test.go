package modules_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/model"
	"github.com/ACaccel/lcm/internal/modules"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

var epoch = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) (*modules.Store, *audit.Log, context.Context) {
	t.Helper()
	db := storetest.Open(t)
	clk := clock.NewFake(epoch)
	log := audit.New(db, audit.WithClock(clk.Now))
	s, err := modules.New(db, log, clk)
	if err != nil {
		t.Fatal(err)
	}
	return s, log, auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindUser, ID: "1", Name: "admin"})
}

func doc(name, param string) []byte {
	return []byte(`apiVersion: lcm/v1
kind: Module
metadata: { name: ` + name + ` }
spec:
  params: [ { name: ` + param + ` } ]
  nodes:
    - { id: in, type: module.input, next: { out: [o] } }
    - { id: o, type: module.output, props: { port: ok } }
`)
}

func TestPutVersionsAndConflicts(t *testing.T) {
	s, log, ctx := newStore(t)

	res, err := s.Put(ctx, doc("restart-safely", "target"), 0)
	if err != nil || res.Version != 1 || !res.Created || !res.Changed {
		t.Fatalf("create = %+v, %v", res, err)
	}
	if _, err := s.Put(ctx, doc("restart-safely", "target"), 0); !errors.Is(err, modules.ErrExists) {
		t.Fatalf("second create: %v", err)
	}

	same, err := s.Put(ctx, doc("restart-safely", "target"), 1)
	if err != nil || same.Version != 1 || same.Changed {
		t.Fatalf("identical update = %+v, %v", same, err)
	}
	if _, err := s.Put(ctx, doc("restart-safely", "other"), 5); !errors.Is(err, modules.ErrConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if _, err := s.Put(ctx, doc("ghost", "x"), 1); !errors.Is(err, modules.ErrNotFound) {
		t.Fatalf("update of unknown module: %v", err)
	}
	res, err = s.Put(ctx, doc("restart-safely", "other"), 1)
	if err != nil || res.Version != 2 || res.Created {
		t.Fatalf("update = %+v, %v", res, err)
	}

	cur, err := s.Get(ctx, "restart-safely")
	if err != nil || cur.Version != 2 || cur.Spec.Spec.Params[0].Name != "other" || cur.CreatedBy.Name != "admin" {
		t.Fatalf("get = %+v, %v", cur, err)
	}
	old, err := s.GetVersion(ctx, "restart-safely", 1)
	if err != nil || old.Spec.Spec.Params[0].Name != "target" {
		t.Fatalf("get version 1 = %+v, %v", old, err)
	}
	if _, err := s.GetVersion(ctx, "restart-safely", 9); !errors.Is(err, modules.ErrNotFound) {
		t.Fatalf("get missing version: %v", err)
	}
	vs, err := s.Versions(ctx, "restart-safely")
	if err != nil || len(vs) != 2 || vs[0].Version != 2 || vs[1].Version != 1 {
		t.Fatalf("versions = %+v, %v", vs, err)
	}
	if _, err := s.Versions(ctx, "ghost"); !errors.Is(err, modules.ErrNotFound) {
		t.Fatalf("versions of unknown module: %v", err)
	}

	entries, err := log.List(ctx, 10, 0)
	if err != nil || len(entries) != 2 || entries[0].Action != "module.update" || entries[1].Action != "module.create" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
}

func TestPutRejectsInvalidDocumentsAndMissingActor(t *testing.T) {
	s, _, ctx := newStore(t)

	var is model.Issues
	if _, err := s.Put(ctx, []byte("kind: Module"), 0); !errors.As(err, &is) {
		t.Fatalf("invalid document error = %v, want model.Issues", err)
	}
	// A policy document is not a module.
	pol := []byte("apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: p }\nspec: { nodes: [ { id: t, type: trigger.manual } ] }\n")
	if _, err := s.Put(ctx, pol, 0); !errors.As(err, &is) {
		t.Fatalf("policy as module error = %v", err)
	}
	if _, err := s.Put(context.Background(), doc("m", "x"), 0); !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("no actor: %v", err)
	}
}

func TestListAndResolve(t *testing.T) {
	s, _, ctx := newStore(t)
	for _, n := range []string{"b-mod", "a-mod"} {
		if _, err := s.Put(ctx, doc(n, "x"), 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Put(ctx, doc("a-mod", "y"), 1); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "a-mod" || list[0].Version != 2 || list[1].Name != "b-mod" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	m, err := s.ResolveModule(ctx, "a-mod")
	if err != nil || m.Spec.Params[0].Name != "y" {
		t.Fatalf("resolve = %+v, %v", m, err)
	}
	if _, err := s.ResolveModule(ctx, "ghost"); !errors.Is(err, modules.ErrNotFound) {
		t.Fatalf("resolve unknown: %v", err)
	}
}
