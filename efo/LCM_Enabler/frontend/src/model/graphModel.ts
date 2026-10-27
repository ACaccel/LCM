/**
 * graphModel.ts
 *
 * Bidirectional mapping between the React Flow graph representation and the
 * PolicyConfig domain model.
 *
 * Design:
 *  - Each React Flow node carries a CanvasNodeData payload that mirrors a
 *    NodeConfig. The node id is used as the node name everywhere.
 *  - Each React Flow edge carries an EdgeData payload (transFormat + carry) as
 *    its data. The source/target handles encode the port indices using the
 *    convention `src-<portIndex>` and `tgt-<portIndex>`.
 *  - Trigger nodes (numInputPorts === 0) are stored in PolicyConfig.trigger[].
 *    All others go into PolicyConfig.node[].
 *  - Round-trips are stable: open → save → open produces the same graph.
 */

import type { Node, Edge } from 'reactflow';
import type {
  PolicyConfig,
  NodeConfig,
  ConnectionUnit,
  EdgeData,
  TransFormatItem,
  CanvasNodeData,
  CanvasPoint,
  ModuleConfig,
  NodeSchema,
} from './types';
import { getPortConfig } from './nodePortInfo';
import {
  collectLayout,
  computePosition,
  nextPortSlot,
  placeBoundaryPorts,
  placeNodes,
  readLayout,
} from './layout';

// ─── React Flow → PolicyConfig ────────────────────────────────────────────────

/** Extract a port index from a handle id string like "src-2" or "tgt-0". */
export function parseHandleIndex(handleId: string | null | undefined): number {
  if (!handleId) return 0;
  const parts = handleId.split('-');
  const idx = parseInt(parts[parts.length - 1] ?? '0', 10);
  return isNaN(idx) ? 0 : idx;
}

/**
 * Convert one canvas edge to the stored connection.
 *
 * knownNodes prunes carry entries naming a node the graph no longer has. The
 * engine already skips what it cannot find, so this is about the stored config
 * not accumulating names that mean nothing.
 */
function edgeToConnection(e: Edge<EdgeData>, knownNodes: Set<string>): ConnectionUnit {
  return {
    nodePair: [e.source, e.target] as [string, string],
    portPair: [
      parseHandleIndex(e.sourceHandle),
      parseHandleIndex(e.targetHandle),
    ] as [number, number],
    transFormat: e.data?.transFormat ?? [],
    carry: (e.data?.carry ?? []).filter((name) => knownNodes.has(name)),
  };
}

/** Convert the React Flow graph to a PolicyConfig ready to POST/PUT. */
export function flowToPolicyConfig(
  nodes: Node<CanvasNodeData>[],
  edges: Edge<EdgeData>[],
  policyName: string,
): PolicyConfig {
  const triggers: NodeConfig[] = [];
  const fnNodes: NodeConfig[] = [];

  for (const n of nodes) {
    const cfg: NodeConfig = {
      name: n.id,
      type: n.data.nodeType,
      property: { ...n.data.property },
    };
    if (n.data.kind === 'trigger') {
      triggers.push(cfg);
    } else {
      fnNodes.push(cfg);
    }
  }

  const known = new Set(nodes.map((n) => n.id));
  const paths: ConnectionUnit[] = edges.map((e) => edgeToConnection(e, known));

  return {
    name: policyName,
    trigger: triggers,
    node: fnNodes,
    path: paths,
    ui: collectLayout(nodes),
  };
}

// ─── PolicyConfig → React Flow ────────────────────────────────────────────────

/**
 * Coerces a stored mapping list into well-formed rows.
 *
 * Everything downstream — the inputs, the status chip, the validation — assumes
 * two strings per row. Normalising once here means a stored value that is not
 * that (a mistyped YAML key, a wire-shape change) shows up as a blank field to
 * fix rather than as a crash inside a render.
 */
/**
 * Coerce a stored carry list to strings.
 *
 * Same reason normalizeTransFormat exists: a hand-written YAML can put anything
 * here, and a non-string reaching the checkbox list would throw during render
 * and unmount the editor.
 */
function normalizeCarry(names: string[] | undefined): string[] {
  if (!Array.isArray(names)) return [];
  return names.filter((name): name is string => typeof name === 'string');
}

function normalizeTransFormat(rows: TransFormatItem[] | undefined): TransFormatItem[] {
  if (!Array.isArray(rows)) return [];
  return rows.map((row) => ({
    src: typeof row?.src === 'string' ? row.src : '',
    dst: typeof row?.dst === 'string' ? row.dst : '',
  }));
}

