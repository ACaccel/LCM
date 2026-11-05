import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useSearchParams } from "react-router";
import { isApiError } from "../../api/client";
import { resourcesQuery } from "../../api/queries";
import type { Category, DetectionState, HostAction, HostStatus, Severity, Source } from "../../api/security";
import { ErrorBox, Field } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { sec, secLabels } from "../../strings";

/** A monitor older than this is shown as not updating. */
const STALE_AFTER_MS = 300_000;

/** Filter options, in label order; the label records are typed by the API enums. */
export const sources = Object.keys(secLabels.source) as Source[];
export const severities = Object.keys(secLabels.severity) as Severity[];
export const states = Object.keys(secLabels.state) as DetectionState[];
export const categories = Object.keys(secLabels.category) as Category[];

/** The value if it is one of the options, else undefined: URL parameters are untrusted. */
export function pickOption<T extends string>(options: readonly T[], value: string | null): T | undefined {
  return options.find((option) => option === value);
}

export const showTime = (value: string | null | undefined) =>
  value && Date.parse(value) > 0 ? formatDateTime(value) : sec.unknown;

export function showValue(value: unknown): string {
  if (value === null || value === undefined) return sec.unknown;
  if (typeof value === "boolean") return value ? sec.enabled : sec.disabled;
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

export const number = (value: number | null | undefined) =>
  value === null || value === undefined || !Number.isFinite(value)
    ? sec.unknown
    : value.toLocaleString("zh-TW", { maximumFractionDigits: 2 });

export function auditEnabled(value: number | null | undefined): string {
  if (value === 0) return sec.disabled;
  if (value === 1) return sec.enabled;
  if (value === 2) return sec.auditLocked;
  return sec.unknown;
}

export function stale(value: string | null | undefined): boolean {
  return !value || !Number.isFinite(Date.parse(value)) || Date.now() - Date.parse(value) > STALE_AFTER_MS;
}

/**
 * Machine filter shared by the overview, incidents and traffic tabs. Machines
 * are picked by name; the URL keeps the immutable ID the API filters on.
 */
export function MachineSelect() {
  const [params, setParams] = useSearchParams();
  const machines = useQuery(resourcesQuery({ type: "machine" }));
  const value = params.get("machineId") || "";
  const items = machines.data ?? [];
  function pick(id: string) {
    const next = new URLSearchParams(params);
    if (id) next.set("machineId", id);
    else next.delete("machineId");
    next.delete("incident");
    setParams(next, { replace: true });
  }
  return (
    <Field label={sec.machine} htmlFor="security-machine">
      <select id="security-machine" value={value} onChange={(e) => pick(e.target.value)}>
        <option value="">{sec.all}</option>
        {value && !items.some((m) => m.id === value) && <option value={value}>{value}</option>}
        {items.map((m) => (
          <option key={m.id} value={m.id}>
            {m.name}
          </option>
        ))}
      </select>
      <ErrorBox error={machines.error} onRetry={() => void machines.refetch()} />
    </Field>
  );
}

/** Machine names by ID, for showing incidents by name; unknown IDs stay as they are. */
export function useMachineName(): (id: string) => string | undefined {
  const machines = useQuery(resourcesQuery({ type: "machine" }));
  const names = new Map((machines.data ?? []).map((m) => [m.id, m.name]));
  return (id) => names.get(id);
}

/** A failed write; a 409 means someone else changed the record first. */
export function MutationError({ error }: { error: unknown }) {
  return (
    <>
      {isApiError(error) && error.status === 409 && (
        <p className="banner warn" role="status">
          {sec.conflict}
        </p>
      )}
      <ErrorBox error={error} />
    </>
  );
}

export function Metric({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="security-metric">
      <span className="meta">{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

/** Renders a form of a host action, read-only with the reason when the helper cannot perform it. */
export function Capability({
  host,
  action,
  children,
}: {
  host: HostStatus;
  action: HostAction;
  children: (disabled: boolean) => ReactNode;
}) {
  const cap = host.capabilities?.find((item) => item.action === action);
  const reason = !host.available ? host.reason || sec.unavailable : !cap?.enabled ? cap?.reason || sec.noCapability : "";
  return (
    <div className="stack">
      <div>
        {reason && (
          <p className="banner warn">
            {sec.readOnly}：{reason}
          </p>
        )}
      </div>
      {children(Boolean(reason))}
    </div>
  );
}

// The operations guide owns the helper installation procedure.
const hostSetupGuide = "https://github.com/ACaccel/lcm/blob/main/docs/operations.md#security-console-host-controls";

export function HostNotConfigured() {
  return (
    <div className="stack" role="status">
      <p className="banner warn">{sec.hostNotConfigured}</p>
      <p className="meta">
        {sec.hostSetupHelp}{" "}
        <a href={hostSetupGuide} target="_blank" rel="noreferrer">
          {sec.hostSetupLink}
        </a>
      </p>
    </div>
  );
}

/** A report the helper returns as free-form JSON or text. */
export function UnknownReport({ value }: { value: unknown }) {
  const empty =
    value === null ||
    value === undefined ||
    value === "" ||
    (typeof value === "object" && Object.keys(value).length === 0);
  if (empty) return <p className="meta">{sec.unknown}</p>;
  return <pre className="block security-report">{typeof value === "string" ? value : JSON.stringify(value, null, 2)}</pre>;
}
