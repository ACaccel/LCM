import type { Severity } from "../api/security";
import type { Action, Backup, CheckStatus, Deployment, Health, NodeStatus, PolicySummary, ResourcePhase, RouterSnapshot, RouterStatus, RunStatus, TimelineEntry } from "../api/types";
import { s, sec } from "../strings";
import { timelineField } from "./timeline";

/** Visual tone of a badge. Every tone is paired with an icon and text. */
export type Tone = "ok" | "warn" | "bad" | "info" | "neutral";

export interface StatusView {
  tone: Tone;
  icon: string;
  label: string;
}

export function healthView(h: Health): StatusView {
  switch (h) {
    case "healthy":
      return { tone: "ok", icon: "✓", label: s.health.healthy };
    case "degraded":
      return { tone: "warn", icon: "▲", label: s.health.degraded };
    case "down":
      return { tone: "bad", icon: "✕", label: s.health.down };
    default:
      return { tone: "neutral", icon: "?", label: s.health.unknown };
  }
}

/** A type-specific state: service lifecycle states, or online/offline/unknown for observed resources. */
export function stateView(st: string): StatusView {
  const label = st ? (s.state[st] ?? st) : s.state["unknown"]!;
  switch (st) {
    case "running":
    case "online":
      return { tone: "ok", icon: "●", label };
    case "offline":
      return { tone: "bad", icon: "✕", label };
    case "unknown":
    case "":
      return { tone: "neutral", icon: "?", label };
    case "deploying":
    case "updating":
    case "rolling_back":
      return { tone: "info", icon: "↻", label };
    case "failed":
      return { tone: "bad", icon: "✕", label };
    case "stopped":
      return { tone: "neutral", icon: "■", label };
    default:
      return { tone: "neutral", icon: "○", label };
  }
}

export function phaseView(p: ResourcePhase): StatusView {
  const label = s.phase[p];
  switch (p) {
    case "managed":
      return { tone: "ok", icon: "●", label };
    case "retired":
      return { tone: "warn", icon: "‖", label };
    default:
      return { tone: "neutral", icon: "■", label };
  }
}

export function runStatusView(st: RunStatus): StatusView {
  const label = s.runStatus[st];
  switch (st) {
    case "succeeded":
      return { tone: "ok", icon: "✓", label };
    case "failed":
    case "interrupted":
      return { tone: "bad", icon: "✕", label };
    case "running":
      return { tone: "info", icon: "↻", label };
    case "suspended":
      return { tone: "warn", icon: "‖", label };
    case "queued":
      return { tone: "neutral", icon: "…", label };
    default:
      return { tone: "neutral", icon: "–", label };
  }
}

export function nodeStatusView(st: NodeStatus): StatusView {
  const label = s.nodeStatus[st];
  switch (st) {
    case "completed":
      return { tone: "ok", icon: "✓", label };
    case "failed":
    case "dead":
      return { tone: "bad", icon: "✕", label };
    case "running":
      return { tone: "info", icon: "↻", label };
    case "retrying":
      return { tone: "warn", icon: "↺", label };
    case "suspended":
      return { tone: "warn", icon: "‖", label };
    case "ready":
      return { tone: "neutral", icon: "▷", label };
    default:
      return { tone: "neutral", icon: "○", label };
  }
}

export const RUN_ACTIVE: ReadonlySet<RunStatus> = new Set<RunStatus>(["queued", "running", "suspended"]);

type Glyph = Omit<StatusView, "label">;

/** A stale result is shown as unknown: old data must not look healthy or failed. */
export function checkView(c: Pick<CheckStatus, "status" | "stale">): StatusView {
  if (c.stale) return { tone: "neutral", icon: "?", label: s.checks.stale };
  return { ...checkGlyph[c.status], label: s.checks[c.status] };
}
const checkGlyph: Record<CheckStatus["status"], Glyph> = {
  up: { tone: "ok", icon: "✓" },
  down: { tone: "bad", icon: "✕" },
  unknown: { tone: "neutral", icon: "?" },
};

const deploymentGlyph: Record<Deployment["outcome"], Glyph> = {
  running: { tone: "info", icon: "↻" },
  succeeded: { tone: "ok", icon: "✓" },
  failed: { tone: "bad", icon: "✕" },
  interrupted: { tone: "bad", icon: "✕" },
};
export function deploymentOutcomeView(outcome: Deployment["outcome"]): StatusView {
  return { ...deploymentGlyph[outcome], label: s.deployments.outcomes[outcome] };
}

