import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Machine } from "../../api/machines";
import { readMachineAttempt } from "../../lib/machine";
import { MaintenanceDialog } from "./dialogs";
import { MachineControls, MachineOperations } from "./operations";
import { freshMachine, jsonResponse, maintenanceFixture, operationFixture } from "./test-fixtures";

interface Seen {
  path: string;
  match: string | null;
  body: Record<string, unknown>;
}
const caps = [
  { action: "reboot", available: true, reason: "" },
  { action: "graceful-shutdown", available: true, reason: "" },
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
    if (url.pathname.endsWith("/services")) return jsonResponse({ items: [], nextCursor: null });
	if(url.pathname.endsWith("/maintenance/preview"))return jsonResponse(maintenanceFixture.preview);
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
function controls(machine: Machine = freshMachine()) {
  return <MachineControls machine={machine} caps={caps} maintenance={maintenanceFixture} />;
}
async function confirmReboot() {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "重新開機" }));
  const button = screen.getByRole("button", { name: "確認" }) as HTMLButtonElement;
  expect(button.disabled).toBe(true);
  for (const checkbox of screen.getAllByRole("checkbox")) await user.click(checkbox);
  expect(button.disabled).toBe(false);
  await user.click(button);
}
afterEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("MachineControls", () => {
  it("keeps power-on, unsupported, stale and unknown identity controls disabled", () => {
    setupServer();
    const machine = freshMachine();
    machine.status.fresh = false;
    const view = render(wrap(controls(machine)));
    expect((screen.getByRole("button", { name: "開機" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/沒有獨立開機管道/)).toBeTruthy();
    expect((screen.getByRole("button", { name: "重新開機" }) as HTMLButtonElement).disabled).toBe(true);
    view.rerender(
      wrap(
        <MachineControls
          machine={freshMachine()}
          caps={[{ action: "reboot", available: false, reason: "helper unavailable" }]}
          maintenance={maintenanceFixture}
        />,
      ),
    );
    expect(screen.getByText("helper unavailable")).toBeTruthy();
    expect((screen.getByRole("button", { name: "重新開機" }) as HTMLButtonElement).disabled).toBe(true);
    const changed = freshMachine();
    changed.status.identity = "changed";
    view.rerender(wrap(controls(changed)));
    expect((screen.getByRole("button", { name: "重新開機" }) as HTMLButtonElement).disabled).toBe(true);
  });
  it("requires all confirmations, retains the same key/version after a lost response and refresh, and queries accepted operations", async () => {
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
    expect(readMachineAttempt(machine.id)?.key).toBe(key);
    first.unmount();
    render(wrap(controls({ ...machine, version: 4 })));
    await userEvent.click(screen.getByRole("button", { name: "以原 key 查詢／重試" }));
    expect(await screen.findByText("觀測中")).toBeTruthy();
    expect(posts).toHaveLength(2);
    expect(posts[1]).toMatchObject({ match: "3", body: { action: "reboot", idempotencyKey: key } });
    expect(posts.every((p) => p.path === "/api/v1/machines/workstation/operations")).toBe(true);
    expect(readMachineAttempt(machine.id)?.operationId).toBe("operation-1");
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
    for (const checkbox of screen.getAllByRole("checkbox")) await userEvent.click(checkbox);
    await userEvent.click(screen.getByRole("button", { name: "確認" }));
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
  it("requires a reason and acknowledgements and sends If-Match on entering maintenance", async () => {
    const posts = setupServer();
    render(wrap(<MaintenanceDialog machine={freshMachine()} exit={false} onClose={() => undefined} />));
    const button = screen.getByRole("button", { name: "確認" }) as HTMLButtonElement;
    expect(button.disabled).toBe(true);
    await userEvent.type(screen.getByRole("textbox"), "Scheduled inspection");
    expect(button.disabled).toBe(true);
    for (const checkbox of screen.getAllByRole("checkbox")) await userEvent.click(checkbox);
    await userEvent.click(button);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toMatchObject({
      path: "/api/v1/machines/workstation/maintenance",
      match: "3",
      body: { reason: "Scheduled inspection", acknowledged: true,previewHash:maintenanceFixture.preview.hash },
    });
  });
  it("exits maintenance explicitly with a reason", async () => {
    const posts = setupServer();
    render(wrap(<MaintenanceDialog machine={freshMachine()} exit onClose={() => undefined} />));
    await userEvent.type(screen.getByRole("textbox"), "Workloads inspected and recovered");
    for (const checkbox of screen.getAllByRole("checkbox")) await userEvent.click(checkbox);
    await userEvent.click(screen.getByRole("button", { name: "確認" }));
    await waitFor(() => expect(posts[0]?.path).toBe("/api/v1/machines/workstation/maintenance/exit"));
  });
  it("requires explicit manual resolution and shows it as unverified with actor/reason", async () => {
    const posts = setupServer();
    render(wrap(<MachineOperations name="workstation" />));
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
    expect(posts[0]).toMatchObject({ path: "/api/v1/machines/workstation/operations/operation-1/resolve", body: { reason: "Manual inspection" } });
    expect(screen.queryByText("已驗證成功")).toBeNull();
  });
});
