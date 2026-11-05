import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { hostStatusQuery, incidentsQuery, securityStatusQuery } from "../../api/security";
import { Async, ErrorBox, Tag } from "../../components/ui";
import { HostNotConfigured, MachineSelect, Metric, UnknownReport, auditEnabled, number, showTime, stale } from "./shared";
import { sec } from "./strings";

export function SecurityOverview({ machineId }: { machineId: string }) {
  const status = useQuery(securityStatusQuery());
  const host = useQuery(hostStatusQuery());
  const incidents = useInfiniteQuery(incidentsQuery({ machineId: machineId || undefined, state: "firing" }));
  const items = incidents.data?.pages.flatMap((page) => page.items) ?? [];
  const query = machineId ? `?machineId=${encodeURIComponent(machineId)}` : "";
  return <div className="stack">
    <div className="filters"><MachineSelect /></div>
    <section className="panel stack"><h2>{sec.status}</h2><p className="meta">{sec.statusHelp}</p><Async q={status}>{(value) => <>
      <div className="security-metrics"><Metric label={sec.status} value={value.enabled ? sec.enabled : sec.disabled} /><Metric label={sec.notifications} value={value.notificationsEnabled ? sec.enabled : sec.disabled} /><Metric label={sec.pending} value={number(value.pending)} /><Metric label={sec.failed} value={number(value.failed)} /><Metric label={sec.overflow} value={number(value.overflow)} /><Metric label={sec.expired} value={number(value.expired)} /></div>
      <p className="meta">{sec.lastPoll}：{showTime(value.lastPollAt)}</p>{value.enabled && stale(value.lastPollAt) && <p className="banner warn">{sec.stale}</p>}{value.lastError && <p className="banner error">{value.lastError}</p>}
    </>}</Async></section>
    <section className="panel stack"><h2>{sec.incidents}</h2><ErrorBox error={incidents.error} onRetry={() => void incidents.refetch()} />{incidents.isPending ? <p role="status">{sec.unknown}</p> : incidents.data && <><p>{sec.stateLabels.firing}：{items.length}{incidents.hasNextPage ? "+" : ""}；{sec.notAcknowledged}：{items.filter((item) => !item.handling?.acknowledged).length}{incidents.hasNextPage ? "+" : ""}</p><Link to={`/security/incidents${query}`}>{sec.incidents}</Link></>}
    </section>
    <section className="panel stack"><h2>{sec.hostScope}</h2><Async q={host}>{(value) => !value.configured ? <HostNotConfigured /> : <>
      <p><code>{value.machineId || sec.unknown}</code></p>{!value.available && <p className="banner warn">{sec.readOnly}：{value.reason || sec.unavailable}</p>}
      {machineId && value.machineId !== machineId ? <p className="banner warn">{sec.hostScope}：{value.machineId || sec.unknown}</p> : <>
      <div className="security-metrics"><Metric label={sec.aide} value={value.aide?.result || sec.unknown} /><Metric label={sec.audit} value={auditEnabled(value.audit?.enabled)} /><Metric label={sec.lost} value={number(value.audit?.lost)} /><Metric label={sec.backlog} value={number(value.audit?.backlog)} /><Metric label={sec.queue} value={number(value.collector?.queued)} /><Metric label={sec.dropped} value={number(value.collector?.dropped)} /></div>
      <dl className="kv"><dt>{sec.lastRun}</dt><dd>{showTime(value.aide?.lastRunAt)}</dd><dt>{sec.nextRun}</dt><dd>{showTime(value.aide?.nextRunAt)}</dd><dt>{sec.observed}</dt><dd>{showTime(value.collector?.observedAt)}</dd><dt>{sec.lastDelivery}</dt><dd>{showTime(value.collector?.lastDelivery)}</dd><dt>{sec.nginxMode}</dt><dd><Tag>{value.nginx?.mode || sec.unknown}</Tag></dd></dl>
      {value.available && stale(value.collector?.observedAt) && <p className="banner warn">{sec.stale}</p>}{value.collector?.deliveryError && <p className="banner error">{value.collector.deliveryError}</p>}
      </>}
      <h3>{sec.collectionHealth}</h3><UnknownReport value={value.collector?.health} /><Link to="/resources?type=machine">{sec.machineLink}</Link><Link to="/resources?type=service">{sec.serviceLink}</Link>
    </>}</Async></section>
  </div>;
}
