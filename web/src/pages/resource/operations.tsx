import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { client, isApiError, unwrap } from "../../api/client";
import { operationQuery, operationsQuery } from "../../api/observations";
import { qk } from "../../api/queries";
import type { ResourceOperation } from "../../api/types";
import { Dialog } from "../../components/dialog";
import { ErrorBox, Pill, Spinner } from "../../components/ui";
import { actionLabel } from "../../lib/actions";
import { formatDateTime } from "../../lib/format";
import { clearAttempt, newAttempt, operationView, readAttempt, submitAttempt, writeAttempt } from "../../lib/operation";
import type { AttemptConfirmation, OperationAttempt } from "../../lib/operation";
import { s } from "../../strings";

const TIME_KEY = /(At|deadline|validUntil)$/;

/** Scalar fields of a type-specific object (request, receipt, evidence); nested values are left out. */
function OpenFields({ value }: { value: Record<string, unknown> | null | undefined }) {
  const entries = Object.entries(value ?? {}).filter(([, v]) => typeof v === "string" || typeof v === "number" || typeof v === "boolean");
  if (!entries.length) return <>{s.machine.unknown}</>;
  return (
    <>
      {entries.map(([k, v]) => (
        <div key={k}>
          <span className="meta">{k}</span>: {typeof v === "string" && TIME_KEY.test(k) ? formatDateTime(v) : String(v)}
        </div>
      ))}
    </>
  );
}

export function ResolveDialog({ resource, id, version, onClose }: { resource: string; id: string; version: number; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const [ack, setAck] = useState(false);
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: () =>
      unwrap(
        client.POST("/resources/{resource}/operations/{id}/resolve", {
          params: { path: { resource, id }, header: { "If-Match": String(version) } },
          body: { reason: reason.trim() },
        }),
      ),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.resourceAll(resource) });
      onClose();
    },
  });
  return (
    <Dialog
      title={s.operation.resolve}
      onClose={() => !mutation.isPending && onClose()}
      footer={
        <>
          <button type="button" className="btn" disabled={mutation.isPending} onClick={onClose}>
            {s.common.cancel}
          </button>
          <button type="button" className="btn danger" disabled={mutation.isPending || !reason.trim() || !ack} onClick={() => mutation.mutate()}>
            {s.operation.resolve}
          </button>
        </>
      }
    >
      <div className="stack">
        <p>
          {s.operation.target}: {resource} / <code>{id}</code>
        </p>
        <div className="banner warn">{s.operation.resolveNote}</div>
        <div className="field">
          <label htmlFor="resolve-reason">{s.operation.reason}</label>
          <textarea id="resolve-reason" maxLength={1000} value={reason} onChange={(e) => setReason(e.target.value)} />
        </div>
        <label className="check">
          <input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} />
          {s.operation.resolveAck}
        </label>
        <ErrorBox error={mutation.error} />
      </div>
    </Dialog>
  );
}

export function OperationCard({ resource, op }: { resource: string; op: ResourceOperation }) {
  const [resolve, setResolve] = useState(false);
  return (
    <article className="panel stack">
      <div className="row">
        <strong>{actionLabel(op.action)}</strong>
        <Pill view={operationView(op)} />
      </div>
      <dl className="kv">
        <dt>{s.operation.operation}</dt>
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
        <dt>{s.operation.reason}</dt>
        <dd>{op.reason || s.common.none}</dd>
        <dt>{s.common.version}</dt>
        <dd>{op.version}</dd>
        <dt>{s.operation.request}</dt>
        <dd>
          <OpenFields value={op.request} />
        </dd>
        <dt>{s.operation.receipt}</dt>
        <dd>
          <OpenFields value={op.receipt} />
        </dd>
        {op.evidence && Object.keys(op.evidence).length > 0 && (
          <>
            <dt>{s.operation.evidence}</dt>
            <dd>
              <OpenFields value={op.evidence} />
            </dd>
          </>
        )}
        {op.resolvedBy && (
          <>
            <dt>{s.operation.resolvedBy}</dt>
            <dd>
              {op.resolvedBy.name} / {formatDateTime(op.resolvedAt)}
            </dd>
            <dt>{s.operation.resolution}</dt>
            <dd>{op.resolution}</dd>
          </>
        )}
      </dl>
      {(op.resolvedBy || op.resolution) && <div className="banner warn">{s.operation.resolveNote}</div>}
      {op.unresolved && op.state === "indeterminate" && (
        <button type="button" className="btn danger" onClick={() => setResolve(true)}>
          {s.operation.resolve}
        </button>
      )}
      {resolve && <ResolveDialog resource={resource} id={op.id} version={op.version} onClose={() => setResolve(false)} />}
    </article>
  );
}

