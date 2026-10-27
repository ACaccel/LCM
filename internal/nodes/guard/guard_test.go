package guard_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/node"
	"github.com/ACaccel/lcm/internal/nodes/guard"
)

func registry() *node.Registry {
	r := node.NewRegistry()
	guard.Register(r)
	return r
}

func run(t *testing.T, now time.Time, mode node.Mode, props map[string]any) (node.Result, error) {
	t.Helper()
	d, _ := registry().Lookup("guard.window")
	p, err := d.Decode(props)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	rc := &node.RunContext{Clock: clock.NewFake(now), Mode: mode, Logger: slog.New(slog.DiscardHandler)}
	return d.Run(context.Background(), rc, p)
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func windows(ws ...map[string]any) []any {
	out := make([]any, len(ws))
	for i, w := range ws {
		out[i] = w
	}
	return out
}

func TestWindowPassAndBlock(t *testing.T) {
	// 2026-01-05 is a Monday.
	night := map[string]any{"windows": windows(map[string]any{"start": "22:00", "end": "06:00"}), "timezone": "Asia/Taipei"}
	tests := []struct {
		name  string
		now   string
		props map[string]any
		port  string
	}{
		{"inside overnight window, evening", "2026-01-05T22:30:00+08:00", night, "out"},
		{"inside overnight window, after midnight", "2026-01-06T05:59:00+08:00", night, "out"},
		{"start is inclusive", "2026-01-05T22:00:00+08:00", night, "out"},
		{"end is exclusive", "2026-01-06T06:00:00+08:00", night, "blocked"},
		{"daytime", "2026-01-05T12:00:00+08:00", night, "blocked"},
		{"zone is applied", "2026-01-05T15:00:00Z", night, "out"}, // 23:00 in Taipei
		{"weekday only, Monday", "2026-01-05T10:00:00Z", map[string]any{"windows": windows(map[string]any{"days": []any{"mon", "wed"}, "start": "09:00", "end": "17:00"})}, "out"},
		{"weekday only, Tuesday", "2026-01-06T10:00:00Z", map[string]any{"windows": windows(map[string]any{"days": []any{"mon", "wed"}, "start": "09:00", "end": "17:00"})}, "blocked"},
		{"overnight window opened on a listed day continues past midnight", "2026-01-06T01:00:00Z", map[string]any{"windows": windows(map[string]any{"days": []any{"mon"}, "start": "22:00", "end": "06:00"})}, "out"},
		{"overnight window of an unlisted day does not", "2026-01-07T01:00:00Z", map[string]any{"windows": windows(map[string]any{"days": []any{"mon"}, "start": "22:00", "end": "06:00"})}, "blocked"},
		{"whole day via 24:00", "2026-01-05T23:59:00Z", map[string]any{"windows": windows(map[string]any{"start": "00:00", "end": "24:00"})}, "out"},
		{"second window matches", "2026-01-05T20:00:00Z", map[string]any{"windows": windows(
			map[string]any{"start": "09:00", "end": "10:00"}, map[string]any{"start": "19:00", "end": "21:00"})}, "out"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := run(t, at(tt.now), node.ModeLive, tt.props)
			if err != nil {
				t.Fatal(err)
			}
			want := tt.port
			if want == "out" {
				want = ""
			}
			if res.Port != want {
				t.Fatalf("port = %q, want %q (output %v)", res.Port, tt.port, res.Output)
			}
		})
	}
}

func TestWindowBlockedReportsNextOpen(t *testing.T) {
	props := map[string]any{"windows": windows(map[string]any{"days": []any{"sat", "sun"}, "start": "02:00", "end": "05:00"}), "timezone": "Asia/Taipei"}
	res, _ := run(t, at("2026-01-05T12:00:00+08:00"), node.ModeLive, props) // Monday
	out := res.Output.(map[string]any)
	if res.Port != "blocked" || out["allowed"] != false || out["nextOpen"] != "2026-01-10T02:00:00+08:00" {
		t.Fatalf("res = %+v", res)
	}
}

