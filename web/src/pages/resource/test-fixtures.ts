import type { MachineMaintenance, MachineStatus, ResourceAction, ResourceDetail, ResourceImpact, ResourceOperation } from "../../api/types";

const actor = { kind: "user", id: "test-admin", name: "Test admin" } as const;

const machineStatus: MachineStatus = {
  fresh: true,
  identity: "verified",
  health: "healthy",
  reasons: [],
  journal: { state: "unavailable", reason: "journal permission denied" },
  snapshot: {
    id: 1,
    observedAt: "2026-11-01T00:00:00Z",
    bootId: "boot-1",
    os: "linux",
    init: "systemd",
    uptimeSeconds: 100,
    cpuTotal: 1000,
    cpuIdle: 800,
    cpuCount: 4,
    cpuPercent: null,
    memoryTotal: 1000,
    memoryAvailable: 600,
    swapTotal: 0,
    swapFree: 0,
    filesystems: [{ path: "/", total: 1000, available: 800, inodes: 1000, freeInodes: 900 }],
    units: { "app.service": "active" },
    coverage: {
      cpu: { state: "available" },
      memory: { state: "available" },
      filesystems: { state: "available" },
      units: { state: "available" },
      psi: { state: "unknown", reason: "optional probe unsupported" },
    },
  },
};

const action = (name: string, kind: ResourceAction["kind"], available = true, reason = ""): ResourceAction => ({
  name,
  kind,
  available,
  reason,
  risk: "reversible",
  policy: false,
});

/** A resource with the fields every type has; type extras are added by the fixtures below. */
export function resourceFixture(over: Partial<ResourceDetail> = {}): ResourceDetail {
  return {
    id: "res-1",
    name: "web",
    type: "service",
    adapter: "systemd",
    connection: "",
    phase: "managed",
    labels: {},
    document: { apiVersion: "lcm/v2", kind: "Resource", metadata: { name: "web" }, spec: { type: "service", adapter: "systemd", config: { unit: "web.service" } } },
    version: 3,
    createdAt: "2026-11-01T00:00:00Z",
    updatedAt: "2026-11-01T00:00:00Z",
    updatedBy: actor,
    state: "running",
    stateSince: "2026-11-01T00:00:00Z",
    health: { status: "healthy", since: "2026-11-01T00:00:00Z", reasons: [] },
    checks: [],
    alerts: [],
    actions: [action("start", "action", false, "invalidState"), action("stop", "action"), action("restart", "action"), action("retire", "phase"), action("resume", "phase", false, "invalidState"), action("unmanage", "phase", false, "invalidState")],
    operation: null,
    managedBy: null,
    unresolvedRelations: [],
    service: { deployedVersion: "", lastDeployment: null },
    ...over,
  };
}

export const machineFixture: ResourceDetail = resourceFixture({
  id: "machine-1",
  name: "workstation",
  type: "machine",
  adapter: "local",
  state: "online",
  document: {
    apiVersion: "lcm/v2",
    kind: "Resource",
    metadata: { name: "workstation" },
    spec: {
      type: "machine",
      adapter: "local",
      config: {
        interval: "30s",
        staleAfter: "2m",
        filesystems: ["/"],
        journal: true,
        thresholds: { cpuPercent: 90, memoryPercent: 90, diskPercent: 90, consecutive: 2, hysteresis: 5 },
      },
    },
  },
  actions: [
    action("observe", "action"),
    action("maintenance-enter", "action"),
    action("reboot", "operation"),
    action("graceful-shutdown", "operation"),
    action("power-on", "operation", false, "no independent power channel"),
    action("retire", "phase"),
    action("resume", "phase", false, "machine is managed"),
    action("unmanage", "phase", false, "machine must be retired first"),
  ],
  service: undefined,
  machine: { status: machineStatus },
});

export const routerFixture: ResourceDetail = resourceFixture({
  id: "router-1",
  name: "gateway",
  type: "router",
  adapter: "xiaomi",
  connection: "home-router",
  state: "online",
  document: {
    apiVersion: "lcm/v2",
    kind: "Resource",
    metadata: { name: "gateway" },
    spec: { type: "router", adapter: "xiaomi", connection: "home-router", config: { interval: "1m", staleAfter: "3m" } },
  },
  actions: [action("observe", "action"), { ...action("reboot", "operation"), risk: "disruptive" }, action("retire", "phase"), action("resume", "phase", false, "router is managed"), action("unmanage", "phase", false, "router must be retired first")],
  service: undefined,
  router: {
    status: {
      fresh: true,
      health: "healthy",
      reasons: [],
      reachability: "reachable",
      bootAt: "2026-10-31T23:00:00Z",
      snapshot: {
        id: 9,
        observedAt: "2026-11-01T00:00:00Z",
        outcome: "ok",
        cpuPercent: 12.5,
        cpuCores: 4,
        memoryPercent: 40,
        uptimeSeconds: 3600,
        temperatureC: null,
        clients: 7,
        model: "RA70",
        firmware: "1.0.168",
        wan: { link: true, status: 1, uptimeSeconds: 1800, type: "dhcp", upBytesPerSec: 2048, downBytesPerSec: 1048576, uploadedBytes: 1073741824, downloadedBytes: null },
        sources: { status: { state: "available" }, wan: { state: "unavailable", reason: "wan status request failed" } },
      },
    },
  },
});

export const maintenanceFixture: MachineMaintenance = {
  resourceId: "machine-1",
  state: "active",
  enteredAt: "2026-11-01T00:00:00Z",
  actor,
  reason: "Scheduled maintenance",
  acknowledged: true,
  preview: { resourceId: "machine-1", version: 3, hash: "a".repeat(64), partialCoverage: true, controllerOffline: true, manualPowerOn: true, workloads: [] },
};

export const operationFixture: ResourceOperation = {
  id: "operation-1",
  resourceId: "machine-1",
  action: "reboot",
  state: "observing",
  unresolved: true,
  version: 3,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  actor,
  request: {
    issuedAt: "2026-11-01T00:00:00Z",
    validUntil: "2026-11-01T00:01:30Z",
    controllerId: "machine-1",
    operationId: "operation-1",
    requestHash: "synthetic-hash",
    action: "reboot",
    bootId: "boot-1",
    deadline: "2026-11-01T00:10:00Z",
  },
  receipt: { state: "accepted", acceptedAt: "2026-11-01T00:00:00Z", bootId: "boot-1" },
  reason: "Awaiting new boot evidence",
};

/** A machine whose latest snapshot was taken just now. */
export function freshMachine(): ResourceDetail {
  const status = machineFixture.machine!.status!;
  return { ...machineFixture, machine: { status: { ...status, snapshot: { ...status.snapshot!, observedAt: new Date().toISOString() } } } };
}

export function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}

/** The impact of an action on a resource nothing depends on. */
export function noImpact(resource: string, action = ""): ResourceImpact {
  return { resource, dependents: [], controlDependents: [], unresolved: [], ...(action ? { action } : {}), acknowledgementRequired: false };
}
