import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { deploymentsQuery } from "../../api/queries";
import type { Deployment } from "../../api/types";
import { BarSeries, CategoryAxis, ChartFrame, ChartKey, TipRow, UsageBar, fmtMs } from "../../components/chart";
import { Async, Empty, Pill, Tag } from "../../components/ui";
import { formatDateTime, formatDuration, formatTime, shortVersion, spanMs } from "../../lib/format";
import { deploymentOutcomeView } from "../../lib/status";
import { s } from "../../strings";

const OUTCOME_COLOR: Record<Deployment["outcome"], string> = { succeeded: "var(--success)", failed: "var(--destructive)", interrupted: "var(--destructive)", running: "var(--info)" };
const MAX_BARS = 30;

/** The deployments to chart: the latest ones, oldest first. */
export function chartedDeployments(items: readonly Deployment[]): Deployment[] {
  return [...items].sort((a, b) => Date.parse(a.startedAt) - Date.parse(b.startedAt)).slice(-MAX_BARS);
}

/** Duration of each deployment as a bar, coloured by outcome, oldest on the left. */
export function DeploymentChart({ items }: { items: readonly Deployment[] }) {
  const shown = chartedDeployments(items);
  const durations = shown.map((d) => spanMs(d.startedAt, d.finishedAt) ?? 0);
  if (shown.length === 0) return null;
  const latest = shown[shown.length - 1];
  return (
    <div className="deploy-chart">
      <ChartFrame
        from={-0.5}
        to={shown.length - 0.5}
        yMax={Math.max(1, ...durations) * 1.05}
        format={fmtMs}
        timeAxis={false}
        times={shown.map((_, i) => i)}
        height={150}
        label={s.chart.label(s.chart.deploy.title, latest ? `${shortVersion(latest.version)} ${fmtMs(durations[durations.length - 1] ?? 0)}` : s.chart.noValue, s.chart.deploy.count(shown.length))}
        tip={(i) => {
          const d = shown[i];
          if (!d) return null;
          return (
            <>
              <div><strong>{shortVersion(d.version)}</strong> {formatDateTime(d.startedAt)}</div>
              <TipRow color={OUTCOME_COLOR[d.outcome]} name={s.deployments.outcomes[d.outcome]} value={fmtMs(durations[i] ?? 0)} />
            </>
          );
        }}
      >
        <CategoryAxis labels={shown.map((d) => formatTime(d.startedAt))} />
        <BarSeries times={shown.map((_, i) => i)} barMs={1} series={[{ name: s.common.duration, color: (i) => OUTCOME_COLOR[shown[i]?.outcome ?? "running"], values: durations }]} />
      </ChartFrame>
      <ChartKey items={(["succeeded", "failed", "running"] as const).map((o) => ({ name: s.deployments.outcomes[o], color: OUTCOME_COLOR[o], swatch: true }))} />
      <p className="meta">{s.chart.deploy.help}</p>
    </div>
  );
}


/** Deployment history of a service, with rollback to an earlier successful commit. */
export function DeploymentsTab({ resource, deployedVersion, hasSource, onDeploy }: { resource: string; deployedVersion: string; hasSource: boolean; onDeploy: (version: string) => void }) {
  const q = useQuery(deploymentsQuery(resource));
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
              <div className="row">
                <span className="meta">{s.deployments.successRate}</span>
                <UsageBar value={(ok / done.length) * 100} label={s.deployments.successRate} tone="neutral" text={`${Math.round((ok / done.length) * 100)}% (${ok}/${done.length})`} />
              </div>
            )}
            <DeploymentChart items={items} />
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
                          <Pill view={deploymentOutcomeView(d.outcome)} />
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
