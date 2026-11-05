import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { freshMachine, jsonResponse } from "../test-fixtures";
import { MachineOverview } from "./monitoring";

function wrap(children: ReactNode) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>;
}

/** Answers history requests with the given newest-first points and records them. */
function stubHistory(items: unknown[] = []) {
  const reads: URL[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    expect(url.pathname).toBe("/api/v1/resources/workstation/history");
    reads.push(url);
    return jsonResponse({ items, nextCursor: null });
  });
  return reads;
}

afterEach(() => vi.unstubAllGlobals());

describe("machine overview", () => {
  it("shows cached healthy data as stale, with unknown optional coverage and journal permission limitations", () => {
    stubHistory();
    const machine = freshMachine();
    machine.machine!.status!.snapshot!.observedAt = "2000-01-01T00:00:00Z";
    render(wrap(<MachineOverview resource={machine} selector="workstation" />));
    expect(screen.getByText(/過期或無法連線不代表已關機/)).toBeTruthy();
    expect(screen.queryByText("健康", { exact: true })).toBeNull();
    const psi = screen.getAllByRole("row").find((row) => row.textContent?.startsWith("PSI"))!;
    expect(within(psi).getByText("未知", { exact: true })).toBeTruthy();
    expect(screen.getAllByText("journal permission denied").length).toBeGreaterThan(0);
    expect(screen.getByText(/CPU 首樣本/)).toBeTruthy();
  });

  it("shows CPU, memory and disk usage as bars marked with the configured limit", () => {
    stubHistory();
    const machine = freshMachine();
    machine.document.spec.config = { ...machine.document.spec.config, thresholds: { diskPercent: 80 } };
    render(wrap(<MachineOverview resource={machine} selector="workstation" />));
    const disk = screen.getByRole("meter", { name: /^磁碟/ });
    expect(disk.getAttribute("aria-valuetext")).toContain("閾值 80%");
    // Memory keeps the default limit of 90%.
    expect(screen.getByRole("meter", { name: "記憶體使用率" }).getAttribute("aria-valuetext")).toContain("閾值 90%");
    // The rest of the snapshot, such as inodes, is behind a disclosure.
    const more = screen.getByText("更多觀測細節").closest("details")!;
    expect(more.open).toBe(false);
    expect(within(more).getByRole("meter", { name: /^inode/ })).toBeTruthy();
  });

  it("summarizes the coverage, lists the sources that are not available and keeps the full list closed", () => {
    stubHistory();
    render(wrap(<MachineOverview resource={freshMachine()} selector="workstation" />));
    // The fixture answers cpu, memory, filesystems and units out of nine sources.
    expect(screen.getByText("4/9 可用")).toBeTruthy();
    const all = screen.getByText("全部來源").closest("details")!;
    expect(all.open).toBe(false);
    expect(within(all).getAllByRole("row")).toHaveLength(10);
    const section = screen.getByRole("heading", { name: "來源涵蓋狀態" }).closest("section")!;
    const listed = within(section).getAllByRole("row").filter((row) => !all.contains(row));
    const names = listed.slice(1).map((row) => row.querySelector("td")?.textContent ?? "");
    expect(names.some((n) => n.startsWith("PSI"))).toBe(true);
    expect(names.some((n) => n.startsWith("CPU"))).toBe(false);
    expect(listed).toHaveLength(6);
  });

  it("plots the trend of the range that is picked, asking the server for that range", async () => {
    const point = (id: number, at: string, cpu: number) => ({
      id,
      resourceId: "machine-1",
      type: "machine",
      observedAt: at,
      machine: { ...freshMachine().machine!.status!.snapshot!, id, observedAt: at, cpuPercent: cpu },
    });
    const reads = stubHistory([point(2, "2026-11-01T00:01:00Z", 30), point(1, "2026-11-01T00:00:00Z", 10)]);
    render(wrap(<MachineOverview resource={freshMachine()} selector="workstation" />));
    expect(await screen.findByRole("group", { name: /CPU 與記憶體使用率趨勢，CPU 使用率 30%/ })).toBeTruthy();
    await waitFor(() => expect(reads).toHaveLength(1));
    const day = reads[0]!.searchParams;
    expect(day.get("resolution")).toBe("minute");
    expect(day.get("step")).toBe("8m");
    expect(Date.now() - Date.parse(day.get("from")!)).toBeGreaterThan(86_400_000 - 60_000);
    expect(Date.now() - Date.parse(day.get("from")!)).toBeLessThan(86_400_000 + 60_000);

    await userEvent.click(screen.getByRole("button", { name: "7 天" }));
    await waitFor(() => expect(reads).toHaveLength(2));
    const week = reads[1]!.searchParams;
    expect(week.get("step")).toBe("56m");
    expect(Date.now() - Date.parse(week.get("from")!)).toBeGreaterThan(7 * 86_400_000 - 60_000);
    expect(screen.getByRole("button", { name: "7 天" }).getAttribute("aria-pressed")).toBe("true");
  });
});
