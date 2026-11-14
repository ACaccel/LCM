import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { ReactNode } from "react";
import { capabilitiesQuery, maintenanceQuery } from "../../../api/observations";
import type { MachineMaintenance, ResourceCapability, ResourceDetail } from "../../../api/types";
import { ErrorBox, Spinner } from "../../../components/ui";
import { useTick } from "../../../hooks";
import { actionLabel, unavailableReason } from "../../../lib/actions";
import { formatDateTime } from "../../../lib/format";
import { machineFresh, powerUnavailable } from "../../../lib/machine";
import { s } from "../../../strings";
import { AttemptPanel, OperationsList } from "../operations";
import { usePowerRequest } from "../power";
import type { PowerControl } from "../power";
import { IdentityRebindDialog, MaintenanceDialog, PowerMaintenance } from "./dialogs";

const POWER = ["reboot", "graceful-shutdown"] as const;

interface MachinePowerInput {
  caps?: ResourceCapability[];
  maintenance?: MachineMaintenance | null;
  /** The capabilities or the maintenance could not be read. */
  unavailable?: boolean;
}

/**
 * Reboot and shutdown of the local machine. They need fresh verified
 * healthy observations and a ready helper; one confirmed request enters
 * maintenance (or keeps the active one) and submits. Power-on is not
 * offered: there is no independent channel.
 */
export function useMachinePower(resource: ResourceDetail, { caps, maintenance, unavailable }: MachinePowerInput): PowerControl {
  // Freshness is measured against the clock, so it is re-evaluated on a slow tick.
  const now = useTick(5000);
  const status = resource.machine?.status;
  const notReady =
    !!unavailable || !machineFresh(resource, now) || status?.identity !== "verified" || status.health !== "healthy" || resource.phase !== "managed";
  return usePowerRequest(resource, {
    offered: POWER,
    reason: (action) => powerUnavailable(action, caps),
    blocked: () => notReady,
    unavailable: (action) => (notReady ? s.machine.notReady : powerUnavailable(action, caps)),
    disclosure: s.machine.powerDisclosure,
    extra: <PowerMaintenance machine={resource.name} active={maintenance?.state === "active" ? maintenance : null} />,
  });
}

/** Reads what the machine power requests depend on and hands the control to a page. */
export function MachinePowerHost({
  resource,
  selector,
  children,
}: {
  resource: ResourceDetail;
  selector: string;
  children: (power: PowerControl) => ReactNode;
}) {
  const caps = useQuery(capabilitiesQuery(selector));
  const maintenance = useQuery(maintenanceQuery(selector));
  const power = useMachinePower(resource, {
    caps: caps.isSuccess ? caps.data.items : undefined,
    maintenance: maintenance.data?.maintenance,
    unavailable: caps.isError || maintenance.isError || maintenance.isPending,
  });
  return (
    <>
      {children(power)}
      {power.dialog}
    </>
  );
}

/** The power requests of a machine with their kept attempt. */
export function MachineControls({ resource, power }: { resource: ResourceDetail; power: PowerControl }) {
  const { op } = power;
  return (
    <section className="panel stack">
      <h2>{s.machine.controls}</h2>
      {power.offered.map((action) => {
        const reason = power.reason(action);
        return (
          <div className="stack" key={action}>
            <div>
              <button type="button" className="btn danger" disabled={power.disabled(action)} onClick={() => power.request(action)}>
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
    </section>
  );
}

/**
 * Offered while the machine reports another device identity than the one
 * LCM expects: accepting it keeps the resource, its history and its
 * helper binding. Nothing is shown while the identity is not changed.
 */
export function IdentityPanel({ resource }: { resource: ResourceDetail }) {
  const [open, setOpen] = useState(false);
  if (resource.machine?.status?.identity !== "changed") return null;
  const blocked = unavailableReason(resource, "rebind-identity");
  return (
    <section className="panel stack">
      <h2>{s.machine.identityTitle}</h2>
      <div className="banner warn">{s.machine.identityChanged}</div>
      <p className="meta">{s.machine.identityNote}</p>
      <div>
        <button type="button" className="btn danger" disabled={!!blocked} onClick={() => setOpen(true)}>
          {s.machine.identityAction}
        </button>
      </div>
      {blocked && <p className="meta">{blocked}</p>}
      {open && <IdentityRebindDialog machine={resource} onClose={() => setOpen(false)} />}
    </section>
  );
}

/** The machine's maintenance and operations tab. */
export function MachineOperationsTab({ resource, selector, power }: { resource: ResourceDetail; selector: string; power: PowerControl }) {
  const caps = useQuery(capabilitiesQuery(selector));
  const maintenance = useQuery(maintenanceQuery(selector));
  const [dialog, setDialog] = useState<"enter" | "exit" | null>(null);
  const active = maintenance.data?.maintenance;
  return (
    <div className="stack">
      <p className="meta">{s.machine.refreshNote}</p>
      <IdentityPanel resource={resource} />
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
      <MachineControls resource={resource} power={power} />
      <OperationsList resource={selector} />
      {dialog && <MaintenanceDialog machine={resource} exit={dialog === "exit"} onClose={() => setDialog(null)} />}
    </div>
  );
}
