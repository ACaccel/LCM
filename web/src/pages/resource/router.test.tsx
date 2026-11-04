import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ResourceSnapshot, RouterSnapshot } from "../../api/types";
import { formatBytes } from "../../lib/format";
import { RouterControls, RouterMonitoring, RouterSnapshotView } from "./router";
import { jsonResponse, noImpact, operationFixture, routerFixture } from "./test-fixtures";

const snapshot: RouterSnapshot = routerFixture.router!.status!.snapshot!;

function wrap(children: ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

afterEach(() => {
  localStorage.clear();
  vi.unstubAllGlobals();
});

describe("router snapshot", () => {
  it("shows outcome, metrics, WAN and source coverage, leaving missing values unknown", () => {
    render(<RouterSnapshotView snapshot={snapshot} />);
    expect(screen.getByText("成功")).toBeTruthy();
    expect(screen.getByText("RA70")).toBeTruthy();
    expect(screen.getByText("1.0.168")).toBeTruthy();
    expect(screen.getByText("12.5%")).toBeTruthy();
    expect(screen.getByText("(4 核心)")).toBeTruthy();
    expect(screen.getByText("已連結")).toBeTruthy();
    expect(screen.getByText("↑ 2.0 KiB/s / ↓ 1.0 MiB/s")).toBeTruthy();
    expect(screen.getByText("↑ 1.0 GiB / ↓ 未知")).toBeTruthy();
    const wanRow = screen.getByRole("cell", { name: "wan" }).closest("tr")!;
    expect(within(wanRow).getByText("wan status request failed")).toBeTruthy();
  });

  it("names a failed attempt instead of showing measurements as current", () => {
    render(<RouterSnapshotView snapshot={{ ...snapshot, outcome: "auth-failed", reason: "login rejected", cpuPercent: null }} />);
    expect(screen.getByText("登入失敗")).toBeTruthy();
    expect(screen.getByText("login rejected")).toBeTruthy();
  });

  it("formats byte sizes with binary units", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(null)).toBe("-");
  });
});

describe("router monitoring", () => {
  it("shows reachability and boot time and plots stored CPU and memory", async () => {
    const history: ResourceSnapshot[] = [
      { id: 1, resourceId: "router-1", type: "router", observedAt: "2026-11-01T00:00:00Z", router: { ...snapshot, id: 1 } },
      { id: 2, resourceId: "router-1", type: "router", observedAt: "2026-11-01T00:01:00Z", router: { ...snapshot, id: 2, outcome: "unreachable", cpuPercent: null, memoryPercent: null } },
    ];
    vi.stubGlobal("fetch", async (req: Request) => {
      const path = new URL(req.url).pathname;
      if (path.endsWith("/history")) return jsonResponse({ items: history, nextCursor: null });
      return jsonResponse({ error: { message: "Unexpected fixture request" } }, 404);
    });
    const { container } = render(wrap(<RouterMonitoring resource={routerFixture} selector="gateway" />));
    expect(screen.getByText("可連線")).toBeTruthy();
    expect(screen.getByText("推估開機時間")).toBeTruthy();
    expect(await screen.findByRole("img", { name: "CPU 與記憶體使用率趨勢" })).toBeTruthy();
    // The unreachable attempt has no measurements and is not plotted as zero.
    expect(container.querySelectorAll("figure circle")).toHaveLength(1);
    expect([...container.querySelectorAll("summary")].map((x) => x.textContent?.split(" — ")[1])).toEqual(["CPU 使用率 12.5% · 記憶體使用率 40.0%", "無法連線"]);
  });
});

describe("router reboot", () => {
  it.each([
    { name: "without dependents", impact: noImpact("gateway", "reboot"), checks: 1, ack: undefined },
    {
      name: "with a control dependent",
      impact: { ...noImpact("gateway", "reboot"), controlDependents: [{ name: "nas", type: "service" as const, relation: "controlPath" as const, via: "gateway", depth: 1 }], acknowledgementRequired: true },
      checks: 2,
      ack: true,
    },
  ])("submits through the durable operation flow only after acknowledgement ($name)", async ({ impact, checks, ack }) => {
    const posts: { path: string; match: string | null; body: Record<string, unknown> }[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const path = new URL(req.url).pathname;
      if (req.method === "POST") {
        posts.push({ path, match: req.headers.get("If-Match"), body: JSON.parse(await req.text()) as Record<string, unknown> });
        return jsonResponse({ ...operationFixture, resourceId: "router-1" }, 202);
      }
      if (path.endsWith("/capabilities")) return jsonResponse({ items: [{ action: "reboot", available: true, reason: "" }] });
      if (path.endsWith("/impact")) return jsonResponse(impact);
      if (path.endsWith("/operations/operation-1")) return jsonResponse({ ...operationFixture, resourceId: "router-1" });
      return jsonResponse({ error: { message: "Unexpected fixture request" } }, 404);
    });
    const user = userEvent.setup();
    render(wrap(<RouterControls resource={routerFixture} selector="gateway" />));
    const reboot = await screen.findByRole("button", { name: "重新開機" });
    await waitFor(() => expect((reboot as HTMLButtonElement).disabled).toBe(false));
    await user.click(reboot);
    const dialog = screen.getByRole("dialog");
    const confirm = within(dialog).getByRole("button", { name: "確認" }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    await waitFor(() => expect(within(dialog).getAllByRole("checkbox")).toHaveLength(checks));
    for (const box of within(dialog).getAllByRole("checkbox")) {
      expect(confirm.disabled).toBe(true);
      await user.click(box);
    }
    expect(confirm.disabled).toBe(false);
    await user.click(confirm);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.path).toBe("/api/v1/resources/gateway/operations");
    expect(posts[0]!.match).toBe("3");
    expect(posts[0]!.body).toMatchObject({ action: "reboot" });
    expect(posts[0]!.body["acknowledgeImpact"]).toBe(ack);
  });
});
