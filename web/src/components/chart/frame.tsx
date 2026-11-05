import { useCallback, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, PointerEvent, ReactNode } from "react";
import { s } from "../../strings";
import { ScaleContext } from "./context";
import type { ChartScale, Plot } from "./context";
import { fixedScale, nearestIndex, niceScale, stepIndex } from "./scale";
import { TimeAxis, ValueAxis } from "./axes";
import { ChartTooltip } from "./tooltip";

const FALLBACK_WIDTH = 640;
const AXIS_FONT = 11;

/** Width of an element in pixels, following resizes; a fixed width where layout is not available. */
function useWidth(ref: React.RefObject<HTMLElement | null>): number {
  const [width, setWidth] = useState(FALLBACK_WIDTH);
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const w = el.getBoundingClientRect().width;
      if (w > 0) setWidth(Math.round(w));
    };
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return width;
}

export interface ChartFrameProps {
  from: number;
  to: number;
  /** Largest value in the data; the axis is scaled to cover it. Ignored with `fixedMax`. */
  yMax: number;
  yMin?: number;
  /** Fixes the top of the axis, such as 100 for percentages. */
  fixedMax?: number;
  /** Ticks of the value axis are round in KiB, MiB and so on. */
  binary?: boolean;
  /** Unit formatting of axis labels and tooltips. */
  format: (v: number) => string;
  /** Ascending sample times the tooltip can point at. */
  times?: readonly number[];
  /** Tooltip content of the sample at an index of `times`. */
  tip?: (index: number) => ReactNode;
  /** Name, current value and range; read by screen readers. */
  label: string;
  height?: number;
  /** Draw the built-in axes; turn off to place ValueAxis and TimeAxis yourself. */
  axes?: boolean;
  /** Draw the time axis under the plot; off for charts whose x is not a time. */
  timeAxis?: boolean;
  /** Rendered under the plot; `var(--plot-left)` is the left edge of the plot, for aligning with it. */
  footer?: ReactNode;
  children: ReactNode;
}

/**
 * Frame of a time-series chart: measures its width, derives the scales and
 * axes, and owns the pointer and keyboard interaction that drives the
 * tooltip. Series and marks are children and read the scales from context.
 */
export function ChartFrame({ from, to, yMax, yMin = 0, fixedMax, binary, format, times = [], tip, label, height = 170, axes = true, timeAxis = true, footer, children }: ChartFrameProps) {
  const ref = useRef<HTMLDivElement>(null);
  const width = useWidth(ref);
  const [active, setActive] = useState<number | null>(null);

  const domain = useMemo(() => (fixedMax !== undefined ? fixedScale(yMin, fixedMax) : niceScale(yMin, yMax, Math.max(2, Math.round(height / 45)), binary)), [fixedMax, yMin, yMax, height, binary]);
  const longest = Math.max(...domain.ticks.map((t) => format(t).length), 2);
  const left = Math.min(84, Math.max(34, Math.ceil(longest * (AXIS_FONT * 0.62) + 10)));
  const plot = useMemo<Plot>(
    () => ({ left, top: 10, right: width - 12, bottom: height - 28, width: Math.max(1, width - 12 - left), height: Math.max(1, height - 38) }),
    [left, width, height],
  );

  const scale = useMemo<ChartScale>(() => {
    const span = Math.max(1, to - from);
    const range = Math.max(1e-9, domain.max - domain.min);
    return {
      from,
      to,
      domain,
      plot,
      x: (t) => plot.left + ((t - from) / span) * plot.width,
      y: (v) => plot.bottom - ((Math.min(domain.max, Math.max(domain.min, v)) - domain.min) / range) * plot.height,
      activeT: active === null ? null : (times[active] ?? null),
      format,
    };
  }, [from, to, domain, plot, active, times, format]);

  const interactive = !!tip && times.length > 0;
  const activeIndex = active !== null && active < times.length ? active : null;

  const pick = useCallback(
    (e: PointerEvent<HTMLDivElement>) => {
      if (!interactive) return;
      const r = e.currentTarget.getBoundingClientRect();
      const px = ((e.clientX - r.left) / Math.max(1, r.width)) * width;
      const t = from + ((px - left) / plot.width) * (to - from);
      const i = nearestIndex(times, t);
      setActive(i < 0 ? null : i);
    },
    [interactive, width, left, plot.width, from, to, times],
  );

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const next = stepIndex(activeIndex, e.key, times.length);
    if (next === undefined) return;
    e.preventDefault();
    setActive(next);
  };

  const tipX = scale.activeT === null ? 0 : scale.x(scale.activeT);

  return (
    <div
      ref={ref}
      className="chart-frame"
      style={{ ["--plot-left" as string]: `${left}px` }}
      role="group"
      aria-label={interactive ? `${label}。${s.chart.keyboardHelp}` : label}
      tabIndex={interactive ? 0 : undefined}
      onPointerMove={pick}
      onPointerDown={pick}
      onPointerLeave={() => setActive(null)}
      onFocus={() => interactive && setActive((i) => i ?? times.length - 1)}
      onBlur={() => setActive(null)}
      onKeyDown={onKey}
    >
      <svg className="chart" width={width} height={height} viewBox={`0 0 ${width} ${height}`} aria-hidden="true">
        <ScaleContext.Provider value={scale}>
          {axes && <ValueAxis />}
          {axes && timeAxis && <TimeAxis />}
          {children}
          {scale.activeT !== null && <line className="chart-cursor" x1={tipX} x2={tipX} y1={plot.top} y2={plot.bottom} />}
        </ScaleContext.Provider>
      </svg>
      {footer}
      {interactive && activeIndex !== null && (
        <ChartTooltip x={tipX} width={width}>
          {tip(activeIndex)}
        </ChartTooltip>
      )}
    </div>
  );
}