/**
 * Convert a stored PolicyConfig back to React Flow nodes + edges.
 *
 * `schemas` and `modules` decide each node's port count. Getting the count too
 * low is destructive rather than cosmetic: the missing handle makes React Flow
 * drop every edge attached to it, and saving the canvas then writes the policy
 * back without those connections.
 */
export function policyConfigToFlow(
  policy: PolicyConfig,
  schemas?: Map<string, NodeSchema>,
  modules?: ModuleConfig[],
): {
  nodes: Node<CanvasNodeData>[];
  edges: Edge<EdgeData>[];
} {
  // A Module node's ports come from the module it instantiates, which its
  // property names and which falls back to the node's own name. Resolving it
  // the way the engine does is what lets one module appear twice in a policy
  // under names of the designer's choosing.
  const moduleByName = new Map((modules ?? []).map((m) => [m.name, m]));
  const allNodeConfigs: { cfg: NodeConfig; kind: 'trigger' | 'function' }[] = [
    ...policy.trigger.map((cfg) => ({ cfg, kind: 'trigger' as const })),
    ...policy.node.map((cfg) => ({ cfg, kind: 'function' as const })),
  ];

  const placed = placeNodes(
    allNodeConfigs.map(({ cfg }) => cfg.name),
    readLayout(policy.ui),
  );

  const nodes: Node<CanvasNodeData>[] = allNodeConfigs.map(({ cfg, kind }, i) => {
    const portCfg = getPortConfig(cfg.type, schemas, moduleByName.get(moduleOfNode(cfg)));
    return {
      id: cfg.name,
      type: 'lcmNode',
      position: placed.get(cfg.name) ?? computePosition(i, allNodeConfigs.length),
      data: {
        label: cfg.name,
        nodeType: cfg.type,
        kind,
        property: { ...(cfg.property ?? {}) },
        numInputPorts: portCfg.numInputPorts,
        numOutputPorts: portCfg.numOutputPorts,
        outputLabels: portCfg.outputLabels,
      },
    };
  });

  const edges: Edge<EdgeData>[] = policy.path.map(
    (conn, i) => ({
      id: `e-${i}-${conn.nodePair[0]}-${conn.nodePair[1]}`,
      source: conn.nodePair[0],
      target: conn.nodePair[1],
      sourceHandle: `src-${conn.portPair[0]}`,
      targetHandle: `tgt-${conn.portPair[1]}`,
      data: {
        transFormat: normalizeTransFormat(conn.transFormat),
        carry: normalizeCarry(conn.carry),
      },
      type: 'mapping',
      animated: false,
    }),
  );

  return { nodes, edges };
}

/** Build an empty PolicyConfig stub for a brand-new policy. */
export function emptyPolicyConfig(name: string): PolicyConfig {
  return { name, trigger: [], node: [], path: [] };
}

// ─── Module ⇄ React Flow ──────────────────────────────────────────────────────

/*
 * A module is edited on the same canvas as a policy. The two differences are
 * that it has no triggers, and that its own ports appear as pseudo-nodes: an
 * edge from input pseudo-node 0 to an inner node IS the inPort binding, so the
 * boundary is wired by dragging rather than filled into a separate form.
 */

/**
 * Prefix marking a boundary pseudo-node.
 *
 * The colon keeps it out of the space of names anyone would type, which matters
 * because inner nodes are serialised by canvas id: a real node colliding with a
 * pseudo-node id would write a corrupted inPort.nodeName rather than fail.
 */
export const BOUNDARY_PREFIX = '__';

export function boundaryNodeId(side: 'in' | 'out', port: number): string {
  return `${BOUNDARY_PREFIX}${side}:${port}`;
}

export function isBoundaryId(id: string): boolean {
  return id.startsWith(BOUNDARY_PREFIX);
}

/** Build an empty ModuleConfig stub for a brand-new module. */
export function emptyModuleConfig(name: string): ModuleConfig {
  return { name, numInPort: 1, numOutPort: 1, inPort: [], outPort: [], node: [], path: [] };
}

