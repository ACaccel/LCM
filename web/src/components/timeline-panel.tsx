import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { resourcesQuery, timelineQuery } from "../api/queries";
import { flattenTimeline } from "../lib/timeline";
import { s } from "../strings";
import { TimelineEvents } from "./timeline-list";
import { Empty, ErrorBox, Spinner } from "./ui";

const KINDS = Object.keys(s.timeline.kindNames);

/**
 * Filterable timeline with infinite scroll: a sentinel below the list loads
 * the next page when it scrolls into view, and a button does the same for
 * keyboard users and browsers without IntersectionObserver.
 */
export function TimelinePanel({ resource, withResourceFilter }: { resource?: string; withResourceFilter?: boolean }) {
  const [kinds, setKinds] = useState<string[]>([]);
  const [picked, setPicked] = useState("");
  const effective = resource ?? picked;
  const q = useInfiniteQuery(timelineQuery({ resource: effective || undefined, kinds }));
  const resources = useQuery({ ...resourcesQuery(), enabled: !!withResourceFilter });
  const sentinel = useRef<HTMLDivElement>(null);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = q;

  useEffect(() => {
    const el = sentinel.current;
    if (!el || !hasNextPage || typeof IntersectionObserver === "undefined") return;
    const io = new IntersectionObserver((entries) => {
      if (entries.some((e) => e.isIntersecting) && !isFetchingNextPage) void fetchNextPage();
    });
    io.observe(el);
    return () => io.disconnect();
  }, [hasNextPage, isFetchingNextPage, fetchNextPage]);

  function toggle(kind: string) {
    setKinds((cur) => (cur.includes(kind) ? cur.filter((k) => k !== kind) : [...cur, kind]));
  }

  const entries = q.data ? flattenTimeline(q.data.pages) : [];

  return (
    <div>
      <div className="filters">
        {withResourceFilter && (
          <div className="field">
            <label htmlFor="tl-resource">{s.timeline.resource}</label>
            <select id="tl-resource" value={picked} onChange={(e) => setPicked(e.target.value)}>
              <option value="">{s.timeline.allResources}</option>
              {(resources.data ?? []).map((x) => (
                <option key={x.name} value={x.name}>
                  {x.name} ({s.resourceType[x.type]})
                </option>
              ))}
            </select>
          </div>
        )}
        <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="meta" style={{ marginBottom: 4 }}>
            {s.timeline.kinds}
          </legend>
          <div className="row">
            {KINDS.map((k) => (
              <label key={k} className="check">
                <input type="checkbox" checked={kinds.includes(k)} onChange={() => toggle(k)} />
                {s.timeline.kindNames[k]}
              </label>
            ))}
          </div>
        </fieldset>
      </div>
      {q.isPending && <Spinner />}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.data && entries.length === 0 && <Empty>{s.timeline.empty}</Empty>}
      {entries.length > 0 && <TimelineEvents entries={entries} showResource={!resource} />}
      <div ref={sentinel} />
      {hasNextPage && (
        <p>
          <button type="button" className="btn" disabled={isFetchingNextPage} onClick={() => void fetchNextPage()}>
            {isFetchingNextPage ? s.common.loading : s.common.more}
          </button>
        </p>
      )}
    </div>
  );
}
