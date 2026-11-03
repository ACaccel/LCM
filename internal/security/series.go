package security

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// SeriesPoint contains measured counters, never a synthetic empty bucket.
// Complete observations are assigned by their start; counters are not split.
// ObservedSeconds is the union of their full intervals, including any interval
// extending past the nominal bucket end. It is not a completeness assertion.
type SeriesPoint struct {
	WindowStart       time.Time `json:"windowStart"`
	WindowEnd         time.Time `json:"windowEnd"`
	Requests          int64     `json:"requests"`
	Status429         int64     `json:"status429"`
	Status5xx         int64     `json:"status5xx"`
	DryRunHits        int64     `json:"dryRunHits"`
	RequestTimeSumMs  float64   `json:"requestTimeSumMs"`
	RequestTimeMaxMs  float64   `json:"requestTimeMaxMs"`
	RequestBytes      int64     `json:"requestBytes"`
	ResponseBytes     int64     `json:"responseBytes"`
	AverageMs         float64   `json:"averageMs"`
	RequestsPerSecond float64   `json:"requestsPerSecond"`
	ObservedWindows   int64     `json:"observedWindows"`
	ObservedSeconds   float64   `json:"observedSeconds"`
}

type observedInterval struct{ start, end time.Time }
type seriesBucket struct {
	point     SeriesPoint
	intervals []observedInterval
}

func validateSeries(f TrafficFilter, bucketSeconds int) error {
	if err := f.validate(); err != nil {
		return err
	}
	if f.From.IsZero() || f.To.IsZero() {
		return filterIssue("from", "from and to are required for traffic series")
	}
	if f.To.Sub(f.From) > 7*24*time.Hour {
		return filterIssue("to", "traffic series cannot exceed seven days")
	}
	if bucketSeconds != 60 && bucketSeconds != 300 && bucketSeconds != 900 && bucketSeconds != 3600 {
		return filterIssue("bucketSeconds", "must be 60, 300, 900, or 3600")
	}
	width := int64(bucketSeconds) * 1000
	if (f.To.UnixMilli()-1)/width-f.From.UnixMilli()/width+1 > 1000 {
		return filterIssue("bucketSeconds", "traffic series cannot exceed 1000 buckets")
	}
	return nil
}

func intervalSeconds(intervals []observedInterval) float64 {
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].start.Before(intervals[j].start) })
	current := intervals[0]
	var total time.Duration
	for _, interval := range intervals[1:] {
		if interval.start.After(current.end) {
			total += current.end.Sub(current.start)
			current = interval
		} else if interval.end.After(current.end) {
			current.end = interval.end
		}
	}
	total += current.end.Sub(current.start)
	return total.Seconds()
}

// TrafficSeries groups complete observations into nominal UTC time buckets.
// Rates use observed interval coverage rather than filling collection gaps with
// zeros; category windows sharing an interval do not multiply the denominator.
func (m *Manager) TrafficSeries(ctx context.Context, f TrafficFilter, bucketSeconds int) ([]SeriesPoint, error) {
	if err := validateSeries(f, bucketSeconds); err != nil {
		return nil, err
	}
	where, args := trafficFilterSQL(f)
	args = append(args, MaxTrafficRows+1)
	rows, err := m.cfg.DB.Reader().QueryContext(ctx, `SELECT body FROM security_traffic WHERE `+strings.Join(where, " AND ")+` ORDER BY window_start,id LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	width := int64(bucketSeconds) * 1000
	buckets := map[int64]*seriesBucket{}
	count := 0
	for rows.Next() {
		count++
		if count > MaxTrafficRows {
			return nil, filterIssue("from", "too many observations; narrow the query")
		}
		var body string
		if err := rows.Scan(&body); err != nil {
			return nil, err
		}
		var observation Traffic
		if err := json.Unmarshal([]byte(body), &observation); err != nil {
			return nil, err
		}
		key := observation.WindowStart.UnixMilli() / width * width
		bucket := buckets[key]
		if bucket == nil {
			bucket = &seriesBucket{point: SeriesPoint{WindowStart: time.UnixMilli(key).UTC(), WindowEnd: time.UnixMilli(key + width).UTC()}}
			buckets[key] = bucket
		}
		p := &bucket.point
		p.Requests += observation.Requests
		p.Status429 += observation.Status429
		p.Status5xx += observation.Status5xx
		p.DryRunHits += observation.DryRunHits
		p.RequestTimeSumMs += observation.RequestTimeSumMs
		p.RequestTimeMaxMs = max(p.RequestTimeMaxMs, observation.RequestTimeMaxMs)
		p.RequestBytes += observation.RequestBytes
		p.ResponseBytes += observation.ResponseBytes
		p.ObservedWindows++
		bucket.intervals = append(bucket.intervals, observedInterval{observation.WindowStart, observation.WindowEnd})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	keys := make([]int64, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	items := make([]SeriesPoint, 0, len(keys))
	for _, key := range keys {
		bucket := buckets[key]
		p := bucket.point
		p.ObservedSeconds = intervalSeconds(bucket.intervals)
		if p.Requests > 0 {
			p.AverageMs = p.RequestTimeSumMs / float64(p.Requests)
		}
		if p.ObservedSeconds > 0 {
			p.RequestsPerSecond = float64(p.Requests) / p.ObservedSeconds
		}
		items = append(items, p)
	}
	return items, nil
}
