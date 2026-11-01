import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../api/machines";
import { MachineDetailPage } from "./machine-detail";
import { freshMachine, jsonResponse } from "./machine/test-fixtures";

afterEach(() => vi.unstubAllGlobals());

describe("machine phase controls", () => {
  it.each([
    { action: "退役", phase: "managed", method: "POST", path: "/phase", body: { phase: "retired" } },
    { action: "恢復納管", phase: "retired", method: "POST", path: "/phase", body: { phase: "managed" } },
    { action: "解除納管", phase: "retired", method: "DELETE", path: "", body: undefined },
  ] as const)("confirms $action for the named target before changing management state", async ({ action, phase, method, path, body }) => {
    const machine: Machine = { ...freshMachine(), phase };
    const writes: { method: string; path: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", async (request: Request) => {
      const url = new URL(request.url);
      if (request.method !== "GET") {
        const text = await request.text();
        writes.push({ method: request.method, path: url.pathname, body: text ? JSON.parse(text) : undefined });
        return request.method === "DELETE" ? new Response(null, { status: 204 }) : jsonResponse(machine);
      }
      if (url.pathname.endsWith("/maintenance")) return jsonResponse({ maintenance: null });
      if (url.pathname.endsWith("/capabilities") || url.pathname.endsWith("/versions")) return jsonResponse({ items: [], nextCursor: null });
      return jsonResponse(machine);
    });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter initialEntries={["/machines/workstation/config"]}>
          <Routes>
            <Route path="/machines/:name/:tab" element={<MachineDetailPage />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await userEvent.click(await screen.findByRole("button", { name: action }));
    expect(writes).toHaveLength(0);
    expect(screen.getByRole("dialog").textContent).toContain("workstation");
    expect(screen.getByRole("dialog").textContent).toContain("不會關機");
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "取消" }));
    expect(writes).toHaveLength(0);
    await userEvent.click(screen.getByRole("button", { name: action }));
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "確認" }));
    await waitFor(() => expect(writes).toEqual([{ method, path: `/api/v1/machines/workstation${path}`, body }]));
  });
});
