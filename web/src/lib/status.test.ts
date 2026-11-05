import { describe, expect, it } from "vitest";
import type { TimelineEntry } from "../api/types";
import { s, sec } from "../strings";
import { actionStatusView, checkView, deploymentOutcomeView, policyStateView, reachabilityView, routerOutcomeView, severityView, timelineDot } from "./status";

const entry = (kind: string, data: Record<string, unknown>): TimelineEntry => ({ id: 1, resource: "web", time: "2026-11-05T00:00:00Z", kind, ref: "", summary: "", data, summaryMessage: { code: "", params: {}, message: "" } });

describe("status views", () => {
  it("shows a stale check as unknown whatever its last result", () => {
    expect(checkView({ status: "up", stale: false })).toEqual({ tone: "ok", icon: "✓", label: s.checks.up });
    expect(checkView({ status: "down", stale: false }).tone).toBe("bad");
    expect(checkView({ status: "down", stale: true })).toEqual({ tone: "neutral", icon: "?", label: s.checks.stale });
    expect(checkView({ status: "unknown", stale: false }).tone).toBe("neutral");
  });

  it("maps deployment, action and policy states", () => {
    expect(deploymentOutcomeView("interrupted")).toEqual({ tone: "bad", icon: "✕", label: s.deployments.outcomes.interrupted });
    expect(deploymentOutcomeView("running").tone).toBe("info");
    expect(actionStatusView("succeeded").tone).toBe("ok");
    expect(actionStatusView("failed").label).toBe(s.action.failed);
    expect(policyStateView({ deployed: false, paused: true }).label).toBe(s.policies.notDeployed);
    expect(policyStateView({ deployed: true, paused: true }).tone).toBe("warn");
    expect(policyStateView({ deployed: true, paused: false }).tone).toBe("ok");
  });

  it("maps router outcomes, reachability and incident severity", () => {
    expect(routerOutcomeView("auth-failed").tone).toBe("bad");
    expect(routerOutcomeView("not-configured").tone).toBe("warn");
    expect(reachabilityView("unknown")).toEqual({ tone: "neutral", icon: "?", label: s.router.reachability.unknown });
    expect(severityView("critical")).toEqual({ tone: "bad", icon: "✕", label: sec.severityLabels.critical });
    expect(severityView("warning").tone).toBe("warn");
    expect(severityView("info").tone).toBe("info");
  });

  it("colours timeline entries, including data nested by signals", () => {
    expect(timelineDot(entry("deployment", { outcome: "failed" }))).toBe("bad");
    expect(timelineDot(entry("action", { outcome: "succeeded" }))).toBe("act");
    expect(timelineDot(entry("health.changed", { data: { health: "degraded" } }))).toBe("warn");
    expect(timelineDot(entry("check.changed", { status: "up" }))).toBe("ok");
    expect(timelineDot(entry("alert", { severity: "critical" }))).toBe("bad");
    expect(timelineDot(entry("other", {}))).toBe("info");
  });
});
