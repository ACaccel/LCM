/**
 * coerce.ts
 *
 * Converts what a form control produced into the type the backend expects.
 *
 * This exists because the previous generic editor stored every value as a
 * string. That is not merely untidy: a []string property saved as "a,b" fails
 * to decode, and since no node checks GetProperty's error the whole property
 * struct stays at its zero value, the request field becomes "", and the node
 * silently answers 400 without doing anything. Coercing at the commit point is
 * what prevents that class of failure.
 *
 * Pure module, no React import, so it can be unit-tested as-is.
 */

import type { FieldType } from './types';

/**
 * Returns the value to store, or undefined to delete the key.
 *
 * Deleting beats storing a zero: an empty number field means "not configured",
 * and writing 0 would look like a deliberate value to both the backend and the
 * required-field validation.
 */
export function coerce(type: FieldType, raw: string | boolean | string[]): unknown | undefined {
  if (typeof raw === 'boolean') return raw;

  if (Array.isArray(raw)) {
    // Stored YAML can put numbers in a string list, and those have no trim.
    const cleaned = raw.map((v) => String(v)).filter((v) => v.trim() !== '');
    return cleaned.length > 0 ? cleaned : undefined;
  }

  const text = raw.trim();

  switch (type) {
    case 'integer':
    case 'number': {
      if (text === '') return undefined;
      const n = Number(text);
      // Keep the raw text rather than storing NaN, so the value stays visible
      // and the validation can report it.
      return Number.isFinite(n) ? n : text;
    }
    case 'boolean':
      return text === 'true';
    case 'string[]':
      return text === '' ? undefined : [text];
    default:
      return text === '' ? undefined : text;
  }
}

/** Renders a stored value into what a text or number control expects. */
export function toControlValue(value: unknown): string {
  if (value === undefined || value === null) return '';
  if (Array.isArray(value)) return value.join(', ');
  return String(value);
}

/** Stored value as a string list, for the repeatable list control. */
export function toStringList(value: unknown): string[] {
  if (Array.isArray(value)) return value.map((v) => String(v));
  if (value === undefined || value === null || value === '') return [];
  return [String(value)];
}

/** Stored value as a row list, for the repeatable object[] control. */
export function toRowList(value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value)) return [];
  return value.filter(
    (row): row is Record<string, unknown> =>
      typeof row === 'object' && row !== null && !Array.isArray(row),
  );
}
