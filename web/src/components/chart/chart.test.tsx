import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BarSeries, ChartFrame, ChartKey, ChartTooltip, LineSeries, RangeSwitch, ThresholdMark, TimeSeriesChart, TipRow, UsageBar, fmtBytes, fmtCelsius, fmtCount, fmtMs, fmtPercent, fmtRate, usageTone } from ".";
import type { RangeKey } from ".";
import { useState } from "react";
import { s } from "../../strings";

const T0 = Date.parse("2026-11-05T00:00:00Z");
const times = [T0, T0 + 3_600_000, T0 + 7_200_000];
const tip = (i: number) => <span>{`tip-${i}`}</span>;

function Sample({ values = [10, 20, 30] as (number | null)[] }) {
  return (
    <ChartFrame from={T0} to={T0 + 7_200_000} yMax={100} fixedMax={100} format={(v) => `${v}%`} times={times} tip={tip} label="CPU，最新 30%，24 小時">
      <LineSeries points={times.map((t, i) => ({ t, v: values[i] ?? null }))} color="var(--chart-1)" />
      <ThresholdMark value={90} label="90%" />
    </ChartFrame>
  );
}

describe("ChartFrame", () => {
  it("labels the chart with its value and range and draws axes", () => {
    const { container } = render(<Sample />);
    expect(screen.getByRole("group", { name: /CPU，最新 30%，24 小時/ })).toBeTruthy();
    expect(container.querySelectorAll(".chart-axis-y .chart-tick")).toHaveLength(5);
    expect(container.querySelectorAll(".chart-axis-x .chart-tick").length).toBeGreaterThanOrEqual(3);
    expect(container.querySelector(".chart-threshold")).toBeTruthy();
  });

  it("walks the samples with the arrow keys and announces each one", () => {
    render(<Sample />);
    const frame = screen.getByRole("group");
    expect(screen.queryByRole("status")).toBeNull();
    fireEvent.focus(frame);
    expect(screen.getByRole("status").textContent).toBe("tip-2");
    fireEvent.keyDown(frame, { key: "ArrowLeft" });
    expect(screen.getByRole("status").textContent).toBe("tip-1");
    fireEvent.keyDown(frame, { key: "Home" });
    expect(screen.getByRole("status").textContent).toBe("tip-0");
    fireEvent.keyDown(frame, { key: "ArrowLeft" });
    expect(screen.getByRole("status").textContent).toBe("tip-0");
    fireEvent.keyDown(frame, { key: "End" });
    expect(screen.getByRole("status").textContent).toBe("tip-2");
    fireEvent.keyDown(frame, { key: "Escape" });
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("closes the tooltip when focus leaves", () => {
    render(<Sample />);
    const frame = screen.getByRole("group");
    fireEvent.focus(frame);
    fireEvent.blur(frame);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("is not focusable without a tooltip", () => {
    render(
      <ChartFrame from={0} to={10} yMax={1} format={String} label="empty">
        <g />
      </ChartFrame>,
    );
    expect(screen.getByRole("group").getAttribute("tabindex")).toBeNull();
  });
});

describe("LineSeries", () => {
  it("draws one path per run and a dot for a lone sample", () => {
    const { container } = render(<Sample values={[10, null, 30]} />);
    expect(container.querySelectorAll("path.chart-path")).toHaveLength(0);
    expect(container.querySelectorAll("circle.chart-dot")).toHaveLength(2);
  });

  it("joins neighbouring samples into a single path", () => {
    const { container } = render(<Sample values={[10, 20, 30]} />);
    expect(container.querySelectorAll("path.chart-path")).toHaveLength(1);
    expect(container.querySelectorAll("circle.chart-dot")).toHaveLength(0);
  });

  it("marks the active sample", () => {
    const { container } = render(<Sample />);
    fireEvent.focus(screen.getByRole("group"));
    expect(container.querySelectorAll("circle.chart-active")).toHaveLength(1);
  });
});

describe("BarSeries", () => {
  function Bars({ stacked }: { stacked: boolean }) {
    return (
      <ChartFrame from={T0} to={T0 + 7_200_000} yMax={10} format={String} label="bars" times={times} tip={tip}>
        <BarSeries
          times={times}
          stacked={stacked}
          barMs={3_000_000}
          series={[
            { color: "var(--chart-1)", values: [4, 0, 5] },
            { color: "var(--chart-3)", values: [2, 3, null] },
          ]}
        />
      </ChartFrame>
    );
  }

  it("stacks series into one bar per time and skips empty segments", () => {
    const { container } = render(<Bars stacked />);
    // Bar 0: two segments, bar 1: only the second series, bar 2: only the first.
    expect(container.querySelectorAll("rect.chart-bar.stacked")).toHaveLength(4);
    const rects = [...container.querySelectorAll("rect.chart-bar.stacked")];
    const first = rects.slice(0, 2).map((r) => ({ y: Number(r.getAttribute("y")), h: Number(r.getAttribute("height")) }));
    // The second segment sits on top of the first without overlapping it.
    expect((first[1]?.y ?? 0) + (first[1]?.h ?? 0)).toBeCloseTo(first[0]?.y ?? 0, 0);
  });

  it("places series side by side when not stacked", () => {
    const { container } = render(<Bars stacked={false} />);
    const bars = [...container.querySelectorAll("rect.chart-bar")];
    expect(bars).toHaveLength(4);
    expect(Number(bars[0]?.getAttribute("x"))).toBeLessThan(Number(bars[1]?.getAttribute("x")));
  });
});

describe("RangeSwitch", () => {
  it("shows the three ranges and reports the pressed one", () => {
    function Host() {
      const [value, setValue] = useState<RangeKey>("24h");
      return <RangeSwitch value={value} onChange={setValue} />;
    }
    render(<Host />);
    expect(screen.getByRole("button", { name: "24 小時" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "7 天" }));
    expect(screen.getByRole("button", { name: "7 天" }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: "24 小時" }).getAttribute("aria-pressed")).toBe("false");
    expect(screen.getByRole("button", { name: "30 天" })).toBeTruthy();
  });
});

describe("UsageBar", () => {
  it("picks the tone from the limits", () => {
    expect(usageTone(50, {})).toBe("neutral");
    expect(usageTone(50, { warnAt: 80, critAt: 90 })).toBe("ok");
    expect(usageTone(80, { warnAt: 80, critAt: 90 })).toBe("warn");
    expect(usageTone(90, { warnAt: 80, critAt: 90 })).toBe("bad");
    expect(usageTone(120, { warnAt: 80, critAt: 90 })).toBe("bad");
  });

  it("exposes the value, range and threshold as a meter", () => {
    const { container } = render(<UsageBar value={85} label="磁碟 /" threshold={90} />);
    const meter = screen.getByRole("meter", { name: "磁碟 /" });
    expect(meter.getAttribute("aria-valuenow")).toBe("85");
    expect(meter.getAttribute("aria-valuemax")).toBe("100");
    expect(meter.getAttribute("aria-valuetext")).toContain("85.0%");
    expect(container.querySelector(".usage-fill.warn")).toBeTruthy();
    expect((container.querySelector(".usage-mark") as HTMLElement).style.left).toBe("90%");
  });

  it("marks a breach in text as well as colour", () => {
    const { container } = render(<UsageBar value={95} label="記憶體" threshold={90} />);
    expect(container.querySelector(".usage-fill.bad")).toBeTruthy();
    expect(container.querySelector(".usage-text")?.textContent).toContain("✕");
  });

  it("shows unknown values without a bar and caps the fill at the maximum", () => {
    const { container, rerender } = render(<UsageBar value={null} label="x" />);
    expect(container.querySelector("[role=meter]")).toBeNull();
    expect(container.textContent).toBe("未知");
    rerender(<UsageBar value={140} label="x" />);
    expect((container.querySelector(".usage-fill") as HTMLElement).style.width).toBe("100%");
  });
});

describe("value formatters", () => {
  it("trims trailing zeros and keeps one decimal at most", () => {
    expect(fmtPercent(12.5)).toBe("12.5%");
    expect(fmtPercent(30)).toBe("30%");
    expect(fmtPercent(33.333)).toBe("33.3%");
    expect(fmtCelsius(47.04)).toBe("47 °C");
    expect(fmtCelsius(47.25)).toBe("47.3 °C");
  });

  it("abbreviates large counts but not small ones", () => {
    expect(fmtCount(0)).toBe("0");
    expect(fmtCount(9_999)).toBe("9999");
    expect(fmtCount(10_000)).toBe("10k");
    expect(fmtCount(12_500)).toBe("12.5k");
  });

  it("formats durations, sizes and rates with their units", () => {
    expect(fmtMs(250)).toBe("250ms");
    expect(fmtBytes(1024)).toMatch(/KiB|KB/);
    expect(fmtRate(1024)).toBe(`${fmtBytes(1024)}/s`);
  });
});

describe("ChartTooltip and ChartKey", () => {
  it("sits right of the cursor in the left part of the chart and flips left in the right part", () => {
    const view = render(
      <ChartTooltip x={100} width={400}>
        value
      </ChartTooltip>,
    );
    expect(screen.getByRole("status").className).not.toContain("left");
    view.rerender(
      <ChartTooltip x={300} width={400}>
        value
      </ChartTooltip>,
    );
    expect(screen.getByRole("status").className).toContain("left");
  });

  it("shows a tooltip row as colour key, name and value", () => {
    const { container } = render(<TipRow color="red" name="CPU" value="12%" dashed />);
    expect(screen.getByText("CPU")).toBeTruthy();
    expect(screen.getByText("12%").tagName).toBe("STRONG");
    expect(container.querySelector(".tip-key.dashed")).toBeTruthy();
  });

  it("names every series and tells lines, dashed lines and bars apart by shape", () => {
    const { container } = render(
      <ChartKey
        items={[
          { name: "Requests", color: "var(--chart-1)", swatch: true },
          { name: "Down", color: "var(--chart-2)", dashed: true },
          { name: "Up", color: "var(--chart-3)" },
        ]}
      />,
    );
    expect(screen.getByText("Requests")).toBeTruthy();
    expect(container.querySelector(".key.bar")).toBeTruthy();
    expect(container.querySelector(".key.series-dashed")).toBeTruthy();
    expect(container.querySelector(".key.series")).toBeTruthy();
    // The colour key is decoration: the name carries the identity.
    expect([...container.querySelectorAll("i")].every((i) => i.getAttribute("aria-hidden") === "true")).toBe(true);
  });
});

describe("TimeSeriesChart", () => {
  const point = (i: number, v: number | null) => ({ t: T0 + i * 3_600_000, v });
  const cpu = { name: "CPU", color: "var(--chart-1)", points: [point(0, 10), point(1, 40), point(2, 30)] };
  const memory = { name: "Memory", color: "var(--chart-2)", dashed: true, points: [point(0, 50), point(1, null), point(2, 70)] };
  const base = { from: T0, to: T0 + 7_200_000, format: (v: number) => `${v}%` };

  it("names the chart with the latest value of each series and its range", () => {
    render(<TimeSeriesChart name="Usage" series={[cpu, memory]} {...base} />);
    const frame = screen.getByRole("group", { name: /Usage/ });
    expect(frame.getAttribute("aria-label")).toContain("CPU 30%");
    expect(frame.getAttribute("aria-label")).toContain("Memory 70%");
  });

  it("says a series has no value instead of showing zero", () => {
    render(<TimeSeriesChart name="Usage" series={[{ ...cpu, points: [point(0, null)] }]} {...base} />);
    expect(screen.getByRole("group", { name: /Usage/ }).getAttribute("aria-label")).toContain(`CPU ${s.chart.noValue}`);
  });

  it("shows a legend only when there is more than one series", () => {
    const view = render(<TimeSeriesChart name="Usage" series={[cpu]} {...base} />);
    expect(view.container.querySelector(".chart-legend")).toBeNull();
    view.rerender(<TimeSeriesChart name="Usage" series={[cpu, memory]} {...base} />);
    expect(within(view.container.querySelector(".chart-legend") as HTMLElement).getByText("Memory")).toBeTruthy();
  });

  it("lists every series at the focused time and marks a missing sample", () => {
    render(<TimeSeriesChart name="Usage" series={[cpu, memory]} {...base} />);
    const frame = screen.getByRole("group", { name: /Usage/ });
    fireEvent.focus(frame);
    // Focus starts on the latest sample.
    let tip = screen.getByRole("status");
    expect(within(tip).getByText("CPU").parentElement?.textContent).toContain("30%");
    expect(within(tip).getByText("Memory").parentElement?.textContent).toContain("70%");
    fireEvent.keyDown(frame, { key: "ArrowLeft" });
    tip = screen.getByRole("status");
    expect(within(tip).getByText("CPU").parentElement?.textContent).toContain("40%");
    expect(within(tip).getByText("Memory").parentElement?.textContent).toContain(s.chart.noValue);
  });

  it("draws a limit line for each threshold", () => {
    const { container } = render(<TimeSeriesChart name="Usage" series={[cpu]} {...base} thresholds={[{ value: 90, label: "90%" }]} />);
    expect(container.querySelector(".chart-threshold")).toBeTruthy();
  });
});
