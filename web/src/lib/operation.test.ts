import { afterEach, describe, expect, it, vi } from "vitest";
import { machineFixture, operationFixture } from "../pages/resource/test-fixtures";
import { clearAttempt, newAttempt, operationView, readAttempt, writeAttempt } from "./operation";

afterEach(() => {
  localStorage.clear();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("operation outcome", () => {
  it("never presents manual resolution as verified success", () => {
    expect(
      operationView({ ...operationFixture, state: "indeterminate", unresolved: false, resolvedBy: operationFixture.actor, resolution: "Inspected manually" }),
    ).toMatchObject({ tone: "warn", label: "人工結案／結果未驗證" });
    expect(operationView({ ...operationFixture, state: "succeeded" }).tone).toBe("ok");
  });
});

describe("durable browser attempt", () => {
  it("preserves request identity across reloads and creates a fresh key for a new attempt", () => {
    const a = newAttempt(machineFixture, "reboot");
    writeAttempt({ ...a, phase: "uncertain" });
    expect(readAttempt(machineFixture.id)).toEqual({ ...a, phase: "uncertain" });
    expect(readAttempt("replacement-machine")).toBeNull();
    const b = newAttempt(machineFixture, "reboot");
    expect(b.key).not.toBe(a.key);
    clearAttempt(machineFixture.id);
    expect(readAttempt(machineFixture.id)).toBeNull();
  });
  it("keeps the impact acknowledgement for retries and rejects a malformed one", () => {
    const a = newAttempt(machineFixture, "reboot", true);
    writeAttempt({ ...a, phase: "uncertain" });
    expect(readAttempt(machineFixture.id)).toEqual({ ...a, phase: "uncertain", acknowledgeImpact: true });
    localStorage.setItem(`lcm.operation-attempt.${machineFixture.id}`, JSON.stringify({ ...a, acknowledgeImpact: "yes" }));
    expect(() => readAttempt(machineFixture.id)).toThrow();
  });
  it("keeps an attempt stored by the earlier machine console", () => {
    const legacy = { machineId: machineFixture.id, action: "reboot", version: 3, key: "k-1", phase: "uncertain" };
    localStorage.setItem(`lcm.machine-attempt.${machineFixture.id}`, JSON.stringify(legacy));
    expect(readAttempt(machineFixture.id)).toEqual({ resourceId: machineFixture.id, action: "reboot", version: 3, key: "k-1", phase: "uncertain" });
    clearAttempt(machineFixture.id);
    expect(readAttempt(machineFixture.id)).toBeNull();
  });
  it("generates random keys when randomUUID is unavailable on a plain HTTP console", () => {
    const fill = crypto.getRandomValues.bind(crypto);
    vi.stubGlobal("crypto", { getRandomValues: fill });
    const a = newAttempt(machineFixture, "reboot");
    const b = newAttempt(machineFixture, "reboot");
    expect(a.key).toMatch(/^[a-f0-9]{32}$/);
    expect(a.key).not.toBe(b.key);
  });
  it("blocks corrupt stored requests instead of generating a replacement key", () => {
    localStorage.setItem(`lcm.operation-attempt.${machineFixture.id}`, '{"action":"Force Off"}');
    expect(() => readAttempt(machineFixture.id)).toThrow();
  });
});
