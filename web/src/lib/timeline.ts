import type { ResourceEvent, TimelineEntry, TimelineList } from "../api/types";
import { s } from "../strings";
import { translate } from "./messages";

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

/** The kind of a machine or router observation event shown among timeline entries. */
export const OBSERVATION_KIND = "observation";

/** The label of an event kind; kinds start with the resource type (machine., router.). */
export function eventKindLabel(kind: string): string {
  const event = kind.slice(kind.indexOf(".") + 1);
  return s.observation.eventKinds[event] ?? kind;
}

/** A stored observation event as a timeline entry, placed at the time it occurred. */
export function observationEntry(ev: ResourceEvent, resource: string): TimelineEntry {
  const reason = ev.reasonMessage ? translate(ev.reasonMessage) : (ev.reason ?? "");
  const label = eventKindLabel(ev.kind);
  const summary = reason ? `${label}：${reason}` : label;
  return {
    id: ev.id,
    resource,
    time: ev.occurredAt,
    kind: OBSERVATION_KIND,
    ref: `observation-${ev.id}`,
    summary,
    summaryMessage: { code: "", params: {}, message: summary },
    data: { severity: ev.severity, source: ev.source, unit: ev.unit ?? "", historical: ev.historical, gap: ev.kind.includes("gap") },
  };
}

export interface TimelineStream {
  /** Newest first, as the server pages it. */
  items: readonly TimelineEntry[];
  /** The server has older entries than the loaded ones. */
  more: boolean;
}

/**
 * Merges newest-first streams that page separately. An entry is only safe to
 * show if no unloaded page can hold something newer: below the newest of the
 * streams' oldest loaded entries (among streams that have more), an
 * unloaded page may still insert entries. Those entries stay hidden until
 * the limiting stream loads its next page, so paging never reorders what is
 * on screen. `limiting` are the streams that must load next to show more.
 */
export function mergeNewestFirst(streams: readonly TimelineStream[]): { entries: TimelineEntry[]; limiting: number[] } {
  let frontier = Number.NEGATIVE_INFINITY;
  let limiting: number[] = [];
  streams.forEach((st, i) => {
    const last = st.items.at(-1);
    if (!st.more || !last) return;
    const t = Date.parse(last.time);
    if (t > frontier) {
      frontier = t;
      limiting = [i];
    } else if (t === frontier) {
      limiting.push(i);
    }
  });
  const entries = streams
    .flatMap((st) => st.items)
    .filter((e) => Date.parse(e.time) >= frontier)
    .sort((a, b) => Date.parse(b.time) - Date.parse(a.time));
  return { entries, limiting };
}
