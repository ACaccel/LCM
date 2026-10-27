/**
 * namespaces.ts
 *
 * What travels on a connection is one namespace per node that has run, keyed by
 * node name. This module answers the two questions the edge editor needs: which
 * namespaces can reach a given edge, and which one wins when two of them carry
 * the same field name.
 *
 * Pure module, no React import, so it can be unit-tested as-is.
 */

import type { Edge, Node } from 'reactflow';
import type { CanvasNodeData, EdgeData, NodeSchema, RunNodeEvent } from './types';

export type Namespace = {
  /** The node whose output this namespace holds. */
  name: string;
  /** Field names it is known to carry, from the schema or the last run. */
  fields: string[];
};

/** Which namespaces an edge forwards: its explicit list, or all of them. */
export function carriedBy(edge: Edge<EdgeData>, upstream: string[]): string[] {
  const carry = edge.data?.carry ?? [];
  return carry.length > 0 ? carry.filter((name) => upstream.includes(name)) : upstream;
}

/**
 * Namespace names leaving a node: everything it forwards, plus its own.
 *
 * The visited set guards against a cycle. The engine rejects one at deploy time,
 * but a half-drawn canvas can hold one, and recursing forever would blank the
 * editor rather than show the mistake.
 */
function leaving(
  nodeId: string,
  nodes: Node<CanvasNodeData>[],
  edges: Edge<EdgeData>[],
  schemas: Map<string, NodeSchema>,
  visited: Set<string>,
): string[] {
  if (visited.has(nodeId)) return [];
  visited.add(nodeId);

  const names = new Set<string>();
  for (const edge of edges) {
    if (edge.target !== nodeId) continue;
    for (const name of carriedBy(edge, leaving(edge.source, nodes, edges, schemas, visited))) {
      names.add(name);
    }
  }
  if (contributes(nodeId, nodes, schemas)) names.add(nodeId);

  visited.delete(nodeId);
  return [...names];
}

/**
 * Whether a node adds a namespace of its own.
 *
 * Two kinds do not. A boundary pseudo-node is the module's own port rather than
 * a node that runs at all. A passthrough node -- If, Compare, Merge, Wait --
 * runs but emits nothing, because what arrived already travels on. Offering
 * either as a namespace to carry would look like a way to forward its payload
 * while forwarding nothing at all.
 */
function contributes(
  nodeId: string,
  nodes: Node<CanvasNodeData>[],
  schemas: Map<string, NodeSchema>,
): boolean {
  const self = nodes.find((n) => n.id === nodeId);
  if (!self || self.data.boundary) return false;
  return schemas.get(self.data.nodeType)?.passthrough !== true;
}

/**
 * Namespaces available on an edge, in the order the engine folds them.
 *
 * The order is what decides a clash, so it has to match the engine exactly:
 * everything else sorted by name, then the immediately upstream node last.
 */
export function availableNamespaces(
  edge: Edge<EdgeData>,
  nodes: Node<CanvasNodeData>[],
  edges: Edge<EdgeData>[],
  schemas: Map<string, NodeSchema>,
  byNode: Record<string, RunNodeEvent>,
): Namespace[] {
  const producer = edge.source;
  const names = leaving(producer, nodes, edges, schemas, new Set());
  const ordered = names.filter((name) => name !== producer).sort();
  if (names.includes(producer)) ordered.push(producer);

  return ordered.map((name) => ({ name, fields: fieldsOf(name, nodes, schemas, byNode) }));
}

/**
 * Field names a namespace is known to carry.
 *
 * The declared output schema is the design-time answer; the last run is the
 * only answer for a webhook payload or anything a dynamic port forwards, so
 * both are merged rather than one preferred.
 */
function fieldsOf(
  name: string,
  nodes: Node<CanvasNodeData>[],
  schemas: Map<string, NodeSchema>,
  byNode: Record<string, RunNodeEvent>,
): string[] {
  const fields = new Set<string>();
  const node = nodes.find((n) => n.id === name);
  const declared = node ? schemas.get(node.data.nodeType)?.output?.fields ?? [] : [];
  for (const field of declared) fields.add(field.name);

  const event = byNode[name];
  for (const port of event?.output ?? []) {
    for (const key of Object.keys(port ?? {})) fields.add(key);
  }
  return [...fields];
}

