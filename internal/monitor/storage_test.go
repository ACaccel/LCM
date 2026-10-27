package monitor

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// manualPrune keeps the periodic prune timer out of tests that jump the
// clock by months and call Prune themselves.
func manualPrune(c *Config) { c.PruneInterval = 10 * 365 * 24 * time.Hour }

func (h *harness) count(table string) int {
	h.t.Helper()
	var n int
	if err := h.db.Reader().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil { //nolint:gosec // table names are literals in this file
		h.t.Fatal(err)
	}
	return n
}

// recordRun records 18 results, one every 10s from the epoch: three
// minutes of six results each. The sixth result of each minute fails, and
// the latency of result i is i+1 ms.
func (h *harness) recordRun(service string) {
	h.t.Helper()
	key := service + "/web"
	h.sync(fakeDef(service, "web"))
	for i := range 18 {
		if i%6 == 5 {
			h.fail(key)
		} else {
			h.set(key, Result{OK: true, Latency: time.Duration(i+1) * time.Millisecond})
		}
		if i == 0 {
			h.step(0)
		} else {
			h.step(testInterval)
		}
	}
}

func TestRollupsAndRawResults(t *testing.T) {
	h := newHarness(t)
	h.recordRun("api")
	if got := h.count("monitor_results"); got != 18 {
		t.Fatalf("raw results = %d", got)
	}
	var count, failures, sum, lo, hi int64
	err := h.db.Reader().QueryRow(`SELECT count, failures, lat_sum_us, lat_min_us, lat_max_us FROM monitor_rollups
        WHERE check_id = 'api/web' AND bucket_ms = ?`, epoch.UnixMilli()).Scan(&count, &failures, &sum, &lo, &hi)
	if err != nil {
		t.Fatal(err)
	}
	// Results 0-4 succeed with 1..5 ms; result 5 fails.
	if count != 6 || failures != 1 || sum != 15000 || lo != 1000 || hi != 5000 {
		t.Fatalf("rollup = count %d failures %d sum %d min %d max %d", count, failures, sum, lo, hi)
	}
	if got := h.count("monitor_rollups"); got != 3 {
		t.Fatalf("rollup rows = %d", got)
	}
}

func TestRollupOfOnlyFailures(t *testing.T) {
	h := newHarness(t)
	h.sync(fakeDef("api", "web"))
	h.fail("api/web")
	h.step(0)
	h.step(testInterval)
	var lo *int64
	if err := h.db.Reader().QueryRow(`SELECT lat_min_us FROM monitor_rollups`).Scan(&lo); err != nil || lo != nil {
		t.Fatalf("min latency of an all-failure bucket = %v, %v; want NULL", lo, err)
	}
	h.pass("api/web")
	h.step(testInterval)
	sum, err := h.m.CheckSummary(h.ctx, "api", "web", time.Hour)
	if err != nil || sum.Count != 3 || sum.Failures != 2 || sum.MinLatency != 5*time.Millisecond {
		t.Fatalf("summary = %+v, %v", sum, err)
	}
}

