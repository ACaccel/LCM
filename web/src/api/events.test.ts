import { QueryClient } from "@tanstack/react-query";
import type { InfiniteData } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createEventApplier, EventStream } from "./events";
import type { SourceLike } from "./events";
import { qk } from "./queries";
import type { Action, EventEnvelope, Run, ServiceSummary, TimelineEntry, TimelineList } from "./types";

function env(type: EventEnvelope["type"], data: unknown, extra: Partial<EventEnvelope> = {}): EventEnvelope {
  return { id: 1, type, time: "2026-10-28T10:00:00Z", data, ...extra } as EventEnvelope;
}

const svc = (name: string, over: Partial<ServiceSummary> = {}): ServiceSummary => ({
  name,
  host: "local",
  labels: {},
  state: "running",
  stateSince: "2026-10-28T09:00:00Z",
  health: "healthy",
  version: 1,
  updatedAt: "2026-10-28T09:00:00Z",
  deployedVersion: "",
  managedBy: null,
  ...over,
});

const entry = (over: Partial<TimelineEntry> = {}): TimelineEntry => ({
  id: 0,
  service: "web",
  time: "2026-10-28T10:00:00Z",
  kind: "deployment",
  summary: "deployed",
  data: {},
  ref: "7",
  ...over,
});

let qc: QueryClient;

