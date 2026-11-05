import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { overviewQuery, timelineQuery } from "../api/queries";
import type { Health, Overview } from "../api/types";
import { HealthPill, ManagedBadges, StatePill, TypeTag } from "../components/badges";
import { Sparkline } from "../components/charts";
import { TimelineEvents } from "../components/timeline-list";
import { Async, Empty, ErrorBox, PageHead, Spinner } from "../components/ui";
import { formatTime, formatUptime, shortVersion, startOfDay } from "../lib/format";
import { translateAll } from "../lib/messages";
import { healthView } from "../lib/status";
import { s } from "../strings";

type OverviewResource = Overview["resources"][number];
type Attention = Overview["attention"][number];

function Stat({ value, label, tone, to }: { value: number | string; label: string; tone?: "warn" | "bad"; to?: string }) {
  const body = (
    <>
      <b className={tone ? `tone-${tone}` : undefined}>{value}</b>
      <span>{label}</span>
    </>
  );
  return to ? (
    <Link to={to} className="stat">
      {body}
    </Link>
  ) : (
    <div className="stat">{body}</div>
  );
}

function Availability({ r }: { r: OverviewResource }) {
  const tone = healthView(r.health).tone;
  return r.buckets.length > 0 ? (
    <>
      <Sparkline buckets={r.buckets} tone={tone === "info" ? "neutral" : tone} />
      <div className="meta">
        {s.overview.uptime24h} {formatUptime(r.uptime, r.uptime == null ? 0 : 1)}
      </div>
    </>
  ) : (
    <div className="meta slot">
      {r.hasChecks ? s.overview.noUptimeData : s.checks.none}
    </div>
  );
}

/** A resource that needs a look: why, since when, and a day of availability. */
function AttentionCard({ item, resource }: { item: Attention; resource: OverviewResource | undefined }) {
  return (
    <Link to={`/resources/${encodeURIComponent(item.name)}`} className="svc-card attention">
      <div className="top">
        <span className="name" title={item.name}>
          {item.name}
        </span>
        <HealthPill health={item.health} />
      </div>
      <div className="row tight">
        <TypeTag type={item.type} />
        <StatePill state={item.state} />
      </div>
      {item.reasons.length > 0 && <div className="meta">{translateAll(item.reasons)}</div>}
      {item.firingAlerts > 0 && <div className="meta">{s.overview.alertCount(item.firingAlerts)}</div>}
      {item.since && (
        <div className="meta">
          {s.resource.since} {formatTime(item.since)}
        </div>
      )}
      {resource && <Availability r={resource} />}
    </Link>
  );
}

/** A resource that needs nothing: one line, with its day of availability. */
function QuietCard({ r }: { r: OverviewResource }) {
  const tone = healthView(r.health).tone;
  return (
    <Link to={`/resources/${encodeURIComponent(r.name)}`} className="svc-card compact">
      <div className="top">
        <span className="name" title={r.name}>
          {r.name}
        </span>
        <HealthPill health={r.health} />
      </div>
      <div className="meta">
        {r.type === "service" && r.deployedVersion ? (
          <>
            <code>{shortVersion(r.deployedVersion)}</code> ·{" "}
            {r.lastDeployedAt ? `${s.overview.lastDeploy} ${formatTime(r.lastDeployedAt)}` : s.overview.noDeploy}
          </>
        ) : (
          s.resourceType[r.type]
        )}
      </div>
      {r.buckets.length > 0 && <Sparkline buckets={r.buckets} tone={tone === "info" ? "neutral" : tone} />}
      <div className="row tight">
        <ManagedBadges managedBy={r.managedBy} hideGui />
      </div>
    </Link>
  );
}

const HEALTHS: Health[] = ["healthy", "degraded", "down", "unknown"];

