import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ResourceDetail, ResourceImpact } from "../api/types";
import { ToastProvider } from "../components/toast";
import { stubGraphEnvironment } from "../test-graph-env";
import { ResourceDetailPage, tabsFor } from "./resource-detail";
import { freshMachine, jsonResponse, noImpact, resourceFixture, routerFixture } from "./resource/test-fixtures";

interface Write {
  method: string;
  path: string;
  body: unknown;
}

function setup(detail: ResourceDetail, entry: string, impact: ResourceImpact = noImpact(detail.name)) {
  const writes: Write[] = [];
  const reads: string[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    const path = decodeURIComponent(url.pathname);
    if (req.method !== "GET") {
      const text = await req.text();
      writes.push({ method: req.method, path, body: text ? JSON.parse(text) : undefined });
      return jsonResponse({ ...detail, phase: "retired" });
    }
    reads.push(path);
    if (path.endsWith("/impact")) return jsonResponse({ ...impact, ...(url.searchParams.get("action") ? { action: url.searchParams.get("action") } : { acknowledgementRequired: false }) });
    if (path === "/api/v1/resources") return jsonResponse({ items: [], nextCursor: null });
    if (path.endsWith("/series")) {
      const to = Date.now();
      return jsonResponse({ resource: detail.name, check: "http", from: new Date(to - 86_400_000).toISOString(), to: new Date(to).toISOString(), stepMs: 900_000, points: [] });
    }
    if (path.endsWith("/summary")) return jsonResponse({ resource: detail.name, window: "24h", uptime: 1, count: 10, failures: 0, checks: {} });
    if (path.endsWith("/timeline")) return jsonResponse({ items: [], nextCursor: null });
    if (path.endsWith("/history") || path.endsWith("/events") || path.endsWith("/operations") || path.endsWith("/capabilities")) {
      return jsonResponse({ items: [], nextCursor: null });
    }
    return jsonResponse(detail);
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <MemoryRouter initialEntries={[entry]}>
          <Routes>
            <Route path="/resources/:name/:tab?" element={<ResourceDetailPage />} />
            <Route path="/resources" element={<p>list</p>} />
          </Routes>
        </MemoryRouter>
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { writes, reads };
}

const tabNames = () => within(screen.getByRole("navigation")).getAllByRole("link").map((a) => a.textContent);

afterEach(() => vi.unstubAllGlobals());

describe("type-specific tabs", () => {
  it("puts the panels of each type between the common tabs", () => {
    expect(tabsFor("service")).toEqual(["overview", "deployments", "backups", "checks", "timeline", "policies", "relations", "config"]);
    expect(tabsFor("machine")).toEqual(["overview", "maintenance", "checks", "timeline", "policies", "relations", "config"]);
    expect(tabsFor("router")).toEqual(["overview", "operations", "checks", "timeline", "policies", "relations", "config"]);
  });

  it("shows the router's status and charts in the overview and no service panels", async () => {
    const { reads } = setup(routerFixture, "/resources/gateway");
    expect(await screen.findByText("RA70")).toBeTruthy();
    expect(screen.getAllByText("12.5%").length).toBeGreaterThan(0);
    expect(tabNames()).toEqual(["總覽", "操作", "檢查", "時間軸", "Policy", "關係", "文件"]);
    await waitFor(() => expect(reads).toContain("/api/v1/resources/gateway/history"));
    expect(screen.queryByText("最近部署")).toBeNull();
  });

  it("does not repeat the header's phase, state and health in the overview", async () => {
    setup(freshMachine(), "/resources/workstation");
    await screen.findByRole("heading", { name: "資源使用" });
    // The phase pill is in the header only, and the identity list has no row for it.
    expect(screen.getAllByText("納管中")).toHaveLength(1);
    expect(screen.queryByText("階段", { exact: true })).toBeNull();
    expect(screen.getAllByText("健康", { exact: true })).toHaveLength(1);
  });

  it("shows a service's check summary, uptime strip and latest deployment", async () => {
    const detail = resourceFixture({
      checks: [
        { name: "http", type: "http", critical: true, intervalMs: 30_000, status: "up", stale: false, since: null, message: "", lastResult: "2026-11-01T00:00:00Z", lastLatencyMs: 12, consecutiveFailures: 0 },
        { name: "tcp", type: "tcp", critical: false, intervalMs: 30_000, status: "down", stale: false, since: null, message: "refused", lastResult: "2026-11-01T00:00:00Z", lastLatencyMs: 0, consecutiveFailures: 3 },
      ],
      service: {
        deployedVersion: "abcdef0123456789",
        lastDeployment: { id: 1, resource: "web", action: "deploy", version: "abcdef0123456789", previousVersion: "", startedAt: "2026-11-01T00:00:00Z", finishedAt: "2026-11-01T00:01:00Z", outcome: "succeeded", actor: { kind: "user", id: "u", name: "Test admin" }, runId: "", error: "", logExcerpt: "" },
      },
    });
    setup(detail, "/resources/web");
    expect(await screen.findByText("1/2 項檢查通過")).toBeTruthy();
    expect(screen.getByRole("heading", { name: "最近部署" })).toBeTruthy();
    expect(screen.getAllByText("abcdef0").length).toBeGreaterThan(0);
    await waitFor(() => expect(screen.getByRole("img", { name: "可用率分段" })).toBeTruthy());
  });

  it("sends the old monitoring URL of a machine to the overview", async () => {
    setup(freshMachine(), "/resources/workstation/monitoring");
    expect(await screen.findByRole("heading", { name: "資源使用" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "總覽" }).getAttribute("aria-current")).toBe("page");
  });

  it("sends the old events URL to the timeline with the observation filter", async () => {
    const { reads } = setup(freshMachine(), "/resources/workstation/events");
    const filter = (await screen.findByRole("checkbox", { name: "觀測事件" })) as HTMLInputElement;
    expect(filter.checked).toBe(true);
    expect(screen.getByRole("link", { name: "時間軸" }).getAttribute("aria-current")).toBe("page");
    await waitFor(() => expect(reads).toContain("/api/v1/resources/workstation/events"));
    expect(tabNames()).not.toContain("事件");
    // Only observation events were asked for.
    expect(reads.filter((p) => p.endsWith("/timeline"))).toHaveLength(0);
  });

  it("offers the observation filter on the timeline of a machine or router but not of a service", async () => {
    setup(resourceFixture(), "/resources/web/timeline");
    expect(await screen.findByRole("checkbox", { name: "部署" })).toBeTruthy();
    expect(screen.queryByRole("checkbox", { name: "觀測事件" })).toBeNull();
  });

  it("falls back to the overview for a tab the type does not have", async () => {
    setup(resourceFixture(), "/resources/web/monitoring");
    expect(await screen.findByRole("link", { name: "總覽" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "總覽" }).getAttribute("aria-current")).toBe("page");
    expect(tabNames()).toContain("部署");
    expect(tabNames()).not.toContain("監控");
  });

  it("shows an archive read only, without actions", async () => {
    setup(resourceFixture({ phase: "unmanaged", id: "old-1", actions: [] }), "/resources/@old-1");
    expect(await screen.findByText("這是已解除納管的封存紀錄，只能檢視。")).toBeTruthy();
    expect(screen.queryByRole("group", { name: "可執行的動作" })).toBeNull();
  });
});

describe("phase controls", () => {
  it.each([
    { label: "退役", phase: "managed" as const, body: { phase: "retired", version: 3, reason: "Hardware swap" } },
    { label: "恢復納管", phase: "retired" as const, body: { phase: "managed", version: 3, reason: "Hardware swap" } },
  ])("confirms $label for the named target before changing the phase", async ({ label, phase, body }) => {
    const machine = freshMachine();
    machine.phase = phase;
    machine.actions = machine.actions.map((a) => (a.kind === "phase" ? { ...a, available: true, reason: "" } : a));
    const { writes } = setup(machine, "/resources/workstation");
    const group = await screen.findByRole("group", { name: "可執行的動作" });
    // Only the change that applies to the current phase is offered.
    expect(within(group).queryByRole("button", { name: phase === "managed" ? "恢復納管" : "退役" })).toBeNull();
    await userEvent.click(within(group).getByRole("button", { name: label }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("workstation");
    expect(dialog.textContent).toContain("不會關機");
    await userEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(writes).toHaveLength(0);
    await userEvent.click(within(group).getByRole("button", { name: label }));
    await userEvent.type(screen.getByLabelText("理由（選填）"), "Hardware swap");
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "確認" }));
    await waitFor(() => expect(writes).toEqual([{ method: "POST", path: "/api/v1/resources/workstation/phase", body }]));
  });

  it("keeps an unavailable phase change visible with the server's reason", async () => {
    setup(freshMachine(), "/resources/workstation");
    const group = await screen.findByRole("group", { name: "可執行的動作" });
    const unmanage = within(group).getByRole("button", { name: "解除納管" }) as HTMLButtonElement;
    expect(unmanage.disabled).toBe(true);
    expect(unmanage.title).toBe("machine must be retired first");
  });

  it("returns to the list after unmanaging", async () => {
    const machine = freshMachine();
    machine.phase = "retired";
    machine.actions = machine.actions.map((a) => (a.name === "unmanage" ? { ...a, available: true, reason: "" } : a));
    const { writes } = setup(machine, "/resources/workstation");
    const group = await screen.findByRole("group", { name: "可執行的動作" });
    await userEvent.click(within(group).getByRole("button", { name: "解除納管" }));
    expect(screen.getByRole("dialog").textContent).toContain("釋出名稱");
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "確認" }));
    expect(await screen.findByText("list")).toBeTruthy();
    expect(writes[0]).toEqual({ method: "POST", path: "/api/v1/resources/workstation/phase", body: { phase: "unmanaged", version: 3 } });
  });
});

