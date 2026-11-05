import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { deploymentsQuery } from "../../api/queries";
import type { Deployment } from "../../api/types";
import { Async, Empty, Pill, Tag } from "../../components/ui";
import { formatDateTime, formatDuration, formatTime, shortVersion, spanMs } from "../../lib/format";
import { deploymentOutcomeView } from "../../lib/status";
import { s } from "../../strings";


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
