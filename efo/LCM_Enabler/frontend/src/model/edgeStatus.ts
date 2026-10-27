/**
 * edgeStatus.ts
 *
 * Classifies an edge's field mapping for the on-canvas label.
 *
 * Pure module, no React import. The result is decoration and is attached only to
 * the derived edge array PolicyCanvas builds for rendering; flowToPolicyConfig
 * reads nothing but `transFormat`, so it structurally cannot reach the wire.
 */

import type { NodeSchema, TransFormatItem } from './types';

export type EdgeStatus = {
  kind: 'passthrough' | 'mapped' | 'invalid';
  label: string;
  detail: string;
};

export type MappingEdgeData = {
  transFormat: TransFormatItem[];
  _status?: EdgeStatus;
};

/**
 * Reasons a mapping row cannot work at runtime.
 *
 * A dotted src is listed because the engine resolves only the first path
 * segment and silently returns the value found there, so "a.b" quietly maps the
 * wrong thing rather than failing.
 */
export function mappingProblems(
  transFormat: TransFormatItem[],
  sourceSchema: NodeSchema | undefined,
  targetSchema: NodeSchema | undefined,
): string[] {
  const problems: string[] = [];
  const seenDst = new Set<string>();

  if (transFormat.length === 0) {
    return passthroughProblems(sourceSchema, targetSchema);
  }

  const sourceFields = sourceSchema?.output?.dynamic
    ? null
    : new Set((sourceSchema?.output?.fields ?? []).map((f) => f.name));
  const targetFields = targetSchema?.input?.dynamic
    ? null
    : sourceOrNull(targetSchema);

  for (const row of transFormat) {
    // Never assume the wire shape. A hand-written YAML with a mistyped key, or
    // any future drift between the Go DTO and this type, arrives as undefined
    // here; throwing would unmount the whole canvas over one bad edge.
    const src = text(row?.src);
    const dst = text(row?.dst);

    if (src === '') problems.push('a mapping has no source field');
    if (dst === '') problems.push('a mapping has no target field');
    if (src.includes('.')) {
      problems.push(`"${src}" uses a dotted path; the engine resolves only the first segment`);
    }
    if (dst !== '') {
      if (seenDst.has(dst)) problems.push(`"${dst}" is mapped more than once; the last one wins`);
      seenDst.add(dst);
    }
    if (src !== '' && sourceFields && sourceFields.size > 0 && !sourceFields.has(src)) {
      problems.push(`"${src}" is not produced by the source node`);
    }
    if (dst !== '' && targetFields && targetFields.size > 0 && !targetFields.has(dst)) {
      problems.push(`"${dst}" is not read by the target node`);
    }
  }
  return problems;
}

/** Any wire value as trimmed text; anything unusable reads as empty. */
function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : '';
}

/**
 * Reasons an unmapped edge cannot work.
 *
 * Pass-through is the right default almost everywhere, but it silently fails
 * when the target reads a field the source never produces: the property decodes
 * to its zero value and the node acts on 0 or "" as though it were configured.
 * Only judged when both shapes are known -- a dynamic port could carry anything.
 */
function passthroughProblems(
  sourceSchema: NodeSchema | undefined,
  targetSchema: NodeSchema | undefined,
): string[] {
  const wanted = targetSchema?.input;
  if (!wanted || wanted.dynamic || wanted.fields.length === 0) return [];

  const produced = sourceSchema?.output;
  if (!produced || produced.dynamic) return [];

  const available = new Set(produced.fields.map((f) => f.name));
  const missing = wanted.fields.filter((f) => !available.has(f.name)).map((f) => f.name);
  if (missing.length === 0) return [];

  return [
    `the source does not produce ${missing.join(', ')}, which the target reads; map a field instead of passing through`,
  ];
}

function sourceOrNull(schema: NodeSchema | undefined): Set<string> | null {
  if (!schema || schema.input === null) return null;
  return new Set(schema.input.fields.map((f) => f.name));
}

export function edgeStatus(
  transFormat: TransFormatItem[],
  sourceSchema: NodeSchema | undefined,
  targetSchema: NodeSchema | undefined,
): EdgeStatus {
  const problems = mappingProblems(transFormat, sourceSchema, targetSchema);
  if (problems.length > 0) {
    return {
      kind: 'invalid',
      label: `⚠ ${transFormat.length}`,
      detail: problems.join('\n'),
    };
  }
  if (transFormat.length === 0) {
    return {
      kind: 'passthrough',
      label: '⇥ all',
      detail: 'Pass-through — the upstream payload is delivered unchanged.',
    };
  }
  return {
    kind: 'mapped',
    label: `${transFormat.length} ↦`,
    detail: transFormat.map((r) => `${r.src} → ${r.dst}`).join('\n'),
  };
}
