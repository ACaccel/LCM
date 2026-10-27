package catalog_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/catalog"
	"github.com/ACaccel/lcm/internal/signal"
)

func TestTimelineFollowsSignals(t *testing.T) {
	e := newEnv(t)
	e.put(svcDoc("app", ""), 0)
	f := e.cat.FollowSignals(context.Background(), e.bus)

	e.bus.Publish(signal.Signal{Kind: signal.KindCheck, Service: "app", Source: "monitor", Severity: signal.SeverityCritical,
		Data: map[string]any{"check": "http", "status": "down", "previous": "up", "message": "HTTP 502"}})
	e.bus.Publish(signal.Signal{Kind: signal.KindHealth, Service: "app", Source: "monitor",
		Data: map[string]any{"health": "down", "previous": "healthy"}})
	e.bus.Publish(signal.Signal{Kind: signal.KindAlert, Service: "app", Source: "webhook",
		Data: map[string]any{"alert": "HighLatency", "status": "firing"}})
	e.bus.Publish(signal.Signal{Kind: signal.KindAlert, Source: "webhook", Data: map[string]any{"alert": "global"}}) // no service
	// The catalog writes its own lifecycle entries; the follower must not duplicate them.
	e.setState("app", catalog.StateRunning)
	f.Close()
	f.Close()

	page, err := e.cat.Timeline(context.Background(), catalog.TimelineQuery{Service: "app"})
	if err != nil {
		t.Fatal(err)
	}
	var summaries []string
	for _, en := range page.Entries {
		summaries = append(summaries, en.Kind+": "+en.Summary)
	}
	want := []string{
		"lifecycle.changed: registered -> running (test)",
		"alert: alert HighLatency: firing",
		"health.changed: health: healthy -> down",
		"check.changed: check http: up -> down (HTTP 502)",
		"config.changed: configuration version 1 saved by alice",
	}
	if fmt.Sprint(summaries) != fmt.Sprint(want) {
		t.Fatalf("timeline =\n%v\nwant\n%v", summaries, want)
	}
	check := page.Entries[3]
	if check.Ref == "" || check.Data["severity"] != "critical" || check.Data["source"] != "monitor" {
		t.Fatalf("check entry = %+v", check)
	}
}

func TestTimelineQueryAndPagination(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	add := func(service, kind string, at time.Duration) {
		t.Helper()
		if err := e.cat.AppendTimeline(ctx, catalog.Entry{
			Service: service, Kind: kind, Summary: fmt.Sprintf("%s %s +%s", service, kind, at), Time: epoch.Add(at),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 7 {
		add("a", "k1", time.Duration(i)*time.Minute)
	}
	add("a", "k2", 10*time.Minute)
	add("b", "k1", 11*time.Minute)

	// Cursor pagination covers every entry exactly once.
	var seen []int64
	var before int64
	pages := 0
	for {
		page, err := e.cat.Timeline(ctx, catalog.TimelineQuery{Service: "a", Limit: 3, Before: before})
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, en := range page.Entries {
			seen = append(seen, en.ID)
		}
		if page.Next == 0 {
			break
		}
		before = page.Next
	}
	if len(seen) != 8 || pages != 3 {
		t.Fatalf("seen %d entries in %d pages", len(seen), pages)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] >= seen[i-1] {
			t.Fatalf("not newest first: %v", seen)
		}
	}

	page, _ := e.cat.Timeline(ctx, catalog.TimelineQuery{Service: "a", Kinds: []string{"k2"}})
	if len(page.Entries) != 1 || page.Entries[0].Kind != "k2" || page.Next != 0 {
		t.Fatalf("kind filter = %+v", page)
	}
	page, _ = e.cat.Timeline(ctx, catalog.TimelineQuery{Service: "a", Kinds: []string{"k1", "k2"}, From: epoch.Add(2 * time.Minute), To: epoch.Add(5 * time.Minute)})
	if len(page.Entries) != 3 {
		t.Fatalf("time range [2m,5m) = %d entries", len(page.Entries))
	}
	page, _ = e.cat.Timeline(ctx, catalog.TimelineQuery{})
	if len(page.Entries) != 9 {
		t.Fatalf("all services = %d entries", len(page.Entries))
	}
	page, _ = e.cat.Timeline(ctx, catalog.TimelineQuery{Service: "nobody"})
	if page.Entries == nil || len(page.Entries) != 0 {
		t.Fatalf("empty result must be a non-nil empty slice: %+v", page)
	}
	if err := e.cat.AppendTimeline(ctx, catalog.Entry{Kind: "k"}); err == nil {
		t.Fatal("entry without service accepted")
	}
}

func TestPruneTimeline(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	for _, age := range []time.Duration{-10 * 24 * time.Hour, -8 * 24 * time.Hour, -time.Hour, 0} {
		if err := e.cat.AppendTimeline(ctx, catalog.Entry{Service: "a", Kind: "k", Summary: "s", Time: epoch.Add(age)}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := e.cat.PruneTimeline(ctx, 0); err != nil || n != 0 {
		t.Fatalf("prune with no retention = %d %v", n, err)
	}
	n, err := e.cat.PruneTimeline(ctx, 7*24*time.Hour)
	if err != nil || n != 2 {
		t.Fatalf("pruned %d, %v", n, err)
	}
	e.clk.Advance(2 * time.Hour)
	if n, _ := e.cat.PruneTimeline(ctx, 2*time.Hour+30*time.Minute); n != 1 {
		t.Fatalf("second prune removed %d", n)
	}
	page, _ := e.cat.Timeline(ctx, catalog.TimelineQuery{})
	if len(page.Entries) != 1 {
		t.Fatalf("remaining = %d", len(page.Entries))
	}
}