/** Counts per type and health, with links into the filtered resource list. */
function TypeSummary({ types }: { types: Overview["types"] }) {
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>{s.resources.columns.type}</th>
            <th className="num">{s.overview.total}</th>
            {HEALTHS.map((h) => (
              <th key={h} className="num">
                {s.health[h]}
              </th>
            ))}
            <th className="num">{s.overview.retired}</th>
          </tr>
        </thead>
        <tbody>
          {types.map(({ type, total, retired, health }) => (
            <tr key={type}>
              <td>
                <Link to={`/resources?type=${type}`}>{s.resourceType[type]}</Link>
              </td>
              <td className="num">{total}</td>
              {HEALTHS.map((h) => {
                const n = health[h] ?? 0;
                return (
                  <td key={h} className="num">
                    {n > 0 ? <Link to={`/resources?type=${type}&health=${h}&phase=managed`}>{n}</Link> : 0}
                  </td>
                );
              })}
              <td className="num">{retired}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function OverviewBody({ data }: { data: Overview }) {
  const attentionNames = new Set(data.attention.map((a) => a.name));
  const byName = new Map(data.resources.map((r) => [r.name, r]));
  const quiet = data.resources.filter((r) => !attentionNames.has(r.name));
  const count = (h: Health) => data.types.reduce((n, t) => n + (t.health[h] ?? 0), 0);

  return (
    <>
      <div className="stats" aria-label={s.overview.stats}>
        <Stat value={count("healthy")} label={s.overview.healthy} />
        <Stat value={count("degraded")} label={s.overview.degraded} tone={count("degraded") ? "warn" : undefined} />
        <Stat value={count("down")} label={s.overview.down} tone={count("down") ? "bad" : undefined} />
        {count("unknown") > 0 && <Stat value={count("unknown")} label={s.overview.unknownHealth} />}
        <Stat value={data.activeRuns} label={s.overview.runningRuns} to="/runs" />
        <Stat value={data.deploys.count} label={s.overview.deploysToday} />
        <Stat value={data.firingAlerts} label={s.overview.firingAlerts} tone={data.firingAlerts ? "warn" : undefined} to="/alerts" />
      </div>

      {data.resources.length === 0 && data.types.every((t) => t.total === 0) ? (
        <Empty>
          {s.overview.noResources} <Link to="/resources?create=1">{s.overview.createFirst}</Link>
        </Empty>
      ) : (
        <>
          <h3>{s.overview.needsAttention}</h3>
          {data.attention.length === 0 ? (
            <p className="banner ok">{s.overview.allClear}</p>
          ) : (
            <div className="cards" aria-label={s.overview.needsAttention}>
              {data.attention.map((a) => (
                <AttentionCard key={a.name} item={a} resource={byName.get(a.name)} />
              ))}
            </div>
          )}
          <h3>{s.overview.byType}</h3>
          <TypeSummary types={data.types} />
          {quiet.length > 0 && (
            <>
              <h3>{s.overview.otherResources}</h3>
              <div className="cards compact">
                {quiet.map((r) => (
                  <QuietCard key={r.name} r={r} />
                ))}
              </div>
            </>
          )}
        </>
      )}
    </>
  );
}

export function OverviewPage() {
  // One request fills the page whatever the number of resources; the event
  // stream refreshes it.
  const overview = useQuery(overviewQuery(startOfDay()));
  const events = useInfiniteQuery(timelineQuery({}));

  return (
    <div className="page">
      <PageHead title={s.overview.title} />
      <Async q={overview}>{(data) => <OverviewBody data={data} />}</Async>

      <h3>{s.overview.recentEvents}</h3>
      {events.isPending ? (
        <Spinner />
      ) : events.isError ? (
        <ErrorBox error={events.error} onRetry={() => void events.refetch()} />
      ) : (
        <>
          <TimelineEvents entries={(events.data.pages[0]?.items ?? []).slice(0, 10)} showResource />
          <p>
            <Link to="/timeline">{s.overview.viewAll}</Link>
          </p>
        </>
      )}
    </div>
  );
}
