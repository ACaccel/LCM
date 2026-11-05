import type { ReactNode } from "react";

/**
 * The floating value box of a chart. It sits next to the cursor line and
 * flips to the left side in the right part of the chart. Announced politely
 * so keyboard navigation is read out.
 */
export function ChartTooltip({ x, width, children }: { x: number; width: number; children: ReactNode }) {
  return (
    <div className={`chart-tip${x > width * 0.6 ? " left" : ""}`} style={{ left: x }} role="status">
      {children}
    </div>
  );
}

/** One line of a tooltip: a colour key, a name and a value. */
export function TipRow({ color, dashed, name, value }: { color: string; dashed?: boolean; name: string; value: string }) {
  return (
    <div className="tip-row">
      <i className={`tip-key${dashed ? " dashed" : ""}`} style={{ ["--key" as string]: color }} aria-hidden="true" />
      <span>{name}</span>
      <strong>{value}</strong>
    </div>
  );
}