function boundaryNode(
  side: 'in' | 'out',
  port: number,
  position: CanvasPoint,
): Node<CanvasNodeData> {
  const id = boundaryNodeId(side, port);
  return {
    id,
    type: 'moduleBoundary',
    // Not deletable from the canvas. A module's ports are a positional arity,
    // so removing one in the middle renumbers every higher port -- which
    // silently reinterprets the edges of every policy holding an instance, and
    // the server cannot detect it (its guard only catches an index that no
    // longer exists). The toolbar adds and removes the last port instead.
    deletable: false,
    position,
    data: {
      label: `${side === 'in' ? 'input' : 'output'} ${port}`,
      nodeType: side === 'in' ? 'ModuleInput' : 'ModuleOutput',
      kind: 'function',
      property: {},
      // A boundary faces the graph: the module's input feeds inner nodes, so on
      // the canvas it is a source.
      numInputPorts: side === 'in' ? 0 : 1,
      numOutputPorts: side === 'in' ? 1 : 0,
      boundary: { side, port },
    },
  };
}

/** Convert a stored ModuleConfig to React Flow nodes + edges. */
export function moduleConfigToFlow(
  module: ModuleConfig,
  schemas?: Map<string, NodeSchema>,
  modules?: ModuleConfig[],
): { nodes: Node<CanvasNodeData>[]; edges: Edge<EdgeData>[] } {
  const moduleByName = new Map((modules ?? []).map((m) => [m.name, m]));
  const stored = readLayout(module.ui);
  const placed = placeNodes(module.node.map((cfg) => cfg.name), stored);
  const inner = module.node.map((cfg, index) => {
    const portCfg = getPortConfig(cfg.type, schemas, moduleByName.get(moduleOfNode(cfg)));
    return {
      id: cfg.name,
      type: 'lcmNode',
      position: placed.get(cfg.name) ?? computePosition(index, module.node.length),
      data: {
        label: cfg.name,
        nodeType: cfg.type,
        kind: 'function' as const,
        property: { ...(cfg.property ?? {}) },
        numInputPorts: portCfg.numInputPorts,
        numOutputPorts: portCfg.numOutputPorts,
        outputLabels: portCfg.outputLabels,
      },
    };
  });

  // Taken from where the inner nodes actually ended up, so an unplaced output
  // port sits right of the arranged graph rather than right of a grid nobody
  // is looking at any more.
  const rightmost = inner.reduce((max, n) => Math.max(max, n.position.x), 100);
  const inPorts = placeBoundaryPorts('in', module.numInPort, stored, rightmost);
  const outPorts = placeBoundaryPorts('out', module.numOutPort, stored, rightmost);
  const nodes: Node<CanvasNodeData>[] = [
    ...inPorts.map((position, port) => boundaryNode('in', port, position)),
    ...inner,
    ...outPorts.map((position, port) => boundaryNode('out', port, position)),
  ];

  const edges: Edge<EdgeData>[] = [
    ...module.inPort.map((binding, index) => ({
      id: `e-in-${index}-${binding.nodeName}`,
      source: boundaryNodeId('in', binding.modulePort),
      target: binding.nodeName,
      sourceHandle: 'src-0',
      targetHandle: `tgt-${binding.nodePort}`,
      data: { transFormat: [], carry: [] },
      type: 'mapping',
      animated: false,
    })),
    ...module.path.map((conn, index) => ({
      id: `e-${index}-${conn.nodePair[0]}-${conn.nodePair[1]}`,
      source: conn.nodePair[0],
      target: conn.nodePair[1],
      sourceHandle: `src-${conn.portPair[0]}`,
      targetHandle: `tgt-${conn.portPair[1]}`,
      data: {
        transFormat: normalizeTransFormat(conn.transFormat),
        carry: normalizeCarry(conn.carry),
      },
      type: 'mapping',
      animated: false,
    })),
    ...module.outPort.map((binding, index) => ({
      id: `e-out-${index}-${binding.nodeName}`,
      source: binding.nodeName,
      target: boundaryNodeId('out', binding.modulePort),
      sourceHandle: `src-${binding.nodePort}`,
      targetHandle: 'tgt-0',
      data: { transFormat: [], carry: [] },
      type: 'mapping',
      animated: false,
    })),
  ];

  return { nodes, edges };
}

/**
 * A Module node instantiating a named module.
 *
 * The instance is named after the module but kept distinct, so the same module
 * can appear more than once in a policy; the property is what the engine reads
 * to resolve it, and the node name is free to say what this instance is for.
 *
 * The port count comes from the module rather than from a default, because a
 * typeless Module node has no arity to guess at -- guessing 1-in/1-out is what
 * silently dropped an edge the next time the policy was saved.
 */
