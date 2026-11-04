import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
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

export const eventsQuery = (ref: string) =>
  infiniteQueryOptions({
    queryKey: qk.resourceEvents(ref),
    ...page,
    ...poll,
    queryFn: ({ pageParam }) => unwrap(client.GET("/resources/{resource}/events", { params: { path: { resource: ref }, query: { limit: 50, cursor: pageParam } } })),
  });

export const historyQuery = (ref: string, resolution: "raw" | "minute" = "raw") =>
  infiniteQueryOptions({
    queryKey: [...qk.resourceHistory(ref), resolution],
    ...page,
    ...poll,
    queryFn: ({ pageParam }) =>
      unwrap(client.GET("/resources/{resource}/history", { params: { path: { resource: ref }, query: { limit: 50, cursor: pageParam, resolution } } })),
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
