import type { Machine, MachineMaintenance, MachineOperation } from "../../api/machines";

export const machineFixture: Machine = {
  id: "machine-1",
  name: "workstation",
  host: "local",
  version: 3,
  phase: "managed",
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  document: {
    apiVersion: "lcm/v1",
    kind: "Machine",
    metadata: { name: "workstation" },
    spec: {
      host: "local",
      monitoring: {
        interval: "30s",
        staleAfter: "2m",
        filesystems: ["/"],
        journal: true,
        thresholds: { cpuPercent: 90, memoryPercent: 90, diskPercent: 90, consecutive: 2, hysteresis: 5 },
      },
    },
  },
  status: {
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
  },
};
export const maintenanceFixture: MachineMaintenance = {
  machineId: "machine-1",
  state: "active",
  enteredAt: "2026-11-01T00:00:00Z",
  actor: { kind: "user", id: "test-admin", name: "Test admin" },
  reason: "Scheduled maintenance",
  acknowledged: true,
	preview:{machineId:"machine-1",version:3,hash:"a".repeat(64),partialCoverage:true,controllerOffline:true,manualPowerOn:true,workloads:[]},
};
export const operationFixture: MachineOperation = {
  id: "operation-1",
  machineId: "machine-1",
  action: "reboot",
  state: "observing",
  unresolved: true,
  version: 3,
  createdAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  actor: maintenanceFixture.actor,
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

export function freshMachine(): Machine {
  return { ...machineFixture, status: { ...machineFixture.status, snapshot: { ...machineFixture.status.snapshot!, observedAt: new Date().toISOString() } } };
}
export function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), { status, headers: { "Content-Type": "application/json" } });
}