export function moduleInstance(
  module: ModuleConfig,
  existing: Node<CanvasNodeData>[],
  position: { x: number; y: number },
): Node<CanvasNodeData> {
  const used = existing.filter(
    (n) =>
      moduleOfNode({ name: n.id, type: n.data.nodeType, property: n.data.property }) ===
      module.name,
  );
  const name = used.length === 0 ? module.name : `${module.name}_${used.length + 1}`;
  return {
    id: name,
    type: 'lcmNode',
    position,
    data: {
      label: name,
      nodeType: 'Module',
      kind: 'function',
      property: { module: module.name },
      numInputPorts: module.numInPort,
      numOutputPorts: module.numOutPort,
    },
  };
}

/** How many ports one side of the module boundary currently has. */
export function boundaryPortCount(nodes: Node<CanvasNodeData>[], side: 'in' | 'out'): number {
  return nodes.filter((n) => n.data.boundary?.side === side).length;
}

/**
 * Append one port to a side of the module boundary.
 *
 * Appending is the only growth direction that leaves existing port numbers
 * alone, which is what keeps a policy already holding an instance of this
 * module wired to the same things it was wired to before.
 */
export function addBoundaryPort(
  side: 'in' | 'out',
  nodes: Node<CanvasNodeData>[],
): Node<CanvasNodeData>[] {
  const port = boundaryPortCount(nodes, side);
  return [...nodes, boundaryNode(side, port, nextPortSlot(side, nodes))];
}

/**
 * Why the last port of a side cannot be removed yet, or null if it can.
 *
 * A bound port is refused rather than silently unwired: the edge IS the port
 * binding, so dropping both at once would look like the port simply vanished.
 */
export function lastBoundaryPortProblem(
  side: 'in' | 'out',
  nodes: Node<CanvasNodeData>[],
  edges: Edge<EdgeData>[],
): string | null {
  const count = boundaryPortCount(nodes, side);
  const label = side === 'in' ? 'input' : 'output';
  if (count === 0) return `The module has no ${label} port to remove.`;
  if (count === 1) {
    return `A module needs at least one ${label} port; nothing could reach it otherwise.`;
  }
  const id = boundaryNodeId(side, count - 1);
  const bound = edges.some((e) => e.source === id || e.target === id);
  if (bound) {
    return `Disconnect ${label} ${count - 1} before removing it.`;
  }
  return null;
}

/** Drop the highest-numbered port of a side; see lastBoundaryPortProblem. */
export function removeLastBoundaryPort(
  side: 'in' | 'out',
  nodes: Node<CanvasNodeData>[],
): Node<CanvasNodeData>[] {
  const id = boundaryNodeId(side, boundaryPortCount(nodes, side) - 1);
  return nodes.filter((n) => n.id !== id);
}

/**
 * Convert the React Flow graph back to a ModuleConfig.
 *
 * This is deliberately not flowToPolicyConfig with a filter: that one buckets
 * every node into trigger[] or node[], so a boundary pseudo-node leaking
 * through would be written out as a real node of type ModuleInput.
 */
export function flowToModuleConfig(
  nodes: Node<CanvasNodeData>[],
  edges: Edge<EdgeData>[],
  moduleName: string,
): ModuleConfig {
  const boundaries = nodes.filter((n) => n.data.boundary);
  const inner = nodes.filter((n) => !n.data.boundary);
  const knownInner = new Set(inner.map((n) => n.id));

  const module: ModuleConfig = {
    name: moduleName,
    numInPort: boundaries.filter((n) => n.data.boundary?.side === 'in').length,
    numOutPort: boundaries.filter((n) => n.data.boundary?.side === 'out').length,
    inPort: [],
    outPort: [],
    node: inner.map((n) => ({
      name: n.id,
      type: n.data.nodeType,
      property: { ...n.data.property },
    })),
    path: [],
  };

  for (const edge of edges) {
    const source = nodes.find((n) => n.id === edge.source);
    const target = nodes.find((n) => n.id === edge.target);
    const from = source?.data.boundary;
    const to = target?.data.boundary;

    if (from?.side === 'in') {
      module.inPort.push({
        modulePort: from.port,
        nodeName: edge.target,
        nodePort: parseHandleIndex(edge.targetHandle),
      });
    } else if (to?.side === 'out') {
      module.outPort.push({
        modulePort: to.port,
        nodeName: edge.source,
        nodePort: parseHandleIndex(edge.sourceHandle),
      });
    } else if (!from && !to) {
      module.path.push(edgeToConnection(edge, knownInner));
    }
  }

  module.inPort.sort((a, b) => a.modulePort - b.modulePort);
  module.outPort.sort((a, b) => a.modulePort - b.modulePort);
  // nodes still holds the boundary pseudo-nodes here, so a module's own ports
  // are captured with no extra work.
  module.ui = collectLayout(nodes);
  return module;
}

