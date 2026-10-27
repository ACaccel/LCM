import type { ReactNode } from "react";
import type { UseQueryResult } from "@tanstack/react-query";
import { isApiError } from "../api/client";
import type { StatusView } from "../lib/status";
import { s } from "../strings";

export function Pill({ view, title }: { view: StatusView; title?: string }) {
  return (
    <span className={`pill ${view.tone}`} title={title}>
      <span className="icon" aria-hidden="true">
        {view.icon}
      </span>
      {view.label}
    </span>
  );
}

export function Tag({ children, title }: { children: ReactNode; title?: string }) {
  return (
    <span className="pill neutral" title={title}>
      {children}
    </span>
  );
}

export function Spinner({ label = s.common.loading }: { label?: string }) {
  return (
    <span role="status" aria-live="polite">
      <span className="spinner" aria-hidden="true" /> <span className="meta">{label}</span>
    </span>
  );
}

/** The API envelope's message, plus the paths of validation issues. Never swallowed. */
export function ErrorBox({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  if (!error) return null;
  const e = isApiError(error) ? error : undefined;
  const message = e ? e.message : error instanceof Error ? error.message : s.common.unknownError;
  const issues = e?.issues ?? [];
  return (
    <div className="banner error" role="alert">
      <div>
        {message}
        {e && e.code && e.code !== "error" ? <span className="meta"> ({e.code})</span> : null}
      </div>
      {issues.length > 0 && (
        <ul>
          {issues.map((i, n) => (
            <li key={`${i.path}-${n}`}>
              {i.path && <code>{i.path}</code>}
              {i.path ? ": " : ""}
              {i.message}
            </li>
          ))}
        </ul>
      )}
      {onRetry && (
        <div style={{ marginTop: 6 }}>
          <button type="button" className="btn small" onClick={onRetry}>
            {s.common.retry}
          </button>
        </div>
      )}
    </div>
  );
}

/** Renders loading and error states of a query; children only see data. */
export function Async<T>({ q, children }: { q: UseQueryResult<T>; children: (data: T) => ReactNode }) {
  if (q.isPending) return <Spinner />;
  if (q.isError) return <ErrorBox error={q.error} onRetry={() => void q.refetch()} />;
  return <>{children(q.data)}</>;
}

export function Empty({ children }: { children: ReactNode }) {
  return <div className="empty">{children}</div>;
}

export function Field({
  label,
  hint,
  htmlFor,
  children,
}: {
  label: string;
  hint?: ReactNode;
  htmlFor?: string;
  children: ReactNode;
}) {
  return (
    <div className="field">
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {hint && <span className="hint">{hint}</span>}
    </div>
  );
}

export function PageHead({ title, children, meta }: { title: ReactNode; children?: ReactNode; meta?: ReactNode }) {
  return (
    <div className="page-head">
      <div>
        <h1>{title}</h1>
        {meta}
      </div>
      {children && <div className="row">{children}</div>}
    </div>
  );
}
