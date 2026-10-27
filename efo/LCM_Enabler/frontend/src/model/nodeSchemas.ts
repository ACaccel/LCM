/**
 * nodeSchemas.ts
 *
 * Client-side cache of the node schemas served by GET /api/lcm/nodetypes.
 *
 * Schemas are immutable and fetched once per session, so this is zero-frequency
 * state and deliberately does NOT live in a Zustand store: a module-level map
 * plus an in-flight promise is enough, and it keeps schema reads out of the
 * render-subscription graph entirely.
 */

import { useEffect, useState } from 'react';
import { getNodeTypes } from '../api/lcmApi';
import type { FieldSchema, NodeSchema } from './types';

let cache: Map<string, NodeSchema> | null = null;
let inFlight: Promise<Map<string, NodeSchema>> | null = null;

/** Fetches the schemas once; concurrent callers share the same request. */
export function loadNodeSchemas(): Promise<Map<string, NodeSchema>> {
  if (cache) return Promise.resolve(cache);
  if (inFlight) return inFlight;

  inFlight = getNodeTypes()
    .then((res) => {
      const map = new Map<string, NodeSchema>();
      for (const schema of res.schemas ?? []) {
        map.set(schema.type, schema);
      }
      cache = map;
      return map;
    })
    .finally(() => {
      inFlight = null;
    });

  return inFlight;
}

/**
 * Returns the schema map, loading it on first use. `loaded` distinguishes "still
 * fetching" from "fetched and this node type genuinely has no schema", which the
 * property form needs in order to pick the right fallback.
 */
export function useNodeSchemas(): { schemas: Map<string, NodeSchema>; loaded: boolean } {
  const [schemas, setSchemas] = useState<Map<string, NodeSchema>>(cache ?? new Map());
  const [loaded, setLoaded] = useState(cache !== null);

  useEffect(() => {
    if (cache) return;
    let cancelled = false;
    void loadNodeSchemas()
      .then((map) => {
        if (!cancelled) {
          setSchemas(map);
          setLoaded(true);
        }
      })
      .catch(() => {
        // A missing catalog must not break editing; the form falls back to the
        // raw key/value editor and the picker to free text.
        if (!cancelled) setLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { schemas, loaded };
}

/** Properties that apply given the current discriminator value. */
export function visibleProperties(
  schema: NodeSchema,
  property: Record<string, unknown>,
): FieldSchema[] {
  const discriminator = schema.discriminator;
  if (!discriminator) return schema.properties;

  const current = String(property[discriminator] ?? '');
  return schema.properties.filter((f) => {
    if (f.name === discriminator) return true;
    if (!f.showWhen || f.showWhen.length === 0) return true;
    return f.showWhen.includes(current);
  });
}

/**
 * Properties excluded by the current discriminator value that still hold a
 * value. They are hidden rather than deleted, because switching the
 * discriminator to explore should not destroy what was typed.
 */
export function strandedProperties(
  schema: NodeSchema,
  property: Record<string, unknown>,
): FieldSchema[] {
  const visible = new Set(visibleProperties(schema, property).map((f) => f.name));
  return schema.properties.filter(
    (f) => !visible.has(f.name) && !isEmptyValue(property[f.name]),
  );
}

/** Stored keys with no matching schema field, so loading never hides data. */
export function extraPropertyKeys(
  schema: NodeSchema,
  property: Record<string, unknown>,
): string[] {
  const known = new Set(schema.properties.map((f) => f.name));
  return Object.keys(property).filter((k) => !known.has(k));
}

export function isEmptyValue(value: unknown): boolean {
  if (value === undefined || value === null) return true;
  if (typeof value === 'string') return value.trim() === '';
  if (Array.isArray(value)) return value.length === 0;
  return false;
}
