import type { ResourceDetail } from "../../api/types";
import { HealthPill, ManagedBadges, PhasePill, StatePill, TypeTag } from "../../components/badges";
import { Pill, Tag } from "../../components/ui";
import { formatDateTime, formatTime, shortVersion } from "../../lib/format";
import { s } from "../../strings";
import { OperationCard } from "./operations";

const checkTone = { up: { tone: "ok", icon: "✓" }, down: { tone: "bad", icon: "✕" }, unknown: { tone: "neutral", icon: "?" } } as const;

/** What every resource has: identity, the four status dimensions, checks, alerts and type extras. */
export function OverviewTab({ detail, selector }: { detail: ResourceDetail; selector: string }) {
  const firing = detail.alerts.filter((a) => !a.resolvedAt);
  const last = detail.service?.lastDeployment;
  return (
    <div className="two">
      <section className="panel stack">
        <dl className="kv">
          <dt>{s.resource.id}</dt>
          <dd>
            <code>{detail.id}</code>
          </dd>
          <dt>{s.resources.columns.type}</dt>
          <dd>
            <TypeTag type={detail.type} adapter={detail.adapter} />
          </dd>
          {detail.connection && (
            <>
              <dt>{s.resource.connection}</dt>
              <dd>
                <code>{detail.connection}</code>
              </dd>
            </>
          )}
          <dt>{s.resources.columns.phase}</dt>
          <dd>
            <PhasePill phase={detail.phase} />
          </dd>
          <dt>{s.resources.columns.state}</dt>
          <dd>
            <StatePill state={detail.state} />
            {detail.stateSince && (
              <span className="meta">
                {" "}
                {s.resource.since} {formatTime(detail.stateSince)}
              </span>
            )}
          </dd>
          <dt>{s.resources.columns.health}</dt>
          <dd>
            <HealthPill health={detail.health.status} />
            {detail.health.since && (
              <span className="meta">
                {" "}
                {s.resource.since} {formatTime(detail.health.since)}
              </span>
            )}
            {detail.health.reasons.length > 0 && (
              <ul aria-label={s.resource.healthReasons}>
                {detail.health.reasons.map((r, i) => (
                  <li key={i}>{r}</li>
                ))}
              </ul>
            )}
          </dd>
          <dt>{s.resource.labels}</dt>
          <dd>
            <div className="row" style={{ gap: 4 }}>
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
            <div className="row" style={{ gap: 4 }}>
              <ManagedBadges managedBy={detail.managedBy} />
            </div>
          </dd>
          {detail.service && (
            <>
              <dt>{s.resource.deployedVersion}</dt>
              <dd>
                <code>{shortVersion(detail.service.deployedVersion)}</code>
              </dd>
              <dt>{s.resource.lastDeployment}</dt>
              <dd>
                {last ? `${s.deployments.outcomes[last.outcome] ?? last.outcome} · ${formatTime(last.finishedAt ?? last.startedAt)}` : s.overview.noDeploy}
              </dd>
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
        <section className="panel stack">
          <h2>{s.checks.title}</h2>
          {detail.checks.length === 0 ? (
            <p className="meta">{s.checks.none}</p>
          ) : (
            <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
              {detail.checks.map((c) => {
                const st = c.stale ? "unknown" : c.status;
                return (
                  <li key={c.name} className="row between" style={{ padding: "4px 0" }}>
                    <span>{c.name}</span>
                    <Pill view={{ ...checkTone[st], label: c.stale ? s.checks.stale : s.checks[st] }} title={c.message} />
                  </li>
                );
              })}
            </ul>
          )}
        </section>
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
  );
}
