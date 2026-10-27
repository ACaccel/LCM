import { TimelinePanel } from "../components/timeline-panel";
import { PageHead } from "../components/ui";
import { s } from "../strings";

export function TimelinePage() {
  return (
    <div className="page">
      <PageHead title={s.timeline.title} />
      <TimelinePanel withServiceFilter />
    </div>
  );
}
