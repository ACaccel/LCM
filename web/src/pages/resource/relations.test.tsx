import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ResourceDetail } from "../../api/types";
import { stubGraphEnvironment } from "../../test-graph-env";
import { RelationsTab } from "./relations";
import { jsonResponse, noImpact, resourceFixture } from "./test-fixtures";

beforeEach(stubGraphEnvironment);
afterEach(() => vi.unstubAllGlobals());

const summary = (name: string, type: "service" | "machine", health: "healthy" | "down") => ({
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

function setup(detail: ResourceDetail, impact = noImpact(detail.name)) {
  vi.stubGlobal("fetch", async (req: Request) => {
    const path = new URL(req.url).pathname;
    if (path === "/api/v1/resources") return jsonResponse({ items: [summary("web", "service", "healthy"), summary("api", "service", "healthy"), summary("host", "machine", "down")], nextCursor: null });
    if (path.endsWith("/impact")) return jsonResponse(impact);
    return jsonResponse({ error: { message: "unexpected" } }, 404);
  });
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <RelationsTab detail={detail} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const withRelations = (relations: { type: "dependsOn" | "runsOn" | "controlPath"; target: string }[]) => {
  const base = resourceFixture();
  return resourceFixture({ document: { ...base.document, spec: { ...base.document.spec, relations } } });
};

describe("relations tab graph", () => {
  it("draws the resource, what it declares and who depends on it above the tables", async () => {
    const { container } = setup(withRelations([{ type: "runsOn", target: "host" }]), {
      ...noImpact("web"),
      dependents: [{ name: "api", type: "service", relation: "dependsOn", via: "web", depth: 1 }],
    });
    expect(await screen.findByRole("button", { name: /^開啟資源 host，機器，異常/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^開啟資源 api，服務，健康/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /^開啟資源 web，服務，健康/ }).closest(".topo-node")?.className).toContain("focus");
    expect(container.querySelectorAll(".react-flow__edge")).toHaveLength(2);
    // The tables stay below the graph.
    const headings = screen.getAllByRole("heading", { level: 2 }).map((h) => h.textContent);
    expect(headings.indexOf("依賴關係圖")).toBeLessThan(headings.indexOf("宣告的關係"));
  });

  it("shows no graph for a resource without any relation", async () => {
    setup(withRelations([]));
    expect(await screen.findByText("這個資源沒有宣告關係。")).toBeTruthy();
    expect(screen.queryByText("依賴關係圖")).toBeNull();
  });
});
