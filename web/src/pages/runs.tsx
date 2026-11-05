import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { policiesQuery, runsQuery } from "../api/queries";
import type { RunStatus, RunSummary } from "../api/types";
import { SortTh, useSort } from "../components/sort-header";
import { Empty, ErrorBox, PageHead, Pill, Spinner, Tag } from "../components/ui";
import { formatDateTime, formatDuration, formatTime, spanMs } from "../lib/format";
import { sortRows } from "../lib/list";
import type { SortValue } from "../lib/list";
import { runStatusView } from "../lib/status";
import { s } from "../strings";

const STATUSES: RunStatus[] = ["queued", "running", "suspended", "succeeded", "failed", "cancelled", "interrupted", "dropped"];

const runSortKeys = ["policy", "version", "status", "created", "duration"] as const;
type RunSortKey = (typeof runSortKeys)[number];
const runSort: Record<RunSortKey, (r: RunSummary) => SortValue> = {
  policy: (r) => r.policy,
  version: (r) => r.version,
  status: (r) => s.runStatus[r.status],
  created: (r) => Date.parse(r.createdAt),
  duration: (r) => (r.startedAt ? spanMs(r.startedAt, r.finishedAt) : null),
};

/**
 * Runs filtered by policy and status, both kept in the URL (`policy`, and
 * `status` once per selected status) so a filtered list can be linked.
 * Sorting applies to the pages loaded so far; the server pages newest first.
 */
export function RunsPage() {
  const [params, setParams] = useSearchParams();
  const policy = params.get("policy") ?? "";
  const status = STATUSES.filter((st) => params.getAll("status").includes(st));
  const { sort, toggle: toggleSort } = useSort(runSortKeys, { key: "created", dir: "desc" });
  const policies = useQuery(policiesQuery());
  const q = useInfiniteQuery(runsQuery({ policy: policy || undefined, status }));
  const rows = sortRows(q.data?.pages.flatMap((p) => p.items) ?? [], sort, runSort);

  function setPolicy(value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set("policy", value);
    else next.delete("policy");
    setParams(next, { replace: true });
  }
  function toggle(st: RunStatus) {
    const next = new URLSearchParams(params);
    next.delete("status");
    for (const x of STATUSES) if (x === st ? !status.includes(x) : status.includes(x)) next.append("status", x);
    setParams(next, { replace: true });
  }
  const th = (key: RunSortKey, label: string, num?: boolean) => <SortTh sortKey={key} label={label} sort={sort} onSort={toggleSort} num={num} />;

  return (
    <div className="page">
      <PageHead title={s.runs.title} />
      <div className="filters">
        <div className="field">
          <label htmlFor="r-policy">{s.runs.filterPolicy}</label>
          <select id="r-policy" value={policy} onChange={(e) => setPolicy(e.target.value)}>
            <option value="">{s.common.all}</option>
            {policy && !(policies.data ?? []).some((p) => p.name === policy) && <option value={policy}>{policy}</option>}
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
                {th("policy", s.runs.columns.policy)}
                {th("version", s.runs.columns.version, true)}
                {th("status", s.runs.columns.status)}
                <th>{s.runs.columns.mode}</th>
                <th>{s.runs.columns.trigger}</th>
                {th("created", s.runs.columns.created)}
                {th("duration", s.runs.columns.duration)}
                <th>{s.runs.columns.error}</th>
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
                    <Pill view={runStatusView(r.status)} />
                  </td>
                  <td>{r.mode === "dry" ? <Tag>{s.runMode["dry"]}</Tag> : s.runMode["live"]}</td>
                  <td>
                    <code>{r.triggerNode}</code>
                  </td>
                  <td title={formatDateTime(r.createdAt)}>{formatTime(r.createdAt)}</td>
                  <td>{r.startedAt ? formatDuration(spanMs(r.startedAt, r.finishedAt)) : "-"}</td>
                  <td className="clip" title={r.error || undefined}>
                    {r.error || ""}
                  </td>
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
