import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { client, fetchAll, unwrap } from "./client";
import type { ActionName, Health, RunStatus, ServiceState } from "./types";

/**
 * Query keys. The event reducer (events.ts) patches caches by these keys, so
 * they are defined in one place and built only through these helpers.
 */
export const qk = {
  me: () => ["me"] as const,
  services: (f: ServiceFilter = {}) => ["services", f] as const,
  servicesAll: () => ["services"] as const,
  service: (name: string) => ["service", name] as const,
  serviceChecks: (name: string) => ["service", name, "checks"] as const,
  serviceSeries: (name: string, check: string, windowKey: string) => ["service", name, "series", check, windowKey] as const,
  serviceSummary: (name: string, window: string) => ["service", name, "summary", window] as const,
  serviceDeployments: (name: string) => ["service", name, "deployments"] as const,
  serviceVersions: (name: string) => ["service", name, "versions"] as const,
  serviceVersion: (name: string, v: number) => ["service", name, "version", v] as const,
  timeline: (f: TimelineFilter = {}) => ["timeline", f.service ?? "", f.kinds ?? []] as const,
  timelineAll: () => ["timeline"] as const,
  deploymentsSince: (since: string) => ["deployments-since", since] as const,
  policies: () => ["policies"] as const,
  policy: (name: string) => ["policy", name] as const,
  policyVersions: (name: string) => ["policy", name, "versions"] as const,
  policyVersion: (name: string, v: number) => ["policy", name, "version", v] as const,
  nodeTypes: () => ["node-types"] as const,
  modules: () => ["modules"] as const,
  runs: (f: RunFilter = {}) => ["runs", f.policy ?? "", f.status ?? []] as const,
  runsAll: () => ["runs"] as const,
  run: (id: string) => ["run", id] as const,
  alerts: (service?: string) => ["alerts", service ?? ""] as const,
  hosts: () => ["hosts"] as const,
  tokens: () => ["tokens"] as const,
  sources: () => ["sources"] as const,
  ownership: () => ["ownership"] as const,
  locks: () => ["locks"] as const,
  action: (id: string) => ["action", id] as const,
};

export interface ServiceFilter {
  label?: string[];
  state?: ServiceState;
  health?: Health;
}

export interface TimelineFilter {
  service?: string;
  kinds?: string[];
}

export interface RunFilter {
  policy?: string;
  status?: RunStatus[];
}

export const meQuery = () =>
  queryOptions({
    queryKey: qk.me(),
    queryFn: () => unwrap(client.GET("/auth/me")),
    retry: false,
    staleTime: 5 * 60_000,
  });

export const servicesQuery = (f: ServiceFilter = {}) =>
  queryOptions({
    queryKey: qk.services(f),
    queryFn: () =>
      fetchAll((cursor) =>
        unwrap(
          client.GET("/services", {
            params: { query: { limit: 500, cursor, label: f.label?.length ? f.label : undefined, state: f.state, health: f.health } },
          }),
        ),
      ),
  });

export const serviceQuery = (name: string) =>
  queryOptions({
    queryKey: qk.service(name),
    queryFn: () => unwrap(client.GET("/services/{svc}", { params: { path: { svc: name } } })),
  });

export const checksQuery = (name: string) =>
  queryOptions({
    queryKey: qk.serviceChecks(name),
    queryFn: () => unwrap(client.GET("/services/{svc}/checks", { params: { path: { svc: name } } })),
  });

export const summaryQuery = (name: string, window: "24h" | "7d" | "30d") =>
  queryOptions({
    queryKey: qk.serviceSummary(name, window),
    queryFn: () => unwrap(client.GET("/services/{svc}/summary", { params: { path: { svc: name }, query: { window } } })),
    staleTime: 60_000,
  });

export const seriesQuery = (name: string, check: string, windowMs: number, points: number) =>
  queryOptions({
    queryKey: qk.serviceSeries(name, check, `${windowMs}:${points}`),
    queryFn: () => {
      const to = new Date();
      const from = new Date(to.getTime() - windowMs);
      return unwrap(
        client.GET("/services/{svc}/checks/{check}/series", {
          params: { path: { svc: name, check }, query: { from: from.toISOString(), to: to.toISOString(), points } },
        }),
      );
    },
    staleTime: 60_000,
  });

export const deploymentsQuery = (name: string) =>
  queryOptions({
    queryKey: qk.serviceDeployments(name),
    queryFn: () => unwrap(client.GET("/services/{svc}/deployments", { params: { path: { svc: name }, query: { limit: 100 } } })),
  });

export const serviceVersionsQuery = (name: string) =>
  queryOptions({
    queryKey: qk.serviceVersions(name),
    queryFn: () => unwrap(client.GET("/services/{svc}/versions", { params: { path: { svc: name }, query: { limit: 100 } } })),
  });

