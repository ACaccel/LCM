package monitor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Series limits.
const (
	// RawSeriesMaxRange is the longest range served from raw results.
	RawSeriesMaxRange = 6 * time.Hour
	// DefaultSeriesPoints is used when maxPoints is not positive.
	DefaultSeriesPoints = 120
	// MaxSeriesPoints caps maxPoints.
	MaxSeriesPoints = 2000
)

// ErrInvalidRange is returned for an empty or inverted time range.
var ErrInvalidRange = errors.New("monitor: invalid time range")

func (e *entry) view(effective bool) CheckStatus {
	st := e.status
	if effective && e.stale {
		st = StatusUnknown
	}
	var failures int
	if st == StatusDown {
		failures = e.failRun
	}
	return CheckStatus{
		Service: e.service, Name: e.check.Name, Type: e.check.Type, Critical: e.check.IsCritical(),
		Interval: e.check.Interval.D(), Status: st, Stale: e.stale, Since: e.since, Message: e.lastMessage,
		LastResult: e.lastAt, LastLatency: e.lastLatency, ConsecutiveFailures: failures,
	}
}

func (s *service) view() ServiceStatus {
	out := ServiceStatus{
		Service: s.name, Labels: cloneLabels(s.labels), Health: healthOf(s.checks, true), Since: s.since,
		Checks: make([]CheckStatus, 0, len(s.checks)),
	}
	for _, e := range s.checks {
		out.Checks = append(out.Checks, e.view(true))
	}
	slices.SortFunc(out.Checks, func(a, b CheckStatus) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Services returns the current status of every service, sorted by name.
// Statuses restored after a restart read as unknown until a fresh result
// arrives if they were stale.
func (m *Monitor) Services() []ServiceStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ServiceStatus, 0, len(m.services))
	for _, s := range m.services {
		out = append(out, s.view())
	}
	slices.SortFunc(out, func(a, b ServiceStatus) int { return strings.Compare(a.Service, b.Service) })
	return out
}

// Service returns the status of one service and its checks.
func (m *Monitor) Service(name string) (ServiceStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.services[name]
	if s == nil {
		return ServiceStatus{}, false
	}
	return s.view(), true
}

// Point is one sample of a series: a single result (Count 1) or a bucket
// of results.
type Point struct {
	Time     time.Time `json:"time"`
	Count    int64     `json:"count"`
	Failures int64     `json:"failures"`
	// Latencies cover successful runs only and are zero when none
	// succeeded.
	AvgLatency time.Duration `json:"avgLatency"`
	MinLatency time.Duration `json:"minLatency"`
	MaxLatency time.Duration `json:"maxLatency"`
}

// Series is the history of one check.
type Series struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Source is "raw" or "rollup".
	Source string `json:"source"`
	// Step is the bucket width; zero when every point is one raw result.
	Step   time.Duration `json:"step"`
	Points []Point       `json:"points"`
}

// Series returns the history of a check over [from, to) as at most
// maxPoints points. Ranges of up to RawSeriesMaxRange that lie within raw
// retention use raw results; anything else uses the one-minute rollups.
func (m *Monitor) Series(ctx context.Context, service, check string, from, to time.Time, maxPoints int) (Series, error) {
	if !to.After(from) {
		return Series{}, ErrInvalidRange
	}
	switch {
	case maxPoints <= 0:
		maxPoints = DefaultSeriesPoints
	case maxPoints > MaxSeriesPoints:
		maxPoints = MaxSeriesPoints
	}
	id := checkID(service, check)
	now := m.clock.Now()
	if to.Sub(from) <= RawSeriesMaxRange && !from.Before(now.Add(-m.rawRetention)) {
		return m.rawSeries(ctx, id, from, to, maxPoints)
	}
	return m.rollupSeries(ctx, id, from, to, maxPoints)
}

