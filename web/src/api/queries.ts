import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { client, fetchAll, unwrap } from "./client";
import type { ActionName, Health, ResourceTypeName, RunStatus } from "./types";

/**
 * Query keys. The event reducer (events.ts) patches caches by these keys, so
 * they are defined in one place and built only through these helpers.
 */
export const qk = {
  me: () => ["me"] as const,
  resourceTypes: () => ["resource-types"] as const,
  resources: (f: ResourceFilter = {}) => ["resources", f] as const,
  resourcesAll: () => ["resources"] as const,
  archives: (type?: ResourceTypeName) => ["resource-archives", type ?? ""] as const,
  archivesAll: () => ["resource-archives"] as const,
  /** Prefix of every query about one resource (its name, or `@<id>` for archives). */
  resourceAll: (ref: string) => ["resource", ref] as const,
  resource: (ref: string) => ["resource", ref, "detail"] as const,
  impact: (ref: string, action: string) => ["resource", ref, "impact", action] as const,
  resourceChecks: (ref: string) => ["resource", ref, "checks"] as const,
  resourceSeries: (ref: string, check: string, windowKey: string) => ["resource", ref, "series", check, windowKey] as const,
  resourceSummary: (ref: string, window: string) => ["resource", ref, "summary", window] as const,
  resourceDeployments: (ref: string) => ["resource", ref, "deployments"] as const,
  resourceVersions: (ref: string) => ["resource", ref, "versions"] as const,
  resourceVersion: (ref: string, v: number) => ["resource", ref, "version", v] as const,
  resourceYaml: (ref: string, version: number) => ["resource", ref, "yaml", version] as const,
  resourceHistory: (ref: string) => ["resource", ref, "history"] as const,
  resourceEvents: (ref: string) => ["resource", ref, "events"] as const,
  resourceOperations: (ref: string) => ["resource", ref, "operations"] as const,
  resourceOperation: (ref: string, id: string) => ["resource", ref, "operations", id] as const,
  resourceCapabilities: (ref: string) => ["resource", ref, "capabilities"] as const,
  resourceMaintenance: (ref: string) => ["resource", ref, "maintenance"] as const,
  timeline: (f: TimelineFilter = {}) => ["timeline", f.resource ?? "", f.kinds ?? [], f.from ?? "", f.to ?? ""] as const,
  timelineAll: () => ["timeline"] as const,
  deploymentsSince: (since: string) => ["deployments-since", since] as const,
  policies: () => ["policies"] as const,
  policy: (name: string) => ["policy", name] as const,
  policyVersions: (name: string) => ["policy", name, "versions"] as const,
  policyVersion: (name: string, v: number) => ["policy", name, "version", v] as const,
  nodeTypes: () => ["node-types"] as const,
  modules: () => ["modules"] as const,
  templates: () => ["templates"] as const,
  runs: (f: RunFilter = {}) => ["runs", f.policy ?? "", f.status ?? []] as const,
  runsAll: () => ["runs"] as const,
  run: (id: string) => ["run", id] as const,
  alerts: (resource?: string) => ["alerts", resource ?? ""] as const,
  tokens: () => ["tokens"] as const,
  sources: () => ["sources"] as const,
  ownership: () => ["ownership"] as const,
  locks: () => ["locks"] as const,
  connections: () => ["connections"] as const,
  action: (id: string) => ["action", id] as const,
};

export interface ResourceFilter {
  type?: ResourceTypeName;
  label?: string[];
  phase?: "managed" | "retired";
  health?: Health;
}

