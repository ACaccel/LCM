import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { ResourceEvents } from "../observations";
import { freshMachine, jsonResponse } from "../test-fixtures";
import { MachineOverview } from "./monitoring";

function wrap(children: ReactNode) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>;
}
afterEach(() => vi.unstubAllGlobals());

describe("machine monitoring", () => {
  it("shows cached healthy data as stale, with unknown optional coverage and journal permission limitations", () => {
    const machine = freshMachine();
    machine.machine!.status!.snapshot!.observedAt = "2000-01-01T00:00:00Z";
    render(<MachineOverview resource={machine} />);
    expect(screen.getByText(/過期或無法連線不代表已關機/)).toBeTruthy();
    expect(screen.queryByText("健康", { exact: true })).toBeNull();
    const psi = screen.getAllByRole("row").find((row) => row.textContent?.startsWith("PSI"))!;
    expect(within(psi).getByText("未知", { exact: true })).toBeTruthy();
    expect(screen.getByText("journal permission denied")).toBeTruthy();
    expect(screen.getByText(/CPU 首樣本/)).toBeTruthy();
  });
  it("pages durable journal/derived events and distinguishes gaps, source/received time and historical/live events", async () => {
    const cursors: (string | null)[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      expect(url.pathname).toBe("/api/v1/resources/workstation/events");
      cursors.push(url.searchParams.get("cursor"));
      const first = !url.searchParams.has("cursor");
      return jsonResponse({
        items: [
          {
            id: first ? 2 : 1,
            resourceId: "machine-1",
            source: first ? "journal" : "poll",
            kind: first ? "machine.journal.gap" : "machine.boot.changed",
            severity: "warning",
            occurredAt: "2026-11-01T00:00:00Z",
            ingestedAt: "2026-11-01T00:01:00Z",
            historical: first,
            reason: first ? "journal cursor expired" : "boot identity changed",
            ...(first ? {} : { reasonMessage: { code: "event.bootChanged", params: {}, message: "boot identity changed" } }),
          },
        ],
        nextCursor: first ? "opaque-page-2" : null,
      });
    });
    render(wrap(<ResourceEvents resource="workstation" />));
    expect(await screen.findByText("journal cursor expired")).toBeTruthy();
    expect(screen.getByText("事件缺口", { exact: true })).toBeTruthy();
    expect(screen.getByText("歷史補讀", { exact: true })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "來源時間" })).toBeTruthy();
    expect(screen.getByRole("columnheader", { name: "接收時間" })).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: "載入更多" }));
    // A reason with a code is translated; the kind and severity are labelled.
    expect(await screen.findByText("Boot ID 改變（已重新開機）")).toBeTruthy();
    expect(screen.getByText("重新開機", { exact: true })).toBeTruthy();
    expect(screen.getAllByText("警告", { exact: true })).toHaveLength(2);
    expect(screen.getByRole("cell", { name: /^輪詢/ })).toBeTruthy();
    expect(screen.getByText("即時事件", { exact: true })).toBeTruthy();
    expect(cursors).toEqual([null, "opaque-page-2"]);
    expect(screen.queryByRole("button", { name: "載入更多" })).toBeNull();
  });
});
