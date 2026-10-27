import type { Series, SeriesPoint } from "../api/types";

/** Adds up the points of several checks that fall into the same bucket. */
export function mergeSeries(all: readonly Series[]): SeriesPoint[] {
  const byTime = new Map<string, SeriesPoint>();
  for (const s of all) {
    for (const p of s.points) {
      const prev = byTime.get(p.time);
      if (!prev) {
        byTime.set(p.time, { ...p });
        continue;
      }
      const okPrev = prev.count - prev.failures;
      const okNew = p.count - p.failures;
      const ok = okPrev + okNew;
      byTime.set(p.time, {
        time: p.time,
        count: prev.count + p.count,
        failures: prev.failures + p.failures,
        // Weighted by the successful samples behind each average.
        avgLatencyMs: ok > 0 ? (prev.avgLatencyMs * okPrev + p.avgLatencyMs * okNew) / ok : 0,
        minLatencyMs: okPrev && okNew ? Math.min(prev.minLatencyMs, p.minLatencyMs) : okPrev ? prev.minLatencyMs : p.minLatencyMs,
        maxLatencyMs: Math.max(prev.maxLatencyMs, p.maxLatencyMs),
      });
    }
  }
  return [...byTime.values()].sort((a, b) => Date.parse(a.time) - Date.parse(b.time));
}

export interface Bucket {
  /** Uptime ratio 0..1, or null when no check ran in the bucket. */
  ratio: number | null;
  failures: number;
}

/**
 * Resamples points into `n` equal buckets between `from` and `to`, summing
 * counts, so charts have a fixed width whatever step the server chose.
 */
export function bucketUptime(points: readonly SeriesPoint[], from: number, to: number, n: number): Bucket[] {
  const span = to - from;
  const counts = new Array<number>(n).fill(0);
  const fails = new Array<number>(n).fill(0);
  if (span <= 0 || n <= 0) return [];
  for (const p of points) {
    const t = Date.parse(p.time);
    if (Number.isNaN(t) || t < from || t >= to) continue;
    const i = Math.min(n - 1, Math.floor(((t - from) / span) * n));
    counts[i] = (counts[i] ?? 0) + p.count;
    fails[i] = (fails[i] ?? 0) + p.failures;
  }
  return counts.map((c, i) => ({ ratio: c > 0 ? 1 - (fails[i] ?? 0) / c : null, failures: fails[i] ?? 0 }));
}

/** Overall uptime of a bucket list; undefined when nothing ran. */
export function overallUptime(points: readonly SeriesPoint[]): number | undefined {
  let count = 0;
  let failures = 0;
  for (const p of points) {
    count += p.count;
    failures += p.failures;
  }
  return count > 0 ? 1 - failures / count : undefined;
}
