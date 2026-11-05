import { s } from "../../strings";

export type RangeKey = "24h" | "7d" | "30d";

export const RANGE_MS: Record<RangeKey, number> = { "24h": 86_400_000, "7d": 7 * 86_400_000, "30d": 30 * 86_400_000 };

const KEYS: readonly RangeKey[] = ["24h", "7d", "30d"];

/** Segmented control for the time range of a chart. */
export function RangeSwitch({ value, onChange, label = s.chart.range }: { value: RangeKey; onChange: (value: RangeKey) => void; label?: string }) {
  return (
    <div className="range-switch" role="group" aria-label={label}>
      {KEYS.map((k) => (
        <button key={k} type="button" className="btn small" aria-pressed={value === k} onClick={() => onChange(k)}>
          {s.chart.ranges[k]}
        </button>
      ))}
    </div>
  );
}
