import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { client, unwrap } from "../../api/client";
import { qk } from "../../api/queries";
import type { ResourceAction, ResourceDetail, ResourcePhase } from "../../api/types";
import { ConfirmDialog } from "../../components/dialog";
import { useToast } from "../../components/toast";
import { actionLabel, reasonText } from "../../lib/actions";
import { s } from "../../strings";
import { ImpactAcknowledgement, useImpactAck } from "./impact";

export type PhaseAction = "retire" | "resume" | "unmanage";

const TARGET: Record<PhaseAction, ResourcePhase> = { retire: "retired", resume: "managed", unmanage: "unmanaged" };

function isPhaseAction(name: string): name is PhaseAction {
  return name in TARGET;
}

/**
 * The phase changes that apply to the current phase, from the server's
 * action list: resuming a managed resource or retiring a retired one is
 * never shown.
 */
export function phaseActions(detail: Pick<ResourceDetail, "actions" | "phase">): (ResourceAction & { name: PhaseAction })[] {
  return detail.actions.filter(
    (a): a is ResourceAction & { name: PhaseAction } =>
      a.kind === "phase" && isPhaseAction(a.name) && !(a.name === "resume" && detail.phase === "managed") && !(a.name === "retire" && detail.phase === "retired"),
  );
}

/**
 * Confirmation of a phase change. The version is pinned when the dialog
 * opens, so a change made underneath the confirmation answers a conflict
 * instead of acting on a document the operator has not seen.
 */
export function PhaseDialog({ detail, action, onClose }: { detail: ResourceDetail; action: PhaseAction; onClose: () => void }) {
  const [version] = useState(detail.version);
  const [reason, setReason] = useState("");
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const impact = useImpactAck(detail.name, action);
  const mutation = useMutation({
    mutationFn: () =>
      unwrap(
        client.POST("/resources/{resource}/phase", {
          params: { path: { resource: detail.name } },
          body: {
            phase: TARGET[action],
            version,
            ...(reason.trim() ? { reason: reason.trim() } : {}),
            ...(impact.acknowledged ? { acknowledgeImpact: true } : {}),
          },
        }),
      ),
    onSuccess: () => {
      toast.success(s.phaseChange.done);
      void qc.invalidateQueries({ queryKey: qk.resourceAll(detail.name) });
      void qc.invalidateQueries({ queryKey: qk.resourcesAll() });
      void qc.invalidateQueries({ queryKey: qk.archivesAll() });
      onClose();
      if (action === "unmanage") void navigate("/resources", { replace: true });
    },
  });
  const verb = actionLabel(action);
  return (
    <ConfirmDialog
      title={s.phaseChange.title(verb, detail.name)}
      confirmLabel={s.common.confirm}
      danger={action !== "resume"}
      busy={mutation.isPending}
      disabled={!impact.ready}
      error={mutation.error}
      onClose={() => !mutation.isPending && onClose()}
      onConfirm={() => mutation.mutate()}
      body={
        <div className="stack">
          <p>
            {s.operation.target}: <strong>{detail.name}</strong> ({s.resourceType[detail.type]}) / {s.config.version(version)}
          </p>
          <p>{s.phaseChange[action]}</p>
          {action === "retire" && detail.type === "service" && <p>{s.phaseChange.retireService}</p>}
          <p className="meta">{s.phaseChange.common}</p>
          <ImpactAcknowledgement state={impact} />
          <div className="field">
            <label htmlFor="phase-reason">{s.phaseChange.reason}</label>
            <textarea id="phase-reason" maxLength={1000} value={reason} onChange={(e) => setReason(e.target.value)} />
            <span className="hint">{s.phaseChange.reasonHint}</span>
          </div>
        </div>
      }
    />
  );
}

/** Buttons for the applicable phase changes; unavailable ones stay visible and explain why. */
export function PhaseControls({ detail }: { detail: ResourceDetail }) {
  const [action, setAction] = useState<PhaseAction | null>(null);
  const actions = phaseActions(detail);
  if (!actions.length) return null;
  return (
    <>
      {actions.map((a) => {
        const reason = a.available ? "" : reasonText(a, detail.state) || s.action.unavailable.notOffered;
        return (
          <button
            key={a.name}
            type="button"
            className={`btn${a.name === "resume" ? "" : " danger"}`}
            disabled={!a.available}
            title={reason || undefined}
            onClick={() => setAction(a.name)}
          >
            {actionLabel(a.name)}
          </button>
        );
      })}
      {action && <PhaseDialog key={action} detail={detail} action={action} onClose={() => setAction(null)} />}
    </>
  );
}