describe("power requests in the header", () => {
  it("opens the router reboot confirmation without leaving the tab", async () => {
    setup(routerFixture, "/resources/gateway");
    const group = await screen.findByRole("group", { name: "可執行的動作" });
    const reboot = within(group).getByRole("button", { name: "重新開機" }) as HTMLButtonElement;
    await waitFor(() => expect(reboot.disabled).toBe(false));
    await userEvent.click(reboot);
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/重開路由器會中斷網路/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "總覽" }).getAttribute("aria-current")).toBe("page");
    await userEvent.click(within(dialog).getByRole("button", { name: "取消" }));
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("offers the machine power requests as buttons that stay disabled until the helper reports them", async () => {
    setup(freshMachine(), "/resources/workstation");
    const group = await screen.findByRole("group", { name: "可執行的動作" });
    const reboot = within(group).getByRole("button", { name: "重新開機" }) as HTMLButtonElement;
    expect(reboot.disabled).toBe(true);
    expect(within(group).queryByRole("link", { name: "重新開機" })).toBeNull();
  });
});

const dependentImpact: ResourceImpact = {
  ...noImpact("web"),
  dependents: [{ name: "api", type: "service", relation: "dependsOn", via: "web", depth: 1 }],
  controlDependents: [{ name: "nas", type: "machine", relation: "controlPath", via: "api", depth: 2 }],
  acknowledgementRequired: true,
};

