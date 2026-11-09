import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { jsonResponse } from "../test-fixtures";
import { HostDriftNotice } from "./host-drift";

afterEach(() => vi.unstubAllGlobals());

type File = { path: string; state: string; detail?: string };

function answer(components: { name: string; state: string; files: File[] }[], status = 200) {
  const asked: string[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    asked.push(new URL(req.url).pathname);
    return status === 200 ? jsonResponse({ items: components, checkedAt: "2026-11-09T00:00:00Z" }) : jsonResponse({ error: { code: "internal", message: "x", details: {} } }, status);
  });
  return asked;
}

function show() {
  return render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <HostDriftNotice />
    </QueryClientProvider>,
  );
}

describe("host drift notice", () => {
  it("names the outdated files and the documented update command", async () => {
    const asked = answer([
      { name: "core", state: "outdated", files: [{ path: "/etc/systemd/system/lcm.service", state: "outdated" }, { path: "/etc/sudoers.d/lcm", state: "unknown", detail: "not readable as this user" }] },
      { name: "machine-power", state: "not_installed", files: [{ path: "/usr/local/libexec/lcm-machine-power", state: "missing" }] },
    ]);
    show();
    expect(await screen.findByText("主機上的 helper 檔案與這個 LCM 版本不一致")).toBeTruthy();
    expect(asked).toEqual(["/api/v1/host/components"]);
    expect(screen.getByText("/etc/systemd/system/lcm.service")).toBeTruthy();
    expect(screen.getByText("make build && sudo bin/lcm host apply")).toBeTruthy();
    expect(screen.getByText(/不要執行 sudo lcm host apply/)).toBeTruthy();
    expect(screen.getByRole("link", { name: "更新步驟說明" }).getAttribute("href")).toContain("docs/operations.md#rerunning-checking-and-updates");
    // A component that is not installed by choice is no drift, and unreadable files are no alarm.
    expect(screen.queryByText("/usr/local/libexec/lcm-machine-power")).toBeNull();
    expect(screen.queryByText("/etc/sudoers.d/lcm")).toBeNull();
  });

  it("warns about a partly installed component", async () => {
    answer([{ name: "machine-power", state: "missing", files: [{ path: "/etc/sudoers.d/lcm-machine-power", state: "missing" }, { path: "/usr/local/libexec/lcm-machine-power", state: "up_to_date" }] }]);
    show();
    expect(await screen.findByText("/etc/sudoers.d/lcm-machine-power")).toBeTruthy();
    expect(screen.getByText(/缺少/)).toBeTruthy();
  });

  it("shows only a quiet hint, no warning, for unreadable files", async () => {
    answer([{ name: "core", state: "unknown", files: [{ path: "/etc/sudoers.d/lcm", state: "unknown", detail: "not readable as this user" }, { path: "/usr/local/libexec/lcm/self-install", state: "up_to_date" }] }]);
    show();
    expect(await screen.findByText(/lcm 帳號無法讀取/)).toBeTruthy();
    expect(screen.getByText("/etc/sudoers.d/lcm")).toBeTruthy();
    expect(screen.queryByText("make build && sudo bin/lcm host apply")).toBeNull();
    expect(document.querySelector(".banner")).toBeNull();
  });

  it("shows nothing when everything is current, not installed, or unreadable by the server", async () => {
    const asked = answer([
      { name: "core", state: "up_to_date", files: [{ path: "/usr/local/libexec/lcm/self-install", state: "up_to_date" }] },
      { name: "security-host", state: "not_installed", files: [{ path: "/usr/local/libexec/lcm/security-host/helper.py", state: "missing" }] },
    ]);
    const { container } = show();
    await waitFor(() => expect(asked.length).toBe(1));
    await new Promise((r) => setTimeout(r, 0));
    expect(container.textContent).toBe("");
  });

  it("shows nothing when the host cannot be read", async () => {
    const asked = answer([], 500);
    const { container } = show();
    await waitFor(() => expect(asked.length).toBe(1));
    await new Promise((r) => setTimeout(r, 0));
    expect(container.textContent).toBe("");
  });
});
