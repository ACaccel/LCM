import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { trafficSeriesQuery } from "../../api/security";
import type { Category, TrafficPoint, TrafficSeries } from "../../api/security";
import { Async, Empty, Field } from "../../components/ui";
import { sec, secLabels } from "../../strings";
import { MachineSelect, Metric, categories, number, pickOption, showTime } from "./shared";
import { TrafficCharts } from "./traffic-charts";

const WINDOWS = {
  "1h": { ms: 3_600_000, bucket: 60, label: sec.oneHour },
  "24h": { ms: 86_400_000, bucket: 300, label: sec.day },
  "7d": { ms: 604_800_000, bucket: 900, label: sec.week },
} as const;
type WindowKey = keyof typeof WINDOWS;
const WINDOW_KEYS = Object.keys(WINDOWS) as WindowKey[];

const minuteNow = () => Math.floor(Date.now() / 60_000) * 60_000;

export function trafficTotals(items: readonly TrafficPoint[]) {
  const total = items.reduce(
    (sum, item) => ({
      requests: sum.requests + item.requests,
      status429: sum.status429 + item.status429,
      status5xx: sum.status5xx + item.status5xx,
      dryRunHits: sum.dryRunHits + item.dryRunHits,
      requestTimeSumMs: sum.requestTimeSumMs + item.requestTimeSumMs,
      requestTimeMaxMs: Math.max(sum.requestTimeMaxMs, item.requestTimeMaxMs),
      requestBytes: sum.requestBytes + item.requestBytes,
      responseBytes: sum.responseBytes + item.responseBytes,
    }),
    { requests: 0, status429: 0, status5xx: 0, dryRunHits: 0, requestTimeSumMs: 0, requestTimeMaxMs: 0, requestBytes: 0, responseBytes: 0 },
  );
  return { ...total, averageMs: total.requests > 0 ? total.requestTimeSumMs / total.requests : null };
}

export function SecurityTraffic({ machineId }: { machineId: string }) {
  const [windowKey, setWindowKey] = useState<WindowKey>("24h");
  const [category, setCategory] = useState<Category | undefined>();
  const [service, setService] = useState("");
  const [to, setTo] = useState(minuteNow);
  // The range ends at the current minute and moves on as time passes.
  useEffect(() => {
    const timer = setInterval(() => setTo(minuteNow()), 60_000);
    return () => clearInterval(timer);
  }, []);
  const range = WINDOWS[windowKey];
  const q = useQuery(
    trafficSeriesQuery(
      {
        machineId: machineId || undefined,
        category,
        service: service || undefined,
        from: new Date(to - range.ms).toISOString(),
        to: new Date(to).toISOString(),
      },
      range.bucket,
    ),
  );
  function pickWindow(value: WindowKey) {
    setWindowKey(value);
    setTo(minuteNow());
  }
  return (
    <div className="stack">
      <div className="filters">
        <MachineSelect />
        <Field label={sec.window} htmlFor="traffic-window">
          <select id="traffic-window" value={windowKey} onChange={(e) => pickWindow(pickOption(WINDOW_KEYS, e.target.value) ?? "24h")}>
            {WINDOW_KEYS.map((value) => (
              <option key={value} value={value}>
                {WINDOWS[value].label}
              </option>
            ))}
          </select>
        </Field>
        <Field label={sec.category} htmlFor="traffic-category">
          <select id="traffic-category" value={category ?? ""} onChange={(e) => setCategory(pickOption(categories, e.target.value))}>
            <option value="">{sec.all}</option>
            {categories.map((value) => (
              <option key={value} value={value}>
                {secLabels.category[value]}
              </option>
            ))}
          </select>
        </Field>
        <Field label={sec.service} htmlFor="traffic-service">
          <input
            id="traffic-service"
            type="search"
            value={service}
            placeholder={sec.all}
            list="traffic-services"
            onChange={(e) => setService(e.target.value)}
          />
          <datalist id="traffic-services">
            <option value="__unassigned__">{sec.unassigned}</option>
          </datalist>
        </Field>
      </div>
      <p className="meta">{sec.gaps}</p>
      <Async q={q}>{(data) => <TrafficDisplay data={data} />}</Async>
    </div>
  );
}

const FIELDS = [
  [sec.requests, "requests"],
  [sec.status429, "status429"],
  [sec.status5xx, "status5xx"],
  [sec.dryRun, "dryRunHits"],
  [sec.average, "averageMs"],
  [sec.maximum, "requestTimeMaxMs"],
  [sec.requestBytes, "requestBytes"],
  [sec.responseBytes, "responseBytes"],
] as const;

export function TrafficDisplay({ data }: { data: TrafficSeries }) {
  if (!data.items.length) return <Empty>{sec.noTraffic}</Empty>;
  const total = trafficTotals(data.items);
  return (
    <div className="stack">
      <p className="meta">
        {showTime(data.from)} ～ {showTime(data.to)}
      </p>
      <div className="security-metrics">
        {FIELDS.map(([label, key]) => (
          <Metric key={key} label={label} value={number(total[key])} />
        ))}
      </div>
      <TrafficCharts data={data} />
      <details>
        <summary>{sec.chartTable}</summary>
        <div className="table-wrap">
          <table className="table">
            <thead>
              <tr>
                <th>{sec.from}</th>
                {FIELDS.map(([label, key]) => (
                  <th key={key} className="num">
                    {label}
                  </th>
                ))}
                <th className="num">{sec.rate}</th>
                <th className="num">{sec.observedWindows}</th>
                <th className="num">{sec.observedSeconds}</th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((item) => (
                <tr key={item.windowStart}>
                  <td>{showTime(item.windowStart)}</td>
                  {FIELDS.map(([, key]) => (
                    <td className="num" key={key}>
                      {number(key === "averageMs" && item.requests === 0 ? null : item[key])}
                    </td>
                  ))}
                  <td className="num">{number(item.requestsPerSecond)}</td>
                  <td className="num">{number(item.observedWindows)}</td>
                  <td className="num">{number(item.observedSeconds)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
      <p className="meta">{sec.rateHelp}</p>
    </div>
  );
}
