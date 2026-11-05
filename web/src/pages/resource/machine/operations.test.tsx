import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { MachineMaintenance, ResourceDetail } from "../../../api/types";
import { readAttempt } from "../../../lib/operation";
import { OperationsList } from "../operations";
import { freshMachine, jsonResponse, maintenanceFixture, noImpact, operationFixture } from "../test-fixtures";
import { MachineControls } from "./controls";
import { MaintenanceDialog } from "./dialogs";

interface Seen {
  path: string;
  match: string | null;
  body: Record<string, unknown>;
}
const caps = [
  { action: "reboot", available: true, reason: "", reasonMessage: { code: "", params: {}, message: "" } },
  { action: "graceful-shutdown", available: true, reason: "", reasonMessage: { code: "", params: {}, message: "" } },
];
function setupServer(submit?: (n: number, request: Request) => Response | Promise<Response>) {
  const posts: Seen[] = [];
  let resolved = false;
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    if (req.method === "POST") {
      posts.push({ path: url.pathname, match: req.headers.get("If-Match"), body: JSON.parse(await req.clone().text()) as Record<string, unknown> });
      if (url.pathname.endsWith("/resolve")) {
        resolved = true;
        return jsonResponse({
          ...operationFixture,
          state: "indeterminate",
          unresolved: false,
          resolvedBy: operationFixture.actor,
          resolution: "Manual inspection",
        });
      }
      if (url.pathname.endsWith("/operations"))
        return submit?.(posts.filter((p) => p.path.endsWith("/operations")).length, req) ?? jsonResponse(operationFixture, 202);
      if (url.pathname.endsWith("/maintenance")) return jsonResponse({ maintenance: maintenanceFixture });
      if (url.pathname.endsWith("/exit")) return new Response(null, { status: 204 });
    }
    if (url.pathname.endsWith("/resources")) return jsonResponse({ items: [], nextCursor: null });
    if (url.pathname.endsWith("/impact")) return jsonResponse(noImpact("workstation", url.searchParams.get("action") ?? ""));
    if (url.pathname.endsWith("/maintenance/preview")) return jsonResponse(maintenanceFixture.preview);
    if (url.pathname.endsWith("/operations/operation-1")) return jsonResponse(operationFixture);
    if (url.pathname.endsWith("/operations"))
      return jsonResponse({
        items: [
          {
            ...operationFixture,
            state: "indeterminate",
            ...(resolved ? { unresolved: false, resolvedBy: operationFixture.actor, resolution: "Manual inspection" } : {}),
          },
        ],
        nextCursor: null,
      });
    return jsonResponse({ error: { message: "Unexpected fixture request" } }, 404);
  });
  return posts;
}
function wrap(children: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}
function controls(machine: ResourceDetail = freshMachine(), maintenance: MachineMaintenance | null = maintenanceFixture) {
  return <MachineControls resource={machine} caps={caps} maintenance={maintenance} />;
}
async function fillConfirmation(name = "workstation") {
  const user = userEvent.setup();
  const button = screen.getByRole("button", { name: "確認" }) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  await user.type(screen.getByLabelText("理由"), "Kernel update");
  expect(button.disabled).toBe(true);
  await user.type(screen.getByLabelText(/輸入資源名稱/), name);
  return button;
}
async function confirmReboot() {
  await userEvent.click(screen.getByRole("button", { name: "重新開機" }));
  const button = await fillConfirmation();
  await waitFor(() => expect(button.disabled).toBe(false));
  await userEvent.click(button);
}
afterEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("MachineControls", () => {
  it("offers no power-on and keeps unsupported, stale and unknown identity controls disabled", () => {
    setupServer();
    const machine = freshMachine();
    machine.machine!.status!.fresh = false;
    const view = render(wrap(controls(machine)));
    expect(screen.queryByRole("button", { name: "開機" })).toBeNull();
    expect(screen.getByText(/沒有獨立開機管道/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "重新開機" }) as HTMLButtonElement).disabled).toBe(true);
    view.rerender(
      wrap(
        <MachineControls
          resource={freshMachine()}
          caps={[{ action: "reboot", available: false, reason: "helper unavailable", reasonMessage: { code: "", params: {}, message: "helper unavailable" } }]}
          maintenance={maintenanceFixture}
        />,
      ),
    );
    expect(screen.getByText("helper unavailable")).toBeTruthy();
    expect((screen.getByRole("button", { name: "重新開機" }) as HTMLButtonElement).disabled).toBe(true);
    const changed = freshMachine();
    changed.machine!.status!.identity = "changed";
    view.rerender(wrap(controls(changed)));
    expect((screen.getByRole("button", { name: "重新開機" }) as HTMLButtonElement).disabled).toBe(true);
  });
  it("enters maintenance with the request: one dialog lists the recorded workloads once and needs a reason and the typed name", async () => {
    const posts = setupServer();
    const fetch = globalThis.fetch;
    vi.stubGlobal("fetch", async (req: Request) =>
      new URL(req.url).pathname.endsWith("/maintenance/preview")
        ? jsonResponse({ ...maintenanceFixture.preview, workloads: [{ name: "cache", version: 2, state: "running", autostart: "unknown" }] })
        : fetch(req),
    );
    render(wrap(controls(freshMachine(), null)));
    await userEvent.click(screen.getByRole("button", { name: "重新開機" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/自動進入維護/)).toBeTruthy();
    expect(await within(dialog).findAllByText("cache")).toHaveLength(1);
    expect(screen.getAllByText(/此操作會讓同機 LCM 與控制台離線/)).toHaveLength(1);
    expect(within(dialog).queryAllByRole("checkbox")).toHaveLength(0);
    const button = await fillConfirmation("workstatio");
    expect(button.disabled).toBe(true);
    await userEvent.type(screen.getByLabelText(/輸入資源名稱/), "n");
    await waitFor(() => expect(button.disabled).toBe(false));
    await userEvent.click(button);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toMatchObject({
      path: "/api/v1/resources/workstation/operations",
      match: "3",
      body: { action: "reboot", reason: "Kernel update", confirm: "workstation" },
    });
    expect(typeof posts[0]?.body.idempotencyKey).toBe("string");
  });
  it("keeps an active maintenance instead of listing a new one", async () => {
    setupServer();
    render(wrap(controls()));
    await userEvent.click(screen.getByRole("button", { name: "重新開機" }));
    expect(screen.getByText(/維護已啟用（Scheduled maintenance）/)).toBeTruthy();
    expect(screen.queryByText(/自動進入維護/)).toBeNull();
  });
  it("retains the same key/version after a lost response and refresh, and queries accepted operations", async () => {
    const posts = setupServer((n) => {
      if (n === 1) throw new TypeError("Simulated connection loss");
      return jsonResponse(operationFixture, 202);
    });
    const machine = freshMachine();
    const first = render(wrap(controls(machine)));
    expect(posts).toHaveLength(0);
    await confirmReboot();
    expect(await screen.findByText(/已保留原 key/)).toBeTruthy();
    expect(posts).toHaveLength(1);
    expect(posts[0]?.match).toBe("3");
    expect(posts[0]?.body.action).toBe("reboot");
    const key = posts[0]?.body.idempotencyKey;
    expect(readAttempt(machine.id)?.key).toBe(key);
    first.unmount();
    render(wrap(controls({ ...machine, version: 4 })));
    await userEvent.click(screen.getByRole("button", { name: "以原 key 查詢／重試" }));
    expect(await screen.findByText("觀測中")).toBeTruthy();
    expect(posts).toHaveLength(2);
    expect(posts[1]).toMatchObject({ match: "3", body: { action: "reboot", idempotencyKey: key, reason: "Kernel update", confirm: "workstation" } });
    expect(posts.every((p) => p.path === "/api/v1/resources/workstation/operations")).toBe(true);
    expect(readAttempt(machine.id)?.operationId).toBe("operation-1");
    await userEvent.click(screen.getByRole("button", { name: "查詢操作" }));
    expect(posts).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "結束此嘗試" })).toBeNull();
  });
  it.each([408, 503])("retains the original key after HTTP %s and a later conflict", async (status) => {
    const posts = setupServer((n) => jsonResponse({ error: { code: "unavailable", message: "Uncertain response" } }, n === 1 ? status : 409));
    render(wrap(controls()));
    await confirmReboot();
    await userEvent.click(await screen.findByRole("button", { name: "以原 key 查詢／重試" }));
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[1]?.body.idempotencyKey).toBe(posts[0]?.body.idempotencyKey);
    expect(screen.queryByRole("button", { name: "結束此嘗試" })).toBeNull();
  });
  it("pins If-Match to the version the user confirmed", async () => {
    const posts = setupServer();
    const machine = freshMachine();
    const view = render(wrap(controls(machine)));
    await userEvent.click(screen.getByRole("button", { name: "重新開機" }));
    view.rerender(wrap(controls({ ...machine, version: 4 })));
    const button = await fillConfirmation();
    await waitFor(() => expect(button.disabled).toBe(false));
    await userEvent.click(button);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]?.match).toBe("3");
  });
  it("uses a new key only after a definitive rejected attempt is ended and confirmed again", async () => {
    const posts = setupServer(() => jsonResponse({ error: { code: "precondition", message: "Maintenance required" } }, 409));
    render(wrap(controls()));
    await confirmReboot();
    await userEvent.click(await screen.findByRole("button", { name: "結束此嘗試" }));
    await confirmReboot();
    await waitFor(() => expect(posts).toHaveLength(2));
    expect(posts[0]?.body.idempotencyKey).not.toBe(posts[1]?.body.idempotencyKey);
  });
  it("does not send an operation when persistence fails", async () => {
    const posts = setupServer();
    render(wrap(controls()));
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("Storage unavailable");
    });
    await confirmReboot();
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(posts).toHaveLength(0);
  });
});

