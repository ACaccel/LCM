import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ResourceSummary, ResourceType } from "../api/types";
import { ToastProvider } from "../components/toast";
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
  ...over,
});

const all = [summary("api", "service"), summary("gateway", "router", { health: "degraded" }), summary("web", "service"), summary("workstation", "machine")];

function setup() {
  const lists: URLSearchParams[] = [];
  const archives: URLSearchParams[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
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
  return { lists, archives };
}

const rowNames = () =>
  screen
    .getAllByRole("row")
    .slice(1)
    .map((r) => within(r).getAllByRole("cell")[0]?.querySelector("a")?.textContent);

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
