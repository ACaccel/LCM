import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { capabilitiesQuery, maintenanceQuery } from "../../../api/observations";
import type { MachineMaintenance, ResourceCapability, ResourceDetail } from "../../../api/types";
import { Dialog } from "../../../components/dialog";
import { ErrorBox, Spinner } from "../../../components/ui";
import { useNow } from "../../../hooks";
import { actionLabel } from "../../../lib/actions";
import { formatDateTime } from "../../../lib/format";
import { machineFresh, powerUnavailable } from "../../../lib/machine";
import { s } from "../../../strings";
import { ImpactAcknowledgement, useImpactAck } from "../impact";
import { AttemptPanel, OperationsList, useDurableOperation } from "../operations";
import { Acknowledgements, MaintenanceDialog, WorkloadDisclosure } from "./dialogs";

const POWER = ["reboot", "graceful-shutdown", "power-on"] as const;

/**
 * Reboot and shutdown of the local machine. They need fresh observations,
 * a verified identity, healthy status and acknowledged maintenance, and
 * every confirmation; power-on is never offered because there is no
 * independent channel.
 */
export function MachineControls({
  resource,
  caps,
  maintenance,
  unavailable,
}: {
  resource: ResourceDetail;
  caps?: ResourceCapability[];
  maintenance?: MachineMaintenance | null;
  unavailable?: boolean;
}) {
  const [confirm, setConfirm] = useState<{ action: string; version: number } | null>(null);
  const [workload, setWorkload] = useState(false);
  const [recovery, setRecovery] = useState(false);
  const [ack, setAck] = useState(false);
  const impact = useImpactAck(resource.name, confirm?.action ?? "", !!confirm);
  const now = useNow(true);
  const op = useDurableOperation(resource);
  const status = resource.machine?.status;
  const readiness = unavailable || !machineFresh(resource, now) || status?.identity !== "verified" || status.health !== "healthy" || resource.phase !== "managed";
  const inMaintenance = !!maintenance?.acknowledged && maintenance.state === "active";
  return (
    <section className="panel stack">
      <h2>{s.machine.controls}</h2>
      <p>{s.machine.powerDisclosure}</p>
      {POWER.map((action) => {
        const reason = powerUnavailable(action, caps);
        const disabled = !!reason || !!op.attempt || !!op.storageError || readiness || !inMaintenance;
        return (
          <div className="stack" key={action}>
            <div>
              <button
                type="button"
                className="btn danger"
                disabled={disabled}
                onClick={() => {
                  if (action === "power-on") return;
                  setWorkload(false);
                  setRecovery(false);
                  setAck(false);
                  impact.setChecked(false);
                  setConfirm({ action, version: resource.version });
                }}
              >
                {actionLabel(action)}
              </button>
            </div>
            {reason && <p className="meta">{reason}</p>}
          </div>
        );
      })}
      {!maintenance?.acknowledged && <p className="meta">{s.machine.requireMaintenance}</p>}
      <ErrorBox error={op.storageError} />
      <ErrorBox error={op.mutation.error} />
      <AttemptPanel resource={resource.name} op={op} />
      {confirm && (
        <Dialog
          title={actionLabel(confirm.action)}
          onClose={() => setConfirm(null)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => setConfirm(null)}>
                {s.common.cancel}
              </button>
              <button
                type="button"
                className="btn danger solid"
                disabled={!ack || !workload || !recovery || !impact.ready || readiness || !!powerUnavailable(confirm.action, caps) || !inMaintenance}
                onClick={() => {
                  op.start(confirm.action, confirm.version, impact.acknowledged);
                  setConfirm(null);
                }}
              >
                {s.common.confirm}
              </button>
            </>
          }
        >
          <div className="stack">
            <p>
              {s.operation.target}: <strong>{resource.name}</strong> / <code>{resource.id}</code> / {s.config.version(confirm.version)}
            </p>
            <div className="banner warn">{s.machine.powerDisclosure}</div>
            <p>{s.machine.noPowerOn}</p>
            <WorkloadDisclosure />
            <p>
              {s.machine.reason}: {maintenance?.reason}
            </p>
            <ImpactAcknowledgement state={impact} />
            <Acknowledgements workload={workload} recovery={recovery} onWorkload={setWorkload} onRecovery={setRecovery} />
            <label className="check">
              <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} />
              {s.machine.powerAck}
            </label>
          </div>
        </Dialog>
      )}
    </section>
  );
}

/** The machine's maintenance and operations tab. */
export function MachineOperationsTab({ resource, selector }: { resource: ResourceDetail; selector: string }) {
  const caps = useQuery(capabilitiesQuery(selector));
  const maintenance = useQuery(maintenanceQuery(selector));
  const [dialog, setDialog] = useState<"enter" | "exit" | null>(null);
  const active = maintenance.data?.maintenance;
  return (
    <div className="stack">
      <p className="meta">{s.machine.refreshNote}</p>
      <section className="panel stack">
        <h2>{s.machine.maintenance}</h2>
        <p className="meta">{s.machine.maintenanceNote}</p>
        <ErrorBox error={maintenance.error} onRetry={() => void maintenance.refetch()} />
        {maintenance.isPending && <Spinner />}
        {maintenance.data &&
          (active ? (
            <>
              <strong>{s.machine.maintenanceActive}</strong>
              <dl className="kv">
                <dt>{s.common.actor}</dt>
                <dd>{active.actor.name}</dd>
                <dt>{s.machine.reason}</dt>
                <dd>{active.reason}</dd>
                <dt>{s.common.time}</dt>
                <dd>{formatDateTime(active.enteredAt)}</dd>
              </dl>
            </>
          ) : (
            <p>{s.machine.noMaintenance}</p>
          ))}
        <div>
          <button
            type="button"
            className="btn"
            disabled={!maintenance.isSuccess || maintenance.isRefetchError || resource.phase !== "managed"}
            onClick={() => setDialog(active ? "exit" : "enter")}
          >
            {active ? actionLabel("maintenance-exit") : actionLabel("maintenance-enter")}
          </button>
        </div>
      </section>
      <ErrorBox error={caps.error} onRetry={() => void caps.refetch()} />
      <MachineControls
        resource={resource}
        caps={caps.isSuccess ? caps.data.items : undefined}
        maintenance={active}
        unavailable={caps.isError || maintenance.isError || maintenance.isPending}
      />
      <OperationsList resource={selector} />
      {dialog && <MaintenanceDialog machine={resource} exit={dialog === "exit"} onClose={() => setDialog(null)} />}
    </div>
  );
}
