import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Action, ActionPlan } from "../../api/types";
import { ActionDialog } from "./action-dialog";

const plan: ActionPlan = { service: "web", action: "deploy", from: "running", to: "running", steps: ["git fetch origin main", "build: make build", "restart"] };
const finished: Action = {
  id: "act-1",
  service: "web",
  action: "deploy",
  status: "succeeded",
  actor: { kind: "user", id: "1", name: "admin" },
  startedAt: "2026-10-28T10:00:00Z",
  finishedAt: "2026-10-28T10:00:05Z",
  result: { service: "web", action: "deploy", from: "running", to: "running", version: "a41c9e2f", durationMs: 5000 },
  error: "",
};

interface Seen {
  method: string;
  path: string;
  query: string;
  body: unknown;
}

function server(handlers: { preview?: () => Response }) {
  const seen: Seen[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    const text = await req.text();
    seen.push({ method: req.method, path: url.pathname, query: url.search, body: text ? JSON.parse(text) : undefined });
    const j = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
    if (url.pathname.endsWith("/actions/deploy") && url.searchParams.get("dryRun") === "true") return handlers.preview?.() ?? j(200, plan);
    if (url.pathname.endsWith("/actions/deploy")) return j(202, { actionId: "act-1", status: "running" });
    if (url.pathname === "/api/v1/actions/act-1") return j(200, finished);
    return j(404, { error: { code: "not_found", message: "unexpected " + url.pathname, details: {} } });
  });
  return seen;
}

function renderDialog() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <ActionDialog service="web" action="deploy" onClose={() => undefined} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => vi.unstubAllGlobals());

describe("ActionDialog", () => {
  it("previews first, runs only after confirmation and then shows the outcome", async () => {
    const seen = server({});
    const user = userEvent.setup();
    renderDialog();

    // The dry run starts by itself and lists the steps.
    expect(await screen.findByText("git fetch origin main")).toBeTruthy();
    expect(seen.map((s) => `${s.method} ${s.query}`)).toEqual(["POST ?dryRun=true"]);

    await user.type(screen.getByLabelText(/版本/), "a41c9e2");
    // Editing the input makes the shown plan stale: running is blocked until it is previewed again.
    expect((screen.getByRole("button", { name: "確認執行" }) as HTMLButtonElement).disabled).toBe(true);
    await user.click(screen.getByRole("button", { name: /預覽/ }));
    await waitFor(() => expect((screen.getByRole("button", { name: "確認執行" }) as HTMLButtonElement).disabled).toBe(false));
    expect(seen.at(-1)).toMatchObject({ query: "?dryRun=true", body: { version: "a41c9e2" } });

    await user.click(screen.getByRole("button", { name: "確認執行" }));
    expect(await screen.findByText("已完成")).toBeTruthy();
    expect(screen.getByText("a41c9e2f")).toBeTruthy();
    const run = seen.find((s) => s.method === "POST" && s.query === "");
    expect(run?.body).toEqual({ version: "a41c9e2" });
  });

  it("keeps the run button disabled and shows the server message when the preview fails", async () => {
    server({
      preview: () => new Response(JSON.stringify({ error: { code: "invalid_state", message: "service is busy", details: {} } }), { status: 409, headers: { "Content-Type": "application/json" } }),
    });
    renderDialog();
    expect((await screen.findByRole("alert")).textContent).toContain("service is busy");
    expect((screen.getByRole("button", { name: "確認執行" }) as HTMLButtonElement).disabled).toBe(true);
  });
});
