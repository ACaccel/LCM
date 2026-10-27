import type { TimelineEntry, TimelineList } from "../api/types";

/**
 * Identity of a timeline entry. The row id is not used because live
 * `timeline` events may carry id 0 for entries whose id is not known yet;
 * the remaining fields identify an entry just as well and stay stable when
 * the same entry is later loaded from a page.
 */
export function entryKey(e: TimelineEntry): string {
  return [e.service, e.time, e.kind, e.ref, e.summary].join("\u0000");
}

/** Whether a live entry belongs in a timeline query with this service and kind filter. */
export function matchesFilter(e: TimelineEntry, service: string, kinds: readonly string[]): boolean {
  if (service && e.service !== service) return false;
  return kinds.length === 0 || kinds.includes(e.kind);
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
