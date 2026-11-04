import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { freshMachine } from "../test-fixtures";
import { MachineTrend } from "./trend";

describe("machine trend", () => {
  it("keeps missing CPU and invalid memory empty instead of plotting zero", () => {
    const sample = freshMachine().machine!.status!.snapshot!;
    const { container } = render(
      <MachineTrend
        snapshots={[
          { ...sample, id: 1, cpuPercent: null, memoryTotal: 0 },
          { ...sample, id: 2, cpuPercent: 25, memoryTotal: 100, memoryAvailable: 50 },
        ]}
      />,
    );
    expect(screen.getByRole("img", { name: "CPU 與記憶體使用率趨勢" })).toBeTruthy();
    expect(container.querySelectorAll("circle")).toHaveLength(1);
    expect(container.querySelectorAll("g path")).toHaveLength(1);
    expect(container.querySelectorAll("polyline")).toHaveLength(0);
  });
});