describe("relations and impact", () => {
  beforeEach(stubGraphEnvironment);

  it("lists declared relations, dependents and control dependents", async () => {
    const detail = resourceFixture({
      document: { ...resourceFixture().document, spec: { ...resourceFixture().document.spec, relations: [{ type: "dependsOn", target: "db" }] } },
      unresolvedRelations: ["db"],
    });
    setup(detail, "/resources/web/relations", dependentImpact);
    expect(await screen.findByRole("heading", { name: "依賴此資源的資源" })).toBeTruthy();
    expect(screen.getAllByRole("link", { name: "api" })[0]!.getAttribute("href")).toBe("/resources/api");
    expect(screen.getByRole("link", { name: "nas" })).toBeTruthy();
    // The name is in the graph and in the table.
    expect(screen.getAllByText("db")).toHaveLength(2);
    expect(await screen.findByText("目標資源不存在")).toBeTruthy();
  });

  it("shows the impact in the retire dialog and acknowledges it on confirm", async () => {
    const service = resourceFixture();
    const { writes } = setup(service, "/resources/web", dependentImpact);
    const group = await screen.findByRole("group", { name: "可執行的動作" });
    await userEvent.click(within(group).getByRole("button", { name: "退役" }));
    const dialog = screen.getByRole("dialog");
    expect(await within(dialog).findByText("api（依賴 web）")).toBeTruthy();
    expect(within(dialog).queryByRole("checkbox")).toBeNull();
    const confirm = within(dialog).getByRole("button", { name: "確認退役（影響 2 個資源）" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(false);
    await userEvent.click(confirm);
    await waitFor(() => expect(writes).toEqual([{ method: "POST", path: "/api/v1/resources/web/phase", body: { phase: "retired", version: 3, acknowledgeImpact: true } }]));
  });
});
