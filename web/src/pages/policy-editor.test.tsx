import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createMemoryRouter, RouterProvider } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { GraphProps } from "../components/graph";
import { ToastProvider } from "../components/toast";
import { s } from "../strings";
import { PolicyEditorPage } from "./policy-editor";
import { jsonResponse } from "./resource/test-fixtures";

// React Flow needs layout APIs jsdom lacks; the stub lists nodes and edges and deletes them like the canvas does.
vi.mock("../components/graph", () => ({
  PolicyGraph: ({ model, onDelete }: GraphProps) => (
    <ul aria-label="graph">
      {model.nodes.map((n) => (
        <li key={n.id}>
          <span data-node="">{n.id}</span>
          <button type="button" onClick={() => onDelete?.([n.id], [])}>{`delete ${n.id}`}</button>
        </li>
      ))}
      {model.edges.map((e) => (
        <li key={e.id}>
          <button type="button" onClick={() => onDelete?.([], [e])}>{`delete ${e.from}->${e.to}`}</button>
        </li>
      ))}
    </ul>
  ),
}));
vi.mock("../components/yaml-editor-lazy", () => ({
  YamlEditor: ({ value, onChange, ariaLabel }: { value: string; onChange: (v: string) => void; ariaLabel: string }) => (
    <textarea aria-label={ariaLabel} value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));

const TEXT = `apiVersion: lcm/v2
kind: Policy
metadata:
  name: demo
spec:
  nodes:
    - id: start
      type: trigger.manual
      next:
        out: [notify]
    - id: notify
      type: notify.log
`;
const DOCUMENT = {
  apiVersion: "lcm/v2",
  kind: "Policy",
  metadata: { name: "demo" },
  spec: { nodes: [{ id: "start", type: "trigger.manual", next: { out: ["notify"] } }, { id: "notify", type: "notify.log" }] },
};

function detail(deployed: boolean) {
  return {
    name: "demo",
    labels: {},
    version: 2,
    createdAt: "2026-11-05T00:00:00Z",
    createdBy: { kind: "user", id: "1", name: "admin" },
    document: DOCUMENT,
    latestVersion: 2,
    deployedVersion: deployed ? 2 : null,
    deployed,
    paused: false,
    deployedAt: deployed ? "2026-11-05T00:00:00Z" : null,
    triggers: [],
    lastRun: null,
    managedBy: null,
    warnings: [],
  };
}

function setup(deployed: boolean) {
  const puts: { match: string | null; body: string }[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    if (req.method === "PUT") {
      puts.push({ match: req.headers.get("If-Match"), body: await req.text() });
      return jsonResponse({ ...detail(deployed), version: 3, warnings: [] });
    }
    if (url.pathname === "/api/v1/policies/demo") return jsonResponse(detail(deployed));
    if (url.pathname === "/api/v1/policies/demo/versions/2") return jsonResponse(detail(deployed));
    if (url.pathname === "/api/v1/export") return new Response(TEXT, { status: 200, headers: { "Content-Type": "text/plain" } });
    if (url.pathname === "/api/v1/node-types" || url.pathname === "/api/v1/modules") return jsonResponse({ items: [], nextCursor: null });
    return jsonResponse({ error: { code: "not_found", message: `unexpected ${url.pathname}`, details: {} } }, 404);
  });
  const router = createMemoryRouter([{ path: "/policies/:name", element: <PolicyEditorPage /> }], { initialEntries: ["/policies/demo"] });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
      <ToastProvider>
        <RouterProvider router={router} />
      </ToastProvider>
    </QueryClientProvider>,
  );
  return puts;
}

const nodes = () => Array.from(document.querySelectorAll("[data-node]")).map((n) => n.textContent);

afterEach(() => vi.unstubAllGlobals());

