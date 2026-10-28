import type { Labels, PolicySummary, Trigger } from "../api/types";

function mentions(value: unknown, name: string): boolean {
  if (typeof value === "string") return value === name;
  if (Array.isArray(value)) return value.some((v) => mentions(v, name));
  return false;
}

/**
 * Best-effort guess whether a trigger concerns a service: a prop that lists
 * or equals the service name (such as `services` or `service`), or a
 * `labels` selector that the service's labels satisfy. Policies can also
 * reach services through expressions; those cannot be seen here.
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

/**
 * Whether a policy concerns a service: its `service` label (set by the
 * built-in templates) names it, or one of its triggers does.
 */
export function policyConcernsService(policy: Pick<PolicySummary, "labels" | "triggers">, service: string, labels: Labels): boolean {
  if (policy.labels["service"] === service) return true;
  return policy.triggers.some((t) => triggerConcernsService(t, service, labels));
}
