/**
 * fieldOptions.ts
 *
 * Merges the static node schema with what a run actually carried, so the field
 * picker can offer real names and sample values instead of asking the user to
 * remember them.
 *
 * Pure module, no React import, so it can be unit-tested as-is.
 */

import type { FieldSchema, PortSchema, RunNodeEvent, RunTrace } from './types';

export type FieldOption = {
  name: string;
  type: string;
  /** Where the name came from: the declared schema, the last run, or both. */
  origin: 'schema' | 'run' | 'both';
  /** Truncated JSON of the live value, when a run supplied one. */
  sample?: string;
};

const SAMPLE_MAX = 40;

function sampleOf(value: unknown): string {
  let text: string;
  try {
    text = JSON.stringify(value) ?? String(value);
  } catch {
    text = String(value);
  }
  return text.length > SAMPLE_MAX ? `${text.slice(0, SAMPLE_MAX - 1)}…` : text;
}

/**
 * Schema fields first in declaration order, then anything the run carried that
 * the schema does not describe. The live-only names are the whole point for a
 * webhook source, whose payload shape is set by the caller.
 */
export function mergeFieldOptions(
  port: PortSchema | null,
  liveContent: Record<string, unknown> | null,
): FieldOption[] {
  const options: FieldOption[] = [];
  const seen = new Set<string>();

  for (const field of port?.fields ?? []) {
    seen.add(field.name);
    const hasLive = liveContent !== null && field.name in liveContent;
    options.push({
      name: field.name,
      type: field.type,
      origin: hasLive ? 'both' : 'schema',
      sample: hasLive ? sampleOf(liveContent[field.name]) : undefined,
    });
  }

  if (liveContent) {
    for (const name of Object.keys(liveContent).sort()) {
      if (seen.has(name)) continue;
      options.push({
        name,
        type: inferType(liveContent[name]),
        origin: 'run',
        sample: sampleOf(liveContent[name]),
      });
    }
  }

  return options;
}

function inferType(value: unknown): string {
  if (typeof value === 'number') return 'number';
  if (typeof value === 'boolean') return 'boolean';
  if (typeof value === 'string') return 'string';
  if (Array.isArray(value)) return 'array';
  if (value !== null && typeof value === 'object') return 'object';
  return 'unknown';
}

/**
 * The best available live sample of what travelled along one edge.
 *
 * Preference order matters: the target's inputRaw is what arrived on THIS edge,
 * so it disambiguates a source with several output ports. A trigger never
 * appears in byNode at all -- only executed nodes are recorded -- so its payload
 * has to come from the run's trigger content, and since triggers are always the
 * first hop that path is exercised constantly.
 */
export function liveContentForEdge(
  run: RunTrace | null,
  byNode: Record<string, RunNodeEvent>,
  sourceNode: string,
  sourcePort: number,
  targetNode: string,
  targetPort: number,
): Record<string, unknown> | null {
  const target = byNode[targetNode];
  const arrived = target?.inputRaw?.[targetPort];
  if (arrived) return arrived;

  const source = byNode[sourceNode];
  const produced = source?.output?.[sourcePort];
  if (produced) return produced;

  if (run?.hasRun && run.trigger.node === sourceNode) {
    return run.trigger.content;
  }
  return null;
}

/** Field names a target declares, for the destination column. */
export function targetFieldOptions(port: PortSchema | null): FieldOption[] {
  return mergeFieldOptions(port, null);
}

/** True when the picker must fall back to free text rather than a closed list. */
export function isFreeForm(port: PortSchema | null | undefined): boolean {
  return !port || port.dynamic === true;
}

/** Human-readable reason a target accepts no mapping at all. */
export function noInputReason(
  nodeType: string,
  input: PortSchema | null | undefined,
): string | null {
  if (input === undefined) return null; // no schema; free text applies
  if (input === null) {
    return `${nodeType} reads no input fields — it is configured entirely by its properties. Leave this edge as pass-through.`;
  }
  if (input.fields.length === 0 && !input.dynamic) {
    return `${nodeType} declares an empty input. Leave this edge as pass-through.`;
  }
  return null;
}

export type { FieldSchema };
