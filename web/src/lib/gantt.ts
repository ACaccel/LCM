import type { NodeStatus, RunNode } from "../api/types";

export interface GanttRow {
  id: string;
  type: string;
  status: NodeStatus;
  /** Milliseconds since the run's first node started. */
  start: number;
  end: number;
  /** Still running, so `end` is the current time. */
  open: boolean;
}

export interface Gantt {
  rows: GanttRow[];
  /** Length of the whole axis in milliseconds; at least 1. */
  total: number;
}

const ms = (iso: string | undefined): number | undefined => {
  if (!iso) return undefined;
  const t = Date.parse(iso);
  return Number.isNaN(t) ? undefined : t;
};

/**
 * Timing of the nodes that have started, relative to the first start. Nodes
 * keep the run's order (topological) when they started together. A node that
 * has not finished ends at `now`; nodes that never started have no row.
 */
export function buildGantt(nodes: readonly RunNode[], now: number): Gantt {
  const started = nodes.flatMap((n, order) => {
    const start = ms(n.startedAt);
    return start === undefined ? [] : [{ n, order, start }];
  });
  if (started.length === 0) return { rows: [], total: 1 };
  const origin = Math.min(...started.map((x) => x.start));
  const rows = started
    .sort((a, b) => a.start - b.start || a.order - b.order)
    .map(({ n, start }): GanttRow => {
      const finish = ms(n.finishedAt);
      const open = finish === undefined && (n.status === "running" || n.status === "retrying" || n.status === "suspended");
      const end = Math.max(start, finish ?? (open ? now : start));
      return { id: n.id, type: n.type, status: n.status, start: start - origin, end: end - origin, open };
    });
  return { rows, total: Math.max(1, ...rows.map((r) => r.end)) };
}

/** The first node, in run order, that failed for good or was marked dead. */
export function firstFailedNode(nodes: readonly RunNode[]): string | null {
  return nodes.find((n) => n.status === "failed" || n.status === "dead")?.id ?? null;
}
