import type { Health, ManagedBy, ResourcePhase, ResourceTypeName } from "../api/types";
import { healthView, phaseView, stateView } from "../lib/status";
import { s } from "../strings";
import { Pill, Tag } from "./ui";

export function HealthPill({ health }: { health: Health }) {
  return <Pill view={healthView(health)} />;
}

export function StatePill({ state }: { state: string }) {
  return <Pill view={stateView(state)} />;
}

export function PhasePill({ phase }: { phase: ResourcePhase }) {
  return <Pill view={phaseView(phase)} />;
}

export function TypeTag({ type, adapter }: { type: ResourceTypeName; adapter?: string }) {
  return <Tag title={s.resources.columns.type}>{adapter ? `${s.resourceType[type]} · ${adapter}` : s.resourceType[type]}</Tag>;
}

/** Where a resource is managed from, and whether it left its git source. */
export function ManagedBadges({ managedBy }: { managedBy: ManagedBy | null | undefined }) {
  if (!managedBy) return <Tag>{s.ownership.gui}</Tag>;
  return (
    <>
      <Tag title={managedBy.path}>{s.ownership.managed(managedBy.source)}</Tag>
      {managedBy.conflict ? (
        <Pill view={{ tone: "bad", icon: "✕", label: s.ownership.conflict }} />
      ) : managedBy.drifted ? (
        <Pill view={{ tone: "warn", icon: "▲", label: s.ownership.drifted }} />
      ) : null}
    </>
  );
}
