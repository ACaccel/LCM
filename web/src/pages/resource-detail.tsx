import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useParams } from "react-router";
import { client, isApiError, unwrap } from "../api/client";
import { qk, resourceQuery } from "../api/queries";
import type { ActionName, ResourceDetail, ResourceTypeName } from "../api/types";
import { HealthPill, ManagedBadges, PhasePill, StatePill, TypeTag } from "../components/badges";
import { TimelinePanel } from "../components/timeline-panel";
import { useToast } from "../components/toast";
import { ErrorBox, PageHead, Spinner, Tag } from "../components/ui";
import { actionLabel, reasonText } from "../lib/actions";
import { shortVersion } from "../lib/format";
import { translateAll } from "../lib/messages";
import { dashboardUrl } from "../lib/yamldoc";
import { s } from "../strings";
import { ActionDialog } from "./resource/action-dialog";
import { ChecksTab } from "./resource/checks";
import { ConfigTab, resourceRef } from "./resource/config-tab";
import { DeploymentsTab } from "./resource/deployments";
import { MachineOperationsTab } from "./resource/machine/controls";
import { MachineMonitoring } from "./resource/machine/monitoring";
import { ResourceEvents } from "./resource/observations";
import { OverviewTab } from "./resource/overview";
import { PhaseControls } from "./resource/phase";
import { PoliciesTab } from "./resource/policies";
import { RelationsTab } from "./resource/relations";
import { RouterMonitoring, RouterOperationsTab } from "./resource/router";

export type Tab = keyof typeof s.resource.tabs;

const TYPE_TABS: Record<ResourceTypeName, Tab[]> = {
  service: ["deployments"],
  machine: ["monitoring", "events", "maintenance"],
  router: ["monitoring", "events", "operations"],
};

/** The tabs of a resource: the common ones around the panels of its type. */
export function tabsFor(type: ResourceTypeName): Tab[] {
  return ["overview", ...TYPE_TABS[type], "checks", "timeline", "policies", "relations", "config"];
}

const SERVICE_ACTIONS: readonly ActionName[] = ["start", "stop", "restart", "backup", "run-job", "rollback", "deploy"];

interface Pending {
  action: ActionName;
  version?: string;
}

/**
 * Actions offered by the server for this resource. Service actions open the
 * preview dialog; observation runs at once; durable operations live on the
 * operations tab, where their confirmation and recovery flow is; phase
 * changes have their own confirmation.
 */
