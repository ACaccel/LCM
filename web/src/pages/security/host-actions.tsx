import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { ReactNode } from "react";
import { isApiError } from "../../api/client";
import { applyHostAction, hostOperationQuery, hostOperationsQuery, previewHostAction, sk } from "../../api/security";
import type { HostAction, HostPreview, HostSettings, HostStatus } from "../../api/security";
import { Dialog } from "../../components/dialog";
import { ErrorBox, Field, Spinner } from "../../components/ui";
import { useNow } from "../../hooks";
import { sec } from "../../strings";
import { clearAttempt, readAttempt, saveAttempt } from "./attempts";
import type { Attempt } from "./attempts";
import { MutationError, UnknownReport, showTime } from "./shared";

export type BeginHostAction = (action: HostAction, settings: HostSettings, reason: string) => void;

interface Prepared {
  preview: HostPreview;
  reason: string;
  digest: string;
}

/** Operation states after which the browser record of an attempt can be dropped. */
const SETTLED_STATES = ["succeeded", "failed", "awaiting-local-approval"];

/**
 * Runs host actions as preview, review and apply. The attempt is stored
 * before the apply request leaves, so a lost response can be queried after a
 * reload instead of being sent again.
 */
export function HostActions({ host, children }: { host: HostStatus; children: (begin: BeginHostAction, busy: boolean) => ReactNode }) {
  const key = `lcm:security:host-attempt:${host.machineId}`;
  const [attempt, setAttempt] = useState<Attempt | null>(() => readAttempt(key));
  const [prepared, setPrepared] = useState<Prepared | null>(null);
  const [storageError, setStorageError] = useState("");
  const operation = useQuery({ ...hostOperationQuery(attempt?.requestId ?? ""), enabled: Boolean(attempt), retry: false });
  const qc = useQueryClient();
  const preview = useMutation({
    mutationFn: (input: { action: HostAction; settings: HostSettings; reason: string; expectedDigest: string }) =>
      previewHostAction({ requestId: crypto.randomUUID(), ...input }),
    onSuccess: (value, variables) => setPrepared({ preview: value, reason: variables.reason, digest: variables.expectedDigest }),
  });
  const apply = useMutation({
    mutationFn: (value: Attempt) => applyHostAction({ requestId: value.requestId, previewHash: value.previewHash ?? "", reason: value.reason }),
    onSuccess: () => {
      setPrepared(null);
      void qc.invalidateQueries({ queryKey: sk.all });
    },
  });

  function begin(action: HostAction, settings: HostSettings, reason: string) {
    if (reason.trim() === "" || attempt) return;
    preview.mutate({ action, settings, reason: reason.trim(), expectedDigest: host.effectiveDigest });
  }
  function confirm() {
    if (!prepared || Date.parse(prepared.preview.expiresAt) <= Date.now() || prepared.digest !== host.effectiveDigest) return;
    const value = { requestId: prepared.preview.requestId, previewHash: prepared.preview.previewHash, reason: prepared.reason };
    try {
      saveAttempt(key, value);
    } catch {
      setStorageError(sec.storageError);
      return;
    }
    setAttempt(value);
    apply.mutate(value);
  }
  function closeAttempt() {
    try {
      clearAttempt(key);
      setAttempt(null);
      apply.reset();
    } catch {
      setStorageError(sec.storageError);
    }
  }

  const settled = operation.data !== undefined && SETTLED_STATES.includes(operation.data.state);
  const missing = isApiError(operation.error) && operation.error.status === 404;
  return (
    <div className="stack">
      {children(begin, Boolean(attempt) || preview.isPending || apply.isPending)}
      <MutationError error={preview.error} />
      {storageError && (
        <p className="banner error" role="alert">
          {storageError}
        </p>
      )}
      {attempt && (
        <section className="panel stack">
          <h3>{sec.operation}</h3>
          <p>
            {sec.operationId}：<code>{attempt.requestId}</code>
          </p>
          <p className="meta">{sec.retainAttempt}</p>
          {operation.data ? (
            <>
              <p role="status">{sec.operationStates[operation.data.state]}</p>
              <p>{operation.data.reason}</p>
              <p className="meta">
                {operation.data.actor} · {showTime(operation.data.updatedAt)}
              </p>
              {operation.data.error && <p className="banner error">{operation.data.error}</p>}
              <UnknownReport value={operation.data.result} />
            </>
          ) : (
            <p className="banner warn">{sec.uncertain}</p>
          )}
          <ErrorBox error={operation.error} />
          <MutationError error={apply.error} />
          <div className="row">
            <button type="button" className="btn" disabled={operation.isFetching} onClick={() => void operation.refetch()}>
              {sec.queryOperation}
            </button>
            {missing && (
              <button type="button" className="btn" disabled={apply.isPending || !host.available} onClick={() => apply.mutate(attempt)}>
                {sec.applyHost}
              </button>
            )}
            {settled && (
              <button type="button" className="btn" onClick={closeAttempt}>
                {sec.close}
              </button>
            )}
          </div>
        </section>
      )}
      {prepared && (
        <PreviewDialog
          prepared={prepared}
          host={host}
          busy={apply.isPending}
          error={apply.error}
          onConfirm={confirm}
          onClose={() => {
            if (!apply.isPending) setPrepared(null);
          }}
        />
      )}
      <HostOperations />
    </div>
  );
}

