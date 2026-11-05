import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
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

describe("overview page states", () => {
  function mount(handler: (url: URL) => Response | Promise<Response>) {
    vi.stubGlobal("fetch", async (req: Request) => handler(new URL(req.url)));
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <OverviewPage />
        </MemoryRouter>
      </QueryClientProvider>,
    );
  }
  const quietTimeline = (url: URL) => (url.pathname === "/api/v1/timeline" ? jsonResponse({ items: [], nextCursor: null }) : undefined);

  it("shows a spinner while the overview loads, then the page", async () => {
    let finish: (r: Response) => void = () => undefined;
    mount((url) => quietTimeline(url) ?? new Promise<Response>((resolve) => (finish = resolve)));
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);
    expect(screen.queryByText(s.overview.needsAttention)).toBeNull();
    finish(jsonResponse(overview(2)));
    expect(await screen.findByRole("heading", { name: s.overview.needsAttention })).toBeTruthy();
  });

  it("reports a failed overview without claiming all is clear, and retries on request", async () => {
    let failing = true;
    mount((url) => quietTimeline(url) ?? (failing ? jsonResponse({ error: { code: "unavailable", message: "Overview failed", details: {} } }, 503) : jsonResponse(overview(2))));
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("Overview failed");
    expect(screen.queryByText(s.overview.allClear)).toBeNull();
    failing = false;
    await userEvent.click(screen.getByRole("button", { name: s.common.retry }));
    expect(await screen.findByRole("heading", { name: s.overview.needsAttention })).toBeTruthy();
  });

  it("keeps the overview when only the recent events fail", async () => {
    mount((url) => (url.pathname === "/api/v1/overview" ? jsonResponse(overview(2)) : jsonResponse({ error: { code: "unavailable", message: "Events failed", details: {} } }, 503)));
    expect(await screen.findByRole("heading", { name: s.overview.needsAttention })).toBeTruthy();
    await waitFor(() => expect(screen.getByText(/Events failed/)).toBeTruthy());
  });

  it("counts resources per type and health and links each count into the filtered list", async () => {
    mount((url) => quietTimeline(url) ?? jsonResponse(overview(10)));
    const table = (await screen.findByRole("heading", { name: s.overview.byType })).nextElementSibling as HTMLElement;
    const row = within(table).getByRole("row", { name: new RegExp(s.resourceType.service) });
    expect(within(row).getByRole("link", { name: s.resourceType.service }).getAttribute("href")).toBe("/resources?type=service");
    // Only non-zero health counts are links.
    const hrefs = within(row)
      .getAllByRole("link")
      .map((a) => a.getAttribute("href"));
    expect(hrefs).toContain("/resources?type=service&health=down&phase=managed");
    expect(hrefs).toContain("/resources?type=service&health=degraded&phase=managed");
    expect(hrefs.some((h) => h?.includes("health=unknown"))).toBe(false);
  });

  it("marks degraded and down counts by tone only when there are some", async () => {
    mount((url) => quietTimeline(url) ?? jsonResponse(overview(10)));
    const stats = await screen.findByLabelText(s.overview.stats);
    const value = (label: string) => within(stats).getByText(label).previousElementSibling as HTMLElement;
    expect(value(s.overview.down).className).toContain("tone-bad");
    expect(value(s.overview.degraded).className).toContain("tone-warn");
    expect(value(s.overview.healthy).className).toBe("");
    expect(within(stats).queryByText(s.overview.unknownHealth)).toBeNull();
  });

  it("explains a resource without availability data instead of drawing an empty strip", async () => {
    const data = overview(10);
    data.attention = [{ name: "svc-09", type: "service", state: "running", health: "degraded", since: null, firingAlerts: 0, reasons: [] }];
    mount((url) => quietTimeline(url) ?? jsonResponse(data));
    const block = await screen.findByLabelText(s.overview.needsAttention);
    expect(within(block).getByText(s.checks.none)).toBeTruthy();
    expect(within(block).queryByText(s.resource.since)).toBeNull();
  });
});
