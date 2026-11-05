import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { TrafficPoint, TrafficSeries } from "../../api/security";
import { TrafficCharts, requestSplit } from "./traffic-charts";

const START = Date.parse("2026-11-02T00:00:00Z");
const point = (minute: number, requests: number, status429 = 0, status5xx = 0): TrafficPoint => ({
  windowStart: new Date(START + minute * 60_000).toISOString(),
  windowEnd: new Date(START + (minute + 1) * 60_000).toISOString(),
  requests,
  status429,
  status5xx,
  dryRunHits: 0,
  requestBytes: 1024 * (minute + 1),
  responseBytes: 4096,
  observedWindows: 1,
  requestTimeSumMs: 10,
  requestTimeMaxMs: 5,
  averageMs: 1,
  requestsPerSecond: requests / 60,
  observedSeconds: 60,
});
const series = (items: TrafficPoint[]): TrafficSeries => ({ items, from: new Date(START).toISOString(), to: new Date(START + 10 * 60_000).toISOString(), bucketSeconds: 60 });

describe("traffic charts", () => {
  it("splits requests so 429 and 5xx are not counted as normal", () => {
    expect(requestSplit({ requests: 10, status429: 3, status5xx: 2 })).toEqual({ normal: 5, status429: 3, status5xx: 2 });
    expect(requestSplit({ requests: 1, status429: 3, status5xx: 2 }).normal).toBe(0);
  });

  it("draws one stacked chart for requests and one chart for bytes", () => {
    const { container } = render(<TrafficCharts data={series([point(0, 10, 2, 1), point(1, 20), point(2, 5, 0, 5)])} />);
    expect(screen.getAllByRole("group")).toHaveLength(2);
    // Segments: bar 0 has three, bar 1 one, bar 2 only the 5xx part (normal is 0).
    expect(container.querySelectorAll("rect.chart-bar.stacked")).toHaveLength(5);
    expect(container.querySelectorAll("path.chart-path")).toHaveLength(2);
  });

  it("leaves a missing bucket empty and breaks the bytes lines there", () => {
    const { container } = render(<TrafficCharts data={series([point(0, 1), point(1, 1), point(5, 1), point(6, 1)])} />);
    expect(container.querySelectorAll("rect.chart-bar.stacked")).toHaveLength(4);
    // Two series, two runs each.
    expect(container.querySelectorAll("path.chart-path")).toHaveLength(4);
  });

  it("describes a bucket by keyboard", () => {
    render(<TrafficCharts data={series([point(0, 10, 2, 1), point(1, 20)])} />);
    const [requests] = screen.getAllByRole("group");
    fireEvent.focus(requests!);
    fireEvent.keyDown(requests!, { key: "Home" });
    const tip = screen.getByRole("status").textContent ?? "";
    expect(tip).toContain("HTTP 429");
    expect(tip).toContain("一般請求");
    expect(tip).toContain("7");
  });
});
