import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Run, RunNode } from "../api/types";
import { ToastProvider } from "../components/toast";
import { s } from "../strings";
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

describe("run detail page states and actions", () => {
  interface Write {
    path: string;
    body: unknown;
  }

  function mount(read: () => Response | Promise<Response>) {
    const writes: Write[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const path = new URL(req.url).pathname;
      if (req.method === "POST") {
        const text = await req.text();
        writes.push({ path, body: text ? JSON.parse(text) : undefined });
        return jsonResponse({});
      }
      if (path === "/api/v1/runs/run-0001-aaaa") return read();
      if (path === "/api/v1/node-types") return jsonResponse({ items: [] });
      return jsonResponse({ error: { code: "not_found", message: "missing", details: {} } }, 404);
    });
    const router = createMemoryRouter([{ path: "/runs/:id", element: <RunDetailPage /> }], { initialEntries: ["/runs/run-0001-aaaa"] });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryClientProvider>,
    );
    return writes;
  }

  it("shows a spinner while loading and says so when the run does not exist", async () => {
    mount(() => jsonResponse({ error: { code: "not_found", message: "no such run", details: {} } }, 404));
    expect(screen.getAllByRole("status").length).toBeGreaterThan(0);
    expect((await screen.findByRole("alert")).textContent).toContain(s.run.notFound);
  });

  it("reports a failed read and shows the run after a retry", async () => {
    let failing = true;
    mount(() => (failing ? jsonResponse({ error: { code: "unavailable", message: "Run unavailable", details: {} } }, 503) : jsonResponse(run([node("fetch", "completed", 0, 4)]))));
    expect((await screen.findByRole("alert")).textContent).toContain("Run unavailable");
    failing = false;
    await userEvent.click(screen.getByRole("button", { name: s.common.retry }));
    expect(await screen.findByText(s.run.selectNode)).toBeTruthy();
  });

  it("shows the run's own error and the policy version it ran", async () => {
    mount(() => jsonResponse({ ...run([node("fetch", "completed", 0, 4)]), error: "node build exhausted its retries" }));
    expect((await screen.findByText("node build exhausted its retries")).closest("[role=alert]")).toBeTruthy();
    const link = screen.getByRole("link", { name: new RegExp(`deploy-web · ${s.run.policyVersion} 3`) });
    expect(link.getAttribute("href")).toBe("/policies/deploy-web");
  });

  it("offers cancelling only while the run is active, after a confirmation", async () => {
    const writes = mount(() => jsonResponse({ ...run([node("wait", "running", 0, 0, { finishedAt: undefined })]), status: "running", finishedAt: undefined }));
    await userEvent.click(await screen.findByRole("button", { name: s.run.cancel }));
    const dialog = screen.getByRole("dialog");
    expect(writes).toEqual([]);
    await userEvent.click(within(dialog).getByRole("button", { name: s.run.cancel }));
    await waitFor(() => expect(writes).toEqual([{ path: "/api/v1/runs/run-0001-aaaa/cancel", body: undefined }]));
  });

  it("has no cancel button for a finished run", async () => {
    mount(() => jsonResponse(run([node("fetch", "completed", 0, 4)])));
    await screen.findByText(s.run.selectNode);
    expect(screen.queryByRole("button", { name: s.run.cancel })).toBeNull();
  });

  it("shows the props and output of the selected node", async () => {
    mount(() => jsonResponse(run([node("fetch", "completed", 0, 4, { props: { url: "https://example.test" }, output: { status: 200 }, attempts: 2, port: "out" })])));
    await screen.findByText(s.run.selectNode);
    fireEvent.click(screen.getByRole("button", { name: /^fetch/ }));
    expect(await screen.findByRole("heading", { name: "fetch" })).toBeTruthy();
    expect(screen.getByText(/"url": "https:\/\/example.test"/)).toBeTruthy();
    expect(screen.getByText(/"status": 200/)).toBeTruthy();
    expect(screen.getByRole("heading", { name: s.run.props })).toBeTruthy();
    expect(screen.getByRole("heading", { name: s.run.output })).toBeTruthy();
  });

  it("resumes a suspended node with the typed payload and rejects invalid JSON first", async () => {
    const suspended = node("approve", "suspended", 0, 0, { finishedAt: undefined, suspendKey: "key-1" });
    const writes = mount(() => jsonResponse({ ...run([suspended]), status: "suspended", finishedAt: undefined }));
    await screen.findByText(s.run.selectNode);
    fireEvent.click(screen.getByRole("button", { name: /^approve/ }));
    await userEvent.click(await screen.findByRole("button", { name: s.run.resume }));
    const dialog = screen.getByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(s.run.resumePayload), "{{not json");
    await userEvent.click(within(dialog).getByRole("button", { name: s.run.resume }));
    expect((await within(dialog).findByRole("alert")).textContent).toContain(s.common.invalidJson);
    expect(writes).toEqual([]);
    await userEvent.clear(within(dialog).getByLabelText(s.run.resumePayload));
    await userEvent.type(within(dialog).getByLabelText(s.run.resumePayload), '{{"approved": true}');
    await userEvent.click(within(dialog).getByRole("button", { name: s.run.resume }));
    await waitFor(() => expect(writes).toHaveLength(1));
    expect(writes[0]).toEqual({ path: "/api/v1/runs/run-0001-aaaa/nodes/approve/resume", body: { key: "key-1", payload: { approved: true } } });
  });

  it("offers no resume for a suspended node once the run has ended", async () => {
    const suspended = node("approve", "suspended", 0, 0, { suspendKey: "key-1" });
    mount(() => jsonResponse({ ...run([suspended]), status: "cancelled" }));
    await screen.findByText(s.run.selectNode);
    fireEvent.click(screen.getByRole("button", { name: /^approve/ }));
    await screen.findByRole("heading", { name: "approve" });
    expect(screen.queryByRole("button", { name: s.run.resume })).toBeNull();
  });
});
