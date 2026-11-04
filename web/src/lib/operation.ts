import { client, unwrap } from "../api/client";
import type { ResourceOperation } from "../api/types";
import { s } from "../strings";
import type { StatusView } from "./status";

export function operationView(op: ResourceOperation): StatusView {
  if (op.resolvedBy || op.resolution) return { tone: "warn", icon: "?", label: s.operation.unverified };
  const label = s.operation.states[op.state] ?? op.state;
  return {
    tone: op.state === "succeeded" ? "ok" : op.state === "failed" ? "bad" : op.state === "indeterminate" ? "warn" : "info",
    icon: op.state === "succeeded" ? "✓" : "?",
    label,
  };
}

/**
 * A durable operation request as the browser keeps it. It is persisted
 * before submitting, so a refresh or a lost response preserves the exact
 * request (action, version and idempotency key) and a retry cannot become a
 * second operation.
 */
export interface OperationAttempt {
  resourceId: string;
  action: string;
  version: number;
  key: string;
  phase: "prepared" | "uncertain" | "accepted" | "rejected";
  operationId?: string;
  /** The operator acknowledged the impact; a retry must send it again. */
  acknowledgeImpact?: boolean;
}

const storageKey = (id: string) => `lcm.operation-attempt.${id}`;
// Attempts stored by earlier consoles for machines; read so that an open attempt survives the upgrade.
const legacyKey = (id: string) => `lcm.machine-attempt.${id}`;

export function readAttempt(id: string): OperationAttempt | null {
  let raw = localStorage.getItem(storageKey(id));
  let legacy = false;
  if (!raw) {
    raw = localStorage.getItem(legacyKey(id));
    legacy = raw !== null;
  }
  if (!raw) return null;
  const v: unknown = JSON.parse(raw);
  if (!v || typeof v !== "object") throw new Error(s.operation.storageError);
  const stored = v as OperationAttempt & { machineId?: string };
  const a: OperationAttempt = legacy ? { ...stored, resourceId: stored.machineId ?? "" } : stored;
  if (
    a.resourceId !== id ||
    typeof a.action !== "string" ||
    !/^[a-z][a-z-]{0,63}$/.test(a.action) ||
    !Number.isInteger(a.version) ||
    a.version < 1 ||
    typeof a.key !== "string" ||
    !/^[\w.:-]{1,128}$/.test(a.key) ||
    !["prepared", "uncertain", "accepted", "rejected"].includes(a.phase) ||
    (a.operationId !== undefined && typeof a.operationId !== "string") ||
    (a.acknowledgeImpact !== undefined && typeof a.acknowledgeImpact !== "boolean")
  )
    throw new Error(s.operation.storageError);
  const { resourceId, action, version, key, phase, operationId, acknowledgeImpact } = a;
  return {
    resourceId,
    action,
    version,
    key,
    phase,
    ...(operationId === undefined ? {} : { operationId }),
    ...(acknowledgeImpact ? { acknowledgeImpact } : {}),
  };
}

export function writeAttempt(a: OperationAttempt): void {
  localStorage.setItem(storageKey(a.resourceId), JSON.stringify(a));
}

export function clearAttempt(id: string): void {
  localStorage.removeItem(storageKey(id));
  localStorage.removeItem(legacyKey(id));
}

export function newAttempt(resource: { id: string; version: number }, action: string, acknowledgeImpact = false): OperationAttempt {
  return {
    ...(acknowledgeImpact ? { acknowledgeImpact } : {}),
    resourceId: resource.id,
    action,
    version: resource.version,
    key:
      typeof crypto.randomUUID === "function"
        ? crypto.randomUUID()
        : Array.from(crypto.getRandomValues(new Uint8Array(16)), (n) => n.toString(16).padStart(2, "0")).join(""),
    phase: "prepared",
  };
}

export function submitAttempt(resource: string, attempt: OperationAttempt): Promise<ResourceOperation> {
  return unwrap(
    client.POST("/resources/{resource}/operations", {
      params: { path: { resource }, header: { "If-Match": String(attempt.version) } },
      body: { action: attempt.action, idempotencyKey: attempt.key, ...(attempt.acknowledgeImpact ? { acknowledgeImpact: true } : {}) },
    }),
  );
}
