/**
 * layout.ts
 *
 * Where nodes sit on the canvas.
 *
 * Positions used to be recomputed from scratch every time the canvas was built
 * from stored config, so a graph someone had arranged by hand snapped back to a
 * grid on the next save. They are stored now (PolicyConfig.ui / ModuleConfig.ui),
 * and this module is the one place that decides what to do when a position is
 * missing -- which is the whole difficulty, because a config can gain a node
 * behind the canvas's back and a module can gain a port.
 *
 * Pure module, no React import, so it can be unit-tested as-is.
 */

import type { CanvasLayout, CanvasNodeData, CanvasPoint } from './types';
import type { Node } from 'reactflow';

/** Top-left of the automatic grid. */
const ORIGIN = { x: 100, y: 80 };
const COL_PITCH = 220;
const ROW_PITCH = 160;
/** Vertical gap between a module's boundary ports. */
const PORT_PITCH = 120;
/** Half-pitch the boundary column is shifted up per extra port, to centre it. */
const PORT_CENTER_STEP = 20;
/** Column a module's input ports sit in, left of the inner graph. */
const IN_COLUMN = -140;
/** Gap between the rightmost inner node and the output port column. */
const OUT_GAP = 240;

/** A stable automatic layout: a loose grid, so nothing overlaps. */
export function computePosition(index: number, total: number): CanvasPoint {
  const cols = Math.max(1, Math.ceil(Math.sqrt(total)));
  return {
    x: ORIGIN.x + (index % cols) * COL_PITCH,
    y: ORIGIN.y + Math.floor(index / cols) * ROW_PITCH,
  };
}

/**
 * Read a stored layout, dropping anything malformed.
 *
 * Same defensive posture as normalizeTransFormat and normalizeCarry, for the
 * same reason: a hand-written config can put anything here, and a NaN reaching
 * React Flow corrupts the viewport rather than merely looking wrong.
 */
export function readLayout(ui: CanvasLayout | undefined): Map<string, CanvasPoint> {
  const stored = new Map<string, CanvasPoint>();
  const entries: Record<string, CanvasPoint> = ui?.node ?? {};
  for (const [id, point] of Object.entries(entries)) {
    if (Number.isFinite(point?.x) && Number.isFinite(point?.y)) {
      stored.set(id, { x: point.x, y: point.y });
    }
  }
  return stored;
}

/**
 * Resolve a position for every node, stored where there is one.
 *
 * A config with no layout at all reproduces the old grid exactly, so existing
 * policies and the harness fixtures look unchanged.
 *
 * A node the layout does not mention -- added by another session, or by a
 * hand-edited config -- goes in a grid one full row below everything that IS
 * placed, left-aligned with it. Anywhere is better than the origin, where such
 * nodes would stack on each other and on top of the arranged graph.
 */
export function placeNodes(ids: string[], stored: Map<string, CanvasPoint>): Map<string, CanvasPoint> {
  const placed = new Map<string, CanvasPoint>();
  const unknown: string[] = [];
  ids.forEach((id, index) => {
    const point = stored.get(id);
    if (point) {
      placed.set(id, point);
    } else {
      unknown.push(id);
      placed.set(id, computePosition(index, ids.length));
    }
  });
  if (placed.size === unknown.length) return placed;

  const known = ids.filter((id) => stored.has(id)).map((id) => stored.get(id) as CanvasPoint);
  const baseX = Math.min(...known.map((p) => p.x));
  const baseY = Math.max(...known.map((p) => p.y)) + ROW_PITCH;
  const cols = Math.max(1, Math.ceil(Math.sqrt(unknown.length)));
  unknown.forEach((id, index) => {
    placed.set(id, {
      x: baseX + (index % cols) * COL_PITCH,
      y: baseY + Math.floor(index / cols) * ROW_PITCH,
    });
  });
  return placed;
}

/**
 * Resolve a position for each of a module's boundary ports.
 *
 * With nothing stored this is the original centred column. Once any port of the
 * side has been placed, the column follows the placed ones and an unplaced port
 * stacks below the lowest of them -- so adding a port to a module whose ports
 * were dragged does not drop the new one on top of the inner graph.
 */
