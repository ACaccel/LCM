import { s } from "../../strings";

export type UsageTone = "ok" | "warn" | "bad" | "neutral";

/**
 * Tone of a usage value against its limits: at or above `critAt` is bad, at
 * or above `warnAt` is a warning. Without limits the bar stays neutral.
 */
export function usageTone(value: number, limits: { warnAt?: number; critAt?: number }): UsageTone {
  const { warnAt, critAt } = limits;
  if (critAt !== undefined && value >= critAt) return "bad";
  if (warnAt !== undefined && value >= warnAt) return "warn";
  return critAt === undefined && warnAt === undefined ? "neutral" : "ok";
}

/** Warning starts 10 points below the limit, so a value close to it is visible before it trips. */
export function limitsFor(threshold: number | undefined): { warnAt?: number; critAt?: number } {
  return threshold === undefined ? {} : { warnAt: Math.max(0, threshold - 10), critAt: threshold };
}

const TONE_ICON: Record<UsageTone, string> = { ok: "", warn: "▲", bad: "✕", neutral: "" };

/**
 * A horizontal usage bar with a marker at the limit. The value is always
 * written next to it, and a warning or breach adds a symbol, so the colour is
 * never the only signal. A null value shows as unknown.
 */
export function UsageBar({ value, label, max = 100, threshold, text, tone: forced }: { value: number | null; label: string; max?: number; threshold?: number; text?: string; tone?: UsageTone }) {
  if (value === null || !Number.isFinite(value)) {
    return <span className="usage-unknown">{text ?? s.machine.unknown}</span>;
  }
  const clamped = Math.min(max, Math.max(0, value));
  const tone = forced ?? usageTone(value, limitsFor(threshold));
  const shown = text ?? `${value.toFixed(1)}%`;
  const icon = TONE_ICON[tone];
  return (
    <span className="usage">
      <span
        className="usage-track"
        role="meter"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={max}
        aria-valuenow={Number(clamped.toFixed(2))}
        aria-valuetext={threshold === undefined ? shown : `${shown}，${s.chart.thresholdAt(threshold)}`}
      >
        <span className={`usage-fill ${tone}`} style={{ width: `${(clamped / max) * 100}%` }} />
        {threshold !== undefined && threshold <= max && <span className="usage-mark" style={{ left: `${(threshold / max) * 100}%` }} title={s.chart.thresholdAt(threshold)} />}
      </span>
      <span className="usage-text">
        {icon && <span aria-hidden="true">{icon} </span>}
        {shown}
      </span>
    </span>
  );
}