describe("maintenance and manual resolution", () => {
  it("enters maintenance with a reason and the reviewed preview's hash", async () => {
    const posts = setupServer();
    render(wrap(<MaintenanceDialog machine={freshMachine()} exit={false} onClose={() => undefined} />));
    const button = screen.getByRole("button", { name: "確認" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    expect(await screen.findByText("沒有已知的服務。")).toBeTruthy();
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    await userEvent.type(screen.getByRole("textbox"), "Scheduled inspection");
    await userEvent.click(button);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({
      path: "/api/v1/resources/workstation/maintenance",
      match: "3",
      body: { reason: "Scheduled inspection", previewHash: maintenanceFixture.preview.hash },
    });
  });
  it("exits maintenance with only a reason", async () => {
    const posts = setupServer();
    render(wrap(<MaintenanceDialog machine={freshMachine()} exit onClose={() => undefined} />));
    expect(screen.queryAllByRole("checkbox")).toHaveLength(0);
    await userEvent.type(screen.getByRole("textbox"), "Workloads inspected and recovered");
    await userEvent.click(screen.getByRole("button", { name: "確認" }));
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({ path: "/api/v1/resources/workstation/maintenance/exit", match: "3", body: { reason: "Workloads inspected and recovered" } });
  });
  it("shows the operator's request reason and the translated state reason", async () => {
    setupServer();
    render(wrap(<OperationsList resource="workstation" />));
    expect(await screen.findByText("請求理由")).toBeTruthy();
    expect(screen.getByText("Kernel update")).toBeTruthy();
    expect(screen.getByText("狀態說明")).toBeTruthy();
    // The state reason is the translation of its code, not the server's English text.
    expect(screen.getByText("操作缺少已驗證的完成證據")).toBeTruthy();
    expect(screen.queryByText("Awaiting new boot evidence")).toBeNull();
  });
  it("requires explicit manual resolution and shows it as unverified with actor/reason", async () => {
    const posts = setupServer();
    render(wrap(<OperationsList resource="workstation" />));
    await userEvent.click(await screen.findByRole("button", { name: "人工結案（未驗證）" }));
    const dialog = screen.getByRole("dialog");
    expect(dialog.textContent).toContain("不等於已驗證成功");
    const buttons = screen.getAllByRole("button", { name: "人工結案（未驗證）" });
    const submit = buttons.at(-1) as HTMLButtonElement;
    expect(submit.disabled).toBe(true);
    await userEvent.type(screen.getByRole("textbox"), "Manual inspection");
    expect(submit.disabled).toBe(true);
    await userEvent.click(screen.getByRole("checkbox"));
    await userEvent.click(submit);
    expect(await screen.findByText("人工結案／結果未驗證")).toBeTruthy();
    expect(screen.getByText("Manual inspection")).toBeTruthy();
    expect(posts).toHaveLength(1);
    expect(posts[0]).toMatchObject({ path: "/api/v1/resources/workstation/operations/operation-1/resolve", body: { reason: "Manual inspection" } });
    expect(screen.queryByText("已驗證成功")).toBeNull();
  });
});
