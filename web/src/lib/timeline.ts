import type { TimelineEntry, TimelineList } from "../api/types";

/** A field of an entry's data; entries that come from signals nest the signal's own data one level down. */
export function timelineField(d: Record<string, unknown>, key: string): unknown {
  if (key in d) return d[key];
  const inner = d["data"];
  return typeof inner === "object" && inner !== null ? (inner as Record<string, unknown>)[key] : undefined;
}

/**
 * Identity of a timeline entry. The row id is not used because live
 * `timeline` events may carry id 0 for entries whose id is not known yet;
 * the remaining fields identify an entry just as well and stay stable when
 * the same entry is later loaded from a page.
 */
export function entryKey(e: TimelineEntry): string {
  return [e.resource, e.time, e.kind, e.ref, e.summary].join("\u0000");
}

/**
 * Whether a live entry belongs in a timeline query with this resource, kind
 * and time filter; `from` is inclusive and `to` exclusive, as in the API.
 */
export function matchesFilter(e: TimelineEntry, resource: string, kinds: readonly string[], from = "", to = ""): boolean {
  if (resource && e.resource !== resource) return false;
  const t = Date.parse(e.time);
  if (from && t < Date.parse(from)) return false;
  if (to && t >= Date.parse(to)) return false;
  return kinds.length === 0 || kinds.includes(e.kind);
}

/** Local calendar day of an instant, as YYYY-MM-DD. */
export function localDay(iso: string): string {
  const d = new Date(iso);
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

/** Splits a newest-first list into runs of entries of the same local day, keeping the order. */
export function groupByDay(entries: readonly TimelineEntry[]): { day: string; entries: TimelineEntry[] }[] {
  const out: { day: string; entries: TimelineEntry[] }[] = [];
  for (const e of entries) {
    const day = localDay(e.time);
    const last = out.at(-1);
    if (last && last.day === day) last.entries.push(e);
    else out.push({ day, entries: [e] });
  }
  return out;
}

/** Adds a live entry to the newest page unless any page already holds it. */
export function insertLive(pages: TimelineList[], e: TimelineEntry): TimelineList[] {
  const key = entryKey(e);
  if (pages.some((p) => p.items.some((x) => entryKey(x) === key))) return pages;
  const [first, ...rest] = pages;
  if (!first) return pages;
  return [{ ...first, items: [e, ...first.items] }, ...rest];
}

/**
 * Flattens loaded pages into one newest-first list. A live insert can shift
 * entries across a page boundary, so the same entry may appear in two pages;
 * the first occurrence wins.
 */
export function flattenTimeline(pages: readonly TimelineList[]): TimelineEntry[] {
  const seen = new Set<string>();
  const out: TimelineEntry[] = [];
  for (const p of pages) {
    for (const e of p.items) {
      const k = entryKey(e);
      if (seen.has(k)) continue;
      seen.add(k);
      out.push(e);
    }
  }
  return out;
}
