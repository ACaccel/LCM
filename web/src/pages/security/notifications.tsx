import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { isApiError } from "../../api/client";
import { notificationTestQuery, saveSettings, securitySettingsQuery, sendTestNotification, settingsHistoryQuery, sk } from "../../api/security";
import type { SecuritySettings, TestReceipt } from "../../api/security";
import { Async, ErrorBox, Field } from "../../components/ui";
import { sec, secLabels } from "../../strings";
import { clearAttempt, readAttempt, saveAttempt } from "./attempts";
import type { Attempt } from "./attempts";
import { MutationError, severities, showTime } from "./shared";

export function NotificationSettings() {
  const q = useQuery(securitySettingsQuery());
  return (
    <section className="panel stack">
      <h2>{sec.notifications}</h2>
      <p className="meta">{sec.notificationHelp}</p>
      <Async q={q}>
        {(value) => (
          <>
            <SettingsForm settings={value} />
            <NotificationTest settings={value} />
            <SettingsHistory />
          </>
        )}
      </Async>
    </section>
  );
}

export function SettingsForm({ settings }: { settings: SecuritySettings }) {
  const [version, setVersion] = useState(settings.version);
  const [enabled, setEnabled] = useState(settings.notificationsEnabled);
  const [cooldown, setCooldown] = useState(settings.cooldownSeconds);
  const [severity, setSeverity] = useState(settings.minimumSeverity);
  const [reason, setReason] = useState("");
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: () =>
      saveSettings(version, { notificationsEnabled: enabled, cooldownSeconds: cooldown, minimumSeverity: severity, reason: reason.trim() }),
    onSuccess: (value) => {
      setVersion(value.version);
      setReason("");
      void qc.invalidateQueries({ queryKey: sk.all });
    },
  });
  const allowed = settings.rootEnabled && settings.notificationConfigured;
  const outdated = settings.version !== version;
  function reload() {
    setVersion(settings.version);
    setEnabled(settings.notificationsEnabled);
    setCooldown(settings.cooldownSeconds);
    setSeverity(settings.minimumSeverity);
    setReason("");
    mutation.reset();
  }
  function submit(e: FormEvent) {
    e.preventDefault();
    mutation.mutate();
  }
  return (
    <form className="stack" onSubmit={submit}>
      {!settings.rootEnabled && <p className="banner warn">{sec.rootDisabled}</p>}
      {!settings.notificationConfigured && <p className="banner warn">{sec.notConfigured}</p>}
      {outdated && (
        <p className="banner warn">
          {sec.conflict}
          <button className="btn small" type="button" onClick={reload}>
            {sec.refresh}
          </button>
        </p>
      )}
      <label className="check">
        <input type="checkbox" checked={enabled} disabled={!allowed} onChange={(e) => setEnabled(e.target.checked)} />
        {sec.notificationsEnabled}
      </label>
      <div className="filters">
        <Field label={sec.cooldown} htmlFor="notify-cooldown">
          <input id="notify-cooldown" type="number" step={1} value={cooldown} onChange={(e) => setCooldown(e.target.valueAsNumber)} />
        </Field>
        <Field label={sec.minimumSeverity} htmlFor="notify-severity">
          <select id="notify-severity" value={severity} onChange={(e) => setSeverity(severities.find((v) => v === e.target.value) ?? severity)}>
            {severities.map((value) => (
              <option key={value} value={value}>
                {secLabels.severity[value]}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <Field label={sec.reason} htmlFor="notify-reason">
        <input id="notify-reason" required value={reason} onChange={(e) => setReason(e.target.value)} />
      </Field>
      <MutationError error={mutation.error} />
      {mutation.isSuccess && <p role="status">{sec.saved}</p>}
      <button className="btn primary" disabled={!allowed || !reason.trim() || Number.isNaN(cooldown) || outdated || mutation.isPending}>
        {sec.saveSettings}
      </button>
    </form>
  );
}

const TEST_KEY = "lcm:security:notification-test";
const TEST_TERMINAL = ["resolved", "succeeded", "failed", "completed", "expired", "cancelled"];
const isTestTerminal = (value: TestReceipt) => !value.pending && TEST_TERMINAL.includes(value.state);

export function NotificationTest({ settings }: { settings: SecuritySettings }) {
  const [attempt, setAttempt] = useState<Attempt | null>(() => readAttempt(TEST_KEY));
  const [reason, setReason] = useState("");
  const [confirmed, setConfirmed] = useState(false);
  const [storageError, setStorageError] = useState("");
  const query = useQuery({ ...notificationTestQuery(attempt?.requestId ?? ""), enabled: Boolean(attempt), retry: false });
  const qc = useQueryClient();
  const mutation = useMutation({
    mutationFn: (value: Attempt) => sendTestNotification({ requestId: value.requestId, reason: value.reason }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: sk.all });
    },
    onError: (error) => {
      // A refused request created nothing to query, so the form can be used again.
      if (isApiError(error) && (error.status === 400 || error.status === 422)) discard();
    },
  });
  const canTest = settings.rootEnabled && settings.notificationConfigured && settings.notificationsEnabled;
  const missing = isApiError(query.error) && query.error.status === 404;

  function submit() {
    const value = { requestId: crypto.randomUUID(), reason: reason.trim() };
    try {
      saveAttempt(TEST_KEY, value);
    } catch {
      setStorageError(sec.storageError);
      return;
    }
    setAttempt(value);
    mutation.mutate(value);
  }
  function discard() {
    try {
      clearAttempt(TEST_KEY);
      setAttempt(null);
      setConfirmed(false);
    } catch {
      setStorageError(sec.storageError);
    }
  }
  function close() {
    discard();
    setReason("");
    mutation.reset();
  }
  return (
    <section className="stack">
      <h3>{sec.test}</h3>
      <p className="meta">{sec.testHelp}</p>
      {attempt ? (
        <>
          <p>
            <code>{attempt.requestId}</code>
          </p>
          <p className="meta">{sec.retainAttempt}</p>
          {query.data && (
            <p role="status">
              {sec.testState}：{query.data.state} · {sec.pending}：{String(query.data.pending)} · {sec.testReceipt}：<code>{query.data.incidentId}</code>
            </p>
          )}
          {!query.data && <p className="banner warn">{sec.uncertain}</p>}
          <ErrorBox error={query.error} />
          <MutationError error={mutation.error} />
          <div className="row">
            <button type="button" className="btn" disabled={query.isFetching} onClick={() => void query.refetch()}>
              {sec.queryTest}
            </button>
            {missing && (
              <button type="button" className="btn" disabled={!canTest || mutation.isPending} onClick={() => mutation.mutate(attempt)}>
                {sec.test}
              </button>
            )}
            {query.data && isTestTerminal(query.data) && (
              <button type="button" className="btn" onClick={close}>
                {sec.close}
              </button>
            )}
          </div>
        </>
      ) : (
        <>
          <Field label={sec.reason} htmlFor="test-reason">
            <input id="test-reason" value={reason} onChange={(e) => setReason(e.target.value)} />
          </Field>
          <label className="check">
            <input type="checkbox" checked={confirmed} onChange={(e) => setConfirmed(e.target.checked)} />
            {sec.confirmedTest}
          </label>
          <MutationError error={mutation.error} />
          <button type="button" className="btn" disabled={!canTest || !confirmed || !reason.trim() || mutation.isPending} onClick={submit}>
            {sec.test}
          </button>
        </>
      )}
      {storageError && (
        <p className="banner error" role="alert">
          {storageError}
        </p>
      )}
    </section>
  );
}

export function SettingsHistory() {
  const q = useQuery(settingsHistoryQuery());
  return (
    <section className="stack">
      <h3>{sec.settingsHistory}</h3>
      <Async q={q}>
        {(data) =>
          data.items.length === 0 ? (
            <p className="meta">{sec.noHistory}</p>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{sec.version}</th>
                    <th>{sec.updated}</th>
                    <th>{sec.actor}</th>
                    <th>{sec.reason}</th>
                    <th>{sec.notifications}</th>
                    <th>{sec.cooldown}</th>
                    <th>{sec.minimumSeverity}</th>
                  </tr>
                </thead>
                <tbody>
                  {data.items.map((entry) => (
                    <tr key={entry.version}>
                      <td>{entry.version}</td>
                      <td>{showTime(entry.at)}</td>
                      <td>{entry.actor}</td>
                      <td>{entry.reason}</td>
                      <td>{entry.notificationsEnabled ? sec.enabled : sec.disabled}</td>
                      <td>{entry.cooldownSeconds}</td>
                      <td>{secLabels.severity[entry.minimumSeverity]}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
    </section>
  );
}
