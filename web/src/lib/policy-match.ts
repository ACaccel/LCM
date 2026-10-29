import type { Labels, PolicySummary, Trigger } from "../api/types";

function mentions(value: unknown, name: string): boolean {
  if (typeof value === "string") return value === name;
  if (Array.isArray(value)) return value.some((v) => mentions(v, name));
  return false;
}

/**
 * Whether a trigger listens to a service: a prop that lists or equals the
 * service name (such as `services` or `service`), or a `labels` selector
 * that the service's labels satisfy. Trigger props are always static.
 */
export function triggerConcernsService(trigger: Trigger, service: string, labels: Labels): boolean {
  for (const [key, value] of Object.entries(trigger.props ?? {})) {
    if (key === "labels" && value && typeof value === "object" && !Array.isArray(value)) {
      const sel = Object.entries(value as Record<string, unknown>);
      if (sel.length > 0 && sel.every(([k, v]) => labels[k] === v)) return true;
      continue;
    }
    if (key === "service" || key === "services") {
      if (mentions(value, service)) return true;
    }
  }
  return false;
}

export type PolicyRelation =
  /** Nodes of the policy act on the service; `types` are their node types. */
  | { kind: "acts"; types: string[] }
  /** A trigger listens to the service and some node picks its service at run time. */
  | { kind: "dynamic" }
  /** A trigger listens to the service; no node acts on it. */
  | { kind: "watches" };

/**
 * How a deployed policy relates to a service, from the targets the server
 * resolved and the trigger filters; null when it does not concern it.
 */
export function policyRelation(policy: Pick<PolicySummary, "targets" | "triggers">, service: string, labels: Labels): PolicyRelation | null {
  const acting = policy.targets.filter((t) => t.service === service);
  if (acting.length > 0) return { kind: "acts", types: [...new Set(acting.map((t) => t.type))] };
  if (!policy.triggers.some((t) => triggerConcernsService(t, service, labels))) return null;
  return policy.targets.some((t) => t.service === null) ? { kind: "dynamic" } : { kind: "watches" };
}
