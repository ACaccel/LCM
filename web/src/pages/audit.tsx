import { useInfiniteQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { auditQuery } from "../api/queries";
import type { AuditFilter } from "../api/queries";
import type { AuditEntry } from "../api/types";
import { Empty, ErrorBox, PageHead, Pill, Spinner, Tag } from "../components/ui";
import { formatDateTime, toLocalInput } from "../lib/format";
import type { StatusView } from "../lib/status";
import { s } from "../strings";

const OUTCOME_VIEWS: Record<AuditEntry["outcome"], StatusView> = {
  ok: { tone: "ok", icon: "✓", label: s.audit.outcomes["ok"] ?? "ok" },
  denied: { tone: "warn", icon: "▲", label: s.audit.outcomes["denied"] ?? "denied" },
  error: { tone: "bad", icon: "✕", label: s.audit.outcomes["error"] ?? "error" },
};

/** Actions on resources record the resource's name as their target. */
const RESOURCE_ACTION = /^(service|machine|router)\./;

function toISO(local: string): string | undefined {
  return local ? new Date(local).toISOString() : undefined;
}

function filterFromParams(params: URLSearchParams): AuditFilter {
  return {
    from: params.get("from") || undefined,
    to: params.get("to") || undefined,
    actor: params.get("actor") || undefined,
    action: params.get("action") || undefined,
    target: params.get("target") || undefined,
  };
}

function Target({ entry }: { entry: AuditEntry }) {
  if (!entry.target) return <span className="meta">{s.common.none}</span>;
  if (RESOURCE_ACTION.test(entry.action) && !entry.target.includes(":")) return <Link to={`/resources/${encodeURIComponent(entry.target)}`}>{entry.target}</Link>;
  return <code>{entry.target}</code>;
}

function Details({ details }: { details: AuditEntry["details"] }) {
  if (Object.keys(details).length === 0) return <span className="meta">{s.audit.noDetails}</span>;
  return (
    <details>
      <summary>{s.audit.showDetails}</summary>
      <pre className="block">{JSON.stringify(details, null, 2)}</pre>
    </details>
  );
}

/**
 * The audit log: who did what to which object, filtered by time, actor,
 * action and target on the server. The applied filters are in the URL.
 */
export function AuditPage() {
  const [params, setParams] = useSearchParams();
  const filter = filterFromParams(params);
  const q = useInfiniteQuery(auditQuery(filter));
  // The form keeps what was typed; only Apply moves it into the URL.
  const [form, setForm] = useState(() => ({
    from: toLocalInput(filter.from),
    to: toLocalInput(filter.to),
    actor: filter.actor ?? "",
    action: filter.action ?? "",
    target: filter.target ?? "",
  }));
  const [error, setError] = useState("");
  const rows = q.data?.pages.flatMap((p) => p.items) ?? [];

  function apply(e: FormEvent) {
    e.preventDefault();
    const from = toISO(form.from);
    const to = toISO(form.to);
    if (from && to && Date.parse(from) >= Date.parse(to)) {
      setError(s.audit.invalidRange);
      return;
    }
    setError("");
    const next = new URLSearchParams();
    for (const [key, value] of Object.entries({ from, to, actor: form.actor.trim(), action: form.action.trim(), target: form.target.trim() })) {
      if (value) next.set(key, value);
    }
    setParams(next, { replace: true });
  }
  function clear() {
    setForm({ from: "", to: "", actor: "", action: "", target: "" });
    setError("");
    setParams(new URLSearchParams(), { replace: true });
  }
  const field = (key: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, [key]: e.target.value });

  return (
    <div className="page">
      <PageHead title={s.audit.title} />
      <p className="meta">{s.audit.help}</p>
      <form className="filters" onSubmit={apply}>
        <div className="field">
          <label htmlFor="au-from">{s.audit.from}</label>
          <input id="au-from" type="datetime-local" value={form.from} onChange={field("from")} />
        </div>
        <div className="field">
          <label htmlFor="au-to">{s.audit.to}</label>
          <input id="au-to" type="datetime-local" value={form.to} onChange={field("to")} />
        </div>
        <div className="field">
          <label htmlFor="au-actor">{s.audit.actor}</label>
          <input id="au-actor" type="search" value={form.actor} onChange={field("actor")} />
        </div>
        <div className="field">
          <label htmlFor="au-action">{s.audit.action}</label>
          <input id="au-action" type="search" value={form.action} placeholder="service.deploy" title={s.audit.actionHint} onChange={field("action")} />
        </div>
        <div className="field">
          <label htmlFor="au-target">{s.audit.target}</label>
          <input id="au-target" type="search" value={form.target} onChange={field("target")} />
        </div>
        <button type="submit" className="btn primary">
          {s.common.confirm}
        </button>
        <button type="button" className="btn" onClick={clear}>
          {s.audit.clear}
        </button>
      </form>
      {error && (
        <p className="banner error" role="alert">
          {error}
        </p>
      )}
      {q.isPending && <Spinner />}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.data && rows.length === 0 && <Empty>{s.audit.empty}</Empty>}
      {rows.length > 0 && (
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>{s.audit.columns.time}</th>
                <th>{s.audit.columns.actor}</th>
                <th>{s.audit.columns.action}</th>
                <th>{s.audit.columns.target}</th>
                <th>{s.audit.columns.outcome}</th>
                <th>{s.audit.columns.details}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e) => (
                <tr key={e.id}>
                  <td>{formatDateTime(e.time)}</td>
                  <td>
                    {e.actor.name} <Tag>{s.audit.kinds[e.actor.kind] ?? e.actor.kind}</Tag>
                  </td>
                  <td>
                    <code>{e.action}</code>
                  </td>
                  <td>
                    <Target entry={e} />
                  </td>
                  <td>
                    <Pill view={OUTCOME_VIEWS[e.outcome]} />
                  </td>
                  <td>
                    <Details details={e.details} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.hasNextPage && (
        <button type="button" className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
    </div>
  );
}
