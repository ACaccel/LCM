import type { InfiniteData, QueryClient, QueryKey } from "@tanstack/react-query";
import { insertLive, matchesFilter } from "../lib/timeline";
import { qk } from "./queries";
import type {
  Action,
  EngineEvent,
  EventEnvelope,
  EventType,
  NodeStatus,
  Run,
  RunStatus,
  ServiceSummary,
  Signal,
  TimelineEntry,
  TimelineList,
} from "./types";

/** Delay that merges bursts of invalidations into one refetch per key. */
const FLUSH_MS = 250;

export interface EventApplier {
  /** Applies one event to the caches. */
  apply(env: EventEnvelope): void;
  /** Refetches everything; used after an overflow or a reconnect. */
  resync(): void;
  /** Runs pending invalidations now (tests, teardown). */
  flush(): void;
  dispose(): void;
}

/**
 * Turns stream events into cache updates. Events that carry enough data
 * patch the cached values in place, so the UI changes without a request;
 * the rest invalidate the affected queries, coalesced so that a burst of
 * events costs one refetch per query.
 */
export function createEventApplier(qc: QueryClient): EventApplier {
  const pending = new Map<string, QueryKey>();
  let timer: ReturnType<typeof setTimeout> | undefined;

  function flush() {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
    const keys = [...pending.values()];
    pending.clear();
    for (const queryKey of keys) void qc.invalidateQueries({ queryKey });
  }

  function invalidate(key: QueryKey) {
    pending.set(JSON.stringify(key), key);
    timer ??= setTimeout(flush, FLUSH_MS);
  }

  function patchServices(name: string, patch: Partial<Pick<ServiceSummary, "health" | "state" | "stateSince">>) {
    qc.setQueriesData<ServiceSummary[]>({ queryKey: qk.servicesAll() }, (list) =>
      Array.isArray(list) ? list.map((x) => (x.name === name ? { ...x, ...patch } : x)) : list,
    );
    qc.setQueryData<Record<string, unknown>>(qk.service(name), (d) => (d ? { ...d, ...patch } : d));
  }

  function onSignal(env: EventEnvelope, sig: Signal) {
    const name = sig.service ?? env.service;
    if (!name) {
      if (sig.kind === "alert") invalidate(["alerts"]);
      return;
    }
    const data = sig.data ?? {};
    switch (sig.kind) {
      case "health.changed":
        if (typeof data["health"] === "string") patchServices(name, { health: data["health"] as ServiceSummary["health"] });
        else invalidate(qk.service(name));
        invalidate(qk.serviceChecks(name));
        break;
      case "lifecycle.changed":
        if (typeof data["state"] === "string") patchServices(name, { state: data["state"] as ServiceSummary["state"], stateSince: sig.time });
        else invalidate(qk.service(name));
        break;
      case "check.changed":
        invalidate(qk.serviceChecks(name));
        invalidate(qk.service(name));
        break;
      case "alert":
        invalidate(["alerts"]);
        invalidate(qk.service(name));
        break;
    }
  }

  function onTimeline(entry: TimelineEntry) {
    for (const [key] of qc.getQueriesData<InfiniteData<TimelineList>>({ queryKey: qk.timelineAll() })) {
      const service = String(key[1] ?? "");
      const kinds = (key[2] ?? []) as string[];
      if (!matchesFilter(entry, service, kinds)) continue;
      qc.setQueryData<InfiniteData<TimelineList>>(key, (d) => (d ? { ...d, pages: insertLive(d.pages, entry) } : d));
    }
    if (entry.kind === "deployment") {
      invalidate(["deployments-since"]);
      invalidate(qk.serviceDeployments(entry.service));
      invalidate(qk.service(entry.service));
    }
  }

  function onRun(ev: EngineEvent) {
    if (ev.kind === "policy.deployed" || ev.kind === "policy.undeployed") {
      invalidate(qk.policies());
      invalidate(qk.policy(ev.policy));
      return;
    }
    const id = ev.runId;
    if (!id) return;
    const status = ev.status as RunStatus | undefined;
    if (status) {
      qc.setQueryData<Run>(qk.run(id), (r) => (r ? { ...r, status } : r));
      qc.setQueriesData<InfiniteData<{ items: { id: string; status: RunStatus }[]; nextCursor: string | null }>>(
        { queryKey: qk.runsAll() },
        (d) => (d ? { ...d, pages: d.pages.map((p) => ({ ...p, items: p.items.map((x) => (x.id === id ? { ...x, status } : x)) })) } : d),
      );
    }
    if (ev.kind === "run.queued") invalidate(qk.runsAll());
    if (ev.kind === "run.finished") {
      invalidate(qk.run(id));
      invalidate(qk.policies());
      invalidate(qk.policy(ev.policy));
    }
  }

  function onNode(ev: EngineEvent) {
    const id = ev.runId;
    if (!id || !ev.nodeId) return;
    const nodeId = ev.nodeId;
    const status = ev.status as NodeStatus | undefined;
    qc.setQueryData<Run>(qk.run(id), (r) => {
      if (!r) return r;
      return {
        ...r,
        nodes: r.nodes.map((n) =>
          n.id === nodeId
            ? {
                ...n,
                ...(status ? { status } : {}),
                ...(ev.port ? { port: ev.port } : {}),
                ...(ev.attempt ? { attempts: ev.attempt } : {}),
                ...(ev.error ? { error: ev.error } : {}),
              }
            : n,
        ),
      };
    });
    // Output, resolved props and timings are not in the event.
    if (ev.kind === "node.finished" || ev.kind === "node.dead" || ev.kind === "node.suspended") invalidate(qk.run(id));
  }

  function onAction(a: Action) {
    qc.setQueryData<Action>(qk.action(a.id), a);
    if (a.status !== "running") {
      invalidate(qk.service(a.service));
      invalidate(qk.serviceChecks(a.service));
    }
  }

  return {
    apply(env) {
      const type: EventType = env.type;
      switch (type) {
        case "signal":
          onSignal(env, env.data as Signal);
          break;
        case "timeline":
          onTimeline(env.data as TimelineEntry);
          break;
        case "run":
          onRun(env.data as EngineEvent);
          break;
        case "node":
          onNode(env.data as EngineEvent);
          break;
        case "action":
          onAction(env.data as Action);
          break;
        case "catalog": {
          const d = env.data as { service?: string; kind?: string; policy?: string };
          if (d.kind) {
            onRun(env.data as EngineEvent);
          } else {
            invalidate(qk.servicesAll());
            if (d.service) invalidate(qk.service(d.service));
            invalidate(qk.ownership());
          }
          break;
        }
      }
    },
    resync() {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      pending.clear();
      void qc.invalidateQueries();
    },
    flush,
    dispose() {
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      pending.clear();
    },
  };
}

