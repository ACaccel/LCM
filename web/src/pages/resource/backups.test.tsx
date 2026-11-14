import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ActionPlan, Backup } from "../../api/types";
import { translate } from "../../lib/messages";
import { ActionDialog } from "./action-dialog";
import { BackupsTab, describeSource, restorable } from "./backups";
import { jsonResponse } from "./test-fixtures";

const SET = "20261101T120000Z-0a1b2c3d";
const PRE = "pre-restore-20261101T130000Z-0a1b2c3d";

const backup = (over: Partial<Backup>): Backup => ({
  id: 1,
  resource: "web",
  kind: "backup",
  backupId: SET,
  startedAt: "2026-11-01T12:00:00Z",
  finishedAt: "2026-11-01T12:00:09Z",
  outcome: "succeeded",
  size: 5 * 1024 * 1024,
  sources: [
    { type: "files", path: "media" },
    { type: "command", argv: ["bin/dump", "--all"] },
  ],
  manifestSha256: "a".repeat(64),
  skipped: 2,
  actor: { kind: "user", id: "1", name: "admin" },
  runId: "",
  error: "",
  prunedAt: null,
  restorable: true,
  ...over,
});

afterEach(() => vi.unstubAllGlobals());

function renderTab(items: Backup[], canRestore: boolean, onRestore: (id: string) => void = () => undefined) {
  vi.stubGlobal("fetch", async () => jsonResponse({ items, nextCursor: null }));
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <BackupsTab resource="web" canRestore={canRestore} onRestore={onRestore} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

describe("backup sources", () => {
  it("names each type by what it backs up and knows what a restore skips", () => {
    expect(describeSource({ type: "files", path: "media" })).toBe("files media");
    expect(describeSource({ type: "mongodb", database: "app", users: true })).toBe("mongodb app + users");
    expect(describeSource({ type: "command", argv: ["bin/dump", "--all"] })).toBe("command bin/dump --all");
    expect(restorable({ type: "command", argv: ["x"] })).toBe(false);
    expect(restorable({ type: "sqlite", path: "a.db" })).toBe(true);
  });
});

describe("backups tab", () => {
  it("tells how to configure backups when there are none", async () => {
    renderTab([], true);
    expect(await screen.findByText("尚無備份紀錄。")).toBeTruthy();
    expect(screen.getByText(/backup\.sources/)).toBeTruthy();
  });

  it("lists sets and restores one by its ID", async () => {
    const restore = vi.fn();
    const user = userEvent.setup();
    renderTab(
      [
        backup({ id: 4, kind: "restore", backupId: SET, restorable: false, sources: [], size: 0, manifestSha256: "" }),
        backup({ id: 3, kind: "pre-restore", backupId: PRE, size: 2048, sources: [{ type: "files", path: "media" }], skipped: 0 }),
        backup({ id: 2, kind: "backup", backupId: "20261030T120000Z-deadbeef", restorable: false, prunedAt: "2026-11-02T00:00:00Z" }),
        backup({ id: 1, kind: "backup" }),
      ],
      true,
      restore,
    );
    expect(await screen.findByText(PRE)).toBeTruthy();
    // Command output is kept, not restored; skipped entries are counted.
    expect(screen.getAllByText(/command bin\/dump --all/)[0]?.textContent).toContain("指令輸出只保存，不會還原");
    expect(screen.getAllByText("2 個項目（連結、裝置、socket、pipe）沒有納入備份").length).toBeGreaterThan(0);
    expect(screen.getAllByText("5.0 MiB", { selector: "td" })).toHaveLength(2);
    expect(screen.getByText("已被保留規則移除")).toBeTruthy();

    const buttons = screen.getAllByRole("button", { name: "還原到此備份" });
    // The restore row has no button; the removed set's is disabled.
    expect(buttons).toHaveLength(3);
    expect(buttons.map((b) => (b as HTMLButtonElement).disabled)).toEqual([false, true, false]);
    expect(buttons[1]?.getAttribute("title")).toContain("已被保留規則移除");
    await user.click(buttons[0]!);
    expect(restore).toHaveBeenCalledWith(PRE);
  });

  it("disables restore when the service cannot restore now", async () => {
    renderTab([backup({})], false);
    const button = (await screen.findByRole("button", { name: "還原到此備份" })) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(button.title).toContain("目前不能還原");
  });
});

describe("restore steps", () => {
  it("translates the steps of a typed backup and a restore", () => {
    expect(translate({ code: "step.helperBackup", params: { sources: "files media", mode: "stopped" }, message: "back up …" })).toBe(
      "由 deploy helper 備份 files media（停機模式），存成備份目錄中的新備份",
    );
    expect(translate({ code: "step.helperPrune", params: { daily: "7", weekly: "4" }, message: "remove …" })).toContain("最近 7 天與 4 週");
    expect(translate({ code: "step.restoreApply", params: { sources: "files media", backup: SET }, message: "replace …" })).toBe(
      `用 ${SET} 取代 files media；失敗時自動換回還原前副本`,
    );
    expect(translate({ code: "step.restoreCheck", params: {}, message: "check …" })).toContain("還原「還原前副本」");
  });
});

describe("restore dialog", () => {
  const plan: ActionPlan = {
    resource: "web",
    action: "restore",
    from: "running",
    to: "running",
    steps: ["verify"],
    stepMessages: [{ code: "step.restoreVerify", params: { backup: SET }, message: "verify" }],
    impact: { resource: "web", dependents: [], controlDependents: [], unresolved: [], action: "restore", acknowledgementRequired: false },
  };

  it("names the backup in the preview and in the request", async () => {
    const seen: { path: string; query: string; body: unknown }[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      const text = await req.text();
      if (url.pathname.endsWith("/backups")) return jsonResponse({ items: [backup({ backupId: SET })], nextCursor: null });
      seen.push({ path: url.pathname, query: url.search, body: text ? JSON.parse(text) : undefined });
      if (url.searchParams.get("dryRun") === "true") return jsonResponse(plan);
      if (url.pathname === "/api/v1/actions/act-1") {
        return jsonResponse({
          id: "act-1", resource: "web", action: "restore", status: "running", actor: { kind: "user", id: "1", name: "admin" },
          startedAt: "2026-11-01T12:00:00Z", finishedAt: null, result: null, error: "",
        });
      }
      return jsonResponse({ actionId: "act-1", status: "running" }, 202);
    });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <ActionDialog resource="web" action="restore" backupId={SET} onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByText(`用 manifest 的 SHA-256 驗證備份 ${SET}`)).toBeTruthy();
    expect(screen.getByText(SET, { selector: "code" })).toBeTruthy();
    expect(seen[0]).toMatchObject({ path: "/api/v1/resources/web/actions/restore", query: "?dryRun=true", body: { backupId: SET } });
    const run = screen.getByRole("button", { name: /確認執行/ });
    await waitFor(() => expect((run as HTMLButtonElement).disabled).toBe(false));
    // A restore is destructive: the confirmation button is the danger one.
    expect(run.className).toContain("danger");
    await user.click(run);
    await waitFor(() => expect(seen.some((x) => x.query === "" && x.path.endsWith("/actions/restore"))).toBe(true));
    expect(seen.find((x) => x.query === "")?.body).toEqual({ backupId: SET });
    expect(within(document.body).queryByText(/最新的備份/)).toBeNull();
  });
  it("offers the MongoDB users of a set that holds them and sends the typed name with the request", async () => {
    const seen: { path: string; query: string; body: unknown }[] = [];
    const withUsers = backup({ backupId: SET, sources: [{ type: "mongodb", database: "app", users: true }] });
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      const text = await req.text();
      if (url.pathname.endsWith("/backups")) return jsonResponse({ items: [withUsers], nextCursor: null });
      seen.push({ path: url.pathname, query: url.search, body: text ? JSON.parse(text) : undefined });
      if (url.searchParams.get("dryRun") === "true") return jsonResponse(plan);
      if (url.pathname === "/api/v1/actions/act-1") {
        return jsonResponse({
          id: "act-1", resource: "web", action: "restore", status: "running", actor: { kind: "user", id: "1", name: "admin" },
          startedAt: "2026-11-01T12:00:00Z", finishedAt: null, result: null, error: "",
        });
      }
      return jsonResponse({ actionId: "act-1", status: "running" }, 202);
    });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <ActionDialog resource="web" action="restore" onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    // The option is off by default and the plain request carries no users.
    const box = await screen.findByRole("checkbox", { name: /MongoDB 的使用者與角色/ });
    expect((box as HTMLInputElement).checked).toBe(false);
    expect(seen[0]?.body).toEqual({});

    await user.click(box);
    await user.click(screen.getByRole("button", { name: /預覽/ }));
    await waitFor(() => expect(seen.at(-1)?.body).toEqual({ restoreUsers: true, confirm: "web" }));
    // Replacing every account needs the service name typed first.
    const run = screen.getByRole("button", { name: /確認執行/ }) as HTMLButtonElement;
    expect(run.disabled).toBe(true);
    await user.type(screen.getByLabelText(/輸入服務名稱 web/), "web");
    await waitFor(() => expect(run.disabled).toBe(false));
    await user.click(run);
    await waitFor(() => expect(seen.some((x) => x.query === "" && x.path.endsWith("/actions/restore"))).toBe(true));
    expect(seen.find((x) => x.query === "")?.body).toEqual({ restoreUsers: true, confirm: "web" });
  });

  it("does not offer users for a set without them", async () => {
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      if (url.pathname.endsWith("/backups")) return jsonResponse({ items: [backup({ backupId: SET })], nextCursor: null });
      return jsonResponse(plan);
    });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter>
          <ActionDialog resource="web" action="restore" backupId={SET} onClose={() => undefined} />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await screen.findByText(`用 manifest 的 SHA-256 驗證備份 ${SET}`);
    expect(screen.queryByRole("checkbox")).toBeNull();
  });
});
