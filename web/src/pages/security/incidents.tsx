import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { Link, useSearchParams } from "react-router";
import { incidentQuery, incidentsQuery, saveHandling, sk } from "../../api/security";
import type { HandlingInput, Incident, IncidentFilter } from "../../api/security";
import { Dialog } from "../../components/dialog";
import { Async, Empty, ErrorBox, Field, Pill, Spinner } from "../../components/ui";
import { toLocalInput } from "../../lib/format";
import { severityView } from "../../lib/status";
import { sec, secLabels } from "../../strings";
import { MachineSelect, MutationError, pickOption, severities, showTime, sources, states, useMachineName } from "./shared";

const HANDLING_ACTIONS = ["acknowledge", "note", "silence", "unsilence"] as const satisfies readonly HandlingInput["action"][];
const HANDLING_LABELS: Record<HandlingInput["action"], string> = {
  acknowledge: sec.acknowledge,
  note: sec.note,
  silence: sec.silence,
  unsilence: sec.unsilence,
};
const DEFAULT_SILENCE_MINUTES = 60;

function toISO(value: string): string | undefined {
  return value ? new Date(value).toISOString() : undefined;
}

function filterFromParams(params: URLSearchParams, machineId: string): IncidentFilter {
  return {
    machineId: machineId || undefined,
    service: params.get("service") || undefined,
    source: pickOption(sources, params.get("source")),
    severity: pickOption(severities, params.get("severity")),
    state: pickOption(states, params.get("state")),
    from: params.get("from") || undefined,
    to: params.get("to") || undefined,
  };
}

