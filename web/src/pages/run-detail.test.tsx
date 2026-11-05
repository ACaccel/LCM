import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen } from "@testing-library/react";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Run, RunNode } from "../api/types";
import { ToastProvider } from "../components/toast";
import { RunDetailPage } from "./run-detail";
import { RunGantt } from "./run-gantt";
import { jsonResponse } from "./resource/test-fixtures";

const T0 = Date.parse("2026-11-05T01:00:00Z");
const at = (seconds: number) => new Date(T0 + seconds * 1000).toISOString();
const node = (id: string, status: RunNode["status"], start: number, end: number, extra: Partial<RunNode> = {}): RunNode => ({ id, type: "core.http", status, startedAt: at(start), finishedAt: at(end), ...extra });

const run = (nodes: RunNode[]): Run => ({
  id: "run-0001-aaaa",
  policy: "deploy-web",
  version: 3,
  status: "failed",
  mode: "live",
  triggerNode: "start",
  createdAt: at(0),
  startedAt: at(0),
  finishedAt: at(60),
  event: {},
  nodes,
});

afterEach(() => vi.unstubAllGlobals());

function renderPage(r: Run) {
  vi.stubGlobal("fetch", async (req: Request) => {
    const path = new URL(req.url).pathname;
    if (path === `/api/v1/runs/${r.id}`) return jsonResponse(r);
    if (path === "/api/v1/node-types") return jsonResponse({ items: [] });
    return jsonResponse({ error: { code: "not_found", message: "missing" } }, 404);
  });
  const router = createMemoryRouter([{ path: "/runs/:id", element: <RunDetailPage /> }], { initialEntries: [`/runs/${r.id}`] });
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
}

describe("run gantt component", () => {
  const nodes = [node("fetch", "completed", 0, 4), node("build", "failed", 4, 40, { error: "boom" }), node("deploy", "pending", 0, 0, { startedAt: undefined, finishedAt: undefined })];

  it("draws a row per started node with status and duration in its name", () => {
    render(<RunGantt nodes={nodes} selected={null} onSelect={() => undefined} now={T0} />);
    expect(screen.getAllByRole("button")).toHaveLength(2);
    expect(screen.getByRole("button", { name: "build，失敗，36s" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "fetch，完成，4s" })).toBeTruthy();
  });

  it("marks the selected node and reports clicks", () => {
    const picks: string[] = [];
    render(<RunGantt nodes={nodes} selected="build" onSelect={(id) => picks.push(id)} now={T0} />);
    expect(screen.getByRole("button", { name: /^build/ }).getAttribute("aria-pressed")).toBe("true");
    expect(screen.getByRole("button", { name: /^fetch/ }).getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(screen.getByRole("button", { name: /^fetch/ }));
    expect(picks).toEqual(["fetch"]);
  });

  it("explains an empty run", () => {
    render(<RunGantt nodes={[node("a", "pending", 0, 0, { startedAt: undefined, finishedAt: undefined })]} selected={null} onSelect={() => undefined} now={T0} />);
    expect(screen.getByText("還沒有節點開始執行。")).toBeTruthy();
  });
});

describe("run detail page", () => {
  it("opens on the first failed node and lets the user pick another", async () => {
    renderPage(run([node("fetch", "completed", 0, 4), node("build", "failed", 4, 40, { error: "boom" }), node("notify", "dead", 40, 41)]));
    expect(await screen.findByRole("heading", { name: "build" })).toBeTruthy();
    expect(screen.getByText("boom")).toBeTruthy();
    expect(screen.getByRole("button", { name: /^build/ }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: /^fetch/ }));
    expect(await screen.findByRole("heading", { name: "fetch" })).toBeTruthy();
  });

  it("asks to pick a node when nothing failed", async () => {
    renderPage({ ...run([node("fetch", "completed", 0, 4)]), status: "succeeded" });
    expect(await screen.findByText("點選節點以查看詳細資料。")).toBeTruthy();
  });

  it("lays out with a class instead of an inline grid", async () => {
    const { container } = renderPage(run([node("fetch", "completed", 0, 4)]));
    await screen.findByText("點選節點以查看詳細資料。");
    const grid = container.querySelector(".run-grid") as HTMLElement;
    expect(grid).toBeTruthy();
    expect(grid.getAttribute("style")).toBeNull();
  });
});
