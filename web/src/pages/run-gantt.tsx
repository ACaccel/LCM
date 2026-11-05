import type { RunNode } from "../api/types";
import { niceScale } from "../components/chart/scale";
import { formatDuration } from "../lib/format";
import { buildGantt } from "../lib/gantt";
import { nodeStatusView } from "../lib/status";
import { s } from "../strings";

/**
 * Where each started node spent its time, as bars on a shared elapsed-time
 * axis. Rows are buttons: selecting one opens the node's details. The status
 * is written in each row's name, not only coloured.
 */
export function RunGantt({ nodes, selected, onSelect, now }: { nodes: readonly RunNode[]; selected: string | null; onSelect: (id: string) => void; now: number }) {
  const { rows, total } = buildGantt(nodes, now);
  if (rows.length === 0) return <p className="meta">{s.chart.gantt.empty}</p>;
  const axis = niceScale(0, total, 4);
  const span = axis.max;
  const pct = (v: number) => `${(v / span) * 100}%`;
  return (
    <div className="gantt" role="group" aria-label={s.chart.gantt.title}>
      <div className="gantt-cols gantt-head" aria-hidden="true">
        <span />
        <span className="gantt-axis">
          {axis.ticks.map((t) => (
            <span key={t} style={{ left: pct(t) }}>
              {formatDuration(t)}
            </span>
          ))}
        </span>
      </div>
      {rows.map((r) => {
        const view = nodeStatusView(r.status);
        const duration = formatDuration(r.end - r.start);
        return (
          <button key={r.id} type="button" className="gantt-row gantt-cols" aria-pressed={selected === r.id} aria-label={`${r.id}，${view.label}，${duration}`} onClick={() => onSelect(r.id)}>
            <span className="gantt-name" title={r.id}>
              <span aria-hidden="true">{view.icon} </span>
              {r.id}
            </span>
            <span className="gantt-track">
              <span className={`gantt-bar ${view.tone}${r.open ? " open" : ""}`} style={{ left: pct(r.start), width: pct(Math.max(0, r.end - r.start)) }} title={`${view.label} · ${duration}`} />
              <span className="gantt-dur" style={r.end / span > 0.8 ? { right: `calc(100% - ${pct(r.start)})` } : { left: pct(r.end) }}>
                {duration}
              </span>
            </span>
          </button>
        );
      })}
    </div>
  );
}