export function placeBoundaryPorts(
  side: 'in' | 'out',
  count: number,
  stored: Map<string, CanvasPoint>,
  innerRightmost: number,
): CanvasPoint[] {
  const ids = Array.from({ length: count }, (_, port) => boundaryLayoutId(side, port));
  const known = ids.map((id) => stored.get(id)).filter((p): p is CanvasPoint => p !== undefined);

  const column =
    known.length > 0
      ? Math.min(...known.map((p) => p.x))
      : side === 'in'
        ? IN_COLUMN
        : innerRightmost + OUT_GAP;
  let stackFrom = known.length > 0 ? Math.max(...known.map((p) => p.y)) : 0;

  return ids.map((id, port) => {
    const point = stored.get(id);
    if (point) return point;
    if (known.length === 0) {
      return { x: column, y: ORIGIN.y + port * PORT_PITCH - (count - 1) * PORT_CENTER_STEP };
    }
    stackFrom += PORT_PITCH;
    return { x: column, y: stackFrom };
  });
}

/**
 * Where a port appended in the designer should go.
 *
 * Below the lowest port already on that side, in their column, rather than at a
 * fixed offset from the port index -- which would land on the inner graph as
 * soon as the existing ports had been moved.
 */
export function nextPortSlot(side: 'in' | 'out', nodes: Node<CanvasNodeData>[]): CanvasPoint {
  const siblings = nodes.filter((n) => n.data.boundary?.side === side);
  if (siblings.length > 0) {
    return {
      x: Math.min(...siblings.map((n) => n.position.x)),
      y: Math.max(...siblings.map((n) => n.position.y)) + PORT_PITCH,
    };
  }
  const inner = nodes.filter((n) => !n.data.boundary);
  const column =
    side === 'in' ? IN_COLUMN : inner.reduce((max, n) => Math.max(max, n.position.x), ORIGIN.x) + OUT_GAP;
  return { x: column, y: ORIGIN.y };
}

/**
 * The layout to store for a canvas, or undefined when there is nothing to say.
 *
 * Coordinates are rounded: a drag produces sub-pixel values, and storing them
 * makes for a noisy config and a noisy diff for no gain. Boundary pseudo-nodes
 * are included, which is what makes a module's own ports stay where they were
 * put.
 */
export function collectLayout(nodes: Node<CanvasNodeData>[]): CanvasLayout | undefined {
  if (nodes.length === 0) return undefined;
  const node: Record<string, CanvasPoint> = {};
  for (const n of nodes) {
    node[n.id] = { x: Math.round(n.position.x), y: Math.round(n.position.y) };
  }
  return { node };
}

/** Positions currently on the canvas, for carrying across a reload. */
export function collectPositions(nodes: Node<CanvasNodeData>[]): Map<string, CanvasPoint> {
  return new Map(nodes.map((n) => [n.id, { x: n.position.x, y: n.position.y }]));
}

/**
 * Overlay positions onto a freshly built graph.
 *
 * Only positions: never data, never edges. Used when a reload rebuilds the item
 * already on screen, so that what the user is looking at does not jump.
 */
export function applyPositions(
  nodes: Node<CanvasNodeData>[],
  positions: Map<string, CanvasPoint>,
): Node<CanvasNodeData>[] {
  if (positions.size === 0) return nodes;
  return nodes.map((n) => {
    const point = positions.get(n.id);
    return point ? { ...n, position: point } : n;
  });
}

/**
 * The layout key for a module's boundary port.
 *
 * Deliberately duplicated from graphModel.boundaryNodeId rather than imported:
 * this module must not depend on graphModel, which imports it. The two are
 * pinned together by the shared "__" prefix, which renameProblem already
 * reserves so no real node can collide with one.
 */
function boundaryLayoutId(side: 'in' | 'out', port: number): string {
  return `__${side}:${port}`;
}
