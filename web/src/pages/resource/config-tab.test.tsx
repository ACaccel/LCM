import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "../../components/toast";
import { ConfigTab } from "./config-tab";
import { jsonResponse, machineFixture, resourceFixture } from "./test-fixtures";

// CodeMirror needs layout APIs jsdom lacks; a textarea stands in for it.
vi.mock("../../components/yaml-editor-lazy", () => ({
  YamlEditor: ({ value, onChange, ariaLabel }: { value: string; onChange: (v: string) => void; ariaLabel: string }) => (
    <textarea aria-label={ariaLabel} value={value} onChange={(e) => onChange(e.target.value)} />
  ),
}));

function wrap(children: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return (
    <QueryClientProvider client={qc}>
      <ToastProvider>{children}</ToastProvider>
    </QueryClientProvider>
  );
}

interface Seen {
  method: string;
  path: string;
  match: string | null;
  body: string;
}

function server(handle: (req: Request, url: URL, seen: Seen[]) => Response | undefined) {
  const seen: Seen[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    if (req.method !== "GET") seen.push({ method: req.method, path: url.pathname, match: req.headers.get("If-Match"), body: await req.clone().text() });
    const answer = handle(req, url, seen);
    if (answer) return answer;
    if (url.pathname === "/api/v1/export") return new Response("apiVersion: lcm/v2\nkind: Resource\n", { status: 200, headers: { "Content-Type": "text/plain" } });
    if (url.pathname.endsWith("/versions")) {
      return jsonResponse({
        items: [3, 2, 1].map((version) => ({ version, createdAt: "2026-11-01T00:00:00Z", createdBy: machineFixture.updatedBy, restoredFrom: null })),
        nextCursor: null,
      });
    }
    return jsonResponse({ error: { code: "not_found", message: `unexpected ${url.pathname}`, details: {} } }, 404);
  });
  return seen;
}

afterEach(() => vi.unstubAllGlobals());

describe("resource config tab", () => {
  it("saves against the version the edit started from and resolves a conflict explicitly", async () => {
    const seen = server((req, _url, s) => {
      if (req.method !== "PUT") return undefined;
      return s.length === 1
        ? jsonResponse({ error: { code: "version_conflict", message: "Concurrent edit", details: { currentVersion: 4 } } }, 409)
        : jsonResponse(resourceFixture({ version: 5 }));
    });
    const user = userEvent.setup();
    const view = render(wrap(<ConfigTab detail={resourceFixture()} />));
    const editor = await screen.findByLabelText("Resource YAML");
    await user.type(editor, "# edited");
    // A background refresh to a newer version must not move the base of the draft.
    view.rerender(wrap(<ConfigTab detail={resourceFixture({ version: 4 })} />));
    await user.click(screen.getByRole("button", { name: "儲存新版本" }));
    const dialog = await screen.findByRole("dialog");
    expect(seen[0]).toMatchObject({ method: "PUT", path: "/api/v1/resources/web", match: "3" });
    expect(seen[0]?.body).toContain("# edited");
    await user.click(within(dialog).getByRole("button", { name: "以最新版本為基礎覆寫" }));
    await waitFor(() => expect(seen.map((x) => x.match)).toEqual(["3", "4"]));
  });

  it("restores only after confirmation, with the version the confirmation was shown for", async () => {
    const seen = server((req) => (req.method === "POST" ? jsonResponse(resourceFixture({ version: 4 })) : undefined));
    const user = userEvent.setup();
    const view = render(wrap(<ConfigTab detail={resourceFixture()} />));
    const restore = await screen.findAllByRole("button", { name: "還原為新版本" });
    await user.click(restore.at(-1)!);
    expect(seen).toHaveLength(0);
    expect(screen.getByRole("dialog").textContent).toContain("版本 1");
    view.rerender(wrap(<ConfigTab detail={resourceFixture({ version: 4 })} />));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "還原為新版本" }));
    await waitFor(() => expect(seen).toEqual([{ method: "POST", path: "/api/v1/resources/web/restore", match: "3", body: '{"version":1}' }]));
  });

  it("shows an archived resource read only, addressed by its ID", async () => {
    const paths: string[] = [];
    server((_req, url) => {
      paths.push(decodeURIComponent(url.pathname));
      return undefined;
    });
    render(wrap(<ConfigTab detail={resourceFixture({ phase: "unmanaged", id: "old-1" })} />));
    expect(await screen.findByText("封存紀錄只能檢視。")).toBeTruthy();
    await screen.findAllByText(/版本 2/);
    expect(screen.queryByRole("button", { name: "儲存新版本" })).toBeNull();
    expect(screen.queryByRole("button", { name: "還原為新版本" })).toBeNull();
    expect(paths).toContain("/api/v1/resources/@old-1/versions");
  });
});
