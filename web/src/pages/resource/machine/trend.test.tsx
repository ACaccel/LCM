import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { freshMachine } from "../test-fixtures";
import { MachineTrend, UsageTrend, thresholdLines } from "./trend";

const sample = freshMachine().machine!.status!.snapshot!;
const at = (minutes: number) => new Date(Date.parse("2026-11-05T00:00:00Z") + minutes * 60_000).toISOString();

describe("machine trend", () => {
  it("keeps missing CPU and invalid memory empty instead of plotting zero", () => {
    const { container } = render(
      <MachineTrend
        snapshots={[
          { ...sample, id: 1, observedAt: at(0), cpuPercent: null, memoryTotal: 0 },
          { ...sample, id: 2, observedAt: at(1), cpuPercent: 25, memoryTotal: 100, memoryAvailable: 50 },
        ]}
      />,
    );
    expect(screen.getByRole("group", { name: /CPU 與記憶體使用率趨勢，CPU 使用率 25%，記憶體使用率 50%/ })).toBeTruthy();
    // Each series has a single valid sample, drawn as a dot; nothing is joined to the missing first sample.
    expect(container.querySelectorAll("circle.chart-dot")).toHaveLength(2);
    expect(container.querySelectorAll("path.chart-path")).toHaveLength(0);
  });

  it("breaks lines where observations are missing", () => {
    const points = [0, 1, 2, 30, 31, 32].map((m, i) => ({ id: i, observedAt: at(m), cpu: 10 + i, memory: 50 }));
    const { container } = render(<UsageTrend points={points} />);
    // CPU and memory: two runs each.
    expect(container.querySelectorAll("path.chart-path")).toHaveLength(4);
  });

  it("shows the limits as dashed lines and merges equal ones", () => {
    expect(thresholdLines({ cpu: 90, memory: 90 })).toHaveLength(1);
    expect(thresholdLines({ cpu: 90, memory: 80 })).toHaveLength(2);
    expect(thresholdLines(undefined)).toEqual([]);
    const points = [0, 1].map((m, i) => ({ id: i, observedAt: at(m), cpu: 10, memory: 20 }));
    const { container } = render(<UsageTrend points={points} thresholds={{ cpu: 90, memory: 90 }} />);
    expect(container.querySelectorAll(".chart-threshold")).toHaveLength(1);
  });

  it("limits the samples to the chosen range, ending at the newest one", () => {
    const day = 24 * 60;
    const points = [0, 2 * day, 7 * day - 10, 7 * day].map((m, i) => ({ id: i, observedAt: at(m), cpu: 10, memory: 20 }));
    const { container } = render(<UsageTrend points={points} />);
    // Within 24 hours only the last two samples remain.
    expect(container.querySelectorAll("path.chart-path")).toHaveLength(2);
    fireEvent.click(screen.getByRole("button", { name: "7 天" }));
    // The older samples are back: one line per series through all four samples.
    const paths = [...container.querySelectorAll("path.chart-path")];
    expect(paths).toHaveLength(2);
    expect(paths[0]?.getAttribute("d")?.match(/L/g)).toHaveLength(3);
  });

  it("walks the samples with the arrow keys", () => {
    const points = [0, 1, 2].map((m, i) => ({ id: i, observedAt: at(m), cpu: 10 * (i + 1), memory: 20 }));
    render(<UsageTrend points={points} />);
    const frame = screen.getByRole("group", { name: /CPU 與記憶體使用率趨勢/ });
    fireEvent.focus(frame);
    expect(screen.getByRole("status").textContent).toContain("30%");
    fireEvent.keyDown(frame, { key: "ArrowLeft" });
    expect(screen.getByRole("status").textContent).toContain("20%");
  });

  it("renders nothing without samples", () => {
    const { container } = render(<UsageTrend points={[]} />);
    expect(container.textContent).toBe("");
  });
});
