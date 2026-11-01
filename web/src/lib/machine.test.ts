import { afterEach, describe, expect, it, vi } from "vitest";
import { machineFixture, operationFixture } from "../pages/machine/test-fixtures";
import {
  clearMachineAttempt,
  coverageView,
  cpuPercent,
  machineFresh,
  machineHealth,
  newMachineAttempt,
  operationView,
  percentUsed,
  powerUnavailable,
  readMachineAttempt,
  writeMachineAttempt,
} from "./machine";

const now = Date.parse("2026-11-01T00:01:00Z");
afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("machine observation semantics", () => {
  it("expires a previously healthy cached snapshot at staleAfter, without claiming shutdown", () => {
    expect(machineFresh(machineFixture, now)).toBe(true);
    expect(machineHealth(machineFixture, now).tone).toBe("ok");
    expect(machineHealth(machineFixture, now + 60_000)).toMatchObject({ tone: "neutral", label: "未知（觀測過期或尚無資料）" });
    expect(machineHealth({ ...machineFixture, status: { ...machineFixture.status, fresh: false } }, now).tone).toBe("neutral");
    expect(machineFresh({ ...machineFixture, status: { ...machineFixture.status, snapshot: null } }, now)).toBe(false);
  });
  it("handles compound durations conservatively and rejects future observations", () => {
    const machine = structuredClone(machineFixture);
    machine.document.spec.monitoring.staleAfter = "1m30s";
    expect(machineFresh(machine, now)).toBe(true);
    expect(machineFresh(machine, now + 30_000)).toBe(false);
    expect(machineFresh(machine, now - 61_000)).toBe(false);
    machine.document.spec.monitoring.staleAfter = "invalid";
    expect(machineFresh(machine, now)).toBe(false);
  });
  it("keeps optional unknown coverage separate from machine health and rejects identity mismatch", () => {
    expect(coverageView(machineFixture.status.snapshot?.coverage.psi).tone).toBe("neutral");
    expect(coverageView(undefined).label).toBe("未知");
    expect(coverageView({ state: "unsupported" }).tone).toBe("neutral");
    expect(machineHealth(machineFixture, now).tone).toBe("ok");
    expect(machineHealth({ ...machineFixture, status: { ...machineFixture.status, identity: "changed" } }, now).tone).toBe("neutral");
    expect(machineHealth({ ...machineFixture, status: { ...machineFixture.status, health: "unknown" } }, now).tone).toBe("neutral");
  });
  it("represents first CPU samples and unavailable values as unknown", () => {
    expect(cpuPercent(null)).toBe("未知");
    expect(percentUsed(0, 0)).toBe("未知");
    expect(percentUsed(100, 200)).toBe("未知");
    expect(percentUsed(100, 60)).toBe("40.0%");
  });
  it("requires explicit server capability, and never enables power-on", () => {
    expect(powerUnavailable("reboot", undefined)).not.toBe("");
    expect(powerUnavailable("reboot", [{ action: "reboot", available: false, reason: "helper unavailable" }])).toBe("helper unavailable");
    expect(powerUnavailable("reboot", [{ action: "reboot", available: true, reason: "" }])).toBe("");
    expect(powerUnavailable("power-on", [{ action: "power-on", available: true, reason: "" }])).toContain("人工開機");
  });
  it("never presents manual resolution as verified success", () => {
    expect(
      operationView({ ...operationFixture, state: "indeterminate", unresolved: false, resolvedBy: operationFixture.actor, resolution: "Inspected manually" }),
    ).toMatchObject({ tone: "warn", label: "人工結案／結果未驗證" });
    expect(operationView({ ...operationFixture, state: "succeeded" }).tone).toBe("ok");
  });
});

describe("durable browser attempt", () => {
  it("preserves request identity across reloads and creates a fresh key for a new attempt", () => {
    const a = newMachineAttempt(machineFixture, "reboot");
    writeMachineAttempt({ ...a, phase: "uncertain" });
    expect(readMachineAttempt(machineFixture.id)).toEqual({ ...a, phase: "uncertain" });
    expect(readMachineAttempt("replacement-machine")).toBeNull();
    const b = newMachineAttempt(machineFixture, "reboot");
    expect(b.key).not.toBe(a.key);
    clearMachineAttempt(machineFixture.id);
    expect(readMachineAttempt(machineFixture.id)).toBeNull();
  });
  it("generates random keys when randomUUID is unavailable on a plain HTTP console", () => {
    const fill = crypto.getRandomValues.bind(crypto);
    vi.stubGlobal("crypto", { getRandomValues: fill });
    const a = newMachineAttempt(machineFixture, "reboot");
    const b = newMachineAttempt(machineFixture, "reboot");
    expect(a.key).toMatch(/^[a-f0-9]{32}$/);
    expect(a.key).not.toBe(b.key);
  });
  it("blocks corrupt stored requests instead of generating a replacement key", () => {
    localStorage.setItem(`lcm.machine-attempt.${machineFixture.id}`, '{"action":"force-off"}');
    expect(() => readMachineAttempt(machineFixture.id)).toThrow();
  });
});
