import { useInfiniteQuery } from "@tanstack/react-query";
import {useState} from "react";
import type { Machine, MachineSnapshot } from "../../api/machines";
import { machineEventsQuery, machineHistoryQuery } from "../../api/machines";
import { ErrorBox, Pill, Spinner, Tag } from "../../components/ui";
import { useNow } from "../../hooks";
import { formatDateTime, formatDuration } from "../../lib/format";
import { coverageView, cpuPercent, machineFresh, machineHealth, percentUsed } from "../../lib/machine";
import { s } from "../../strings";
import {MachineTrend} from "./trend";

export function MachineHealth({ machine }: { machine: Machine }) {
  const now = useNow(true);
  return <Pill view={machineHealth(machine, now)} />;
}

export function SnapshotView({ snapshot }: { snapshot: MachineSnapshot }) {
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
        <dd>{available("cpu") ? cpuPercent(snapshot.cpuPercent) : s.machine.unknown}</dd>
        <dt>{s.machine.load}</dt>
        <dd>{available("cpu") ? `${snapshot.load?.join(" / ") ?? s.machine.unknown} / ${snapshot.cpuCount}` : s.machine.unknown}</dd>
        <dt>{s.machine.memory}</dt>
        <dd>{available("memory") ? percentUsed(snapshot.memoryTotal, snapshot.memoryAvailable) : s.machine.unknown}</dd>
        <dt>{s.machine.swap}</dt>
        <dd>{available("memory") ? percentUsed(snapshot.swapTotal, snapshot.swapFree) : s.machine.unknown}</dd>
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
                <td>{available("filesystems") ? percentUsed(fs.total, fs.available) : s.machine.unknown}</td>
                <td>{available("filesystems") ? percentUsed(fs.inodes, fs.freeInodes) : s.machine.unknown}</td>
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

export function MachineOverview({ machine }: { machine: Machine }) {
  const now = useNow(true);
  const fresh = machineFresh(machine, now);
  const status = machine.status;
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
          <MachineHealth machine={machine} />
          <Tag>{fresh ? s.machine.fresh : s.machine.stale}</Tag>
        </div>
        <dl className="kv">
          <dt>{s.machine.identity}</dt>
          <dd>{s.machine.identityStates[status.identity] ?? status.identity}</dd>
          <dt>{s.machine.healthReasons}</dt>
          <dd>{status.reasons.join("; ") || s.common.none}</dd>
        </dl>
      </section>
      <section className="panel stack">
        <h2>{s.machine.resources}</h2>
        {status.snapshot ? <SnapshotView snapshot={status.snapshot} /> : <p>{s.machine.unknown}</p>}
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
                    {source}
                    {["interfaces", "psi", "journal"].includes(source) && <span className="meta"> ({s.common.optional})</span>}
                  </td>
                  <td>
                    <Pill view={coverageView(value)} />
                  </td>
                  <td>{value?.reason ?? s.common.none}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}

export function MachineEvents({ name }: { name: string }) {
  const q = useInfiniteQuery(machineEventsQuery(name));
  const events = q.data?.pages.flatMap((p) => p.items) ?? [];
  return (
    <section className="panel stack">
      <h2>{s.machine.events}</h2>
      <p className="meta">{s.machine.journalNote}</p>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {!q.isPending && !events.length && <p>{s.common.empty}</p>}
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>{s.machine.sourceTime}</th>
              <th>{s.machine.receivedTime}</th>
              <th>{s.machine.source}</th>
              <th>{s.machine.kind}</th>
              <th>{s.common.details}</th>
            </tr>
          </thead>
          <tbody>
            {events.map((ev) => (
              <tr key={ev.id}>
                <td>{formatDateTime(ev.occurredAt)}</td>
                <td>{formatDateTime(ev.ingestedAt)}</td>
                <td>
                  {ev.source}
                  <div>
                    <Tag>{ev.historical ? s.machine.historical : s.machine.live}</Tag>
                  </div>
                </td>
                <td>
                  {ev.kind}
                  <div className="meta">{ev.severity}</div>
                  {ev.kind.includes("gap") && <Tag>{s.machine.gap}</Tag>}
                </td>
                <td>
                  {ev.reason}
                  <div>
                    <code>{ev.unit}</code>
                  </div>
                  <div className="meta">{ev.bootId}</div>
				  {ev.operationId&&<div className="meta"><code>{ev.operationId}</code></div>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {q.hasNextPage && (
        <button className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
    </section>
  );
}

export function MachineHistory({ name }: { name: string }) {
	const [resolution,setResolution]=useState<"raw"|"minute">("raw");
  const q = useInfiniteQuery(machineHistoryQuery(name,resolution));
  return (
    <section className="panel stack">
      <h2>{s.machine.history}</h2>
	  <select aria-label={s.machine.history} value={resolution} onChange={e=>setResolution(e.target.value as "raw"|"minute")}><option value="raw">{s.machine.rawHistory}</option><option value="minute">{s.machine.minuteHistory}</option></select>
      <p className="meta">{s.machine.firstCpu}</p>
	  <MachineTrend snapshots={q.data?.pages.flatMap(p=>p.items)??[]}/>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {q.data?.pages
        .flatMap((p) => p.items)
        .map((snapshot) => (
          <details className="panel" key={snapshot.id}>
            <summary>
              {formatDateTime(snapshot.observedAt)} — {s.machine.cpu} {cpuPercent(snapshot.cpuPercent)}
            </summary>
            <SnapshotView snapshot={snapshot} />
          </details>
        ))}
      {q.data && !q.data.pages.some((p) => p.items.length) && <p>{s.common.empty}</p>}
      {q.hasNextPage && (
        <button className="btn" disabled={q.isFetchingNextPage} onClick={() => void q.fetchNextPage()}>
          {s.common.more}
        </button>
      )}
    </section>
  );
}
