import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { ReactNode } from "react";
import { eventsQuery, historyQuery } from "../../api/observations";
import type { ResourceSnapshot } from "../../api/types";
import { ErrorBox, Spinner, Tag } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { translate } from "../../lib/messages";
import { s } from "../../strings";

/** The label of an event kind; kinds start with the resource type (machine., router.). */
function eventKind(kind: string): string {
  const event = kind.slice(kind.indexOf(".") + 1);
  return s.observation.eventKinds[event] ?? kind;
}

/** Stored events of a machine or router, oldest first, paged. */
export function ResourceEvents({ resource, note }: { resource: string; note?: string }) {
  const q = useInfiniteQuery(eventsQuery(resource));
  const events = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <section className="panel stack">
      <h2>{s.machine.events}</h2>
      {note && <p className="meta">{note}</p>}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {!q.isPending && !events.length && <p>{s.common.empty}</p>}
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>{s.machine.sourceTime}</th>
              <th>{s.machine.receivedTime}</th>
              <th>{s.machine.source}</th>
              <th>{s.machine.kind}</th>
              <th>{s.common.details}</th>
            </tr>
          </thead>
          <tbody>
            {events.map((ev) => (
              <tr key={ev.id}>
                <td>{formatDateTime(ev.occurredAt)}</td>
                <td>{formatDateTime(ev.ingestedAt)}</td>
                <td>
                  {s.observation.eventSources[ev.source] ?? ev.source}
                  <div>
                    <Tag>{ev.historical ? s.machine.historical : s.machine.live}</Tag>
                  </div>
                </td>
                <td>
                  <span title={ev.kind}>{eventKind(ev.kind)}</span>
                  <div className="meta">{s.observation.severities[ev.severity] ?? ev.severity}</div>
                  {ev.kind.includes("gap") && <Tag>{s.machine.gap}</Tag>}
                </td>
                <td>
                  {ev.reasonMessage ? translate(ev.reasonMessage) : ev.reason}
                  {ev.unit && (
                    <div>
                      <code>{ev.unit}</code>
                    </div>
                  )}
                  {ev.bootId && <div className="meta">{ev.bootId}</div>}
                  {ev.operationId && (
                    <div className="meta">
                      <code>{ev.operationId}</code>
                    </div>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {q.hasNextPage && (
        <button type="button" className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
    </section>
  );
}

interface HistoryProps {
  resource: string;
  /** One-line summary of a snapshot, shown while it is collapsed. Without summary and render the panel lists no samples. */
  summary?: (snapshot: ResourceSnapshot) => ReactNode;
  /** The full snapshot. */
  render?: (snapshot: ResourceSnapshot) => ReactNode;
  /** Optional chart over the loaded snapshots. */
  trend?: (snapshots: ResourceSnapshot[]) => ReactNode;
  note?: string;
}

/** Stored snapshots of a machine or router at raw or minute resolution. */
export function HistoryPanel({ resource, summary, render, trend, note }: HistoryProps) {
  const [resolution, setResolution] = useState<"raw" | "minute">("raw");
  const q = useInfiniteQuery(historyQuery(resource, resolution));
  const snapshots = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <section className="panel stack">
      <h2>{s.machine.history}</h2>
      <div className="field">
        <label htmlFor="history-resolution">{s.machine.resolution}</label>
        <select id="history-resolution" value={resolution} onChange={(e) => setResolution(e.target.value as "raw" | "minute")}>
          <option value="raw">{s.machine.rawHistory}</option>
          <option value="minute">{s.machine.minuteHistory}</option>
        </select>
      </div>
      {note && <p className="meta">{note}</p>}
      {trend?.(snapshots)}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {summary &&
        render &&
        snapshots.map((snapshot) => (
          <details className="panel" key={snapshot.id}>
            <summary>
              {formatDateTime(snapshot.observedAt)} — {summary(snapshot)}
            </summary>
            {render(snapshot)}
          </details>
        ))}
      {q.data && !snapshots.length && <p>{s.common.empty}</p>}
      {q.hasNextPage && (
        <button type="button" className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
    </section>
  );
}
