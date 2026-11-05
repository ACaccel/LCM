import { useState } from "react";
import type { MachineSnapshot } from "../../../api/types";
import { RANGE_MS, RangeSwitch, TimeSeriesChart, fmtPercent, windowSeries } from "../../../components/chart";
import type { RangeKey, XY } from "../../../components/chart";
import { s } from "../../../strings";

/** One plotted sample; a null value is missing data and is left empty. */
export interface UsagePoint {
  id: number;
  observedAt: string;
  cpu: number | null;
  memory: number | null;
}

/** Limits drawn as dashed lines; the server default is 90 for both. */
export interface UsageThresholds {
  cpu?: number;
  memory?: number;
}

const valid = (v: number | null): v is number => v !== null && Number.isFinite(v) && v >= 0 && v <= 100;

/** Dashed limit lines; equal limits are one line with a joint label. */
export function thresholdLines(t: UsageThresholds | undefined): { value: number; label: string }[] {
  if (!t) return [];
  const { cpu, memory } = t;
  if (cpu !== undefined && memory !== undefined && cpu === memory) return [{ value: cpu, label: `${s.machine.cpu}／${s.machine.memory} ${s.chart.thresholdAt(cpu)}` }];
  return [
    ...(cpu === undefined ? [] : [{ value: cpu, label: `${s.machine.cpu} ${s.chart.thresholdAt(cpu)}` }]),
    ...(memory === undefined ? [] : [{ value: memory, label: `${s.machine.memory} ${s.chart.thresholdAt(memory)}` }]),
  ];
}

/**
 * CPU and memory over time. Out-of-range values stay empty instead of being
 * plotted as zero, and gaps in observation break the lines. The range ends at
 * the newest loaded sample; with `range` given the switch is the caller's.
 */
export function UsageTrend({ points, thresholds, range: controlled }: { points: UsagePoint[]; thresholds?: UsageThresholds; range?: RangeKey }) {
  const [own, setOwn] = useState<RangeKey>("24h");
  const range = controlled ?? own;
  const at = (key: "cpu" | "memory"): XY[] => points.map((p) => ({ t: Date.parse(p.observedAt), v: valid(p[key]) ? p[key] : null }));
  const win = windowSeries([at("cpu"), at("memory")], RANGE_MS[range]);
  const cpu = win?.series[0];
  const memory = win?.series[1];
  if (!win || !cpu || !memory) return null;
  return (
    <figure className="stack">
      <div className="chart-title">
        <figcaption>{s.machine.trendNote}</figcaption>
        {controlled === undefined && <RangeSwitch value={range} onChange={setOwn} />}
      </div>
      <TimeSeriesChart
        name={s.machine.trend}
        from={win.from}
        to={win.to}
        format={fmtPercent}
        fixedMax={100}
        gapMs={win.gapMs}
        thresholds={thresholdLines(thresholds)}
        series={[
          { name: s.machine.cpu, color: "var(--chart-1)", points: cpu },
          { name: s.machine.memory, color: "var(--chart-3)", dashed: true, points: memory },
        ]}
      />
    </figure>
  );
}

export function MachineTrend({ snapshots, thresholds, range }: { snapshots: MachineSnapshot[]; thresholds?: UsageThresholds; range?: RangeKey }) {
  return (
    <UsageTrend
      thresholds={thresholds}
      range={range}
      points={snapshots.map((p) => ({
        id: p.id,
        observedAt: p.observedAt,
        cpu: p.cpuPercent,
        memory: p.memoryTotal > 0 && p.memoryAvailable >= 0 && p.memoryAvailable <= p.memoryTotal ? ((p.memoryTotal - p.memoryAvailable) / p.memoryTotal) * 100 : null,
      }))}
    />
  );
}
