import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Deployment } from "../../api/types";
import { DeploymentChart, DeploymentsTab, chartedDeployments } from "./deployments";
import { jsonResponse } from "./test-fixtures";

const T0 = Date.parse("2026-11-05T00:00:00Z");
const deployment = (id: number, minutes: number, seconds: number | null, outcome: Deployment["outcome"]): Deployment => ({
  id,
  resource: "web",
  action: "deploy",
  version: `abcdef${id}0123456789`,
  previousVersion: "",
  startedAt: new Date(T0 + minutes * 60_000).toISOString(),
  finishedAt: seconds === null ? null : new Date(T0 + minutes * 60_000 + seconds * 1000).toISOString(),
  outcome,
  actor: { kind: "user", id: "u", name: "Test admin" },
  runId: "",
  error: "",
  logExcerpt: "",
});

afterEach(() => vi.unstubAllGlobals());

describe("deployment chart", () => {
  it("charts the latest deployments oldest first", () => {
    const items = Array.from({ length: 40 }, (_, i) => deployment(i, 40 - i, 10, "succeeded"));
    const shown = chartedDeployments(items);
    expect(shown).toHaveLength(30);
    expect(Date.parse(shown[0]!.startedAt)).toBeLessThan(Date.parse(shown[29]!.startedAt));
    expect(shown.at(-1)?.id).toBe(0);
  });

  it("draws a bar per deployment coloured by outcome", () => {
    const items = [deployment(3, 30, 20, "failed"), deployment(2, 20, 60, "succeeded"), deployment(1, 10, 5, "succeeded")];
    const { container } = render(<DeploymentChart items={items} />);
    const fills = [...container.querySelectorAll("rect.chart-bar")].map((r) => r.getAttribute("fill"));
    expect(fills).toEqual(["var(--success)", "var(--success)", "var(--destructive)"]);
    // The tallest bar is the 60 s deployment.
    const heights = [...container.querySelectorAll("rect.chart-bar")].map((r) => Number(r.getAttribute("height")));
    expect(heights[1]).toBe(Math.max(...heights));
    expect(screen.getByRole("group", { name: /部署耗時/ })).toBeTruthy();
  });

  it("describes a deployment by keyboard", () => {
    render(<DeploymentChart items={[deployment(2, 20, 60, "succeeded"), deployment(1, 10, 5, "failed")]} />);
    const frame = screen.getByRole("group", { name: /部署耗時/ });
    fireEvent.focus(frame);
    fireEvent.keyDown(frame, { key: "Home" });
    expect(screen.getByRole("status").textContent).toContain("失敗");
    expect(screen.getByRole("status").textContent).toContain("5s");
  });
});

describe("deployments tab", () => {
  it("shows the success rate as a bar next to the history", async () => {
    vi.stubGlobal("fetch", async () => jsonResponse({ items: [deployment(3, 30, 20, "failed"), deployment(2, 20, 60, "succeeded"), deployment(1, 10, 5, "succeeded")], nextCursor: null }));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <DeploymentsTab resource="web" deployedVersion="" hasSource={false} onDeploy={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const meter = await screen.findByRole("meter", { name: "成功率" });
    expect(meter.getAttribute("aria-valuenow")).toBe("66.67");
    expect(screen.getByText("67% (2/3)")).toBeTruthy();
  });
});