function ActionBar({ detail, base, onAction }: { detail: ResourceDetail; base: string; onAction: (a: ActionName) => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const observe = useMutation({
    mutationFn: () => unwrap(client.POST("/resources/{resource}/observe", { params: { path: { resource: detail.name } } })),
    onSuccess: (d) => {
      qc.setQueryData(qk.resource(detail.name), d);
      void qc.invalidateQueries({ queryKey: qk.resourceAll(detail.name) });
      toast.success(s.resource.observed);
    },
    onError: (e) => toast.error(e.message),
  });
  // Actions the document does not configure (no source, backup or jobs) are
  // hidden; the others stay visible and explain why they are disabled.
  const service = SERVICE_ACTIONS.flatMap((name) => {
    const a = detail.actions.find((x) => x.kind === "action" && x.name === name);
    return a && a.reason !== "notConfigured" ? [a] : [];
  });
  const observeAction = detail.actions.find((x) => x.kind === "action" && x.name === "observe");
  const operations = detail.actions.filter((x) => x.kind === "operation" && x.available);
  const opsTab = detail.type === "machine" ? "maintenance" : "operations";
  return (
    <div className="row" role="group" aria-label={s.resource.actionsGroup}>
      {service.map((a) => {
        const reason = a.available ? "" : reasonText(a, detail.state);
        return (
          <button
            key={a.name}
            type="button"
            className={`btn${a.name === "deploy" ? " primary" : ""}`}
            disabled={!a.available}
            title={reason || undefined}
            onClick={() => onAction(a.name as ActionName)}
          >
            {actionLabel(a.name)}
          </button>
        );
      })}
      {observeAction && (
        <button
          type="button"
          className="btn"
          disabled={!observeAction.available || observe.isPending}
          title={observeAction.available ? undefined : reasonText(observeAction, detail.state)}
          onClick={() => observe.mutate()}
        >
          {actionLabel("observe")}
        </button>
      )}
      {operations.map((a) => (
        <Link key={a.name} className="btn" to={`${base}/${opsTab}`} title={s.resource.goOperations}>
          {actionLabel(a.name)}
        </Link>
      ))}
      <PhaseControls detail={detail} />
    </div>
  );
}

function Detail({ detail, tab }: { detail: ResourceDetail; tab: string | undefined }) {
  const [pending, setPending] = useState<Pending | null>(null);
  const selector = resourceRef(detail);
  const archived = detail.phase === "unmanaged";
  const base = `/resources/${encodeURIComponent(selector)}`;
  const tabs = tabsFor(detail.type);
  const active: Tab = tabs.includes(tab as Tab) ? (tab as Tab) : "overview";
  const dash = dashboardUrl(detail.document);
  const firing = detail.alerts.filter((a) => !a.resolvedAt).length;
  const config = detail.document.spec.config ?? {};
  const jobs = typeof config["jobs"] === "object" && config["jobs"] !== null ? Object.keys(config["jobs"]) : [];

  return (
    <div className="page">
      <PageHead
        title={detail.name}
        meta={
          <div className="pills">
            <TypeTag type={detail.type} adapter={detail.adapter} />
            <PhasePill phase={detail.phase} />
            <StatePill state={detail.state} />
            <HealthPill health={detail.health.status} />
            {detail.service?.deployedVersion && <Tag title={s.resource.deployedVersion}>v {shortVersion(detail.service.deployedVersion)}</Tag>}
            <Tag title={s.resource.configVersion}>{s.config.version(detail.version)}</Tag>
            <ManagedBadges managedBy={detail.managedBy} />
            {firing > 0 && <Tag>{`${s.resource.alerts} ${firing}`}</Tag>}
            {dash && (
              <a className="pill neutral" href={dash} target="_blank" rel="noopener noreferrer">
                {s.resource.dashboard} ↗
              </a>
            )}
          </div>
        }
      >
        {!archived && <ActionBar detail={detail} base={base} onAction={(a) => setPending({ action: a })} />}
      </PageHead>
      {detail.health.reasons.length > 0 && (
        <p className="meta">
          {s.resource.healthReasons}: {translateAll(detail.health.reasonMessages)}
        </p>
      )}
      {archived && <div className="banner">{s.resource.archivedBanner}</div>}
      {detail.phase === "retired" && <div className="banner warn">{s.resource.retiredBanner}</div>}

      <nav className="tabs" aria-label={detail.name}>
        {tabs.map((t) => (
          // A plain link: the shown tab is not always the URL's (unknown tabs fall back to the overview).
          <Link key={t} to={`${base}/${t}`} aria-current={active === t ? "page" : undefined} className={active === t ? "active" : undefined}>
            {s.resource.tabs[t]}
          </Link>
        ))}
      </nav>

      {active === "overview" && <OverviewTab detail={detail} selector={selector} />}
      {active === "deployments" && (
        <DeploymentsTab
          resource={selector}
          deployedVersion={detail.service?.deployedVersion ?? ""}
          hasSource={!archived && !!config["source"]}
          onDeploy={(version) => setPending({ action: "deploy", version })}
        />
      )}
      {active === "monitoring" && detail.type === "machine" && <MachineMonitoring resource={detail} selector={selector} />}
      {active === "monitoring" && detail.type === "router" && <RouterMonitoring resource={detail} selector={selector} />}
      {active === "events" && <ResourceEvents resource={selector} note={detail.type === "machine" ? s.machine.journalNote : undefined} />}
      {active === "maintenance" && <MachineOperationsTab resource={detail} selector={selector} />}
      {active === "operations" && <RouterOperationsTab resource={detail} selector={selector} />}
      {active === "checks" && <ChecksTab resource={selector} />}
      {active === "timeline" && <TimelinePanel resource={detail.name} />}
      {active === "policies" && <PoliciesTab detail={detail} />}
      {active === "relations" && <RelationsTab detail={detail} />}
      {active === "config" && <ConfigTab detail={detail} />}

      {pending && <ActionDialog resource={detail.name} action={pending.action} version={pending.version} jobs={jobs} onClose={() => setPending(null)} />}
    </div>
  );
}

export function ResourceDetailPage() {
  const { name = "", tab } = useParams();
  const q = useQuery({
    ...resourceQuery(name),
    // Observation freshness changes with time and snapshots do not travel as
    // events, so observed resources are polled while they are managed.
    refetchInterval: (query) => (query.state.data && query.state.data.type !== "service" && query.state.data.phase === "managed" ? 15_000 : false),
  });
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
        <ErrorBox error={notFound ? new Error(s.resource.notFound) : q.error} onRetry={() => void q.refetch()} />
        <p>
          <Link to="/resources">{s.common.back}</Link>
        </p>
      </div>
    );
  }
  // A new incarnation under the same name gets a fresh page state.
  return <Detail key={q.data.id} detail={q.data} tab={tab} />;
}
