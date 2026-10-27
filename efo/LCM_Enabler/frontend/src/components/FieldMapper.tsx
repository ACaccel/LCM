/**
 * FieldMapper.tsx
 *
 * Edits the two independent things a connection carries.
 *
 * Carried context is the checkbox list: which upstream namespaces continue
 * downstream. Everything selected is the default, so the common case needs
 * nothing, and unticking one is how a field that would otherwise shadow another
 * is dropped.
 *
 * Field mapping is the src → dst list: it renames fields for the receiving node
 * only, and does not change what the rest of the policy still sees under the
 * original name. An unconfigured edge is pass-through, so the banner says so
 * rather than showing an empty list that looks broken. Both sides are picked
 * from the node's declared fields, augmented with whatever the last run actually
 * carried -- the only source of truth for a webhook payload, whose shape the
 * caller decides.
 */

import { useState } from 'react';
import {
  isFreeForm,
  liveContentForEdge,
  mergeFieldOptions,
  noInputReason,
  targetFieldOptions,
  type FieldOption,
} from '../model/fieldOptions';
import { useNodeSchemas } from '../model/nodeSchemas';
import { useRunStore } from '../hooks/useRunStore';
import {
  availableNamespaces,
  canUntick,
  clashes,
  selectedNamespaces,
  staleCarry,
  toCarry,
} from '../model/namespaces';
import type { CanvasNodeData, EdgeData, TransFormatItem } from '../model/types';
import type { Edge, Node } from 'reactflow';
import '../styles/FieldMapper.css';

type Props = {
  edge: Edge<EdgeData>;
  sourceNode: Node<CanvasNodeData> | undefined;
  targetNode: Node<CanvasNodeData> | undefined;
  sourcePort: number;
  targetPort: number;
  /** The whole graph, so the reachable namespaces can be walked back. */
  nodes: Node<CanvasNodeData>[];
  edges: Edge<EdgeData>[];
  onChange: (data: EdgeData) => void;
};