function PreviewDialog({
  prepared,
  host,
  busy,
  error,
  onConfirm,
  onClose,
}: {
  prepared: Prepared;
  host: HostStatus;
  busy: boolean;
  error: unknown;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const now = useNow(true);
  const [confirmed, setConfirmed] = useState(false);
  const expired = Date.parse(prepared.preview.expiresAt) <= now || prepared.digest !== host.effectiveDigest;
  const capable = host.available && host.capabilities.some((item) => item.action === prepared.preview.action && item.enabled);
  return (
    <Dialog
      title={sec.previewTitle}
      onClose={onClose}
      pending={busy}
      footer={
        <>
          <button className="btn" type="button" disabled={busy} onClick={onClose}>
            {sec.cancel}
          </button>
          <button className="btn primary" type="button" disabled={busy || !confirmed || expired || !capable} onClick={onConfirm}>
            {sec.applyHost}
          </button>
        </>
      }
    >
      <div className="stack">
        <p>
          <code>{prepared.preview.action}</code> · <code>{host.machineId}</code>
        </p>
        <p>{prepared.reason}</p>
        <UnknownReport value={prepared.preview.changes} />
        <p className="meta">
          {sec.previewExpires}：{showTime(prepared.preview.expiresAt)}
        </p>
        {prepared.preview.requiresLocalApproval && <p className="banner warn">{sec.localApproval}</p>}
        {expired && <p className="banner warn">{sec.previewStale}</p>}
        <label className="check">
          <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} />
          {sec.confirmHost}
        </label>
        <MutationError error={error} />
      </div>
    </Dialog>
  );
}

export function HostOperations() {
  const q = useInfiniteQuery(hostOperationsQuery());
  const rows = q.data?.pages.flatMap((page) => page.items) ?? [];
  return (
    <section className="panel stack">
      <h2>{sec.operations}</h2>
      {q.isPending && <Spinner />}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.data && rows.length === 0 && <p className="meta">{sec.operationEmpty}</p>}
      {rows.map((item) => (
        <details key={item.id}>
          <summary>
            {sec.operationStates[item.state]} · {item.action} · {showTime(item.updatedAt)}
          </summary>
          <dl className="kv">
            <dt>{sec.operationId}</dt>
            <dd>
              <code>{item.id}</code>
            </dd>
            <dt>{sec.reason}</dt>
            <dd>{item.reason}</dd>
            <dt>{sec.updated}</dt>
            <dd>
              {item.actor} · {showTime(item.updatedAt)}
            </dd>
          </dl>
          {item.error && <p className="banner error">{item.error}</p>}
          <UnknownReport value={item.result} />
        </details>
      ))}
      {q.hasNextPage && (
        <button type="button" className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {sec.more}
        </button>
      )}
    </section>
  );
}

export function Reason({ id, value, onChange }: { id: string; value: string; onChange: (value: string) => void }) {
  return (
    <Field label={sec.reason} htmlFor={id}>
      <input id={id} required value={value} onChange={(e) => onChange(e.target.value)} />
    </Field>
  );
}
