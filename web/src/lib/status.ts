import type { Health, NodeStatus, ResourcePhase, RunStatus } from "../api/types";
import { s } from "../strings";

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
  const label = s.runStatus[st] ?? st;
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
  const label = s.nodeStatus[st] ?? st;
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
