import { describe, expect, it } from "vitest";
import type { Trigger } from "../api/types";
import { policyRelation, triggerConcernsResource } from "./policy-match";
import type { ResourceRef } from "./policy-match";

const sig = (props: Record<string, unknown>): Trigger => ({ id: "t", type: "trigger.signal", props });
const web: ResourceRef = { name: "web", type: "service", labels: {} };

describe("triggerConcernsResource", () => {
  it("matches a resource listed by name", () => {
    expect(triggerConcernsResource(sig({ resources: ["web", "api"] }), web)).toBe(true);
    expect(triggerConcernsResource(sig({ resource: "web" }), web)).toBe(true);
    expect(triggerConcernsResource(sig({ resources: ["api"] }), web)).toBe(false);
  });

  it("matches a label selector the resource satisfies", () => {
    expect(triggerConcernsResource(sig({ labels: { tier: "web" } }), { ...web, labels: { tier: "web", env: "prod" } })).toBe(true);
    expect(triggerConcernsResource(sig({ labels: { tier: "db" } }), { ...web, labels: { tier: "web" } })).toBe(false);
  });

  it("requires every selector to match, including the resource type", () => {
    expect(triggerConcernsResource(sig({ types: ["router"] }), { name: "gw", type: "router", labels: {} })).toBe(true);
    expect(triggerConcernsResource(sig({ types: ["router"], resources: ["web"] }), web)).toBe(false);
    expect(triggerConcernsResource(sig({ types: ["service"], labels: { tier: "db" } }), { ...web, labels: { tier: "web" } })).toBe(false);
  });

  it("does not match an empty selector or unrelated props", () => {
    expect(triggerConcernsResource(sig({ labels: {} }), { ...web, labels: { a: "b" } })).toBe(false);
    expect(triggerConcernsResource(sig({ name: "web" }), web)).toBe(false);
  });
});

describe("policyRelation", () => {
  const target = (type: string, resource: string | null) => ({ node: type, type, resource });

  it("prefers the resolved targets", () => {
    const p = { targets: [target("lifecycle.deploy", "web"), target("lifecycle.rollback", "web"), target("lifecycle.deploy", "api")], triggers: [] };
    expect(policyRelation(p, web)).toEqual({ kind: "acts", types: ["lifecycle.deploy", "lifecycle.rollback"] });
  });

  it("falls back to the trigger filter", () => {
    expect(policyRelation({ targets: [target("lifecycle.restart", null)], triggers: [sig({ resources: ["web"] })] }, web)).toEqual({ kind: "dynamic" });
    expect(policyRelation({ targets: [], triggers: [sig({ resources: ["web"] })] }, web)).toEqual({ kind: "watches" });
  });

  it("ignores policies that neither act on nor listen to the resource", () => {
    expect(policyRelation({ targets: [target("lifecycle.deploy", "api")], triggers: [] }, web)).toBeNull();
    expect(policyRelation({ targets: [target("lifecycle.restart", null)], triggers: [sig({ resources: ["api"] })] }, web)).toBeNull();
  });
});
