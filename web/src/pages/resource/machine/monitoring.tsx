import { useQuery } from "@tanstack/react-query";
import { Fragment, useState } from "react";
import { historyQuery } from "../../../api/observations";
import type { MachineSnapshot, ResourceDetail } from "../../../api/types";
import { RangeSwitch, UsageBar } from "../../../components/chart";
import type { RangeKey } from "../../../components/chart";
import { ErrorBox, Spinner } from "../../../components/ui";
import { useNow } from "../../../hooks";
import { formatDateTime, formatDuration } from "../../../lib/format";
import { coverageView, machineFresh, thresholdOf, usedPercent } from "../../../lib/machine";
import type { Observed } from "../../../lib/machine";
import { coverageSource, s } from "../../../strings";
import { CoverageSummary } from "../coverage";
import type { CoverageRow } from "../coverage";
import { MachineTrend } from "./trend";

export interface UsageLimits {
  cpu: number;
  memory: number;
  disk: number;
}

/** Limits of the machine document, which the usage bars mark. */
export function limitsOf(resource: Observed): UsageLimits {
  return { cpu: thresholdOf(resource, "cpuPercent"), memory: thresholdOf(resource, "memoryPercent"), disk: thresholdOf(resource, "diskPercent") };
}

const availableIn = (snapshot: MachineSnapshot, source: string) => snapshot.coverage[source]?.state === "available";

/** CPU, memory and every watched filesystem as bars marked with the configured limit. */
export function UsageBars({ snapshot, limits }: { snapshot: MachineSnapshot; limits: UsageLimits }) {
  return (
    <dl className="kv">
      <dt>{s.machine.cpu}</dt>
      <dd>{availableIn(snapshot, "cpu") ? <UsageBar value={snapshot.cpuPercent} label={s.machine.cpu} threshold={limits.cpu} /> : s.machine.unknown}</dd>
      <dt>{s.machine.memory}</dt>
      <dd>
        {availableIn(snapshot, "memory") ? <UsageBar value={usedPercent(snapshot.memoryTotal, snapshot.memoryAvailable)} label={s.machine.memory} threshold={limits.memory} /> : s.machine.unknown}
      </dd>
      {(snapshot.filesystems ?? []).map((fs) => (
        <Fragment key={fs.path}>
          <dt>
            {s.machine.disk} <code>{fs.path}</code>
          </dt>
          <dd>
            {availableIn(snapshot, "filesystems") ? <UsageBar value={usedPercent(fs.total, fs.available)} label={`${s.machine.disk} ${fs.path}`} threshold={limits.disk} /> : s.machine.unknown}
          </dd>
        </Fragment>
      ))}
    </dl>
  );
}

