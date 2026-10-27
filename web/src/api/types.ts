import type { components } from "./schema";

type S = components["schemas"];

export type Actor = S["Actor"];
export type Issue = S["Issue"];
export type Labels = S["Labels"];
export type Host = S["Host"];
export type ServiceState = S["ServiceState"];
export type Health = S["Health"];
export type ServiceSummary = S["ServiceSummary"];
export type ServiceDetail = S["ServiceDetail"];
export type ServiceDoc = S["ServiceDoc"];
export type ServiceVersionInfo = S["ServiceVersionInfo"];
export type ServiceVersion = S["ServiceVersion"];
export type ManagedBy = NonNullable<S["ManagedBy"]>;
export type ActionName = S["ActionName"];
export type ActionPlan = S["ActionPlan"];
export type Action = S["Action"];
export type ActionResult = S["ActionResult"];
export type Deployment = S["Deployment"];
export type TimelineEntry = S["TimelineEntry"];
export type TimelineList = S["TimelineList"];
export type CheckStatus = S["CheckStatus"];
export type Checks = S["Checks"];
export type Series = S["Series"];
export type SeriesPoint = S["SeriesPoint"];
export type SummaryStats = S["SummaryStats"];
export type ServiceSummaryStats = S["ServiceSummaryStats"];
export type Alert = S["Alert"];
export type PolicySummary = S["PolicySummary"];
export type PolicyDetail = S["PolicyDetail"];
export type PolicyDocument = S["PolicyDocument"];
export type PolicyNode = S["PolicyNode"];
export type DocVersionInfo = S["DocVersionInfo"];
export type PolicyDoc = S["PolicyDoc"];
export type Trigger = S["Trigger"];
export type NodeType = S["NodeType"];
export type NodeProp = S["NodeProp"];
export type ValidationResult = S["ValidationResult"];
export type RunStatus = S["RunStatus"];
export type RunMode = S["RunMode"];
export type NodeStatus = S["NodeStatus"];
export type RunSummary = S["RunSummary"];
export type Run = S["Run"];
export type RunNode = S["RunNode"];
export type Token = S["Token"];
export type TokenCreated = S["TokenCreated"];
export type TokenScope = S["TokenScope"];
export type ConfigSource = S["ConfigSource"];
export type ConfigSourceRequest = S["ConfigSourceRequest"];
export type Ownership = S["Ownership"];
export type LockInfo = S["LockInfo"];
export type ApplyResult = S["ApplyResult"];
export type ModuleSummary = S["ModuleSummary"];
export type EventEnvelope = S["EventEnvelope"];
export type EventType = S["EventType"];
export type EngineEvent = S["EngineEvent"];
export type Signal = S["Signal"];

/** Shape shared by every cursor-paged list response. */
export interface Page<T> {
  items: T[];
  nextCursor: string | null;
}
