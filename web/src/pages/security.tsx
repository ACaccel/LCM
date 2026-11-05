import { useQueryClient } from "@tanstack/react-query";
import { Navigate, NavLink, useParams, useSearchParams } from "react-router";
import { sk } from "../api/security";
import { PageHead } from "../components/ui";
import { sec } from "../strings";
import { SecurityHost } from "./security/controls";
import { SecurityIncidents } from "./security/incidents";
import { NotificationSettings } from "./security/notifications";
import { SecurityStatus } from "./security/overview";
import { SecurityTraffic } from "./security/traffic";
import "../styles/security.css";

const TABS = [
  ["incidents", sec.tabIncidents],
  ["traffic", sec.traffic],
  ["host", sec.tabHost],
  ["notifications", sec.tabNotifications],
] as const;

/** Tabs of earlier versions and where their content went: rules and checks are host control now, the overview is the incident tab. */
export const legacyTabs: Record<string, string> = { overview: "incidents", rules: "host", checks: "host" };

export function SecurityPage() {
  const { tab = "" } = useParams();
  const [params] = useSearchParams();
  const qc = useQueryClient();
  const machineId = params.get("machineId") || "";
  const target = tab === "" ? "incidents" : legacyTabs[tab];
  if (target) return <Navigate replace to={{ pathname: `/security/${target}`, search: params.toString() }} />;
  return (
    <div className="page security-console">
      <PageHead title={sec.title}>
        <button type="button" className="btn" onClick={() => void qc.invalidateQueries({ queryKey: sk.all })}>
          {sec.refresh}
        </button>
      </PageHead>
      <nav className="tabs" aria-label={sec.title}>
        {TABS.map(([id, label]) => (
          <NavLink key={id} to={{ pathname: `/security/${id}`, search: params.toString() }} className={() => (tab === id ? "active" : "")}>
            {label}
          </NavLink>
        ))}
      </nav>
      {tab === "incidents" && (
        <div className="stack">
          <SecurityStatus />
          <SecurityIncidents machineId={machineId} />
        </div>
      )}
      {tab === "traffic" && <SecurityTraffic machineId={machineId} />}
      {tab === "host" && <SecurityHost />}
      {tab === "notifications" && <NotificationSettings />}
      {!TABS.some(([id]) => id === tab) && <p className="empty">{sec.unknown}</p>}
    </div>
  );
}
