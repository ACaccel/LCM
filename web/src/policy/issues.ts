import type { Issue } from "../api/types";
import type { PolicyModel, Position } from "./model";

export type PathToken = string | number;

/** Splits a server path such as `spec.nodes[3].next.true[0]` into tokens. */
export function tokenizePath(path: string): PathToken[] {
  const out: PathToken[] = [];
  for (const m of path.matchAll(/([^.[\]]+)|\[(\d+)\]/g)) {
    if (m[2] !== undefined) out.push(Number(m[2]));
    else if (m[1] !== undefined) out.push(m[1]);
  }
  return out;
}

export interface MappedIssue {
  issue: Issue;
  /** Id of the node the issue belongs to; undefined for document-level issues. */
  nodeId?: string;
  /** Location inside the node: ["props", "cases", 1, "when"], ["next", "true", 0], ["id"]... */
  field: PathToken[];
}

export interface IssueMap {
  byNode: Map<string, MappedIssue[]>;
  /** Issues outside any node (metadata, spec.concurrency, ...) and nodes that do not exist. */
  document: MappedIssue[];
  all: MappedIssue[];
}

/** Assigns each issue to the node its path points into. */
export function mapIssues(issues: readonly Issue[], model: PolicyModel | undefined): IssueMap {
  const byNode = new Map<string, MappedIssue[]>();
  const document: MappedIssue[] = [];
  const all: MappedIssue[] = [];
  const idAt = new Map(model?.nodes.map((n) => [n.index, n.id]));
  for (const issue of issues) {
    const tokens = tokenizePath(issue.path);
    let mapped: MappedIssue = { issue, field: tokens };
    if (tokens[0] === "spec" && tokens[1] === "nodes" && typeof tokens[2] === "number") {
      const nodeId = idAt.get(tokens[2]);
      if (nodeId !== undefined) mapped = { issue, nodeId, field: tokens.slice(3) };
    }
    all.push(mapped);
    if (mapped.nodeId === undefined) {
      document.push(mapped);
      continue;
    }
    const list = byNode.get(mapped.nodeId);
    if (list) list.push(mapped);
    else byNode.set(mapped.nodeId, [mapped]);
  }
  return { byNode, document, all };
}

function startsWith(field: readonly PathToken[], prefix: readonly PathToken[]): boolean {
  return prefix.length <= field.length && prefix.every((t, i) => field[i] === t);
}

/** Issues of a node that are located exactly at, or below, a field path. */
export function issuesAt(node: readonly MappedIssue[] | undefined, prefix: readonly PathToken[]): MappedIssue[] {
  return (node ?? []).filter((m) => startsWith(m.field, prefix));
}

/** Issues located exactly at a field path (not below it). */
export function issuesExactlyAt(node: readonly MappedIssue[] | undefined, path: readonly PathToken[]): MappedIssue[] {
  return (node ?? []).filter((m) => m.field.length === path.length && startsWith(m.field, path));
}

/** Layered left-to-right layout for nodes without a stored position. */
export function autoLayout(model: Pick<PolicyModel, "nodes" | "edges">, gapX = 280, gapY = 120): Record<string, Position> {
  const incoming = new Map<string, string[]>();
  const outgoing = new Map<string, string[]>();
  for (const n of model.nodes) {
    incoming.set(n.id, []);
    outgoing.set(n.id, []);
  }
  for (const e of model.edges) {
    if (e.from === e.to) continue;
    incoming.get(e.to)?.push(e.from);
    outgoing.get(e.from)?.push(e.to);
  }
  // Longest path from the roots; Kahn's algorithm, so cycles cannot hang it.
  const depth = new Map<string, number>();
  const pending = new Map<string, number>();
  const queue: string[] = [];
  for (const n of model.nodes) {
    const count = incoming.get(n.id)?.length ?? 0;
    pending.set(n.id, count);
    if (count === 0) {
      depth.set(n.id, 0);
      queue.push(n.id);
    }
  }
  for (let i = 0; i < queue.length; i++) {
    const id = queue[i] as string;
    for (const to of outgoing.get(id) ?? []) {
      depth.set(to, Math.max(depth.get(to) ?? 0, (depth.get(id) ?? 0) + 1));
      const left = (pending.get(to) ?? 0) - 1;
      pending.set(to, left);
      if (left === 0) queue.push(to);
    }
  }
  const rows = new Map<number, number>();
  const out: Record<string, Position> = {};
  for (const n of model.nodes) {
    const d = depth.get(n.id) ?? 0;
    const row = rows.get(d) ?? 0;
    rows.set(d, row + 1);
    out[n.id] = { x: d * gapX, y: row * gapY };
  }
  return out;
}

/**
 * A free spot for a new node: to the right of the anchor (or of the
 * rightmost node), moved down until it overlaps nothing.
 */
export function nextPosition(positions: Readonly<Record<string, Position>>, anchor?: string, gapX = 280, gapY = 120): Position {
  const all = Object.values(positions);
  const base = anchor ? positions[anchor] : undefined;
  const rightmost = all.reduce<Position | undefined>((m, p) => (!m || p.x > m.x ? p : m), undefined);
  let x = base ? base.x + gapX : rightmost ? rightmost.x + gapX : 0;
  const y = base ? base.y : (rightmost?.y ?? 0);
  if (!base && !rightmost) x = 0;
  let cand: Position = { x, y };
  const clash = (c: Position) => all.some((p) => Math.abs(p.x - c.x) < gapX * 0.7 && Math.abs(p.y - c.y) < gapY * 0.8);
  for (let i = 0; i < 500 && clash(cand); i++) cand = { x, y: y + (i + 1) * gapY };
  return cand;
}
