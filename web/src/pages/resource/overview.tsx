import { useQueries, useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { seriesQuery, summaryQuery } from "../../api/queries";
import type { ResourceDetail, ServiceInstance } from "../../api/types";
import { ManagedBadges } from "../../components/badges";
import { RANGE_MS } from "../../components/chart";
import { STRIP_CELLS, UptimeStrip } from "../../components/charts";
import { Pill, Tag } from "../../components/ui";
import { actionLabel } from "../../lib/actions";
import { formatDateTime, formatUptime, shortVersion } from "../../lib/format";
import { bucketUptime, mergeSeries } from "../../lib/series";
import { checkView, healthView } from "../../lib/status";
import { s } from "../../strings";
import { ActionDialog } from "./action-dialog";
import { HostDriftNotice } from "./machine/host-drift";
import { MachineOverview } from "./machine/monitoring";
import { OperationCard } from "./operations";
import { RouterOverview } from "./router";
import { RoutePanel } from "./route";

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
                <span className="row tight">
                  <span>{c.name}</span>
                  {c.instance && <Tag title={s.instances.checkInstance}>{c.instance}</Tag>}
                  {c.paused && <Tag title={c.message}>{s.checks.paused}</Tag>}
                </span>
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

type InstanceAction = "start" | "stop" | "restart";

/**
 * The instances of a managed service: each one's state, whether an operator
 * stopped it, its health from its own checks, and restart, stop and start
 * of that instance alone through the action dialog.
 */
export function InstancesPanel({ detail, instances }: { detail: ResourceDetail; instances: ServiceInstance[] }) {
  const [open, setOpen] = useState<{ action: InstanceAction; instance: string } | null>(null);
  return (
    <section className="panel stack">
      <h2>{s.instances.title}</h2>
      <table className="table">
        <thead>
          <tr>
            <th>{s.instances.name}</th>
            <th>{s.instances.port}</th>
            <th>{s.instances.state}</th>
            <th>{s.instances.health}</th>
            <th>{s.instances.actions}</th>
          </tr>
        </thead>
        <tbody>
          {instances.map((i) => (
            <tr key={i.name}>
              <td>
                <code>{i.name}</code>
              </td>
              <td>{i.port ?? s.common.none}</td>
              <td>
                <span className="row tight">
                  <span>{s.instances.states[i.state] ?? i.state}</span>
                  {i.stopped && <Tag title={s.instances.disabledHint}>{s.instances.disabled}</Tag>}
                </span>
              </td>
              <td>
                <Pill view={healthView(i.health)} title={i.checks.join(", ")} />
              </td>
              <td>
                <div className="row tight">
                  {i.actions.map((a) => (
                    <button
                      key={a.name}
                      type="button"
                      className="btn small"
                      disabled={!a.available}
                      title={a.available ? undefined : (s.instances.unavailable[a.reason] ?? a.reason)}
                      onClick={() => setOpen({ action: a.name, instance: i.name })}
                    >
                      {s.actionName[a.name] ?? a.name}
                    </button>
                  ))}
                </div>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {open && <ActionDialog resource={detail.name} action={open.action} instance={open.instance} onClose={() => setOpen(null)} />}
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
      {detail.service?.instances && <InstancesPanel detail={detail} instances={detail.service.instances} />}
      {detail.type === "machine" && <HostDriftNotice />}
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
          {detail.type === "service" && <RoutePanel detail={detail} />}
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
