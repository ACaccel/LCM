import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { capabilitiesQuery, historyQuery } from "../../api/observations";
import type { ResourceDetail, RouterSnapshot, RouterStatus } from "../../api/types";
import { RANGE_MS, RangeSwitch, TimeSeriesChart, fmtCelsius, fmtCount, fmtRate, windowSeries } from "../../components/chart";
import type { RangeKey, XY } from "../../components/chart";
import { ErrorBox, Pill, Spinner } from "../../components/ui";
import { actionLabel, reasonText } from "../../lib/actions";
import { formatBytes, formatDateTime, formatDuration } from "../../lib/format";
import { capabilityReason, coverageView, cpuPercent, thresholdOf } from "../../lib/machine";
import { translate, translateAll } from "../../lib/messages";
import { routerOutcomeView } from "../../lib/status";
import { s } from "../../strings";
import { PowerDialog } from "./machine/dialogs";
import { UsageTrend } from "./machine/trend";
import { CoverageSummary } from "./coverage";
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
          {snapshot.reason && <div className="meta">{snapshot.reasonMessage ? translate(snapshot.reasonMessage) : snapshot.reason}</div>}
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
      <CoverageSummary
        source={s.router.sources}
        rows={sources.map(([name, c]) => ({
          key: name,
          label: name,
          view: coverageView(c),
          available: c.state === "available",
          reason: c.reason ? (c.reasonMessage ? translate(c.reasonMessage) : c.reason) : "",
        }))}
      />
    </div>
  );
}

function RouterStatusPanel({ status }: { status: RouterStatus }) {
  return (
    <section className="panel stack">
      <h2>{s.router.keyStatus}</h2>
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

/**
 * Stored router observations of a range the reader picks, as charts: CPU and
 * memory with their limits, WAN rates, connected clients and temperature. A
 * metric the router never reported is not drawn; unreachable attempts leave
 * gaps. Each range is a request for that range.
 */
export function RouterCharts({ selector, resource }: { selector: string; resource: ResourceDetail }) {
  const [range, setRange] = useState<RangeKey>("24h");
  const q = useQuery(historyQuery(selector, range));
  const rows = (q.data ?? []).flatMap((x) => (x.router ? [{ id: x.id, at: x.observedAt, t: Date.parse(x.observedAt), r: x.router }] : []));
  const metric = (read: (r: RouterSnapshot) => number | null | undefined): XY[] => rows.map((x) => ({ t: x.t, v: read(x.r) ?? null }));
  const has = (xy: XY[]) => xy.some((p) => p.v !== null);
  const up = metric((r) => r.wan.upBytesPerSec);
  const down = metric((r) => r.wan.downBytesPerSec);
  const clients = metric((r) => r.clients);
  const temperature = metric((r) => r.temperatureC);
  const win = windowSeries([up, down, clients, temperature], RANGE_MS[range]);
  const panels = [
    { key: "wan", title: s.chart.wanTitle, show: has(up) || has(down), format: fmtRate, binary: true, series: [
      { name: s.chart.wanUp, color: "var(--chart-1)", points: win?.series[0] ?? [] },
      { name: s.chart.wanDown, color: "var(--chart-2)", dashed: true, points: win?.series[1] ?? [] },
    ] },
    { key: "clients", title: s.chart.clientsTitle, show: has(clients), format: fmtCount, binary: false, series: [{ name: s.router.clients, color: "var(--chart-4)", points: win?.series[2] ?? [] }] },
    { key: "temperature", title: s.chart.temperatureTitle, show: has(temperature), format: fmtCelsius, binary: false, series: [{ name: s.router.temperature, color: "var(--chart-3)", points: win?.series[3] ?? [] }] },
  ].filter((p) => p.show);
  return (
    <section className="panel stack">
      <div className="chart-title">
        <h2>{s.chart.usageTitle}</h2>
        <RangeSwitch value={range} onChange={setRange} />
      </div>
      <ErrorBox error={q.error} onRetry={() => void q.refetch()} />
      {q.isPending && <Spinner />}
      {q.data && rows.length === 0 && <p>{s.machine.trendEmpty}</p>}
      <UsageTrend
        range={range}
        thresholds={{ cpu: thresholdOf(resource, "cpuPercent"), memory: thresholdOf(resource, "memoryPercent") }}
        points={rows.map((x) => ({ id: x.id, observedAt: x.at, cpu: x.r.cpuPercent, memory: x.r.memoryPercent }))}
      />
      {win && panels.length > 0 && (
        <div className="chart-grid-2">
          {panels.map((p) => (
            <section key={p.key} className="stack">
              <h3>{p.title}</h3>
              <TimeSeriesChart name={p.title} from={win.from} to={win.to} format={p.format} binary={p.binary} gapMs={win.gapMs} series={p.series} height={140} />
            </section>
          ))}
        </div>
      )}
    </section>
  );
}

/** The router's overview: the latest status and charts over a chosen range. */
export function RouterOverview({ resource, selector }: { resource: ResourceDetail; selector: string }) {
  const status = resource.router?.status;
  return (
    <div className="stack">
      {resource.state !== "online" && <div className="banner warn">{s.router.staleNote}</div>}
      {status ? (
        <RouterStatusPanel status={status} />
      ) : (
        <section className="panel stack">
          <h2>{s.router.keyStatus}</h2>
          <p>{s.router.noSnapshot}</p>
        </section>
      )}
      <RouterCharts selector={selector} resource={resource} />
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
    return cap && !cap.available ? capabilityReason(cap) : "";
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
