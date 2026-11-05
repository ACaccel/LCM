import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { useSearchParams } from "react-router";
import { isApiError } from "../../api/client";
import { resourcesQuery } from "../../api/queries";
import type { HostAction, HostStatus } from "../../api/security";
import { ErrorBox, Field } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { sec } from "./strings";

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

export const sources = ["machine", "service", "nginx", "auditd", "aide", "collector"] as const;
export const severities = ["info", "warning", "critical"] as const;
export const states = ["firing", "resolved", "retired"] as const;
export const categories = ["auth", "api", "upload", "socket", "static", "other"] as const;
export const showTime = (value: string | null | undefined) => value && Date.parse(value) > 0 ? formatDateTime(value) : sec.unknown;
export const showValue = (value: unknown): string => value === null || value === undefined ? sec.unknown : typeof value === "boolean" ? value ? sec.enabled : sec.disabled : typeof value === "object" ? JSON.stringify(value) : String(value);
export const number = (value: number | null | undefined) => value === null || value === undefined || !Number.isFinite(value) ? sec.unknown : value.toLocaleString("zh-TW", { maximumFractionDigits: 2 });
export function validReason(value: string): boolean { return value.trim().length > 0 && value.length <= 500 && !Array.from(value).some((character) => { const code = character.charCodeAt(0); return code < 32 || code === 127; }); }
export function MutationError({ error }: { error: unknown }) {
  return <>{isApiError(error) && error.status === 409 && <p className="banner warn" role="status">{sec.conflict}</p>}<ErrorBox error={error} /></>;
}
export const auditEnabled = (value: number | null | undefined) => value === 0 ? sec.disabled : value === 1 ? sec.enabled : value === 2 ? sec.auditLocked : sec.unknown;
export function Metric({ label, value }: { label: string; value: ReactNode }) {
  return <div className="security-metric"><span className="meta">{label}</span><strong>{value}</strong></div>;
}
export function Capability({ host, action, children }: { host: HostStatus; action: HostAction; children: (disabled: boolean) => ReactNode }) {
  const cap = host.capabilities?.find((item) => item.action === action);
  const reason = !host.available ? host.reason || sec.unavailable : !cap?.enabled ? cap?.reason || sec.noCapability : "";
  return <div className="stack"><div>{reason && <p className="banner warn">{sec.readOnly}：{reason}</p>}</div>{children(Boolean(reason))}</div>;
}
// The operations guide owns the helper installation procedure.
const hostSetupGuide = "https://github.com/ACaccel/lcm/blob/main/docs/operations.md#security-console-host-controls";
export function HostNotConfigured() {
  return <div className="stack" role="status"><p className="banner warn">{sec.hostNotConfigured}</p><p className="meta">{sec.hostSetupHelp} <a href={hostSetupGuide} target="_blank" rel="noreferrer">{sec.hostSetupLink}</a></p></div>;
}
export function UnknownReport({ value }: { value: unknown }) {
  if (value === null || value === undefined || value === "" || (typeof value === "object" && Object.keys(value).length === 0)) return <p className="meta">{sec.unknown}</p>;
  return <pre className="block security-report">{typeof value === "string" ? value : JSON.stringify(value, null, 2)}</pre>;
}
export function stale(value: string | null | undefined): boolean { return !value || !Number.isFinite(Date.parse(value)) || Date.now() - Date.parse(value) > 300_000; }
