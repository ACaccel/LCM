import type { MachineCoverage, MachineStatus, ResourceCapability, ResourceDetail } from "../api/types";
import { s } from "../strings";
import { healthView } from "./status";
import type { StatusView } from "./status";

/** The parts of a resource the observation semantics depend on. */
export type Observed = Pick<ResourceDetail, "document"> & { machine?: { status: MachineStatus | null } };

const UNITS: Record<string, number> = { ns: 1e-6, us: 1e-3, µs: 1e-3, ms: 1, s: 1000, m: 60_000, h: 3_600_000 };

/** A Go duration such as `1m30s` in milliseconds; NaN unless the whole text is a duration. */
export function durationMs(text: unknown): number {
  if (typeof text !== "string" || text === "") return NaN;
  let ms = 0;
  let consumed = "";
  for (const match of text.matchAll(/(\d+(?:\.\d+)?)(ns|us|µs|ms|s|m|h)/g)) {
    consumed += match[0];
    ms += Number(match[1]) * (UNITS[match[2] ?? ""] ?? 0);
  }
  return consumed === text ? ms : NaN;
}

/** The observation window of a machine or router document (`spec.config.staleAfter`). */
export function staleAfterMs(r: Pick<ResourceDetail, "document">): number {
  return durationMs(r.document.spec.config?.["staleAfter"]);
}

/** Never let cached server freshness outlive the configured observation window. */
export function machineFresh(r: Observed, now: number): boolean {
  const status = r.machine?.status;
  if (!status) return false;
  const at = Date.parse(status.snapshot?.observedAt ?? "");
  const ms = staleAfterMs(r);
  return status.fresh && Number.isFinite(at) && Number.isFinite(ms) && ms > 0 && now >= at && now - at < ms;
}

export function machineHealth(r: Observed, now: number): StatusView {
  const status = r.machine?.status;
  if (!status || !machineFresh(r, now)) return { tone: "neutral", icon: "?", label: s.machine.stale };
  if (status.identity !== "verified") return healthView("unknown");
  const h = status.health;
  return healthView(h === "healthy" || h === "degraded" || h === "down" ? h : "unknown");
}

export function coverageView(coverage?: MachineCoverage): StatusView {
  const state = coverage?.state ?? "unknown";
  const label = s.machine.coverageStates[state] ?? state;
  return { tone: state === "available" ? "ok" : state === "partial" || state === "gap" ? "warn" : "neutral", icon: state === "available" ? "✓" : "?", label };
}

/** Why a durable operation cannot be requested now; power-on never can, as there is no independent channel. */
export function powerUnavailable(action: string, caps: ResourceCapability[] | undefined): string {
  if (action === "power-on") return s.machine.noPowerOn;
  const cap = caps?.find((c) => c.action === action);
  return cap?.available ? "" : cap?.reason || s.machine.capabilityUnknown;
}

export function percentUsed(total: number, available: number): string {
  return total > 0 && available >= 0 && available <= total ? `${((1 - available / total) * 100).toFixed(1)}%` : s.machine.unknown;
}

export function cpuPercent(value: number | null | undefined): string {
  return value != null && Number.isFinite(value) ? `${value.toFixed(1)}%` : s.machine.unknown;
}
