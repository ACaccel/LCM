import type { Machine, MachineCapability, MachineCoverage, MachineOperation, PowerAction } from "../api/machines";
import { client, unwrap } from "../api/client";
import { s } from "../strings";
import { healthView } from "./status";
import type { StatusView } from "./status";

/** Never let cached server freshness outlive the configured observation window. */
export function machineFresh(machine: Machine, now: number): boolean {
  const at = Date.parse(machine.status.snapshot?.observedAt ?? "");
  const duration = machine.document.spec.monitoring.staleAfter;
  const units: Record<string, number> = { ns: 1e-6, us: 1e-3, µs: 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 };
  let ms = 0;
  let consumed = "";
  for (const match of duration.matchAll(/(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g)) {
    consumed += match[0];
    ms += Number(match[1]) * (units[match[2] ?? ""] ?? 0);
  }
  return machine.status.fresh && Number.isFinite(at) && Number.isFinite(ms) && ms > 0 && consumed === duration && now >= at && now - at < ms;
}

export function machineHealth(machine: Machine, now: number): StatusView {
  if (!machineFresh(machine, now)) return { tone: "neutral", icon: "?", label: s.machine.stale };
  if (machine.status.identity !== "verified") return healthView("unknown");
  const h = machine.status.health;
  return healthView(h === "healthy" || h === "degraded" || h === "down" ? h : "unknown");
}

export function coverageView(coverage?: MachineCoverage): StatusView {
  const state = coverage?.state ?? "unknown";
  const label = s.machine.coverageStates[state] ?? state;
  return { tone: state === "available" ? "ok" : state === "partial" || state === "gap" ? "warn" : "neutral", icon: state === "available" ? "✓" : "?", label };
}

export function operationView(op: MachineOperation): StatusView {
  if (op.resolvedBy || op.resolution) return { tone: "warn", icon: "?", label: s.machine.unverified };
  const label = s.machine.operationStates[op.state] ?? op.state;
  return {
    tone: op.state === "succeeded" ? "ok" : op.state === "failed" ? "bad" : op.state === "indeterminate" ? "warn" : "info",
    icon: op.state === "succeeded" ? "✓" : "?",
    label,
  };
}

export function powerUnavailable(action: PowerAction | "power-on", caps: MachineCapability[] | undefined): string {
  if (action === "power-on") return s.machine.noPowerOn;
  const cap = caps?.find((c) => c.action === action);
  return cap?.available ? "" : cap?.reason || s.machine.capabilityUnknown;
}

/** Persist before submitting: a refresh or lost response must preserve the exact request. */
export interface MachineAttempt {
  machineId: string;
  action: PowerAction;
  version: number;
  key: string;
  phase: "prepared" | "uncertain" | "accepted" | "rejected";
  operationId?: string;
}
const storageKey = (id: string) => `lcm.machine-attempt.${id}`;
export function readMachineAttempt(id: string): MachineAttempt | null {
  const raw = localStorage.getItem(storageKey(id));
  if (!raw) return null;
  const v: unknown = JSON.parse(raw);
  if (!v || typeof v !== "object") throw new Error(s.machine.storageError);
  const a = v as MachineAttempt;
  if (
    a.machineId !== id ||
    !["reboot", "graceful-shutdown"].includes(a.action) ||
    !Number.isInteger(a.version) ||
    a.version < 1 ||
    typeof a.key !== "string" ||
    !/^[\w.:-]{1,128}$/.test(a.key) ||
    !["prepared", "uncertain", "accepted", "rejected"].includes(a.phase) ||
    (a.operationId !== undefined && typeof a.operationId !== "string")
  )
    throw new Error(s.machine.storageError);
  return a;
}
export function writeMachineAttempt(a: MachineAttempt): void {
  localStorage.setItem(storageKey(a.machineId), JSON.stringify(a));
}
export function clearMachineAttempt(id: string): void {
  localStorage.removeItem(storageKey(id));
}
export function newMachineAttempt(machine: Machine, action: PowerAction): MachineAttempt {
  return {
    machineId: machine.id,
    action,
    version: machine.version,
    key:
      typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : Array.from(crypto.getRandomValues(new Uint8Array(16)), (n) => n.toString(16).padStart(2, "0")).join(""),
    phase: "prepared",
  };
}
export function submitMachineAttempt(machine: string, attempt: MachineAttempt): Promise<MachineOperation> {
  return unwrap(
    client.POST("/machines/{machine}/operations", {
      params: { path: { machine }, header: { "If-Match": String(attempt.version) } },
      body: { action: attempt.action, idempotencyKey: attempt.key },
    }),
  );
}

export function percentUsed(total: number, available: number): string {
  return total > 0 && available >= 0 && available <= total ? `${((1 - available / total) * 100).toFixed(1)}%` : s.machine.unknown;
}
export function cpuPercent(value: number | null | undefined): string {
  return value != null && Number.isFinite(value) ? `${value.toFixed(1)}%` : s.machine.unknown;
}