func TestWindowDST(t *testing.T) {
	// America/New_York springs forward on 2026-03-08. A 01:00-04:00 window
	// that night is only two real hours long.
	props := map[string]any{"windows": windows(map[string]any{"start": "01:00", "end": "04:00"}), "timezone": "America/New_York"}
	for _, tt := range []struct {
		now  string
		open bool
	}{
		{"2026-03-08T06:30:00Z", true},  // 01:30 EST
		{"2026-03-08T07:30:00Z", true},  // 03:30 EDT
		{"2026-03-08T08:00:00Z", false}, // 04:00 EDT, closed
		{"2026-03-08T05:59:00Z", false}, // 00:59 EST
	} {
		res, _ := run(t, at(tt.now), node.ModeLive, props)
		if (res.Port == "") != tt.open {
			t.Errorf("%s: open=%v, got port %q", tt.now, tt.open, res.Port)
		}
	}
}

func TestWindowWaitSuspendsUntilOpen(t *testing.T) {
	props := map[string]any{"windows": windows(map[string]any{"start": "22:00", "end": "23:00"}), "wait": true}
	res, err := run(t, at("2026-01-05T10:00:00Z"), node.ModeLive, props)
	if err != nil || res.Suspend == nil || !res.Suspend.ResumeAt.Equal(at("2026-01-05T22:00:00Z")) {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	// maxWait turns a long wait into a block.
	props["maxWait"] = "2h"
	if res, _ = run(t, at("2026-01-05T10:00:00Z"), node.ModeLive, props); res.Port != "blocked" {
		t.Fatalf("maxWait: res = %+v", res)
	}
	props["maxWait"] = "12h"
	if res, _ = run(t, at("2026-01-05T10:00:00Z"), node.ModeLive, props); res.Suspend == nil {
		t.Fatalf("within maxWait: res = %+v", res)
	}
	// Dry runs continue instead of waiting.
	res, _ = run(t, at("2026-01-05T10:00:00Z"), node.ModeDry, props)
	if res.Suspend != nil || res.Port != "" || res.Output.(map[string]any)["dry"] != true {
		t.Fatalf("dry: res = %+v", res)
	}
}

func TestWindowValidation(t *testing.T) {
	d, _ := registry().Lookup("guard.window")
	w := func(m map[string]any) map[string]any { return map[string]any{"windows": windows(m)} }
	bad := map[string]map[string]any{
		"windows":       {"windows": []any{}},
		"HH:MM":         w(map[string]any{"start": "9:00", "end": "17:00"}),
		"24:00":         w(map[string]any{"start": "24:00", "end": "05:00"}),
		"equal":         w(map[string]any{"start": "09:00", "end": "09:00"}),
		"unknown day":   w(map[string]any{"days": []any{"funday"}, "start": "09:00", "end": "17:00"}),
		"timezone":      {"windows": windows(map[string]any{"start": "09:00", "end": "17:00"}), "timezone": "Nowhere/City"},
		"between 00:00": w(map[string]any{"start": "09:60", "end": "17:00"}),
	}
	for want, props := range bad {
		_, err := d.Decode(props)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestWindowWaitThroughEngine(t *testing.T) {
	h := enginetest.New(t)
	guard.Register(h.Registry)
	e := h.Open()
	// The harness clock starts at 2026-01-01 00:00 UTC.
	h.Deploy(e, "apiVersion: lcm/v1\nkind: Policy\nmetadata: { name: deploy }\nspec:\n  nodes:\n"+`
    - { id: start, type: trigger.manual, next: { out: [win] } }
    - id: win
      type: guard.window
      props: { windows: [{ start: "03:00", end: "05:00" }], wait: true }
      next: { out: [go], blocked: [no] }
    - { id: go, type: test.echo, props: { value: deployed } }
    - { id: no, type: test.echo, props: { value: blocked } }
`)
	id := h.Start(e, "deploy", nil)
	opens := enginetest.Epoch.Add(3 * time.Hour)
	h.Events.WaitFor(t, "suspended", func(ev engine.Event) bool {
		return ev.Kind == engine.EventNodeSuspended && ev.RunID == id && ev.RetryAt != nil && ev.RetryAt.Equal(opens)
	})
	h.WaitTimers(1)
	h.Clock.Advance(3 * time.Hour)
	v := h.Wait(e, id)
	if goNode, _ := v.Node("go"); v.Status != engine.RunSucceeded || goNode.Status != engine.NodeCompleted {
		t.Fatalf("status=%s go=%+v err=%s", v.Status, goNode, v.Error)
	}
	if n, _ := v.Node("no"); n.Status == engine.NodeCompleted {
		t.Fatal("blocked branch ran")
	}
}
