import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { RANGE_MS } from "../components/chart/range";
import type { RangeKey } from "../components/chart/range";
import { client, unwrap } from "./client";
import { qk } from "./queries";

/**
 * Queries of observed resources (machines and routers): stored snapshots,
 * events, durable operations and, for the local machine, maintenance. They
 * poll as a fallback because freshness changes with time even when no event
 * arrives.
 */
const poll = { refetchInterval: 15_000, staleTime: 0 } as const;
const page = { initialPageParam: undefined as string | undefined, getNextPageParam: (last: { nextCursor: string | null }) => last.nextCursor ?? undefined };

export const capabilitiesQuery = (ref: string) =>
  queryOptions({
    queryKey: qk.resourceCapabilities(ref),
    ...poll,
    queryFn: () => unwrap(client.GET("/resources/{resource}/capabilities", { params: { path: { resource: ref } } })),
  });

export const maintenanceQuery = (ref: string) =>
  queryOptions({
    queryKey: qk.resourceMaintenance(ref),
    ...poll,
    queryFn: () => unwrap(client.GET("/resources/{resource}/maintenance", { params: { path: { resource: ref } } })),
  });

/** Stored events, newest first by the time they occurred; `from` and `to` bound that time. */
export const eventsQuery = (ref: string, range: { from?: string; to?: string } = {}) =>
  infiniteQueryOptions({
    queryKey: [...qk.resourceEvents(ref), "newest", range.from ?? "", range.to ?? ""],
    ...page,
    ...poll,
    queryFn: ({ pageParam }) =>
      unwrap(
        client.GET("/resources/{resource}/events", {
          params: { path: { resource: ref }, query: { limit: 50, cursor: pageParam, order: "desc", from: range.from, to: range.to } },
        }),
      ),
  });

/** Most points a history request asks for; the server allows 199 per page. */
const HISTORY_POINTS = 180;

/**
 * The step that fits a range into one page of minute points: one snapshot
 * per step, whole minutes because minute points are no finer.
 */
export function historyStep(rangeMs: number): string {
  return `${Math.max(1, Math.ceil(rangeMs / HISTORY_POINTS / 60_000))}m`;
}

/**
 * Minute points of the last `range`, thinned to one page and returned
 * oldest first for plotting. The request is made when the query runs, so a
 * poll moves the window forward.
 */
export const historyQuery = (ref: string, range: RangeKey) =>
  queryOptions({
    queryKey: [...qk.resourceHistory(ref), "range", range],
    ...poll,
    queryFn: async () => {
      const from = new Date(Date.now() - RANGE_MS[range]).toISOString();
      const list = await unwrap(
        client.GET("/resources/{resource}/history", {
          params: { path: { resource: ref }, query: { resolution: "minute", from, step: historyStep(RANGE_MS[range]), limit: 199 } },
        }),
      );
      return [...list.items].reverse();
    },
  });

export const operationsQuery = (ref: string) =>
  infiniteQueryOptions({
    queryKey: qk.resourceOperations(ref),
    ...page,
    ...poll,
    queryFn: ({ pageParam }) =>
      unwrap(client.GET("/resources/{resource}/operations", { params: { path: { resource: ref }, query: { limit: 50, cursor: pageParam } } })),
  });

export const operationQuery = (ref: string, id: string) =>
  queryOptions({
    queryKey: qk.resourceOperation(ref, id),
    ...poll,
    enabled: !!id,
    queryFn: () => unwrap(client.GET("/resources/{resource}/operations/{id}", { params: { path: { resource: ref, id } } })),
  });