export type StreamState = "connecting" | "open" | "retrying";

export interface SourceLike {
  addEventListener(type: string, listener: (e: MessageEvent<string>) => void): void;
  onopen: ((e: Event) => void) | null;
  onerror: ((e: Event) => void) | null;
  close(): void;
}

const NAMED_EVENTS: EventType[] = ["signal", "run", "node", "timeline", "action", "catalog"];

export interface StreamOptions {
  url?: string;
  createSource?: (url: string) => SourceLike;
  /** Delay before reconnect attempt n (0-based). */
  backoff?: (attempt: number) => number;
}

/** Reconnect delay: 1s doubling to 30s, with up to 20% jitter. */
export function defaultBackoff(attempt: number): number {
  const base = Math.min(30_000, 1000 * 2 ** attempt);
  return base * (0.8 + Math.random() * 0.4);
}

/**
 * One server-sent-event connection for the tab. EventSource would reconnect
 * by itself but with a fixed delay and no resync, and the server does not
 * replay missed events, so reconnecting is done here: after the stream is
 * back, all caches are refetched.
 */
export class EventStream {
  private source: SourceLike | undefined;
  private timer: ReturnType<typeof setTimeout> | undefined;
  private attempt = 0;
  private everOpened = false;
  private stopped = true;
  private current: StreamState = "connecting";
  private readonly listeners = new Set<() => void>();
  private readonly applier: EventApplier;
  private readonly opts: Required<StreamOptions>;

  constructor(qc: QueryClient, opts: StreamOptions = {}) {
    this.applier = createEventApplier(qc);
    this.opts = {
      url: opts.url ?? "/api/v1/events",
      createSource: opts.createSource ?? ((u) => new EventSource(u) as unknown as SourceLike),
      backoff: opts.backoff ?? defaultBackoff,
    };
  }

  get state(): StreamState {
    return this.current;
  }

  subscribe = (fn: () => void): (() => void) => {
    this.listeners.add(fn);
    return () => this.listeners.delete(fn);
  };

  getSnapshot = (): StreamState => this.current;

  start(): void {
    if (!this.stopped) return;
    this.stopped = false;
    this.connect();
  }

  stop(): void {
    this.stopped = true;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.timer = undefined;
    this.source?.close();
    this.source = undefined;
    this.applier.dispose();
  }

  private set(state: StreamState) {
    if (this.current === state) return;
    this.current = state;
    for (const l of this.listeners) l();
  }

  private connect() {
    if (this.stopped) return;
    this.set(this.everOpened ? "retrying" : "connecting");
    const src = this.opts.createSource(this.opts.url);
    this.source = src;
    src.onopen = () => {
      this.attempt = 0;
      this.set("open");
      // Events published while disconnected are lost; catch up.
      if (this.everOpened) this.applier.resync();
      this.everOpened = true;
    };
    src.onerror = () => this.reconnect();
    for (const type of NAMED_EVENTS) {
      src.addEventListener(type, (e) => this.onMessage(e.data));
    }
    src.addEventListener("overflow", () => {
      this.applier.resync();
      this.reconnect(0);
    });
  }

  private onMessage(raw: string) {
    try {
      this.applier.apply(JSON.parse(raw) as EventEnvelope);
    } catch (e) {
      // A malformed frame must not take the stream down.
      console.error("event stream: unreadable event", e);
    }
  }

  private reconnect(delay?: number) {
    if (this.stopped) return;
    this.source?.close();
    this.source = undefined;
    if (this.timer !== undefined) clearTimeout(this.timer);
    this.set("retrying");
    const wait = delay ?? this.opts.backoff(this.attempt++);
    this.timer = setTimeout(() => {
      this.timer = undefined;
      this.connect();
    }, wait);
  }
}
