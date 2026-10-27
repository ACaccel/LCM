import { useMemo } from "react";
import { collapseContext, diffLines, hasChanges } from "../lib/diff";
import { s } from "../strings";

const marks = { add: "+", del: "-", eq: " ", sep: "" } as const;

/** Unified line diff between two texts. */
export function DiffView({ before, after, context = 3 }: { before: string; after: string; context?: number }) {
  const lines = useMemo(() => diffLines(before, after), [before, after]);
  const view = useMemo(() => collapseContext(lines, context), [lines, context]);
  if (!hasChanges(lines)) return <div className="meta">{s.config.noChanges}</div>;
  return (
    <div className="diff" role="group" aria-label={s.config.compare}>
      {view.map((l, i) => (
        <div key={i} className={`l ${l.kind}`}>
          <span aria-hidden="true">{marks[l.kind]}</span>
          <span>
            {l.kind === "add" && <span className="sr-only">+ </span>}
            {l.kind === "del" && <span className="sr-only">- </span>}
            {l.text || " "}
          </span>
        </div>
      ))}
    </div>
  );
}
