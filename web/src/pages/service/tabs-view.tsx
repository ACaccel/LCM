import { useQueries, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { checksQuery, deploymentsQuery, policiesQuery, seriesQuery, summaryQuery } from "../../api/queries";
import type { CheckStatus, Deployment, ServiceDetail } from "../../api/types";
import { LatencyChart, UptimeBars } from "../../components/charts";
import { Async, Empty, ErrorBox, Pill, Spinner, Tag } from "../../components/ui";
import { formatDateTime, formatDuration, formatTime, formatUptime, shortVersion, spanMs } from "../../lib/format";
import { policyRelation, type PolicyRelation } from "../../lib/policy-match";
import { TemplateDialog } from "../template-dialog";
import { bucketUptime, overallUptime } from "../../lib/series";
import { s } from "../../strings";
import { ManagedBadges } from "../../components/badges";

type WindowKey = "24h" | "7d" | "30d";
const WINDOWS: Record<WindowKey, number> = { "24h": 86_400_000, "7d": 7 * 86_400_000, "30d": 30 * 86_400_000 };

function checkView(c: CheckStatus) {
  if (c.stale || c.status === "unknown") return { tone: "neutral" as const, icon: "?", label: c.stale ? s.checks.stale : s.checks.unknown };
  return c.status === "up" ? { tone: "ok" as const, icon: "✓", label: s.checks.up } : { tone: "bad" as const, icon: "✕", label: s.checks.down };
}

function CheckPanel({ service, check, windowKey }: { service: string; check: CheckStatus; windowKey: WindowKey }) {
  const points = windowKey === "24h" ? 96 : 120;
  const q = useQuery(seriesQuery(service, check.name, WINDOWS[windowKey], points));
  const from = q.data ? Date.parse(q.data.from) : 0;
  const to = q.data ? Date.parse(q.data.to) : 0;
  return (
    <div className="panel">
      <div className="title">
        <span>{s.checks.latencyChart(check.name)}</span>
        {q.data && (
          <span>
            {s.checks.uptime} {formatUptime(overallUptime(q.data.points), q.data.points.reduce((n, p) => n + p.count, 0))}
          </span>
        )}
      </div>
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      ) : (
        <>
          <LatencyChart points={q.data.points} from={from} to={to} />
          <UptimeBars buckets={bucketUptime(q.data.points, from, to, 48)} />
        </>
      )}
    </div>
  );
}

export function ChecksTab({ service }: { service: string }) {
  const [win, setWin] = useState<WindowKey>("24h");
  const checks = useQuery(checksQuery(service));
  const summaries = useQueries({ queries: (["24h", "7d", "30d"] as const).map((w) => summaryQuery(service, w)) });
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
                <CheckPanel key={`${c.name}-${win}`} service={service} check={c} windowKey={win} />
              ))}
            </div>
          </div>
        )
      }
    </Async>
  );
}

const outcomeTone = { running: "info", succeeded: "ok", failed: "bad", interrupted: "bad" } as const;
const outcomeIcon = { running: "↻", succeeded: "✓", failed: "✕", interrupted: "✕" } as const;

