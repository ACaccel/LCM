import { useMemo } from "react";
import { useChartScale } from "./context";
import { timeTicks } from "./scale";

/** Horizontal grid lines with the value labels at the left, from the frame's value scale. */
export function ValueAxis() {
  const { domain, plot, y, format } = useChartScale();
  return (
    <g className="chart-axis-y">
      {domain.ticks.map((v) => (
        <g key={v}>
          <line className={v === domain.min ? "chart-baseline" : "chart-grid"} x1={plot.left} x2={plot.right} y1={y(v)} y2={y(v)} />
          <text className="chart-tick" x={plot.left - 6} y={y(v)} textAnchor="end" dominantBaseline="middle">
            {format(v)}
          </text>
        </g>
      ))}
    </g>
  );
}

/**
 * Labels for charts whose x positions are item numbers 0..n-1 (set `from` to
 * -0.5 and `to` to n-0.5). At most `max` evenly spread labels are drawn.
 */
export function CategoryAxis({ labels, max = 5 }: { labels: readonly string[]; max?: number }) {
  const { plot, x } = useChartScale();
  const n = labels.length;
  const shown = n <= max ? labels.map((_, i) => i) : Array.from({ length: max }, (_, k) => Math.round((k * (n - 1)) / (max - 1)));
  return (
    <g className="chart-axis-x">
      {shown.map((i, k) => {
        const px = x(i);
        const anchor = k === 0 ? "start" : k === shown.length - 1 ? "end" : "middle";
        return (
          <text key={i} className="chart-tick" x={anchor === "start" ? Math.max(plot.left, px - 4) : px} y={plot.bottom + 14} textAnchor={anchor}>
            {labels[i]}
          </text>
        );
      })}
    </g>
  );
}

/** Time labels below the plot: 3 to 6 ticks, with the date where the range crosses days. */
export function TimeAxis() {
  const { from, to, plot, x } = useChartScale();
  const ticks = useMemo(() => timeTicks(from, to, Math.max(3, Math.min(6, Math.floor(plot.width / 78)))), [from, to, plot.width]);
  return (
    <g className="chart-axis-x">
      {ticks.map((tick) => {
        const px = x(tick.t);
        // Labels at the edges grow inwards so they are not cut off.
        const anchor = px - plot.left < 20 ? "start" : plot.right - px < 20 ? "end" : "middle";
        const lines = [tick.time, tick.date].filter((l): l is string => !!l);
        return (
          <g key={tick.t}>
            <line className="chart-tick-mark" x1={px} x2={px} y1={plot.bottom} y2={plot.bottom + 3} />
            {lines.map((text, i) => (
              <text key={text} className="chart-tick" x={px} y={plot.bottom + 14 + i * 12} textAnchor={anchor}>
                {text}
              </text>
            ))}
          </g>
        );
      })}
    </g>
  );
}
