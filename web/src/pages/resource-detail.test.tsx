import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ResourceDetail, ResourceImpact } from "../api/types";
import { ToastProvider } from "../components/toast";
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
    expect(tabsFor("service")).toEqual(["overview", "deployments", "checks", "timeline", "policies", "relations", "config"]);
    expect(tabsFor("machine")).toEqual(["overview", "monitoring", "events", "maintenance", "checks", "timeline", "policies", "relations", "config"]);
    expect(tabsFor("router")).toEqual(["overview", "monitoring", "events", "operations", "checks", "timeline", "policies", "relations", "config"]);
  });

  it("shows typed router monitoring and no service panels", async () => {
    const { reads } = setup(routerFixture, "/resources/gateway/monitoring");
    expect(await screen.findByText("RA70")).toBeTruthy();
    expect(screen.getAllByText("12.5%").length).toBeGreaterThan(0);
    expect(tabNames()).toEqual(["概覽", "監控", "事件", "操作", "檢查", "時間軸", "Policy", "關係", "設定"]);
    await waitFor(() => expect(reads).toContain("/api/v1/resources/gateway/history"));
  });

  it("falls back to the overview for a tab the type does not have", async () => {
    setup(resourceFixture(), "/resources/web/monitoring");
    expect(await screen.findByRole("link", { name: "概覽" })).toBeTruthy();
    expect(screen.getByRole("link", { name: "概覽" }).getAttribute("aria-current")).toBe("page");
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

const dependentImpact: ResourceImpact = {
  ...noImpact("web"),
  dependents: [{ name: "api", type: "service", relation: "dependsOn", via: "web", depth: 1 }],
  controlDependents: [{ name: "nas", type: "machine", relation: "controlPath", via: "api", depth: 2 }],
  acknowledgementRequired: true,
};

describe("relations and impact", () => {
  it("lists declared relations, dependents and control dependents", async () => {
    const detail = resourceFixture({
      document: { ...resourceFixture().document, spec: { ...resourceFixture().document.spec, relations: [{ type: "dependsOn", target: "db" }] } },
      unresolvedRelations: ["db"],
    });
    setup(detail, "/resources/web/relations", dependentImpact);
    expect(await screen.findByRole("heading", { name: "依賴此資源的資源" })).toBeTruthy();
    expect(screen.getAllByRole("link", { name: "api" })[0]!.getAttribute("href")).toBe("/resources/api");
    expect(screen.getByRole("link", { name: "nas" })).toBeTruthy();
    expect(screen.getByText("db")).toBeTruthy();
    expect(await screen.findByText("目標資源不存在")).toBeTruthy();
  });

  it("requires acknowledging the impact before retiring and sends it", async () => {
    const service = resourceFixture();
    const { writes } = setup(service, "/resources/web", dependentImpact);
    const group = await screen.findByRole("group", { name: "可執行的動作" });
    await userEvent.click(within(group).getByRole("button", { name: "退役" }));
    const dialog = screen.getByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "確認" }) as HTMLButtonElement;
    expect(await within(dialog).findByText("api（依賴 web）")).toBeTruthy();
    expect(confirm.disabled).toBe(true);
    await userEvent.click(within(dialog).getByRole("checkbox", { name: "我已檢查影響範圍，確認要繼續。" }));
    await userEvent.click(confirm);
    await waitFor(() => expect(writes).toEqual([{ method: "POST", path: "/api/v1/resources/web/phase", body: { phase: "retired", version: 3, acknowledgeImpact: true } }]));
  });
});