beforeEach(() => {
  vi.useFakeTimers();
  qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("health and lifecycle signals", () => {
  it("patches the services list and detail without a request", () => {
    qc.setQueryData(qk.services({}), [svc("web"), svc("api")]);
    qc.setQueryData(qk.service("web"), { ...svc("web"), checks: [] });
    const a = createEventApplier(qc);

    a.apply(env("signal", { id: "s1", kind: "health.changed", time: "2026-10-28T10:00:00Z", service: "web", source: "monitor", data: { health: "down" } }));

    const list = qc.getQueryData<ServiceSummary[]>(qk.services({}));
    expect(list?.map((x) => x.health)).toEqual(["down", "healthy"]);
    expect(qc.getQueryData<ServiceSummary>(qk.service("web"))?.health).toBe("down");
  });

  it("updates the state and its start time on lifecycle changes", () => {
    qc.setQueryData(qk.services({}), [svc("web")]);
    const a = createEventApplier(qc);
    a.apply(env("signal", { id: "s2", kind: "lifecycle.changed", time: "2026-10-28T10:05:00Z", service: "web", source: "catalog", data: { state: "updating", previous: "running" } }));
    const [web] = qc.getQueryData<ServiceSummary[]>(qk.services({})) ?? [];
    expect(web?.state).toBe("updating");
    expect(web?.stateSince).toBe("2026-10-28T10:05:00Z");
  });
});

describe("timeline events", () => {
  const page = (items: TimelineEntry[]): InfiniteData<TimelineList> => ({
    pages: [{ items, nextCursor: null }],
    pageParams: [undefined],
  });

  it("prepends to every matching timeline and skips non-matching filters", () => {
    qc.setQueryData(qk.timeline({}), page([]));
    qc.setQueryData(qk.timeline({ service: "web" }), page([]));
    qc.setQueryData(qk.timeline({ service: "api" }), page([]));
    qc.setQueryData(qk.timeline({ kinds: ["alert"] }), page([]));
    const a = createEventApplier(qc);

    a.apply(env("timeline", entry()));

    const count = (key: readonly unknown[]) => qc.getQueryData<InfiniteData<TimelineList>>(key)?.pages[0]?.items.length;
    expect(count(qk.timeline({}))).toBe(1);
    expect(count(qk.timeline({ service: "web" }))).toBe(1);
    expect(count(qk.timeline({ service: "api" }))).toBe(0);
    expect(count(qk.timeline({ kinds: ["alert"] }))).toBe(0);
  });

  it("ignores a repeated entry", () => {
    qc.setQueryData(qk.timeline({}), page([entry({ id: 5 })]));
    createEventApplier(qc).apply(env("timeline", entry({ id: 0 })));
    expect(qc.getQueryData<InfiniteData<TimelineList>>(qk.timeline({}))?.pages[0]?.items).toHaveLength(1);
  });

  it("coalesces deployment invalidations into one refetch", async () => {
    const spy = vi.spyOn(qc, "invalidateQueries");
    const a = createEventApplier(qc);
    a.apply(env("timeline", entry({ ref: "1" })));
    a.apply(env("timeline", entry({ ref: "2", summary: "again" })));
    expect(spy).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(300);
    const keys = spy.mock.calls.map((c) => JSON.stringify(c[0]?.queryKey));
    expect(keys.filter((k) => k === JSON.stringify(["deployments-since"]))).toHaveLength(1);
  });
});

describe("run and node events", () => {
  const run = (): Run => ({
    id: "r1",
    policy: "p",
    version: 1,
    status: "running",
    mode: "live",
    triggerNode: "start",
    createdAt: "2026-10-28T10:00:00Z",
    event: {},
    nodes: [
      { id: "start", type: "trigger.manual", status: "completed" },
      { id: "a", type: "data.set", status: "pending" },
    ],
  });

  it("colours a node from its event", () => {
    qc.setQueryData(qk.run("r1"), run());
    const a = createEventApplier(qc);
    a.apply(env("node", { kind: "node.started", time: "x", policy: "p", runId: "r1", nodeId: "a", status: "running", attempt: 1 }));
    const r = qc.getQueryData<Run>(qk.run("r1"));
    expect(r?.nodes.find((n) => n.id === "a")).toMatchObject({ status: "running", attempts: 1 });
    expect(r?.nodes.find((n) => n.id === "start")?.status).toBe("completed");
  });

  it("keeps module-scoped node ids intact", () => {
    const r = run();
    r.nodes.push({ id: "outer/inner/check", type: "http.get", status: "pending" });
    qc.setQueryData(qk.run("r1"), r);
    createEventApplier(qc).apply(
      env("node", { kind: "node.finished", time: "x", policy: "p", runId: "r1", nodeId: "outer/inner/check", status: "failed", error: "boom" }),
    );
    expect(qc.getQueryData<Run>(qk.run("r1"))?.nodes[2]).toMatchObject({ status: "failed", error: "boom" });
  });

  it("updates the run status in the cached run and run list", () => {
    qc.setQueryData(qk.run("r1"), run());
    qc.setQueryData(qk.runs({}), {
      pages: [{ items: [{ id: "r1", status: "running" }], nextCursor: null }],
      pageParams: [undefined],
    });
    createEventApplier(qc).apply(env("run", { kind: "run.finished", time: "x", policy: "p", runId: "r1", status: "succeeded" }));
    expect(qc.getQueryData<Run>(qk.run("r1"))?.status).toBe("succeeded");
    const list = qc.getQueryData<InfiniteData<{ items: { status: string }[] }>>(qk.runs({}));
    expect(list?.pages[0]?.items[0]?.status).toBe("succeeded");
  });
});

describe("action events", () => {
  it("replaces the cached action", () => {
    const running: Action = { id: "a1", service: "web", action: "deploy", status: "running", actor: { kind: "user", id: "1", name: "admin" }, startedAt: "x", finishedAt: null, result: null, error: "" };
    qc.setQueryData(qk.action("a1"), running);
    createEventApplier(qc).apply(env("action", { ...running, status: "succeeded", finishedAt: "y" }));
    expect(qc.getQueryData<Action>(qk.action("a1"))?.status).toBe("succeeded");
  });
});

describe("EventStream", () => {
  class FakeSource implements SourceLike {
    static all: FakeSource[] = [];
    onopen: ((e: Event) => void) | null = null;
    onerror: ((e: Event) => void) | null = null;
    closed = false;
    private handlers = new Map<string, ((e: MessageEvent<string>) => void)[]>();
    constructor(readonly url: string) {
      FakeSource.all.push(this);
    }
    addEventListener(type: string, fn: (e: MessageEvent<string>) => void) {
      this.handlers.set(type, [...(this.handlers.get(type) ?? []), fn]);
    }
    emit(type: string, data: unknown) {
      for (const fn of this.handlers.get(type) ?? []) fn({ data: JSON.stringify(data) } as MessageEvent<string>);
    }
    close() {
      this.closed = true;
    }
  }

  beforeEach(() => {
    FakeSource.all = [];
  });

  function stream() {
    return new EventStream(qc, { createSource: (u) => new FakeSource(u), backoff: (n) => 1000 * (n + 1) });
  }

  it("reconnects with backoff and refetches everything once it is back", async () => {
    const spy = vi.spyOn(qc, "invalidateQueries");
    const s = stream();
    s.start();
    expect(FakeSource.all).toHaveLength(1);
    FakeSource.all[0]?.onopen?.(new Event("open"));
    expect(s.state).toBe("open");
    expect(spy).not.toHaveBeenCalled();

    FakeSource.all[0]?.onerror?.(new Event("error"));
    expect(s.state).toBe("retrying");
    expect(FakeSource.all[0]?.closed).toBe(true);
    await vi.advanceTimersByTimeAsync(999);
    expect(FakeSource.all).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(2);
    expect(FakeSource.all).toHaveLength(2);

    FakeSource.all[1]?.onerror?.(new Event("error"));
    await vi.advanceTimersByTimeAsync(1500);
    expect(FakeSource.all).toHaveLength(2); // second attempt waits 2s
    await vi.advanceTimersByTimeAsync(600);
    expect(FakeSource.all).toHaveLength(3);

    FakeSource.all[2]?.onopen?.(new Event("open"));
    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy.mock.calls[0]?.[0]).toBeUndefined();
    s.stop();
  });

  it("refetches on overflow and reconnects", async () => {
    const spy = vi.spyOn(qc, "invalidateQueries");
    const s = stream();
    s.start();
    FakeSource.all[0]?.onopen?.(new Event("open"));
    FakeSource.all[0]?.emit("overflow", {});
    expect(spy).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(10);
    expect(FakeSource.all).toHaveLength(2);
    s.stop();
  });

  it("survives an unreadable frame and applies the next one", () => {
    qc.setQueryData(qk.services({}), [svc("web")]);
    const err = vi.spyOn(console, "error").mockImplementation(() => undefined);
    const s = stream();
    s.start();
    const src = FakeSource.all[0];
    src?.emit("signal", undefined);
    src?.emit("signal", env("signal", { id: "s", kind: "health.changed", time: "t", service: "web", source: "m", data: { health: "degraded" } }));
    expect(err).toHaveBeenCalledTimes(1);
    expect(qc.getQueryData<ServiceSummary[]>(qk.services({}))?.[0]?.health).toBe("degraded");
    err.mockRestore();
    s.stop();
  });

  it("stops cleanly", () => {
    const s = stream();
    s.start();
    s.stop();
    expect(FakeSource.all[0]?.closed).toBe(true);
  });
});
