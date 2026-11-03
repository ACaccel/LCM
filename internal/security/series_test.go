package security

import (
	"errors"
	"testing"
	"time"

	"github.com/ACaccel/lcm/internal/model"
)

func TestTrafficSeriesWeightedCountersCoverageAndGaps(t *testing.T) {
	f := setup(t)
	start := f.clk.Now().Add(-time.Hour)
	for _, observation := range []Traffic{
		{MachineID: "machine-a", Category: "auth", WindowStart: start, WindowEnd: start.Add(time.Minute), Requests: 2, Status429: 1, RequestTimeSumMs: 20, RequestTimeMaxMs: 10, RequestBytes: 12, ResponseBytes: 24, DryRunHits: 2},
		{MachineID: "machine-a", Category: "api", WindowStart: start, WindowEnd: start.Add(time.Minute), Requests: 18, Status5xx: 3, RequestTimeSumMs: 1800, RequestTimeMaxMs: 100, RequestBytes: 108, ResponseBytes: 216, DryRunHits: 4},
		{MachineID: "machine-a", Category: "auth", WindowStart: start.Add(2 * time.Minute), WindowEnd: start.Add(3 * time.Minute), Requests: 10, RequestTimeSumMs: 100, RequestTimeMaxMs: 10, RequestBytes: 60, ResponseBytes: 120},
		{MachineID: "machine-a", Category: "auth", WindowStart: start.Add(10 * time.Minute), WindowEnd: start.Add(11 * time.Minute)},
	} {
		if _, err := f.m.IngestTraffic(f.ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	points, err := f.m.TrafficSeries(f.ctx, TrafficFilter{MachineID: "machine-a", From: start, To: start.Add(15 * time.Minute)}, 300)
	if err != nil || len(points) != 2 {
		t.Fatalf("observed buckets only: %+v %v", points, err)
	}
	p := points[0]
	if !p.WindowStart.Equal(start) || !p.WindowEnd.Equal(start.Add(5*time.Minute)) || p.Requests != 30 || p.Status429 != 1 || p.Status5xx != 3 || p.DryRunHits != 6 || p.RequestBytes != 180 || p.ResponseBytes != 360 || p.RequestTimeSumMs != 1920 || p.RequestTimeMaxMs != 100 || p.AverageMs != 64 || p.ObservedWindows != 3 || p.ObservedSeconds != 120 || p.RequestsPerSecond != 0.25 {
		t.Fatalf("weighted aggregation and interval union: %+v", p)
	}
	if !points[1].WindowStart.Equal(start.Add(10*time.Minute)) || points[1].Requests != 0 || points[1].ObservedWindows != 1 || points[1].ObservedSeconds != 60 || points[1].AverageMs != 0 || points[1].RequestsPerSecond != 0 {
		t.Fatalf("observed empty differs from missing bucket: %+v", points[1])
	}
	filtered, err := f.m.TrafficSeries(f.ctx, TrafficFilter{Category: "api", From: start, To: start.Add(15 * time.Minute)}, 300)
	if err != nil || len(filtered) != 1 || filtered[0].Requests != 18 || filtered[0].AverageMs != 100 || filtered[0].ObservedSeconds != 60 {
		t.Fatalf("category applied before aggregation: %+v %v", filtered, err)
	}
	empty, err := f.m.TrafficSeries(f.ctx, TrafficFilter{Service: "missing", From: start, To: start.Add(15 * time.Minute)}, 300)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty JSON array, no invented points: %+v %v", empty, err)
	}
}

func TestTrafficSeriesCrossBucketWindowsAreNotSplit(t *testing.T) {
	f := setup(t)
	start := f.clk.Now().Add(-time.Hour)
	for _, observation := range []Traffic{
		{MachineID: "machine-a", Category: "auth", WindowStart: start.Add(4 * time.Minute), WindowEnd: start.Add(7 * time.Minute), Requests: 180},
		{MachineID: "machine-a", Category: "api", WindowStart: start.Add(4*time.Minute + 30*time.Second), WindowEnd: start.Add(6 * time.Minute), Requests: 90},
	} {
		if _, err := f.m.IngestTraffic(f.ctx, observation); err != nil {
			t.Fatal(err)
		}
	}
	points, err := f.m.TrafficSeries(f.ctx, TrafficFilter{From: start, To: start.Add(10 * time.Minute)}, 300)
	if err != nil || len(points) != 1 {
		t.Fatalf("cross-bucket grouping: %+v %v", points, err)
	}
	p := points[0]
	if p.Requests != 270 || p.ObservedSeconds != 180 || p.RequestsPerSecond != 1.5 || !p.WindowEnd.Equal(start.Add(5*time.Minute)) {
		t.Fatalf("complete counters and full observed interval: %+v", p)
	}
	points, err = f.m.TrafficSeries(f.ctx, TrafficFilter{From: start, To: start.Add(6 * time.Minute)}, 300)
	if err != nil || len(points) != 1 || points[0].Requests != 90 || points[0].ObservedSeconds != 90 {
		t.Fatalf("partly contained window excluded: %+v %v", points, err)
	}
}

func TestTrafficSeriesValidationBoundsAndAlignedBucketCount(t *testing.T) {
	f := setup(t)
	start := f.clk.Now().Add(-7 * 24 * time.Hour)
	for name, test := range map[string]struct {
		filter TrafficFilter
		bucket int
	}{
		"missing range":          {TrafficFilter{}, 60},
		"missing end":            {TrafficFilter{From: start}, 60},
		"reversed range":         {TrafficFilter{From: start.Add(time.Hour), To: start}, 60},
		"over seven days":        {TrafficFilter{From: start, To: start.Add(7*24*time.Hour + time.Millisecond)}, 3600},
		"invalid bucket":         {TrafficFilter{From: start, To: start.Add(time.Hour)}, 30},
		"too many buckets":       {TrafficFilter{From: start, To: start.Add(1001 * time.Minute)}, 60},
		"unaligned extra bucket": {TrafficFilter{From: start.Add(time.Second), To: start.Add(1000*time.Minute + time.Second)}, 60},
		"invalid category":       {TrafficFilter{Category: "raw", From: start, To: start.Add(time.Hour)}, 60},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := f.m.TrafficSeries(f.ctx, test.filter, test.bucket)
			var issues model.Issues
			if !errors.As(err, &issues) {
				t.Fatalf("expected model.Issues, got %v", err)
			}
		})
	}
	for _, test := range []struct {
		to     time.Time
		bucket int
	}{{start.Add(1000 * time.Minute), 60}, {start.Add(7 * 24 * time.Hour), 900}, {start.Add(7 * 24 * time.Hour), 3600}} {
		if _, err := f.m.TrafficSeries(f.ctx, TrafficFilter{From: start, To: test.to}, test.bucket); err != nil {
			t.Fatalf("valid boundary rejected: %+v %v", test, err)
		}
	}
}
