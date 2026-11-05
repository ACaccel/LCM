import { describe, expect, it } from "vitest";
import { downsample, fixedScale, lineSegments, medianInterval, nearestIndex, niceScale, niceStep, stackSegments, stepIndex, timeTicks, windowSeries } from "./scale";

describe("value scales", () => {
  it("rounds steps to 1, 2 or 5 times a power of ten", () => {
    expect(niceStep(0.7)).toBe(1);
    expect(niceStep(1.2)).toBe(2);
    expect(niceStep(3)).toBe(5);
    expect(niceStep(7)).toBe(10);
    expect(niceStep(0)).toBe(1);
  });

  it("scales to cover the data with round ticks starting at zero", () => {
    const sc = niceScale(0, 47, 4);
    expect(sc.min).toBe(0);
    expect(sc.max).toBeGreaterThanOrEqual(47);
    expect(sc.ticks[0]).toBe(0);
    expect(sc.ticks.at(-1)).toBe(sc.max);
    expect(sc.ticks.length).toBeLessThanOrEqual(7);
    // Every tick is a multiple of the step.
    for (const t of sc.ticks) expect(Math.abs(t / sc.step - Math.round(t / sc.step))).toBeLessThan(1e-9);
  });

  it("does not collapse for flat or empty data", () => {
    expect(niceScale(0, 0).max).toBeGreaterThan(0);
    expect(niceScale(5, 5).max).toBeGreaterThan(5);
    expect(niceScale(Number.NaN, 3).ticks).toEqual([0, 1]);
  });

  it("keeps byte ticks round in the binary unit", () => {
    const sc = niceScale(0, 3.2 * 1024 * 1024, 4, true);
    for (const t of sc.ticks) {
      const mib = t / (1024 * 1024);
      expect(Math.abs(mib * 2 - Math.round(mib * 2))).toBeLessThan(1e-9);
    }
    expect(sc.max).toBeGreaterThanOrEqual(3.2 * 1024 * 1024);
  });

  it("supports a fixed 0 to 100 percent axis", () => {
    expect(fixedScale(0, 100).ticks).toEqual([0, 25, 50, 75, 100]);
  });
});

describe("time ticks", () => {
  const HOUR = 3_600_000;
  const DAY = 24 * HOUR;
  const at = (d: string) => new Date(d).getTime();

  it("gives 3 to 6 ascending ticks inside the range for the usual windows", () => {
    for (const span of [3 * 60_000, 30 * 60_000, HOUR, 6 * HOUR, DAY, 7 * DAY, 30 * DAY]) {
      const from = at("2026-11-05T09:17:00");
      const ticks = timeTicks(from, from + span);
      expect(ticks.length).toBeGreaterThanOrEqual(3);
      expect(ticks.length).toBeLessThanOrEqual(6);
      for (const [i, t] of ticks.entries()) {
        expect(t.t).toBeGreaterThanOrEqual(from);
        expect(t.t).toBeLessThanOrEqual(from + span);
        if (i > 0) expect(t.t).toBeGreaterThan(ticks[i - 1]?.t ?? 0);
      }
    }
  });

  it("respects the tick limit", () => {
    const from = at("2026-11-05T00:00:00");
    expect(timeTicks(from, from + DAY, 4).length).toBeLessThanOrEqual(4);
  });

  it("adds the date to the first tick and at each new day when the range crosses days", () => {
    const from = at("2026-11-05T14:30:00");
    const ticks = timeTicks(from, from + DAY);
    expect(ticks[0]?.date).toBe("11/05");
    const midnight = ticks.find((t) => t.time === "00:00");
    expect(midnight?.date).toBe("11/06");
    expect(ticks.filter((t) => t.date).length).toBe(2);
  });

  it("shows no date within a single day", () => {
    const from = at("2026-11-05T09:00:00");
    const ticks = timeTicks(from, from + 4 * HOUR);
    expect(ticks.every((t) => t.date === undefined)).toBe(true);
    expect(ticks[0]?.time).toMatch(/^\d\d:\d\d$/);
  });

  it("labels multi-day ranges by date only, on local midnights", () => {
    const from = at("2026-11-01T10:00:00");
    const ticks = timeTicks(from, from + 7 * DAY);
    expect(ticks.length).toBeGreaterThanOrEqual(3);
    for (const t of ticks) {
      expect(t.time).toBe("");
      expect(t.date).toMatch(/^\d\d\/\d\d$/);
      expect(new Date(t.t).getHours()).toBe(0);
    }
  });

  it("returns nothing for an empty range", () => {
    expect(timeTicks(5, 5)).toEqual([]);
  });
});

