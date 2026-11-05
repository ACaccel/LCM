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
