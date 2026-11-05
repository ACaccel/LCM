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
  /**
   * For an edge that spans more than one column: the y of the gutter it runs
   * along, so it passes between the rows of the columns in between instead of
   * through the nodes on its straight line.
   */
  lane?: number;
}

export interface Topology {
  nodes: TopoNode[];
  edges: TopoEdge[];
}

export const COL_W = 250;
export const ROW_H = 78;
/** Size of a drawn node; the gutter between two rows is ROW_H - NODE_H high. */
export const NODE_W = 168;
export const NODE_H = 48;
/** How far an edge runs straight out of and into a node before it bends. */
export const STUB = 20;
/** Distance between edges that share a gutter. */
const LANE_STEP = 4;

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
  return { nodes: placed, edges: routeLongEdges(edges, depths, row) };
}

/**
 * Edges that skip a column would run straight through the nodes of the
 * columns in between when both ends sit on the same row. They get a lane in
 * the gutter below (or above, towards a higher target) their source row;
 * gutters are free of nodes in every column. Edges sharing a gutter are
 * spread a few pixels apart.
 */
function routeLongEdges(edges: readonly TopoEdge[], depths: ReadonlyMap<string, number>, row: ReadonlyMap<string, number>): TopoEdge[] {
  const gutter = (ROW_H - NODE_H) / 2;
  const used = new Map<number, number>();
  return edges.map((e) => {
    const span = (depths.get(e.from) ?? 0) - (depths.get(e.to) ?? 0);
    if (span <= 1) return { ...e };
    const from = row.get(e.from) ?? 0;
    const up = (row.get(e.to) ?? 0) < from;
    const base = up ? from * ROW_H - gutter : from * ROW_H + NODE_H + gutter;
    const n = used.get(base) ?? 0;
    used.set(base, n + 1);
    // Alternate around the middle of the gutter: 0, +1, -1, +2, ...
    const offset = (n % 2 === 1 ? (n + 1) / 2 : -n / 2) * LANE_STEP;
    return { ...e, lane: base + (up ? -offset : offset) };
  });
}

/** Corner points of an edge from the right side of a node to the left side of another. */
export function edgeRoute(source: { x: number; y: number }, target: { x: number; y: number }, lane?: number): { x: number; y: number }[] {
  if (lane === undefined) return [source, target];
  return [source, { x: source.x + STUB, y: source.y }, { x: source.x + STUB, y: lane }, { x: target.x - STUB, y: lane }, { x: target.x - STUB, y: target.y }, target];
}

/** An SVG path through the points with rounded corners. */
export function roundedPath(points: readonly { x: number; y: number }[], radius = 8): string {
  const [first, ...rest] = points;
  if (!first) return "";
  let d = `M ${first.x} ${first.y}`;
  rest.forEach((p, i) => {
    const prev = points[i];
    const next = points[i + 2];
    if (!prev) return;
    if (!next) {
      d += ` L ${p.x} ${p.y}`;
      return;
    }
    const inLen = Math.hypot(p.x - prev.x, p.y - prev.y);
    const outLen = Math.hypot(next.x - p.x, next.y - p.y);
    const r = Math.min(radius, inLen / 2, outLen / 2);
    if (r <= 0) {
      d += ` L ${p.x} ${p.y}`;
      return;
    }
    const a = { x: p.x - ((p.x - prev.x) / inLen) * r, y: p.y - ((p.y - prev.y) / inLen) * r };
    const b = { x: p.x + ((next.x - p.x) / outLen) * r, y: p.y + ((next.y - p.y) / outLen) * r };
    d += ` L ${a.x} ${a.y} Q ${p.x} ${p.y} ${b.x} ${b.y}`;
  });
  return d;
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
