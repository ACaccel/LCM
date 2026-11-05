import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Overview } from "../api/types";
import { s } from "../strings";
import { OverviewPage } from "./overview";
import { jsonResponse } from "./resource/test-fixtures";

type Card = Overview["resources"][number];

const card = (name: string, over: Partial<Card> = {}): Card => ({
  name,
  type: "service",
  state: "running",
  health: "healthy",
  deployedVersion: "a41c9e2f0b7d",
  lastDeployedAt: "2026-11-01T00:00:00Z",
  managedBy: null,
  hasChecks: true,
  uptime: 1,
  buckets: Array.from({ length: 24 }, () => ({ ratio: 1, failures: 0 })),
  ...over,
});

function overview(count: number): Overview {
  const resources = Array.from({ length: count }, (_, i) => card(`svc-${String(i).padStart(2, "0")}`));
  const set = (i: number, c: Card) => {
    if (i < count) resources[i] = c;
  };
  set(3, card("svc-03", { health: "degraded" }));
  set(7, card("svc-07", { health: "down" }));
  set(9, card("svc-09", { hasChecks: false, uptime: null, buckets: [] }));
  return {
    generatedAt: "2026-11-01T12:00:00Z",
    types: [{ type: "service", total: count, retired: 0, health: { healthy: count - 2, degraded: 1, down: 1, unknown: 0 } }],
    // The server sorts by urgency: down first.
    attention: [
      {
        name: "svc-07",
        type: "service",
        state: "running",
        health: "down",
        since: "2026-11-01T10:00:00Z",
        firingAlerts: 0,
        reasons: [{ code: "health.checkDown", params: { check: "http" }, message: "check http is down" }],
      },
      { name: "svc-03", type: "service", state: "running", health: "degraded", since: "2026-11-01T11:00:00Z", firingAlerts: 2, reasons: [] },
    ],
    resources,
    activeRuns: 3,
    deploys: { since: "2026-11-01T00:00:00Z", count: 4 },
    firingAlerts: 2,
  };
}

function setup(data: Overview) {
  const requests: string[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    requests.push(url.pathname);
    if (url.pathname === "/api/v1/overview") return jsonResponse(data);
    if (url.pathname === "/api/v1/timeline") return jsonResponse({ items: [], nextCursor: null });
    return jsonResponse({ error: { code: "not_found", message: `unexpected ${url.pathname}`, details: {} } }, 404);
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <OverviewPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return requests;
}

afterEach(() => vi.unstubAllGlobals());

describe("overview page", () => {
  it("issues the same requests for 3 resources and for 20", async () => {
    const seen: string[][] = [];
    for (const count of [3, 20]) {
      const requests = setup(overview(count));
      expect(await screen.findByRole("heading", { name: s.overview.needsAttention })).toBeTruthy();
      await screen.findByText(s.overview.recentEvents);
      // Let every query settle before counting.
      await vi.waitFor(() => expect(requests).toContain("/api/v1/timeline"));
      seen.push([...requests].sort());
      document.body.innerHTML = "";
    }
    expect(seen[0]).toEqual(["/api/v1/overview", "/api/v1/timeline"]);
    expect(seen[1]).toEqual(seen[0]);
  });

  it("lists what needs attention in the server's order, with reasons, and shows the rest compactly", async () => {
    setup(overview(20));
    const block = await screen.findByLabelText(s.overview.needsAttention);
    const names = within(block)
      .getAllByRole("link")
      .map((a) => a.querySelector(".name")?.textContent);
    expect(names).toEqual(["svc-07", "svc-03"]);
    expect(within(block).getByText("檢查 http 失敗")).toBeTruthy();
    expect(within(block).getByText(s.overview.alertCount(2))).toBeTruthy();

    const quiet = (await screen.findByRole("heading", { name: s.overview.otherResources })).nextElementSibling as HTMLElement;
    const quietNames = [...quiet.querySelectorAll(".name")].map((n) => n.textContent);
    expect(quietNames).toHaveLength(18);
    expect(quietNames).not.toContain("svc-07");
    expect(quiet.querySelector("a.svc-card")?.classList.contains("compact")).toBe(true);
  });

  it("links the firing alerts tile to the alerts page and shows the headline numbers", async () => {
    setup(overview(5));
    const tile = (await screen.findByText(s.overview.firingAlerts)).closest("a");
    expect(tile?.getAttribute("href")).toBe("/alerts");
    expect(tile?.textContent).toContain("2");
    expect(screen.getByText(s.overview.runningRuns).closest("a")?.textContent).toContain("3");
    expect(screen.getByText(s.overview.deploysToday).closest(".stat")?.textContent).toContain("4");
  });

  it("says so when nothing needs attention", async () => {
    setup({ ...overview(2), attention: [] });
    expect(await screen.findByText(s.overview.allClear)).toBeTruthy();
  });

  it("offers to create the first resource when there are none", async () => {
    setup({ ...overview(0), types: [], resources: [], attention: [] });
    expect(await screen.findByText(s.overview.createFirst)).toBeTruthy();
  });
});
