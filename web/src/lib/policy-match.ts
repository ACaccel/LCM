import type { Labels, PolicySummary, ResourceTypeName, Trigger } from "../api/types";

/** The fields of a resource that trigger filters select on. */
export interface ResourceRef {
  name: string;
  type: ResourceTypeName;
  labels: Labels;
}

function mentions(value: unknown, name: string): boolean {
  if (typeof value === "string") return value === name;
  if (Array.isArray(value)) return value.some((v) => mentions(v, name));
  return false;
}

/**
 * Whether a trigger listens to a resource. Trigger props are static, and
 * the resource selectors (`resource`/`resources` by name, `types`, and a
 * `labels` selector) all have to match, as the server combines them; a
 * trigger without any resource selector is not attributed to one resource.
 */
export function triggerConcernsResource(trigger: Trigger, r: ResourceRef): boolean {
  let selectors = 0;
  for (const [key, value] of Object.entries(trigger.props ?? {})) {
    if (key === "labels" && value && typeof value === "object" && !Array.isArray(value)) {
      const sel = Object.entries(value as Record<string, unknown>);
      if (sel.length === 0) continue;
      selectors++;
      if (!sel.every(([k, v]) => r.labels[k] === v)) return false;
      continue;
    }
    if (key === "resource" || key === "resources") {
      if (Array.isArray(value) && value.length === 0) continue;
      selectors++;
      if (!mentions(value, r.name)) return false;
      continue;
    }
    if (key === "types" && Array.isArray(value) && value.length > 0) {
      selectors++;
      if (!mentions(value, r.type)) return false;
    }
  }
  return selectors > 0;
}

export type PolicyRelation =
  /** Nodes of the policy act on the resource; `types` are their node types. */
  | { kind: "acts"; types: string[] }
  /** A trigger listens to the resource and some node picks its resource at run time. */
  | { kind: "dynamic" }
  /** A trigger listens to the resource; no node acts on it. */
  | { kind: "watches" };

/**
 * How a deployed policy relates to a resource, from the targets the server
 * resolved and the trigger filters; null when it does not concern it.
 */
export function policyRelation(policy: Pick<PolicySummary, "targets" | "triggers">, r: ResourceRef): PolicyRelation | null {
  const acting = policy.targets.filter((t) => t.resource === r.name);
  if (acting.length > 0) return { kind: "acts", types: [...new Set(acting.map((t) => t.type))] };
  if (!policy.triggers.some((t) => triggerConcernsResource(t, r))) return null;
  return policy.targets.some((t) => t.resource === null) ? { kind: "dynamic" } : { kind: "watches" };
}
