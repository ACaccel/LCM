import { s } from "../../strings";
import { formatDateTime, formatTime } from "../../lib/format";
import { ChartFrame } from "./frame";
import { ChartKey } from "./legend";
import { LineSeries, ThresholdMark } from "./series";
import type { XY } from "./scale";
import { TipRow } from "./tooltip";

export interface TimeSeriesSpec {
  name: string;
  color: string;
  dashed?: boolean;
  points: readonly XY[];
}

export interface ThresholdSpec {
  value: number;
  label: string;
}

export interface TimeSeriesChartProps {
  /** What the chart shows; the accessible name is built from it, the latest values and the range. */
  name: string;
  series: readonly TimeSeriesSpec[];
  from: number;
  to: number;
  format: (v: number) => string;
  /** Fix the top of the axis, for percentages. */
  fixedMax?: number;
  binary?: boolean;
  thresholds?: readonly ThresholdSpec[];
  /** A spacing wider than this breaks the lines. */
  gapMs?: number;
  height?: number;
}

const latestOf = (points: readonly XY[]): number | null => {
  for (let i = points.length - 1; i >= 0; i--) {
    const v = points[i]?.v;
    if (v !== null && v !== undefined) return v;
  }
  return null;
};

/**
 * One or more lines over time with axes, optional limits, a legend and a
 * tooltip that lists every series at the pointed time.
 */
export function TimeSeriesChart({ name, series, from, to, format, fixedMax, binary, thresholds = [], gapMs, height = 160 }: TimeSeriesChartProps) {
  const times = [...new Set(series.flatMap((x) => x.points.filter((p) => p.v !== null).map((p) => p.t)))].sort((a, b) => a - b);
  const yMax = Math.max(1, ...series.flatMap((x) => x.points.map((p) => p.v ?? 0)), ...thresholds.map((t) => t.value));
  const latest = series.map((x) => {
    const v = latestOf(x.points);
    return `${x.name} ${v === null ? s.chart.noValue : format(v)}`;
  });
  const label = s.chart.label(name, latest.join("，"), `${formatTime(new Date(from).toISOString())} – ${formatTime(new Date(to).toISOString())}`);
  return (
    <div>
      <ChartFrame
        from={from}
        to={to}
        yMax={yMax * 1.05}
        fixedMax={fixedMax}
        binary={binary}
        format={format}
        times={times}
        height={height}
        label={label}
        tip={(i) => {
          const t = times[i] ?? 0;
          return (
            <>
              <div className="sub">{formatDateTime(new Date(t).toISOString())}</div>
              {series.map((x) => {
                const v = x.points.find((p) => p.t === t)?.v ?? null;
                return <TipRow key={x.name} color={x.color} dashed={x.dashed} name={x.name} value={v === null ? s.chart.noValue : format(v)} />;
              })}
            </>
          );
        }}
      >
        {thresholds.map((t) => (
          <ThresholdMark key={t.label + t.value} value={t.value} label={t.label} />
        ))}
        {series.map((x) => (
          <LineSeries key={x.name} points={x.points} color={x.color} dashed={x.dashed} gapMs={gapMs} />
        ))}
      </ChartFrame>
      {series.length > 1 && <ChartKey items={series.map((x) => ({ name: x.name, color: x.color, dashed: x.dashed }))} />}
    </div>
  );
}
