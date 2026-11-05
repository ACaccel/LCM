import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter, useSearchParams } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { hostStatusQuery } from "../../api/security";
import type { HostStatus, Incident, SecuritySettings, TrafficPoint } from "../../api/security";
import { SecurityIncidents, HandlingForm } from "./incidents";
import { ProfileForm, ScheduleForm, SecurityChecks } from "./controls";
import { HostActions } from "./host-actions";
import { NotificationTest, SettingsForm, SettingsHistory } from "./notifications";
import { trafficSegments, trafficTotals, TrafficDisplay } from "./traffic";
import { resourceFixture } from "../resource/test-fixtures";
import { sec } from "./strings";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
const incident: Incident = { seq: 1, id: "incident-1", machineId: "machine-1", service: "", source: "aide", fingerprint: "aide-change", state: "firing", severity: "warning", summary: "Integrity differs", openedAt: "2026-11-02T01:00:00Z", observedAt: "2026-11-02T01:01:00Z", updatedAt: "2026-11-02T01:01:00Z", occurrences: 2, revision: 2, delivery: "sent", lastError: "", handling: { version: 0, acknowledged: false, note: "", silenceUntil: null, updatedAt: null, updatedBy: "" } };
const settings: SecuritySettings = { version: 3, notificationsEnabled: true, cooldownSeconds: 900, minimumSeverity: "info", rootEnabled: true, notificationConfigured: true, maxSilenceSeconds: 86400 };
const host: HostStatus = { configured: true, available: true, reason: "", machineId: "machine-1", effectiveDigest: "digest-1", capabilities: [{ action: "aide.schedule", enabled: true, reason: "" }, { action: "audit.profile", enabled: true, reason: "" }, { action: "aide.check", enabled: true, reason: "" }], aide: { targets: null, result: "clean", lastRunAt: null, nextRunAt: null, baselineCreatedAt: null, baselineDigest: null, schedule: { hour: 4, minute: 20 }, profiles: [], reportSummary: null }, audit: { enabled: 1, lost: 0, backlog: 0, rulesLoaded: true, profiles: [{ name: "core", enabled: true, required: true }, { name: "optional", enabled: true, required: false }] }, nginx: { mode: null, dryRunOnly: true, r11Approved: false, limits: [] }, collector: { observedAt: null, lastDelivery: null, queued: null, dropped: null, deliveryError: null, health: {} } };
function wrap(node: ReactNode, initial = "/security/incidents") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return <QueryClientProvider client={qc}><MemoryRouter initialEntries={[initial]}>{node}</MemoryRouter></QueryClientProvider>;
}
afterEach(() => { cleanup(); localStorage.clear(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe("security incidents", () => {
  const machines = { items: [{ ...resourceFixture({ id: "machine-1", name: "nas", type: "machine" }) }, { ...resourceFixture({ id: "machine-2", name: "gateway-host", type: "machine" }) }], nextCursor: null };
  /** Serves the machine list and hands incident reads to `incidents`, recording their URLs. */
  function stub(incidents: (url: URL) => Response | Promise<Response>) {
    const urls: URL[] = [];
    vi.stubGlobal("fetch", async (request: Request) => {
      const url = new URL(request.url);
      if (url.pathname === "/api/v1/resources") return json(machines);
      urls.push(url);
      return incidents(url);
    });
    return urls;
  }
  it("renders loading then a recoverable read error without claiming empty", async () => {
    let finish: ((response: Response) => void) | undefined;
    stub(() => new Promise<Response>((resolve) => { finish = resolve; }));
    render(wrap(<SecurityIncidents machineId="machine-1" />));
    expect(screen.getByRole("status")).toBeTruthy();
    await waitFor(() => expect(finish).toBeDefined());
    finish?.(json({ error: { message: "Read failed" } }, 503));
    expect(await screen.findByRole("alert")).toHaveProperty("textContent", expect.stringContaining("Read failed"));
    expect(screen.queryByText(sec.emptyIncidents)).toBeNull();
  });
  it("retains machine/source/severity/time filters on the next cursor page", async () => {
    const urls = stub((url) => json({ items: url.searchParams.has("cursor") ? [{ ...incident, id: "incident-2", summary: "Next result" }] : [incident], nextCursor: url.searchParams.has("cursor") ? null : "cursor-1" }));
    render(wrap(<SecurityIncidents machineId="machine-1" />, "/security/incidents?source=aide&severity=warning&state=firing&from=2026-11-01T00%3A00%3A00Z"));
    await userEvent.click(await screen.findByRole("button", { name: sec.more }));
    expect(await screen.findByText("Next result")).toBeTruthy();
    expect(urls.at(-1)?.searchParams.get("cursor")).toBe("cursor-1");
    for (const url of urls) expect(Object.fromEntries(url.searchParams)).toMatchObject({ machineId: "machine-1", source: "aide", severity: "warning", state: "firing", from: "2026-11-01T00:00:00Z" });
  });
  it("picks the machine by name, filters by its ID and applies every filter at once", async () => {
    const urls = stub(() => json({ items: [incident], nextCursor: null }));
    function Page() { const [params] = useSearchParams(); return <SecurityIncidents machineId={params.get("machineId") || ""} />; }
    render(wrap(<Page />));
    const machine = await screen.findByLabelText(sec.machine);
    await screen.findByRole("option", { name: "gateway-host" });
    // The incident row names its machine instead of showing the raw ID.
    expect(await screen.findByTitle("machine-1")).toHaveProperty("textContent", "nas");
    await userEvent.selectOptions(machine, "gateway-host");
    await waitFor(() => expect(urls.at(-1)?.searchParams.get("machineId")).toBe("machine-2"));
    await userEvent.selectOptions(screen.getByLabelText(sec.severity), "critical");
    await waitFor(() => expect(Object.fromEntries(urls.at(-1)!.searchParams)).toMatchObject({ machineId: "machine-2", severity: "critical" }));
  });
  it("rejects reversed time filters before another read", async () => {
    const urls = stub(() => json({ items: [], nextCursor: null }));
    render(wrap(<SecurityIncidents machineId="" />));
    await screen.findByText(sec.emptyIncidents);
    fireEvent.change(screen.getByLabelText(sec.from), { target: { value: "2026-11-03T10:00" } });
    await waitFor(() => expect(urls).toHaveLength(2));
    expect(urls[1]?.searchParams.has("from")).toBe(true);
    fireEvent.change(screen.getByLabelText(sec.to), { target: { value: "2026-11-02T10:00" } });
    expect(await screen.findByText(sec.filterInvalid)).toBeTruthy();
    expect(urls).toHaveLength(2);
  });
  it("requires a reason and acknowledges with quoted version without resolving detection", async () => {
    const seen: { body: unknown; match: string | null }[] = [];
    vi.stubGlobal("fetch", async (request: Request) => { seen.push({ body: await request.json(), match: request.headers.get("If-Match") }); return json({ ...incident.handling, version: 1, acknowledged: true }); });
    render(wrap(<HandlingForm incident={incident} />));
    const button = screen.getByRole("button", { name: sec.saveHandling }); expect((button as HTMLButtonElement).disabled).toBe(true);
    await userEvent.type(screen.getByLabelText(sec.reason), "Reviewed legitimate update"); await userEvent.click(button);
    await waitFor(() => expect(seen).toHaveLength(1));
    expect(seen[0]).toEqual({ match: '"0"', body: { action: "acknowledge", reason: "Reviewed legitimate update" } });
  });
  it("does not silently replace the reviewed handling version after another update", async () => {
    const view = render(wrap(<HandlingForm incident={incident} />));
    await userEvent.type(screen.getByLabelText(sec.reason), "Review in progress");
    view.rerender(wrap(<HandlingForm incident={{ ...incident, handling: { ...incident.handling, version: 1 } }} />));
    expect(screen.getByText(sec.conflict)).toBeTruthy(); expect((screen.getByRole("button", { name: sec.saveHandling }) as HTMLButtonElement).disabled).toBe(true);
  });
});

describe("traffic observations", () => {
  const point = (start: string, requests: number, sum: number): TrafficPoint => ({ windowStart: start, windowEnd: new Date(Date.parse(start) + 60_000).toISOString(), requests, requestTimeSumMs: sum, requestTimeMaxMs: 40, status429: 0, status5xx: 0, dryRunHits: 0, requestBytes: 100, responseBytes: 200, averageMs: requests ? sum / requests : 0, requestsPerSecond: requests / 60, observedWindows: 1, observedSeconds: 60 });
  it("weights average latency by requests and breaks missing buckets", () => {
    const points = [point("2026-11-02T00:00:00Z", 1, 10), point("2026-11-02T00:01:00Z", 9, 360), point("2026-11-02T00:03:00Z", 0, 0)];
    expect(trafficTotals(points).averageMs).toBe(37); expect(trafficSegments(points, 60).map((segment) => segment.length)).toEqual([2, 1]);
    expect(trafficTotals([]).averageMs).toBeNull();
  });
  it("shows no observation as unknown instead of successful zero", () => {
    render(wrap(<TrafficDisplay data={{ items: [], from: "2026-11-02T00:00:00Z", to: "2026-11-02T01:00:00Z", bucketSeconds: 60 }} />));
    expect(screen.getByText(sec.noTraffic)).toBeTruthy(); expect(screen.queryByText(sec.requests)).toBeNull();
  });
});

describe("notification operations", () => {
  it("shows persisted notification version history and actor reasons", async () => {
    vi.stubGlobal("fetch", async () => json({ items: [{ version: 3, actor: "operator", at: "2026-11-02T01:00:00Z", reason: "Reviewed notification scope", notificationsEnabled: true, cooldownSeconds: 900, minimumSeverity: "warning" }] }));
    render(wrap(<SettingsHistory />));
    expect(await screen.findByText("Reviewed notification scope")).toBeTruthy(); expect(screen.getByText("operator")).toBeTruthy();
  });
  it("uses the reviewed settings version and a structured body", async () => {
    let body: unknown; let match: string | null = null;
    vi.stubGlobal("fetch", async (request: Request) => { body = await request.json(); match = request.headers.get("If-Match"); return json({ ...settings, version: 4 }); });
    render(wrap(<SettingsForm settings={settings} />));
    await userEvent.type(screen.getByLabelText(sec.reason), "Reduce repeated warnings"); await userEvent.click(screen.getByRole("button", { name: sec.saveSettings }));
    await waitFor(() => expect(body).toMatchObject({ reason: "Reduce repeated warnings", cooldownSeconds: 900 })); expect(match).toBe('"3"');
  });
  it("respects root disable and unconfigured destination", () => {
    render(wrap(<SettingsForm settings={{ ...settings, rootEnabled: false, notificationConfigured: false }} />));
    expect(screen.getByText(sec.rootDisabled)).toBeTruthy(); expect(screen.getByText(sec.notConfigured)).toBeTruthy(); expect((screen.getByRole("button", { name: sec.saveSettings }) as HTMLButtonElement).disabled).toBe(true);
  });
  it("persists test identity before a lost POST response and queries it after remount", async () => {
    const posts: string[] = []; const reads: string[] = [];
    vi.stubGlobal("fetch", async (request: Request) => { if (request.method === "POST") { const body = await request.json() as { requestId: string }; posts.push(body.requestId); throw new Error("Lost response"); } reads.push(new URL(request.url).pathname); return json({ requestId: posts[0], incidentId: "test-1", state: "resolved", pending: false }); });
    const view = render(wrap(<NotificationTest settings={settings} />));
    await userEvent.type(screen.getByLabelText(sec.reason), "Acceptance check"); await userEvent.click(screen.getByRole("checkbox")); await userEvent.click(screen.getByRole("button", { name: sec.test }));
    await waitFor(() => expect(posts).toHaveLength(1)); view.unmount();
    render(wrap(<NotificationTest settings={settings} />));
    expect(await screen.findByText(new RegExp(sec.testState))).toBeTruthy(); expect(reads.some((path) => path.endsWith(posts[0]!))).toBe(true); expect(posts).toHaveLength(1);
  });
  it("does not send a notification when attempt persistence fails", async () => {
    const fetch = vi.fn(); vi.stubGlobal("fetch", fetch); render(wrap(<NotificationTest settings={settings} />));
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => { throw new Error("Blocked"); });
    await userEvent.type(screen.getByLabelText(sec.reason), "Acceptance check"); await userEvent.click(screen.getByRole("checkbox")); await userEvent.click(screen.getByRole("button", { name: sec.test }));
    expect(screen.getByText(sec.storageError)).toBeTruthy(); expect(fetch).not.toHaveBeenCalled();
  });
});

describe("host controls", () => {
  it("shows setup guidance for an unconfigured host and stops polling it", async () => {
    const unconfigured: HostStatus = { ...host, configured: false, available: false, machineId: "", reason: "not configured" };
    const fetch = vi.fn(async () => json(unconfigured)); vi.stubGlobal("fetch", fetch);
    render(wrap(<SecurityChecks />));
    expect(await screen.findByText(sec.hostNotConfigured)).toBeTruthy();
    expect(screen.getByRole("link", { name: sec.hostSetupLink }).getAttribute("href")).toContain("docs/operations.md");
    expect(screen.queryByRole("button", { name: sec.scheduleAction })).toBeNull();
    const interval = hostStatusQuery().refetchInterval as (query: { state: { data?: HostStatus } }) => number | false;
    expect(interval({ state: { data: unconfigured } })).toBe(false);
    expect(interval({ state: { data: host } })).toBeGreaterThan(0);
    expect(interval({ state: {} })).toBeGreaterThan(0);
  });
  it("shows helper absence as read-only and never sends a scan", () => {
    const begin = vi.fn(); render(wrap(<ScheduleForm host={{ ...host, available: false, reason: "Helper not installed" }} begin={begin} />));
    expect(screen.getByText(/Helper not installed/)).toBeTruthy(); expect((screen.getByRole("button", { name: sec.scheduleAction }) as HTMLButtonElement).disabled).toBe(true); expect(begin).not.toHaveBeenCalled();
  });
  it("keeps required profiles enabled and includes only typed profile changes", async () => {
    const begin = vi.fn(); render(wrap(<ProfileForm host={host} type="audit" begin={begin} />));
    expect((screen.getByRole("checkbox", { name: /core/ }) as HTMLInputElement).disabled).toBe(true);
    await userEvent.click(screen.getByRole("checkbox", { name: /optional/ })); await userEvent.type(screen.getByLabelText(sec.reason), "Reduce optional coverage"); await userEvent.click(screen.getByRole("button", { name: sec.preview }));
    expect(begin).toHaveBeenCalledWith("audit.profile", { profiles: [{ name: "core", enabled: true }, { name: "optional", enabled: false }] }, "Reduce optional coverage");
  });
  it("requires preview acknowledgement then persists and reuses the operation identity", async () => {
    const applied: unknown[] = []; let previewId = "";
    vi.stubGlobal("fetch", async (request: Request) => {
      const path = new URL(request.url).pathname;
      if (path.endsWith("/preview")) { const body = await request.json() as { requestId: string }; previewId = body.requestId; return json({ requestId: previewId, action: "aide.check", previewHash: "preview-1", expiresAt: new Date(Date.now() + 300000).toISOString(), changes: { scan: true }, requiresLocalApproval: false }); }
      if (path.endsWith("/apply")) { applied.push(await request.json()); return json({ id: previewId, action: "aide.check", state: "running" }); }
      if (path.endsWith("/operations")) return json({ items: [], nextCursor: null });
      return json({ id: previewId, action: "aide.check", state: "running", reason: "Manual inspection" });
    });
    render(wrap(<HostActions host={host}>{(begin, busy) => <button disabled={busy} onClick={() => begin("aide.check", {}, "Manual inspection")}>Start preview</button>}</HostActions>));
    await userEvent.click(screen.getByRole("button", { name: "Start preview" }));
    const button = await screen.findByRole("button", { name: sec.applyHost }); expect((button as HTMLButtonElement).disabled).toBe(true);
    await userEvent.click(screen.getByRole("checkbox")); await userEvent.click(button);
    await waitFor(() => expect(applied).toHaveLength(1)); expect(applied[0]).toMatchObject({ requestId: previewId, previewHash: "preview-1", reason: "Manual inspection" });
    expect(JSON.parse(localStorage.getItem("lcm:security:host-attempt:machine-1")!)).toMatchObject({ requestId: previewId });
  });
});
