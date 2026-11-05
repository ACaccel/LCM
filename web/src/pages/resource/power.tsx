import { useState } from "react";
import type { ReactNode } from "react";
import type { ResourceDetail } from "../../api/types";
import { PowerDialog } from "./machine/dialogs";
import { useDurableOperation } from "./operations";
import type { DurableOperation } from "./operations";

/**
 * The durable power requests of a machine or router (reboot, shutdown). The
 * state lives above the page header and the operations tab, so a button in
 * either opens the same confirmation and both show the same kept attempt.
 */
export interface PowerControl {
  op: DurableOperation;
  /** Actions this resource offers as requests. */
  offered: readonly string[];
  /** Why the action cannot be requested now; empty when it can. */
  reason: (action: string) => string;
  /** Whether the button of the action is disabled. */
  disabled: (action: string) => boolean;
  /** Opens the confirmation at the resource's current version. */
  request: (action: string) => void;
  /** The confirmation dialog while one is open, else null. */
  dialog: ReactNode;
}

export interface PowerConfig {
  offered: readonly string[];
  reason: (action: string) => string;
  /** Extra conditions that keep the buttons disabled. */
  blocked: (action: string) => boolean;
  /** Why the open confirmation cannot be sent; empty when it can. */
  unavailable: (action: string) => string;
  disclosure: string;
  /** Extra content of the confirmation, such as the maintenance a request enters. */
  extra?: ReactNode;
}

export function usePowerRequest(resource: ResourceDetail, config: PowerConfig): PowerControl {
  const op = useDurableOperation(resource);
  const [confirm, setConfirm] = useState<{ action: string; version: number } | null>(null);
  const dialog = confirm && (
    <PowerDialog
      resource={resource}
      action={confirm.action}
      version={confirm.version}
      disclosure={config.disclosure}
      unavailable={config.unavailable(confirm.action)}
      onClose={() => setConfirm(null)}
      onConfirm={(c) => {
        op.start(confirm.action, confirm.version, c);
        setConfirm(null);
      }}
    >
      {config.extra}
    </PowerDialog>
  );
  return {
    op,
    offered: config.offered,
    reason: config.reason,
    disabled: (action) => !!config.reason(action) || !!op.attempt || !!op.storageError || config.blocked(action),
    request: (action) => setConfirm({ action, version: resource.version }),
    dialog,
  };
}
