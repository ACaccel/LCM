import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { client, unwrap } from "../../api/client";
import { actionQuery, qk } from "../../api/queries";
import type { Action, ActionName, ActionPlan } from "../../api/types";
import { Dialog } from "../../components/dialog";
import { ErrorBox, Field, Pill, Spinner } from "../../components/ui";
import { formatDuration, shortVersion } from "../../lib/format";
import { useLiveState } from "../../live";
import { s } from "../../strings";

interface Props {
  service: string;
  action: ActionName;
  /** Preset commit, for "deploy this version" from the history. */
  version?: string;
  /** Job names of the service, offered by run-job. */
  jobs?: string[];
  onClose: () => void;
}

const DANGEROUS: ReadonlySet<ActionName> = new Set(["stop", "retire", "rollback"]);

function ActionProgress({ id, onClose }: { id: string; onClose: () => void }) {
  const live = useLiveState();
  const q = useQuery({
    ...actionQuery(id),
    // The event stream patches this query; polling is only the fallback for
    // when the stream is down.
    refetchInterval: (query) => (query.state.data?.status === "running" && live !== "open" ? 2000 : false),
  });
  const a: Action | undefined = q.data;

  if (q.isError) return <ErrorBox error={q.error} onRetry={() => void q.refetch()} />;
  if (!a) return <Spinner label={s.action.running} />;
  const running = a.status === "running";
  const view = running
    ? { tone: "info" as const, icon: "↻", label: s.action.running }
    : a.status === "succeeded"
      ? { tone: "ok" as const, icon: "✓", label: s.action.succeeded }
      : { tone: "bad" as const, icon: "✕", label: s.action.failed };
  return (
    <div className="stack" aria-live="polite">
      <div className="row">
        <Pill view={view} />
        <span className="meta">
          {s.action.startedBy}: {a.actor.name}
        </span>
        {live !== "open" && running && <span className="meta">{s.action.trackerFallback}</span>}
      </div>
      {a.error && <ErrorBox error={new Error(a.error)} />}
      {a.result && (
        <dl className="kv">
          <dt>{s.action.planFrom}</dt>
          <dd>
            {s.state[a.result.from]} → {s.state[a.result.to]}
          </dd>
          {a.result.version && (
            <>
              <dt>{s.common.version}</dt>
              <dd>
                <code>{shortVersion(a.result.version)}</code>
                {a.result.previousVersion && (
                  <span className="meta">
                    {" "}
                    ({s.deployments.previous} <code>{shortVersion(a.result.previousVersion)}</code>)
                  </span>
                )}
              </dd>
            </>
          )}
          <dt>{s.common.duration}</dt>
          <dd>{formatDuration(a.result.durationMs)}</dd>
        </dl>
      )}
      {a.result?.output && (
        <div>
          <div className="meta">{s.action.output}</div>
          <pre className="block">{a.result.output}</pre>
        </div>
      )}
      <div className="row end">
        <Link to={`/services/${encodeURIComponent(a.service)}/timeline`} onClick={onClose}>
          {s.service.tabs.timeline}
        </Link>
      </div>
    </div>
  );
}

/**
 * Preview (dry-run), confirmation and live progress of one lifecycle
 * action. The buttons of the service page are not shortcuts around policy:
 * the server checks the same preconditions for the preview and the run.
 */
export function ActionDialog({ service, action, version: presetVersion, jobs, onClose }: Props) {
  const qc = useQueryClient();
  const [version, setVersion] = useState(presetVersion ?? "");
  const [job, setJob] = useState(jobs?.[0] ?? "");
  const [plan, setPlan] = useState<{ plan: ActionPlan; key: string } | null>(null);
  const [actionId, setActionId] = useState<string | null>(null);

  const body = {
    ...(action === "deploy" && version.trim() ? { version: version.trim() } : {}),
    ...(action === "run-job" ? { job } : {}),
  };
  const key = JSON.stringify(body);

  const preview = useMutation({
    mutationFn: async () =>
      (await unwrap(
        client.POST("/services/{svc}/actions/{action}", { params: { path: { svc: service, action }, query: { dryRun: true } }, body }),
      )) as unknown as ActionPlan,
    onSuccess: (p) => setPlan({ plan: p, key }),
  });
  const run = useMutation({
    mutationFn: async () =>
      (await unwrap(
        client.POST("/services/{svc}/actions/{action}", { params: { path: { svc: service, action } }, body }),
      )) as unknown as { actionId: string },
    onSuccess: (r) => {
      setActionId(r.actionId);
      void qc.invalidateQueries({ queryKey: qk.service(service) });
    },
  });

  const { mutate: previewMutate } = preview;
  useEffect(() => {
    // Show the steps as soon as the dialog opens; there is nothing to type for most actions.
    previewMutate();
  }, [previewMutate]);

  const verb = s.actionName[action] ?? action;
  const current = plan?.key === key;

  return (
    <Dialog
      title={s.action.confirmTitle(verb, service)}
      onClose={onClose}
      footer={
        actionId ? (
          <button type="button" className="btn" onClick={onClose}>
            {s.common.close}
          </button>
        ) : (
          <>
            <button type="button" className="btn" onClick={onClose}>
              {s.common.cancel}
            </button>
            <button type="button" className="btn" disabled={preview.isPending} onClick={() => preview.mutate()}>
              {preview.isPending ? s.action.previewing : s.action.preview}
            </button>
            <button
              type="button"
              className={`btn ${DANGEROUS.has(action) ? "danger solid" : "primary"}`}
              disabled={!current || run.isPending || (action === "run-job" && !job)}
              title={current ? undefined : s.action.needPreview}
              onClick={() => run.mutate()}
            >
              {s.action.confirmRun}
            </button>
          </>
        )
      }
    >
      {actionId ? (
        <ActionProgress id={actionId} onClose={onClose} />
      ) : (
        <div className="stack">
          {action === "deploy" && (
            <Field label={s.action.versionLabel} htmlFor="act-version">
              <input id="act-version" type="text" value={version} onChange={(e) => setVersion(e.target.value)} placeholder="a41c9e2" spellCheck={false} />
            </Field>
          )}
          {action === "run-job" && (
            <Field label={s.action.jobLabel} htmlFor="act-job">
              <select id="act-job" value={job} onChange={(e) => setJob(e.target.value)}>
                {(jobs ?? []).map((j) => (
                  <option key={j} value={j}>
                    {j}
                  </option>
                ))}
              </select>
            </Field>
          )}
          {preview.isPending && <Spinner label={s.action.previewing} />}
          {plan && (
            <div className={current ? undefined : "meta"}>
              <h3 style={{ marginTop: 0 }}>{s.action.planTitle}</h3>
              <p className="meta">
                {s.action.planFrom}: {s.state[plan.plan.from]} → {s.state[plan.plan.to]}
              </p>
              <ol>
                {plan.plan.steps.map((st, i) => (
                  <li key={i}>
                    <code>{st}</code>
                  </li>
                ))}
              </ol>
            </div>
          )}
          {!current && plan && <p className="meta">{s.action.needPreview}</p>}
          <ErrorBox error={preview.error ?? run.error} />
        </div>
      )}
    </Dialog>
  );
}
