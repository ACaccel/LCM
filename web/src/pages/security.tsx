import { useInfiniteQuery, useQueryClient } from "@tanstack/react-query";
import { NavLink, useParams, useSearchParams } from "react-router";
import { machinesQuery } from "../api/machines";
import { ErrorBox } from "../components/ui";
import { sk } from "../api/security";
import { PageHead } from "../components/ui";
import { SecurityOverview } from "./security/overview";
import { SecurityIncidents } from "./security/incidents";
import { SecurityTraffic } from "./security/traffic";
import { SecurityRules, SecurityChecks } from "./security/controls";
import { sec } from "./security/strings";
import "../styles/security.css";

const tabs = [["overview", sec.overview], ["incidents", sec.incidents], ["traffic", sec.traffic], ["rules", sec.rules], ["checks", sec.checks]] as const;
export function SecurityPage() {
  const { tab = "overview" } = useParams();
  const [params, setParams] = useSearchParams();
  const qc = useQueryClient();
  const machines = useInfiniteQuery(machinesQuery());
  const machineItems = machines.data?.pages.flatMap((page) => page.items) ?? [];
  const machineId = params.get("machineId") || "";
  return <div className="page stack security-console">
    <PageHead title={sec.title} meta={<p className="meta">{sec.statusHelp}</p>}><button type="button" className="btn" onClick={() => void qc.invalidateQueries({ queryKey: sk.all })}>{sec.refresh}</button></PageHead>
    <nav className="tabs" aria-label={sec.title}>{tabs.map(([id, label]) => <NavLink key={id} to={{ pathname: `/security/${id}`, search: params.toString() }} className={() => tab === id || tab === "" && id === "overview" ? "active" : ""}>{label}</NavLink>)}</nav>
    <div className="field security-machine"><label htmlFor="security-machine">{sec.machine}</label><input id="security-machine" list="security-machine-options" value={machineId} placeholder={sec.all} onChange={(e) => { const next = new URLSearchParams(params); if (e.target.value) next.set("machineId", e.target.value); else next.delete("machineId"); next.delete("incident"); setParams(next, { replace: true }); }} /><datalist id="security-machine-options">{machineItems.map((machine) => <option key={machine.id} value={machine.id}>{machine.name}</option>)}</datalist><span className="hint">{sec.machineHelp}</span>{machines.hasNextPage && <button type="button" className="btn small" disabled={machines.isFetchingNextPage} onClick={() => void machines.fetchNextPage()}>{sec.machineMore}</button>}<ErrorBox error={machines.error} onRetry={() => void machines.refetch()} /></div>
    {tab === "overview" ? <SecurityOverview machineId={machineId} /> : tab === "incidents" ? <SecurityIncidents machineId={machineId} /> : tab === "traffic" ? <SecurityTraffic machineId={machineId} /> : tab === "rules" ? <SecurityRules /> : tab === "checks" ? <SecurityChecks /> : <p className="empty">{sec.unknown}</p>}
  </div>;
}
