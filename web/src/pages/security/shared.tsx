import type { ReactNode } from "react";
import { isApiError } from "../../api/client";
import type { HostAction, HostStatus } from "../../api/security";
import { ErrorBox } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { sec } from "./strings";

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