export const serviceVersionQuery = (name: string, v: number) =>
  queryOptions({
    queryKey: qk.serviceVersion(name, v),
    queryFn: () => unwrap(client.GET("/services/{svc}/versions/{v}", { params: { path: { svc: name, v } } })),
    staleTime: Infinity,
  });

export const TIMELINE_PAGE = 50;

/** Timeline of all services, or of one when `service` is set. Newest first. */
export const timelineQuery = (f: TimelineFilter = {}) =>
  infiniteQueryOptions({
    queryKey: qk.timeline(f),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => {
      const query = { limit: TIMELINE_PAGE, cursor: pageParam, kinds: f.kinds?.length ? f.kinds : undefined };
      return f.service
        ? unwrap(client.GET("/services/{svc}/timeline", { params: { path: { svc: f.service }, query } }))
        : unwrap(client.GET("/timeline", { params: { query } }));
    },
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });

/** Deployments finished or started since the given instant, from the global timeline. */
export const deploymentsSinceQuery = (since: Date) =>
  queryOptions({
    queryKey: qk.deploymentsSince(since.toISOString().slice(0, 10)),
    queryFn: async () => {
      const items = await fetchAll((cursor) =>
        unwrap(client.GET("/timeline", { params: { query: { limit: 500, cursor, kinds: ["deployment"], from: since.toISOString() } } })),
      );
      return items;
    },
  });

export const policiesQuery = () =>
  queryOptions({
    queryKey: qk.policies(),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/policies", { params: { query: { limit: 500, cursor } } }))),
  });

export const policyQuery = (name: string) =>
  queryOptions({
    queryKey: qk.policy(name),
    queryFn: () => unwrap(client.GET("/policies/{p}", { params: { path: { p: name } } })),
  });

export const policyVersionsQuery = (name: string) =>
  queryOptions({
    queryKey: qk.policyVersions(name),
    queryFn: () => unwrap(client.GET("/policies/{p}/versions", { params: { path: { p: name }, query: { limit: 100 } } })),
  });

export const policyVersionQuery = (name: string, v: number) =>
  queryOptions({
    queryKey: qk.policyVersion(name, v),
    queryFn: () => unwrap(client.GET("/policies/{p}/versions/{v}", { params: { path: { p: name, v } } })),
    staleTime: Infinity,
  });

export const nodeTypesQuery = () =>
  queryOptions({
    queryKey: qk.nodeTypes(),
    queryFn: async () => (await unwrap(client.GET("/node-types"))).items,
    staleTime: Infinity,
  });

export const modulesQuery = () =>
  queryOptions({
    queryKey: qk.modules(),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/modules", { params: { query: { limit: 500, cursor } } }))),
  });

export const RUN_PAGE = 50;

export const runsQuery = (f: RunFilter = {}) =>
  infiniteQueryOptions({
    queryKey: qk.runs(f),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) =>
      unwrap(
        client.GET("/runs", {
          params: { query: { limit: RUN_PAGE, cursor: pageParam, policy: f.policy || undefined, status: f.status?.length ? f.status : undefined } },
        }),
      ),
    getNextPageParam: (last) => last.nextCursor ?? undefined,
  });

export const runQuery = (id: string) =>
  queryOptions({
    queryKey: qk.run(id),
    queryFn: () => unwrap(client.GET("/runs/{id}", { params: { path: { id } } })),
  });

export const alertsQuery = (service?: string) =>
  queryOptions({
    queryKey: qk.alerts(service),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/alerts", { params: { query: { limit: 500, cursor, service } } }))),
  });

export const hostsQuery = () =>
  queryOptions({ queryKey: qk.hosts(), queryFn: async () => (await unwrap(client.GET("/hosts"))).items });

export const tokensQuery = () =>
  queryOptions({ queryKey: qk.tokens(), queryFn: async () => (await unwrap(client.GET("/tokens"))).items });

export const sourcesQuery = () =>
  queryOptions({
    queryKey: qk.sources(),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/config/sources", { params: { query: { limit: 500, cursor } } }))),
  });

export const ownershipQuery = () =>
  queryOptions({
    queryKey: qk.ownership(),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/config/ownership", { params: { query: { limit: 500, cursor } } }))),
  });

export const locksQuery = () =>
  queryOptions({
    queryKey: qk.locks(),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/locks", { params: { query: { limit: 500, cursor } } }))),
  });

export const actionQuery = (id: string) =>
  queryOptions({
    queryKey: qk.action(id),
    queryFn: () => unwrap(client.GET("/actions/{id}", { params: { path: { id } } })),
  });

export type { ActionName };
