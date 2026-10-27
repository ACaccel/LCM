import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { policiesQuery, runsQuery } from "../api/queries";
import type { RunStatus } from "../api/types";
import { Empty, ErrorBox, PageHead, Pill, Spinner, Tag } from "../components/ui";
import { formatDateTime, formatDuration, formatTime, spanMs } from "../lib/format";
import { runStatusView } from "../lib/status";
import { s } from "../strings";

const STATUSES: RunStatus[] = ["queued", "running", "suspended", "succeeded", "failed", "cancelled", "interrupted", "dropped"];

export function RunsPage() {
  const [policy, setPolicy] = useState("");
  const [status, setStatus] = useState<RunStatus[]>([]);
  const policies = useQuery(policiesQuery());
  const q = useInfiniteQuery(runsQuery({ policy: policy || undefined, status }));
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];

  const toggle = (st: RunStatus) => setStatus((cur) => (cur.includes(st) ? cur.filter((x) => x !== st) : [...cur, st]));

  return (
    <div className="page">
      <PageHead title={s.runs.title} />
      <div className="filters">
        <div className="field">
          <label htmlFor="r-policy">{s.runs.filterPolicy}</label>
          <select id="r-policy" value={policy} onChange={(e) => setPolicy(e.target.value)}>
            <option value="">{s.common.all}</option>
            {(policies.data ?? []).map((p) => (
              <option key={p.name} value={p.name}>
                {p.name}
              </option>
            ))}
          </select>
        </div>
        <fieldset style={{ border: 0, padding: 0, margin: 0 }}>
          <legend className="meta" style={{ marginBottom: 4 }}>
            {s.runs.filterStatus}
          </legend>
          <div className="row">
            {STATUSES.map((st) => (
              <label key={st} className="check">
                <input type="checkbox" checked={status.includes(st)} onChange={() => toggle(st)} />
                {s.runStatus[st]}
              </label>
            ))}
          </div>
        </fieldset>
      </div>
      {q.isPending && <Spinner />}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.data && rows.length === 0 && <Empty>{s.runs.empty}</Empty>}
      {rows.length > 0 && (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>{s.runs.columns.id}</th>
                <th>{s.runs.columns.policy}</th>
                <th className="num">{s.runs.columns.version}</th>
                <th>{s.runs.columns.status}</th>
                <th>{s.runs.columns.mode}</th>
                <th>{s.runs.columns.trigger}</th>
                <th>{s.runs.columns.created}</th>
                <th>{s.runs.columns.duration}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id}>
                  <td>
                    <Link to={`/runs/${encodeURIComponent(r.id)}`}>
                      <code>{r.id.slice(0, 8)}</code>
                    </Link>
                  </td>
                  <td>
                    <Link to={`/policies/${encodeURIComponent(r.policy)}`}>{r.policy}</Link>
                  </td>
                  <td className="num">{r.version}</td>
                  <td>
                    <Pill view={runStatusView(r.status)} title={r.error} />
                  </td>
                  <td>{r.mode === "dry" ? <Tag>{s.runMode["dry"]}</Tag> : s.runMode["live"]}</td>
                  <td>
                    <code>{r.triggerNode}</code>
                  </td>
                  <td title={formatDateTime(r.createdAt)}>{formatTime(r.createdAt)}</td>
                  <td>{r.startedAt ? formatDuration(spanMs(r.startedAt, r.finishedAt)) : "-"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.hasNextPage && (
        <p>
          <button type="button" className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
            {q.isFetchingNextPage ? s.common.loading : s.common.more}
          </button>
        </p>
      )}
    </div>
  );
}
