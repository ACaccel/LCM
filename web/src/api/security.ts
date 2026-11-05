import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { client, unwrap } from "./client";
import type { components } from "./schema";

type S = components["schemas"];
export type SecurityStatus = S["SecurityStatus"];
export type Incident = S["SecurityIncident"];
export type IncidentDetail = S["SecurityIncidentDetail"];
export type Handling = S["SecurityHandling"];
export type HandlingInput = S["SecurityHandlingUpdate"];
export type SecuritySettings = S["SecuritySettings"];
export type SettingsHistoryEntry = S["SecuritySettingsHistory"];
export type TestReceipt = S["SecurityNotificationTest"];
export type TrafficPoint = S["SecuritySeriesPoint"];
export type TrafficSeries = S["SecuritySeries"];
export type HostStatus = S["SecurityHostStatus"];
export type Profile = S["SecurityHostProfile"];
export type NginxLimit = S["SecurityHostLimit"];
export type HostPreview = S["SecurityHostPreview"];
export type HostOperation = S["SecurityHostOperation"];
export type HostAction = HostPreview["action"];
export type Severity = Incident["severity"];
export type Source = Incident["source"];
export type DetectionState = Incident["state"];
export type Category = NginxLimit["category"];
export interface IncidentFilter { machineId?: string; service?: string; source?: Source; severity?: Severity; state?: DetectionState; from?: string; to?: string }
export interface TrafficFilter { machineId?: string; service?: string; category?: Category; from?: string; to?: string }
export interface Page<T> { items: T[]; nextCursor: string | null }
export type HostSettings = Record<string, never> | { hour: number; minute: number } | { profiles: { name: string; enabled: boolean }[] } | { limits: NginxLimit[] };
interface PreviewInput { requestId: string; action: HostAction; reason: string; expectedDigest: string; settings: HostSettings }
interface ApplyInput { requestId: string; previewHash: string; reason: string }
const poll = { staleTime: 0, refetchInterval: 15_000 } as const;
export const sk = { all: ["security"] as const, detail: (id: string) => ["security", "incident", id] as const };
export const securityStatusQuery = () => queryOptions({ queryKey: [...sk.all, "status"], queryFn: ({ signal }) => unwrap(client.GET("/security/status", { signal })), ...poll });
// An unconfigured host stays that way until LCM is reconfigured and restarted, so it is not polled.
export const hostStatusQuery = () => queryOptions({ queryKey: [...sk.all, "host"], queryFn: ({ signal }) => unwrap(client.GET("/security/host", { signal })), ...poll, refetchInterval: (query) => query.state.data?.configured === false ? false : poll.refetchInterval });
export const securitySettingsQuery = () => queryOptions({ queryKey: [...sk.all, "settings"], queryFn: ({ signal }) => unwrap(client.GET("/security/settings", { signal })), ...poll });
export const settingsHistoryQuery = () => queryOptions({ queryKey: [...sk.all, "settings-history"], queryFn: ({ signal }) => unwrap(client.GET("/security/settings/history", { signal })), ...poll });
export const incidentsQuery = (filter: IncidentFilter) => infiniteQueryOptions({
  queryKey: [...sk.all, "incidents", filter], initialPageParam: undefined as string | undefined,
  getNextPageParam: (last: Page<Incident>) => last.nextCursor ?? undefined,
  queryFn: ({ pageParam, signal }) => unwrap(client.GET("/security/incidents", { params: { query: { ...filter, cursor: pageParam, limit: 50 } }, signal })), ...poll,
});
export const incidentQuery = (id: string) => queryOptions({ queryKey: sk.detail(id), queryFn: ({ signal }) => unwrap(client.GET("/security/incidents/{id}", { params: { path: { id } }, signal })), ...poll });
export const trafficSeriesQuery = (filter: TrafficFilter, bucketSeconds: 60 | 300 | 900 | 3600) => queryOptions({
  queryKey: [...sk.all, "traffic", filter, bucketSeconds],
  queryFn: ({ signal }) => unwrap(client.GET("/security/traffic/series", { params: { query: { ...filter, bucketSeconds } }, signal })), ...poll,
});
export const operationsQuery = () => infiniteQueryOptions({
  queryKey: [...sk.all, "operations"], initialPageParam: undefined as string | undefined,
  getNextPageParam: (last: Page<HostOperation>) => last.nextCursor ?? undefined,
  queryFn: ({ pageParam, signal }) => unwrap(client.GET("/security/host/operations", { params: { query: { cursor: pageParam, limit: 30 } }, signal })), ...poll,
});
export const operationQuery = (id: string) => queryOptions({ queryKey: [...sk.all, "operation", id], queryFn: ({ signal }) => unwrap(client.GET("/security/host/operations/{id}", { params: { path: { id } }, signal })), ...poll });
export const testQuery = (requestId: string) => queryOptions({ queryKey: [...sk.all, "test", requestId], queryFn: ({ signal }) => unwrap(client.GET("/security/tests/{id}", { params: { path: { id: requestId } }, signal })), ...poll });
export const securityAPI = {
  handle: (id: string, version: number, body: HandlingInput) => unwrap(client.PUT("/security/incidents/{id}/handling", { params: { path: { id }, header: { "If-Match": `"${version}"` } }, body })),
  settings: (version: number, body: { notificationsEnabled: boolean; cooldownSeconds: number; minimumSeverity: Severity; reason: string }) => unwrap(client.PUT("/security/settings", { params: { header: { "If-Match": `"${version}"` } }, body })),
  test: (body: { requestId: string; reason: string }) => unwrap(client.POST("/security/test-notification", { body })),
  preview: (body: PreviewInput) => unwrap(client.POST("/security/host/preview", { body })),
  apply: (body: ApplyInput) => unwrap(client.POST("/security/host/apply", { body })),
};
