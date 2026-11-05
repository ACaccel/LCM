import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { hostStatusQuery } from "../../api/security";
import type { HostAction, HostStatus, NginxLimit, Profile } from "../../api/security";
import { Async, Field } from "../../components/ui";
import { sec } from "../../strings";
import { HostActions, Reason } from "./host-actions";
import type { BeginHostAction } from "./host-actions";
import { HostSummary } from "./overview";
import { Capability, UnknownReport, auditEnabled, number, showTime, showValue } from "./shared";

/** The host control tab: what the helper reports, then the rules, scans and schedule it can change. */
export function SecurityHost() {
  const q = useQuery(hostStatusQuery());
  return (
    <div className="stack">
      <HostSummary />
      <Async q={q}>{(host) => (host.configured ? <HostForms host={host} /> : null)}</Async>
    </div>
  );
}

function HostForms({ host }: { host: HostStatus }) {
  const [draft, setDraft] = useState(host);
  const changed = draft.effectiveDigest !== host.effectiveDigest;
  return (
    <div className="stack">
      <p className="meta">
        {sec.hostScope}：<code>{host.machineId || sec.unknown}</code> · {sec.digest}：<code>{host.effectiveDigest || sec.unknown}</code>
      </p>
      {!host.available && (
        <p className="banner warn">
          {sec.readOnly}：{host.reason || sec.unavailable}
        </p>
      )}
      {changed && (
        <p className="banner warn">
          {sec.conflict}
          <button type="button" className="btn small" onClick={() => setDraft(host)}>
            {sec.refresh}
          </button>
        </p>
      )}
      <HostActions host={host}>
        {(begin, busy) => (
          <div className="stack" key={draft.effectiveDigest}>
            <fieldset className="security-fields" disabled={busy || changed}>
              <NginxForm host={draft} begin={begin} />
              <section className="panel stack">
                <h2>{sec.audit}</h2>
                <dl className="kv">
                  <dt>{sec.status}</dt>
                  <dd>{auditEnabled(host.audit?.enabled)}</dd>
                  <dt>{sec.lost}</dt>
                  <dd>{number(host.audit?.lost)}</dd>
                  <dt>{sec.backlog}</dt>
                  <dd>{number(host.audit?.backlog)}</dd>
                  <dt>{sec.rulesLoaded}</dt>
                  <dd>{showValue(host.audit?.rulesLoaded)}</dd>
                </dl>
                <ProfileForm host={draft} type="audit" begin={begin} />
              </section>
              <section className="panel stack">
                <h2>{sec.aideProfiles}</h2>
                <ProfileForm host={draft} type="aide" begin={begin} />
              </section>
              <section className="panel stack">
                <h2>{sec.aide}</h2>
                <dl className="kv">
                  <dt>{sec.status}</dt>
                  <dd>{host.aide?.result || sec.unknown}</dd>
                  <dt>{sec.lastRun}</dt>
                  <dd>{showTime(host.aide?.lastRunAt)}</dd>
                  <dt>{sec.nextRun}</dt>
                  <dd>{showTime(host.aide?.nextRunAt)}</dd>
                  <dt>{sec.baseline}</dt>
                  <dd>{showTime(host.aide?.baselineCreatedAt)}</dd>
                  <dt>{sec.baselineDigest}</dt>
                  <dd>
                    <code>{host.aide?.baselineDigest || sec.unknown}</code>
                  </dd>
                </dl>
                <h3>{sec.targets}</h3>
                <UnknownReport value={host.aide?.targets} />
                <h3>{sec.report}</h3>
                <UnknownReport value={host.aide?.reportSummary} />
              </section>
              <ScheduleForm host={draft} begin={begin} />
              <SimpleAction host={draft} action="aide.check" title={sec.scan} begin={begin} />
              <SimpleAction host={draft} action="baseline.request" title={sec.baselineRequest} help={sec.baselineHelp} begin={begin} />
            </fieldset>
          </div>
        )}
      </HostActions>
    </div>
  );
}

/** The number a number input holds, or "" while it is empty. */
const numberInput = (e: { target: HTMLInputElement }): number | "" => (e.target.value === "" ? "" : e.target.valueAsNumber);

export function ScheduleForm({ host, begin }: { host: HostStatus; begin: BeginHostAction }) {
  const [hour, setHour] = useState<number | "">(host.aide?.schedule?.hour ?? "");
  const [minute, setMinute] = useState<number | "">(host.aide?.schedule?.minute ?? "");
  const [reason, setReason] = useState("");
  return (
    <section className="panel stack">
      <h2>{sec.schedule}</h2>
      <p className="meta">{sec.scheduleHelp}</p>
      <Capability host={host} action="aide.schedule">
        {(disabled) => (
          <form
            className="stack"
            onSubmit={(e) => {
              e.preventDefault();
              if (typeof hour === "number" && typeof minute === "number") begin("aide.schedule", { hour, minute }, reason);
            }}
          >
            <div className="filters">
              <Field label={sec.hour} htmlFor="schedule-hour">
                <input id="schedule-hour" type="number" value={hour} disabled={disabled} onChange={(e) => setHour(numberInput(e))} />
              </Field>
              <Field label={sec.minute} htmlFor="schedule-minute">
                <input id="schedule-minute" type="number" value={minute} disabled={disabled} onChange={(e) => setMinute(numberInput(e))} />
              </Field>
            </div>
            <Reason id="schedule-reason" value={reason} onChange={setReason} />
            <button className="btn" disabled={disabled || !Number.isFinite(hour) || !Number.isFinite(minute) || !reason.trim()}>
              {sec.scheduleAction}
            </button>
          </form>
        )}
      </Capability>
    </section>
  );
}

