import type { Health, ManagedBy, ServiceState } from "../api/types";
import { healthView, stateView } from "../lib/status";
import { s } from "../strings";
import { Pill, Tag } from "./ui";

export function HealthPill({ health }: { health: Health }) {
  return <Pill view={healthView(health)} />;
}

export function StatePill({ state }: { state: ServiceState }) {
  return <Pill view={stateView(state)} />;
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
