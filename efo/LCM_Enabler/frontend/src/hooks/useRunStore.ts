/**
 * useRunStore.ts
 *
 * Execution-trace state, deliberately kept in a store of its own.
 *
 * The components that existed before this feature subscribe to useStore without
 * a selector, so every write there re-renders all of them, canvas included.
 * Polling a run writes roughly once a second, so putting run state in that store
 * would repaint the whole canvas on every tick. New consumers subscribe here
 * with selectors instead.
 */

import { create } from 'zustand';
import type { RunNodeEvent, RunTrace } from '../model/types';

/** Key identifying one taken edge: "<source node>:<source port>". */
export function edgeKey(node: string, port: number): string {
  return `${node}:${port}`;
}

type RunState = {
  /** Latest trace, with events accumulated across incremental polls. */
  run: RunTrace | null;
  /** Top-level events keyed by node name, for the on-canvas badges. */
  byNode: Record<string, RunNodeEvent>;
  /** The trigger that started this run, which produces no node event of its own. */
  triggerNode: string | null;
  /** Edges the run actually traversed, as edgeKey strings. */
  takenEdges: Set<string>;
  polling: boolean;
  /** Last polling error, shown in the panel instead of repeated toasts. */
  error: string | null;
};

type RunActions = {
  /** Replaces the trace wholesale; used on the first poll and on runId change. */
  setRun: (trace: RunTrace) => void;
  /** Appends newly fetched events to the current run. */
  mergeRun: (trace: RunTrace) => void;
  clearRun: () => void;
  setPolling: (polling: boolean) => void;
  setError: (message: string | null) => void;
};

/** Derives the badge and edge indexes from the accumulated event list. */
function index(
  trace: RunTrace,
  events: RunNodeEvent[],
): Pick<RunState, 'byNode' | 'takenEdges' | 'triggerNode'> {
  const byNode: Record<string, RunNodeEvent> = {};
  const takenEdges = new Set<string>();
  for (const ev of events) {
    // Module internals are not on the canvas, so they get neither a badge nor
    // an edge highlight; the panel shows them nested instead.
    if (ev.scope !== '') continue;
    byNode[ev.node] = ev;
    for (const port of ev.ports) {
      takenEdges.add(edgeKey(ev.node, port));
    }
  }

  // A trigger is recorded once on the run itself and never as a node event, so
  // without this the node that started the run reads as "not executed" and the
  // first edge stays faded. Triggers always have exactly one output port.
  const triggerNode = trace.hasRun ? trace.trigger.node : null;
  if (triggerNode) takenEdges.add(edgeKey(triggerNode, 0));

  return { byNode, takenEdges, triggerNode };
}

const emptyIndexes = { byNode: {}, takenEdges: new Set<string>(), triggerNode: null };

export const useRunStore = create<RunState & RunActions>((set) => ({
  run: null,
  byNode: {},
  takenEdges: new Set<string>(),
  triggerNode: null,
  polling: false,
  error: null,

  setRun: (trace) => set({ run: trace, ...index(trace, trace.events), error: null }),

  mergeRun: (trace) =>
    set((s) => {
      // A different runId means a new execution superseded the one we were
      // following, so the accumulated events no longer belong together.
      if (!s.run || s.run.runId !== trace.runId) {
        return { run: trace, ...index(trace, trace.events), error: null };
      }
      const events = [...s.run.events, ...trace.events];
      return { run: { ...trace, events }, ...index(trace, events), error: null };
    }),

  clearRun: () => set({ run: null, ...emptyIndexes, error: null }),

  setPolling: (polling) => set({ polling }),

  setError: (message) => set({ error: message }),
}));