export function DeploymentsTab({ service, deployedVersion, hasSource, onDeploy }: { service: string; deployedVersion: string; hasSource: boolean; onDeploy: (version: string) => void }) {
  const q = useQuery(deploymentsQuery(service));
  return (
    <Async q={q}>
      {(data) => {
        const items: Deployment[] = data.items;
        if (items.length === 0) return <Empty>{s.deployments.empty}</Empty>;
        const done = items.filter((d) => d.outcome !== "running");
        const ok = done.filter((d) => d.outcome === "succeeded").length;
        return (
          <div className="stack">
            {done.length > 0 && (
              <div className="meta">
                {s.deployments.successRate} {Math.round((ok / done.length) * 100)}% ({ok}/{done.length})
              </div>
            )}
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.deployments.commit}</th>
                    <th>{s.deployments.started}</th>
                    <th>{s.common.duration}</th>
                    <th>{s.deployments.outcome}</th>
                    <th>{s.common.actor}</th>
                    <th>{s.common.actions}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((d) => {
                    const canRollback = hasSource && d.outcome === "succeeded" && d.version && d.version !== deployedVersion;
                    return (
                      <tr key={d.id}>
                        <td>
                          <code title={d.version}>{shortVersion(d.version)}</code>
                          {d.action === "rollback" && <Tag>{s.actionName["rollback"]}</Tag>}
                          {d.previousVersion && (
                            <div className="meta">
                              {s.deployments.previous} <code>{shortVersion(d.previousVersion)}</code>
                            </div>
                          )}
                        </td>
                        <td title={formatDateTime(d.startedAt)}>{formatTime(d.startedAt)}</td>
                        <td>{formatDuration(spanMs(d.startedAt, d.finishedAt))}</td>
                        <td>
                          <Pill view={{ tone: outcomeTone[d.outcome], icon: outcomeIcon[d.outcome], label: s.deployments.outcomes[d.outcome] ?? d.outcome }} />
                          {d.error && <div className="meta">{d.error}</div>}
                          {d.logExcerpt && (
                            <details>
                              <summary className="meta">{s.deployments.log}</summary>
                              <pre className="block">{d.logExcerpt}</pre>
                            </details>
                          )}
                        </td>
                        <td>{d.actor.name}</td>
                        <td>
                          <button type="button" className="btn small" disabled={!canRollback} title={canRollback ? undefined : s.deployments.rollbackDisabled} onClick={() => onDeploy(d.version)}>
                            {s.deployments.rollbackTo}
                          </button>
                          {d.runId && (
                            <>
                              {" "}
                              <Link to={`/runs/${encodeURIComponent(d.runId)}`}>{s.common.run}</Link>
                            </>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          </div>
        );
      }}
    </Async>
  );
}

function relationText(r: PolicyRelation): string {
  if (r.kind === "dynamic") return s.servicePolicies.relation.dynamic;
  if (r.kind === "watches") return s.servicePolicies.relation.watches;
  return s.servicePolicies.relation.acts(r.types.map((t) => s.actionName[t.replace(/^lifecycle\./, "")] ?? t).join("、"));
}

export function PoliciesTab({ service, detail }: { service: string; detail: ServiceDetail }) {
  const q = useQuery(policiesQuery());
  const [applying, setApplying] = useState(false);
  return (
    <Async q={q}>
      {(policies) => {
        const labels = detail.document.metadata.labels ?? {};
        const matched = policies.flatMap((p) => {
          const relation = policyRelation(p, service, labels);
          return relation ? [{ policy: p, relation }] : [];
        });
        return (
          <div className="stack">
            <div className="row">
              <p className="meta grow">{s.servicePolicies.help}</p>
              <button type="button" className="btn primary" onClick={() => setApplying(true)}>
                {s.servicePolicies.applyTemplate}
              </button>
            </div>
            {applying && <TemplateDialog service={service} onClose={() => setApplying(false)} />}
            {matched.length === 0 ? (
              <Empty>{s.servicePolicies.empty}</Empty>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th>{s.servicePolicies.columns.name}</th>
                      <th>{s.servicePolicies.columns.relation}</th>
                      <th>{s.servicePolicies.columns.state}</th>
                      <th>{s.servicePolicies.columns.managed}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {matched.map(({ policy: p, relation }) => (
                      <tr key={p.name}>
                        <td>
                          <Link to={`/policies/${encodeURIComponent(p.name)}`}>{p.name}</Link>
                        </td>
                        <td>{relationText(relation)}</td>
                        <td>{p.paused ? s.policies.paused : s.policies.deployed}</td>
                        <td>
                          <div className="row" style={{ gap: 4 }}>
                            <ManagedBadges managedBy={p.managedBy} />
                          </div>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        );
      }}
    </Async>
  );
}