export function ProfileForm({ host, type, begin }: { host: HostStatus; type: "audit" | "aide"; begin: BeginHostAction }) {
  const profiles = type === "audit" ? host.audit?.profiles : host.aide?.profiles;
  const action: HostAction = `${type}.profile`;
  const [drafts, setDrafts] = useState<Record<string, boolean>>({});
  const [reason, setReason] = useState("");
  const current = (profile: Profile) => (profile.required ? profile.enabled : (drafts[profile.name] ?? profile.enabled));
  function submit(e: FormEvent) {
    e.preventDefault();
    if (profiles?.length) begin(action, { profiles: profiles.map((profile) => ({ name: profile.name, enabled: current(profile) })) }, reason);
  }
  return (
    <Capability host={host} action={action}>
      {(disabled) => (
        <form className="stack" onSubmit={submit}>
          <p className="meta">{sec.profileHelp}</p>
          {profiles?.length ? (
            profiles.map((profile) => (
              <label className="check" key={profile.name}>
                <input
                  type="checkbox"
                  checked={current(profile)}
                  disabled={disabled || profile.required}
                  onChange={(e) => setDrafts({ ...drafts, [profile.name]: e.target.checked })}
                />
                <code>{profile.name}</code>
                {profile.required && <span className="meta">{sec.required}</span>}
              </label>
            ))
          ) : (
            <p className="meta">{sec.noProfiles}</p>
          )}
          <Reason id={`${type}-profile-reason`} value={reason} onChange={setReason} />
          <button className="btn" disabled={disabled || !profiles?.some((profile) => !profile.required) || !reason.trim()}>
            {sec.preview}
          </button>
        </form>
      )}
    </Capability>
  );
}

export function NginxForm({ host, begin }: { host: HostStatus; begin: BeginHostAction }) {
  const [limits, setLimits] = useState<NginxLimit[]>(() => host.nginx?.limits?.map((limit) => ({ ...limit })) ?? []);
  const [reason, setReason] = useState("");
  // Ranges and the rate format are checked by the preview; here only complete input is required.
  const complete =
    limits.length > 0 &&
    limits.every((limit) => limit.rate.trim() !== "" && Number.isFinite(limit.burst) && Number.isFinite(limit.connections));
  function update(index: number, patch: Partial<NginxLimit>) {
    setLimits((previous) => previous.map((limit, i) => (i === index ? { ...limit, ...patch } : limit)));
  }
  function submit(e: FormEvent) {
    e.preventDefault();
    if (complete) begin("nginx.configure", { limits }, reason);
  }
  return (
    <section className="panel stack">
      <h2>{sec.nginx}</h2>
      <p className="meta">{sec.nginxHelp}</p>
      <dl className="kv">
        <dt>{sec.nginxMode}</dt>
        <dd>{host.nginx?.mode || sec.unknown}</dd>
        <dt>dry-run</dt>
        <dd>{showValue(host.nginx?.dryRunOnly)}</dd>
        <dt>{sec.r11}</dt>
        <dd>{showValue(host.nginx?.r11Approved)}</dd>
      </dl>
      <Capability host={host} action="nginx.configure">
        {(disabled) => (
          <form className="stack" onSubmit={submit}>
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{sec.category}</th>
                    <th>{sec.rate}</th>
                    <th>{sec.burst}</th>
                    <th>{sec.connections}</th>
                  </tr>
                </thead>
                <tbody>
                  {limits.map((limit, index) => (
                    <tr key={limit.category}>
                      <td>
                        <code>{limit.category}</code>
                      </td>
                      <td>
                        <input
                          aria-label={`${limit.category} ${sec.rate}`}
                          value={limit.rate}
                          disabled={disabled}
                          onChange={(e) => update(index, { rate: e.target.value })}
                        />
                      </td>
                      <td>
                        <input
                          type="number"
                          aria-label={`${limit.category} ${sec.burst}`}
                          value={limit.burst}
                          disabled={disabled}
                          onChange={(e) => update(index, { burst: e.target.valueAsNumber })}
                        />
                      </td>
                      <td>
                        <input
                          type="number"
                          aria-label={`${limit.category} ${sec.connections}`}
                          value={limit.connections}
                          disabled={disabled}
                          onChange={(e) => update(index, { connections: e.target.valueAsNumber })}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {!limits.length && <p className="meta">{sec.unknown}</p>}
            <Reason id="nginx-reason" value={reason} onChange={setReason} />
            <button className="btn" disabled={disabled || !complete || !reason.trim()}>
              {sec.preview}
            </button>
          </form>
        )}
      </Capability>
    </section>
  );
}

function SimpleAction({
  host,
  action,
  title,
  help,
  begin,
}: {
  host: HostStatus;
  action: "aide.check" | "baseline.request";
  title: string;
  help?: string;
  begin: BeginHostAction;
}) {
  const [reason, setReason] = useState("");
  return (
    <section className="panel stack">
      <h2>{title}</h2>
      {help && <p className="meta">{help}</p>}
      <Capability host={host} action={action}>
        {(disabled) => (
          <form
            className="stack"
            onSubmit={(e) => {
              e.preventDefault();
              begin(action, {}, reason);
            }}
          >
            <Reason id={`${action}-reason`} value={reason} onChange={setReason} />
            <button className="btn" disabled={disabled || !reason.trim()}>
              {sec.preview}
            </button>
          </form>
        )}
      </Capability>
    </section>
  );
}