const routeGlyph: Record<string, Omit<StatusView, "label">> = {
  applied: { tone: "ok", icon: "✓" },
  pending: { tone: "info", icon: "↻" },
  refused: { tone: "bad", icon: "✕" },
  failed: { tone: "bad", icon: "✕" },
  unknown: { tone: "warn", icon: "?" },
  skipped: { tone: "neutral", icon: "○" },
};
/** The outcome of sending a service's route section to the host. */
export function routeStateView(state: string): StatusView {
  return { ...(routeGlyph[state] ?? { tone: "neutral", icon: "○" }), label: s.route.states[state] ?? state };
}

export function backupOutcomeView(outcome: Backup["outcome"]): StatusView {
  return { ...deploymentGlyph[outcome], label: s.backups.outcomes[outcome] };
}

const actionGlyph: Record<Action["status"], StatusView> = {
  running: { tone: "info", icon: "↻", label: s.action.running },
  succeeded: { tone: "ok", icon: "✓", label: s.action.succeeded },
  failed: { tone: "bad", icon: "✕", label: s.action.failed },
};
export function actionStatusView(st: Action["status"]): StatusView {
  return actionGlyph[st];
}

/** Deployment state of a policy: not deployed, paused, or deployed and receiving events. */
export function policyStateView(p: Pick<PolicySummary, "deployed" | "paused">): StatusView {
  if (!p.deployed) return { tone: "neutral", icon: "○", label: s.policies.notDeployed };
  if (p.paused) return { tone: "warn", icon: "‖", label: s.policies.paused };
  return { tone: "ok", icon: "●", label: s.policies.deployed };
}

const routerOutcomeGlyph: Record<RouterSnapshot["outcome"], Glyph> = {
  ok: { tone: "ok", icon: "✓" },
  unreachable: { tone: "bad", icon: "!" },
  "auth-failed": { tone: "bad", icon: "!" },
  unavailable: { tone: "warn", icon: "!" },
  "not-configured": { tone: "warn", icon: "!" },
};
export function routerOutcomeView(outcome: RouterSnapshot["outcome"]): StatusView {
  return { ...routerOutcomeGlyph[outcome], label: s.router.outcomes[outcome] };
}

const reachabilityGlyph: Record<RouterStatus["reachability"], Glyph> = {
  reachable: { tone: "ok", icon: "✓" },
  unreachable: { tone: "bad", icon: "!" },
  unknown: { tone: "neutral", icon: "?" },
};
export function reachabilityView(r: RouterStatus["reachability"]): StatusView {
  return { ...reachabilityGlyph[r], label: s.router.reachability[r] };
}

const severityGlyph: Record<Severity, Glyph> = {
  info: { tone: "info", icon: "i" },
  warning: { tone: "warn", icon: "▲" },
  critical: { tone: "bad", icon: "✕" },
};
export function severityView(severity: Severity): StatusView {
  return { ...severityGlyph[severity], label: sec.severityLabels[severity] };
}

/** Dot colour of a timeline entry; "act" marks an action LCM performed. */
export type TimelineDot = Exclude<Tone, "neutral"> | "act";

export function timelineDot(e: TimelineEntry): TimelineDot {
  const d = e.data ?? {};
  switch (e.kind) {
    case "deployment":
    case "action":
    case "backup":
    case "job":
      return timelineField(d, "outcome") === "failed" ? "bad" : "act";
    case "health.changed": {
      const h = timelineField(d, "health");
      return h === "down" ? "bad" : h === "degraded" ? "warn" : h === "healthy" ? "ok" : "info";
    }
    case "check.changed": {
      const st = timelineField(d, "status");
      return st === "down" ? "bad" : st === "up" ? "ok" : "info";
    }
    case "alert": {
      const sev = timelineField(d, "severity");
      return sev === "critical" ? "bad" : sev === "warning" ? "warn" : "info";
    }
    case "lifecycle.changed":
      return timelineField(d, "state") === "failed" ? "bad" : "info";
    case "observation": {
      const sev = timelineField(d, "severity");
      return sev === "warning" ? "warn" : sev === "error" || sev === "critical" || sev === "alert" || sev === "emergency" ? "bad" : "info";
    }
    default:
      return "info";
  }
}