export function OperationsList({ resource }: { resource: string }) {
  const q = useInfiniteQuery(operationsQuery(resource));
  const items = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <section className="stack">
      <h2>{s.operation.title}</h2>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {items.map((op) => (
        <OperationCard key={op.id} resource={resource} op={op} />
      ))}
      {q.data && !items.length && <p>{s.common.empty}</p>}
      {q.hasNextPage && (
        <button type="button" className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
    </section>
  );
}

function loadAttempt(id: string): { attempt: OperationAttempt | null; error?: unknown } {
  try {
    return { attempt: readAttempt(id) };
  } catch {
    return { attempt: null, error: new Error(s.operation.storageError) };
  }
}

/**
 * The browser side of a durable operation. The request is persisted before
 * it is sent; after an uncertain answer only the same request (same key and
 * version) may be retried, and a new attempt is possible only once the
 * server definitively rejected the first one or its outcome is settled.
 */
export function useDurableOperation(resource: { id: string; name: string }) {
  const [stored, setStored] = useState(() => loadAttempt(resource.id));
  const qc = useQueryClient();
  useEffect(() => {
    const sync = () => setStored(loadAttempt(resource.id));
    window.addEventListener("storage", sync);
    return () => window.removeEventListener("storage", sync);
  }, [resource.id]);
  const attempt = stored.attempt;
  const operation = useQuery(operationQuery(resource.name, attempt?.operationId ?? ""));
  const mutation = useMutation({
    mutationFn: async (a: OperationAttempt) => {
      // The durable browser record must exist before any request can leave.
      const uncertain: OperationAttempt = { ...a, phase: "uncertain" };
      writeAttempt(uncertain);
      setStored({ attempt: uncertain });
      try {
        const op = await submitAttempt(resource.name, a);
        const accepted: OperationAttempt = { ...a, phase: "accepted", operationId: op.id };
        writeAttempt(accepted);
        setStored({ attempt: accepted });
        qc.setQueryData(operationQuery(resource.name, op.id).queryKey, op);
        return op;
      } catch (error) {
        // Only a first, definitive client rejection permits a new attempt.
        // Once a response is uncertain, a later error cannot prove no handoff.
        if (a.phase === "prepared" && isApiError(error) && [400, 401, 403, 404, 409, 413, 422].includes(error.status)) {
          const rejected: OperationAttempt = { ...a, phase: "rejected" };
          writeAttempt(rejected);
          setStored({ attempt: rejected });
        }
        throw error;
      }
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.resourceAll(resource.name) });
    },
  });

  /** Persists and sends a new request; returns false when an attempt is already kept. */
  function start(action: string, version: number, confirmation: AttemptConfirmation): boolean {
    if (attempt || stored.error) return false;
    try {
      const existing = readAttempt(resource.id);
      if (existing) {
        setStored({ attempt: existing });
        return false;
      }
      const a = newAttempt({ id: resource.id, version }, action, confirmation);
      writeAttempt(a);
      setStored({ attempt: a });
      mutation.mutate(a);
      return true;
    } catch {
      setStored({ attempt: null, error: new Error(s.operation.storageError) });
      return false;
    }
  }

  const safelyFinished = attempt?.phase === "rejected" || (!!operation.data && !operation.data.unresolved);
  function finish() {
    if (!safelyFinished) return;
    try {
      clearAttempt(resource.id);
      setStored({ attempt: null });
      mutation.reset();
    } catch {
      setStored({ attempt, error: new Error(s.operation.storageError) });
    }
  }

  return { attempt, storageError: stored.error, operation, mutation, start, finish, safelyFinished };
}

export type DurableOperation = ReturnType<typeof useDurableOperation>;

/** The kept attempt: its exact request, what is known of its outcome, and the safe next steps. */
export function AttemptPanel({ resource, op }: { resource: string; op: DurableOperation }) {
  const { attempt, operation, mutation, safelyFinished } = op;
  if (!attempt) return null;
  return (
    <div className="panel stack">
      <h3>{s.operation.retained}</h3>
      <code>{attempt.key}</code>
      <div>
        {actionLabel(attempt.action)} / {s.config.version(attempt.version)}
      </div>
      <div className="banner warn">
        {attempt.phase === "rejected" ? s.operation.rejected : attempt.phase === "accepted" ? s.operation.accepted : s.operation.uncertain}
      </div>
      {attempt.operationId ? (
        <button type="button" className="btn" disabled={operation.isFetching} onClick={() => void operation.refetch()}>
          {s.operation.queryOperation}
        </button>
      ) : (
        attempt.phase !== "rejected" && (
          <button type="button" className="btn" disabled={mutation.isPending} onClick={() => mutation.mutate(attempt)}>
            {s.operation.retryAttempt}
          </button>
        )
      )}
      {operation.data && <OperationCard resource={resource} op={operation.data} />}
      <ErrorBox error={operation.error} />
      {safelyFinished && (
        <button type="button" className="btn" onClick={op.finish}>
          {s.operation.newAttempt}
        </button>
      )}
    </div>
  );
}
