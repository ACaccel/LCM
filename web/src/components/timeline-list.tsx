import { Link } from "react-router";
import type { TimelineEntry } from "../api/types";
import { formatDateTime, formatDuration, formatTime } from "../lib/format";
import { s } from "../strings";

type Dot = "ok" | "warn" | "bad" | "info" | "act";

/** Entries that come from signals nest the signal's own data one level down. */
function pick(d: Record<string, unknown>, key: string): unknown {
  if (key in d) return d[key];
  const inner = d["data"];
  return typeof inner === "object" && inner !== null ? (inner as Record<string, unknown>)[key] : undefined;
}

function dotFor(e: TimelineEntry): Dot {
  const d = e.data ?? {};
  switch (e.kind) {
    case "deployment":
    case "action":
    case "backup":
    case "job":
      return pick(d, "outcome") === "failed" ? "bad" : "act";
    case "health.changed": {
      const h = pick(d, "health");
      return h === "down" ? "bad" : h === "degraded" ? "warn" : h === "healthy" ? "ok" : "info";
    }
    case "check.changed": {
      const st = pick(d, "status");
      return st === "down" ? "bad" : st === "up" ? "ok" : "info";
    }
    case "alert": {
      const sev = pick(d, "severity");
      return sev === "critical" ? "bad" : sev === "warning" ? "warn" : "info";
    }
    case "lifecycle.changed":
      return pick(d, "state") === "failed" ? "bad" : "info";
    default:
      return "info";
  }
}

function detailFor(e: TimelineEntry): string {
  const d = e.data ?? {};
  const parts: string[] = [];
  const actor = pick(d, "actor");
  const outcome = pick(d, "outcome");
  const ms = pick(d, "durationMs");
  if (typeof actor === "string") parts.push(actor);
  if (typeof outcome === "string") parts.push(outcome);
  if (typeof ms === "number") parts.push(formatDuration(ms));
  return parts.join(" · ");
}

/** Timeline rows in the style of the approved mockup. */
export function TimelineEvents({ entries, showService }: { entries: readonly TimelineEntry[]; showService?: boolean }) {
  return (
    <div className="events" role="list">
      {entries.map((e, i) => {
        const dot = dotFor(e);
        const detail = detailFor(e);
        return (
          <div className="ev" role="listitem" key={`${e.time}-${e.kind}-${e.ref}-${i}`}>
            <time dateTime={e.time} title={formatDateTime(e.time)}>
              {formatTime(e.time)}
            </time>
            <div className={`dot d-${dot}`} aria-hidden="true" />
            <div>
              <span className="kind">{s.timeline.kindNames[e.kind] ?? e.kind}</span>
              {showService && e.service && (
                <>
                  <Link to={`/services/${encodeURIComponent(e.service)}`}>{e.service}</Link>{" "}
                </>
              )}
              {e.summary}
              {detail && <small>{detail}</small>}
            </div>
          </div>
        );
      })}
    </div>
  );
}
