import { describe, expect, it } from "vitest";
import type { Trigger } from "../api/types";
import { policyConcernsService, triggerConcernsService } from "./policy-match";

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

describe("policyConcernsService", () => {
  it("matches the service label or a trigger", () => {
    expect(policyConcernsService({ labels: { service: "web" }, triggers: [] }, "web", {})).toBe(true);
    expect(policyConcernsService({ labels: {}, triggers: [sig({ services: ["web"] })] }, "web", {})).toBe(true);
    expect(policyConcernsService({ labels: { service: "api" }, triggers: [] }, "web", {})).toBe(false);
  });
});
