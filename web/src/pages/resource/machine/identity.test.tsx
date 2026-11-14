import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ResourceDetail } from "../../../api/types";
import { freshMachine, jsonResponse } from "../test-fixtures";
import { IdentityPanel } from "./controls";

function changedMachine(available = true): ResourceDetail {
  const machine = freshMachine();
  machine.machine!.status!.identity = "changed";
  machine.version = 4;
  machine.actions = [
    ...machine.actions,
    { name: "rebind-identity", kind: "action", available, reason: available ? "" : "x", reasonMessage: { code: "action.identityNotChanged", params: {}, message: "x" }, risk: "reversible", policy: false },
  ];
  return machine;
}

function renderPanel(machine: ResourceDetail) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <IdentityPanel resource={machine} />
    </QueryClientProvider>,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("IdentityPanel", () => {
  it("shows nothing while the identity is not changed", () => {
    renderPanel(freshMachine());
    expect(screen.queryByRole("button", { name: "接受目前身分" })).toBeNull();
  });

  it("stays disabled while the server does not offer the action", () => {
    renderPanel(changedMachine(false));
    expect((screen.getByRole("button", { name: "接受目前身分" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("sends the reason and the typed name at the version it was opened at", async () => {
    const posts: { path: string; match: string | null; body: Record<string, unknown> }[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      if (req.method === "POST") {
        posts.push({ path: url.pathname, match: req.headers.get("If-Match"), body: JSON.parse(await req.clone().text()) as Record<string, unknown> });
        return jsonResponse(freshMachine());
      }
      return jsonResponse({ error: { message: "Unexpected fixture request" } }, 404);
    });
    renderPanel(changedMachine());
    await userEvent.click(screen.getByRole("button", { name: "接受目前身分" }));
    const confirm = screen.getByRole("button", { name: "確認" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await userEvent.type(screen.getByLabelText("理由"), "Host moved");
    expect(confirm.disabled).toBe(true);
    await userEvent.type(screen.getByLabelText(/輸入資源名稱/), "workstation");
    expect(confirm.disabled).toBe(false);
    await userEvent.click(confirm);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]).toEqual({
      path: "/api/v1/resources/workstation/identity/rebind",
      match: "4",
      body: { reason: "Host moved", confirm: "workstation" },
    });
  });
});
