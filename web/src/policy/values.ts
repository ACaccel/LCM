/** Helpers for editing prop values as text. */

export function isTemplate(v: unknown): v is string {
  return typeof v === "string" && v.includes("{{");
}

export type Parsed<T> = { ok: true; value: T | undefined } | { ok: false };

/** Empty text clears the value; templates stay strings; otherwise a finite number is required. */
export function parseNumber(text: string, integer: boolean): Parsed<number | string> {
  const t = text.trim();
  if (t === "") return { ok: true, value: undefined };
  if (t.includes("{{")) return { ok: true, value: t };
  const n = Number(t);
  if (!Number.isFinite(n) || (integer && !Number.isInteger(n))) return { ok: false };
  return { ok: true, value: n };
}

/**
 * Free-form values are typed as JSON when the text is valid JSON and as a
 * plain string otherwise, so `5`, `true` and `{"a":1}` keep their types and
 * `hello` stays text.
 */
export function parseAny(text: string): Parsed<unknown> {
  if (text.trim() === "") return { ok: true, value: undefined };
  try {
    return { ok: true, value: JSON.parse(text) as unknown };
  } catch {
    return { ok: true, value: text };
  }
}

/** Inverse of parseAny: strings that would read back as another type are quoted. */
export function formatAny(v: unknown): string {
  if (v === undefined) return "";
  if (typeof v === "string") {
    try {
      JSON.parse(v);
      return JSON.stringify(v);
    } catch {
      return v;
    }
  }
  return JSON.stringify(v);
}

export function formatNumber(v: unknown): string {
  return typeof v === "number" || typeof v === "string" ? String(v) : "";
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Removes undefined entries and empty containers that the user cleared. */
export function prune<T>(v: T): T | undefined {
  if (Array.isArray(v)) {
    return v.map((x) => prune(x) ?? x) as T;
  }
  if (isRecord(v)) {
    const out: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v)) {
      const p = prune(x);
      if (p === undefined) continue;
      if (isRecord(p) && Object.keys(p).length === 0) continue;
      out[k] = p;
    }
    return out as T;
  }
  return v === undefined ? undefined : v;
}

export function setIn(obj: unknown, path: readonly (string | number)[], value: unknown): unknown {
  const [head, ...rest] = path;
  if (head === undefined) return value;
  if (typeof head === "number") {
    const arr = Array.isArray(obj) ? [...obj] : [];
    arr[head] = setIn(arr[head], rest, value);
    return arr;
  }
  const rec = isRecord(obj) ? { ...obj } : {};
  const next = setIn(rec[head], rest, value);
  if (next === undefined) delete rec[head];
  else rec[head] = next;
  return rec;
}
