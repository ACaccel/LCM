import { describe, expect, it } from "vitest";
import type { TimelineEntry, TimelineList } from "../api/types";
import { OBSERVATION_KIND, entryKey, flattenTimeline, groupByDay, insertLive, localDay, matchesFilter, mergeNewestFirst, observationEntry } from "./timeline";

const e = (n: number, over: Partial<TimelineEntry> = {}): TimelineEntry => ({
  id: n,
  resource: "web",
  time: `2026-10-28T10:00:${String(60 - n).padStart(2, "0")}Z`.replace(":60Z", ":59Z"),
  kind: "action",
  summary: `entry ${n}`,
  summaryMessage: { code: "", params: {}, message: `entry ${n}` },
  data: {},
  ref: String(n),
  ...over,
});

const page = (items: TimelineEntry[], next: string | null = null): TimelineList => ({ items, nextCursor: next });

describe("timeline merge", () => {
  it("flattens pages in order and drops entries repeated across a page boundary", () => {
    const pages = [page([e(5), e(4), e(3)], "c1"), page([e(3), e(2), e(1)])];
    expect(flattenTimeline(pages).map((x) => x.id)).toEqual([5, 4, 3, 2, 1]);
  });

  it("inserts a live entry into the newest page only", () => {
    const pages = [page([e(2)], "c"), page([e(1)])];
    const next = insertLive(pages, e(3));
    expect(next[0]?.items.map((x) => x.id)).toEqual([3, 2]);
    expect(next[1]).toBe(pages[1]);
  });

  it("does not insert an entry that any page already has, whatever its id", () => {
    const pages = [page([e(2)], "c"), page([e(1)])];
    expect(insertLive(pages, { ...e(1), id: 0 })).toBe(pages);
  });

  it("keeps a live entry and its later loaded copy as one", () => {
    const live = { ...e(7), id: 0 };
    const merged = flattenTimeline([page([live, e(6)], "c"), page([e(7), e(5)])]);
    expect(merged.map((x) => x.summary)).toEqual(["entry 7", "entry 6", "entry 5"]);
  });

  it("matches entries against resource and kind filters", () => {
    expect(matchesFilter(e(1), "", [])).toBe(true);
    expect(matchesFilter(e(1), "web", ["action"])).toBe(true);
    expect(matchesFilter(e(1), "api", [])).toBe(false);
    expect(matchesFilter(e(1), "", ["alert"])).toBe(false);
  });

  it("matches entries against a time range, from inclusive and to exclusive", () => {
    const at = { ...e(1), time: "2026-10-28T10:00:00Z" };
    expect(matchesFilter(at, "", [], "2026-10-28T10:00:00Z", "")).toBe(true);
    expect(matchesFilter(at, "", [], "2026-10-28T10:00:01Z", "")).toBe(false);
    expect(matchesFilter(at, "", [], "", "2026-10-28T10:00:00Z")).toBe(false);
    expect(matchesFilter(at, "", [], "2026-10-28T09:00:00Z", "2026-10-28T11:00:00Z")).toBe(true);
  });

  it("groups a newest-first list by local day", () => {
    const at = (iso: string, n: number) => ({ ...e(n), time: new Date(iso).toISOString() });
    const list = [at("2026-10-29T08:00:00", 4), at("2026-10-29T01:00:00", 3), at("2026-10-28T23:00:00", 2), at("2026-10-27T12:00:00", 1)];
    const groups = groupByDay(list);
    expect(groups.map((g) => g.day)).toEqual(["2026-10-29", "2026-10-28", "2026-10-27"]);
    expect(groups.map((g) => g.entries.map((x) => x.id))).toEqual([[4, 3], [2], [1]]);
    expect(localDay(list[0]!.time)).toBe("2026-10-29");
  });

  it("gives different entries different keys", () => {
    expect(entryKey(e(1))).not.toBe(entryKey(e(2)));
  });
});

describe("merging newest-first streams", () => {
  const at = (minute: number, name: string): TimelineEntry => e(minute, { time: `2026-10-28T10:${String(minute).padStart(2, "0")}:00Z`, summary: name, ref: name });
  const names = (entries: readonly TimelineEntry[]) => entries.map((x) => x.summary);

  it("interleaves the streams by time", () => {
    const merged = mergeNewestFirst([
      { items: [at(50, "a1"), at(30, "a2")], more: false },
      { items: [at(40, "b1"), at(20, "b2")], more: false },
    ]);
    expect(names(merged.entries)).toEqual(["a1", "b1", "a2", "b2"]);
    expect(merged.limiting).toEqual([]);
  });

  it("holds back what an unloaded page of the other stream could still precede", () => {
    // The timeline reached 30; events not yet loaded below 40 may hold entries between.
    const merged = mergeNewestFirst([
      { items: [at(50, "a1"), at(30, "a2")], more: true },
      { items: [at(40, "b1"), at(20, "b2")], more: true },
    ]);
    expect(names(merged.entries)).toEqual(["a1", "b1", "a2"]);
    // a2 is the newest of the oldest loaded entries, so its stream loads next.
    expect(merged.limiting).toEqual([0]);
  });

  it("shows everything of a stream that has no more once the other has loaded further", () => {
    const merged = mergeNewestFirst([
      { items: [at(50, "a1"), at(30, "a2"), at(10, "a3")], more: false },
      { items: [at(40, "b1"), at(20, "b2")], more: true },
    ]);
    expect(names(merged.entries)).toEqual(["a1", "b1", "a2", "b2"]);
    expect(merged.limiting).toEqual([1]);
  });

  it("works with one stream or none", () => {
    expect(names(mergeNewestFirst([{ items: [at(2, "x"), at(1, "y")], more: true }, { items: [], more: false }]).entries)).toEqual(["x", "y"]);
    expect(mergeNewestFirst([{ items: [], more: false }]).entries).toEqual([]);
  });

  it("names both streams as limiting when they stop at the same time", () => {
    const merged = mergeNewestFirst([
      { items: [at(50, "a1"), at(30, "a2")], more: true },
      { items: [at(40, "b1"), at(30, "b2")], more: true },
    ]);
    expect(merged.limiting).toEqual([0, 1]);
    expect(names(merged.entries)).toEqual(["a1", "b1", "a2", "b2"]);
  });
});

describe("observation entries", () => {
  it("places an event where it occurred and explains it with its translated reason", () => {
    const entry = observationEntry(
      {
        id: 7,
        resourceId: "m-1",
        source: "journal",
        kind: "machine.journal.gap",
        severity: "warning",
        occurredAt: "2026-10-28T09:00:00Z",
        ingestedAt: "2026-10-28T10:00:00Z",
        historical: true,
        reason: "journal cursor expired",
      },
      "workstation",
    );
    expect(entry).toMatchObject({ resource: "workstation", time: "2026-10-28T09:00:00Z", kind: OBSERVATION_KIND });
    expect(entry.summary).toContain("journal cursor expired");
    expect(entry.data).toMatchObject({ gap: true, historical: true, severity: "warning" });
  });
});
