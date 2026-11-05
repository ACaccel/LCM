import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { RunSummary } from "../api/types";
import { s } from "../strings";
import { RunsPage } from "./runs";
import { jsonResponse } from "./resource/test-fixtures";

const run: RunSummary = {
  id: "run-0001-aaaa",
  policy: "deploy-web",
  version: 3,
  status: "failed",
  mode: "live",
  triggerNode: "start",
  error: "health check timed out after 5m",
  createdAt: "2026-11-05T01:00:00Z",
  startedAt: "2026-11-05T01:00:00Z",
  finishedAt: "2026-11-05T01:05:00Z",
};

afterEach(() => vi.unstubAllGlobals());

describe("runs", () => {
  it("reads filters from the URL, writes changes back and shows the error", async () => {
    const reads: URLSearchParams[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      if (url.pathname === "/api/v1/policies") return jsonResponse({ items: [], nextCursor: null });
      reads.push(url.searchParams);
      return jsonResponse({ items: [run], nextCursor: null });
    });
    const router = createMemoryRouter([{ path: "/runs", element: <RunsPage /> }], { initialEntries: ["/runs?policy=deploy-web&status=failed"] });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    expect(await screen.findByText(run.error!)).toBeTruthy();
    expect(reads[0]?.get("policy")).toBe("deploy-web");
    expect(reads[0]?.getAll("status")).toEqual(["failed"]);
    expect((screen.getByLabelText(s.runs.filterPolicy) as HTMLSelectElement).value).toBe("deploy-web");
    expect((screen.getByRole("checkbox", { name: s.runStatus.failed }) as HTMLInputElement).checked).toBe(true);

    await userEvent.click(screen.getByRole("checkbox", { name: s.runStatus.running }));
    await waitFor(() => expect(router.state.location.search).toBe("?policy=deploy-web&status=running&status=failed"));
    await waitFor(() => expect(reads.at(-1)?.getAll("status")).toEqual(["running", "failed"]));

    await userEvent.selectOptions(screen.getByLabelText(s.runs.filterPolicy), "");
    await waitFor(() => expect(router.state.location.search).toBe("?status=running&status=failed"));
  });
});

describe("run list states", () => {
  const second: RunSummary = { ...run, id: "run-0002-bbbb", policy: "backup-db", status: "succeeded", mode: "dry", error: "", createdAt: "2026-11-05T02:00:00Z", startedAt: "2026-11-05T02:00:00Z", finishedAt: "2026-11-05T02:01:00Z" };

  function mount(runs: (url: URL) => Response | Promise<Response>, entry = "/runs") {
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      if (url.pathname === "/api/v1/policies") return jsonResponse({ items: [], nextCursor: null });
      return runs(url);
    });
    const router = createMemoryRouter([{ path: "/runs", element: <RunsPage /> }], { initialEntries: [entry] });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <RouterProvider router={router} />
      </QueryClientProvider>,
    );
    return router;
  }
  const rowNames = () =>
    screen
      .getAllByRole("row")
      .slice(1)
      .map((r) => within(r).getAllByRole("link")[1]?.textContent);

  it("shows a spinner while loading and the empty note, not a table, when no run matches", async () => {
    mount(() => jsonResponse({ items: [], nextCursor: null }));
    expect(screen.getByRole("status")).toBeTruthy();
    expect(await screen.findByText(s.runs.empty)).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("reports a failed read and loads again on retry", async () => {
    let failing = true;
    mount(() => (failing ? jsonResponse({ error: { code: "unavailable", message: "Runs unavailable", details: {} } }, 503) : jsonResponse({ items: [run], nextCursor: null })));
    expect((await screen.findByRole("alert")).textContent).toContain("Runs unavailable");
    expect(screen.queryByText(s.runs.empty)).toBeNull();
    failing = false;
    await userEvent.click(screen.getByRole("button", { name: s.common.retry }));
    expect(await screen.findByText(run.error!)).toBeTruthy();
  });

  it("links each run and its policy, and marks dry runs", async () => {
    mount(() => jsonResponse({ items: [run, second], nextCursor: null }));
    await screen.findByText(run.error!);
    const rows = screen.getAllByRole("row").slice(1);
    // Newest first: the dry run, then the live one.
    const live = within(rows[1]!);
    expect(live.getByRole("link", { name: "run-0001" }).getAttribute("href")).toBe("/runs/run-0001-aaaa");
    expect(live.getByRole("link", { name: "deploy-web" }).getAttribute("href")).toBe("/policies/deploy-web");
    expect(live.queryByText(s.runMode["dry"]!)).toBeNull();
    expect(within(rows[0]!).getByText(s.runMode["dry"]!)).toBeTruthy();
  });

  it("pages with the cursor and keeps the loaded rows", async () => {
    const cursors: (string | null)[] = [];
    mount((url) => {
      cursors.push(url.searchParams.get("cursor"));
      return url.searchParams.get("cursor") ? jsonResponse({ items: [second], nextCursor: null }) : jsonResponse({ items: [run], nextCursor: "c1" });
    });
    await userEvent.click(await screen.findByRole("button", { name: s.common.more }));
    await waitFor(() => expect(screen.getAllByRole("row")).toHaveLength(3));
    expect(cursors).toEqual([null, "c1"]);
    expect(screen.queryByRole("button", { name: s.common.more })).toBeNull();
  });

  it("sorts the loaded rows by a column and writes the order to the URL", async () => {
    const router = mount(() => jsonResponse({ items: [run, second], nextCursor: null }));
    await screen.findByText(run.error!);
    // The default order is newest first.
    expect(rowNames()).toEqual(["backup-db", "deploy-web"]);
    await userEvent.click(screen.getByRole("button", { name: new RegExp(s.runs.columns.policy) }));
    await waitFor(() => expect(router.state.location.search).toContain("sort=policy"));
    const dir = new URLSearchParams(router.state.location.search).get("dir");
    expect(rowNames()).toEqual(dir === "asc" ? ["backup-db", "deploy-web"] : ["deploy-web", "backup-db"]);
    expect(screen.getByRole("columnheader", { name: new RegExp(s.runs.columns.policy) }).getAttribute("aria-sort")).toBe(dir === "asc" ? "ascending" : "descending");
  });

  it("keeps a policy from the URL selectable although it is not deployed any more", async () => {
    mount(() => jsonResponse({ items: [], nextCursor: null }), "/runs?policy=retired-policy");
    const select = (await screen.findByLabelText(s.runs.filterPolicy)) as HTMLSelectElement;
    expect(select.value).toBe("retired-policy");
  });
});