export function SecurityIncidents({ machineId }: { machineId: string }) {
  const [params, setParams] = useSearchParams();
  const filter = filterFromParams(params, machineId);
  const q = useInfiniteQuery(incidentsQuery(filter));
  const machineName = useMachineName();
  // The time inputs keep what was typed; only a valid, ordered range reaches the URL.
  const [range, setRange] = useState(() => ({ from: toLocalInput(filter.from), to: toLocalInput(filter.to) }));
  const [rangeError, setRangeError] = useState(false);
  const id = params.get("incident");
  const rows = q.data?.pages.flatMap((page) => page.items) ?? [];

  function apply(values: Record<string, string | undefined>) {
    const next = new URLSearchParams(params);
    for (const [name, value] of Object.entries(values)) {
      if (value) next.set(name, value);
      else next.delete(name);
    }
    next.delete("incident");
    setParams(next, { replace: true });
  }
  function setTime(name: "from" | "to", value: string) {
    const typed = { ...range, [name]: value };
    setRange(typed);
    let from: string | undefined;
    let to: string | undefined;
    try {
      from = toISO(typed.from);
      to = toISO(typed.to);
    } catch {
      setRangeError(true);
      return;
    }
    if (from && to && Date.parse(from) >= Date.parse(to)) {
      setRangeError(true);
      return;
    }
    setRangeError(false);
    apply({ from, to });
  }
  function closeDetails() {
    const next = new URLSearchParams(params);
    next.delete("incident");
    setParams(next);
  }

  return (
    <div className="stack">
      <div className="filters">
        <MachineSelect />
        <Field label={sec.service} htmlFor="si-service">
          <input
            id="si-service"
            type="search"
            value={filter.service ?? ""}
            placeholder={sec.all}
            list="security-service-options"
            onChange={(e) => apply({ service: e.target.value })}
          />
          <datalist id="security-service-options">
            <option value="__unassigned__">{sec.unassigned}</option>
          </datalist>
        </Field>
        <Field label={sec.source} htmlFor="si-source">
          <select id="si-source" value={filter.source ?? ""} onChange={(e) => apply({ source: e.target.value })}>
            <option value="">{sec.all}</option>
            {sources.map((value) => (
              <option key={value} value={value}>
                {secLabels.source[value]}
              </option>
            ))}
          </select>
        </Field>
        <Field label={sec.severity} htmlFor="si-severity">
          <select id="si-severity" value={filter.severity ?? ""} onChange={(e) => apply({ severity: e.target.value })}>
            <option value="">{sec.all}</option>
            {severities.map((value) => (
              <option key={value} value={value}>
                {secLabels.severity[value]}
              </option>
            ))}
          </select>
        </Field>
        <Field label={sec.state} htmlFor="si-state">
          <select id="si-state" value={filter.state ?? ""} onChange={(e) => apply({ state: e.target.value })}>
            <option value="">{sec.all}</option>
            {states.map((value) => (
              <option key={value} value={value}>
                {secLabels.state[value]}
              </option>
            ))}
          </select>
        </Field>
        <Field label={sec.from} htmlFor="si-from">
          <input id="si-from" type="datetime-local" value={range.from} onChange={(e) => setTime("from", e.target.value)} />
        </Field>
        <Field label={sec.to} htmlFor="si-to">
          <input id="si-to" type="datetime-local" value={range.to} onChange={(e) => setTime("to", e.target.value)} />
        </Field>
      </div>
      {rangeError && (
        <p className="banner error" role="alert">
          {sec.filterInvalid}
        </p>
      )}
      {q.isPending && <Spinner />}
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.data && rows.length === 0 && <Empty>{sec.emptyIncidents}</Empty>}
      {rows.length > 0 && (
        <div className="panel table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>{sec.summary}</th>
                <th>{sec.machine}</th>
                <th>{sec.service}</th>
                <th>{sec.source}</th>
                <th>{sec.severity}</th>
                <th>{sec.state}</th>
                <th>{sec.handling}</th>
                <th>{sec.updated}</th>
                <th>{sec.delivery}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((item) => (
                <IncidentRow key={item.id} item={item} params={params} machineName={machineName(item.machineId)} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      {q.hasNextPage && (
        <button className="btn" type="button" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {sec.more}
        </button>
      )}
      {id && (
        <Dialog
          title={sec.incidents}
          wide
          onClose={closeDetails}
          footer={
            <button type="button" className="btn" onClick={closeDetails}>
              {sec.close}
            </button>
          }
        >
          <IncidentDetails key={id} id={id} />
        </Dialog>
      )}
    </div>
  );
}

function IncidentRow({ item, params, machineName }: { item: Incident; params: URLSearchParams; machineName: string | undefined }) {
  const next = new URLSearchParams(params);
  next.set("incident", item.id);
  return (
    <tr>
      <td>
        <Link to={{ search: next.toString() }}>{item.summary}</Link>
      </td>
      <td>{machineName ? <span title={item.machineId}>{machineName}</span> : <code>{item.machineId}</code>}</td>
      <td>{item.service ? <Link to={`/resources/${encodeURIComponent(item.service)}`}>{item.service}</Link> : sec.unassigned}</td>
      <td>{secLabels.source[item.source]}</td>
      <td>
        <Pill view={severityView(item.severity)} />
      </td>
      <td>{secLabels.state[item.state]}</td>
      <td>
        {item.handling.acknowledged ? sec.acknowledged : sec.notAcknowledged}
        {item.handling.silenceUntil && (
          <div className="meta">
            {sec.silenceUntil} {showTime(item.handling.silenceUntil)}
          </div>
        )}
      </td>
      <td>{showTime(item.updatedAt)}</td>
      <td>{secLabels.delivery[item.delivery]}</td>
    </tr>
  );
}

export function IncidentDetails({ id }: { id: string }) {
  const q = useQuery(incidentQuery(id));
  return (
    <Async q={q}>
      {({ incident, history }) => (
        <div className="stack">
          <h3>{incident.summary}</h3>
          <dl className="kv">
            <dt>{sec.state}</dt>
            <dd>{secLabels.state[incident.state]}</dd>
            <dt>{sec.severity}</dt>
            <dd>
              <Pill view={severityView(incident.severity)} />
            </dd>
            <dt>{sec.source}</dt>
            <dd>{secLabels.source[incident.source]}</dd>
            <dt>{sec.machine}</dt>
            <dd>
              <code>{incident.machineId}</code>
            </dd>
            <dt>{sec.service}</dt>
            <dd>{incident.service || sec.unassigned}</dd>
            <dt>{sec.opened}</dt>
            <dd>{showTime(incident.openedAt)}</dd>
            <dt>{sec.observed}</dt>
            <dd>{showTime(incident.observedAt)}</dd>
            <dt>{sec.updated}</dt>
            <dd>{showTime(incident.updatedAt)}</dd>
            <dt>{sec.occurrences}</dt>
            <dd>{incident.occurrences}</dd>
            <dt>{sec.delivery}</dt>
            <dd>{secLabels.delivery[incident.delivery]}</dd>
            <dt>{sec.note}</dt>
            <dd>{incident.handling.note || "—"}</dd>
          </dl>
          {incident.lastError && <p className="banner error">{incident.lastError}</p>}
          <HandlingForm incident={incident} />
          <h3>{sec.history}</h3>
          {history?.length ? (
            <ol className="security-history">
              {history.map((item) => (
                <li key={item.id}>
                  <strong>{item.action}</strong> · {item.actor} · {showTime(item.at)}
                  <p>{item.reason}</p>
                  {item.silenceUntil && (
                    <p>
                      {sec.silenceUntil}：{showTime(item.silenceUntil)}
                    </p>
                  )}
                </li>
              ))}
            </ol>
          ) : (
            <p className="meta">{sec.noHistory}</p>
          )}
        </div>
      )}
    </Async>
  );
}

export function HandlingForm({ incident }: { incident: Incident }) {
  const [reason, setReason] = useState("");
  const [version, setVersion] = useState(incident.handling.version);
  const [action, setAction] = useState<HandlingInput["action"]>("acknowledge");
  const [minutes, setMinutes] = useState(DEFAULT_SILENCE_MINUTES);
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: (input: HandlingInput) => saveHandling(incident.id, version, input),
    onSuccess: (handling) => {
      setVersion(handling.version);
      setReason("");
      void qc.invalidateQueries({ queryKey: sk.all });
    },
  });
  const outdated = incident.handling.version !== version;
  const valid = reason.trim() !== "" && (action !== "silence" || Number.isFinite(minutes));
  function submit(e: FormEvent) {
    e.preventDefault();
    if (!valid) return;
    mutation.mutate({
      action,
      reason: reason.trim(),
      ...(action === "silence" ? { silenceUntil: new Date(Date.now() + minutes * 60_000).toISOString() } : {}),
    });
  }
  return (
    <form className="stack" onSubmit={submit}>
      <p className="meta">{sec.detectionHelp}</p>
      {outdated && (
        <p className="banner warn">
          {sec.conflict}
          <button
            type="button"
            className="btn small"
            onClick={() => {
              setVersion(incident.handling.version);
              mutation.reset();
            }}
          >
            {sec.refresh}
          </button>
        </p>
      )}
      <Field label={sec.handling} htmlFor="handling-action">
        <select
          id="handling-action"
          value={action}
          onChange={(e) => setAction(HANDLING_ACTIONS.find((a) => a === e.target.value) ?? "acknowledge")}
        >
          {HANDLING_ACTIONS.map((value) => (
            <option key={value} value={value}>
              {HANDLING_LABELS[value]}
            </option>
          ))}
        </select>
      </Field>
      <Field label={sec.reason} htmlFor="handling-reason" hint={sec.reasonHelp}>
        <input id="handling-reason" value={reason} required onChange={(e) => setReason(e.target.value)} />
      </Field>
      {action === "silence" && (
        <Field label={sec.silenceMinutes} htmlFor="handling-minutes">
          <input id="handling-minutes" type="number" step={1} value={minutes} onChange={(e) => setMinutes(e.target.valueAsNumber)} />
        </Field>
      )}
      <MutationError error={mutation.error} />
      {mutation.isSuccess && <p role="status">{sec.saved}</p>}
      <button className="btn primary" type="submit" disabled={!valid || mutation.isPending || outdated}>
        {mutation.isPending ? "…" : sec.saveHandling}
      </button>
    </form>
  );
}
