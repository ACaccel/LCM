import { useQueries, useQuery } from "@tanstack/react-query";
import { seriesQuery, summaryQuery } from "../../api/queries";
import type { ResourceDetail } from "../../api/types";
import { ManagedBadges } from "../../components/badges";
import { RANGE_MS } from "../../components/chart";
import { STRIP_CELLS, UptimeStrip } from "../../components/charts";
import { Pill, Tag } from "../../components/ui";
import { actionLabel } from "../../lib/actions";
import { formatDateTime, formatUptime, shortVersion } from "../../lib/format";
import { bucketUptime, mergeSeries } from "../../lib/series";
import { checkView } from "../../lib/status";
import { s } from "../../strings";
import { MachineOverview } from "./machine/monitoring";
import { OperationCard } from "./operations";
import { RouterOverview } from "./router";

/**
 * How many checks pass, each check's state and, for a service, the
 * availability of the last 24 hours as a strip over all its checks.
 */
function ChecksPanel({ detail, selector, strip }: { detail: ResourceDetail; selector: string; strip: boolean }) {
  const checks = detail.checks;
  const up = checks.filter((c) => c.status === "up").length;
  const series = useQueries({ queries: strip ? checks.map((c) => seriesQuery(selector, c.name, RANGE_MS["24h"], 96)) : [] });
  const day = useQuery({ ...summaryQuery(selector, "24h"), enabled: strip && checks.length > 0 });
  const loaded = series.flatMap((q) => (q.data ? [q.data] : []));
  const first = loaded[0];
  const points = mergeSeries(loaded);
  return (
    <section className="panel stack">
      <h2>{s.checks.title}</h2>
      {checks.length === 0 ? (
        <p className="meta">{s.checks.none}</p>
      ) : (
        <>
          <p>
            <b>{s.checks.passing(up, checks.length)}</b>
            {strip && day.data && (
              <span className="meta">
                {" "}
                · {s.checks.uptimeDay} {formatUptime(day.data.uptime, day.data.count)}
              </span>
            )}
          </p>
          <ul className="plain-list">
            {checks.map((c) => (
              <li key={c.name} className="row between list-item">
                <span>{c.name}</span>
                <Pill view={checkView(c)} title={c.message} />
              </li>
            ))}
          </ul>
          {strip && first && <UptimeStrip buckets={bucketUptime(points, Date.parse(first.from), Date.parse(first.to), STRIP_CELLS)} from={Date.parse(first.from)} to={Date.parse(first.to)} />}
        </>
      )}
    </section>
  );
}

/** The last deployment of a service: what ran, how it ended and when. */
function LatestDeployment({ detail }: { detail: ResourceDetail }) {
  const last = detail.service?.lastDeployment;
  return (
    <section className="panel stack">
      <h2>{s.resource.lastDeployment}</h2>
      {last ? (
        <dl className="kv">
          <dt>{s.deployments.commit}</dt>
          <dd>
            <code>{shortVersion(last.version)}</code>
          </dd>
          <dt>{s.deployments.outcome}</dt>
          <dd>
            {actionLabel(last.action)} · {s.deployments.outcomes[last.outcome]}
          </dd>
          <dt>{s.deployments.started}</dt>
          <dd>{formatDateTime(last.finishedAt ?? last.startedAt)}</dd>
        </dl>
      ) : (
        <p className="meta">{s.overview.noDeploy}</p>
      )}
    </section>
  );
}

/**
 * The overview of a resource. The page header already shows phase, state and
 * health, so this holds what the reader checks next: the service's checks
 * and latest deployment, or the machine's or router's usage and trend.
 */
export function OverviewTab({ detail, selector }: { detail: ResourceDetail; selector: string }) {
  const firing = detail.alerts.filter((a) => !a.resolvedAt);
  return (
    <div className="stack">
      {detail.type === "service" && (
        <div className="two">
          <ChecksPanel detail={detail} selector={selector} strip />
          <LatestDeployment detail={detail} />
        </div>
      )}
      {detail.type === "machine" && <MachineOverview resource={detail} selector={selector} />}
      {detail.type === "router" && <RouterOverview resource={detail} selector={selector} />}
      <div className="two">
        <section className="panel stack">
          <dl className="kv">
            <dt>{s.resource.id}</dt>
            <dd>
              <code>{detail.id}</code>
            </dd>
            {detail.connection && (
              <>
                <dt>{s.resource.connection}</dt>
                <dd>
                  <code>{detail.connection}</code>
                </dd>
              </>
            )}
            <dt>{s.resource.labels}</dt>
            <dd>
              <div className="row tight">
                {Object.entries(detail.labels).map(([k, v]) => (
                  <Tag key={k}>{`${k}=${v}`}</Tag>
                ))}
                {Object.keys(detail.labels).length === 0 && s.common.none}
              </div>
            </dd>
            <dt>{s.resource.configVersion}</dt>
            <dd>
              {s.config.version(detail.version)} · {formatDateTime(detail.updatedAt)} · {detail.updatedBy.name}
            </dd>
            <dt>{s.resource.created}</dt>
            <dd>{formatDateTime(detail.createdAt)}</dd>
            <dt>{s.resources.columns.managed}</dt>
            <dd>
              <div className="row tight">
                <ManagedBadges managedBy={detail.managedBy} />
              </div>
            </dd>
            {detail.service && (
              <>
                <dt>{s.resource.deployedVersion}</dt>
                <dd>
                  <code>{shortVersion(detail.service.deployedVersion)}</code>
                </dd>
              </>
            )}
            {detail.stateSince && (
              <>
                <dt>{s.resource.stateSince}</dt>
                <dd>{formatDateTime(detail.stateSince)}</dd>
              </>
            )}
          </dl>
        </section>
        <div className="stack">
          {detail.operation && (
            <section className="stack">
              <h2>{s.resource.openOperation}</h2>
              <OperationCard resource={selector} op={detail.operation} />
            </section>
          )}
          {detail.type !== "service" && <ChecksPanel detail={detail} selector={selector} strip={false} />}
          {firing.length > 0 && (
            <section className="panel stack">
              <h2>
                {s.resource.alerts} {firing.length}
              </h2>
              <ul>
                {firing.map((a) => (
                  <li key={`${a.source}/${a.fingerprint}`}>
                    <b>{a.name}</b> <span className="meta">{a.severity}</span>
                    {a.message && <div className="meta">{a.message}</div>}
                  </li>
                ))}
              </ul>
            </section>
          )}
        </div>
      </div>
    </div>
  );
}