describe("line segments", () => {
  const pts = (...v: (number | null)[]) => v.map((value, i) => ({ t: i * 10, v: value }));

  it("breaks the line at missing values instead of bridging them", () => {
    const segs = lineSegments(pts(1, 2, null, 3, 4, 5));
    expect(segs.map((s) => s.map((p) => p.v))).toEqual([[1, 2], [3, 4, 5]]);
  });

  it("breaks at spacings wider than the gap limit", () => {
    const segs = lineSegments([{ t: 0, v: 1 }, { t: 10, v: 2 }, { t: 100, v: 3 }, { t: 110, v: 4 }], 25);
    expect(segs.map((s) => s.length)).toEqual([2, 2]);
  });

  it("keeps a lone sample as its own run and ignores non-finite values", () => {
    expect(lineSegments(pts(null, 7, null)).map((s) => s.length)).toEqual([1]);
    expect(lineSegments(pts(Number.NaN, Number.POSITIVE_INFINITY))).toEqual([]);
  });
});

describe("stacked bars", () => {
  it("accumulates segments in series order and skips empty values", () => {
    expect(stackSegments([5, 0, 3, null, 2])).toEqual([
      { series: 0, from: 0, to: 5 },
      { series: 2, from: 5, to: 8 },
      { series: 4, from: 8, to: 10 },
    ]);
    expect(stackSegments([null, 0])).toEqual([]);
  });
});

describe("sampling", () => {
  it("finds the nearest sample time", () => {
    const times = [0, 10, 20, 40];
    expect(nearestIndex(times, -5)).toBe(0);
    expect(nearestIndex(times, 14)).toBe(1);
    expect(nearestIndex(times, 31)).toBe(3);
    expect(nearestIndex(times, 99)).toBe(3);
    expect(nearestIndex([], 1)).toBe(-1);
  });

  it("downsamples by bucket average and leaves empty buckets empty", () => {
    const points = Array.from({ length: 100 }, (_, i) => ({ t: i, v: i < 50 ? 10 : null }));
    const out = downsample(points, 0, 100, 10);
    expect(out.length).toBe(5);
    expect(out.every((p) => p.v === 10)).toBe(true);
    const few = [{ t: 1, v: 2 }];
    expect(downsample(few, 0, 10, 10)).toEqual(few);
  });

  it("windows series to the newest sample and never starts before the oldest", () => {
    const a = [0, 10, 20, 30].map((t) => ({ t, v: t }));
    const win = windowSeries([a], 15);
    expect(win?.from).toBe(15);
    expect(win?.to).toBe(30);
    expect(win?.series[0]?.map((p) => p.t)).toEqual([20, 30]);
    expect(windowSeries([a], 1000)?.from).toBe(0);
    expect(windowSeries([[]], 10)).toBeNull();
    expect(windowSeries([[{ t: 5, v: 1 }]], 10)?.to).toBeGreaterThan(5);
  });

  it("averages a long history into at most the requested number of buckets", () => {
    const a = Array.from({ length: 1000 }, (_, t) => ({ t, v: 1 }));
    const b = Array.from({ length: 1000 }, (_, t) => ({ t, v: t % 2 }));
    const win = windowSeries([a, b], 1000, 100);
    expect(win?.series[0]?.length).toBeLessThanOrEqual(100);
    // Series share bucket times so one tooltip can describe all of them.
    expect(win?.series[1]?.map((p) => p.t)).toEqual(win?.series[0]?.map((p) => p.t));
  });

  it("measures the median spacing", () => {
    expect(medianInterval([0, 10, 20, 30, 100])).toBe(10);
    expect(medianInterval([5])).toBeUndefined();
  });
});

describe("keyboard stepping", () => {
  it("moves with the arrows and clamps at the ends", () => {
    expect(stepIndex(null, "ArrowLeft", 5)).toBe(4);
    expect(stepIndex(null, "ArrowRight", 5)).toBe(0);
    expect(stepIndex(2, "ArrowLeft", 5)).toBe(1);
    expect(stepIndex(0, "ArrowLeft", 5)).toBe(0);
    expect(stepIndex(4, "ArrowRight", 5)).toBe(4);
  });

  it("jumps with Home and End, clears with Escape and ignores other keys", () => {
    expect(stepIndex(2, "Home", 5)).toBe(0);
    expect(stepIndex(2, "End", 5)).toBe(4);
    expect(stepIndex(2, "Escape", 5)).toBeNull();
    expect(stepIndex(2, "a", 5)).toBeUndefined();
    expect(stepIndex(null, "ArrowLeft", 0)).toBeUndefined();
  });
});
