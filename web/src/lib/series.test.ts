import { describe, expect, it } from "vitest";
import type { Series, SeriesPoint } from "../api/types";
import { bucketUptime, latencyStats, mergeSeries, overallUptime } from "./series";

const pt = (time: string, count: number, failures: number, avg = 100): SeriesPoint => ({
  time,
  count,
  failures,
  avgLatencyMs: avg,
  minLatencyMs: avg,
  maxLatencyMs: avg,
});

const series = (points: SeriesPoint[]): Series => ({
  service: "s",
  check: "c",
  from: "2026-10-28T00:00:00Z",
  to: "2026-10-29T00:00:00Z",
  source: "rollup",
  stepMs: 3_600_000,
  points,
});

describe("series helpers", () => {
  it("merges checks bucket by bucket with weighted latency", () => {
    const merged = mergeSeries([
      series([pt("2026-10-28T01:00:00Z", 10, 0, 100), pt("2026-10-28T02:00:00Z", 10, 10, 0)]),
      series([pt("2026-10-28T01:00:00Z", 30, 0, 200)]),
    ]);
    expect(merged).toHaveLength(2);
    expect(merged[0]).toMatchObject({ count: 40, failures: 0, avgLatencyMs: 175 });
    expect(merged[1]).toMatchObject({ count: 10, failures: 10 });
  });

  it("resamples into fixed buckets and leaves empty ones null", () => {
    const from = Date.parse("2026-10-28T00:00:00Z");
    const to = from + 4 * 3_600_000;
    const b = bucketUptime([pt("2026-10-28T00:30:00Z", 10, 1), pt("2026-10-28T00:45:00Z", 10, 1), pt("2026-10-28T02:10:00Z", 5, 5)], from, to, 4);
    expect(b.map((x) => x.ratio)).toEqual([0.9, null, 0, null]);
    expect(b[0]?.failures).toBe(2);
  });

  it("ignores points outside the range", () => {
    const from = Date.parse("2026-10-28T00:00:00Z");
    expect(bucketUptime([pt("2026-10-27T23:00:00Z", 1, 1)], from, from + 3_600_000, 2).every((x) => x.ratio === null)).toBe(true);
  });

  it("computes overall uptime", () => {
    expect(overallUptime([pt("a", 10, 1), pt("b", 10, 0)])).toBeCloseTo(0.95);
    expect(overallUptime([])).toBeUndefined();
  });
});

describe("latencyStats", () => {
  const pt = (count: number, failures: number, avg: number, max: number) => ({ time: "2026-01-01T00:00:00Z", count, failures, avgLatencyMs: avg, minLatencyMs: avg, maxLatencyMs: max });

  it("weights the average by successful samples and ignores failed buckets", () => {
    expect(latencyStats([pt(3, 0, 10, 20), pt(2, 1, 40, 90), pt(2, 2, 0, 0)])).toEqual({ avg: 17.5, max: 90 });
  });

  it("is undefined without a successful sample", () => {
    expect(latencyStats([pt(1, 1, 0, 0)])).toBeUndefined();
    expect(latencyStats([])).toBeUndefined();
  });
});
