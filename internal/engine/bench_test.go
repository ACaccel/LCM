package engine_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ACaccel/lcm/internal/clock"
	"github.com/ACaccel/lcm/internal/engine"
	"github.com/ACaccel/lcm/internal/engine/enginetest"
	"github.com/ACaccel/lcm/internal/model"
)

// chainPolicy builds a policy of n nodes: a trigger followed by blocks of
// flow.if -> two branches -> merge (dead-path elimination on every block),
// then a linear tail, with expressions referencing upstream outputs.
func chainPolicy(name string, n int, concurrency string) string {
	var b strings.Builder
	b.WriteString("  concurrency: " + concurrency + "\n  params: { k: 7 }\n  nodes:\n")
	b.WriteString("    - { id: start, type: trigger.manual, next: { out: [n1] } }\n")
	next := func(i int) string {
		if i+1 >= n {
			return ""
		}
		return fmt.Sprintf(", next: { out: [n%d] }", i+1)
	}
	i := 1
	for ; i+3 < n; i += 4 {
		cond, yes, no, join := i, i+1, i+2, i+3
		fmt.Fprintf(&b, "    - { id: n%d, type: flow.if, props: { condition: \"trigger.x %% 2 == %d\" }, next: { \"true\": [n%d], \"false\": [n%d] } }\n", cond, cond%2, yes, no)
		fmt.Fprintf(&b, "    - { id: n%d, type: data.set, props: { values: { v: \"{{ trigger.x + %d }}\" } }, next: { out: [n%d] } }\n", yes, yes, join)
		fmt.Fprintf(&b, "    - { id: n%d, type: test.echo, props: { value: \"{{ params.k }}-%d\" }, next: { out: [n%d] } }\n", no, no, join)
		fmt.Fprintf(&b, "    - { id: n%d, type: flow.merge, props: { value: \"{{ nodes.n%d ?? nodes.n%d }}\" }%s }\n", join, yes, no, next(join))
	}
	for ; i < n; i++ {
		fmt.Fprintf(&b, "    - { id: n%d, type: test.echo, props: { value: \"{{ run.attempt }}\" }%s }\n", i, next(i))
	}
	return policy(name, b.String())
}

func BenchmarkCompile200(b *testing.B) {
	h := enginetest.New(b)
	p, err := model.DecodePolicy([]byte(chainPolicy("bench", 200, "{ mode: queue }")))
	if err != nil {
		b.Fatal(err)
	}
	opts := engine.CompileOptions{Registry: h.Registry}
	if _, err := engine.Compile(context.Background(), p, opts); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := engine.Compile(context.Background(), p, opts); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkRuns1000x20 measures one batch of 1,000 runs of a 20-node plan
// per iteration, including SQLite checkpoints.
func BenchmarkRuns1000x20(b *testing.B) {
	h := enginetest.New(b)
	e := h.Open(func(c *engine.Config) { c.Clock = clock.Real(); c.Workers = 0; c.Observers = nil })
	h.Deploy(e, chainPolicy("bench", 20, "{ mode: parallel, limit: 200 }"))
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		ids := make([]string, 1000)
		for i := range ids {
			id, err := e.Start(ctx, "bench", "start", map[string]any{"x": i}, engine.ModeLive)
			if err != nil {
				b.Fatal(err)
			}
			ids[i] = id
		}
		for _, id := range ids {
			v, err := e.Wait(ctx, id)
			if err != nil || v.Status != engine.RunSucceeded {
				b.Fatalf("run %s: %v %s %s", id, err, v.Status, v.Error)
			}
		}
	}
	b.ReportMetric(float64(b.N*1000)/b.Elapsed().Seconds(), "runs/s")
}
