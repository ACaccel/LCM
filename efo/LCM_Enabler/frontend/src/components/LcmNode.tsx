/**
 * LcmNode.tsx
 *
 * Custom React Flow node rendering for all LCM node types.
 * Renders source/target handles dynamically based on port counts stored in
 * CanvasNodeData. Multiple output ports (e.g. If node: true/false) are
 * stacked vertically on the right side, labelled per the port config.
 */

import { memo } from 'react';
import { Handle, Position, type NodeProps } from 'reactflow';
import type { CanvasNodeData } from '../model/types';
import { useRunStore } from '../hooks/useRunStore';

// Color coding by node category for quick visual identification.
const BRANCHING = new Set(['If', 'Compare']);
const FLOW = new Set(['Merge', 'Wait']);

function nodeColor(nodeType: string): string {
  if (nodeType === 'Webhook' || nodeType === 'Timer') return '#4a90d9';
  if (BRANCHING.has(nodeType)) return '#e07b39';
  if (FLOW.has(nodeType)) return '#c9a227';
  if (nodeType === 'Module') return '#7b6fd6';
  // CALM keeps its own colour so the method's nodes read as one pipeline;
  // the selector's branching nature is already visible from its four
  // labelled output ports.
  if (nodeType.startsWith('CALM_')) return '#2bb3a8';
  if (nodeType === 'HTTPRequest' || nodeType.startsWith('IEEE1935_')) return '#3aab6e';
  return '#888';
}

/**
 * One-line preview of a property value for the node body.
 *
 * String() on a condition list renders "[object Object]", which tells the reader
 * nothing and looks like a bug. Structured values get a shape summary instead.
 */
function previewValue(value: unknown): string {
  if (value === null || value === undefined) return '';
  if (Array.isArray(value)) {
    return `${value.length} item${value.length === 1 ? '' : 's'}`;
  }
  if (typeof value === 'object') {
    return `{${Object.keys(value as Record<string, unknown>).join(', ')}}`;
  }
  return String(value);
}

function LcmNode({ id, data, selected }: NodeProps<CanvasNodeData>) {
  // Subscribed with a selector so a polling tick only repaints the nodes that
  // actually took part in the run, not the whole canvas.
  const runEvent = useRunStore((s) => s.byNode[id]);
  const hasRun = useRunStore((s) => s.run?.hasRun ?? false);
  // A trigger fires the run but records no node event of its own, so it needs
  // its own check or it would show as "not executed" in the run that it started.
  const firedTrigger = useRunStore((s) => (s.triggerNode === id ? s.run?.trigger : undefined));
  const isWaiting = useRunStore((s) =>
    (s.run?.waiting ?? []).some((w) => w.scope === '' && w.node === id),
  );

  // Read the arity off the node, not off the type: whoever built this node
  // already resolved it (schema, or the module's own declaration), and
  // re-deriving it here is how the rendered handles drifted from the saved edges.
  const numIn = data.numInputPorts;
  const numOut = data.numOutputPorts;
  const outLabels = data.outputLabels ?? [];

  const color = nodeColor(data.nodeType);

  return (
    <div
      style={{
        background: '#1e2130',
        border: `2px solid ${selected ? '#fff' : color}`,
        borderRadius: 8,
        minWidth: 160,
        padding: '6px 12px 10px',
        position: 'relative',
        boxShadow: selected ? `0 0 0 2px ${color}44` : 'none',
      }}
    >
      {/* Run status badge: only meaningful once a run has been recorded. */}
      {hasRun && (
        <div
          title={
            runEvent
              ? `executed in ${runEvent.durationMs}ms, took output port(s) ${runEvent.ports.join(', ')}`
              : firedTrigger
                ? `fired this run with ${JSON.stringify(firedTrigger.content ?? {})}`
                : isWaiting
                  ? 'waiting for an input that never arrived in this run'
                  : 'not executed in this run'
          }
          style={{
            position: 'absolute',
            top: -8,
            right: -8,
            width: 16,
            height: 16,
            borderRadius: '50%',
            display: 'flex',
            alignItems: 'center',
            justifyContent: 'center',
            fontSize: 10,
            fontWeight: 700,
            color: '#0f1117',
            background: runEvent || firedTrigger ? '#3aab6e' : isWaiting ? '#e07b39' : '#556070',
            border: '2px solid #0f1117',
            zIndex: 1,
          }}
        >
          {firedTrigger ? '\u26a1' : runEvent ? '\u2713' : isWaiting ? '\u23f8' : ''}
        </div>
      )}

      {/* Header */}
      <div
        style={{
          background: color,
          borderRadius: '4px 4px 0 0',
          margin: '-6px -12px 6px',
          padding: '3px 8px',
          fontSize: 10,
          fontWeight: 700,
          letterSpacing: 0.5,
          color: '#fff',
          textTransform: 'uppercase',
        }}
      >
        {data.nodeType}
      </div>

      {/* Node name */}
      <div style={{ fontSize: 12, color: '#d0d6e8', fontWeight: 600, marginBottom: 2 }}>
        {data.label}
      </div>

      {/* Property summary — show first two key=value pairs */}
      {Object.entries(data.property)
        .slice(0, 2)
        .map(([k, v]) => (
          <div key={k} style={{ fontSize: 10, color: '#8899aa' }}>
            {k}: <span style={{ color: '#c0cfe0' }}>{previewValue(v)}</span>
          </div>
        ))}

      {/* Latest run result, so the outcome is readable without opening a panel */}
      {runEvent && (
        <div
          style={{
            marginTop: 3,
            paddingTop: 3,
            borderTop: '1px dashed #2a3050',
            fontSize: 9,
            color: '#3aab6e',
            fontFamily: 'var(--font-mono)',
            whiteSpace: 'nowrap',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            maxWidth: 220,
          }}
        >
          #{runEvent.seq} out:{runEvent.ports.join(',')} {runEvent.durationMs}ms
        </div>
      )}

      {/* Target handles (input ports) — evenly spaced on the left */}
      {Array.from({ length: numIn }).map((_, i) => {
        const top = numIn === 1 ? '50%' : `${((i + 1) / (numIn + 1)) * 100}%`;
        return (
          <Handle
            key={`tgt-${i}`}
            id={`tgt-${i}`}
            type="target"
            position={Position.Left}
            style={{ top, background: '#8899aa', width: 10, height: 10 }}
          />
        );
      })}

      {/* Source handles (output ports) — evenly spaced on the right */}
      {Array.from({ length: numOut }).map((_, i) => {
        const top = numOut === 1 ? '50%' : `${((i + 1) / (numOut + 1)) * 100}%`;
        const label = outLabels[i];
        return (
          <div key={`src-wrapper-${i}`}>
            {label && (
              <div
                style={{
                  position: 'absolute',
                  right: 16,
                  top,
                  transform: 'translateY(-50%)',
                  fontSize: 9,
                  color: '#8899aa',
                  pointerEvents: 'none',
                }}
              >
                {label}
              </div>
            )}
            <Handle
              id={`src-${i}`}
              type="source"
              position={Position.Right}
              style={{ top, background: color, width: 10, height: 10 }}
            />
          </div>
        );
      })}
    </div>
  );
}

export default memo(LcmNode);
