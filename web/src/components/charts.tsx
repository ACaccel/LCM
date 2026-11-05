import type { Bucket } from "../lib/series";
import { bucketUptime } from "../lib/series";
import type { SeriesPoint } from "../api/types";
import { formatDuration, formatTime, formatUptime } from "../lib/format";
import { s } from "../strings";
import { ChartFrame, ChartKey, LineSeries, TimeMarks, fmtMs } from "./chart";

const SPARK_W = 100;
const SPARK_H = 24;

/**
 * Availability over time as a small line. Gaps (no check ran) break the
 * line; buckets with failures get a marker so the dip is visible without
 * relying on colour alone.
 */
export function Sparkline({ buckets, tone }: { buckets: readonly Bucket[]; tone: "ok" | "warn" | "bad" | "neutral" }) {
  const n = buckets.length;
  const x = (i: number) => (n <= 1 ? SPARK_W / 2 : (i / (n - 1)) * SPARK_W);
  // 0.9 and below all sit on the baseline: small dips matter most here.
  const y = (r: number) => 3 + (SPARK_H - 6) * (1 - Math.max(0, Math.min(1, (r - 0.9) / 0.1)));
  const segments: string[] = [];
  let cur: string[] = [];
  buckets.forEach((b, i) => {
    if (b.ratio === null) {
      if (cur.length) segments.push(cur.join(" "));
      cur = [];
      return;
    }
    // A lone bucket becomes a short flat stroke, which stays visible
    // under the non-uniform scaling that stretches circles.
    if (cur.length === 0 && (i === n - 1 || buckets[i + 1]?.ratio === null)) {
      segments.push(`${(x(i) - 1.5).toFixed(1)},${y(b.ratio).toFixed(1)} ${(x(i) + 1.5).toFixed(1)},${y(b.ratio).toFixed(1)}`);
      return;
    }
    cur.push(`${x(i).toFixed(1)},${y(b.ratio).toFixed(1)}`);
  });
  if (cur.length) segments.push(cur.join(" "));
  const color = `var(--${tone === "ok" ? "success" : tone === "warn" ? "warning" : tone === "bad" ? "destructive" : "muted-foreground"})`;
  return (
    <svg className="spark" viewBox={`0 0 ${SPARK_W} ${SPARK_H}`} preserveAspectRatio="none" role="img" aria-label={s.overview.uptime24h}>
      {segments.map((pts, i) => (
        <polyline key={i} fill="none" stroke={color} strokeWidth="1.5" vectorEffect="non-scaling-stroke" points={pts} />
      ))}
      {buckets.map((b, i) =>
        b.failures > 0 && b.ratio !== null ? (
          <line key={`f${i}`} x1={x(i)} x2={x(i)} y1={y(b.ratio) - 2} y2={y(b.ratio) + 2} stroke="var(--destructive)" strokeWidth="2" vectorEffect="non-scaling-stroke" />
        ) : null,
      )}
    </svg>
  );
}

function PointTip({ point, stepMs }: { point: SeriesPoint; stepMs: number }) {
  const ok = point.count - point.failures;
  const start = formatTime(point.time);
  const range = stepMs > 0 ? `${start} – ${formatTime(new Date(Date.parse(point.time) + stepMs).toISOString())}` : start;
  return (
    <>
      {ok > 0 ? (
        <div>
          <strong>{formatDuration(point.avgLatencyMs)}</strong> {s.checks.avg}
          <span className="sep">·</span>
          {s.checks.max} {formatDuration(point.maxLatencyMs)}
        </div>
      ) : (
        <div>
          <strong>{s.checks.allFailed}</strong>
        </div>
      )}
      <div className="sub">{range}</div>
      <div className="sub">{s.checks.samples(point.count, point.failures)}</div>
    </>
  );
}

/** Cells of the uptime strip. */
export const STRIP_CELLS = 48;

