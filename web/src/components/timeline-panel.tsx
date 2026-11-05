import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { useMergedTimeline } from "../api/merged-timeline";
import { resourcesQuery } from "../api/queries";
import { formatDay, toLocalInput } from "../lib/format";
import { OBSERVATION_KIND, groupByDay, localDay } from "../lib/timeline";
import { s } from "../strings";
import { TimelineEvents } from "./timeline-list";
import { Empty, ErrorBox, Field, Spinner } from "./ui";

const KINDS = Object.keys(s.timeline.kindNames).filter((k) => k !== OBSERVATION_KIND);

function instant(value: string | null): string | undefined {
  return value && Number.isFinite(Date.parse(value)) ? value : undefined;
}

function dayLabel(day: string, sample: string, now: Date): string {
  const yesterday = new Date(now.getFullYear(), now.getMonth(), now.getDate() - 1);
  if (day === localDay(now.toISOString())) return `${s.timeline.today} · ${formatDay(sample)}`;
  if (day === localDay(yesterday.toISOString())) return `${s.timeline.yesterday} · ${formatDay(sample)}`;
  return formatDay(sample);
}

/**
 * Filterable timeline with infinite scroll: a sentinel below the list loads
 * the next page when it scrolls into view, and a button does the same for
 * keyboard users and browsers without IntersectionObserver. The filters live
 * in the URL (`resource` on the global timeline, `kind` once per kind,
 * `from` and `to` as RFC 3339), and entries are grouped by local day.
 *
 * With `observations` (the selector of a machine or router) its stored
 * observation events are one more kind, merged into the list by time.
 * `note` explains the limits of those events.
 */
export function TimelinePanel({ resource, withResourceFilter, observations, note }: { resource?: string; withResourceFilter?: boolean; observations?: string; note?: string }) {
  const [params, setParams] = useSearchParams();
  const picked = withResourceFilter ? (params.get("resource") ?? "") : "";
  const kindList = observations ? [...KINDS, OBSERVATION_KIND] : KINDS;
  const kinds = kindList.filter((k) => params.getAll("kind").includes(k));
  const from = instant(params.get("from"));
  const to = instant(params.get("to"));
  const effective = resource ?? picked;
  const q = useMergedTimeline({ resource: effective, kinds, from, to, observations });
  const resources = useQuery({ ...resourcesQuery(), enabled: !!withResourceFilter });
  // The time inputs keep what was typed; only a valid, ordered range reaches the URL.
  const [range, setRange] = useState(() => ({ from: toLocalInput(from), to: toLocalInput(to) }));
  const [rangeError, setRangeError] = useState(false);
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

  function update(change: (next: URLSearchParams) => void) {
    const next = new URLSearchParams(params);
    change(next);
    setParams(next, { replace: true });
  }

  function toggle(kind: string) {
    update((next) => {
      next.delete("kind");
      for (const k of kindList) if (k === kind ? !kinds.includes(k) : kinds.includes(k)) next.append("kind", k);
    });
  }

  function setTime(name: "from" | "to", value: string) {
    const typed = { ...range, [name]: value };
    setRange(typed);
    const a = typed.from ? new Date(typed.from) : null;
    const b = typed.to ? new Date(typed.to) : null;
    if ((a && Number.isNaN(a.getTime())) || (b && Number.isNaN(b.getTime())) || (a && b && a >= b)) {
      setRangeError(true);
      return;
    }
    setRangeError(false);
    update((next) => {
      for (const [key, d] of [["from", a], ["to", b]] as const) {
        if (d) next.set(key, d.toISOString());
        else next.delete(key);
      }
    });
  }

  const entries = q.entries;
  const now = new Date();

  return (
    <div>
      <div className="filters">
        {withResourceFilter && (
          <div className="field">
            <label htmlFor="tl-resource">{s.timeline.resource}</label>
            <select id="tl-resource" value={picked} onChange={(e) => update((next) => (e.target.value ? next.set("resource", e.target.value) : next.delete("resource")))}>
              <option value="">{s.timeline.allResources}</option>
              {picked && !(resources.data ?? []).some((x) => x.name === picked) && <option value={picked}>{picked}</option>}
              {(resources.data ?? []).map((x) => (
                <option key={x.name} value={x.name}>
                  {x.name} ({s.resourceType[x.type]})
                </option>
              ))}
            </select>
          </div>
        )}
        <Field label={s.timeline.from} htmlFor="tl-from">
          <input id="tl-from" type="datetime-local" value={range.from} onChange={(e) => setTime("from", e.target.value)} />
        </Field>
        <Field label={s.timeline.to} htmlFor="tl-to">
          <input id="tl-to" type="datetime-local" value={range.to} onChange={(e) => setTime("to", e.target.value)} />
        </Field>
        <fieldset className="plain-fieldset">
          <legend className="meta">
            {s.timeline.kinds}
          </legend>
          <div className="row">
            {kindList.map((k) => (
              <label key={k} className="check">
                <input type="checkbox" checked={kinds.includes(k)} onChange={() => toggle(k)} />
                {s.timeline.kindNames[k]}
              </label>
            ))}
          </div>
        </fieldset>
      </div>
      {rangeError && (
        <p className="banner error" role="alert">
          {s.timeline.rangeInvalid}
        </p>
      )}
      {note && (!kinds.length || kinds.includes(OBSERVATION_KIND)) && <p className="meta">{note}</p>}
      {q.isPending && <Spinner />}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isLoaded && !hasNextPage && entries.length === 0 && <Empty>{s.timeline.empty}</Empty>}
      {groupByDay(entries).map((g) => (
        <section key={g.day} aria-label={dayLabel(g.day, g.entries[0]!.time, now)}>
          <h3 className="day-sep">
            <time dateTime={g.day}>{dayLabel(g.day, g.entries[0]!.time, now)}</time>
          </h3>
          <TimelineEvents entries={g.entries} showResource={!resource} />
        </section>
      ))}
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