/** What the usage bars leave out: identity of the boot, swap, inodes, units, interfaces and pressure. */
export function SnapshotDetails({ snapshot, limits }: { snapshot: MachineSnapshot; limits: UsageLimits }) {
  const listed = (entries: Record<string, string | number> | undefined) =>
    !Object.keys(entries ?? {}).length
      ? s.machine.unknown
      : Object.entries(entries ?? {}).map(([name, value]) => (
          <div key={name}>
            <code>{name}</code>: {String(value)}
          </div>
        ));
  return (
    <div className="stack">
      <dl className="kv">
        <dt>{s.machine.observed}</dt>
        <dd>{formatDateTime(snapshot.observedAt)}</dd>
        <dt>{s.machine.os}</dt>
        <dd>
          {snapshot.os || s.machine.unknown} / {snapshot.init || s.machine.unknown}
        </dd>
        <dt>{s.machine.bootId}</dt>
        <dd>
          <code>{snapshot.bootId || s.machine.unknown}</code>
        </dd>
        <dt>{s.machine.uptime}</dt>
        <dd>{formatDuration(snapshot.uptimeSeconds * 1000)}</dd>
        <dt>{s.machine.load}</dt>
        <dd>{availableIn(snapshot, "cpu") ? `${snapshot.load?.join(" / ") ?? s.machine.unknown} / ${snapshot.cpuCount}` : s.machine.unknown}</dd>
        <dt>{s.machine.swap}</dt>
        <dd>{availableIn(snapshot, "memory") ? <UsageBar value={usedPercent(snapshot.swapTotal, snapshot.swapFree)} label={s.machine.swap} /> : s.machine.unknown}</dd>
      </dl>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>{s.machine.mountpoint}</th>
              <th>{s.machine.inodes}</th>
            </tr>
          </thead>
          <tbody>
            {(snapshot.filesystems ?? []).map((fs) => (
              <tr key={fs.path}>
                <td>
                  <code>{fs.path}</code>
                </td>
                <td>{availableIn(snapshot, "filesystems") ? <UsageBar value={usedPercent(fs.inodes, fs.freeInodes)} label={`${s.machine.inodes} ${fs.path}`} threshold={limits.disk} /> : s.machine.unknown}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <dl className="kv">
        <dt>{s.machine.units}</dt>
        <dd>{listed(snapshot.units)}</dd>
        <dt>{s.machine.interfaces}</dt>
        <dd>{listed(snapshot.interfaces)}</dd>
        <dt>{s.machine.pressure}</dt>
        <dd>{listed(snapshot.pressure)}</dd>
      </dl>
    </div>
  );
}

/** Whether each data source of the machine answered; optional sources are marked. */
export function machineCoverageRows(resource: Observed): CoverageRow[] {
  const status = resource.machine?.status;
  const coverage = {
    probe: undefined,
    identity: undefined,
    cpu: undefined,
    memory: undefined,
    filesystems: undefined,
    units: undefined,
    interfaces: undefined,
    psi: undefined,
    ...status?.snapshot?.coverage,
    journal: status?.journal,
  };
  return Object.entries(coverage).map(([key, value]) => ({
    key,
    label: coverageSource(key),
    optional: ["interfaces", "psi", "journal"].includes(key),
    view: coverageView(value),
    available: value?.state === "available",
    reason: value?.reason ? (s.observation.reasons[value.reason] ?? value.reason) : "",
  }));
}

/** CPU and memory over a range the reader picks; each range is a request for that range. */
export function MachineTrendPanel({ resource, selector }: { resource: Observed; selector: string }) {
  const [range, setRange] = useState<RangeKey>("24h");
  const q = useQuery(historyQuery(selector, range));
  const limits = limitsOf(resource);
  const snapshots = (q.data ?? []).flatMap((x) => (x.type === "machine" ? [x.data] : []));
  return (
    <section className="panel stack">
      <div className="chart-title">
        <h2>{s.machine.trend}</h2>
        <RangeSwitch value={range} onChange={setRange} />
      </div>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {q.data && snapshots.length === 0 && <p>{s.machine.trendEmpty}</p>}
      <MachineTrend range={range} thresholds={{ cpu: limits.cpu, memory: limits.memory }} snapshots={snapshots} />
    </section>
  );
}

/**
 * The machine's overview: current usage, the trend over a chosen range and
 * how many data sources answered. Identity, state and health are the page
 * header's.
 */
export function MachineOverview({ resource, selector }: { resource: ResourceDetail; selector: string }) {
  const now = useNow(true);
  const fresh = machineFresh(resource, now);
  const status = resource.machine?.status;
  if (!status) return <div className="banner warn">{s.machine.staleNote}</div>;
  return (
    <div className="stack">
      {!fresh && <div className="banner warn">{s.machine.staleNote}</div>}
      <section className="panel stack">
        <h2>{s.machine.usage}</h2>
        {status.snapshot ? <UsageBars snapshot={status.snapshot} limits={limitsOf(resource)} /> : <p>{s.machine.unknown}</p>}
        <p className="meta">{s.machine.firstCpu}</p>
      </section>
      <MachineTrendPanel resource={resource} selector={selector} />
      <section className="panel stack">
        <h2>{s.machine.coverage}</h2>
        <p className="meta">{s.machine.optionalCoverage}</p>
        <CoverageSummary rows={machineCoverageRows(resource)} source={s.machine.source} />
      </section>
      {status.snapshot && (
        <details className="panel">
          <summary>{s.machine.moreDetails}</summary>
          <SnapshotDetails snapshot={status.snapshot} limits={limitsOf(resource)} />
        </details>
      )}
    </div>
  );
}
