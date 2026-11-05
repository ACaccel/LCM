import { Link } from "react-router";
import type { TimelineEntry } from "../api/types";
import { formatDateTime, formatDuration, formatTime } from "../lib/format";
import { timelineDot } from "../lib/status";
import { translate } from "../lib/messages";
import { OBSERVATION_KIND } from "../lib/timeline";
import { s } from "../strings";

/** Entries that come from signals nest the signal's own data one level down. */
function pick(d: Record<string, unknown>, key: string): unknown {
  if (key in d) return d[key];
  const inner = d["data"];
  return typeof inner === "object" && inner !== null ? (inner as Record<string, unknown>)[key] : undefined;
}

function observationDetail(d: Record<string, unknown>): string {
  const source = String(d["source"] ?? "");
  const parts = [s.observation.eventSources[source] ?? source, d["historical"] ? s.machine.historical : s.machine.live];
  if (d["gap"]) parts.push(s.machine.gap);
  if (typeof d["unit"] === "string" && d["unit"]) parts.push(d["unit"]);
  return parts.filter(Boolean).join(" · ");
}

function detailFor(e: TimelineEntry): string {
  const d = e.data ?? {};
  if (e.kind === OBSERVATION_KIND) return observationDetail(d);
  const parts: string[] = [];
  const actor = pick(d, "actor");
  const outcome = pick(d, "outcome");
  const ms = pick(d, "durationMs");
  if (typeof actor === "string") parts.push(actor);
  if (typeof outcome === "string") parts.push(outcome in s.deployments.outcomes ? s.deployments.outcomes[outcome as keyof typeof s.deployments.outcomes] : outcome);
  if (typeof ms === "number") parts.push(formatDuration(ms));
  return parts.join(" · ");
}

/** Timeline rows in the style of the approved mockup. */
export function TimelineEvents({ entries, showResource }: { entries: readonly TimelineEntry[]; showResource?: boolean }) {
  return (
    <div className="events" role="list">
      {entries.map((e, i) => {
        const dot = timelineDot(e);
        const detail = detailFor(e);
        return (
          <div className="ev" role="listitem" key={`${e.time}-${e.kind}-${e.ref}-${i}`}>
            <time dateTime={e.time} title={formatDateTime(e.time)}>
              {formatTime(e.time)}
            </time>
            <div className={`dot d-${dot}`} aria-hidden="true" />
            <div>
              <span className="kind">{s.timeline.kindNames[e.kind] ?? e.kind}</span>
              {showResource && e.resource && (
                <>
                  <Link to={`/resources/${encodeURIComponent(e.resource)}`}>{e.resource}</Link>{" "}
                </>
              )}
              {translate(e.summaryMessage)}
              {detail && <small>{detail}</small>}
            </div>
          </div>
        );
      })}
    </div>
  );
}
