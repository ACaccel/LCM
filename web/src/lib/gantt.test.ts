import { describe, expect, it } from "vitest";
import type { RunNode } from "../api/types";
import { buildGantt, firstFailedNode } from "./gantt";

const T0 = Date.parse("2026-11-05T01:00:00Z");
const at = (seconds: number) => new Date(T0 + seconds * 1000).toISOString();
const node = (id: string, status: RunNode["status"], start?: number, end?: number): RunNode => ({
  id,
  type: "core.http",
  status,
  ...(start === undefined ? {} : { startedAt: at(start) }),
  ...(end === undefined ? {} : { finishedAt: at(end) }),
});

describe("run gantt", () => {
  it("places nodes relative to the first start and sizes the axis to the last end", () => {
    const g = buildGantt([node("a", "completed", 10, 12), node("b", "completed", 12, 20)], T0 + 60_000);
    expect(g.rows.map((r) => [r.id, r.start, r.end])).toEqual([
      ["a", 0, 2000],
      ["b", 2000, 10000],
    ]);
    expect(g.total).toBe(10000);
  });

  it("skips nodes that never started and keeps run order for equal starts", () => {
    const g = buildGantt([node("p", "pending"), node("x", "completed", 0, 1), node("y", "completed", 0, 3)], T0);
    expect(g.rows.map((r) => r.id)).toEqual(["x", "y"]);
  });

  it("runs an unfinished node up to now and flags it as open", () => {
    const g = buildGantt([node("a", "completed", 0, 5), node("b", "running", 5)], T0 + 9000);
    const b = g.rows[1];
    expect(b?.open).toBe(true);
    expect(b?.end).toBe(9000);
    expect(g.total).toBe(9000);
  });

  it("gives a started node without an end no length when it is not running", () => {
    const g = buildGantt([node("a", "failed", 3)], T0 + 99_000);
    expect(g.rows[0]).toMatchObject({ start: 0, end: 0, open: false });
    expect(g.total).toBe(1);
  });

  it("returns an empty chart before anything started", () => {
    expect(buildGantt([], T0)).toEqual({ rows: [], total: 1 });
  });
});

describe("firstFailedNode", () => {
  it("returns the first failed or dead node in run order", () => {
    expect(firstFailedNode([node("a", "completed"), node("b", "dead"), node("c", "failed")])).toBe("b");
    expect(firstFailedNode([node("a", "completed", 0, 1), node("outer/inner", "failed", 1, 2)])).toBe("outer/inner");
  });

  it("returns null when nothing failed", () => {
    expect(firstFailedNode([node("a", "completed"), node("b", "running")])).toBeNull();
    expect(firstFailedNode([])).toBeNull();
  });
});
