import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/toast";
import { s } from "../../strings";
import { jsonResponse } from "../resource/test-fixtures";
import { TokensPanel } from "./tokens";

afterEach(() => vi.unstubAllGlobals());

describe("tokens", () => {
  it("creates an ingest token unless admin is chosen explicitly", async () => {
    const created: unknown[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      if (req.method === "POST") {
        created.push(await req.json());
        return jsonResponse({ id: 1, name: "alerts", scope: "ingest", token: "lcm_x", createdAt: "2026-11-05T00:00:00Z" }, 201);
      }
      return jsonResponse({ items: [], nextCursor: null });
    });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <ToastProvider>
          <TokensPanel />
        </ToastProvider>
      </QueryClientProvider>,
    );
    await user.click(await screen.findByRole("button", { name: s.settings.tokens.create }));
    expect((screen.getByLabelText(s.settings.tokens.scope) as HTMLSelectElement).value).toBe("ingest");
    await user.type(screen.getByLabelText(s.settings.tokens.name), "alerts");
    await user.click(screen.getByRole("button", { name: s.common.create }));
    await waitFor(() => expect(created).toEqual([{ name: "alerts", scope: "ingest" }]));
  });
});