func TestSeries(t *testing.T) {
	h := newHarness(t)
	h.recordRun("api")
	end := epoch.Add(3 * time.Minute)

	raw, err := h.m.Series(h.ctx, "api", "web", epoch, end, 100)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Source != "raw" || raw.Step != 0 || len(raw.Points) != 18 {
		t.Fatalf("raw series = %s step %v with %d points", raw.Source, raw.Step, len(raw.Points))
	}
	if p := raw.Points[0]; p.Count != 1 || p.Failures != 0 || p.AvgLatency != time.Millisecond || !p.Time.Equal(epoch) {
		t.Errorf("first point = %+v", p)
	}
	if p := raw.Points[5]; p.Failures != 1 || p.AvgLatency != 0 {
		t.Errorf("failed point = %+v", p)
	}

	bucketed, err := h.m.Series(h.ctx, "api", "web", epoch, end, 3)
	if err != nil {
		t.Fatal(err)
	}
	if bucketed.Source != "raw" || bucketed.Step != time.Minute || len(bucketed.Points) != 3 {
		t.Fatalf("bucketed raw series = %s step %v with %d points", bucketed.Source, bucketed.Step, len(bucketed.Points))
	}
	if p := bucketed.Points[0]; p.Count != 6 || p.Failures != 1 || p.AvgLatency != 3*time.Millisecond ||
		p.MinLatency != time.Millisecond || p.MaxLatency != 5*time.Millisecond {
		t.Errorf("bucket 0 = %+v", p)
	}
	if p := bucketed.Points[1]; !p.Time.Equal(epoch.Add(time.Minute)) {
		t.Errorf("bucket 1 starts at %v", p.Time)
	}

	// A range longer than 6h always uses rollups.
	long, err := h.m.Series(h.ctx, "api", "web", epoch, epoch.Add(7*time.Hour), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if long.Source != "rollup" || long.Step != time.Minute || len(long.Points) != 3 {
		t.Fatalf("rollup series = %s step %v with %d points", long.Source, long.Step, len(long.Points))
	}
	if p := long.Points[1]; p.Count != 6 || p.Failures != 1 || p.AvgLatency != 9*time.Millisecond {
		t.Errorf("rollup bucket 1 = %+v", p)
	}
	coarse, err := h.m.Series(h.ctx, "api", "web", epoch, epoch.Add(7*time.Hour), 1)
	if err != nil || len(coarse.Points) != 1 || coarse.Points[0].Count != 18 || coarse.Points[0].Failures != 3 {
		t.Fatalf("coarse series = %+v, %v", coarse, err)
	}

	// Outside raw retention the rollups answer even for short ranges.
	h.sync()
	h.clk.Advance(8 * 24 * time.Hour)
	old, err := h.m.Series(h.ctx, "api", "web", epoch, end, 100)
	if err != nil || old.Source != "rollup" || len(old.Points) != 3 {
		t.Fatalf("series beyond raw retention = %+v, %v", old, err)
	}

	if _, err := h.m.Series(h.ctx, "api", "web", end, epoch, 10); !errors.Is(err, ErrInvalidRange) {
		t.Errorf("inverted range error = %v", err)
	}
	empty, err := h.m.Series(h.ctx, "api", "missing", epoch, end, 10)
	if err != nil || len(empty.Points) != 0 || empty.Points == nil {
		t.Errorf("series of an unknown check = %+v, %v", empty, err)
	}
}

func TestSummaries(t *testing.T) {
	h := newHarness(t)
	h.recordRun("api")
	// "api-gw" sorts next to "api" and must not leak into its summary.
	h.sync(fakeDef("api", "web"), fakeDef("api-gw", "web"))
	h.fail("api-gw/web")
	h.step(0)

	sum, err := h.m.CheckSummary(h.ctx, "api", "web", 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Count != 18 || sum.Failures != 3 {
		t.Fatalf("check summary = %+v", sum)
	}
	if want := 15.0 / 18.0; sum.Uptime != want {
		t.Errorf("uptime = %v, want %v", sum.Uptime, want)
	}
	// 15 successes with latencies 1..18 ms except 6, 12 and 18.
	if sum.AvgLatency != 9*time.Millisecond || sum.MinLatency != time.Millisecond || sum.MaxLatency != 17*time.Millisecond {
		t.Errorf("latency = avg %v min %v max %v", sum.AvgLatency, sum.MinLatency, sum.MaxLatency)
	}

	svc, err := h.m.ServiceSummary(h.ctx, "api", 24*time.Hour)
	if err != nil || svc.Count != 18 || svc.Failures != 3 {
		t.Fatalf("service summary = %+v, %v", svc, err)
	}
	gw, err := h.m.ServiceSummary(h.ctx, "api-gw", 24*time.Hour)
	if err != nil || gw.Count != 1 || gw.Failures != 1 || gw.Uptime != 0 {
		t.Fatalf("api-gw summary = %+v, %v", gw, err)
	}
	none, err := h.m.ServiceSummary(h.ctx, "nothing", 24*time.Hour)
	if err != nil || none.Count != 0 || none.Uptime != 0 {
		t.Fatalf("empty summary = %+v, %v", none, err)
	}
	if _, err := h.m.CheckSummary(h.ctx, "api", "web", 0); !errors.Is(err, ErrInvalidRange) {
		t.Errorf("zero window error = %v", err)
	}

	// Now is 170s. A 60s window starts at 110s, inside the bucket that
	// starts at 60s, so that bucket and the next one count.
	win, err := h.m.CheckSummary(h.ctx, "api", "web", 60*time.Second)
	if err != nil || win.Count != 12 {
		t.Fatalf("windowed summary = %+v, %v", win, err)
	}
}

func TestPruneBoundaries(t *testing.T) {
	h := newHarness(t, func(c *Config) {
		c.RawRetention, c.RollupRetention, c.PruneInterval = time.Hour, 2*time.Hour, 365*24*time.Hour
	})
	h.recordRun("api")
	h.sync()

	// Raw cutoff is epoch+100s: results at 0..90s go, the one at exactly
	// 100s stays.
	h.clk.Advance(time.Hour + 100*time.Second - h.clk.Now().Sub(epoch))
	if got := h.clk.Now(); !got.Equal(epoch.Add(time.Hour + 100*time.Second)) {
		t.Fatalf("clock = %v", got)
	}
	if err := h.m.Prune(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.count("monitor_results"); got != 8 {
		t.Errorf("raw results after prune = %d, want 8", got)
	}
	var oldest int64
	_ = h.db.Reader().QueryRow(`SELECT MIN(at) FROM monitor_results`).Scan(&oldest)
	if oldest != epoch.Add(100*time.Second).UnixMilli() {
		t.Errorf("oldest raw result at %v", time.UnixMilli(oldest).UTC())
	}
	if got := h.count("monitor_rollups"); got != 3 {
		t.Errorf("rollups pruned early: %d", got)
	}

	// Rollup cutoff is epoch+60s: bucket 0 goes, bucket 60s stays.
	h.clk.Advance(time.Hour - 40*time.Second)
	if got := h.clk.Now(); !got.Equal(epoch.Add(2*time.Hour + 60*time.Second)) {
		t.Fatalf("clock = %v", got)
	}
	if err := h.m.Prune(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.count("monitor_rollups"); got != 2 {
		t.Errorf("rollups after prune = %d, want 2", got)
	}
	if got := h.count("monitor_results"); got != 0 {
		t.Errorf("raw results left: %d", got)
	}
}

func TestPruneRunsPeriodically(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.RawRetention = 30 * time.Minute })
	h.recordRun("api")
	h.sync()
	h.clk.Advance(time.Hour) // the prune timer fires once
	if err := h.m.Flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.count("monitor_results"); got != 0 {
		t.Errorf("raw results after scheduled prune = %d", got)
	}
	if got := h.count("monitor_rollups"); got != 3 {
		t.Errorf("rollups = %d", got)
	}
}

func TestPrunesLargeBacklogInChunks(t *testing.T) {
	h := newHarness(t, manualPrune)
	h.m.pruneChunk = 7
	recs := make([]record, 0, 30)
	for i := range 30 {
		recs = append(recs, resultRec{checkID: "x/y", at: epoch.Add(time.Duration(i) * time.Minute), ok: true})
	}
	h.m.w.submit(nil, recs...)
	if err := h.m.w.flush(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.clk.Advance(DefaultRollupRetention + 24*time.Hour)
	if err := h.m.Prune(h.ctx); err != nil {
		t.Fatal(err)
	}
	if got, rollups := h.count("monitor_results"), h.count("monitor_rollups"); got != 0 || rollups != 0 {
		t.Errorf("left after chunked prune: %d results, %d rollups", got, rollups)
	}
}

func TestManyChecksStayCheap(t *testing.T) {
	h := newHarness(t)
	var defs []Def
	for i := range 50 {
		defs = append(defs, fakeDef("bulk", fmt.Sprintf("c%02d", i)))
	}
	h.sync(defs...)
	h.step(0)
	for range 5 {
		h.step(testInterval)
	}
	if got := h.count("monitor_results"); got != 300 {
		t.Fatalf("results = %d, want 300", got)
	}
	s, _ := h.m.Service("bulk")
	if len(s.Checks) != 50 || s.Health != HealthHealthy {
		t.Fatalf("service = %d checks, %s", len(s.Checks), s.Health)
	}
}
