import { describe, expect, it } from "vitest";
import type { Trigger } from "../api/types";
import { policyRelation, triggerConcernsService } from "./policy-match";

const sig = (props: Record<string, unknown>): Trigger => ({ id: "t", type: "trigger.signal", props });

describe("triggerConcernsService", () => {
  it("matches a service listed by name", () => {
    expect(triggerConcernsService(sig({ services: ["web", "api"] }), "web", {})).toBe(true);
    expect(triggerConcernsService(sig({ service: "web" }), "web", {})).toBe(true);
    expect(triggerConcernsService(sig({ services: ["api"] }), "web", {})).toBe(false);
  });

  it("matches a label selector the service satisfies", () => {
    expect(triggerConcernsService(sig({ labels: { tier: "web" } }), "x", { tier: "web", env: "prod" })).toBe(true);
    expect(triggerConcernsService(sig({ labels: { tier: "db" } }), "x", { tier: "web" })).toBe(false);
  });

  it("does not match an empty selector or unrelated props", () => {
    expect(triggerConcernsService(sig({ labels: {} }), "web", { a: "b" })).toBe(false);
    expect(triggerConcernsService(sig({ name: "web" }), "web", {})).toBe(false);
  });
});

describe("policyRelation", () => {
  const target = (type: string, service: string | null) => ({ node: type, type, service });

  it("prefers the resolved targets", () => {
    const p = { targets: [target("lifecycle.deploy", "web"), target("lifecycle.rollback", "web"), target("lifecycle.deploy", "api")], triggers: [] };
    expect(policyRelation(p, "web", {})).toEqual({ kind: "acts", types: ["lifecycle.deploy", "lifecycle.rollback"] });
  });

  it("falls back to the trigger filter", () => {
    expect(policyRelation({ targets: [target("lifecycle.restart", null)], triggers: [sig({ services: ["web"] })] }, "web", {})).toEqual({ kind: "dynamic" });
    expect(policyRelation({ targets: [], triggers: [sig({ services: ["web"] })] }, "web", {})).toEqual({ kind: "watches" });
  });

  it("ignores policies that neither act on nor listen to the service", () => {
    expect(policyRelation({ targets: [target("lifecycle.deploy", "api")], triggers: [] }, "web", {})).toBeNull();
    expect(policyRelation({ targets: [target("lifecycle.restart", null)], triggers: [sig({ services: ["api"] })] }, "web", {})).toBeNull();
  });
});
