import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { trafficSeriesQuery } from "../../api/security";
import type { Category, TrafficPoint, TrafficSeries } from "../../api/security";
import { Async, Empty, Field } from "../../components/ui";
import { MachineSelect, Metric, categories, number, showTime } from "./shared";
import { sec } from "./strings";

const windows = { "1h": { ms: 3_600_000, bucket: 60, label: sec.oneHour }, "24h": { ms: 86_400_000, bucket: 300, label: sec.day }, "7d": { ms: 604_800_000, bucket: 900, label: sec.week } } as const;
type Window = keyof typeof windows;
export function trafficTotals(items: readonly TrafficPoint[]) {
  const total = items.reduce((sum, item) => ({ requests: sum.requests + item.requests, status429: sum.status429 + item.status429, status5xx: sum.status5xx + item.status5xx, dryRunHits: sum.dryRunHits + item.dryRunHits, requestTimeSumMs: sum.requestTimeSumMs + item.requestTimeSumMs, requestTimeMaxMs: Math.max(sum.requestTimeMaxMs, item.requestTimeMaxMs), requestBytes: sum.requestBytes + item.requestBytes, responseBytes: sum.responseBytes + item.responseBytes }), { requests: 0, status429: 0, status5xx: 0, dryRunHits: 0, requestTimeSumMs: 0, requestTimeMaxMs: 0, requestBytes: 0, responseBytes: 0 });
  return { ...total, averageMs: total.requests > 0 ? total.requestTimeSumMs / total.requests : null };
}
/** Missing buckets split lines rather than implying zero or interpolating gaps. */
export function trafficSegments(items: readonly TrafficPoint[], bucketSeconds: number): TrafficPoint[][] {
  const segments: TrafficPoint[][] = []; let current: TrafficPoint[] = [];
  for (const item of [...items].sort((a, b) => Date.parse(a.windowStart) - Date.parse(b.windowStart))) {
    const previous = current.at(-1);
    if (previous && Date.parse(item.windowStart) - Date.parse(previous.windowStart) > bucketSeconds * 1000 + 1) { segments.push(current); current = []; }
    current.push(item);
  }
  if (current.length) segments.push(current);
  return segments;
}
export function SecurityTraffic({ machineId }: { machineId: string }) {
  const [window, setWindow] = useState<Window>("24h"); const [category, setCategory] = useState<Category | "">(""); const [service, setService] = useState("");
  const [to, setTo] = useState(() => Math.floor(Date.now() / 60_000) * 60_000);
  useEffect(() => { const timer = setInterval(() => setTo(Math.floor(Date.now() / 60_000) * 60_000), 60_000); return () => clearInterval(timer); }, []);
  const range = windows[window];
  const q = useQuery(trafficSeriesQuery({ machineId: machineId || undefined, category: category || undefined, service: service || undefined, from: new Date(to - range.ms).toISOString(), to: new Date(to).toISOString() }, range.bucket));
  function pickWindow(value: Window) { setWindow(value); setTo(Math.floor(Date.now() / 60_000) * 60_000); }
  return <div className="stack"><div className="filters">
    <MachineSelect />
    <Field label={sec.window} htmlFor="traffic-window"><select id="traffic-window" value={window} onChange={(e) => pickWindow(e.target.value as Window)}>{Object.entries(windows).map(([value, item]) => <option key={value} value={value}>{item.label}</option>)}</select></Field>
    <Field label={sec.category} htmlFor="traffic-category"><select id="traffic-category" value={category} onChange={(e) => setCategory(e.target.value as Category | "")}><option value="">{sec.all}</option>{categories.map((value) => <option key={value}>{value}</option>)}</select></Field>
    <Field label={sec.service} htmlFor="traffic-service"><input id="traffic-service" type="search" value={service} placeholder={sec.all} list="traffic-services" onChange={(e) => setService(e.target.value)} /><datalist id="traffic-services"><option value="__unassigned__">{sec.unassigned}</option></datalist></Field>
  </div><p className="meta">{sec.gaps}</p><Async q={q}>{(data) => <TrafficDisplay data={data} />}</Async></div>;
}
export function TrafficDisplay({ data }: { data: TrafficSeries }) {
  if (!data.items.length) return <Empty>{sec.noTraffic}</Empty>;
  const total = trafficTotals(data.items);
  const fields = [ [sec.requests, "requests"], [sec.status429, "status429"], [sec.status5xx, "status5xx"], [sec.dryRun, "dryRunHits"], [sec.average, "averageMs"], [sec.maximum, "requestTimeMaxMs"], [sec.requestBytes, "requestBytes"], [sec.responseBytes, "responseBytes"] ] as const;
  return <div className="stack"><p className="meta">{showTime(data.from)} ～ {showTime(data.to)}</p><div className="security-metrics">{fields.map(([label, key]) => <Metric key={key} label={label} value={number(total[key])} />)}</div>
    <div className="security-chart-grid"><TrafficChart data={data} field="requests" label={sec.requests} /><TrafficChart data={data} field="requestBytes" label={sec.requestBytes} /><TrafficChart data={data} field="responseBytes" label={sec.responseBytes} /><TrafficChart data={data} field="requestsPerSecond" label={sec.rate} /><TrafficChart data={data} field="status429" label={sec.status429} /><TrafficChart data={data} field="status5xx" label={sec.status5xx} /><TrafficChart data={data} field="dryRunHits" label={sec.dryRun} /><TrafficChart data={data} field="averageMs" label={sec.average} /><TrafficChart data={data} field="requestTimeMaxMs" label={sec.maximum} /></div>
    <details><summary>{sec.chartTable}</summary><div className="table-wrap"><table className="table"><thead><tr><th>{sec.from}</th>{fields.map(([label, key]) => <th key={key} className="num">{label}</th>)}<th className="num">{sec.rate}</th><th className="num">{sec.observedWindows}</th><th className="num">{sec.observedSeconds}</th></tr></thead><tbody>{data.items.map((item) => <tr key={item.windowStart}><td>{showTime(item.windowStart)}</td>{fields.map(([, key]) => <td className="num" key={key}>{number(key === "averageMs" && item.requests === 0 ? null : item[key])}</td>)}<td className="num">{number(item.requestsPerSecond)}</td><td className="num">{number(item.observedWindows)}</td><td className="num">{number(item.observedSeconds)}</td></tr>)}</tbody></table></div></details>
    <p className="meta">{sec.rateHelp}</p>
  </div>;
}
type MetricField = "requests" | "requestBytes" | "responseBytes" | "requestsPerSecond" | "status429" | "status5xx" | "dryRunHits" | "averageMs" | "requestTimeMaxMs";
export function TrafficChart({ data, field, label }: { data: TrafficSeries; field: MetricField; label: string }) {
  const [active, setActive] = useState<string | null>(null);
  const points = [...data.items].sort((a, b) => Date.parse(a.windowStart) - Date.parse(b.windowStart));
  const max = Math.max(1, ...points.map((point) => point[field])); const from = Date.parse(data.from); const span = Math.max(1, Date.parse(data.to) - from);
  const x = (point: TrafficPoint) => Math.max(0, Math.min(600, (Date.parse(point.windowStart) - from) / span * 600)); const y = (point: TrafficPoint) => 100 - point[field] / max * 85;
  const known = field === "averageMs" ? points.filter((point) => point.requests > 0) : points;
  const segments = trafficSegments(known, data.bucketSeconds);
  const selected = points.find((point) => point.windowStart === active);
  return <section className="panel stack"><h3>{label}</h3><div className="security-chart" tabIndex={0} role="group" aria-label={`${label}。${sec.chartHelp}`} onKeyDown={(e) => { if (e.key !== "ArrowLeft" && e.key !== "ArrowRight") return; e.preventDefault(); const index = points.findIndex((point) => point.windowStart === active); const next = Math.max(0, Math.min(points.length - 1, (index < 0 ? points.length - 1 : index) + (e.key === "ArrowLeft" ? -1 : 1))); setActive(points[next]?.windowStart ?? null); }} onPointerMove={(e) => { const rect = e.currentTarget.getBoundingClientRect(); const t = from + (e.clientX - rect.left) / Math.max(1, rect.width) * span; const point = points.reduce<TrafficPoint | undefined>((nearest, item) => !nearest || Math.abs(Date.parse(item.windowStart) - t) < Math.abs(Date.parse(nearest.windowStart) - t) ? item : nearest, undefined); setActive(point?.windowStart ?? null); }} onPointerLeave={() => setActive(null)}>
      <svg viewBox="0 0 600 110" preserveAspectRatio="none" role="img" aria-label={label}><line x1={0} x2={600} y1={100} y2={100} stroke="var(--border)" />{segments.map((segment) => <polyline key={segment[0]?.windowStart} points={segment.map((point) => `${x(point)},${y(point)}`).join(" ")} fill="none" stroke="var(--chart-1)" strokeWidth={2} vectorEffect="non-scaling-stroke" />)}{known.map((point) => <circle key={point.windowStart} cx={x(point)} cy={y(point)} r={2} fill="var(--chart-1)"><title>{showTime(point.windowStart)}：{number(point[field])}</title></circle>)}{selected && <line x1={x(selected)} x2={x(selected)} y1={0} y2={100} stroke="var(--muted-foreground)" strokeDasharray="3 3" />}</svg>
      <p className="meta" aria-live="polite">{selected ? `${showTime(selected.windowStart)} · ${number(field === "averageMs" && selected.requests === 0 ? null : selected[field])}` : `${number(max)} / ${sec.chartHelp}`}</p>
    </div></section>;
}
