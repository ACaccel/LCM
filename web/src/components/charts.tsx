import type { Bucket } from "../lib/series";
import type { SeriesPoint } from "../api/types";
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

/** Latency line of one check with failed buckets marked on the baseline. */
export function LatencyChart({ points, from, to }: { points: readonly SeriesPoint[]; from: number; to: number }) {
  const W = 300;
  const H = 90;
  const span = Math.max(1, to - from);
  const ok = points.filter((p) => p.count - p.failures > 0);
  const maxV = Math.max(50, ...ok.map((p) => p.maxLatencyMs));
  const x = (t: string) => ((Date.parse(t) - from) / span) * W;
  const y = (v: number) => H - 6 - (v / maxV) * (H - 14);
  const line = ok.map((p) => `${x(p.time).toFixed(1)},${y(p.avgLatencyMs).toFixed(1)}`).join(" ");
  const failed = points.filter((p) => p.failures > 0);
  if (points.length === 0) return <div className="empty">{s.checks.noData}</div>;
  return (
    <svg className="chart" viewBox={`0 0 ${W} ${H}`} preserveAspectRatio="none" role="img" style={{ width: "100%", height: 90, display: "block" }}>
      <line x1="0" x2={W} y1={H - 6} y2={H - 6} stroke="var(--border)" vectorEffect="non-scaling-stroke" />
      {ok.length > 1 && <polyline fill="none" stroke="var(--chart-1)" strokeWidth="1.6" vectorEffect="non-scaling-stroke" points={line} />}
      {ok.length === 1 && ok[0] && <circle cx={x(ok[0].time)} cy={y(ok[0].avgLatencyMs)} r="2" fill="var(--chart-1)" />}
      {failed.map((p) => (
        <line key={p.time} x1={x(p.time)} x2={x(p.time)} y1="0" y2={H - 6} stroke="var(--destructive)" strokeDasharray="3 3" vectorEffect="non-scaling-stroke" />
      ))}
    </svg>
  );
}

/** One bar per bucket: height is the uptime ratio, failures are drawn as a cap. */
export function UptimeBars({ buckets }: { buckets: readonly Bucket[] }) {
  const n = buckets.length;
  const bw = 100 / Math.max(1, n);
  return (
    <svg viewBox="0 0 100 24" preserveAspectRatio="none" role="img" aria-label={s.checks.uptime} style={{ width: "100%", height: 28, display: "block" }}>
      {buckets.map((b, i) =>
        b.ratio === null ? (
          <rect key={i} x={i * bw + 0.15} y="21" width={Math.max(0.3, bw - 0.3)} height="2" fill="var(--border)" />
        ) : (
          <g key={i}>
            <rect x={i * bw + 0.15} y={24 - 22 * b.ratio} width={Math.max(0.3, bw - 0.3)} height={22 * b.ratio} fill="var(--success)" />
            {b.failures > 0 && <rect x={i * bw + 0.15} y="0" width={Math.max(0.3, bw - 0.3)} height={22 * (1 - b.ratio) + 2} fill="var(--destructive)" />}
          </g>
        ),
      )}
    </svg>
  );
}
