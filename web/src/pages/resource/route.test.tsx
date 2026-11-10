import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ResourceDetail, ServiceRoute, ServiceRouteStatus } from "../../api/types";
import { RoutePanel, routeLines } from "./route";
import { resourceFixture } from "./test-fixtures";

const route: ServiceRoute = {
  proxy: [
    { path: "api/", upstream: "/api/", bodyLimit: "3m", cookiePath: true, category: "api" },
    { path: "socket.io/", upstream: "/socket.io/", websocket: true, timeout: "75s" },
  ],
  static: { spa: true, notFound: "404.html", immutable: ["assets/", "_astro/"], downloads: [{ path: "download/app.apk", type: "application/vnd.android.package-archive" }] },
  redirects: [{ from: "en/", to: "/", code: 301 }],
  headers: { csp: "default-src 'self'", cspReportOnly: true },
};

const status = (over: Partial<ServiceRouteStatus> = {}): ServiceRouteStatus => ({
  configured: true,
  state: "applied",
  current: true,
  message: "the site is up to date",
  at: "2026-11-10T12:00:00Z",
  operationId: "op-1",
  siteSha256: "c".repeat(64),
  changes: [],
  ...over,
});

function detailWith(st: ServiceRouteStatus | null, config: ServiceRoute | undefined = route): ResourceDetail {
  const base = resourceFixture();
  return {
    ...base,
    document: { ...base.document, spec: { ...base.document.spec, config: { unit: "web.service", ...(config ? { route: config } : {}) } } },
    service: { deployedVersion: "", lastDeployment: null, route: st },
  };
}

describe("routeLines", () => {
  it("lists the locations relative to the prefix", () => {
    const groups = routeLines(route);
    expect(groups.map((g) => g.group)).toEqual(["反向代理", "靜態檔案", "轉址", "回應標頭"]);
    expect(groups[0]?.lines[0]).toBe("前綴 + api/ → /api/ · 上限 3m · cookie path · 類別 api");
    expect(groups[0]?.lines[1]).toContain("WebSocket");
    expect(groups[2]?.lines).toEqual(["前綴 + en/ → 前綴 + /（301）"]);
    expect(groups[3]?.lines[0]).toBe("CSP（僅回報）: default-src 'self'");
  });
});

describe("RoutePanel", () => {
  it("shows the last result and the locations", () => {
    render(<RoutePanel detail={detailWith(status())} />);
    expect(screen.getByRole("heading", { name: "路由" })).toBeTruthy();
    expect(screen.getByText("已套用")).toBeTruthy();
    expect(screen.getByText(/前綴 \+ socket.io\/ → \/socket.io\//)).toBeTruthy();
    expect(screen.queryByText(/重新套用路由/)).toBeNull();
  });

  it("shows a refusal with the host's message and how to retry", () => {
    render(<RoutePanel detail={detailWith(status({ state: "refused", message: "service web has no route in host.json", current: false }))} />);
    expect(screen.getByText("被主機拒絕")).toBeTruthy();
    expect(screen.getAllByText("service web has no route in host.json").length).toBeGreaterThan(0);
    expect(screen.getByText("這是先前設定的結果；目前的設定還沒有送出")).toBeTruthy();
    expect(screen.getByText(/重新套用路由/)).toBeTruthy();
  });

  it("renders nothing without a route status", () => {
    const { container } = render(<RoutePanel detail={detailWith(null, undefined)} />);
    expect(container.textContent).toBe("");
  });

  it("explains a section-less route", () => {
    render(<RoutePanel detail={detailWith(status({ state: "" , message: "", at: null, siteSha256: "" }), {})} />);
    expect(screen.getByText("尚未送出")).toBeTruthy();
    expect(screen.getByText(/沒有 route 區段/)).toBeTruthy();
  });
});
