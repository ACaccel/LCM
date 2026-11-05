import { useQueryClient } from "@tanstack/react-query";
import { NavLink, useParams, useSearchParams } from "react-router";
import { sk } from "../api/security";
import { PageHead } from "../components/ui";
import { SecurityOverview } from "./security/overview";
import { SecurityIncidents } from "./security/incidents";
import { SecurityTraffic } from "./security/traffic";
import { SecurityRules, SecurityChecks } from "./security/controls";
import { sec } from "../strings";
import "../styles/security.css";

const tabs = [["overview", sec.overview], ["incidents", sec.incidents], ["traffic", sec.traffic], ["rules", sec.rules], ["checks", sec.checks]] as const;
export function SecurityPage() {
  const { tab = "overview" } = useParams();
  const [params] = useSearchParams();
  const qc = useQueryClient();
  const machineId = params.get("machineId") || "";
  return <div className="page security-console">
    <PageHead title={sec.title}><button type="button" className="btn" onClick={() => void qc.invalidateQueries({ queryKey: sk.all })}>{sec.refresh}</button></PageHead>
    <nav className="tabs" aria-label={sec.title}>{tabs.map(([id, label]) => <NavLink key={id} to={{ pathname: `/security/${id}`, search: params.toString() }} className={() => tab === id || tab === "" && id === "overview" ? "active" : ""}>{label}</NavLink>)}</nav>
    {tab === "overview" ? <SecurityOverview machineId={machineId} /> : tab === "incidents" ? <SecurityIncidents machineId={machineId} /> : tab === "traffic" ? <SecurityTraffic machineId={machineId} /> : tab === "rules" ? <SecurityRules /> : tab === "checks" ? <SecurityChecks /> : <p className="empty">{sec.unknown}</p>}
  </div>;
}
