import { infiniteQueryOptions, queryOptions } from "@tanstack/react-query";
import { client, unwrap } from "./client";
import type { components } from "./schema";

type S = components["schemas"];
export type Machine = S["MachineRecord"];
export type MachineSnapshot = S["MachineSnapshot"];
export type MachineCoverage = S["MachineCoverage"];
export type MachineCapability = S["MachineCapability"];
export type MachineOperation = S["MachineOperation"];
export type MachineMaintenance = S["MachineMaintenance"];
export type MachineDocumentInput = S["MachineDocumentInput"];
export type PowerAction = "reboot" | "graceful-shutdown";

export const mk = {
  all: ["machines"] as const,
  list: ["machines", "list"] as const,
  detail: (name: string) => ["machines", name] as const,
};
const poll = { refetchInterval: 15_000, staleTime: 0 } as const;
const page = { initialPageParam: undefined as string | undefined, getNextPageParam: (last: { nextCursor: string | null }) => last.nextCursor ?? undefined };

export const machinesQuery = (archived=false) =>
  infiniteQueryOptions({
    queryKey: archived?[...mk.all,"archives"]:mk.list,
    ...page,
    ...poll,
    queryFn: ({ pageParam }) => unwrap(client.GET(archived?"/machine-archives":"/machines", { params: { query: { limit: 50, cursor: pageParam } } })),
  });
export const machineQuery = (machine: string) =>
  queryOptions({
    queryKey: mk.detail(machine),
    ...poll,
    queryFn: () => unwrap(client.GET("/machines/{machine}", { params: { path: { machine } } })),
  });
export const capabilitiesQuery = (machine: string) =>
  queryOptions({
    queryKey: [...mk.detail(machine), "capabilities"],
    ...poll,
    queryFn: () => unwrap(client.GET("/machines/{machine}/capabilities", { params: { path: { machine } } })),
  });
export const maintenanceQuery = (machine: string) =>
  queryOptions({
    queryKey: [...mk.detail(machine), "maintenance"],
    ...poll,
    queryFn: () => unwrap(client.GET("/machines/{machine}/maintenance", { params: { path: { machine } } })),
  });
export const machineEventsQuery = (machine: string) =>
  infiniteQueryOptions({
    queryKey: [...mk.detail(machine), "events"],
    ...page,
    ...poll,
    queryFn: ({ pageParam }) => unwrap(client.GET("/machines/{machine}/events", { params: { path: { machine }, query: { limit: 50, cursor: pageParam } } })),
  });
export const machineHistoryQuery = (machine: string,resolution:"raw"|"minute"="raw") =>
  infiniteQueryOptions({
    queryKey: [...mk.detail(machine), "history",resolution],
    ...page,
    ...poll,
    queryFn: ({ pageParam }) => unwrap(client.GET("/machines/{machine}/history", { params: { path: { machine }, query: { limit: 50, cursor: pageParam,resolution } } })),
  });
export const machineVersionsQuery = (machine: string) =>
  infiniteQueryOptions({
    queryKey: [...mk.detail(machine), "versions"],
    ...page,
    queryFn: ({ pageParam }) => unwrap(client.GET("/machines/{machine}/versions", { params: { path: { machine }, query: { limit: 50, cursor: pageParam } } })),
  });
export const machineOperationsQuery = (machine: string) =>
  infiniteQueryOptions({
    queryKey: [...mk.detail(machine), "operations"],
    ...page,
    ...poll,
    queryFn: ({ pageParam }) =>
      unwrap(client.GET("/machines/{machine}/operations", { params: { path: { machine }, query: { limit: 50, cursor: pageParam } } })),
  });
export const machineOperationQuery = (machine: string, id: string) =>
  queryOptions({
    queryKey: [...mk.detail(machine), "operation", id],
    ...poll,
    enabled: !!id,
    queryFn: () => unwrap(client.GET("/machines/{machine}/operations/{id}", { params: { path: { machine, id } } })),
  });
