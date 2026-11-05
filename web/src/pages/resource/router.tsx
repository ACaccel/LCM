import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { capabilitiesQuery } from "../../api/observations";
import type { ResourceDetail, ResourceSnapshot, RouterSnapshot, RouterStatus } from "../../api/types";
import { ErrorBox, Pill, Tag } from "../../components/ui";
import { actionLabel, reasonText } from "../../lib/actions";
import { formatBytes, formatDateTime, formatDuration } from "../../lib/format";
import { coverageView, cpuPercent } from "../../lib/machine";
import { translateAll } from "../../lib/messages";
import { healthView, reachabilityView, routerOutcomeView } from "../../lib/status";
import { s } from "../../strings";
import { PowerDialog } from "./machine/dialogs";
import { UsageTrend } from "./machine/trend";
import { HistoryPanel } from "./observations";
import { AttemptPanel, OperationsList, useDurableOperation } from "./operations";

function seconds(v: number | null): string {
  return v != null ? formatDuration(v * 1000) : s.machine.unknown;
}

function count(v: number | null): string {
  return v != null ? String(v) : s.machine.unknown;
}

function rate(v: number | null): string {
  return v != null ? `${formatBytes(v)}/s` : s.machine.unknown;
}

/** One router observation attempt: outcome, measurements, WAN and the coverage of each status source. */
export function RouterSnapshotView({ snapshot }: { snapshot: RouterSnapshot }) {
  const wan = snapshot.wan;
  const sources = Object.entries(snapshot.sources ?? {});
  return (
    <div className="stack">
      <dl className="kv">
        <dt>{s.router.outcome}</dt>
        <dd>
          <Pill view={routerOutcomeView(snapshot.outcome)} />
          {snapshot.reason && <div className="meta">{snapshot.reason}</div>}
        </dd>
        <dt>{s.router.model}</dt>
        <dd>{snapshot.model || s.machine.unknown}</dd>
        <dt>{s.router.firmware}</dt>
        <dd>{snapshot.firmware || s.machine.unknown}</dd>
        <dt>{s.router.cpu}</dt>
        <dd>
          {cpuPercent(snapshot.cpuPercent)}
          {snapshot.cpuCores != null && <span className="meta"> ({s.router.cores(snapshot.cpuCores)})</span>}
        </dd>
        <dt>{s.router.memory}</dt>
        <dd>{cpuPercent(snapshot.memoryPercent)}</dd>
        <dt>{s.router.uptime}</dt>
        <dd>{seconds(snapshot.uptimeSeconds)}</dd>
        <dt>{s.router.temperature}</dt>
        <dd>{snapshot.temperatureC != null ? `${snapshot.temperatureC.toFixed(1)} °C` : s.machine.unknown}</dd>
        <dt>{s.router.clients}</dt>
        <dd>{count(snapshot.clients)}</dd>
      </dl>
      <h3>{s.router.wan}</h3>
      <dl className="kv">
        <dt>{s.router.wanLink}</dt>
        <dd>{wan.link == null ? s.machine.unknown : wan.link ? s.router.linkUp : s.router.linkDown}</dd>
        <dt>{s.router.wanStatus}</dt>
        <dd>{count(wan.status)}</dd>
        <dt>{s.router.wanType}</dt>
        <dd>{wan.type || s.machine.unknown}</dd>
        <dt>{s.router.wanUptime}</dt>
        <dd>{seconds(wan.uptimeSeconds)}</dd>
        <dt>{s.router.wanRate}</dt>
        <dd>
          ↑ {rate(wan.upBytesPerSec)} / ↓ {rate(wan.downBytesPerSec)}
        </dd>
        <dt>{s.router.wanTotal}</dt>
        <dd>
          ↑ {wan.uploadedBytes != null ? formatBytes(wan.uploadedBytes) : s.machine.unknown} / ↓ {wan.downloadedBytes != null ? formatBytes(wan.downloadedBytes) : s.machine.unknown}
        </dd>
      </dl>
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th>{s.router.sources}</th>
              <th>{s.common.status}</th>
              <th>{s.machine.reason}</th>
            </tr>
          </thead>
          <tbody>
            {sources.length === 0 && (
              <tr>
                <td colSpan={3}>{s.common.none}</td>
              </tr>
            )}
            {sources.map(([name, c]) => (
              <tr key={name}>
                <td>{name}</td>
                <td>
                  <Pill view={coverageView(c)} />
                </td>
                <td>{c.reason || s.common.none}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function RouterStatusPanel({ status }: { status: RouterStatus }) {
  return (
    <section className="panel stack">
      <h2>{s.router.status}</h2>
      <div className="row">
        <Pill view={reachabilityView(status.reachability)} />
        {status.fresh ? <Pill view={healthView(status.health)} /> : <Tag>{s.machine.stale}</Tag>}
        <Tag>{status.fresh ? s.machine.fresh : s.machine.stale}</Tag>
      </div>
      <dl className="kv">
        <dt>{s.machine.observed}</dt>
        <dd>{formatDateTime(status.snapshot?.observedAt)}</dd>
        <dt>{s.router.bootAt}</dt>
        <dd>{status.bootAt ? formatDateTime(status.bootAt) : s.machine.unknown}</dd>
        <dt>{s.machine.healthReasons}</dt>
        <dd>{translateAll(status.reasonMessages)}</dd>
      </dl>
      {status.snapshot ? <RouterSnapshotView snapshot={status.snapshot} /> : <p>{s.router.noSnapshot}</p>}
    </section>
  );
}

function snapshotSummary(snap: ResourceSnapshot): string {
  const r = snap.router;
  if (!r) return s.machine.unknown;
  if (r.outcome !== "ok") return s.router.outcomes[r.outcome];
  return `${s.router.cpu} ${cpuPercent(r.cpuPercent)} · ${s.router.memory} ${cpuPercent(r.memoryPercent)}`;
}

/** The router's monitoring tab: the latest status and stored history. */
export function RouterMonitoring({ resource, selector }: { resource: ResourceDetail; selector: string }) {
  const status = resource.router?.status;
  return (
    <div className="stack">
      {resource.state !== "online" && <div className="banner warn">{s.router.staleNote}</div>}
      {status ? (
        <RouterStatusPanel status={status} />
      ) : (
        <section className="panel stack">
          <h2>{s.router.status}</h2>
          <p>{s.router.noSnapshot}</p>
        </section>
      )}
      <HistoryPanel
        resource={selector}
        summary={snapshotSummary}
        render={(snap) => (snap.router ? <RouterSnapshotView snapshot={snap.router} /> : <p>{s.machine.unknown}</p>)}
        trend={(snaps) => (
          <UsageTrend points={snaps.flatMap((x) => (x.router ? [{ id: x.id, observedAt: x.observedAt, cpu: x.router.cpuPercent, memory: x.router.memoryPercent }] : []))} />
        )}
      />
    </div>
  );
}

/** Durable router operations (reboot): administrator only, confirmed with a reason and the typed name. */
export function RouterControls({ resource, selector }: { resource: ResourceDetail; selector: string }) {
  const caps = useQuery(capabilitiesQuery(selector));
  const op = useDurableOperation(resource);
  const [confirm, setConfirm] = useState<{ action: string; version: number } | null>(null);
  const operations = resource.actions.filter((a) => a.kind === "operation");
  const unavailable = (name: string): string => {
    const a = operations.find((x) => x.name === name);
    if (!a) return s.action.unavailable.notOffered;
    if (!a.available) return reasonText(a, resource.state) || s.action.unavailable.notOffered;
    if (!caps.isSuccess) return s.machine.capabilityUnknown;
    const cap = caps.data.items.find((c) => c.action === name);
    return cap && !cap.available ? cap.reason || s.machine.capabilityUnknown : "";
  };
  return (
    <section className="panel stack">
      <h2>{s.machine.controls}</h2>
      <ErrorBox error={caps.error} onRetry={() => void caps.refetch()} />
      {operations.map((a) => {
        const reason = unavailable(a.name);
        return (
          <div className="stack" key={a.name}>
            <div>
              <button
                type="button"
                className="btn danger"
                disabled={!!reason || !!op.attempt || !!op.storageError || resource.phase !== "managed"}
                onClick={() => setConfirm({ action: a.name, version: resource.version })}
              >
                {actionLabel(a.name)}
              </button>
            </div>
            {reason && <p className="meta">{reason}</p>}
          </div>
        );
      })}
      <ErrorBox error={op.storageError} />
      <ErrorBox error={op.mutation.error} />
      <AttemptPanel resource={resource.name} op={op} />
      {confirm && (
        <PowerDialog
          resource={resource}
          action={confirm.action}
          version={confirm.version}
          disclosure={s.router.rebootDisclosure}
          unavailable={unavailable(confirm.action)}
          onClose={() => setConfirm(null)}
          onConfirm={(c) => {
            op.start(confirm.action, confirm.version, c);
            setConfirm(null);
          }}
        />
      )}
    </section>
  );
}

export function RouterOperationsTab({ resource, selector }: { resource: ResourceDetail; selector: string }) {
  return (
    <div className="stack">
      <p className="meta">{s.machine.refreshNote}</p>
      <RouterControls resource={resource} selector={selector} />
      <OperationsList resource={selector} />
    </div>
  );
}
