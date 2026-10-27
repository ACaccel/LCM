import { useEffect, useState } from "react";
import { useQueries, useQuery } from "@tanstack/react-query";
import { seriesQuery, serviceQuery } from "./api/queries";
import { bucketUptime, mergeSeries, overallUptime } from "./lib/series";
import type { Bucket } from "./lib/series";

const DAY = 24 * 3_600_000;

/** Hourly availability over the last 24 hours, combined over all checks of a service. */
export function useServiceSpark(name: string): { buckets: Bucket[]; uptime: number | undefined; hasChecks: boolean; pending: boolean } {
  const detail = useQuery(serviceQuery(name));
  const checks = detail.data?.checks ?? [];
  const series = useQueries({ queries: checks.map((c) => seriesQuery(name, c.name, DAY, 24)) });
  const loaded = series.map((q) => q.data).filter((d): d is NonNullable<typeof d> => !!d);
  const first = loaded[0];
  // 24 buckets over at most a few hundred points: cheaper than memoizing.
  const merged = mergeSeries(loaded);
  const buckets = first ? bucketUptime(merged, Date.parse(first.from), Date.parse(first.to), 24) : [];

  return { buckets, uptime: overallUptime(merged), hasChecks: checks.length > 0, pending: detail.isPending || series.some((q) => q.isPending) };
}

/** The current time, refreshed every second while `active`, for durations of things still running. */
export function useNow(active: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [active]);
  return now;
}
