import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { ReactNode } from "react";
import { client, unwrap } from "../../../api/client";
import { qk } from "../../../api/queries";
import type { MachineMaintenancePreview, ResourceDetail } from "../../../api/types";
import { Dialog } from "../../../components/dialog";
import { ErrorBox, Spinner } from "../../../components/ui";
import { actionLabel } from "../../../lib/actions";
import { s } from "../../../strings";
import { confirmLabel, ImpactAcknowledgement, useImpactAck } from "../impact";

const previewQuery = (machine: string, enabled: boolean) =>
  queryOptions({
    queryKey: [...qk.resourceMaintenance(machine), "preview"],
    queryFn: () => unwrap(client.GET("/resources/{resource}/maintenance/preview", { params: { path: { resource: machine } } })),
    enabled,
    staleTime: 0,
  });

/** The known services maintenance would record; coverage stays partial. */
export function WorkloadPreview({ preview }: { preview: MachineMaintenancePreview }) {
  return (
    <section className="stack">
      <h3>{s.machine.workloads}</h3>
      <div className="banner warn">{s.machine.partial}</div>
      {preview.workloads.length ? (
        <ul>
          {preview.workloads.map((w) => (
            <li key={w.name}>
              <code>{w.name}</code> — {s.state[w.state] ?? w.state} (v{w.version})
            </li>
          ))}
        </ul>
      ) : (
        <p>{s.machine.noWorkloads}</p>
      )}
    </section>
  );
}

function ReasonField({ id, value, onChange }: { id: string; value: string; onChange: (v: string) => void }) {
  return (
    <div className="field">
      <label htmlFor={id}>{s.machine.reason}</label>
      <textarea id={id} maxLength={1000} value={value} onChange={(e) => onChange(e.target.value)} />
      <span className="hint">{s.machine.reasonHint}</span>
    </div>
  );
}

/** What the operator confirmed in a power dialog. */
export interface PowerConfirmation {
  reason: string;
  confirm: string;
  acknowledgeImpact: boolean;
}

/**
 * The one confirmation of a power request: the target, its consequences
 * (shown once), the impact on other resources, a reason and the typed
 * resource name. The version is the one the dialog was opened at.
 */
export function PowerDialog({
  resource,
  action,
  version,
  disclosure,
  unavailable,
  children,
  onConfirm,
  onClose,
}: {
  resource: Pick<ResourceDetail, "name" | "id">;
  action: string;
  version: number;
  disclosure: string;
  /** Why the request cannot be sent now; empty when it can. */
  unavailable: string;
  children?: ReactNode;
  onConfirm: (c: PowerConfirmation) => void;
  onClose: () => void;
}) {
  const [reason, setReason] = useState("");
  const [typed, setTyped] = useState("");
  const impact = useImpactAck(resource.name, action);
  const ready = !!reason.trim() && typed === resource.name && impact.ready && !unavailable;
  return (
    <Dialog
      title={actionLabel(action)}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {s.common.cancel}
          </button>
          <button
            type="button"
            className="btn danger solid"
            disabled={!ready}
            onClick={() => onConfirm({ reason: reason.trim(), confirm: typed, acknowledgeImpact: impact.acknowledged })}
          >
            {confirmLabel(s.common.confirm, actionLabel(action), impact)}
          </button>
        </>
      }
    >
      <div className="stack">
        <p>
          {s.operation.target}: <strong>{resource.name}</strong> / <code>{resource.id}</code> / {s.config.version(version)}
        </p>
        <div className="banner warn">{disclosure}</div>
        {children}
        <ImpactAcknowledgement state={impact} />
        {unavailable && <p className="meta">{unavailable}</p>}
        <ReasonField id="power-reason" value={reason} onChange={setReason} />
        <div className="field">
          <label htmlFor="power-confirm">{s.machine.confirmName(resource.name)}</label>
          <input id="power-confirm" autoComplete="off" spellCheck={false} value={typed} onChange={(e) => setTyped(e.target.value)} />
        </div>
      </div>
    </Dialog>
  );
}

/** The maintenance a machine power request enters, or the active one it keeps. */
export function PowerMaintenance({ machine, active }: { machine: string; active: { reason: string } | null | undefined }) {
  const preview = useQuery(previewQuery(machine, !active));
  if (active) return <p>{s.machine.keepsMaintenance(active.reason)}</p>;
  return (
    <>
      <p>{s.machine.entersMaintenance}</p>
      <ErrorBox error={preview.error} />
      {preview.isPending && <Spinner />}
      {preview.data && <WorkloadPreview preview={preview.data} />}
    </>
  );
}

/**
 * Entering or exiting maintenance without a power request. Entry shows the
 * workloads it records and sends the preview's hash, so it is refused if
 * they changed meanwhile; exit needs only a reason.
 */
export function MaintenanceDialog({ machine, exit, onClose }: { machine: Pick<ResourceDetail, "name" | "version">; exit: boolean; onClose: () => void }) {
  const preview = useQuery(previewQuery(machine.name, !exit));
  const [baseVersion] = useState(machine.version);
  const [reason, setReason] = useState("");
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: () =>
      exit
        ? unwrap(
            client.POST("/resources/{resource}/maintenance/exit", {
              params: { path: { resource: machine.name }, header: { "If-Match": String(baseVersion) } },
              body: { reason: reason.trim() },
            }),
          )
        : unwrap(
            client.POST("/resources/{resource}/maintenance", {
              params: { path: { resource: machine.name }, header: { "If-Match": String(baseVersion) } },
              body: { reason: reason.trim(), previewHash: preview.data?.hash ?? "" },
            }),
          ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.resourceAll(machine.name) });
      onClose();
    },
  });
  return (
    <Dialog
      title={actionLabel(exit ? "maintenance-exit" : "maintenance-enter")}
      onClose={() => !mutation.isPending && onClose()}
      footer={
        <>
          <button type="button" className="btn" disabled={mutation.isPending} onClick={onClose}>
            {s.common.cancel}
          </button>
          <button type="button" className="btn primary" disabled={mutation.isPending || !reason.trim() || (!exit && !preview.data)} onClick={() => mutation.mutate()}>
            {s.common.confirm}
          </button>
        </>
      }
    >
      <div className="stack">
        <p>
          {s.operation.target}: <strong>{machine.name}</strong>
        </p>
        {exit ? (
          <p>{s.machine.exitNote}</p>
        ) : (
          <>
            <p>{s.machine.maintenanceNote}</p>
            <ErrorBox error={preview.error} />
            {preview.isPending && <Spinner />}
            {preview.data && <WorkloadPreview preview={preview.data} />}
          </>
        )}
        <ReasonField id="maintenance-reason" value={reason} onChange={setReason} />
        <ErrorBox error={mutation.error} />
      </div>
    </Dialog>
  );
}
