import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { alertsQuery, deploymentsSinceQuery, resourceQuery, resourcesQuery, resourceTypesQuery, runsQuery, timelineQuery } from "../api/queries";
import type { Health, ResourceSummary } from "../api/types";
import { HealthPill, ManagedBadges, StatePill, TypeTag } from "../components/badges";
import { Sparkline } from "../components/charts";
import { TimelineEvents } from "../components/timeline-list";
import { Async, Empty, ErrorBox, PageHead, Spinner } from "../components/ui";
import { formatTime, formatUptime, shortVersion, startOfDay } from "../lib/format";
import { s } from "../strings";
import { useResourceSpark } from "../hooks";
import { healthView } from "../lib/status";

function Stat({ value, label, tone }: { value: number | string; label: string; tone?: "warn" | "bad" }) {
  const color = tone === "bad" ? "var(--destructive)" : tone === "warn" ? "var(--warning)" : undefined;
  return (
    <div className="stat">
      <b style={color ? { color } : undefined}>{value}</b>
      <span>{label}</span>
    </div>
  );
}

function ResourceCard({ r }: { r: ResourceSummary }) {
  const { buckets, uptime, hasChecks, pending } = useResourceSpark(r.name);
  const last = useQuery(resourceQuery(r.name)).data?.service?.lastDeployment;
  const tone = healthView(r.health).tone;
  return (
    <Link to={`/resources/${encodeURIComponent(r.name)}`} className="svc-card">
      <div className="top">
        <span className="name" title={r.name}>
          {r.name}
        </span>
        <HealthPill health={r.health} />
      </div>
      <div className="row" style={{ gap: 4 }}>
        <TypeTag type={r.type} />
        <StatePill state={r.state} />
      </div>
      {r.type === "service" && (
        <div className="meta">
          <code>{shortVersion(r.deployedVersion)}</code>
          <br />
          {last ? `${s.overview.lastDeploy} ${formatTime(last.finishedAt ?? last.startedAt)}` : s.overview.noDeploy}
        </div>
      )}
      {buckets.length > 0 ? (
        <>
          <Sparkline buckets={buckets} tone={tone === "info" ? "neutral" : tone} />
          <div className="meta">
            {s.overview.uptime24h} {formatUptime(uptime, 1)}
          </div>
        </>
      ) : (
        <div className="meta" style={{ height: 24 }}>
          {pending ? "" : hasChecks ? s.overview.noUptimeData : s.checks.none}
        </div>
      )}
      <div className="row" style={{ gap: 4 }}>
        <ManagedBadges managedBy={r.managedBy} />
      </div>
    </Link>
  );
}

const HEALTHS: Health[] = ["healthy", "degraded", "down", "unknown"];

/** Counts per type and health, with links into the filtered resource list. */
function TypeSummary({ items }: { items: ResourceSummary[] }) {
  const types = useQuery(resourceTypesQuery());
  const names = types.data?.map((t) => t.type) ?? [...new Set(items.map((r) => r.type))];
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
          {names.map((type) => {
            const of = items.filter((r) => r.type === type);
            const managed = of.filter((r) => r.phase === "managed");
            return (
              <tr key={type}>
                <td>
                  <Link to={`/resources?type=${type}`}>{s.resourceType[type]}</Link>
                </td>
                <td className="num">{of.length}</td>
                {HEALTHS.map((h) => {
                  const n = managed.filter((r) => r.health === h).length;
                  return (
                    <td key={h} className="num">
                      {n > 0 ? <Link to={`/resources?type=${type}&health=${h}&phase=managed`}>{n}</Link> : 0}
                    </td>
                  );
                })}
                <td className="num">{of.length - managed.length}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

export function OverviewPage() {
  const resources = useQuery(resourcesQuery());
  const activeRuns = useInfiniteQuery(runsQuery({ status: ["queued", "running", "suspended"] }));
  const alerts = useQuery(alertsQuery());
  const deploys = useQuery(deploymentsSinceQuery(startOfDay()));
  const events = useInfiniteQuery(timelineQuery({}));

  const list = (resources.data ?? []).filter((x) => x.phase === "managed");
  const count = (h: Health) => list.filter((x) => x.health === h).length;
  const active = activeRuns.data?.pages.flatMap((p) => p.items).length ?? 0;
  const firing = alerts.data?.filter((a) => !a.resolvedAt).length ?? 0;

  return (
    <div className="page">
      <PageHead title={s.overview.title} />
      <ErrorBox error={activeRuns.error ?? alerts.error ?? deploys.error} />
      <div className="stats" aria-label={s.overview.stats}>
        <Stat value={count("healthy")} label={s.overview.healthy} />
        <Stat value={count("degraded")} label={s.overview.degraded} tone={count("degraded") ? "warn" : undefined} />
        <Stat value={count("down")} label={s.overview.down} tone={count("down") ? "bad" : undefined} />
        {count("unknown") > 0 && <Stat value={count("unknown")} label={s.overview.unknownHealth} />}
        <Stat value={activeRuns.hasNextPage ? `${active}+` : active} label={s.overview.runningRuns} />
        <Stat value={deploys.data?.length ?? "-"} label={s.overview.deploysToday} />
        <Stat value={firing} label={s.overview.firingAlerts} tone={firing ? "warn" : undefined} />
      </div>

      <Async q={resources}>
        {(items) =>
          items.length === 0 ? (
            <Empty>
              {s.overview.noResources} <Link to="/resources?create=1">{s.overview.createFirst}</Link>
            </Empty>
          ) : (
            <>
              <h3>{s.overview.byType}</h3>
              <TypeSummary items={items} />
              <h3>{s.overview.resources}</h3>
              <div className="cards">
                {items
                  .filter((r) => r.phase === "managed")
                  .map((r) => (
                    <ResourceCard key={r.id} r={r} />
                  ))}
              </div>
            </>
          )
        }
      </Async>

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
