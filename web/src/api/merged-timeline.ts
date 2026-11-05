import { useInfiniteQuery } from "@tanstack/react-query";
import { useMemo } from "react";
import { OBSERVATION_KIND, flattenTimeline, mergeNewestFirst, observationEntry } from "../lib/timeline";
import { eventsQuery } from "./observations";
import { timelineQuery } from "./queries";
import type { TimelineEntry } from "./types";

export interface MergedTimelineFilter {
  /** Name of one resource; empty for all resources. */
  resource?: string;
  /** Selected kinds, which may include `observation`; empty selects everything. */
  kinds: readonly string[];
  from?: string;
  to?: string;
  /** Selector of the machine or router whose observation events join the timeline. */
  observations?: string;
}

/**
 * The timeline entries and, for a machine or router, its observation events
 * as one newest-first list. The two are paged by separate endpoints; each
 * keeps its own cursor and the lists are merged by time (see
 * `mergeNewestFirst` for what is held back until the other has loaded).
 */
export function useMergedTimeline({ resource, kinds, from, to, observations }: MergedTimelineFilter) {
  const timelineKinds = kinds.filter((k) => k !== OBSERVATION_KIND);
  const wantTimeline = kinds.length === 0 || timelineKinds.length > 0;
  const wantEvents = !!observations && (kinds.length === 0 || kinds.includes(OBSERVATION_KIND));
  const timeline = useInfiniteQuery({ ...timelineQuery({ resource: resource || undefined, kinds: timelineKinds, from, to }), enabled: wantTimeline });
  const events = useInfiniteQuery({ ...eventsQuery(observations ?? "", { from, to }), enabled: wantEvents });

  const merged = useMemo(() => {
    const name = resource ?? "";
    // A disabled query may still hold cached pages of an earlier filter.
    const timelineItems = wantTimeline && timeline.data ? flattenTimeline(timeline.data.pages) : [];
    const seen = new Set<number>();
    const eventItems: TimelineEntry[] = [];
    for (const page of wantEvents && events.data ? events.data.pages : []) {
      for (const ev of page.items) {
        if (seen.has(ev.id)) continue;
        seen.add(ev.id);
        eventItems.push(observationEntry(ev, name));
      }
    }
    return mergeNewestFirst([
      { items: timelineItems, more: wantTimeline && timeline.hasNextPage },
      { items: eventItems, more: wantEvents && events.hasNextPage },
    ]);
  }, [resource, wantTimeline, wantEvents, timeline.data, timeline.hasNextPage, events.data, events.hasNextPage]);

  const active = [...(wantTimeline ? [timeline] : []), ...(wantEvents ? [events] : [])];
  const more = [wantTimeline && timeline.hasNextPage, wantEvents && events.hasNextPage];
  const hasNextPage = more.some(Boolean);
  const isFetchingNextPage = active.some((q) => q.isFetchingNextPage);

  /** Loads the next page of the stream that bounds what can be shown. */
  function fetchNextPage() {
    const targets = merged.limiting.length ? merged.limiting : more.flatMap((m, i) => (m ? [i] : []));
    for (const i of targets) void (i === 0 ? timeline : events).fetchNextPage();
  }

  return {
    entries: merged.entries,
    isPending: active.some((q) => q.isPending),
    isLoaded: active.length > 0 && active.every((q) => q.data !== undefined),
    error: active.find((q) => q.error)?.error ?? null,
    refetch: () => Promise.all(active.map((q) => q.refetch())),
    hasNextPage,
    isFetchingNextPage,
    fetchNextPage,
  };
}