describe("policy editor", () => {
  it("undoes the last node and edge deletion with Ctrl+Z, but not inside text fields", async () => {
    setup(true);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "delete notify" }));
    expect(nodes()).toEqual(["start"]);
    expect(screen.getByText(s.editor.unsaved)).toBeTruthy();

    await user.click(screen.getByRole("button", { name: s.editor.tabYaml }));
    fireEvent.keyDown(screen.getByLabelText("Policy YAML"), { key: "z", ctrlKey: true });
    expect(nodes()).toEqual(["start"]);

    fireEvent.keyDown(window, { key: "z", ctrlKey: true });
    await waitFor(() => expect(nodes()).toEqual(["start", "notify"]));
    expect(screen.queryByText(s.editor.unsaved)).toBeNull();
    expect(screen.queryByRole("button", { name: s.editor.undoDelete })).toBeNull();

    await user.click(screen.getByRole("button", { name: "delete start->notify" }));
    expect(screen.queryByRole("button", { name: "delete start->notify" })).toBeNull();
    await user.click(screen.getByRole("button", { name: s.editor.undoDelete }));
    expect(screen.getByRole("button", { name: "delete start->notify" })).toBeTruthy();
  });

  it("shows the change against the running version before deploying over it", async () => {
    const puts = setup(true);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "delete notify" }));
    await user.click(screen.getByRole("button", { name: s.editor.save }));
    const review = await screen.findByRole("dialog", { name: s.editor.reviewDeploy(2) });
    await waitFor(() => expect(Array.from(review.querySelectorAll(".diff .l.del")).map((l) => l.textContent).join("\n")).toContain("id: notify"));
    expect(puts).toHaveLength(0);
    await user.click(within(review).getByRole("button", { name: s.editor.save }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(puts[0]?.match).toBe("2");
    expect(puts[0]?.body).not.toContain("notify.log");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
  });

  it("saves a policy that is not deployed without a review", async () => {
    const puts = setup(false);
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "delete notify" }));
    await user.click(screen.getByRole("button", { name: s.editor.save }));
    await waitFor(() => expect(puts).toHaveLength(1));
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("policy editor page states and actions", () => {
  interface Calls {
    method: string;
    path: string;
    match: string | null;
    body: string;
  }

  /** Serves the demo policy; `extra` answers first and may return undefined to fall through. */
  function start(opts: { deployed?: boolean; missing?: boolean; entry?: string; extra?: (req: Request, url: URL) => Response | Promise<Response> | undefined } = {}) {
    const calls: Calls[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      const body = req.method === "GET" ? "" : await req.clone().text();
      if (req.method !== "GET") calls.push({ method: req.method, path: url.pathname, match: req.headers.get("If-Match"), body });
      const extra = opts.extra?.(req, url);
      if (extra) return extra;
      if (url.pathname === "/api/v1/node-types" || url.pathname === "/api/v1/modules") return jsonResponse({ items: [], nextCursor: null });
      if (opts.missing && url.pathname.startsWith("/api/v1/policies/demo")) return jsonResponse({ error: { code: "not_found", message: "no policy", details: {} } }, 404);
      if (req.method === "PUT") return jsonResponse({ ...detail(opts.deployed ?? true), version: 3, warnings: [] });
      if (req.method === "POST" || req.method === "DELETE") return jsonResponse({});
      if (url.pathname === "/api/v1/policies/demo" || url.pathname === "/api/v1/policies/demo/versions/2") return jsonResponse(detail(opts.deployed ?? true));
      if (url.pathname === "/api/v1/export") return new Response(TEXT, { status: 200, headers: { "Content-Type": "text/plain" } });
      return jsonResponse({ error: { code: "not_found", message: `unexpected ${url.pathname}`, details: {} } }, 404);
    });
    const router = createMemoryRouter([{ path: "/policies/:name", element: <PolicyEditorPage /> }], { initialEntries: [opts.entry ?? "/policies/demo"] });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })}>
        <ToastProvider>
          <RouterProvider router={router} />
        </ToastProvider>
      </QueryClientProvider>,
    );
    return calls;
  }

  it("says so when the policy does not exist and does not offer an editor", async () => {
    start({ missing: true });
    expect(await screen.findByText(s.editor.notFound)).toBeTruthy();
    expect(screen.queryByRole("button", { name: s.editor.save })).toBeNull();
  });

  it("starts a new policy from the template and saves it without a version to match", async () => {
    const calls = start({ missing: true, entry: "/policies/demo?new=1" });
    expect(await screen.findByText(s.editor.newBanner)).toBeTruthy();
    expect(screen.getByText(s.editor.newPolicy)).toBeTruthy();
    await userEvent.click(screen.getByRole("button", { name: s.editor.save }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PUT")).toHaveLength(1));
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.match).toBeNull();
    expect(put.body).toContain("name: demo");
  });

  it("explains a YAML syntax error and keeps validate and save disabled until it is fixed", async () => {
    start();
    const user = userEvent.setup();
    await screen.findByRole("button", { name: "delete notify" });
    await user.click(screen.getByRole("button", { name: s.editor.tabYaml }));
    const editor = screen.getByLabelText("Policy YAML");
    fireEvent.change(editor, { target: { value: "spec: [unclosed" } });
    expect((await screen.findByRole("alert")).textContent).toContain(s.editor.yamlError);
    expect((screen.getByRole("button", { name: s.editor.validate }) as HTMLButtonElement).disabled).toBe(true);
    expect((screen.getByRole("button", { name: s.editor.save }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.change(editor, { target: { value: TEXT } });
    await waitFor(() => expect((screen.getByRole("button", { name: s.editor.validate }) as HTMLButtonElement).disabled).toBe(false));
  });

  it("sends the current text to the server for validation and lists what it finds", async () => {
    let valid = false;
    const calls = start({
      extra: (_req, url) =>
        url.pathname === "/api/v1/validate"
          ? jsonResponse(valid ? { valid: true, issues: [], warnings: [] } : { valid: false, issues: [{ path: "spec.nodes[1].type", message: "unknown node type notify.log" }], warnings: [] })
          : undefined,
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: s.editor.validate }));
    expect(await screen.findByText(s.editor.invalid)).toBeTruthy();
    expect(screen.getByText(/unknown node type notify\.log/)).toBeTruthy();
    const post = calls.find((c) => c.path === "/api/v1/validate")!;
    expect(post.body).toContain("name: demo");
    valid = true;
    await user.click(screen.getByRole("button", { name: s.editor.validate }));
    expect(await screen.findByText(s.editor.valid)).toBeTruthy();
    expect(screen.queryByText(/unknown node type/)).toBeNull();
  });

  it("offers the stored version when the save lost to a newer one, and takes it on request", async () => {
    const calls = start({
      extra: (req) => (req.method === "PUT" ? jsonResponse({ error: { code: "version_conflict", message: "stale", details: { currentVersion: 5, expectedVersion: 2 } } }, 409) : undefined),
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "delete notify" }));
    await user.click(screen.getByRole("button", { name: s.editor.save }));
    const review = await screen.findByRole("dialog", { name: s.editor.reviewDeploy(2) });
    await user.click(within(review).getByRole("button", { name: s.editor.save }));
    const conflict = await screen.findByRole("dialog", { name: s.config.conflictTitle });
    expect(screen.queryByRole("dialog", { name: s.editor.reviewDeploy(2) })).toBeNull();
    expect(conflict.textContent).toContain(s.config.conflictBody(5, 2));
    await user.click(within(conflict).getByRole("button", { name: s.config.conflictReload }));
    await waitFor(() => expect(nodes()).toEqual(["start", "notify"]));
    expect(screen.queryByText(s.editor.unsaved)).toBeNull();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(1);
  });

  it("overwrites on top of the stored version only after the conflict is confirmed", async () => {
    let first = true;
    const calls = start({
      deployed: false,
      extra: (req) => {
        if (req.method !== "PUT" || !first) return undefined;
        first = false;
        return jsonResponse({ error: { code: "version_conflict", message: "stale", details: { currentVersion: 5 } } }, 409);
      },
    });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "delete notify" }));
    await user.click(screen.getByRole("button", { name: s.editor.save }));
    const conflict = await screen.findByRole("dialog", { name: s.config.conflictTitle });
    await user.click(within(conflict).getByRole("button", { name: s.config.conflictOverwrite }));
    await waitFor(() => expect(calls.filter((c) => c.method === "PUT")).toHaveLength(2));
    expect(calls.filter((c) => c.method === "PUT").map((c) => c.match)).toEqual(["2", "5"]);
  });

  it("pauses a deployed policy and removes its deployment only after a confirmation", async () => {
    const calls = start();
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: s.editor.pause }));
    await waitFor(() => expect(calls.map((c) => `${c.method} ${c.path}`)).toContain("POST /api/v1/policies/demo/pause"));
    await user.click(screen.getByRole("button", { name: s.editor.undeploy }));
    const dialog = screen.getByRole("dialog");
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    await user.click(within(dialog).getByRole("button", { name: s.editor.undeploy }));
    await waitFor(() => expect(calls.map((c) => `${c.method} ${c.path}`)).toContain("DELETE /api/v1/policies/demo/deployment"));
  });

  it("offers run, history and deployment controls only for a stored policy", async () => {
    start({ deployed: false });
    await screen.findByRole("button", { name: "delete notify" });
    expect(screen.getByRole("button", { name: s.editor.run })).toBeTruthy();
    expect(screen.getByRole("button", { name: s.editor.history })).toBeTruthy();
    expect(screen.queryByRole("button", { name: s.editor.pause })).toBeNull();
    expect(screen.queryByRole("button", { name: s.editor.undeploy })).toBeNull();
  });
});