func (m *Monitor) rawSeries(ctx context.Context, id string, from, to time.Time, maxPoints int) (Series, error) {
	rows, err := m.db.Reader().QueryContext(ctx,
		`SELECT at, ok, latency_us FROM monitor_results WHERE check_id = ? AND at >= ? AND at < ? ORDER BY at, id`,
		id, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return Series{}, fmt.Errorf("monitor: series: %w", err)
	}
	type raw struct {
		at      int64
		ok      bool
		latency time.Duration
	}
	var results []raw
	for rows.Next() {
		var r raw
		var ok, lat int64
		if err := rows.Scan(&r.at, &ok, &lat); err != nil {
			_ = rows.Close()
			return Series{}, fmt.Errorf("monitor: series: %w", err)
		}
		r.ok, r.latency = ok != 0, time.Duration(lat)*time.Microsecond
		results = append(results, r)
	}
	if err := closeRows(rows); err != nil {
		return Series{}, fmt.Errorf("monitor: series: %w", err)
	}

	s := Series{From: from.UTC(), To: to.UTC(), Source: "raw", Points: []Point{}}
	if len(results) <= maxPoints {
		for _, r := range results {
			p := Point{Time: fromMillis(r.at), Count: 1}
			if r.ok {
				p.AvgLatency, p.MinLatency, p.MaxLatency = r.latency, r.latency, r.latency
			} else {
				p.Failures = 1
			}
			s.Points = append(s.Points, p)
		}
		return s, nil
	}
	s.Step = stepFor(to.Sub(from), maxPoints, time.Millisecond)
	acc := newBuckets(from.UnixMilli(), s.Step)
	for _, r := range results {
		var lat int64 = -1
		if r.ok {
			lat = r.latency.Microseconds()
		}
		acc.add(r.at, 1, lat)
	}
	s.Points = acc.points()
	return s, nil
}

func (m *Monitor) rollupSeries(ctx context.Context, id string, from, to time.Time, maxPoints int) (Series, error) {
	start := bucketStart(from)
	step := stepFor(to.Sub(from), maxPoints, rollupBucket)
	rows, err := m.db.Reader().QueryContext(ctx,
		`SELECT (bucket_ms - ?1) / ?2 AS idx, SUM(count), SUM(failures), SUM(lat_sum_us), MIN(lat_min_us), MAX(lat_max_us)
         FROM monitor_rollups WHERE check_id = ?3 AND bucket_ms >= ?1 AND bucket_ms < ?4
         GROUP BY idx ORDER BY idx`,
		start, step.Milliseconds(), id, to.UnixMilli())
	if err != nil {
		return Series{}, fmt.Errorf("monitor: series: %w", err)
	}
	s := Series{From: from.UTC(), To: to.UTC(), Source: "rollup", Step: step, Points: []Point{}}
	for rows.Next() {
		var idx, count, failures, sum int64
		var lo, hi *int64
		if err := rows.Scan(&idx, &count, &failures, &sum, &lo, &hi); err != nil {
			_ = rows.Close()
			return Series{}, fmt.Errorf("monitor: series: %w", err)
		}
		p := Point{Time: fromMillis(start + idx*step.Milliseconds()), Count: count, Failures: failures}
		p.AvgLatency, p.MinLatency, p.MaxLatency = latencies(count-failures, sum, lo, hi)
		s.Points = append(s.Points, p)
	}
	if err := closeRows(rows); err != nil {
		return Series{}, fmt.Errorf("monitor: series: %w", err)
	}
	return s, nil
}

// stepFor returns the smallest multiple of unit that fits span into at most
// n buckets.
func stepFor(span time.Duration, n int, unit time.Duration) time.Duration {
	step := (span + time.Duration(n) - 1) / time.Duration(n)
	step = (step + unit - 1) / unit * unit
	return max(step, unit)
}

func latencies(samples, sumUs int64, lo, hi *int64) (avg, minL, maxL time.Duration) {
	if samples <= 0 {
		return 0, 0, 0
	}
	avg = time.Duration(sumUs/samples) * time.Microsecond
	if lo != nil {
		minL = time.Duration(*lo) * time.Microsecond
	}
	if hi != nil {
		maxL = time.Duration(*hi) * time.Microsecond
	}
	return avg, minL, maxL
}

// buckets groups raw results into fixed-width buckets.
type buckets struct {
	start, stepMs int64
	byIdx         map[int64]*bucket
}

type bucket struct {
	count, failures, sum, ok int64
	lo, hi                   int64
}

