import { useChartScale } from "./context";
import { lineSegments, stackSegments } from "./scale";
import type { XY } from "./scale";

const f = (n: number) => n.toFixed(1);

/**
 * One line. Missing values, and spacings wider than `gapMs`, break the line
 * instead of being bridged; a lone sample between gaps is drawn as a dot.
 */
export function LineSeries({ points, color, dashed, gapMs }: { points: readonly XY[]; color: string; dashed?: boolean; gapMs?: number }) {
  const { x, y, activeT } = useChartScale();
  const segments = lineSegments(points, gapMs);
  const active = activeT === null ? undefined : points.find((p) => p.t === activeT && p.v !== null);
  return (
    <g className="chart-line" style={{ ["--series" as string]: color }}>
      {segments.map((seg) =>
        seg.length === 1 ? (
          <circle key={seg[0]?.t} className="chart-dot" cx={x(seg[0]?.t ?? 0)} cy={y(seg[0]?.v ?? 0)} r={3} />
        ) : (
          <path key={seg[0]?.t} className={`chart-path${dashed ? " dashed" : ""}`} d={seg.map((p, i) => `${i ? "L" : "M"}${f(x(p.t))},${f(y(p.v))}`).join("")} />
        ),
      )}
      {active && active.v !== null && <circle className="chart-active" cx={x(active.t)} cy={y(active.v)} r={4} />}
    </g>
  );
}

export interface BarSeriesSpec {
  /** A colour, or a colour per bar index. */
  color: string | ((index: number) => string);
  values: readonly (number | null)[];
  name?: string;
}

/**
 * Bars centred on `times`. Stacked series add up to one bar, with a surface
 * gap between segments; otherwise the series sit side by side.
 */
export function BarSeries({ times, series, stacked, barMs, barPx = 8 }: { times: readonly number[]; series: readonly BarSeriesSpec[]; stacked?: boolean; barMs?: number; barPx?: number }) {
  const { x, y, plot, domain } = useChartScale();
  const colorOf = (s: BarSeriesSpec, i: number) => (typeof s.color === "function" ? s.color(i) : s.color);
  return (
    <g className="chart-bars">
      {times.map((t, i) => {
        const w = Math.max(1, (barMs !== undefined ? x(t + barMs / 2) - x(t - barMs / 2) : barPx) - 1);
        const x0 = x(t) - w / 2;
        const values = series.map((s) => s.values[i] ?? null);
        if (stacked) {
          return (
            <g key={t}>
              {stackSegments(values).map((seg) => {
                const top = y(seg.to);
                const h = Math.max(1, y(seg.from) - top);
                const s = series[seg.series];
                return s ? <rect key={seg.series} className="chart-bar stacked" x={x0} y={y(seg.from) - h} width={w} height={h} fill={colorOf(s, i)} /> : null;
              })}
            </g>
          );
        }
        const each = w / Math.max(1, series.length);
        return (
          <g key={t}>
            {series.map((s, k) => {
              const v = values[k];
              if (v === null || v === undefined || !Number.isFinite(v) || v <= domain.min) return null;
              const h = Math.max(1, plot.bottom - y(v));
              return <rect key={k} className="chart-bar" x={x0 + k * each} y={plot.bottom - h} width={Math.max(1, each - (series.length > 1 ? 1 : 0))} height={h} rx={1} fill={colorOf(s, i)} />;
            })}
          </g>
        );
      })}
    </g>
  );
}

/** A dashed horizontal line at a limit, such as 90% CPU, labelled at its right end. */
export function ThresholdMark({ value, label, tone = "bad" }: { value: number; label?: string; tone?: "warn" | "bad" }) {
  const { y, plot, domain } = useChartScale();
  if (value < domain.min || value > domain.max) return null;
  const py = y(value);
  const color = tone === "warn" ? "var(--warning)" : "var(--destructive)";
  return (
    <g className="chart-threshold" style={{ ["--series" as string]: color }}>
      <line x1={plot.left} x2={plot.right} y1={py} y2={py} />
      {label && (
        <text x={plot.right - 4} y={py - 4} textAnchor="end">
          {label}
        </text>
      )}
    </g>
  );
}

/** Vertical marks at times of interest, such as buckets in which a check failed. */
export function TimeMarks({ times, color }: { times: readonly number[]; color: string }) {
  const { x, plot } = useChartScale();
  return (
    <g className="chart-marks" style={{ ["--series" as string]: color }}>
      {times.map((t) => (
        <line key={t} x1={x(t)} x2={x(t)} y1={plot.top} y2={plot.bottom} />
      ))}
    </g>
  );
}
