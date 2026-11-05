import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { TimelineEntry } from "../api/types";
import { formatDay } from "../lib/format";
import { jsonResponse } from "../pages/resource/test-fixtures";
import { s } from "../strings";
import { TimelinePanel } from "./timeline-panel";

const entry = (local: string, summary: string): TimelineEntry => ({ id: 1, resource: "web", time: new Date(local).toISOString(), kind: "alert", ref: summary, summary, data: {}, summaryMessage: { code: "", params: {}, message: summary } });
const entries = [entry("2026-11-04T09:30:00", "late"), entry("2026-11-04T08:00:00", "early"), entry("2026-11-03T22:15:00", "day before")];

function setup(initial: string) {
  const reads: URLSearchParams[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    if (url.pathname === "/api/v1/resources") return jsonResponse({ items: [], nextCursor: null });
    reads.push(url.searchParams);
    return jsonResponse({ items: entries, nextCursor: null });
  });
  const router = createMemoryRouter([{ path: "/timeline", element: <TimelinePanel withResourceFilter /> }], { initialEntries: [initial] });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { reads, router };
}

afterEach(() => vi.unstubAllGlobals());

describe("timeline panel", () => {
  it("separates entries by day, naming today and yesterday", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-11-04T12:00:00"));
    try {
      setup("/timeline");
      const today = `${s.timeline.today} · ${formatDay(entries[0]!.time)}`;
      const yesterday = `${s.timeline.yesterday} · ${formatDay(entries[2]!.time)}`;
      const days = await screen.findAllByRole("heading", { level: 3 });
      expect(days.map((d) => d.textContent)).toEqual([today, yesterday]);
      expect(screen.getByRole("region", { name: today }).textContent).toContain("early");
      expect(screen.getByRole("region", { name: yesterday }).textContent).toContain("day before");
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps the kind filter in the URL", async () => {
    const { reads, router } = setup("/timeline?kind=alert");
    await screen.findByText("late");
    expect(reads[0]?.getAll("kinds")).toEqual(["alert"]);
    expect((screen.getByRole("checkbox", { name: s.timeline.kindNames.alert }) as HTMLInputElement).checked).toBe(true);
    await userEvent.click(screen.getByRole("checkbox", { name: s.timeline.kindNames.deployment }));
    await waitFor(() => expect(router.state.location.search).toBe("?kind=deployment&kind=alert"));
    await waitFor(() => expect(reads.at(-1)?.getAll("kinds")).toEqual(["deployment", "alert"]));
  });

  it("filters by a time range and refuses a reversed one", async () => {
    const { reads, router } = setup("/timeline");
    await screen.findByText("late");
    fireEvent.change(screen.getByLabelText(s.timeline.from), { target: { value: "2026-11-04T08:00" } });
    const from = new Date("2026-11-04T08:00").toISOString();
    await waitFor(() => expect(reads.at(-1)?.get("from")).toBe(from));
    expect(new URLSearchParams(router.state.location.search).get("from")).toBe(from);
    const count = reads.length;
    fireEvent.change(screen.getByLabelText(s.timeline.to), { target: { value: "2026-11-04T07:00" } });
    expect(await screen.findByText(s.timeline.rangeInvalid)).toBeTruthy();
    expect(reads).toHaveLength(count);
    expect(new URLSearchParams(router.state.location.search).has("to")).toBe(false);
  });
});