func newBuckets(start int64, step time.Duration) *buckets {
	return &buckets{start: start, stepMs: step.Milliseconds(), byIdx: map[int64]*bucket{}}
}

// add records a result; latencyUs is negative for a failure.
func (b *buckets) add(at, count int64, latencyUs int64) {
	idx := (at - b.start) / b.stepMs
	x := b.byIdx[idx]
	if x == nil {
		x = &bucket{}
		b.byIdx[idx] = x
	}
	x.count += count
	if latencyUs < 0 {
		x.failures += count
		return
	}
	if x.ok == 0 || latencyUs < x.lo {
		x.lo = latencyUs
	}
	if x.ok == 0 || latencyUs > x.hi {
		x.hi = latencyUs
	}
	x.ok += count
	x.sum += latencyUs * count
}

func (b *buckets) points() []Point {
	idxs := make([]int64, 0, len(b.byIdx))
	for i := range b.byIdx {
		idxs = append(idxs, i)
	}
	slices.Sort(idxs)
	out := make([]Point, 0, len(idxs))
	for _, i := range idxs {
		x := b.byIdx[i]
		p := Point{Time: fromMillis(b.start + i*b.stepMs), Count: x.count, Failures: x.failures}
		if x.ok > 0 {
			p.AvgLatency, p.MinLatency, p.MaxLatency = latencies(x.ok, x.sum, &x.lo, &x.hi)
		}
		out = append(out, p)
	}
	return out
}

// Summary is the uptime and latency of a check or service over a window,
// computed from one-minute rollups.
type Summary struct {
	Window   time.Duration `json:"window"`
	Count    int64         `json:"count"`
	Failures int64         `json:"failures"`
	// Uptime is the share of successful runs, 0 to 1. It is 0 when Count
	// is 0, which means no data rather than an outage.
	Uptime     float64       `json:"uptime"`
	AvgLatency time.Duration `json:"avgLatency"`
	MinLatency time.Duration `json:"minLatency"`
	MaxLatency time.Duration `json:"maxLatency"`
}

// CheckSummary summarizes one check over the window ending now. Typical
// windows are 24h, 7d and 30d; the window is limited by rollup retention.
func (m *Monitor) CheckSummary(ctx context.Context, service, check string, window time.Duration) (Summary, error) {
	id := checkID(service, check)
	return m.summary(ctx, window, `check_id = ?`, id)
}

// ServiceSummary summarizes all checks of a service over the window,
// weighting every run equally.
func (m *Monitor) ServiceSummary(ctx context.Context, service string, window time.Duration) (Summary, error) {
	// "service/" up to "service0" ('0' follows '/') spans exactly the
	// check ids of the service and uses the primary key.
	return m.summary(ctx, window, `check_id >= ? AND check_id < ?`, service+"/", service+"0")
}

func (m *Monitor) summary(ctx context.Context, window time.Duration, where string, args ...any) (Summary, error) {
	if window <= 0 {
		return Summary{}, ErrInvalidRange
	}
	cutoff := bucketStart(m.clock.Now().Add(-window))
	var count, failures, sum sql64
	var lo, hi *int64
	err := m.db.Reader().QueryRowContext(ctx,
		`SELECT SUM(count), SUM(failures), SUM(lat_sum_us), MIN(lat_min_us), MAX(lat_max_us)
         FROM monitor_rollups WHERE `+where+` AND bucket_ms >= ?`, append(args, cutoff)...).
		Scan(&count, &failures, &sum, &lo, &hi)
	if err != nil {
		return Summary{}, fmt.Errorf("monitor: summary: %w", err)
	}
	s := Summary{Window: window, Count: int64(count), Failures: int64(failures)}
	if s.Count > 0 {
		s.Uptime = float64(s.Count-s.Failures) / float64(s.Count)
	}
	s.AvgLatency, s.MinLatency, s.MaxLatency = latencies(s.Count-s.Failures, int64(sum), lo, hi)
	return s, nil
}

// sql64 scans a SUM() that is NULL over no rows as zero.
type sql64 int64

func (n *sql64) Scan(v any) error {
	switch x := v.(type) {
	case nil:
		*n = 0
	case int64:
		*n = sql64(x)
	default:
		return fmt.Errorf("unexpected %T in aggregate", v)
	}
	return nil
}
