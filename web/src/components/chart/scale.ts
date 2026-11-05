/** Pure scale, tick and sample helpers behind the chart components. */

export interface NiceScale {
  min: number;
  max: number;
  step: number;
  ticks: number[];
}

const round = (v: number) => Number(v.toFixed(10));

/** Smallest 1, 2, 5 or 10 times a power of ten that is at least `rough`. */
export function niceStep(rough: number): number {
  if (!(rough > 0) || !Number.isFinite(rough)) return 1;
  const base = 10 ** Math.floor(Math.log10(rough));
  for (const m of [1, 2, 5, 10]) {
    if (m * base >= rough * (1 - 1e-9)) return m * base;
  }
  return 10 * base;
}

/**
 * A value axis that covers [min, max] with round tick values. With `binary`
 * the ticks are round in the 1024-based unit of the data (KiB, MiB), so byte
 * labels do not land on values such as 1.95 KiB.
 */
export function niceScale(min: number, max: number, count = 4, binary = false): NiceScale {
  if (!Number.isFinite(min) || !Number.isFinite(max)) return { min: 0, max: 1, step: 1, ticks: [0, 1] };
  if (binary && max > 1024) {
    const unit = 1024 ** Math.floor(Math.log(max) / Math.log(1024));
    const inner = niceScale(min / unit, max / unit, count);
    return { min: inner.min * unit, max: inner.max * unit, step: inner.step * unit, ticks: inner.ticks.map((t) => t * unit) };
  }
  let hi = max;
  if (hi <= min) hi = min === 0 ? 1 : min + Math.abs(min) * 0.1;
  const step = niceStep((hi - min) / Math.max(1, count));
  const lo = Math.floor(min / step + 1e-9) * step;
  const top = Math.ceil(hi / step - 1e-9) * step;
  const ticks: number[] = [];
  for (let v = lo; v <= top + step * 1e-9; v += step) ticks.push(round(v));
  return { min: round(lo), max: round(top), step, ticks };
}

/** Axis with a fixed range such as 0 to 100 percent, with evenly spaced ticks. */
export function fixedScale(min: number, max: number, count = 4): NiceScale {
  const step = (max - min) / Math.max(1, count);
  return { min, max, step, ticks: Array.from({ length: count + 1 }, (_, i) => round(min + i * step)) };
}

export interface TimeTick {
  t: number;
  /** HH:mm; empty for ticks that fall on whole days. */
  time: string;
  /** MM/DD; set on the first tick and wherever the day changes, only when the range spans days. */
  date?: string;
}

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
// Neighbouring steps differ by at most 2.5x, so the chosen step gives 3 to 6 ticks.
const TIME_STEPS = [MIN, 2 * MIN, 5 * MIN, 10 * MIN, 15 * MIN, 20 * MIN, 30 * MIN, HOUR, 2 * HOUR, 3 * HOUR, 4 * HOUR, 6 * HOUR, 12 * HOUR, DAY, 2 * DAY, 3 * DAY, 5 * DAY, 7 * DAY, 14 * DAY, 30 * DAY];

const two = (n: number) => String(n).padStart(2, "0");
const dayOf = (d: Date) => `${two(d.getMonth() + 1)}/${two(d.getDate())}`;
const sameDay = (a: number, b: number) => new Date(a).toDateString() === new Date(b).toDateString();

function ticksAt(from: number, to: number, step: number): number[] {
  const out: number[] = [];
  if (step >= DAY) {
    const days = Math.round(step / DAY);
    const d = new Date(from);
    d.setHours(0, 0, 0, 0);
    if (d.getTime() < from) d.setDate(d.getDate() + 1);
    for (; d.getTime() <= to; d.setDate(d.getDate() + days)) out.push(d.getTime());
    return out;
  }
  // Align to local wall-clock multiples of the step.
  const offset = -new Date(from).getTimezoneOffset() * MIN;
  for (let t = Math.ceil((from + offset) / step) * step - offset; t <= to; t += step) out.push(t);
  return out;
}

/** 3 to 6 round local-time ticks across [from, to], with dates where the range crosses days. */
export function timeTicks(from: number, to: number, maxTicks = 6): TimeTick[] {
  if (!(to > from)) return [];
  const limit = Math.max(2, maxTicks);
  let picked: number[] = [];
  let step = DAY;
  for (const candidate of TIME_STEPS) {
    const ticks = ticksAt(from, to, candidate);
    if (ticks.length <= limit) {
      picked = ticks;
      step = candidate;
      break;
    }
  }
  const crosses = !sameDay(from, to);
  return picked.map((t, i) => {
    const d = new Date(t);
    const time = step >= DAY ? "" : `${two(d.getHours())}:${two(d.getMinutes())}`;
    const newDay = i === 0 || !sameDay(picked[i - 1] ?? t, t);
    return step >= DAY || (crosses && newDay) ? { t, time, date: dayOf(d) } : { t, time };
  });
}

export interface XY {
  t: number;
  v: number | null;
}

/**
 * Splits a series into runs that are drawn as one line. A missing value, or a
 * spacing wider than `gapMs`, ends a run: gaps are never bridged.
 */
