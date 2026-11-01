import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { isApiError } from "../../api/client";
import type { Machine, MachineCapability, MachineMaintenance, MachineOperation, PowerAction } from "../../api/machines";
import { machineOperationQuery, machineOperationsQuery, mk } from "../../api/machines";
import { Dialog } from "../../components/dialog";
import { ErrorBox, Pill, Spinner } from "../../components/ui";
import { useNow } from "../../hooks";
import { formatDateTime } from "../../lib/format";
import {
  clearMachineAttempt,
  machineFresh,
  newMachineAttempt,
  operationView,
  powerUnavailable,
  readMachineAttempt,
  submitMachineAttempt,
  writeMachineAttempt,
} from "../../lib/machine";
import type { MachineAttempt } from "../../lib/machine";
import { s } from "../../strings";
import { Acknowledgements, ResolveDialog, WorkloadDisclosure } from "./dialogs";

export function OperationCard({ name, op }: { name: string; op: MachineOperation }) {
  const [resolve, setResolve] = useState(false);
  return (
    <article className="panel stack">
      <div className="row">
        <strong>{s.machine.actions[op.action] ?? op.action}</strong>
        <Pill view={operationView(op)} />
      </div>
      <dl className="kv">
        <dt>{s.machine.operation}</dt>
        <dd>
          <code>{op.id}</code>
        </dd>
        <dt>{s.common.actor}</dt>
        <dd>
          {op.actor.name} ({op.actor.kind})
        </dd>
        <dt>{s.common.time}</dt>
        <dd>
          {formatDateTime(op.createdAt)} / {formatDateTime(op.updatedAt)}
        </dd>
        <dt>{s.machine.reason}</dt>
        <dd>{op.reason || s.common.none}</dd>
        <dt>{s.common.version}</dt>
        <dd>{op.version}</dd>
        <dt>{s.machine.bootId}</dt>
        <dd>{op.request.bootId}</dd>
        <dt>{s.machine.deadline}</dt>
        <dd>{formatDateTime(op.request.deadline)}</dd>
        <dt>{s.machine.receipt}</dt>
        <dd>
          {op.receipt ? (
            <>
              {s.machine.operationStates[op.receipt.state] ?? op.receipt.state} / {formatDateTime(op.receipt.acceptedAt)} / {op.receipt.bootId}
            </>
          ) : (
            s.machine.unknown
          )}
        </dd>
        {op.resolvedBy && (
          <>
            <dt>{s.machine.resolvedBy}</dt>
            <dd>
              {op.resolvedBy.name} / {formatDateTime(op.resolvedAt)}
            </dd>
            <dt>{s.machine.resolution}</dt>
            <dd>{op.resolution}</dd>
          </>
        )}
      </dl>
      {(op.resolvedBy || op.resolution) && <div className="banner warn">{s.machine.resolveNote}</div>}
      {op.unresolved && op.state === "indeterminate" && (
        <button className="btn danger" onClick={() => setResolve(true)}>
          {s.machine.resolve}
        </button>
      )}
      {resolve && <ResolveDialog name={name} id={op.id} version={op.version} onClose={() => setResolve(false)} />}
    </article>
  );
}

function loadAttempt(id: string): { attempt: MachineAttempt | null; error?: unknown } {
  try {
    return { attempt: readMachineAttempt(id) };
  } catch {
    return { attempt: null, error: new Error(s.machine.storageError) };
  }
}

