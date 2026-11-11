import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Action, ActionPlan } from "../../api/types";
import { ActionDialog } from "./action-dialog";

const noImpact = { resource: "web", dependents: [], controlDependents: [], unresolved: [], action: "deploy", acknowledgementRequired: false };
const plan: ActionPlan = {
  resource: "web",
  action: "deploy",
  from: "running",
  to: "running",
  steps: ["git fetch --prune origin", "build: make build", "a step of a newer server"],
  stepMessages: [
    { code: "step.fetch", params: { remote: "origin" }, message: "git fetch --prune origin" },
    { code: "step.build", params: { command: "make build" }, message: "build: make build" },
    { code: "step.fromTheFuture", params: {}, message: "a step of a newer server" },
  ],
  impact: noImpact,
};
const impactedPlan: ActionPlan = {
  ...plan,
  impact: {
    ...noImpact,
    dependents: [{ name: "api", type: "service", relation: "dependsOn", via: "web", depth: 1 }],
    controlDependents: [{ name: "nas", type: "service", relation: "controlPath", via: "api", depth: 2 }],
    acknowledgementRequired: true,
  },
};
const finished: Action = {
  id: "act-1",
  resource: "web",
  action: "deploy",
  status: "succeeded",
  actor: { kind: "user", id: "1", name: "admin" },
  startedAt: "2026-10-28T10:00:00Z",
  finishedAt: "2026-10-28T10:00:05Z",
  result: { resource: "web", action: "deploy", from: "running", to: "running", version: "a41c9e2f", durationMs: 5000 },
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
        <ActionDialog resource="web" action="deploy" onClose={() => undefined} />
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

    // The dry run starts by itself and lists the steps, translated by code;
    // a code the console does not know shows the server's text.
    expect(await screen.findByText("從 origin 取得最新 commit（git fetch --prune）")).toBeTruthy();
    expect(screen.getByText("建置：make build")).toBeTruthy();
    expect(screen.getByText("a step of a newer server")).toBeTruthy();
    expect(seen.map((s) => `${s.method} ${s.path}${s.query}`)).toEqual(["POST /api/v1/resources/web/actions/deploy?dryRun=true"]);

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

  it("lists the impact without a checkbox and acknowledges it when the run is confirmed", async () => {
    const seen = server({ preview: () => new Response(JSON.stringify(impactedPlan), { status: 200, headers: { "Content-Type": "application/json" } }) });
    const user = userEvent.setup();
    renderDialog();
    const group = await screen.findByRole("group", { name: "影響範圍" });
    expect(group.textContent).toContain("api（依賴 web）");
    expect(group.textContent).toContain("nas（經由 api 控制）");
    expect(screen.queryByRole("checkbox")).toBeNull();
    // The button names the verb and how many resources it reaches.
    const run = screen.getByRole("button", { name: "確認部署（影響 2 個資源）" }) as HTMLButtonElement;
    expect(run.disabled).toBe(false);
    await user.click(run);
    expect(await screen.findByText("已完成")).toBeTruthy();
    expect(seen.find((s) => s.method === "POST" && s.query === "")?.body).toEqual({ acknowledgeImpact: true });
  });

  it("names one instance in the title, the preview and the run", async () => {
    const seen: Seen[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      const text = await req.text();
      seen.push({ method: req.method, path: url.pathname, query: url.search, body: text ? JSON.parse(text) : undefined });
      const stopPlan: ActionPlan = {
        ...plan,
        action: "stop",
        steps: ["stop and disable bots@alpha.service"],
        stepMessages: [{ code: "step.instanceStop", params: { unit: "bots@alpha.service" }, message: "stop and disable bots@alpha.service" }],
      };
      const j = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
      if (url.pathname === "/api/v1/actions/act-2") return j(200, { ...finished, id: "act-2", resource: "bots", action: "stop", result: null });
      return url.search ? j(200, stopPlan) : j(202, { actionId: "act-2", status: "running" });
    });
    const user = userEvent.setup();
    const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={qc}>
        <MemoryRouter>
          <ActionDialog resource="bots" action="stop" instance="alpha" onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByText(/停止並停用 bots@alpha.service/)).toBeTruthy();
    expect(screen.getByText("停止 bots 的實例 alpha")).toBeTruthy();
    expect(seen[0]).toMatchObject({ path: "/api/v1/resources/bots/actions/stop", query: "?dryRun=true", body: { instance: "alpha" } });
    await user.click(screen.getByRole("button", { name: "確認執行" }));
    await waitFor(() => expect(seen.find((s) => s.method === "POST" && s.query === "")?.body).toEqual({ instance: "alpha" }));
  });
});
