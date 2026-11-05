import type { MachineSnapshot, ResourceDetail } from "../../../api/types";
import { UsageBar } from "../../../components/chart";
import { Pill, Tag } from "../../../components/ui";
import { useNow } from "../../../hooks";
import { formatDateTime, formatDuration } from "../../../lib/format";
import { coverageView, machineFresh, machineHealth, thresholdOf, usedPercent } from "../../../lib/machine";
import type { Observed } from "../../../lib/machine";
import { translateAll } from "../../../lib/messages";
import { coverageSource, s } from "../../../strings";
import { HistoryPanel } from "../observations";
import { MachineTrend } from "./trend";

/** Machine health as the console may claim it: unknown once observations are stale or the identity is unverified. */
export function MachineHealth({ resource }: { resource: Observed }) {
  const now = useNow(true);
  return <Pill view={machineHealth(resource, now)} />;
}

export interface UsageLimits {
  cpu: number;
  memory: number;
  disk: number;
}

/** Limits of the machine document, which the usage bars mark. */
export function limitsOf(resource: Observed): UsageLimits {
  return { cpu: thresholdOf(resource, "cpuPercent"), memory: thresholdOf(resource, "memoryPercent"), disk: thresholdOf(resource, "diskPercent") };
}

export function SnapshotView({ snapshot, limits }: { snapshot: MachineSnapshot; limits: UsageLimits }) {
  const available = (source: string) => snapshot.coverage[source]?.state === "available";
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
        <dt>{s.machine.cpu}</dt>
        <dd>{available("cpu") ? <UsageBar value={snapshot.cpuPercent} label={s.machine.cpu} threshold={limits.cpu} /> : s.machine.unknown}</dd>
        <dt>{s.machine.load}</dt>
        <dd>{available("cpu") ? `${snapshot.load?.join(" / ") ?? s.machine.unknown} / ${snapshot.cpuCount}` : s.machine.unknown}</dd>
        <dt>{s.machine.memory}</dt>
        <dd>{available("memory") ? <UsageBar value={usedPercent(snapshot.memoryTotal, snapshot.memoryAvailable)} label={s.machine.memory} threshold={limits.memory} /> : s.machine.unknown}</dd>
        <dt>{s.machine.swap}</dt>
        <dd>{available("memory") ? <UsageBar value={usedPercent(snapshot.swapTotal, snapshot.swapFree)} label={s.machine.swap} /> : s.machine.unknown}</dd>
      </dl>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>{s.machine.mountpoint}</th>
              <th>{s.machine.disk}</th>
              <th>{s.machine.inodes}</th>
            </tr>
          </thead>
          <tbody>
            {(snapshot.filesystems ?? []).map((fs) => (
              <tr key={fs.path}>
                <td>
                  <code>{fs.path}</code>
                </td>
                <td>{available("filesystems") ? <UsageBar value={usedPercent(fs.total, fs.available)} label={`${s.machine.disk} ${fs.path}`} threshold={limits.disk} /> : s.machine.unknown}</td>
                <td>{available("filesystems") ? <UsageBar value={usedPercent(fs.inodes, fs.freeInodes)} label={`${s.machine.inodes} ${fs.path}`} threshold={limits.disk} /> : s.machine.unknown}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <dl className="kv">
        <dt>{s.machine.units}</dt>
        <dd>
          {!Object.keys(snapshot.units ?? {}).length
            ? s.machine.unknown
            : Object.entries(snapshot.units ?? {}).map(([unit, state]) => (
                <div key={unit}>
                  <code>{unit}</code>: {state}
                </div>
              ))}
        </dd>
        <dt>{s.machine.interfaces}</dt>
        <dd>
          {!Object.keys(snapshot.interfaces ?? {}).length
            ? s.machine.unknown
            : Object.entries(snapshot.interfaces ?? {}).map(([name, state]) => (
                <div key={name}>
                  <code>{name}</code>: {state}
                </div>
              ))}
        </dd>
        <dt>{s.machine.pressure}</dt>
        <dd>
          {!Object.keys(snapshot.pressure ?? {}).length
            ? s.machine.unknown
            : Object.entries(snapshot.pressure ?? {}).map(([name, value]) => (
                <div key={name}>
                  <code>{name}</code>: {value}
                </div>
              ))}
        </dd>
      </dl>
    </div>
  );
}

export function MachineOverview({ resource }: { resource: Observed }) {
  const now = useNow(true);
  const fresh = machineFresh(resource, now);
  const status = resource.machine?.status;
  if (!status) return <div className="banner warn">{s.machine.staleNote}</div>;
  const coverage = {
    probe: undefined,
    identity: undefined,
    cpu: undefined,
    memory: undefined,
    filesystems: undefined,
    units: undefined,
    interfaces: undefined,
    psi: undefined,
    ...status.snapshot?.coverage,
    journal: status.journal,
  };
  return (
    <div className="stack">
      {!fresh && <div className="banner warn">{s.machine.staleNote}</div>}
      <section className="panel stack">
        <div className="row">
          <MachineHealth resource={resource} />
          <Tag>{fresh ? s.machine.fresh : s.machine.stale}</Tag>
        </div>
        <dl className="kv">
          <dt>{s.machine.identity}</dt>
          <dd>{s.machine.identityStates[status.identity] ?? status.identity}</dd>
          <dt>{s.machine.healthReasons}</dt>
          <dd>{translateAll(status.reasonMessages)}</dd>
        </dl>
      </section>
      <section className="panel stack">
        <h2>{s.machine.resources}</h2>
        {status.snapshot ? <SnapshotView snapshot={status.snapshot} limits={limitsOf(resource)} /> : <p>{s.machine.unknown}</p>}
        <p className="meta">{s.machine.firstCpu}</p>
      </section>
      <section className="panel stack">
        <h2>{s.machine.coverage}</h2>
        <p className="meta">{s.machine.optionalCoverage}</p>
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>{s.machine.source}</th>
                <th>{s.common.status}</th>
                <th>{s.machine.reason}</th>
              </tr>
            </thead>
            <tbody>
              {Object.entries(coverage).map(([source, value]) => (
                <tr key={source}>
                  <td>
                    {coverageSource(source)}
                    {["interfaces", "psi", "journal"].includes(source) && <span className="meta"> ({s.common.optional})</span>}
                  </td>
                  <td>
                    <Pill view={coverageView(value)} />
                  </td>
                  <td>{value?.reason ? (s.observation.reasons[value.reason] ?? value.reason) : s.common.none}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

/** The machine's monitoring tab: current status and stored history. */
export function MachineMonitoring({ resource, selector }: { resource: ResourceDetail; selector: string }) {
  return (
    <div className="stack">
      <MachineOverview resource={resource} />
      <HistoryPanel
        resource={selector}
        note={s.machine.firstCpu}
        trend={(snaps) => {
          const limits = limitsOf(resource);
          return <MachineTrend thresholds={{ cpu: limits.cpu, memory: limits.memory }} snapshots={snaps.flatMap((x) => (x.machine ? [x.machine] : []))} />;
        }}
      />
    </div>
  );
}
