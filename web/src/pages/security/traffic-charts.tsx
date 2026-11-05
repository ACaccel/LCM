import type { TrafficPoint, TrafficSeries } from "../../api/security";
import { BarSeries, ChartFrame, ChartKey, TimeSeriesChart, TipRow, fmtBytes, fmtCount } from "../../components/chart";
import { formatDateTime, formatTime } from "../../lib/format";
import { s, sec } from "../../strings";

const NORMAL = "var(--chart-1)";
const THROTTLED = "var(--chart-3)";
const SERVER_ERROR = "var(--chart-6)";

/** Requests of a bucket split by outcome; a 429 or 5xx is not counted twice as normal. */
export function requestSplit(p: Pick<TrafficPoint, "requests" | "status429" | "status5xx">): { normal: number; status429: number; status5xx: number } {
  return { normal: Math.max(0, p.requests - p.status429 - p.status5xx), status429: p.status429, status5xx: p.status5xx };
}

const rangeText = (from: number, to: number) => `${formatTime(new Date(from).toISOString())} – ${formatTime(new Date(to).toISOString())}`;

/**
 * Two charts instead of one per metric: requests stacked by outcome, and the
 * request and response bytes as lines. Buckets without observations stay
 * empty; they are not zero traffic.
 */
export function TrafficCharts({ data }: { data: TrafficSeries }) {
  const items = [...data.items].filter((p) => Number.isFinite(Date.parse(p.windowStart))).sort((a, b) => Date.parse(a.windowStart) - Date.parse(b.windowStart));
  const from = Date.parse(data.from);
  const to = Date.parse(data.to);
  const bucketMs = data.bucketSeconds * 1000;
  // Samples sit at the middle of their bucket so bars centre on it.
  const times = items.map((p) => Date.parse(p.windowStart) + bucketMs / 2);
  const splits = items.map(requestSplit);
  const maxRequests = Math.max(1, ...items.map((p) => p.requests));
  const last = items.at(-1);
  const bucketRange = (p: TrafficPoint) => `${formatDateTime(p.windowStart)} – ${formatTime(new Date(Date.parse(p.windowStart) + bucketMs).toISOString())}`;
  const bytes = (key: "requestBytes" | "responseBytes") => items.map((p, i) => ({ t: times[i] ?? 0, v: p[key] }));
  return (
    <div className="stack">
      <section className="panel stack">
        <h3>{s.chart.traffic.requestsTitle}</h3>
        <ChartFrame
          from={from}
          to={to}
          yMax={maxRequests * 1.05}
          format={fmtCount}
          times={times}
          label={s.chart.label(s.chart.traffic.requestsTitle, `${sec.requests} ${last ? fmtCount(last.requests) : s.chart.noValue}`, rangeText(from, to))}
          tip={(i) => {
            const p = items[i];
            const sp = splits[i];
            if (!p || !sp) return null;
            return (
              <>
                <div className="sub">{bucketRange(p)}</div>
                <TipRow color={NORMAL} name={s.chart.traffic.normal} value={fmtCount(sp.normal)} />
                <TipRow color={THROTTLED} name={sec.status429} value={fmtCount(sp.status429)} />
                <TipRow color={SERVER_ERROR} name={sec.status5xx} value={fmtCount(sp.status5xx)} />
              </>
            );
          }}
        >
          <BarSeries
            times={times}
            stacked
            barMs={bucketMs}
            series={[
              { name: s.chart.traffic.normal, color: NORMAL, values: splits.map((x) => x.normal) },
              { name: sec.status429, color: THROTTLED, values: splits.map((x) => x.status429) },
              { name: sec.status5xx, color: SERVER_ERROR, values: splits.map((x) => x.status5xx) },
            ]}
          />
        </ChartFrame>
        <ChartKey
          items={[
            { name: s.chart.traffic.normal, color: NORMAL, swatch: true },
            { name: sec.status429, color: THROTTLED, swatch: true },
            { name: sec.status5xx, color: SERVER_ERROR, swatch: true },
          ]}
        />
      </section>
      <section className="panel stack">
        <h3>{s.chart.traffic.bytesTitle}</h3>
        <TimeSeriesChart
          name={s.chart.traffic.bytesTitle}
          from={from}
          to={to}
          format={fmtBytes}
          binary
          gapMs={bucketMs * 1.5}
          series={[
            { name: sec.requestBytes, color: "var(--chart-1)", points: bytes("requestBytes") },
            { name: sec.responseBytes, color: "var(--chart-2)", dashed: true, points: bytes("responseBytes") },
          ]}
        />
      </section>
    </div>
  );
}