/**
 * Average latency of one check, scaled to its own values, with failed
 * buckets marked and a strip of per-bucket availability underneath that is
 * aligned with the plot. Hovering, or the arrow keys, show one bucket.
 */
export function LatencyChart({ points, from, to, stepMs, name }: { points: readonly SeriesPoint[]; from: number; to: number; stepMs: number; name?: string }) {
  if (points.length === 0) return <div className="empty">{s.checks.noData}</div>;
  const ok = points.filter((p) => p.count - p.failures > 0);
  const maxV = Math.max(1, ...ok.map((p) => p.avgLatencyMs));
  const times = points.map((p) => Date.parse(p.time));
  const failed = points.filter((p) => p.failures > 0).map((p) => Date.parse(p.time));
  const latest = ok.at(-1);
  const label = s.chart.label(
    name ? s.checks.latencyChart(name) : s.checks.latency,
    latest ? s.chart.latest(formatDuration(latest.avgLatencyMs)) : s.chart.noValue,
    `${formatTime(new Date(from).toISOString())} – ${formatTime(new Date(to).toISOString())}`,
  );
  return (
    <ChartFrame
      from={from}
      to={to}
      yMax={maxV * 1.05}
      format={fmtMs}
      times={times}
      tip={(i) => {
        const p = points[i];
        return p ? <PointTip point={p} stepMs={stepMs} /> : null;
      }}
      label={label}
      height={150}
      footer={<UptimeStrip buckets={bucketUptime(points as SeriesPoint[], from, to, STRIP_CELLS)} from={from} to={to} />}
    >
      <TimeMarks times={failed} color="var(--destructive)" />
      <LineSeries points={points.map((p, i) => ({ t: times[i] ?? 0, v: p.count - p.failures > 0 ? p.avgLatencyMs : null }))} color="var(--chart-1)" gapMs={stepMs > 0 ? stepMs * 1.5 : undefined} />
    </ChartFrame>
  );
}

/** Keys for the marks of LatencyChart and its uptime strip. */
export function ChartLegend({ bucketMs }: { bucketMs: number }) {
  return (
    <>
      <ChartKey
        items={[
          { name: s.checks.legend.latency, color: "var(--chart-1)" },
          { name: s.checks.legend.failed, color: "var(--destructive)", dashed: true },
        ]}
      />
      <div className="chart-legend">
        <span>{s.checks.legend.uptime(formatDuration(bucketMs))}</span>
        {(["ok", "warn", "bad", "none"] as const).map((k) => (
          <span key={k}>
            <i className={`key bar strip-${k}`} aria-hidden="true" />
            {s.chart.uptimeLegend[k]}
          </span>
        ))}
      </div>
    </>
  );
}

/** How bad a bucket was: no failures, some, or most checks failing. */
export function stripState(b: Bucket): "ok" | "warn" | "bad" | "none" {
  if (b.ratio === null) return "none";
  if (b.failures === 0) return "ok";
  return b.ratio >= 0.9 ? "warn" : "bad";
}

/**
 * One cell per bucket, coloured by availability. The state is also in each
 * cell's title and in the legend, so colour is not the only carrier.
 */
export function UptimeStrip({ buckets, from, to }: { buckets: readonly Bucket[]; from?: number; to?: number }) {
  const n = buckets.length;
  const title = (i: number, b: Bucket) => {
    const value = b.ratio === null ? s.checks.noData : `${s.checks.uptime} ${formatUptime(b.ratio, 1)}`;
    if (from === undefined || to === undefined) return value;
    const w = (to - from) / Math.max(1, n);
    return `${formatTime(new Date(from + i * w).toISOString())} – ${formatTime(new Date(from + (i + 1) * w).toISOString())}  ${value}`;
  };
  return (
    <div className="uptime-strip" role="img" aria-label={s.chart.uptimeStrip}>
      {buckets.map((b, i) => (
        <i key={i} className={stripState(b)} title={title(i, b)} />
      ))}
    </div>
  );
}
