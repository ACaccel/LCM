import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BarSeries, ChartFrame, LineSeries, RangeSwitch, ThresholdMark, UsageBar, usageTone } from ".";
import type { RangeKey } from ".";
import { useState } from "react";

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