export function MachineControls({
  machine,
  caps,
  maintenance,
  unavailable,
}: {
  machine: Machine;
  caps?: MachineCapability[];
  maintenance?: MachineMaintenance | null;
  unavailable?: boolean;
}) {
  const [stored, setStored] = useState(() => loadAttempt(machine.id));
  const [confirm, setConfirm] = useState<{ action: PowerAction; version: number } | null>(null);
  const [workload, setWorkload] = useState(false);
  const [recovery, setRecovery] = useState(false);
  const [ack, setAck] = useState(false);
  const now = useNow(true);
  const qc = useQueryClient();
  useEffect(() => {
    const sync = () => setStored(loadAttempt(machine.id));
    window.addEventListener("storage", sync);
    return () => window.removeEventListener("storage", sync);
  }, [machine.id]);
  const attempt = stored.attempt;
  const operation = useQuery(machineOperationQuery(machine.name, attempt?.operationId ?? ""));
  const mutation = useMutation({
    mutationFn: async (a: MachineAttempt) => {
      // The durable browser record must exist before any request can leave.
      const uncertain: MachineAttempt = { ...a, phase: "uncertain" };
      writeMachineAttempt(uncertain);
      setStored({ attempt: uncertain });
      try {
        const op = await submitMachineAttempt(machine.name, a);
        const accepted: MachineAttempt = { ...a, phase: "accepted", operationId: op.id };
        writeMachineAttempt(accepted);
        setStored({ attempt: accepted });
        qc.setQueryData(machineOperationQuery(machine.name, op.id).queryKey, op);
        return op;
      } catch (error) {
        // Only a first, definitive client rejection permits a new attempt.
        // Once a response is uncertain, a later error cannot prove no handoff.
        if (a.phase === "prepared" && isApiError(error) && [400, 401, 403, 404, 409, 413, 422].includes(error.status)) {
          const rejected: MachineAttempt = { ...a, phase: "rejected" };
          writeMachineAttempt(rejected);
          setStored({ attempt: rejected });
        }
        throw error;
      }
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: mk.all });
    },
  });
  function start() {
    if (!confirm || attempt || stored.error) return;
    try {
      const existing = readMachineAttempt(machine.id);
      if (existing) {
        setStored({ attempt: existing });
        setConfirm(null);
        return;
      }
      const a = newMachineAttempt({ ...machine, version: confirm.version }, confirm.action);
      writeMachineAttempt(a);
      setStored({ attempt: a });
      setConfirm(null);
      mutation.mutate(a);
    } catch {
      setStored({ attempt: null, error: new Error(s.machine.storageError) });
    }
  }
  const safelyFinished = attempt?.phase === "rejected" || (!!operation.data && !operation.data.unresolved);
  function finishAttempt() {
    if (!safelyFinished) return;
    try {
      clearMachineAttempt(machine.id);
      setStored({ attempt: null });
      mutation.reset();
    } catch {
      setStored({ attempt, error: new Error(s.machine.storageError) });
    }
  }
  const readiness =
    unavailable || !machineFresh(machine, now) || machine.status.identity !== "verified" || machine.status.health !== "healthy" || machine.phase !== "managed";
  return (
    <section className="panel stack">
      <h2>{s.machine.controls}</h2>
      <p>{s.machine.powerDisclosure}</p>
      {(["reboot", "graceful-shutdown", "power-on"] as const).map((action) => {
        const reason = powerUnavailable(action, caps);
        const disabled = !!reason || !!attempt || !!stored.error || readiness || !maintenance?.acknowledged || maintenance.state !== "active";
        return (
          <div className="stack" key={action}>
            <div>
              <button
                className="btn danger"
                disabled={disabled}
                onClick={() => {
                  if (action !== "power-on") {
                    setWorkload(false);
                    setRecovery(false);
                    setAck(false);
                    setConfirm({ action, version: machine.version });
                  }
                }}
              >
                {s.machine.actions[action]}
              </button>
            </div>
            {reason && <p className="meta">{reason}</p>}
          </div>
        );
      })}
      {!maintenance?.acknowledged && <p className="meta">{s.machine.requireMaintenance}</p>}
      <ErrorBox error={stored.error} />
      <ErrorBox error={mutation.error} />
      {attempt && (
        <div className="panel stack">
          <h3>{s.machine.retained}</h3>
          <code>{attempt.key}</code>
          <div>
            {s.machine.actions[attempt.action]} / {s.config.version(attempt.version)}
          </div>
          <div className="banner warn">
            {attempt.phase === "rejected" ? s.machine.rejected : attempt.phase === "accepted" ? s.machine.accepted : s.machine.uncertain}
          </div>
          {attempt.operationId ? (
            <button className="btn" disabled={operation.isFetching} onClick={() => void operation.refetch()}>
              {s.machine.queryOperation}
            </button>
          ) : (
            attempt.phase !== "rejected" && (
              <button className="btn" disabled={mutation.isPending} onClick={() => mutation.mutate(attempt)}>
                {s.machine.retryAttempt}
              </button>
            )
          )}
          {operation.data && <OperationCard name={machine.name} op={operation.data} />}
          <ErrorBox error={operation.error} />
          {safelyFinished && (
            <button className="btn" onClick={finishAttempt}>
              {s.machine.newAttempt}
            </button>
          )}
        </div>
      )}
      {confirm && (
        <Dialog
          title={s.machine.actions[confirm.action] ?? confirm.action}
          onClose={() => setConfirm(null)}
          footer={
            <>
              <button className="btn" onClick={() => setConfirm(null)}>
                {s.common.cancel}
              </button>
              <button
                className="btn danger solid"
                disabled={
                  !ack ||
                  !workload ||
                  !recovery ||
                  readiness ||
                  !!powerUnavailable(confirm.action, caps) ||
                  !maintenance?.acknowledged ||
                  maintenance.state !== "active"
                }
                onClick={start}
              >
                {s.common.confirm}
              </button>
            </>
          }
        >
          <div className="stack">
            <p>
              {s.machine.target}: <strong>{machine.name}</strong> / <code>{machine.id}</code> / {s.config.version(confirm.version)}
            </p>
            <div className="banner warn">{s.machine.powerDisclosure}</div>
            <p>{s.machine.noPowerOn}</p>
            <WorkloadDisclosure />
            <p>
              {s.machine.reason}: {maintenance?.reason}
            </p>
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

export function MachineOperations({ name }: { name: string }) {
  const q = useInfiniteQuery(machineOperationsQuery(name));
  return (
    <section className="stack">
      <h2>{s.machine.operations}</h2>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {q.data?.pages
        .flatMap((p) => p.items)
        .map((op) => (
          <OperationCard key={op.id} name={name} op={op} />
        ))}
      {q.data && !q.data.pages.some((p) => p.items.length) && <p>{s.common.empty}</p>}
      {q.hasNextPage && (
        <button className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
    </section>
  );
}
