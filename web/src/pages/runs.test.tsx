import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
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