/** The namespaces an edge actually forwards, in fold order. */
export function selectedNamespaces(available: Namespace[], carry: string[]): Namespace[] {
  if (carry.length === 0) return available;
  const byName = new Map(available.map((ns) => [ns.name, ns]));
  return carry
    .map((name) => byName.get(name))
    .filter((ns): ns is Namespace => ns !== undefined);
}

export type Clash = {
  field: string;
  /** Namespaces carrying this field, in fold order. */
  from: string[];
  /** The one the receiving node reads, i.e. the last to be folded. */
  winner: string;
};

/**
 * Fields more than one selected namespace carries.
 *
 * Nothing is lost when this happens -- both are still on the wire and a dotted
 * source reaches either -- but the flat view the node reads can only hold one,
 * so the designer is told which before saving rather than after a confusing run.
 */
export function clashes(selected: Namespace[]): Clash[] {
  const owners = new Map<string, string[]>();
  for (const ns of selected) {
    for (const field of ns.fields) {
      owners.set(field, [...(owners.get(field) ?? []), ns.name]);
    }
  }
  const found: Clash[] = [];
  for (const [field, from] of owners) {
    if (from.length > 1) {
      found.push({ field, from, winner: from[from.length - 1] ?? '' });
    }
  }
  return found.sort((a, b) => a.field.localeCompare(b.field));
}

/**
 * Turn a checkbox selection back into a stored carry list.
 *
 * Everything selected is stored as an empty list rather than as the full set:
 * the two mean the same thing to the engine, and the empty form keeps working
 * when a node is added upstream later.
 *
 * That convention is also why the caller must not offer an empty selection: an
 * empty list on the wire reads as "forward everything", so unticking the last
 * namespace would do the opposite of what it looks like. canUntick is what the
 * UI asks before allowing the click.
 */
export function toCarry(available: Namespace[], keep: Set<string>): string[] {
  if (keep.size === 0) return [];
  if (available.every((ns) => keep.has(ns.name))) return [];
  return available.filter((ns) => keep.has(ns.name)).map((ns) => ns.name);
}

/** False for the last remaining namespace; see toCarry. */
export function canUntick(kept: Set<string>): boolean {
  return kept.size > 1;
}

/**
 * Carry entries naming a node that is no longer upstream.
 *
 * Deleting a node leaves its name behind on every downstream edge that listed
 * it. The engine skips what it cannot find, so the run is unaffected, but the
 * stored list keeps growing stale and the checkbox state stops matching it.
 */
export function staleCarry(carry: string[], available: Namespace[]): string[] {
  const known = new Set(available.map((ns) => ns.name));
  return carry.filter((name) => !known.has(name));
}

/**
 * Field names a node can name in a condition, at one input port.
 *
 * Both forms are offered: the bare name that the flattening resolves, and the
 * qualified one for when two upstream nodes emit the same field and only one
 * of them survives the flattening.
 */
export function fieldChoices(selected: Namespace[]): string[] {
  const bare = new Set<string>();
  const qualified: string[] = [];
  for (const ns of selected) {
    for (const field of ns.fields) {
      bare.add(field);
      qualified.push(`${ns.name}.${field}`);
    }
  }
  return [...[...bare].sort(), ...qualified.sort()];
}

/**
 * Namespaces arriving at one input port of a node.
 *
 * The edge feeding that port decides them, so a node with nothing wired to the
 * port has none -- which is the honest answer, not an empty guess.
 */
export function namespacesAtInput(
  nodeId: string,
  port: number,
  nodes: Node<CanvasNodeData>[],
  edges: Edge<EdgeData>[],
  schemas: Map<string, NodeSchema>,
  byNode: Record<string, RunNodeEvent>,
): Namespace[] {
  const feeding = edges.find(
    (e) => e.target === nodeId && portOf(e.targetHandle) === port,
  );
  if (!feeding) return [];
  return selectedNamespaces(
    availableNamespaces(feeding, nodes, edges, schemas, byNode),
    feeding.data?.carry ?? [],
  );
}

/** Port index encoded in a React Flow handle id such as "tgt-1". */
function portOf(handleId: string | null | undefined): number {
  if (!handleId) return 0;
  const index = parseInt(handleId.slice(handleId.lastIndexOf('-') + 1), 10);
  return Number.isNaN(index) ? 0 : index;
}
