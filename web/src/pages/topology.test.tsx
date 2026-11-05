import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Relation } from "../api/types";
import { stubGraphEnvironment } from "../test-graph-env";
import { jsonResponse, resourceFixture } from "./resource/test-fixtures";
import { TopologyPage } from "./topology";

beforeEach(stubGraphEnvironment);
afterEach(() => vi.unstubAllGlobals());

const row = (name: string, type: "service" | "machine", health: "healthy" | "down") => ({
  id: `id-${name}`,
  name,
  type,
  adapter: "systemd",
  phase: "managed",
  labels: {},
  state: "running",
  stateSince: null,
  health,
  version: 1,
  updatedAt: "2026-11-01T00:00:00Z",
  deployedVersion: "",
  managedBy: null,
});

const relations: Record<string, Relation[]> = { web: [{ type: "dependsOn", target: "db" }], db: [{ type: "runsOn", target: "host" }], host: [], alone: [] };

function renderPage(list = ["web", "db", "host", "alone"]) {
  const reads: string[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const path = decodeURIComponent(new URL(req.url).pathname);
    reads.push(path);
    if (path === "/api/v1/resources") return jsonResponse({ items: list.map((n) => row(n, n === "host" ? "machine" : "service", n === "host" ? "down" : "healthy")), nextCursor: null });
    const name = path.split("/").pop() ?? "";
    const base = resourceFixture({ name });
    return jsonResponse({ ...base, document: { ...base.document, spec: { ...base.document.spec, relations: relations[name] ?? [] } } });
  });
  const router = createMemoryRouter(
    [
      { path: "/topology", element: <TopologyPage /> },
      { path: "/resources/:name", element: <p>detail page</p> },
    ],
    { initialEntries: ["/topology"] },
  );
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { router, reads };
}

describe("topology page", () => {
  it("draws the declared relations built from the resource documents", async () => {
    renderPage();
    expect(await screen.findByRole("button", { name: /^開啟資源 web/ })).toBeTruthy();
    expect(document.querySelectorAll(".react-flow__edge")).toHaveLength(2);
    // Resources without a relation are hidden by default.
    expect(screen.queryByRole("button", { name: /^開啟資源 alone/ })).toBeNull();
    fireEvent.click(screen.getByRole("checkbox", { name: "只顯示有關係的資源" }));
    expect(await screen.findByRole("button", { name: /^開啟資源 alone/ })).toBeTruthy();
  });

  it("lists the relations as text too", async () => {
    renderPage();
    await screen.findByRole("button", { name: /^開啟資源 web/ });
    expect(screen.getByText("web 依賴 db")).toBeTruthy();
    expect(screen.getByText("db 承載於 host")).toBeTruthy();
  });

  it("colours nodes by health", async () => {
    renderPage();
    const host = await screen.findByRole("button", { name: /^開啟資源 host/ });
    expect(host.closest(".topo-node")?.className).toContain("bad");
    expect(screen.getByRole("button", { name: /^開啟資源 web/ }).closest(".topo-node")?.className).toContain("ok");
  });

  it("opens the resource that is clicked", async () => {
    const { router } = renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /^開啟資源 db/ }));
    expect(router.state.location.pathname).toBe("/resources/db");
    expect(await screen.findByText("detail page")).toBeTruthy();
  });

  it("says so when nothing declares a relation", async () => {
    relations["web"] = [];
    relations["db"] = [];
    renderPage(["web", "db"]);
    expect(await screen.findByText("沒有資源宣告關係。")).toBeTruthy();
  });
});
