import type { NodeStatus, RunNode } from "../api/types";
import type { PolicyModel } from "./model";

/**
 * Status of each node drawn in the graph. A module node expands into nodes
 * with scoped ids (`outer/inner/check`); when the run has no entry of its own
 * for the module node, it takes the most significant status of its parts.
 */
export function statusByTopLevelNode(model: PolicyModel, nodes: readonly RunNode[]): Map<string, NodeStatus> {
  const out = new Map<string, NodeStatus>();
  const direct = new Map(nodes.map((n) => [n.id, n.status]));
  const rank: NodeStatus[] = ["failed", "dead", "running", "retrying", "suspended", "ready", "pending", "completed"];
  for (const n of model.nodes) {
    const own = direct.get(n.id);
    if (own) {
      out.set(n.id, own);
      continue;
    }
    const parts = nodes.filter((r) => r.id.startsWith(`${n.id}/`)).map((r) => r.status);
    if (parts.length === 0) continue;
    if (parts.every((p) => p === "completed")) out.set(n.id, "completed");
    else out.set(n.id, rank.find((r) => parts.includes(r)) ?? "pending");
  }
  return out;
}

/** Edges whose source took their port, for highlighting the path a run followed. */
export function takenEdgeIds(model: PolicyModel, nodes: readonly RunNode[]): Set<string> {
  const port = new Map(nodes.filter((n) => n.port).map((n) => [n.id, n.port as string]));
  return new Set(model.edges.filter((e) => port.get(e.from) === e.port).map((e) => e.id));
}
