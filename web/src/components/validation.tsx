import type { Issue } from "../api/types";
import { s } from "../strings";

/** Server verdict of a validation call: issues, warnings and the all-clear. */
export function ValidationReport({ valid, issues, warnings }: { valid: boolean; issues: readonly Issue[]; warnings: readonly Issue[] }) {
  return (
    <div className="stack" aria-live="polite">
      <div className={`banner ${valid ? "ok" : "error"}`}>
        {valid ? s.services.valid : s.services.invalid}
        {issues.length > 0 && <IssueList issues={issues} />}
      </div>
      {warnings.length > 0 && (
        <div className="banner warn">
          {s.common.warnings}
          <IssueList issues={warnings} />
        </div>
      )}
    </div>
  );
}

export function IssueList({ issues }: { issues: readonly Issue[] }) {
  return (
    <ul>
      {issues.map((i, n) => (
        <li key={`${i.path}-${n}`}>
          {i.path && <code>{i.path}</code>}
          {i.path ? ": " : ""}
          {i.message}
        </li>
      ))}
    </ul>
  );
}