export default function FieldMapper({
  edge,
  sourceNode,
  targetNode,
  sourcePort,
  targetPort,
  nodes,
  edges,
  onChange,
}: Props): JSX.Element {
  const { schemas } = useNodeSchemas();
  const run = useRunStore((s) => s.run);
  const byNode = useRunStore((s) => s.byNode);
  const [pendingSrc, setPendingSrc] = useState<string | null>(null);

  const transFormat = edge.data?.transFormat ?? [];
  const carry = edge.data?.carry ?? [];

  const setTransFormat = (rows: TransFormatItem[]) => onChange({ transFormat: rows, carry });
  const setCarry = (names: string[]) => onChange({ transFormat, carry: names });

  const available = availableNamespaces(edge, nodes, edges, schemas, byNode);
  const kept = new Set(carry.length > 0 ? carry : available.map((ns) => ns.name));
  const overlaps = clashes(selectedNamespaces(available, carry));

  const stale = staleCarry(carry, available);

  function toggleNamespace(name: string) {
    const next = new Set(kept);
    if (next.has(name)) {
      // An empty list on the wire means "forward everything", so emptying the
      // selection would silently do the opposite of the click.
      if (!canUntick(next)) return;
      next.delete(name);
    } else {
      next.add(name);
    }
    setCarry(toCarry(available, next));
  }

  /** Drops carry entries whose node is no longer upstream. */
  function dropStale() {
    setCarry(carry.filter((name) => !stale.includes(name)));
  }

  const sourceSchema = sourceNode ? schemas.get(sourceNode.data.nodeType) : undefined;
  const targetSchema = targetNode ? schemas.get(targetNode.data.nodeType) : undefined;

  const live = liveContentForEdge(
    run,
    byNode,
    sourceNode?.id ?? '',
    sourcePort,
    targetNode?.id ?? '',
    targetPort,
  );

  const sourceOptions = mergeFieldOptions(sourceSchema?.output ?? null, live);
  const targetOptions = targetFieldOptions(targetSchema?.input ?? null);

  const sourceFreeForm = isFreeForm(sourceSchema?.output) && sourceOptions.length === 0;
  const targetFreeForm = isFreeForm(targetSchema?.input) && targetOptions.length === 0;
  const blockedReason = targetNode
    ? noInputReason(targetNode.data.nodeType, targetSchema?.input)
    : null;

  const mappedTargets = new Set(transFormat.map((row) => row.dst));

  function setRow(index: number, key: 'src' | 'dst', value: string) {
    setTransFormat(transFormat.map((row, i) => (i === index ? { ...row, [key]: value } : row)));
  }

  function removeRow(index: number) {
    setTransFormat(transFormat.filter((_, i) => i !== index));
  }

  /**
   * Adds a mapping straight from the source column.
   *
   * A target with no declared fields, such as If, has no chip to click, so the
   * pick-source-then-pick-target flow dead-ends there. Seeding the target with
   * the same name gives the common case (a rename of nothing) in one click and
   * leaves the name editable.
   */
  function mapSourceDirectly(src: string) {
    if (transFormat.some((row) => row.src === src)) return;
    setTransFormat([...transFormat, { src, dst: src }]);
    setPendingSrc(null);
  }

  function pairWith(dst: string) {
    if (!pendingSrc) return;
    const existing = transFormat.findIndex((row) => row.dst === dst);
    if (existing >= 0) {
      setTransFormat(transFormat.map((row, i) => (i === existing ? { src: pendingSrc, dst } : row)));
    } else {
      setTransFormat([...transFormat, { src: pendingSrc, dst }]);
    }
    setPendingSrc(null);
  }

  return (
    <div className="field-mapper">
      <div className="fm-section-head">Carried context</div>
      {available.length === 0 ? (
        <div className="fm-empty">Nothing upstream produces a payload yet.</div>
      ) : (
        <>
          <div className="fm-namespaces">
            {available.map((ns) => (
              <label
                key={ns.name}
                className={`fm-ns${kept.has(ns.name) ? ' kept' : ''}`}
                title={ns.fields.length > 0 ? ns.fields.join(', ') : 'no known fields yet'}
              >
                <input
                  type="checkbox"
                  checked={kept.has(ns.name)}
                  disabled={kept.has(ns.name) && !canUntick(kept)}
                  onChange={() => toggleNamespace(ns.name)}
                />
                <span className="fm-ns-name">{ns.name}</span>
                {ns.fields.length > 0 && (
                  <span className="fm-ns-count">{ns.fields.length}</span>
                )}
              </label>
            ))}
          </div>
          <div className="fm-footnote">
            {carry.length === 0
              ? 'Everything upstream is forwarded. Untick a node to stop its fields here.'
              : `${kept.size} of ${available.length} forwarded; the rest stop at this connection.` +
                (kept.size === 1 ? ' At least one must stay selected.' : '')}
          </div>
          {stale.length > 0 && (
            <div className="fm-note">
              {stale.join(', ')} {stale.length === 1 ? 'is' : 'are'} no longer upstream of this
              connection. The engine ignores them; saving keeps them until they are removed.
              <button className="btn-secondary" onClick={dropStale}>
                Remove
              </button>
            </div>
          )}
          {overlaps.length > 0 && (
            <div className="fm-note">
              {overlaps.map((clash) => (
                <div key={clash.field}>
                  <strong>{clash.field}</strong> comes from {clash.from.join(' and ')} —{' '}
                  {targetNode?.id ?? 'the target'} reads the one from {clash.winner}. Untick the
                  other, or map <code>{`${clash.from[0]}.${clash.field}`}</code> below to reach it.
                </div>
              ))}
            </div>
          )}
        </>
      )}

      <div className="fm-section-head">Field mapping</div>
      {transFormat.length === 0 ? (
        <div className="fm-banner">
          <strong>Pass-through</strong> — {sourceNode?.id ?? 'the source'} delivers{' '}
          {sourceOptions.length > 0 ? `all ${sourceOptions.length} field(s)` : 'its payload'}{' '}
          to {targetNode?.id ?? 'the target'} unchanged.
          {!blockedReason && (
            <button className="btn-secondary" onClick={() => setTransFormat([{ src: '', dst: '' }])}>
              Map fields instead
            </button>
          )}
        </div>
      ) : (
        <div className="fm-banner mapped">
          <span>
            <strong>
              {transFormat.length} field{transFormat.length === 1 ? '' : 's'} mapped
            </strong>{' '}
            — only these reach {targetNode?.id ?? 'the target'}; anything else the source
            produces is dropped.
          </span>
          <button className="btn-secondary" onClick={() => setTransFormat([])}>
            Reset to pass-through
          </button>
        </div>
      )}

      {blockedReason && <div className="fm-note">{blockedReason}</div>}

      {transFormat.map((row, index) => (
        // Keyed by position, never by content: setRow and removeRow address rows
        // by index anyway, and a content-derived key changes on every keystroke,
        // which remounts the input and drops the caret after one character.
        <div key={index} className="tf-row">
          <SideSelect
            value={row.src}
            options={sourceOptions}
            freeForm={sourceFreeForm}
            placeholder="source field"
            title={`field ${sourceNode?.id ?? 'the source'} produces`}
            onChange={(v) => setRow(index, 'src', v)}
          />
          <span className="tf-arrow">→</span>
          <SideSelect
            value={row.dst}
            options={targetOptions}
            freeForm={targetFreeForm}
            placeholder="target field"
            title={`name it arrives under at ${targetNode?.id ?? 'the target'}`}
            onChange={(v) => setRow(index, 'dst', v)}
          />
          <button className="icon-btn danger" onClick={() => removeRow(index)}>
            ×
          </button>
        </div>
      ))}

      {!blockedReason && !(sourceFreeForm && targetFreeForm) &&
        (sourceOptions.length > 0 || targetOptions.length > 0) && (
        <div className="fm-columns">
          <div className="fm-column">
            <div className="fm-column-head">
              {sourceNode?.id ?? 'source'} · out {sourcePort}
            </div>
            {sourceOptions.length === 0 && (
              <div className="fm-empty">
                Payload shape is set by the caller. Fire a webhook to discover its fields.
              </div>
            )}
            {sourceOptions.map((opt) => (
              <button
                key={opt.name}
                className={`fm-chip${pendingSrc === opt.name ? ' selected' : ''}`}
                onClick={() =>
                  targetOptions.length === 0
                    ? mapSourceDirectly(opt.name)
                    : setPendingSrc(pendingSrc === opt.name ? null : opt.name)
                }
                title={
                  targetOptions.length === 0
                    ? `Map ${opt.name} — ${opt.sample ? `last run: ${opt.sample}` : opt.type}`
                    : opt.sample
                      ? `${opt.type} — last run: ${opt.sample}`
                      : opt.type
                }
              >
                <span className="fm-chip-name">{opt.name}</span>
                {opt.sample !== undefined && <span className="fm-chip-sample">{opt.sample}</span>}
              </button>
            ))}
          </div>

          <div className="fm-column">
            <div className="fm-column-head">
              {targetNode?.id ?? 'target'} · in {targetPort}
            </div>
            {targetOptions.length === 0 && (
              <div className="fm-empty">
                {targetFreeForm
                  ? `${targetNode?.id ?? 'This node'} accepts any field name. Click a field on the left to map it, then edit the name it arrives under.`
                  : 'No declared fields.'}
              </div>
            )}
            {targetOptions.map((opt) => (
              <button
                key={opt.name}
                className={`fm-chip${mappedTargets.has(opt.name) ? ' mapped' : ''}`}
                disabled={!pendingSrc && !mappedTargets.has(opt.name)}
                onClick={() => pairWith(opt.name)}
                title={pendingSrc ? `Map ${pendingSrc} to ${opt.name}` : 'Pick a source field first'}
              >
                <span className="fm-chip-name">{opt.name}</span>
              </button>
            ))}
          </div>
        </div>
      )}

      {live && (
        <div className="fm-footnote">
          Live values from run {run?.runId} · fields marked with a value were seen in that run.
        </div>
      )}
    </div>
  );
}

type SideProps = {
  value: string;
  options: FieldOption[];
  freeForm: boolean;
  placeholder: string;
  title: string;
  onChange: (value: string) => void;
};

function SideSelect({
  value,
  options,
  freeForm,
  placeholder,
  title,
  onChange,
}: SideProps): JSX.Element {
  if (freeForm || options.length === 0) {
    return (
      <input
        placeholder={placeholder}
        title={title}
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  }

  // A stored value outside the option list would make the select render blank
  // without firing onChange, so it would look unset while still being saved.
  const unknown = value !== '' && !options.some((o) => o.name === value);
  return (
    <select value={value} title={title} onChange={(e) => onChange(e.target.value)}>
      <option value="">— {placeholder} —</option>
      {options.map((o) => (
        <option key={o.name} value={o.name}>
          {o.name}
        </option>
      ))}
      {unknown && <option value={value}>⚠ {value} (not a known field)</option>}
    </select>
  );
}
