import { useInfiniteQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { alertListQuery } from "../api/queries";
import type { Alert } from "../api/types";
import { Empty, ErrorBox, PageHead, Pill, Spinner, Tag } from "../components/ui";
import { formatDateTime } from "../lib/format";
import { severityView } from "../lib/status";
import { s } from "../strings";

const STATES = ["firing", "resolved", "all"] as const;
type State = (typeof STATES)[number];

function matches(a: Alert, text: string): boolean {
  const needle = text.trim().toLowerCase();
  if (!needle) return true;
  const labels = Object.entries(a.labels ?? {}).map(([k, v]) => `${k}=${v}`);
  return [a.name, a.resource ?? "", a.source, a.message ?? "", ...labels].some((x) => x.toLowerCase().includes(needle));
}

/**
 * Alerts sent by external systems, firing and resolved, with their source,
 * the resource they map to and their labels. The state is in the URL so a
 * filtered list can be linked; the event stream refreshes it.
 */
export function AlertsPage() {
  const [params, setParams] = useSearchParams();
  const rawState = params.get("state") ?? "";
  const state: State = (STATES as readonly string[]).includes(rawState) ? (rawState as State) : "firing";
  const search = params.get("q") ?? "";
  const q = useInfiniteQuery(alertListQuery({ state }));
  const rows = (q.data?.pages.flatMap((p) => p.items) ?? []).filter((a) => matches(a, search));

  function set(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  }

  return (
    <div className="page">
      <PageHead title={s.alerts.title} />
      <p className="meta">{s.alerts.help}</p>
      <div className="filters">
        <div className="field">
          <label htmlFor="al-state">{s.alerts.state}</label>
          <select id="al-state" value={state} onChange={(e) => set("state", e.target.value === "firing" ? "" : e.target.value)}>
            {STATES.map((x) => (
              <option key={x} value={x}>
                {s.alerts.states[x]}
              </option>
            ))}
          </select>
        </div>
        <div className="field">
          <label htmlFor="al-search">{s.resources.search}</label>
          <input id="al-search" type="search" value={search} onChange={(e) => set("q", e.target.value)} />
        </div>
      </div>
      {q.isPending && <Spinner />}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.data && rows.length === 0 && <Empty>{s.alerts.empty}</Empty>}
      {rows.length > 0 && (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>{s.alerts.columns.severity}</th>
                <th>{s.alerts.columns.name}</th>
                <th>{s.alerts.columns.resource}</th>
                <th>{s.alerts.columns.source}</th>
                <th>{s.alerts.columns.labels}</th>
                <th>{s.alerts.columns.startedAt}</th>
                <th>{s.alerts.columns.resolvedAt}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((a) => (
                <tr key={`${a.source}/${a.fingerprint}`}>
                  <td>
                    <Pill view={severityView(a.severity)} />
                  </td>
                  <td>
                    <strong>{a.name}</strong>
                    {a.message && <div className="meta">{a.message}</div>}
                  </td>
                  <td>{a.resource ? <Link to={`/resources/${encodeURIComponent(a.resource)}`}>{a.resource}</Link> : <span className="meta">{s.alerts.unmapped}</span>}</td>
                  <td>
                    <code>{a.source}</code>
                  </td>
                  <td>
                    <div className="row" style={{ gap: 4 }}>
                      {Object.entries(a.labels ?? {}).map(([k, v]) => (
                        <Tag key={k}>{`${k}=${v}`}</Tag>
                      ))}
                    </div>
                  </td>
                  <td>{formatDateTime(a.startedAt)}</td>
                  <td>{a.resolvedAt ? formatDateTime(a.resolvedAt) : <span className="meta">{s.alerts.stillFiring}</span>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.hasNextPage && (
        <button type="button" className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
    </div>
  );
}
