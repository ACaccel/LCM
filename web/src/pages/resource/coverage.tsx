import { Pill } from "../../components/ui";
import type { StatusView } from "../../lib/status";
import { s } from "../../strings";

export interface CoverageRow {
  key: string;
  label: string;
  optional?: boolean;
  view: StatusView;
  available: boolean;
  reason: string;
}

function CoverageTable({ rows, source }: { rows: readonly CoverageRow[]; source: string }) {
  return (
    <div className="table-wrap">
      <table className="table">
        <thead>
          <tr>
            <th>{source}</th>
            <th>{s.common.status}</th>
            <th>{s.machine.reason}</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.key}>
              <td>
                {r.label}
                {r.optional && <span className="meta"> ({s.common.optional})</span>}
              </td>
              <td>
                <Pill view={r.view} />
              </td>
              <td>{r.reason || s.common.none}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

/**
 * How many data sources answered, with the ones that did not listed at once
 * and the complete list one click away.
 */
export function CoverageSummary({ rows, source }: { rows: readonly CoverageRow[]; source: string }) {
  const problems = rows.filter((r) => !r.available);
  return (
    <div className="stack">
      <p>
        <b>{s.machine.coverageSummary(rows.length - problems.length, rows.length)}</b>
      </p>
      {problems.length > 0 ? <CoverageTable rows={problems} source={source} /> : <p className="meta">{s.machine.coverageAllOk}</p>}
      {rows.length > 0 && (
        <details>
          <summary>{s.machine.coverageAll}</summary>
          <CoverageTable rows={rows} source={source} />
        </details>
      )}
    </div>
  );
}
