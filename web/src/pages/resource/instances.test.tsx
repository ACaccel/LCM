import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { ServiceInstance } from "../../api/types";
import { InstancesPanel } from "./overview";
import { resourceFixture } from "./test-fixtures";

const instance = (over: Partial<ServiceInstance>): ServiceInstance => ({
  name: "alpha",
  port: 4100,
  state: "active",
  enabled: true,
  stopped: false,
  subState: "running",
  pid: 42,
  observedAt: "2026-11-11T08:00:00Z",
  health: "healthy",
  checks: ["alpha-relay"],
  actions: [
    { name: "start", available: true, reason: "" },
    { name: "stop", available: true, reason: "" },
    { name: "restart", available: true, reason: "" },
  ],
  ...over,
});

describe("InstancesPanel", () => {
  it("shows each instance's state, port, health and the actions it allows", () => {
    const qc = new QueryClient();
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <InstancesPanel
            detail={resourceFixture({ name: "bots" })}
            instances={[
              instance({ actions: [{ name: "start", available: true, reason: "" }, { name: "stop", available: false, reason: "lastInstance" }, { name: "restart", available: true, reason: "" }] }),
              instance({ name: "log-archive", port: null, state: "inactive", enabled: false, stopped: true, health: "unknown", checks: [] }),
              instance({ name: "gamma", port: 4001, state: "inactive", enabled: false, health: "down", observedAt: null }),
            ]}
          />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const rows = screen.getAllByRole("row");
    const alpha = within(rows[1]!);
    expect(alpha.getByText("alpha")).toBeTruthy();
    expect(alpha.getByText("4100")).toBeTruthy();
    expect(alpha.getByText("執行中")).toBeTruthy();
    const stop = alpha.getByRole("button", { name: "停止" }) as HTMLButtonElement;
    expect(stop.disabled).toBe(true);
    expect(stop.title).toContain("最後一個執行中的實例");
    const archive = within(rows[2]!);
    expect(archive.getByText("已停用（由操作者停止）")).toBeTruthy();
    expect(archive.getByText("未執行")).toBeTruthy();
    // An instance without a unit yet was not stopped by an operator.
    expect(within(rows[3]!).queryByText("已停用（由操作者停止）")).toBeNull();
  });
});