export interface TimelineFilter {
  resource?: string;
  kinds?: string[];
  /** RFC 3339 instants: entries at or after `from` and before `to`. */
  from?: string;
  to?: string;
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


export const resourceTypesQuery = () =>
  queryOptions({
    queryKey: qk.resourceTypes(),
    queryFn: async () => (await unwrap(client.GET("/resource-types"))).items,
    staleTime: Infinity,
  });

export const resourcesQuery = (f: ResourceFilter = {}) =>
  queryOptions({
    queryKey: qk.resources(f),
    queryFn: () =>
      fetchAll((cursor) =>
        unwrap(
          client.GET("/resources", {
            params: { query: { limit: 500, cursor, type: f.type, label: f.label?.length ? f.label : undefined, phase: f.phase, health: f.health } },
          }),
        ),
      ),
  });

export const archivesQuery = (type?: ResourceTypeName) =>
  queryOptions({
    queryKey: qk.archives(type),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/resource-archives", { params: { query: { limit: 500, cursor, type } } }))),
  });

export const resourceQuery = (ref: string) =>
  queryOptions({
    queryKey: qk.resource(ref),
    queryFn: () => unwrap(client.GET("/resources/{resource}", { params: { path: { resource: ref } } })),
  });

/** What acting on a resource reaches; with an action, whether that action needs acknowledgeImpact. */
export const impactQuery = (ref: string, action = "") =>
  queryOptions({
    queryKey: qk.impact(ref, action),
    queryFn: () => unwrap(client.GET("/resources/{resource}/impact", { params: { path: { resource: ref }, query: action ? { action } : {} } })),
  });

export const checksQuery = (ref: string) =>
  queryOptions({
    queryKey: qk.resourceChecks(ref),
    queryFn: () => unwrap(client.GET("/resources/{resource}/checks", { params: { path: { resource: ref } } })),
  });

export const summaryQuery = (ref: string, window: "24h" | "7d" | "30d") =>
  queryOptions({
    queryKey: qk.resourceSummary(ref, window),
    queryFn: () => unwrap(client.GET("/resources/{resource}/summary", { params: { path: { resource: ref }, query: { window } } })),
    staleTime: 60_000,
  });

export const seriesQuery = (ref: string, check: string, windowMs: number, points: number) =>
  queryOptions({
    queryKey: qk.resourceSeries(ref, check, `${windowMs}:${points}`),
    queryFn: () => {
      const to = new Date();
      const from = new Date(to.getTime() - windowMs);
      return unwrap(
        client.GET("/resources/{resource}/checks/{check}/series", {
          params: { path: { resource: ref, check }, query: { from: from.toISOString(), to: to.toISOString(), points } },
        }),
      );
    },
    staleTime: 60_000,
  });

export const deploymentsQuery = (ref: string) =>
  queryOptions({
    queryKey: qk.resourceDeployments(ref),
    queryFn: () => unwrap(client.GET("/resources/{resource}/deployments", { params: { path: { resource: ref }, query: { limit: 100 } } })),
  });

export const resourceVersionsQuery = (ref: string) =>
  queryOptions({
    queryKey: qk.resourceVersions(ref),
    queryFn: () => unwrap(client.GET("/resources/{resource}/versions", { params: { path: { resource: ref }, query: { limit: 100 } } })),
  });

export const resourceVersionQuery = (ref: string, v: number) =>
  queryOptions({
    queryKey: qk.resourceVersion(ref, v),
    queryFn: () => unwrap(client.GET("/resources/{resource}/versions/{v}", { params: { path: { resource: ref, v } } })),
    staleTime: Infinity,
  });

/** Canonical YAML of the stored document of an active resource; refetched whenever the version changes. */
export const resourceYamlQuery = (name: string, version: number) =>
  queryOptions({
    queryKey: qk.resourceYaml(name, version),
    queryFn: () => unwrap(client.GET("/export", { params: { query: { kind: "resource", name } }, parseAs: "text" })) as Promise<string>,
    staleTime: Infinity,
  });

export const TIMELINE_PAGE = 50;

/** Timeline of all resources, or of one when `resource` is set. Newest first. */
export const timelineQuery = (f: TimelineFilter = {}) =>
  infiniteQueryOptions({
    queryKey: qk.timeline(f),
    initialPageParam: undefined as string | undefined,
    queryFn: ({ pageParam }) => {
      const query = { limit: TIMELINE_PAGE, cursor: pageParam, kinds: f.kinds?.length ? f.kinds : undefined, from: f.from || undefined, to: f.to || undefined };
      return f.resource
        ? unwrap(client.GET("/resources/{resource}/timeline", { params: { path: { resource: f.resource }, query } }))
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

export const templatesQuery = () =>
  queryOptions({
    queryKey: qk.templates(),
    queryFn: async () => (await unwrap(client.GET("/templates"))).items,
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

export const alertsQuery = (resource?: string) =>
  queryOptions({
    queryKey: qk.alerts(resource),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/alerts", { params: { query: { limit: 500, cursor, resource } } }))),
  });

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

export const connectionsQuery = () =>
  queryOptions({
    queryKey: qk.connections(),
    queryFn: () => fetchAll((cursor) => unwrap(client.GET("/connections", { params: { query: { limit: 500, cursor } } }))),
  });

export const actionQuery = (id: string) =>
  queryOptions({
    queryKey: qk.action(id),
    queryFn: () => unwrap(client.GET("/actions/{id}", { params: { path: { id } } })),
  });

export type { ActionName };
