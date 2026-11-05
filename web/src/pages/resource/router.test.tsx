import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ResourceSnapshot, RouterSnapshot } from "../../api/types";
import { formatBytes } from "../../lib/format";
import { RouterControls, RouterOverview, RouterPowerHost, RouterSnapshotView } from "./router";
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
    // The source that failed is listed at once; the full list is behind a disclosure.
    expect(screen.getByText("1/2 可用")).toBeTruthy();
    const wanRow = screen.getAllByRole("cell", { name: "wan" })[0]!.closest("tr")!;
    expect(within(wanRow).getByText("wan status request failed")).toBeTruthy();
    const all = screen.getByText("全部來源").closest("details")!;
    expect(all.open).toBe(false);
    expect(within(all).getAllByRole("row")).toHaveLength(3);
  });

  it("names a failed attempt instead of showing measurements as current", () => {
    render(<RouterSnapshotView snapshot={{ ...snapshot, outcome: "auth-failed", reason: "login rejected", cpuPercent: null }} />);
    expect(screen.getByText("登入失敗")).toBeTruthy();
    expect(screen.getByText("login rejected")).toBeTruthy();
  });

  it("translates the reasons of the attempt and of each source", () => {
    const coded = {
      ...snapshot,
      outcome: "unreachable",
      reason: "router unreachable",
      reasonMessage: { code: "health.unreachable", params: {}, message: "router unreachable" },
      sources: { wan: { state: "unavailable", reason: "code 3", reasonMessage: { code: "router.code", params: { code: "3" }, message: "code 3" } } },
    } satisfies RouterSnapshot;
    render(<RouterSnapshotView snapshot={coded} />);
    expect(screen.getByText("路由器無法連線")).toBeTruthy();
    expect(screen.getAllByText("路由器回報錯誤碼 3").length).toBeGreaterThan(0);
    expect(screen.queryByText("router unreachable")).toBeNull();
    expect(screen.queryByText("code 3")).toBeNull();
  });

  it("formats byte sizes with binary units", () => {
    expect(formatBytes(512)).toBe("512 B");
    expect(formatBytes(1536)).toBe("1.5 KiB");
    expect(formatBytes(null)).toBe("-");
  });
});

describe("router overview", () => {
  const history: ResourceSnapshot[] = [
    { id: 2, resourceId: "router-1", type: "router", observedAt: "2026-11-01T00:01:00Z", data: { ...snapshot, id: 2, outcome: "unreachable", cpuPercent: null, memoryPercent: null } },
    { id: 1, resourceId: "router-1", type: "router", observedAt: "2026-11-01T00:00:00Z", data: { ...snapshot, id: 1 } },
  ];

  function stubHistory() {
    const reads: URL[] = [];
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      if (url.pathname.endsWith("/history")) {
        reads.push(url);
        return jsonResponse({ items: history, nextCursor: null });
      }
      return jsonResponse({ error: { message: "Unexpected fixture request" } }, 404);
    });
    return reads;
  }

  it("shows the key status and plots stored CPU and memory without a list of samples", async () => {
    stubHistory();
    const { container } = render(wrap(<RouterOverview resource={routerFixture} selector="gateway" />));
    expect(screen.getByText("推估開機時間")).toBeTruthy();
    expect(screen.getByText("RA70")).toBeTruthy();
    expect(await screen.findByRole("group", { name: /CPU 與記憶體使用率趨勢，CPU 使用率 12.5%，記憶體使用率 40%/ })).toBeTruthy();
    // The unreachable attempt has no measurements and is not plotted as zero.
    expect(container.querySelectorAll("figure circle.chart-dot")).toHaveLength(2);
    // The only disclosure is the source list; samples are read from the chart.
    expect([...container.querySelectorAll("summary")].map((x) => x.textContent)).toEqual(["全部來源"]);
  });

  it("asks for the range that is picked, thinned to fit one page", async () => {
    const reads = stubHistory();
    render(wrap(<RouterOverview resource={routerFixture} selector="gateway" />));
    await waitFor(() => expect(reads).toHaveLength(1));
    const day = reads[0]!.searchParams;
    expect(day.get("resolution")).toBe("minute");
    expect(day.get("step")).toBe("8m");
    expect(Date.now() - Date.parse(day.get("from")!)).toBeGreaterThan(86_400_000 - 60_000);
    expect(Date.now() - Date.parse(day.get("from")!)).toBeLessThan(86_400_000 + 60_000);
    await userEvent.click(screen.getByRole("button", { name: "30 天" }));
    await waitFor(() => expect(reads).toHaveLength(2));
    const month = reads[1]!.searchParams;
    expect(month.get("step")).toBe("240m");
    expect(Date.now() - Date.parse(month.get("from")!)).toBeGreaterThan(30 * 86_400_000 - 60_000);
  });
});

describe("router reboot", () => {
  it.each([
    { name: "without dependents", impact: noImpact("gateway", "reboot"), label: "確認", ack: undefined },
    {
      name: "with a control dependent",
      impact: { ...noImpact("gateway", "reboot"), controlDependents: [{ name: "nas", type: "service" as const, relation: "controlPath" as const, via: "gateway", depth: 1 }], acknowledgementRequired: true },
      label: "確認重新開機（影響 1 個資源）",
      ack: true,
    },
  ])("submits one request with a reason and the typed name ($name)", async ({ impact, label, ack }) => {
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
    render(
      wrap(
        <RouterPowerHost resource={routerFixture} selector="gateway">
          {(power) => <RouterControls resource={routerFixture} selector="gateway" power={power} />}
        </RouterPowerHost>,
      ),
    );
    const reboot = await screen.findByRole("button", { name: "重新開機" });
    await waitFor(() => expect((reboot as HTMLButtonElement).disabled).toBe(false));
    await user.click(reboot);
    const dialog = screen.getByRole("dialog");
    // Confirming is the acknowledgement: the button states the impact and there is no checkbox.
    const confirm = (await within(dialog).findByRole("button", { name: label })) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    expect(within(dialog).queryAllByRole("checkbox")).toHaveLength(0);
    // The consequences are shown once, in the dialog.
    expect(screen.getAllByText(/重開路由器會中斷網路/)).toHaveLength(1);
    await user.type(within(dialog).getByLabelText("理由"), "Clear stuck WAN");
    await user.type(within(dialog).getByLabelText(/輸入資源名稱/), "gateway ");
    expect(confirm.disabled).toBe(true);
    await user.type(within(dialog).getByLabelText(/輸入資源名稱/), "{Backspace}");
    await waitFor(() => expect(confirm.disabled).toBe(false));
    await user.click(confirm);
    await waitFor(() => expect(posts).toHaveLength(1));
    expect(posts[0]!.path).toBe("/api/v1/resources/gateway/operations");
    expect(posts[0]!.match).toBe("3");
    expect(posts[0]!.body).toMatchObject({ action: "reboot", reason: "Clear stuck WAN", confirm: "gateway" });
    expect(posts[0]!.body["acknowledgeImpact"]).toBe(ack);
  });
});
