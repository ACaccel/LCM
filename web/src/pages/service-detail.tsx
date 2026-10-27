import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { NavLink, useParams } from "react-router";
import { serviceQuery } from "../api/queries";
import type { ActionName, ServiceDetail } from "../api/types";
import { HealthPill, ManagedBadges, StatePill } from "../components/badges";
import { TimelinePanel } from "../components/timeline-panel";
import { ErrorBox, PageHead, Spinner, Tag } from "../components/ui";
import { isApiError } from "../api/client";
import { formatTime, shortVersion } from "../lib/format";
import { dashboardUrl } from "../lib/yamldoc";
import { s } from "../strings";
import { ActionDialog } from "./service/action-dialog";
import { ConfigTab } from "./service/config-tab";
import { ChecksTab, DeploymentsTab, PoliciesTab } from "./service/tabs-view";

const TABS = ["timeline", "checks", "deployments", "policies", "config"] as const;
type Tab = (typeof TABS)[number];

interface Pending {
  action: ActionName;
  version?: string;
}

function ActionBar({ detail, onAction }: { detail: ServiceDetail; onAction: (a: ActionName) => void }) {
  const spec = detail.document.spec;
  const has = (a: ActionName) => {
    if (a === "deploy" || a === "rollback") return !!spec.source;
    if (a === "backup") return !!spec.backup;
    if (a === "run-job") return Object.keys(spec.jobs ?? {}).length > 0;
    return a !== "retire";
  };
  const order: ActionName[] = ["start", "stop", "restart", "backup", "run-job", "rollback", "deploy"];
  return (
    <div className="row" role="group" aria-label={s.service.actionsGroup}>
      {order.filter(has).map((a) => (
        <button key={a} type="button" className={`btn${a === "deploy" ? " primary" : ""}`} onClick={() => onAction(a)}>
          {s.actionName[a]}
        </button>
      ))}
    </div>
  );
}

export function ServiceDetailPage() {
  const { name = "", tab } = useParams();
  const q = useQuery(serviceQuery(name));
  const [pending, setPending] = useState<Pending | null>(null);
  const active: Tab = (TABS as readonly string[]).includes(tab ?? "") ? (tab as Tab) : "timeline";

  if (q.isPending) {
    return (
      <div className="page">
        <Spinner />
      </div>
    );
  }
  if (q.isError) {
    const notFound = isApiError(q.error) && q.error.status === 404;
    return (
      <div className="page">
        <ErrorBox error={notFound ? new Error(s.service.notFound) : q.error} onRetry={() => void q.refetch()} />
      </div>
    );
  }
  const d = q.data;
  const dash = dashboardUrl(d.document);
  const firing = d.alerts.filter((a) => !a.resolvedAt).length;
  const jobs = Object.keys(d.document.spec.jobs ?? {});

  return (
    <div className="page">
      <PageHead
        title={d.name}
        meta={
          <div className="pills">
            <StatePill state={d.state} />
            <HealthPill health={d.health} />
            <Tag title={s.service.host}>{d.host}</Tag>
            {d.deployedVersion && <Tag title={s.common.version}>v {shortVersion(d.deployedVersion)}</Tag>}
            <Tag title={s.service.configVersion}>{s.config.version(d.version)}</Tag>
            <ManagedBadges managedBy={d.managedBy} />
            {firing > 0 && <Tag>{`${s.service.alerts} ${firing}`}</Tag>}
            {dash && (
              <a className="pill neutral" href={dash} target="_blank" rel="noopener noreferrer">
                {s.service.dashboard} ↗
              </a>
            )}
          </div>
        }
      >
        <ActionBar detail={d} onAction={(a) => setPending({ action: a })} />
      </PageHead>
      <div className="meta">
        {s.service.since} {formatTime(d.stateSince)}
      </div>

      <nav className="tabs" aria-label={d.name}>
        {TABS.map((t) => (
          <NavLink key={t} to={`/services/${encodeURIComponent(name)}/${t}`} aria-current={active === t ? "page" : undefined} className={active === t ? "active" : undefined}>
            {s.service.tabs[t]}
          </NavLink>
        ))}
      </nav>

      {active === "timeline" && <TimelinePanel service={name} />}
      {active === "checks" && <ChecksTab service={name} />}
      {active === "deployments" && (
        <DeploymentsTab service={name} deployedVersion={d.deployedVersion} hasSource={!!d.document.spec.source} onDeploy={(version) => setPending({ action: "deploy", version })} />
      )}
      {active === "policies" && <PoliciesTab service={name} detail={d} />}
      {active === "config" && <ConfigTab detail={d} onAction={(a) => setPending({ action: a })} />}

      {pending && <ActionDialog service={name} action={pending.action} version={pending.version} jobs={jobs} onClose={() => setPending(null)} />}
    </div>
  );
}
