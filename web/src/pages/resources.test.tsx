import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ResourceSummary, ResourceType } from "../api/types";
import { ToastProvider } from "../components/toast";
import { s } from "../strings";
import { ResourcesPage } from "./resources";
import { jsonResponse } from "./resource/test-fixtures";

const types: ResourceType[] = [
  { type: "service", adapters: ["systemd", "docker"], needsConnection: false, description: "A service", actions: [] },
  { type: "machine", adapters: ["local"], needsConnection: false, description: "The local machine", actions: [] },
  { type: "router", adapters: ["xiaomi"], needsConnection: true, description: "A router", actions: [] },
];

const summary = (name: string, type: ResourceSummary["type"], over: Partial<ResourceSummary> = {}): ResourceSummary => ({
  id: `id-${name}`,
  name,
  type,
  adapter: type === "service" ? "systemd" : type === "machine" ? "local" : "xiaomi",
  phase: "managed",
  labels: {},
  state: type === "service" ? "running" : "online",
  stateSince: null,
  health: "healthy",
  version: 1,
  updatedAt: "2026-11-01T00:00:00Z",
  deployedVersion: "",
  managedBy: null,
  relations: [],
  ...over,
});

const all = [
  summary("api", "service", { labels: { tier: "web" } }),
  summary("gateway", "router", { health: "degraded" }),
  summary("web", "service", { labels: { tier: "web" }, managedBy: { source: "infra", path: "web.yaml", commit: "abc", drifted: false, conflict: false } }),
  summary("workstation", "machine"),
];

const impactOfApi = {
  resource: "api",
  action: "stop",
  dependents: [{ name: "web", type: "service", relation: "dependsOn", via: "api", depth: 1 }],
  controlDependents: [],
  unresolved: [],
  acknowledgementRequired: true,
};
const quiet = { ...impactOfApi, resource: "web", dependents: [], acknowledgementRequired: false };
const groupPlan = {
  action: "stop",
  selector: { type: "service", labels: {}, names: ["api", "web"] },
  steps: [
    { resource: "web", plan: { resource: "web", action: "stop", from: "running", to: "stopped", steps: [], stepMessages: [] }, error: "", impact: quiet },
    { resource: "api", plan: { resource: "api", action: "stop", from: "running", to: "stopped", steps: [], stepMessages: [] }, error: "", impact: impactOfApi },
  ],
  acknowledgementRequired: true,
};

function setup() {
  const lists: URLSearchParams[] = [];
  const archives: URLSearchParams[] = [];
  const groups: Record<string, unknown>[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    if (url.pathname === "/api/v1/resource-groups/actions" && req.method === "POST") {
      const body = JSON.parse(await req.text()) as Record<string, unknown>;
      groups.push(body);
      if (body["dryRun"]) return jsonResponse(groupPlan);
      return jsonResponse(
        {
          id: "grp_1",
          action: "stop",
          status: "running",
          actor: { kind: "user", id: "1", name: "admin" },
          selector: groupPlan.selector,
          startedAt: "2026-11-01T00:00:00Z",
          finishedAt: null,
          steps: [],
        },
        202,
      );
    }
    if (url.pathname === "/api/v1/resource-groups/actions/grp_1") {
      return jsonResponse({
        id: "grp_1",
        action: "stop",
        status: "succeeded",
        actor: { kind: "user", id: "1", name: "admin" },
        selector: groupPlan.selector,
        startedAt: "2026-11-01T00:00:00Z",
        finishedAt: "2026-11-01T00:00:05Z",
        steps: [
          { resource: "web", status: "succeeded", actionId: "a1", result: null, error: "" },
          { resource: "api", status: "failed", actionId: "a2", result: null, error: "unit refused to stop" },
        ],
      });
    }
    if (url.pathname === "/api/v1/resource-types") return jsonResponse({ items: types });
    if (url.pathname === "/api/v1/resources") {
      lists.push(url.searchParams);
      const type = url.searchParams.get("type");
      return jsonResponse({ items: all.filter((r) => !type || r.type === type), nextCursor: null });
    }
    if (url.pathname === "/api/v1/resource-archives") {
      archives.push(url.searchParams);
      return jsonResponse({ items: [summary("old-web", "service", { id: "old-1", phase: "unmanaged" })], nextCursor: null });
    }
    return jsonResponse({ error: { code: "not_found", message: `unexpected ${url.pathname}`, details: {} } }, 404);
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <MemoryRouter initialEntries={["/resources"]}>
          <Routes>
            <Route path="/resources" element={<ResourcesPage />} />
          </Routes>
        </MemoryRouter>
      </ToastProvider>
    </QueryClientProvider>,
  );
  return { lists, archives, groups };
}

