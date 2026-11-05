import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { Alert } from "../api/types";
import { s, sec } from "../strings";
import { AlertsPage } from "./alerts";
import { jsonResponse } from "./resource/test-fixtures";

const alert = (name: string, over: Partial<Alert> = {}): Alert => ({
  source: "alertmanager",
  fingerprint: name,
  resource: "web",
  name,
  severity: "warning",
  labels: { env: "prod" },
  message: `${name} message`,
  startedAt: "2026-11-01T10:00:00Z",
  updatedAt: "2026-11-01T10:05:00Z",
  ...over,
});

function setup(initial = "/alerts") {
  const urls: URL[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    if (url.pathname !== "/api/v1/alerts") return jsonResponse({ error: { code: "not_found", message: url.pathname, details: {} } }, 404);
    urls.push(url);
    const state = url.searchParams.get("state");
    const firing = [alert("HighErrorRate", { severity: "critical" }), alert("DiskFull", { resource: undefined, labels: { host: "nas" } })];
    const resolved = [alert("Latency", { resolvedAt: "2026-11-01T11:00:00Z" })];
    const items = state === "resolved" ? resolved : state === "all" ? [...firing, ...resolved] : firing;
    return jsonResponse({ items, nextCursor: null });
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[initial]}>
        <AlertsPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return urls;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("alerts page", () => {
  it("lists firing alerts with severity, source, resource and labels", async () => {
    const urls = setup();
    const row = (await screen.findByText("HighErrorRate")).closest("tr")!;
    expect(urls[0]?.searchParams.get("state")).toBe("firing");
    expect(within(row).getByText(sec.severityLabels.critical)).toBeTruthy();
    expect(within(row).getByText("alertmanager")).toBeTruthy();
    expect(within(row).getByRole("link", { name: "web" }).getAttribute("href")).toBe("/resources/web");
    expect(within(row).getByText("env=prod")).toBeTruthy();
    expect(within(row).getByText(s.alerts.stillFiring)).toBeTruthy();
    // An alert no resource claims is still listed.
    const unmapped = screen.getByText("DiskFull").closest("tr")!;
    expect(within(unmapped).getByText(s.alerts.unmapped)).toBeTruthy();
    expect(within(unmapped).getByText("host=nas")).toBeTruthy();
  });

  it("shows resolved alerts with their resolution time", async () => {
    const urls = setup("/alerts?state=resolved");
    const row = (await screen.findByText("Latency")).closest("tr")!;
    expect(urls[0]?.searchParams.get("state")).toBe("resolved");
    expect(within(row).queryByText(s.alerts.stillFiring)).toBeNull();
    expect(screen.queryByText("HighErrorRate")).toBeNull();
  });

  it("switches state through the server and searches the loaded alerts", async () => {
    const urls = setup();
    await screen.findByText("HighErrorRate");
    await userEvent.selectOptions(screen.getByLabelText(s.alerts.state), "all");
    await screen.findByText("Latency");
    expect(urls.at(-1)?.searchParams.get("state")).toBe("all");

    await userEvent.type(screen.getByLabelText(s.resources.search), "host=nas");
    await waitFor(() => expect(screen.queryByText("Latency")).toBeNull());
    expect(screen.getByText("DiskFull")).toBeTruthy();
    expect(screen.queryByText("HighErrorRate")).toBeNull();
  });

  it("says so when nothing matches", async () => {
    setup();
    await screen.findByText("HighErrorRate");
    await userEvent.type(screen.getByLabelText(s.resources.search), "no such alert");
    expect(await screen.findByText(s.alerts.empty)).toBeTruthy();
  });
});
