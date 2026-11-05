import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { SeriesPoint } from "../api/types";
import { LatencyChart, UptimeStrip, stripState } from "./charts";

const FROM = Date.parse("2026-11-05T00:00:00Z");
const HOUR = 3_600_000;
const point = (hour: number, over: Partial<SeriesPoint> = {}): SeriesPoint => ({
  time: new Date(FROM + hour * HOUR).toISOString(),
  count: 10,
  failures: 0,
  avgLatencyMs: 120,
  minLatencyMs: 80,
  maxLatencyMs: 300,
  ...over,
});

describe("uptime strip", () => {
  it("classifies buckets by how much of them failed", () => {
    expect(stripState({ ratio: null, failures: 0 })).toBe("none");
    expect(stripState({ ratio: 1, failures: 0 })).toBe("ok");
    expect(stripState({ ratio: 0.95, failures: 1 })).toBe("warn");
    expect(stripState({ ratio: 0.4, failures: 6 })).toBe("bad");
  });

  it("draws one cell per bucket, each naming its state in the title", () => {
    const { container } = render(<UptimeStrip buckets={[{ ratio: 1, failures: 0 }, { ratio: null, failures: 0 }, { ratio: 0.5, failures: 5 }]} from={FROM} to={FROM + 3 * HOUR} />);
    const cells = [...container.querySelectorAll(".uptime-strip i")];
    expect(cells.map((c) => c.className)).toEqual(["ok", "none", "bad"]);
    expect(cells[1]?.getAttribute("title")).toContain("此區間沒有資料");
    expect(cells[2]?.getAttribute("title")).toContain("50.0%");
  });
});

describe("latency chart", () => {
  const points = [point(0), point(1), point(2, { failures: 10, count: 10 }), point(3), point(4, { avgLatencyMs: 2400, maxLatencyMs: 2600 })];

  it("scales to its own values and labels the axes with units", () => {
    const { container } = render(<LatencyChart name="http" points={points} from={FROM} to={FROM + 5 * HOUR} stepMs={HOUR} />);
    const ticks = [...container.querySelectorAll(".chart-axis-y .chart-tick")].map((t) => t.textContent);
    expect(ticks[0]).toBe("0ms");
    // 2.4 s of latency needs an axis in seconds, not the old fixed 50 ms.
    expect(ticks.at(-1)).toMatch(/s$/);
    expect(container.querySelectorAll(".chart-axis-x .chart-tick").length).toBeGreaterThanOrEqual(3);
  });

  it("breaks the line at the failed bucket and marks it", () => {
    const { container } = render(<LatencyChart name="http" points={points} from={FROM} to={FROM + 5 * HOUR} stepMs={HOUR} />);
    expect(container.querySelectorAll("path.chart-path")).toHaveLength(2);
    expect(container.querySelectorAll(".chart-marks line")).toHaveLength(1);
    expect(container.querySelectorAll(".uptime-strip i.bad, .uptime-strip i.warn").length).toBeGreaterThan(0);
  });

  it("names the latest value and range and walks the buckets by keyboard", () => {
    render(<LatencyChart name="http" points={points} from={FROM} to={FROM + 5 * HOUR} stepMs={HOUR} />);
    const frame = screen.getByRole("group", { name: /http 延遲，最新 2s/ });
    fireEvent.focus(frame);
    expect(screen.getByRole("status").textContent).toContain("2s");
    fireEvent.keyDown(frame, { key: "ArrowLeft" });
    fireEvent.keyDown(frame, { key: "ArrowLeft" });
    expect(screen.getByRole("status").textContent).toContain("全部失敗");
  });

  it("shows an empty note without data", () => {
    render(<LatencyChart points={[]} from={FROM} to={FROM + HOUR} stepMs={HOUR} />);
    expect(screen.getByText("此區間沒有資料")).toBeTruthy();
  });
});