const rowNames = () =>
  screen
    .getAllByRole("row")
    .slice(1)
    .map((r) => r.querySelector("a")?.textContent);

afterEach(() => vi.unstubAllGlobals());

describe("resource list", () => {
  it("filters by type through the server and by name in the page", async () => {
    const { lists } = setup();
    expect(await screen.findByRole("link", { name: "workstation" })).toBeTruthy();
    expect(rowNames()).toEqual(["api", "gateway", "web", "workstation"]);

    const group = screen.getByRole("group", { name: "型別" });
    await userEvent.click(within(group).getByRole("button", { name: "服務" }));
    await waitFor(() => expect(rowNames()).toEqual(["api", "web"]));
    expect(lists.at(-1)?.get("type")).toBe("service");
    expect(within(group).getByRole("button", { name: "服務" }).getAttribute("aria-pressed")).toBe("true");
    expect(within(group).getByRole("button", { name: "全部型別" }).getAttribute("aria-pressed")).toBe("false");

    await userEvent.type(screen.getByLabelText("搜尋名稱"), "we");
    expect(rowNames()).toEqual(["web"]);

    await userEvent.click(within(group).getByRole("button", { name: "全部型別" }));
    await waitFor(() => expect(rowNames()).toEqual(["web"]));
    expect(lists.at(-1)?.has("type")).toBe(false);
  });

  it("searches names ignoring case and sorts by a clicked column", async () => {
    setup();
    await screen.findByRole("link", { name: "workstation" });
    await userEvent.type(screen.getByLabelText("搜尋名稱"), "W");
    expect(rowNames()).toEqual(["gateway", "web", "workstation"]);
    await userEvent.clear(screen.getByLabelText("搜尋名稱"));

    const name = screen.getByRole("columnheader", { name: s.resources.columns.name });
    expect(name.getAttribute("aria-sort")).toBe("ascending");
    await userEvent.click(within(name).getByRole("button"));
    expect(rowNames()).toEqual(["workstation", "web", "gateway", "api"]);
    expect(name.getAttribute("aria-sort")).toBe("descending");

    // Ascending health lists the resources that need attention first.
    await userEvent.click(within(screen.getByRole("columnheader", { name: s.resources.columns.health })).getByRole("button"));
    expect(rowNames()[0]).toBe("gateway");
  });

  it("has no phase column and tags only the resources a git source manages", async () => {
    setup();
    await screen.findByRole("link", { name: "workstation" });
    const headers = screen.getAllByRole("columnheader").map((h) => h.textContent);
    expect(headers).not.toContain(s.resources.columns.phase);
    expect(headers).toContain(s.resources.columns.managed);
    expect(screen.queryByText(s.ownership.gui)).toBeNull();
    expect(screen.getAllByText(s.ownership.managed("infra"))).toHaveLength(1);
  });

  it("selects services by checkbox or label and previews the ordered plan before acknowledging the impact", async () => {
    const { groups } = setup();
    await screen.findByRole("link", { name: "workstation" });
    // Only managed services can be selected.
    expect((screen.getByRole("checkbox", { name: "選取 gateway" }) as HTMLInputElement).disabled).toBe(true);
    expect((screen.getByRole("checkbox", { name: "選取 workstation" }) as HTMLInputElement).disabled).toBe(true);
    expect(screen.queryByRole("button", { name: s.bulk.preview })).toBeNull();

    await userEvent.type(screen.getByLabelText(s.bulk.byLabel), "tier=web");
    await userEvent.click(screen.getByRole("button", { name: s.bulk.selectByLabel }));
    expect(screen.getByText(s.bulk.selected(2))).toBeTruthy();
    await userEvent.click(screen.getByRole("checkbox", { name: "選取 web" }));
    expect(screen.getByText(s.bulk.selected(1))).toBeTruthy();
    await userEvent.click(screen.getByRole("checkbox", { name: "選取 web" }));

    await userEvent.selectOptions(screen.getByLabelText(s.bulk.action), "stop");
    await userEvent.click(screen.getByRole("button", { name: s.bulk.preview }));
    const dialog = await screen.findByRole("dialog");
    // Nothing runs on preview: the first request is a dry run.
    expect(groups).toEqual([{ selector: { names: ["api", "web"] }, action: "stop", dryRun: true, acknowledgeImpact: false }]);

    const plan = await within(dialog).findByRole("list", { name: s.bulk.planTitle });
    expect([...plan.querySelectorAll("li strong")].map((x) => x.textContent)).toEqual(["web", "api"]);
    expect(within(dialog).queryByRole("checkbox")).toBeNull();
    expect(within(dialog).getByText("web（依賴 api）")).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: "確認停止（影響 1 個資源）" }));

    // Confirming runs the reviewed group and acknowledges its impact.
    await within(dialog).findByText("unit refused to stop");
    expect(groups).toHaveLength(2);
    expect(groups[1]).toEqual({ selector: { names: ["api", "web"] }, action: "stop", dryRun: false, acknowledgeImpact: true });
  });

  it("does not run a plan with a step that cannot run", async () => {
    const { groups } = setup();
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      if (url.pathname === "/api/v1/resource-groups/actions") {
        groups.push(JSON.parse(await req.text()) as Record<string, unknown>);
        return jsonResponse({
          ...groupPlan,
          acknowledgementRequired: false,
          steps: [{ ...groupPlan.steps[0]!, plan: null, error: "service is busy", impact: quiet }],
        });
      }
      if (url.pathname === "/api/v1/resource-types") return jsonResponse({ items: types });
      return jsonResponse({ items: all, nextCursor: null });
    });
    await userEvent.click(await screen.findByRole("checkbox", { name: "選取 web" }));
    await userEvent.click(screen.getByRole("button", { name: s.bulk.preview }));
    const dialog = await screen.findByRole("dialog");
    expect(await within(dialog).findByText("無法執行：service is busy")).toBeTruthy();
    expect((within(dialog).getByRole("button", { name: "確認啟動（1 個服務）" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("lists archives read only, linked by immutable ID", async () => {
    const { archives } = setup();
    await screen.findByRole("link", { name: "web" });
    await userEvent.click(screen.getByRole("checkbox", { name: "顯示已解除納管的封存紀錄" }));
    const link = await screen.findByRole("link", { name: "old-web" });
    expect(link.getAttribute("href")).toBe("/resources/%40old-1");
    expect(archives).toHaveLength(1);
  });

  it("creates from a skeleton of the chosen type and adapter", async () => {
    setup();
    await screen.findByRole("link", { name: "web" });
    await userEvent.click(screen.getByRole("button", { name: "建立資源" }));
    const dialog = screen.getByRole("dialog");
    await userEvent.selectOptions(within(dialog).getByLabelText("型別"), "router");
    expect(within(dialog).getByText("A router")).toBeTruthy();
    expect((within(dialog).getByLabelText("Adapter") as HTMLSelectElement).value).toBe("xiaomi");
    await userEvent.click(within(dialog).getByRole("button", { name: "下一步" }));
    expect(within(dialog).getByText(/connection 名稱/)).toBeTruthy();
  });
});
