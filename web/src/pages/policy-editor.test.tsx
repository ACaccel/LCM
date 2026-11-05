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
