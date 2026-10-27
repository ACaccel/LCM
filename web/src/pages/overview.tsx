import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { alertsQuery, deploymentsSinceQuery, runsQuery, serviceQuery, servicesQuery, timelineQuery } from "../api/queries";
import type { ServiceSummary } from "../api/types";
import { HealthPill, ManagedBadges } from "../components/badges";
import { Sparkline } from "../components/charts";
import { TimelineEvents } from "../components/timeline-list";
import { Async, Empty, ErrorBox, PageHead, Spinner } from "../components/ui";
import { formatTime, formatUptime, shortVersion, startOfDay } from "../lib/format";
import { s } from "../strings";
import { useServiceSpark } from "../hooks";
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

function ServiceCard({ svc }: { svc: ServiceSummary }) {
  const { buckets, uptime, hasChecks, pending } = useServiceSpark(svc.name);
  const last = useQuery(serviceQuery(svc.name)).data?.lastDeployment;
  const tone = healthView(svc.health).tone;
  return (
    <Link to={`/services/${encodeURIComponent(svc.name)}`} className="svc-card">
      <div className="top">
        <span className="name" title={svc.name}>
          {svc.name}
        </span>
        <HealthPill health={svc.health} />
      </div>
      <div className="meta">
        {s.state[svc.state] ?? svc.state} · <code>{shortVersion(svc.deployedVersion)}</code>
        <br />
        {last ? `${s.overview.lastDeploy} ${formatTime(last.finishedAt ?? last.startedAt)}` : s.overview.noDeploy}
      </div>
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
        <ManagedBadges managedBy={svc.managedBy} />
      </div>
    </Link>
  );
}

export function OverviewPage() {
  const services = useQuery(servicesQuery());
  const activeRuns = useInfiniteQuery(runsQuery({ status: ["queued", "running", "suspended"] }));
  const alerts = useQuery(alertsQuery());
  const deploys = useQuery(deploymentsSinceQuery(startOfDay()));
  const events = useInfiniteQuery(timelineQuery({}));

  const list = services.data ?? [];
  const count = (h: string) => list.filter((x) => x.health === h).length;
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

      <h3>{s.overview.services}</h3>
      <Async q={services}>
        {(items) =>
          items.length === 0 ? (
            <Empty>
              {s.overview.noServices} <Link to="/services?create=1">{s.overview.createFirst}</Link>
            </Empty>
          ) : (
            <div className="cards">
              {items.map((svc) => (
                <ServiceCard key={svc.name} svc={svc} />
              ))}
            </div>
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
          <TimelineEvents entries={(events.data.pages[0]?.items ?? []).slice(0, 10)} showService />
          <p>
            <Link to="/timeline">{s.overview.viewAll}</Link>
          </p>
        </>
      )}
    </div>
  );
}