/**
 * Which module a Module node instantiates.
 *
 * Mirrors the engine's rule: the property names it, and a node that sets none
 * falls back to matching its own name, which is how configs written before the
 * property existed still resolve.
 */
export function moduleOfNode(cfg: NodeConfig): string {
  if (cfg.type !== 'Module') return '';
  const named = cfg.property?.module;
  return typeof named === 'string' && named !== '' ? named : cfg.name;
}

// ─── Renaming ─────────────────────────────────────────────────────────────────

/**
 * Rename a node everywhere its name is meaningful.
 *
 * The canvas id IS the node name on the wire, so renaming used to be cosmetic:
 * only the label moved, and the saved config kept the old name. That got worse
 * once payloads became namespaced, because the namespace key is the id while the
 * canvas shows the label, so the two could disagree on screen.
 *
 * Four places refer to a node by name and all of them move together: the node
 * itself, the endpoints of its edges, the carry lists that forward its
 * namespace, and any condition naming one of its fields as "node.field".
 */
export function renameNode(
  nodes: Node<CanvasNodeData>[],
  edges: Edge<EdgeData>[],
  oldId: string,
  newId: string,
): { nodes: Node<CanvasNodeData>[]; edges: Edge<EdgeData>[] } {
  const renamed = nodes.map((n) => {
    const data = { ...n.data, property: renameInProperty(n.data.property, oldId, newId) };
    if (n.id !== oldId) return { ...n, data };
    return { ...n, id: newId, data: { ...data, label: newId } };
  });

  const rewired = edges.map((e) => ({
    ...e,
    source: e.source === oldId ? newId : e.source,
    target: e.target === oldId ? newId : e.target,
    data: {
      transFormat: (e.data?.transFormat ?? []).map((row) => ({
        ...row,
        src: renameQualified(row.src, oldId, newId),
      })),
      carry: (e.data?.carry ?? []).map((name) => (name === oldId ? newId : name)),
    },
  }));

  return { nodes: renamed, edges: rewired };
}

/** Rewrites "old.field" to "new.field"; anything else is returned unchanged. */
function renameQualified(reference: string, oldId: string, newId: string): string {
  return reference.startsWith(`${oldId}.`)
    ? `${newId}.${reference.slice(oldId.length + 1)}`
    : reference;
}

/**
 * Rewrites qualified field references inside a node's properties.
 *
 * Only the repeatable row lists carry them today (an If or Compare condition),
 * and only their string cells, so the walk stops there rather than rewriting
 * arbitrary strings that merely look like a reference.
 */
function renameInProperty(
  property: Record<string, unknown>,
  oldId: string,
  newId: string,
): Record<string, unknown> {
  const next: Record<string, unknown> = { ...property };
  for (const [key, value] of Object.entries(next)) {
    if (!Array.isArray(value)) continue;
    next[key] = value.map((row) => {
      if (typeof row !== 'object' || row === null) return row;
      const cells = row as Record<string, unknown>;
      const rewritten: Record<string, unknown> = { ...cells };
      for (const [cell, cellValue] of Object.entries(cells)) {
        if (typeof cellValue === 'string') {
          rewritten[cell] = renameQualified(cellValue, oldId, newId);
        }
      }
      return rewritten;
    });
  }
  return next;
}

/**
 * Why a proposed node name cannot be used, or null when it can.
 *
 * The name reaches the wire verbatim and is also the namespace key, so a
 * collision or a boundary-prefixed name would corrupt a saved config rather
 * than merely look wrong.
 */
export function renameProblem(
  nodes: Node<CanvasNodeData>[],
  oldId: string,
  newId: string,
): string | null {
  const name = newId.trim();
  if (name === '') return 'a node needs a name';
  if (name === oldId) return null;
  if (isBoundaryId(name)) return `a name cannot start with ${BOUNDARY_PREFIX}; that marks a module port`;
  if (nodes.some((n) => n.id === name)) return `${name} is already used by another node`;
  return null;
}

/** Carry entries naming a node the graph no longer has. */
export function pruneCarry(carry: string[], nodes: Node<CanvasNodeData>[]): string[] {
  const known = new Set(nodes.map((n) => n.id));
  return carry.filter((name) => known.has(name));
}
