import { describe, expect, it } from "vitest";
import { machineFixture } from "../pages/resource/test-fixtures";
import type { ResourceDetail } from "../api/types";
import { coverageView, cpuPercent, durationMs, machineFresh, machineHealth, percentUsed, powerUnavailable } from "./machine";

const now = Date.parse("2026-11-01T00:01:00Z");
const status = machineFixture.machine!.status!;
const withStatus = (over: Partial<typeof status>): ResourceDetail => ({ ...machineFixture, machine: { status: { ...status, ...over } } });

describe("machine observation semantics", () => {
  it("expires a previously healthy cached snapshot at staleAfter, without claiming shutdown", () => {
    expect(machineFresh(machineFixture, now)).toBe(true);
    expect(machineHealth(machineFixture, now).tone).toBe("ok");
    expect(machineHealth(machineFixture, now + 60_000)).toMatchObject({ tone: "neutral", label: "未知（觀測過期或尚無資料）" });
    expect(machineHealth(withStatus({ fresh: false }), now).tone).toBe("neutral");
    expect(machineFresh(withStatus({ snapshot: null }), now)).toBe(false);
    expect(machineFresh({ ...machineFixture, machine: { status: null } }, now)).toBe(false);
  });
  it("handles compound durations conservatively and rejects future observations", () => {
    const machine = structuredClone(machineFixture);
    machine.document.spec.config = { ...machine.document.spec.config, staleAfter: "1m30s" };
    expect(machineFresh(machine, now)).toBe(true);
    expect(machineFresh(machine, now + 30_000)).toBe(false);
    expect(machineFresh(machine, now - 61_000)).toBe(false);
    machine.document.spec.config = { ...machine.document.spec.config, staleAfter: "invalid" };
    expect(machineFresh(machine, now)).toBe(false);
    expect(durationMs("2m")).toBe(120_000);
    expect(durationMs(undefined)).toBeNaN();
  });
  it("keeps optional unknown coverage separate from machine health and rejects identity mismatch", () => {
    expect(coverageView(status.snapshot?.coverage["psi"]).tone).toBe("neutral");
    expect(coverageView(undefined).label).toBe("未知");
    expect(coverageView({ state: "unsupported" }).tone).toBe("neutral");
    expect(machineHealth(machineFixture, now).tone).toBe("ok");
    expect(machineHealth(withStatus({ identity: "changed" }), now).tone).toBe("neutral");
    expect(machineHealth(withStatus({ health: "unknown" }), now).tone).toBe("neutral");
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
});
