import { createContext, useContext } from "react";
import type { NiceScale } from "./scale";

export interface Plot {
  left: number;
  top: number;
  right: number;
  bottom: number;
  width: number;
  height: number;
}

/** What series and marks need to place themselves; provided by ChartFrame. */
export interface ChartScale {
  from: number;
  to: number;
  domain: NiceScale;
  plot: Plot;
  /** Pixel x of a time. */
  x: (t: number) => number;
  /** Pixel y of a value; values outside the domain are clamped to the plot. */
  y: (v: number) => number;
  /** Time of the sample the tooltip points at, if any. */
  activeT: number | null;
  /** The formatter of the value axis. */
  format: (v: number) => string;
}

export const ScaleContext = createContext<ChartScale | null>(null);

export function useChartScale(): ChartScale {
  const c = useContext(ScaleContext);
  if (!c) throw new Error("chart parts must be rendered inside ChartFrame");
  return c;
}
