import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { capabilitiesQuery, maintenanceQuery } from "../../../api/observations";
import type { MachineMaintenance, ResourceCapability, ResourceDetail } from "../../../api/types";
import { ErrorBox, Spinner } from "../../../components/ui";
import { useNow } from "../../../hooks";
import { actionLabel } from "../../../lib/actions";
import { formatDateTime } from "../../../lib/format";
import { machineFresh, powerUnavailable } from "../../../lib/machine";
import { s } from "../../../strings";
import { AttemptPanel, OperationsList, useDurableOperation } from "../operations";
import { MaintenanceDialog, PowerDialog, PowerMaintenance } from "./dialogs";

const POWER = ["reboot", "graceful-shutdown"] as const;

/**
 * Reboot and shutdown of the local machine. They need fresh verified
 * healthy observations and a ready helper; one confirmed request enters
 * maintenance (or keeps the active one) and submits. Power-on is not
 * offered: there is no independent channel.
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
  const now = useNow(true);
  const op = useDurableOperation(resource);
  const status = resource.machine?.status;
  const readiness = unavailable || !machineFresh(resource, now) || status?.identity !== "verified" || status.health !== "healthy" || resource.phase !== "managed";
  return (
    <section className="panel stack">
      <h2>{s.machine.controls}</h2>
      {POWER.map((action) => {
        const reason = powerUnavailable(action, caps);
        return (
          <div className="stack" key={action}>
            <div>
              <button
                type="button"
                className="btn danger"
                disabled={!!reason || !!op.attempt || !!op.storageError || readiness}
                onClick={() => setConfirm({ action, version: resource.version })}
              >
                {actionLabel(action)}
              </button>
            </div>
            {reason && <p className="meta">{reason}</p>}
          </div>
        );
      })}
      <p className="meta">{s.machine.noPowerOn}</p>
      <ErrorBox error={op.storageError} />
      <ErrorBox error={op.mutation.error} />
      <AttemptPanel resource={resource.name} op={op} />
      {confirm && (
        <PowerDialog
          resource={resource}
          action={confirm.action}
          version={confirm.version}
          disclosure={s.machine.powerDisclosure}
          unavailable={readiness ? s.machine.notReady : powerUnavailable(confirm.action, caps)}
          onClose={() => setConfirm(null)}
          onConfirm={(c) => {
            op.start(confirm.action, confirm.version, c);
            setConfirm(null);
          }}
        >
          <PowerMaintenance machine={resource.name} active={maintenance?.state === "active" ? maintenance : null} />
        </PowerDialog>
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
                {active.action && (
                  <>
                    <dt>{s.machine.enteredFor}</dt>
                    <dd>{actionLabel(active.action)}</dd>
                  </>
                )}
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
