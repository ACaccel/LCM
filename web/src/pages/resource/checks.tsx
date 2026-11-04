import { useQueries, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { checksQuery, seriesQuery, summaryQuery } from "../../api/queries";
import type { CheckStatus, SeriesPoint } from "../../api/types";
import { ChartLegend, LatencyChart, UptimeBars } from "../../components/charts";
import { Async, Empty, ErrorBox, Pill, Spinner } from "../../components/ui";
import { formatDuration, formatTime, formatUptime } from "../../lib/format";
import { bucketUptime, latencyStats, overallUptime } from "../../lib/series";
import { s } from "../../strings";

type WindowKey = "24h" | "7d" | "30d";
const WINDOWS: Record<WindowKey, number> = { "24h": 86_400_000, "7d": 7 * 86_400_000, "30d": 30 * 86_400_000 };

function checkView(c: CheckStatus) {
  if (c.stale || c.status === "unknown") return { tone: "neutral" as const, icon: "?", label: c.stale ? s.checks.stale : s.checks.unknown };
  return c.status === "up" ? { tone: "ok" as const, icon: "✓", label: s.checks.up } : { tone: "bad" as const, icon: "✕", label: s.checks.down };
}

const BARS = 48;

function panelStats(points: readonly SeriesPoint[]): string {
  const uptime = `${s.checks.uptime} ${formatUptime(overallUptime(points), points.reduce((n, p) => n + p.count, 0))}`;
  const lat = latencyStats(points);
  if (!lat) return uptime;
  return `${s.checks.avg} ${formatDuration(lat.avg)} · ${s.checks.max} ${formatDuration(lat.max)} · ${uptime}`;
}

function CheckPanel({ resource, check, windowKey }: { resource: string; check: CheckStatus; windowKey: WindowKey }) {
  const points = windowKey === "24h" ? 96 : 120;
  const q = useQuery(seriesQuery(resource, check.name, WINDOWS[windowKey], points));
  const from = q.data ? Date.parse(q.data.from) : 0;
  const to = q.data ? Date.parse(q.data.to) : 0;
  return (
    <div className="panel">
      <div className="title">
        <span>{s.checks.latencyChart(check.name)}</span>
        {q.data && <span>{panelStats(q.data.points)}</span>}
      </div>
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <>
          <LatencyChart points={q.data.points} from={from} to={to} stepMs={q.data.stepMs} />
          <UptimeBars buckets={bucketUptime(q.data.points, from, to, BARS)} from={from} to={to} />
          <ChartLegend bucketMs={(to - from) / BARS} />
        </>
      )}
    </div>
  );
}

/** Checks of any resource type: current results, uptime windows and latency charts. */
export function ChecksTab({ resource }: { resource: string }) {
  const [win, setWin] = useState<WindowKey>("24h");
  const checks = useQuery(checksQuery(resource));
  const summaries = useQueries({ queries: (["24h", "7d", "30d"] as const).map((w) => summaryQuery(resource, w)) });
  return (
    <Async q={checks}>
      {(data) =>
        data.checks.length === 0 ? (
          <Empty>{s.checks.none}</Empty>
        ) : (
          <div className="stack">
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.checks.name}</th>
                    <th>{s.checks.type}</th>
                    <th>{s.checks.status}</th>
                    <th className="num">{s.checks.latency}</th>
                    <th className="num">{s.checks.failures}</th>
                    {(["24h", "7d", "30d"] as const).map((w) => (
                      <th key={w} className="num">
                        {s.checks.uptime} {s.checks.window[w]}
                      </th>
                    ))}
                    <th>{s.checks.lastResult}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.checks.map((c) => (
                    <tr key={c.name}>
                      <td>
                        {c.name}
                        {!c.critical && <span className="meta"> ({s.checks.nonCritical})</span>}
                      </td>
                      <td>{c.type}</td>
                      <td>
                        <Pill view={checkView(c)} title={c.message} />
                        {c.message && <div className="meta">{c.message}</div>}
                      </td>
                      <td className="num">{c.status === "up" ? formatDuration(c.lastLatencyMs) : "-"}</td>
                      <td className="num">{c.consecutiveFailures}</td>
                      {summaries.map((sm, i) => {
                        const st = sm.data?.checks[c.name];
                        return (
                          <td key={i} className="num">
                            {st ? formatUptime(st.uptime, st.count) : "-"}
                          </td>
                        );
                      })}
                      <td>{formatTime(c.lastResult)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <div className="row">
              <span className="meta">{s.checks.latency}</span>
              {(["24h", "7d", "30d"] as const).map((w) => (
                <button key={w} type="button" className="btn small" aria-pressed={win === w} style={win === w ? { borderColor: "var(--primary)" } : undefined} onClick={() => setWin(w)}>
                  {s.checks.window[w]}
                </button>
              ))}
            </div>
            <div className="cards" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(280px, 1fr))" }}>
              {data.checks.map((c) => (
                <CheckPanel key={`${c.name}-${win}`} resource={resource} check={c} windowKey={win} />
              ))}
            </div>
          </div>
        )
      }
    </Async>
  );
}
