import { useState } from "react";
import type { Bucket } from "../lib/series";
import type { SeriesPoint } from "../api/types";
import { formatDuration, formatTime, formatUptime } from "../lib/format";
import { s } from "../strings";

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

/** Index of the point whose time is nearest to t; points are sorted by time. */
function nearest(points: readonly SeriesPoint[], t: number): number {
  let best = 0;
  let bestDist = Infinity;
  points.forEach((p, i) => {
    const d = Math.abs(Date.parse(p.time) - t);
    if (d < bestDist) {
      best = i;
      bestDist = d;
    }
  });
  return best;
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

/**
 * Average latency of one check with failed buckets marked. The top of the
 * scale is labelled; hovering or the arrow keys show one bucket's values.
 */
export function LatencyChart({ points, from, to, stepMs }: { points: readonly SeriesPoint[]; from: number; to: number; stepMs: number }) {
  const [active, setActive] = useState<number | null>(null);
  const W = 300;
  const H = 90;
  const TOP = 8;
  const span = Math.max(1, to - from);
  const ok = points.filter((p) => p.count - p.failures > 0);
  const maxV = Math.max(50, ...ok.map((p) => p.maxLatencyMs));
  const x = (t: string) => ((Date.parse(t) - from) / span) * W;
  const y = (v: number) => H - 6 - (v / maxV) * (H - 6 - TOP);
  const line = ok.map((p) => `${x(p.time).toFixed(1)},${y(p.avgLatencyMs).toFixed(1)}`).join(" ");
  const failed = points.filter((p) => p.failures > 0);
  if (points.length === 0) return <div className="empty">{s.checks.noData}</div>;
  const point = active === null ? undefined : points[active];
  const pct = point ? Math.min(100, Math.max(0, (x(point.time) / W) * 100)) : 0;
  const onKey = (e: React.KeyboardEvent) => {
    if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return;
    e.preventDefault();
    const step = e.key === "ArrowLeft" ? -1 : 1;
    setActive((i) => Math.min(points.length - 1, Math.max(0, (i ?? points.length - 1) + step)));
  };
  return (
    <div
      className="chart-frame"
      tabIndex={0}
      aria-label={s.checks.chartHelp}
      onPointerMove={(e) => {
        const r = e.currentTarget.getBoundingClientRect();
        setActive(nearest(points, from + ((e.clientX - r.left) / Math.max(1, r.width)) * span));
      }}
      onPointerLeave={() => setActive(null)}
      onFocus={() => setActive((i) => i ?? points.length - 1)}
      onBlur={() => setActive(null)}
      onKeyDown={onKey}
    >
      <svg className="chart" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" aria-hidden="true" style={{ width: "100%", height: H, display: "block" }}>
        <line x1="0" x2={W} y1={TOP} y2={TOP} stroke="var(--border)" strokeDasharray="2 4" vectorEffect="non-scaling-stroke" />
        <line x1="0" x2={W} y1={H - 6} y2={H - 6} stroke="var(--border)" vectorEffect="non-scaling-stroke" />
        {ok.length > 1 && <polyline fill="none" stroke="var(--chart-1)" strokeWidth="1.6" vectorEffect="non-scaling-stroke" points={line} />}
        {ok.length === 1 && ok[0] && <circle cx={x(ok[0].time)} cy={y(ok[0].avgLatencyMs)} r="2" fill="var(--chart-1)" />}
        {failed.map((p) => (
          <line key={p.time} x1={x(p.time)} x2={x(p.time)} y1="0" y2={H - 6} stroke="var(--destructive)" strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />
        ))}
        {point && <line x1={x(point.time)} x2={x(point.time)} y1="0" y2={H - 6} stroke="var(--muted-foreground)" vectorEffect="non-scaling-stroke" />}
      </svg>
      <span className="chart-axis">{formatDuration(maxV)}</span>
      {point && (
        <div className={`chart-tip${pct > 60 ? " left" : ""}`} style={{ left: `${pct}%` }} role="status">
          <PointTip point={point} stepMs={stepMs} />
        </div>
      )}
    </div>
  );
}

/** Keys for the marks of LatencyChart and UptimeBars. */
export function ChartLegend({ bucketMs }: { bucketMs: number }) {
  return (
    <div className="chart-legend">
      <span>
        <i className="key line" />
        {s.checks.legend.latency}
      </span>
      <span>
        <i className="key dashed" />
        {s.checks.legend.failed}
      </span>
      <span>
        <i className="key bar" />
        {s.checks.legend.uptime(formatDuration(bucketMs))}
      </span>
    </div>
  );
}

/**
 * One bar per bucket: height is the uptime ratio, failures are drawn as a
 * cap. With from and to, each bar names its time range and ratio on hover.
 */
export function UptimeBars({ buckets, from, to }: { buckets: readonly Bucket[]; from?: number; to?: number }) {
  const n = buckets.length;
  const bw = 100 / Math.max(1, n);
  const title = (i: number, b: Bucket) => {
    if (from === undefined || to === undefined) return null;
    const w = (to - from) / Math.max(1, n);
    const range = `${formatTime(new Date(from + i * w).toISOString())} – ${formatTime(new Date(from + (i + 1) * w).toISOString())}`;
    return <title>{`${range}  ${b.ratio === null ? s.checks.noData : `${s.checks.uptime} ${formatUptime(b.ratio, 1)}`}`}</title>;
  };
  return (
    <svg viewBox="0 0 100 24" preserveAspectRatio="none" role="img" aria-label={s.checks.uptime} style={{ width: "100%", height: 28, display: "block" }}>
      {buckets.map((b, i) =>
        b.ratio === null ? (
          <rect key={i} x={i * bw + 0.15} y="21" width={Math.max(0.3, bw - 0.3)} height="2" fill="var(--border)">
            {title(i, b)}
          </rect>
        ) : (
          <g key={i}>
            {title(i, b)}
            <rect x={i * bw + 0.15} y={24 - 22 * b.ratio} width={Math.max(0.3, bw - 0.3)} height={22 * b.ratio} fill="var(--success)" />
            {b.failures > 0 && <rect x={i * bw + 0.15} y="0" width={Math.max(0.3, bw - 0.3)} height={22 * (1 - b.ratio) + 2} fill="var(--destructive)" />}
          </g>
        ),
      )}
    </svg>
  );
}