export function lineSegments(points: readonly XY[], gapMs?: number): { t: number; v: number }[][] {
  const out: { t: number; v: number }[][] = [];
  let cur: { t: number; v: number }[] = [];
  for (const p of points) {
    if (p.v === null || !Number.isFinite(p.v) || !Number.isFinite(p.t)) {
      if (cur.length) out.push(cur);
      cur = [];
      continue;
    }
    const prev = cur[cur.length - 1];
    if (prev && gapMs !== undefined && p.t - prev.t > gapMs) {
      out.push(cur);
      cur = [];
    }
    cur.push({ t: p.t, v: p.v });
  }
  if (cur.length) out.push(cur);
  return out;
}

export interface StackSegment {
  /** Index of the series the segment belongs to. */
  series: number;
  from: number;
  to: number;
}

/** Cumulative segments of one stacked bar; null and non-positive values take no space. */
export function stackSegments(values: readonly (number | null)[]): StackSegment[] {
  const out: StackSegment[] = [];
  let acc = 0;
  values.forEach((v, series) => {
    if (v === null || !Number.isFinite(v) || v <= 0) return;
    out.push({ series, from: acc, to: acc + v });
    acc += v;
  });
  return out;
}

/** Index of the time nearest to `t` in an ascending list; -1 when empty. */
export function nearestIndex(times: readonly number[], t: number): number {
  if (times.length === 0) return -1;
  let lo = 0;
  let hi = times.length - 1;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if ((times[mid] ?? 0) < t) lo = mid + 1;
    else hi = mid;
  }
  if (lo > 0 && Math.abs((times[lo - 1] ?? 0) - t) <= Math.abs((times[lo] ?? 0) - t)) return lo - 1;
  return lo;
}

/**
 * Active sample after a key press: arrows step, Home and End jump, and Escape
 * clears. Returns undefined for keys the chart does not handle.
 */
export function stepIndex(current: number | null, key: string, count: number): number | null | undefined {
  if (count <= 0) return undefined;
  switch (key) {
    case "ArrowLeft":
      return Math.max(0, (current ?? count) - 1);
    case "ArrowRight":
      return Math.min(count - 1, (current ?? -1) + 1);
    case "Home":
      return 0;
    case "End":
      return count - 1;
    case "Escape":
      return null;
    default:
      return undefined;
  }
}

/** Median spacing of ascending times, used to decide what counts as a gap; undefined below two samples. */
export function medianInterval(times: readonly number[]): number | undefined {
  if (times.length < 2) return undefined;
  const d: number[] = [];
  for (let i = 1; i < times.length; i++) d.push((times[i] ?? 0) - (times[i - 1] ?? 0));
  d.sort((a, b) => a - b);
  return d[d.length >> 1];
}

export interface Windowed {
  from: number;
  to: number;
  /** Spacings wider than this are gaps. */
  gapMs: number | undefined;
  series: XY[][];
}

/**
 * The samples of several series inside the last `rangeMs` before the newest
 * sample. The window never starts before the oldest sample, so a short
 * history fills the chart. Long histories are averaged into `maxPoints`
 * buckets; missing values stay missing and gaps are never filled.
 */
export function windowSeries(series: readonly (readonly XY[])[], rangeMs: number, maxPoints = 240): Windowed | null {
  const times = series.flatMap((s) => s.map((p) => p.t)).filter((t) => Number.isFinite(t));
  if (times.length === 0) return null;
  const last = Math.max(...times);
  const from = Math.max(Math.min(...times), last - rangeMs);
  const to = last > from ? last : from + 60_000;
  const inside = series.map((s) => s.filter((p) => p.t >= from && p.t <= last).sort((a, b) => a.t - b.t));
  const reduce = Math.max(...inside.map((s) => s.length)) > maxPoints;
  if (reduce) {
    return { from, to, gapMs: ((to + 1 - from) / maxPoints) * 1.5, series: inside.map((s) => downsample(s, from, to + 1, maxPoints)) };
  }
  const step = medianInterval([...new Set(inside.flatMap((s) => s.map((p) => p.t)))].sort((a, b) => a - b));
  return { from, to, gapMs: step === undefined ? undefined : step * 3, series: inside };
}

/**
 * Averages samples into at most `n` equal buckets over [from, to] so a long
 * history stays readable; empty buckets stay empty (they remain gaps).
 */
export function downsample(points: readonly XY[], from: number, to: number, n: number): XY[] {
  const known = points.filter((p) => p.v !== null && p.t >= from && p.t <= to);
  if (known.length <= n || !(to > from)) return known;
  const sum = new Array<number>(n).fill(0);
  const cnt = new Array<number>(n).fill(0);
  const width = (to - from) / n;
  for (const p of known) {
    const i = Math.min(n - 1, Math.floor((p.t - from) / width));
    sum[i] = (sum[i] ?? 0) + (p.v ?? 0);
    cnt[i] = (cnt[i] ?? 0) + 1;
  }
  const out: XY[] = [];
  // Buckets are placed at their midpoint, so series downsampled over the same range share their times.
  for (let i = 0; i < n; i++) {
    const c = cnt[i] ?? 0;
    if (c > 0) out.push({ t: from + (i + 0.5) * width, v: (sum[i] ?? 0) / c });
  }
  return out;
}
