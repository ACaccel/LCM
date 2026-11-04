import type { MachineSnapshot } from "../../../api/types";
import { formatDateTime } from "../../../lib/format";
import { s } from "../../../strings";

/** One plotted sample; a null value is missing data and is left empty. */
export interface UsagePoint {
  id: number;
  observedAt: string;
  cpu: number | null;
  memory: number | null;
}

/** Independent points preserve missing data and gaps without inventing interpolation. */
export function UsageTrend({ points: all }: { points: UsagePoint[] }) {
  const points = all.slice(-100);
  const first = points.at(0);
  const last = points.at(-1);
  if (!first || !last) return null;
  const from = Date.parse(first.observedAt);
  const to = Date.parse(last.observedAt);
  const valid = (v: number | null): v is number => v !== null && Number.isFinite(v) && v >= 0 && v <= 100;
  return (
    <figure className="stack">
      <figcaption>{s.machine.trendNote}</figcaption>
      <svg viewBox="0 0 600 150" role="img" aria-label={s.machine.trend} style={{ width: "100%", maxHeight: 220 }}>
        <title>{s.machine.trend}</title>
        <path d="M35 10V130H585" fill="none" stroke="var(--muted-foreground)" />
        <text x="0" y="18" fill="currentColor" fontSize="12">
          100%
        </text>
        <text x="10" y="130" fill="currentColor" fontSize="12">
          0%
        </text>
        {points.map((p) => {
          const at = Date.parse(p.observedAt);
          if (!Number.isFinite(at)) return null;
          const x = to > from ? 40 + ((at - from) / (to - from)) * 535 : 300;
          return (
            <g key={p.id}>
              {valid(p.cpu) && (
                <circle cx={x} cy={125 - p.cpu * 1.1} r="3" fill="var(--primary)">
                  <title>
                    {formatDateTime(p.observedAt)} — {s.machine.cpu}: {p.cpu.toFixed(1)}%
                  </title>
                </circle>
              )}
              {valid(p.memory) && (
                <path d={`M${x - 3},${125 - p.memory * 1.1 - 3}l6,6m0,-6l-6,6`} fill="none" stroke="var(--warning)" strokeWidth="2">
                  <title>
                    {formatDateTime(p.observedAt)} — {s.machine.memory}: {p.memory.toFixed(1)}%
                  </title>
                </path>
              )}
            </g>
          );
        })}
      </svg>
      <p className="meta">
        ● {s.machine.cpu} / × {s.machine.memory} — {formatDateTime(first.observedAt)} – {formatDateTime(last.observedAt)}
      </p>
    </figure>
  );
}

export function MachineTrend({ snapshots }: { snapshots: MachineSnapshot[] }) {
  return (
    <UsageTrend
      points={snapshots.map((p) => ({
        id: p.id,
        observedAt: p.observedAt,
        cpu: p.cpuPercent,
        memory: p.memoryTotal > 0 && p.memoryAvailable >= 0 && p.memoryAvailable <= p.memoryTotal ? ((p.memoryTotal - p.memoryAvailable) / p.memoryTotal) * 100 : null,
      }))}
    />
  );
}