describe("timeline with observation events", () => {
  const entryAt = (time: string, summary: string): TimelineEntry => ({ id: 1, resource: "gateway", time, kind: "alert", ref: summary, summary, data: {}, summaryMessage: { code: "", params: {}, message: summary } });
  const eventAt = (id: number, occurredAt: string, reason: string) => ({
    id,
    resourceId: "router-1",
    source: "poll",
    kind: "router.health.changed",
    severity: "info",
    occurredAt,
    ingestedAt: occurredAt,
    historical: false,
    reason,
  });
  // Entries and events interleave: A 10:00, E1 09:30, B 09:00, C 08:00, E2 07:00, E3 06:00.
  const timelinePages: Record<string, { items: TimelineEntry[]; nextCursor: string | null }> = {
    first: { items: [entryAt("2026-11-04T10:00:00Z", "entry A"), entryAt("2026-11-04T09:00:00Z", "entry B")], nextCursor: "t2" },
    t2: { items: [entryAt("2026-11-04T08:00:00Z", "entry C")], nextCursor: null },
  };
  const eventPages: Record<string, { items: ReturnType<typeof eventAt>[]; nextCursor: string | null }> = {
    first: { items: [eventAt(3, "2026-11-04T09:30:00Z", "event one"), eventAt(2, "2026-11-04T07:00:00Z", "event two")], nextCursor: "e2" },
    e2: { items: [eventAt(1, "2026-11-04T06:00:00Z", "event three")], nextCursor: null },
  };

  function setupMerged(initial = "/resources/gateway/timeline") {
    const timelineReads: URLSearchParams[] = [];
    const eventReads: URLSearchParams[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      if (url.pathname.endsWith("/timeline")) {
        timelineReads.push(url.searchParams);
        return jsonResponse(timelinePages[url.searchParams.get("cursor") ?? "first"]);
      }
      eventReads.push(url.searchParams);
      return jsonResponse(eventPages[url.searchParams.get("cursor") ?? "first"]);
    });
    const router = createMemoryRouter([{ path: "/resources/:name/timeline", element: <TimelinePanel resource="gateway" observations="gateway" /> }], { initialEntries: [initial] });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    return { timelineReads, eventReads, router };
  }

  const shown = () => [...document.querySelectorAll(".ev")].map((row) => /entry [A-C]|event (one|two|three)/.exec(row.textContent ?? "")?.[0]);

  it("merges entries and events by time and loads the stream that bounds the list", async () => {
    const { timelineReads, eventReads } = setupMerged();
    await screen.findByText("entry A");
    // Event two is older than what the timeline has loaded, so it waits for the timeline's next page.
    await waitFor(() => expect(eventReads).toHaveLength(1));
    expect(shown()).toEqual(["entry A", "event one", "entry B"]);
    expect(eventReads[0]?.get("order")).toBe("desc");

    await userEvent.click(screen.getByRole("button", { name: s.common.more }));
    await screen.findByText("entry C");
    expect(shown()).toEqual(["entry A", "event one", "entry B", "entry C", "event two"]);
    expect(timelineReads.map((p) => p.get("cursor"))).toEqual([null, "t2"]);
    expect(eventReads).toHaveLength(1);

    await userEvent.click(screen.getByRole("button", { name: s.common.more }));
    await screen.findByText(/event three/);
    expect(shown()).toEqual(["entry A", "event one", "entry B", "entry C", "event two", "event three"]);
    expect(eventReads.map((p) => p.get("cursor"))).toEqual([null, "e2"]);
    expect(timelineReads).toHaveLength(2);
    expect(screen.queryByRole("button", { name: s.common.more })).toBeNull();
  });

  it("reads only events when only the observation kind is selected", async () => {
    const { timelineReads, eventReads, router } = setupMerged("/resources/gateway/timeline?kind=observation");
    await screen.findByText(/event one/);
    expect(timelineReads).toHaveLength(0);
    expect((screen.getByRole("checkbox", { name: s.timeline.kindNames.observation }) as HTMLInputElement).checked).toBe(true);
    expect(shown()).toEqual(["event one", "event two"]);
    await userEvent.click(screen.getByRole("checkbox", { name: s.timeline.kindNames.alert }));
    await waitFor(() => expect(router.state.location.search).toBe("?kind=alert&kind=observation"));
    await screen.findByText("entry A");
    expect(timelineReads[0]?.getAll("kinds")).toEqual(["alert"]);
    expect(eventReads.length).toBeGreaterThan(0);
  });

  it("leaves the events out when the observation kind is not among the selected kinds", async () => {
    const { timelineReads, eventReads } = setupMerged("/resources/gateway/timeline?kind=alert");
    await screen.findByText("entry A");
    expect(eventReads).toHaveLength(0);
    expect(timelineReads[0]?.getAll("kinds")).toEqual(["alert"]);
  });
});
