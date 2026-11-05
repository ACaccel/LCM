import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import { client, unwrap } from "../api/client";
import { qk } from "../api/queries";
import type { ActionName, ResourceGroupAction, ResourceGroupPlan } from "../api/types";
import { Dialog } from "../components/dialog";
import { ErrorBox, Pill, Spinner } from "../components/ui";
import { actionStatusView } from "../lib/status";
import { s } from "../strings";
import { ImpactList, impactCount } from "./resource/impact";

type PlanStep = ResourceGroupPlan["steps"][number];

/** Bulk actions offered in the list: lifecycle actions that need no extra input. */
export const BULK_ACTIONS: readonly ActionName[] = ["start", "stop", "restart", "deploy", "rollback", "backup"];

/** The distinct resources the impacted steps of a plan reach. */
function impactedNames(steps: readonly PlanStep[]): number {
  const impacted = steps.filter((st) => st.impact.acknowledgementRequired);
  return impactCount({
    dependents: impacted.flatMap((st) => st.impact.dependents),
    controlDependents: impacted.flatMap((st) => st.impact.controlDependents),
  });
}

function GroupProgress({ id }: { id: string }) {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: ["resource-group-action", id],
    queryFn: () =>
      unwrap(
        client.GET("/resource-groups/actions/{id}", {
          params: { path: { id } },
        }),
      ),
    refetchInterval: (query) => (query.state.data?.status === "running" ? 1000 : false),
  });
  const g: ResourceGroupAction | undefined = q.data;
  const finished = !!g && g.status !== "running";
  useEffect(() => {
    if (!finished) return;
    void qc.invalidateQueries({ queryKey: qk.resourcesAll() });
    void qc.invalidateQueries({ queryKey: qk.overviewAll() });
  }, [finished, qc]);

  if (q.isError) return <ErrorBox error={q.error} onRetry={() => void q.refetch()} />;
  if (!g) return <Spinner label={s.action.running} />;
  return (
    <div className="stack" aria-live="polite">
      <div className="row">
        <Pill view={actionStatusView(g.status)} />
      </div>
      <ol>
        {g.steps.map((st) => (
          <li key={st.resource}>
            <strong>{st.resource}</strong>{" "}
            <Pill
              view={
                st.status === "notRun"
                  ? { tone: "neutral", icon: "–", label: s.bulk.notRun }
                  : st.status === "pending"
                    ? { tone: "neutral", icon: "…", label: s.bulk.pending }
                    : actionStatusView(st.status)
              }
            />
            {st.error && <div className="meta">{st.error}</div>}
          </li>
        ))}
      </ol>
    </div>
  );
}

/**
 * Runs one lifecycle action on the checked services through the group
 * action API. The dry run comes first and shows the ordered plan and its
 * impact; confirming runs exactly that plan, acknowledging the impact it
 * listed.
 */
export function BulkActionDialog({ names, action, onClose }: { names: string[]; action: ActionName; onClose: () => void }) {
  const selector = useMemo(() => ({ names }), [names]);
  const [groupId, setGroupId] = useState<string | null>(null);
  const plan = useQuery({
    queryKey: ["resource-group-plan", action, names],
    queryFn: () =>
      unwrap(
        client.POST("/resource-groups/actions", {
          body: { selector, action, dryRun: true, acknowledgeImpact: false },
        }),
      ) as Promise<ResourceGroupPlan>,
    staleTime: 0,
    gcTime: 0,
    retry: false,
  });
  const run = useMutation({
    mutationFn: () =>
      unwrap(
        client.POST("/resource-groups/actions", {
          body: {
            selector,
            action,
            dryRun: false,
            acknowledgeImpact: plan.data?.acknowledgementRequired === true,
          },
        }),
      ) as Promise<ResourceGroupAction>,
    onSuccess: (g) => setGroupId(g.id),
  });

  const verb = s.actionName[action] ?? action;
  const steps = plan.data?.steps ?? [];
  const blocked = steps.some((st) => st.error);
  const required = plan.data?.acknowledgementRequired === true;
  const affected = required ? impactedNames(steps) : 0;
  const label = required && affected > 0 ? s.impact.confirm(verb, affected) : s.bulk.confirm(verb, names.length);

  return (
    <Dialog
      title={s.bulk.title(verb, names.length)}
      wide
      onClose={onClose}
      pending={run.isPending}
      footer={
        groupId ? (
          <button type="button" className="btn" onClick={onClose}>
            {s.common.close}
          </button>
        ) : (
          <>
            <button type="button" className="btn" onClick={onClose}>
              {s.common.cancel}
            </button>
            <button
              type="button"
              className={`btn ${action === "stop" || action === "rollback" ? "danger solid" : "primary"}`}
              disabled={!plan.data || blocked || run.isPending}
              onClick={() => run.mutate()}
            >
              {label}
            </button>
          </>
        )
      }
    >
      {groupId ? (
        <GroupProgress id={groupId} />
      ) : (
        <div className="stack">
          {plan.isPending && <Spinner label={s.action.previewing} />}
          <ErrorBox error={plan.error ?? run.error} onRetry={plan.isError ? () => void plan.refetch() : undefined} />
          {plan.data && (
            <>
              <h3 className="flat-top">{s.bulk.planTitle}</h3>
              <p className="meta">{s.bulk.planNote}</p>
              <ol aria-label={s.bulk.planTitle}>
                {steps.map((st) => (
                  <li key={st.resource}>
                    <strong>{st.resource}</strong>{" "}
                    {st.error ? (
                      <span className="meta">{s.bulk.cannotRun(st.error)}</span>
                    ) : st.plan ? (
                      <span className="meta">
                        {s.state[st.plan.from] ?? st.plan.from} → {s.state[st.plan.to] ?? st.plan.to}
                      </span>
                    ) : null}
                  </li>
                ))}
              </ol>
              {required && (
                <div className="banner warn stack" role="group" aria-label={s.impact.title}>
                  <strong>{s.impact.title}</strong>
                  <p>{s.impact.intro}</p>
                  {steps
                    .filter((st) => st.impact.acknowledgementRequired)
                    .map((st) => (
                      <div key={st.resource}>
                        <div className="meta">{s.bulk.impactOf(st.resource)}</div>
                        <ImpactList impact={st.impact} />
                      </div>
                    ))}
                  <p className="meta">{s.impact.confirmNote}</p>
                </div>
              )}
            </>
          )}
        </div>
      )}
    </Dialog>
  );
}
