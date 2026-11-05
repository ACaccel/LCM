import type { Health, ImpactEntry, Relation, ResourceDetail, ResourcePhase, ResourceSummary, ResourceTypeName } from "../api/types";

export type RelationType = Relation["type"];

export interface TopoNode {
  name: string;
  type: ResourceTypeName | "unknown";
  health: Health;
  phase: ResourcePhase | "unknown";
  /** A relation names this resource but it does not exist. */
  missing: boolean;
  /** Longest chain of relations below it: 0 for a resource that depends on nothing. */
  depth: number;
  x: number;
  y: number;
}

export interface TopoEdge {
  id: string;
  from: string;
  to: string;
  type: RelationType;
}

export interface Topology {
  nodes: TopoNode[];
  edges: TopoEdge[];
}

export const COL_W = 250;
export const ROW_H = 78;

type Known = Pick<ResourceSummary, "name" | "type" | "health" | "phase">;

/** Edges in a stable order, without duplicates; a relation points from the declaring resource to its target. */
export function uniqueEdges(edges: readonly Omit<TopoEdge, "id">[]): TopoEdge[] {
  const seen = new Set<string>();
  const out: TopoEdge[] = [];
  for (const e of edges) {
    const id = `${e.from}\u0000${e.type}\u0000${e.to}`;
    if (e.from === e.to || seen.has(id)) continue;
    seen.add(id);
    out.push({ ...e, id });
  }
  return out;
}

/**
 * Depth of every node: how many resources deep its chain of dependencies
 * goes. A cycle is cut where it closes, so a malformed document cannot hang
 * the page.
 */
export function relationDepths(names: readonly string[], edges: readonly Pick<TopoEdge, "from" | "to">[]): Map<string, number> {
  const targets = new Map<string, string[]>();
  for (const e of edges) targets.set(e.from, [...(targets.get(e.from) ?? []), e.to]);
  const depth = new Map<string, number>();
  const visiting = new Set<string>();
  const visit = (n: string): number => {
    const known = depth.get(n);
    if (known !== undefined) return known;
    if (visiting.has(n)) return 0;
    visiting.add(n);
    let d = 0;
    for (const t of targets.get(n) ?? []) d = Math.max(d, visit(t) + 1);
    visiting.delete(n);
    depth.set(n, d);
    return d;
  };
  for (const n of names) visit(n);
  return depth;
}

/**
 * Layers by dependency depth, dependents on the left and what they rely on
 * on the right so arrows point right. Within a layer nodes are ordered by
 * the average position of their neighbours to keep edges short.
 */
export function layoutTopology(nodes: readonly Omit<TopoNode, "depth" | "x" | "y">[], edges: readonly TopoEdge[]): Topology {
  const depths = relationDepths(
    nodes.map((n) => n.name),
    edges,
  );
  const top = Math.max(0, ...depths.values());
  const layers: string[][] = Array.from({ length: top + 1 }, () => []);
  for (const n of [...nodes].sort((a, b) => a.name.localeCompare(b.name))) layers[depths.get(n.name) ?? 0]?.push(n.name);

  const row = new Map<string, number>();
  const neighbours = new Map<string, string[]>();
  for (const e of edges) {
    neighbours.set(e.from, [...(neighbours.get(e.from) ?? []), e.to]);
    neighbours.set(e.to, [...(neighbours.get(e.to) ?? []), e.from]);
  }
  // The deepest layer is placed first; each layer to its left follows its neighbours.
  for (let d = top; d >= 0; d--) {
    const layer = layers[d] ?? [];
    const score = (n: string) => {
      const placed = (neighbours.get(n) ?? []).flatMap((m) => (row.has(m) ? [row.get(m) ?? 0] : []));
      return placed.length ? placed.reduce((a, b) => a + b, 0) / placed.length : Number.POSITIVE_INFINITY;
    };
    layer.sort((a, b) => score(a) - score(b) || a.localeCompare(b));
    layer.forEach((n, i) => row.set(n, i));
  }
  const placed = nodes.map((n): TopoNode => {
    const depth = depths.get(n.name) ?? 0;
    return { ...n, depth, x: (top - depth) * COL_W, y: (row.get(n.name) ?? 0) * ROW_H };
  });
  return { nodes: placed, edges: [...edges] };
}

/**
 * The whole fleet: every resource, with the relations their documents
 * declare. A relation to a name that is not in the list gets a placeholder
 * node marked missing.
 */
export function buildTopology(resources: readonly Known[], relations: ReadonlyMap<string, readonly Relation[]>, options: { onlyLinked?: boolean } = {}): Topology {
  const byName = new Map(resources.map((r) => [r.name, r]));
  const edges = uniqueEdges(resources.flatMap((r) => (relations.get(r.name) ?? []).map((rel) => ({ from: r.name, to: rel.target, type: rel.type }))));
  const linked = new Set(edges.flatMap((e) => [e.from, e.to]));
  const names = new Set<string>([...(options.onlyLinked ? [] : resources.map((r) => r.name)), ...linked]);
  const nodes = [...names].map((name) => nodeOf(name, byName.get(name)));
  return layoutTopology(nodes, edges);
}

/**
 * The neighbourhood of one resource: what its document relates it to, and
 * the managed resources that depend on it or reach others through it.
 */
export function buildNeighbourhood(detail: Pick<ResourceDetail, "name" | "type" | "document" | "phase">, impact: { dependents: readonly ImpactEntry[]; controlDependents: readonly ImpactEntry[] } | undefined, resources: readonly Known[]): Topology {
  const byName = new Map(resources.map((r) => [r.name, r]));
  const self = detail.name;
  const edges = uniqueEdges([
    ...(detail.document.spec.relations ?? []).map((rel) => ({ from: self, to: rel.target, type: rel.type })),
    ...[...(impact?.dependents ?? []), ...(impact?.controlDependents ?? [])].map((e) => ({ from: e.name, to: e.via, type: e.relation })),
  ]);
  const names = new Set<string>([self, ...edges.flatMap((e) => [e.from, e.to])]);
  const nodes = [...names].map((name) => (name === self ? { ...nodeOf(name, byName.get(name)), type: detail.type, phase: detail.phase, missing: false } : nodeOf(name, byName.get(name))));
  return layoutTopology(nodes, edges);
}

function nodeOf(name: string, known: Known | undefined): Omit<TopoNode, "depth" | "x" | "y"> {
  return known ? { name, type: known.type, health: known.health, phase: known.phase, missing: false } : { name, type: "unknown", health: "unknown", phase: "unknown", missing: true };
}
