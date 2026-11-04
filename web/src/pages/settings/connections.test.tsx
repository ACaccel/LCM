import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { jsonResponse } from "../resource/test-fixtures";
import { ConnectionsPanel } from "./connections";

afterEach(() => vi.unstubAllGlobals());

describe("connections", () => {
  it("lists host connections read only with their allowlists and users", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse({
        items: [
          { name: "home-router", type: "xiaomi", allowActions: ["reboot"], allowResources: [], resources: ["gateway"] },
          { name: "spare", type: "xiaomi", allowActions: [], allowResources: ["backup-router"], resources: [] },
        ],
        nextCursor: null,
      }),
    );
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <ConnectionsPanel />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByText("home-router")).toBeTruthy();
    expect(screen.getByText("reboot")).toBeTruthy();
    expect(screen.getByText("全部資源")).toBeTruthy();
    expect(screen.getByRole("link", { name: "gateway" }).getAttribute("href")).toBe("/resources/gateway");
    expect(screen.getByText("不允許寫入")).toBeTruthy();
    expect(screen.queryByRole("button")).toBeNull();
  });
});
