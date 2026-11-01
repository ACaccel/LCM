import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { stringify } from "yaml";
import { MachineConfig, MachineVersions, parseMachineYaml } from "./config";
import { freshMachine, jsonResponse, machineFixture, maintenanceFixture } from "./test-fixtures";

function wrap(children: ReactNode) {
  return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>;
}
afterEach(() => vi.unstubAllGlobals());

describe("Machine document editing", () => {
  it("bounds YAML, enforces local registration and preserves the immutable name", () => {
    expect(parseMachineYaml("apiVersion: lcm/v1\nkind: Machine\nmetadata:\n  name: local-machine\nspec:\n  host: local\n").spec.host).toBe("local");
    expect(() => parseMachineYaml("[invalid")).toThrow("YAML 無法解析");
    expect(() => parseMachineYaml("x".repeat(65_537))).toThrow("64 KiB");
    expect(() => parseMachineYaml(stringify({ ...machineFixture.document, spec: { host: "ssh" } }))).toThrow("spec.host");
    expect(() => parseMachineYaml(stringify(machineFixture.document), "different-name")).toThrow("名稱不得變更");
  });
  it("keeps the original If-Match after background refresh, surfaces conflicts and requires explicit reload", async () => {
    const matches: (string | null)[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      matches.push(req.headers.get("If-Match"));
      expect(req.method).toBe("PUT");
      expect(new URL(req.url).pathname).toBe("/api/v1/machines/workstation");
      expect(((await req.json()) as { spec: { host: string } }).spec.host).toBe("local");
      return matches.length === 1
        ? jsonResponse({ error: { code: "version_conflict", message: "Concurrent edit", details: { currentVersion: 4 } } }, 409)
        : jsonResponse({ ...machineFixture, version: 5 });
    });
    const machine = freshMachine();
    const view = render(wrap(<MachineConfig machine={machine} />));
    view.rerender(wrap(<MachineConfig machine={{ ...machine, version: 4 }} />));
    await userEvent.click(screen.getByRole("button", { name: "儲存新版本" }));
    expect(await screen.findByRole("alert")).toBeTruthy();
    expect(screen.getByText(/不會自動覆寫/)).toBeTruthy();
    expect(matches).toEqual(["3"]);
    await userEvent.click(screen.getByRole("button", { name: "重新載入" }));
    await userEvent.click(screen.getByRole("button", { name: "儲存新版本" }));
    await waitFor(() => expect(matches).toEqual(["3", "4"]));
    expect(await screen.findByText("已儲存")).toBeTruthy();
  });
  it("only restores after confirmation and supplies the confirmed current version", async () => {
    const restores: { match: string | null; body: unknown }[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      if (req.method === "POST") {
        restores.push({ match: req.headers.get("If-Match"), body: await req.json() });
        return jsonResponse({ ...machineFixture, version: 4 });
      }
      return jsonResponse({
        items: [{ version: 1, createdAt: machineFixture.createdAt, createdBy: maintenanceFixture.actor, restoredFrom: 0, document: machineFixture.document }],
        nextCursor: null,
      });
    });
    render(wrap(<MachineVersions machine={freshMachine()} />));
    const summary = await screen.findByText(/版本 1 —/);
    await userEvent.click(summary);
    await userEvent.click(screen.getByRole("button", { name: "還原為新版本" }));
    expect(restores).toHaveLength(0);
    expect(screen.getByRole("dialog").textContent).toContain("還原為新的目前版本");
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "確認" }));
    await waitFor(() => expect(restores).toEqual([{ match: "3", body: { version: 1 } }]));
  });
});
