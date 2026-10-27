package audit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestRecordAndList(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	l := audit.New(storetest.Open(t), audit.WithClock(func() time.Time { return now }))

	ctx := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindToken, ID: "7", Name: "ci"})
	if err := l.Record(ctx, "token.create", "token:9", audit.OutcomeOK, map[string]any{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := l.Record(auth.WithActor(context.Background(), auth.CLI), "token.revoke", "token:9", audit.OutcomeDenied, nil); err != nil {
		t.Fatal(err)
	}

	got, err := l.List(context.Background(), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d", len(got))
	}
	// Newest first.
	if got[0].Action != "token.revoke" || got[0].Actor != auth.CLI || got[0].Outcome != audit.OutcomeDenied {
		t.Errorf("newest = %+v", got[0])
	}
	if len(got[0].Details) != 0 {
		t.Errorf("nil details came back as %v", got[0].Details)
	}
	e := got[1]
	if e.Actor.Kind != auth.KindToken || e.Actor.ID != "7" || e.Actor.Name != "ci" ||
		e.Target != "token:9" || e.Details["name"] != "x" || !e.Time.Equal(now) {
		t.Errorf("entry = %+v", e)
	}
}

func TestRecordRequiresActor(t *testing.T) {
	l := audit.New(storetest.Open(t))
	err := l.Record(context.Background(), "a.b", "", audit.OutcomeOK, nil)
	if !errors.Is(err, audit.ErrNoActor) {
		t.Fatalf("err = %v", err)
	}
	ctx := auth.WithActor(context.Background(), auth.Actor{Kind: "root"})
	if err := l.Record(ctx, "a.b", "", audit.OutcomeOK, nil); err == nil {
		t.Fatal("invalid actor kind was accepted")
	}
}

func TestListPagination(t *testing.T) {
	l := audit.New(storetest.Open(t))
	ctx := auth.WithActor(context.Background(), auth.CLI)
	for range 5 {
		if err := l.Record(ctx, "x.y", "", audit.OutcomeOK, nil); err != nil {
			t.Fatal(err)
		}
	}
	page1, err := l.List(ctx, 2, 0)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1: %v %v", page1, err)
	}
	page2, err := l.List(ctx, 2, page1[1].ID)
	if err != nil || len(page2) != 2 {
		t.Fatalf("page2: %v %v", page2, err)
	}
	if page2[0].ID >= page1[1].ID {
		t.Errorf("cursor not respected: %d >= %d", page2[0].ID, page1[1].ID)
	}
	page3, err := l.List(ctx, 2, page2[1].ID)
	if err != nil || len(page3) != 1 {
		t.Fatalf("page3: %v %v", page3, err)
	}
	if all, _ := l.List(ctx, 100000, 0); len(all) != 5 {
		t.Errorf("oversized limit: len = %d", len(all))
	}
}
