import { describe, expect, it } from "vitest";
import type { TimelineEntry } from "../api/types";
import { s, sec } from "../strings";
import { RUN_ACTIVE, actionStatusView, checkView, deploymentOutcomeView, healthView, nodeStatusView, phaseView, policyStateView, reachabilityView, routerOutcomeView, runStatusView, severityView, stateView, timelineDot } from "./status";

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

describe("health, state and phase views", () => {
  it("pairs every health with an icon and text, and treats anything else as unknown", () => {
    expect(healthView("healthy")).toEqual({ tone: "ok", icon: "✓", label: s.health.healthy });
    expect(healthView("degraded").tone).toBe("warn");
    expect(healthView("down")).toEqual({ tone: "bad", icon: "✕", label: s.health.down });
    expect(healthView("unknown").tone).toBe("neutral");
    expect(healthView("surprise" as never)).toEqual({ tone: "neutral", icon: "?", label: s.health.unknown });
  });

  it("maps service lifecycle and observed states", () => {
    expect(stateView("running").tone).toBe("ok");
    expect(stateView("online").tone).toBe("ok");
    expect(stateView("offline").tone).toBe("bad");
    expect(stateView("failed").tone).toBe("bad");
    for (const transient of ["deploying", "updating", "rolling_back"]) expect(stateView(transient).tone).toBe("info");
    expect(stateView("stopped")).toMatchObject({ tone: "neutral", icon: "■" });
  });

  it("shows an empty state as unknown and an unlisted state under its own name", () => {
    expect(stateView("")).toEqual({ tone: "neutral", icon: "?", label: s.state["unknown"] });
    expect(stateView("hibernating")).toEqual({ tone: "neutral", icon: "○", label: "hibernating" });
  });

  it("marks retired resources as a warning and archives as neutral", () => {
    expect(phaseView("managed").tone).toBe("ok");
    expect(phaseView("retired")).toEqual({ tone: "warn", icon: "‖", label: s.phase.retired });
    expect(phaseView("unmanaged").tone).toBe("neutral");
  });
});

describe("run and node statuses", () => {
  it("colours run outcomes and keeps waiting runs apart from finished ones", () => {
    expect(runStatusView("succeeded").tone).toBe("ok");
    expect(runStatusView("failed").tone).toBe("bad");
    expect(runStatusView("interrupted").tone).toBe("bad");
    expect(runStatusView("running").tone).toBe("info");
    expect(runStatusView("suspended").tone).toBe("warn");
    expect(runStatusView("queued")).toMatchObject({ tone: "neutral", icon: "…" });
    expect(runStatusView("cancelled")).toMatchObject({ tone: "neutral", icon: "–" });
  });

  it("treats exactly the runs that can still change as active", () => {
    expect([...RUN_ACTIVE].sort()).toEqual(["queued", "running", "suspended"]);
    expect(RUN_ACTIVE.has("succeeded")).toBe(false);
  });

  it("separates retrying and suspended nodes from failed ones", () => {
    expect(nodeStatusView("completed").tone).toBe("ok");
    expect(nodeStatusView("failed").tone).toBe("bad");
    expect(nodeStatusView("dead").tone).toBe("bad");
    expect(nodeStatusView("retrying")).toMatchObject({ tone: "warn", icon: "↺" });
    expect(nodeStatusView("suspended").tone).toBe("warn");
    expect(nodeStatusView("running").tone).toBe("info");
    expect(nodeStatusView("ready").tone).toBe("neutral");
  });
});

describe("timeline dots by kind", () => {
  it("colours health and check changes by their outcome", () => {
    expect(timelineDot(entry("health.changed", { health: "down" }))).toBe("bad");
    expect(timelineDot(entry("health.changed", { health: "healthy" }))).toBe("ok");
    expect(timelineDot(entry("health.changed", { health: "unknown" }))).toBe("info");
    expect(timelineDot(entry("check.changed", { status: "down" }))).toBe("bad");
    expect(timelineDot(entry("check.changed", { status: "unknown" }))).toBe("info");
  });

  it("colours alerts, lifecycle changes and observations by severity or state", () => {
    expect(timelineDot(entry("alert", { severity: "warning" }))).toBe("warn");
    expect(timelineDot(entry("alert", { severity: "info" }))).toBe("info");
    expect(timelineDot(entry("lifecycle.changed", { state: "failed" }))).toBe("bad");
    expect(timelineDot(entry("lifecycle.changed", { state: "running" }))).toBe("info");
    expect(timelineDot(entry("observation", { severity: "warning" }))).toBe("warn");
    for (const severity of ["error", "critical", "alert", "emergency"]) expect(timelineDot(entry("observation", { severity }))).toBe("bad");
    expect(timelineDot(entry("observation", { severity: "notice" }))).toBe("info");
  });

  it("marks actions LCM took as its own unless they failed", () => {
    for (const kind of ["backup", "job", "deployment"]) {
      expect(timelineDot(entry(kind, { outcome: "succeeded" }))).toBe("act");
      expect(timelineDot(entry(kind, { outcome: "failed" }))).toBe("bad");
    }
  });
});
