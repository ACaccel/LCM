import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { incidentQuery, incidentsQuery, securityAPI, sk } from "../../api/security";
import type { DetectionState, HandlingInput, Incident, IncidentFilter, Severity, Source } from "../../api/security";
import { Dialog } from "../../components/dialog";
import { Async, Empty, ErrorBox, Field, Spinner, Tag } from "../../components/ui";
import { MutationError, severities, showTime, sources, states, validReason } from "./shared";
import { sec } from "./strings";

function toISO(value: string): string | undefined { return value ? new Date(value).toISOString() : undefined; }
function localDate(value: string | null): string { if (!value || !Number.isFinite(Date.parse(value))) return ""; const date = new Date(value); return new Date(date.getTime() - date.getTimezoneOffset() * 60_000).toISOString().slice(0, 16); }
function fromParams(params: URLSearchParams, machineId: string): IncidentFilter {
  const source = params.get("source"); const severity = params.get("severity"); const state = params.get("state");
  return { machineId: machineId || undefined, service: params.get("service") || undefined,
    source: sources.includes(source as Source) ? source as Source : undefined,
    severity: severities.includes(severity as Severity) ? severity as Severity : undefined,
    state: states.includes(state as DetectionState) ? state as DetectionState : undefined,
    from: params.get("from") || undefined, to: params.get("to") || undefined };
}
export function SecurityIncidents({ machineId }: { machineId: string }) {
  const [params, setParams] = useSearchParams();
  const filter = fromParams(params, machineId);
  const q = useInfiniteQuery(incidentsQuery(filter));
  const [error, setError] = useState("");
  const id = params.get("incident");
  const rows = q.data?.pages.flatMap((page) => page.items) ?? [];
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault(); const values = new FormData(e.currentTarget); const next = new URLSearchParams(params);
    try {
      const from = toISO(String(values.get("from") || "")); const to = toISO(String(values.get("to") || ""));
      if (from && to && Date.parse(from) >= Date.parse(to)) throw new Error(sec.filterInvalid);
      for (const name of ["service", "source", "severity", "state"]) { const value = String(values.get(name) || ""); if (value) next.set(name, value); else next.delete(name); }
      for (const [name, value] of [["from", from], ["to", to]]) { if (value) next.set(name!, value); else next.delete(name!); }
      next.delete("incident"); setParams(next); setError("");
    } catch { setError(sec.filterInvalid); }
  }
  return <div className="stack">
    <form className="filters" onSubmit={submit} key={`${filter.service ?? ""}:${filter.source ?? ""}:${filter.severity ?? ""}:${filter.state ?? ""}:${filter.from ?? ""}:${filter.to ?? ""}`}>
      <Field label={sec.service} htmlFor="si-service"><input id="si-service" name="service" defaultValue={filter.service ?? ""} placeholder={sec.all} list="security-service-options" /><datalist id="security-service-options"><option value="__unassigned__">{sec.unassigned}</option></datalist></Field>
      <Field label={sec.source} htmlFor="si-source"><select id="si-source" name="source" defaultValue={filter.source ?? ""}><option value="">{sec.all}</option>{sources.map((value) => <option key={value}>{value}</option>)}</select></Field>
      <Field label={sec.severity} htmlFor="si-severity"><select id="si-severity" name="severity" defaultValue={filter.severity ?? ""}><option value="">{sec.all}</option>{severities.map((value) => <option key={value} value={value}>{sec.severityLabels[value]}</option>)}</select></Field>
      <Field label={sec.state} htmlFor="si-state"><select id="si-state" name="state" defaultValue={filter.state ?? ""}><option value="">{sec.all}</option>{states.map((value) => <option key={value} value={value}>{sec.stateLabels[value]}</option>)}</select></Field>
      <Field label={sec.from} htmlFor="si-from"><input id="si-from" name="from" type="datetime-local" defaultValue={localDate(filter.from ?? null)} /></Field>
      <Field label={sec.to} htmlFor="si-to"><input id="si-to" name="to" type="datetime-local" defaultValue={localDate(filter.to ?? null)} /></Field>
      <button type="submit" className="btn">{sec.applyFilters}</button>
    </form>
    {error && <p className="banner error" role="alert">{error}</p>}{q.isPending && <Spinner />}<ErrorBox error={q.error} onRetry={() => void q.refetch()} />
    {q.data && rows.length === 0 && <Empty>{sec.emptyIncidents}</Empty>}
    {rows.length > 0 && <div className="panel table-wrap"><table className="table"><thead><tr><th>{sec.summary}</th><th>{sec.machine}</th><th>{sec.service}</th><th>{sec.source}</th><th>{sec.severity}</th><th>{sec.state}</th><th>{sec.handling}</th><th>{sec.updated}</th><th>{sec.delivery}</th></tr></thead><tbody>{rows.map((item) => {
      const next = new URLSearchParams(params); next.set("incident", item.id);
      return <tr key={item.id}><td><Link to={{ search: next.toString() }}>{item.summary}</Link></td><td><code>{item.machineId}</code></td><td>{item.service ? <Link to={`/services/${encodeURIComponent(item.service)}`}>{item.service}</Link> : sec.unassigned}</td><td>{item.source}</td><td><Tag>{sec.severityLabels[item.severity] ?? item.severity}</Tag></td><td>{sec.stateLabels[item.state] ?? item.state}</td><td>{item.handling?.acknowledged ? sec.acknowledged : sec.notAcknowledged}{item.handling?.silenceUntil && <div className="meta">{sec.silenceUntil} {showTime(item.handling.silenceUntil)}</div>}</td><td>{showTime(item.updatedAt)}</td><td>{item.delivery}</td></tr>;
    })}</tbody></table></div>}
    {q.hasNextPage && <button className="btn" type="button" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>{sec.more}</button>}
    {id && <Dialog title={sec.incidents} wide onClose={() => { const next = new URLSearchParams(params); next.delete("incident"); setParams(next); }} footer={<button className="btn" onClick={() => { const next = new URLSearchParams(params); next.delete("incident"); setParams(next); }}>{sec.close}</button>}><IncidentDetails key={id} id={id} /></Dialog>}
  </div>;
}
export function IncidentDetails({ id }: { id: string }) {
  const q = useQuery(incidentQuery(id));
  return <Async q={q}>{(value) => <div className="stack"><h3>{value.incident.summary}</h3><dl className="kv"><dt>{sec.state}</dt><dd>{sec.stateLabels[value.incident.state]}</dd><dt>{sec.severity}</dt><dd>{sec.severityLabels[value.incident.severity]}</dd><dt>{sec.source}</dt><dd>{value.incident.source}</dd><dt>{sec.machine}</dt><dd><code>{value.incident.machineId}</code></dd><dt>{sec.service}</dt><dd>{value.incident.service || sec.unassigned}</dd><dt>{sec.opened}</dt><dd>{showTime(value.incident.openedAt)}</dd><dt>{sec.observed}</dt><dd>{showTime(value.incident.observedAt)}</dd><dt>{sec.updated}</dt><dd>{showTime(value.incident.updatedAt)}</dd><dt>{sec.occurrences}</dt><dd>{value.incident.occurrences}</dd><dt>{sec.delivery}</dt><dd>{value.incident.delivery}</dd><dt>{sec.note}</dt><dd>{value.incident.handling.note || "—"}</dd></dl>{value.incident.lastError && <p className="banner error">{value.incident.lastError}</p>}<HandlingForm incident={value.incident} /><h3>{sec.history}</h3>{value.history?.length ? <ol className="security-history">{value.history.map((item) => <li key={item.id}><strong>{item.action}</strong> · {item.actor} · {showTime(item.at)}<p>{item.reason}</p>{item.silenceUntil && <p>{sec.silenceUntil}：{showTime(item.silenceUntil)}</p>}</li>)}</ol> : <p className="meta">{sec.noHistory}</p>}</div>}</Async>;
}
export function HandlingForm({ incident }: { incident: Incident }) {
  const [reason, setReason] = useState(""); const [version, setVersion] = useState(incident.handling.version);
  const [action, setAction] = useState<HandlingInput["action"]>("acknowledge"); const [minutes, setMinutes] = useState(60);
  const qc = useQueryClient();
  const mutation = useMutation({ mutationFn: (input: HandlingInput) => securityAPI.handle(incident.id, version, input), onSuccess: (handling) => { setVersion(handling.version); setReason(""); void qc.invalidateQueries({ queryKey: sk.all }); } });
  const valid = validReason(reason) && (action !== "silence" || Number.isInteger(minutes) && minutes >= 1 && minutes <= 1440);
  return <form className="stack" onSubmit={(e) => { e.preventDefault(); if (!valid) return; mutation.mutate({ action, reason: reason.trim(), ...(action === "silence" ? { silenceUntil: new Date(Date.now() + minutes * 60_000).toISOString() } : {}) }); }}>
    <p className="meta">{sec.detectionHelp}</p>{incident.handling.version !== version && <p className="banner warn">{sec.conflict}<button type="button" className="btn small" onClick={() => { setVersion(incident.handling.version); mutation.reset(); }}>{sec.refresh}</button></p>}
    <Field label={sec.handling} htmlFor="handling-action"><select id="handling-action" value={action} onChange={(e) => setAction(e.target.value as HandlingInput["action"])}><option value="acknowledge">{sec.acknowledge}</option><option value="note">{sec.note}</option><option value="silence">{sec.silence}</option><option value="unsilence">{sec.unsilence}</option></select></Field>
    <Field label={sec.reason} htmlFor="handling-reason" hint={sec.reasonHelp}><input id="handling-reason" value={reason} maxLength={500} required onChange={(e) => setReason(e.target.value)} /></Field>
    {action === "silence" && <Field label={sec.silenceMinutes} htmlFor="handling-minutes"><input id="handling-minutes" type="number" min={1} max={1440} step={1} value={minutes} onChange={(e) => setMinutes(e.target.valueAsNumber)} /></Field>}
    <MutationError error={mutation.error} />{mutation.isSuccess && <p role="status">{sec.saved}</p>}<button className="btn primary" type="submit" disabled={!valid || mutation.isPending || incident.handling.version !== version}>{mutation.isPending ? "…" : sec.saveHandling}</button>
  </form>;
}
