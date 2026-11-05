package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/audit"
	"github.com/ACaccel/lcm/internal/auth"
	"github.com/ACaccel/lcm/internal/store/storetest"
)

func TestSearchFilters(t *testing.T) {
	clk := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	l := audit.New(storetest.Open(t), audit.WithClock(func() time.Time { return clk }))
	record := func(actor, action, target string, at time.Time) {
		t.Helper()
		clk = at
		ctx := auth.WithActor(context.Background(), auth.Actor{Kind: auth.KindUser, ID: actor, Name: actor})
		if err := l.Record(ctx, action, target, audit.OutcomeOK, nil); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	record("alice", "service.deploy", "api", base.Add(1*time.Hour))
	record("bob", "service.stop", "api", base.Add(2*time.Hour))
	record("alice", "serviceX.other", "db", base.Add(3*time.Hour))
	record("alice", "machine.reboot", "host", base.Add(4*time.Hour))

	cases := []struct {
		name string
		q    audit.Query
		want int
	}{
		{"none", audit.Query{}, 4},
		{"actor", audit.Query{Actor: "alice"}, 3},
		{"exact action", audit.Query{Action: "service.stop"}, 1},
		{"action prefix stops at the dot", audit.Query{Action: "service"}, 2},
		{"target", audit.Query{Target: "api"}, 2},
		{"from is inclusive", audit.Query{From: base.Add(2 * time.Hour)}, 3},
		{"to is exclusive", audit.Query{To: base.Add(2 * time.Hour)}, 1},
		{"combined", audit.Query{Actor: "alice", Action: "service", Target: "api", From: base, To: base.Add(24 * time.Hour)}, 1},
		{"no match", audit.Query{Actor: "carol"}, 0},
	}
	for _, c := range cases {
		got, err := l.Search(context.Background(), c.q)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: got %d entries, want %d", c.name, len(got), c.want)
		}
	}
}
