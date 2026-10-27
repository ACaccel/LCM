import { NavLink, useParams } from "react-router";
import { PageHead } from "../components/ui";
import { s } from "../strings";
import { AccountPanel } from "./settings/account";
import { LocksPanel } from "./settings/locks";
import { OwnershipPanel } from "./settings/ownership";
import { SourcesPanel } from "./settings/sources";
import { TokensPanel } from "./settings/tokens";

const TABS = ["tokens", "sources", "ownership", "locks", "account"] as const;
type Tab = (typeof TABS)[number];

export function SettingsPage() {
  const { tab } = useParams();
  const active: Tab = (TABS as readonly string[]).includes(tab ?? "") ? (tab as Tab) : "tokens";
  return (
    <div className="page">
      <PageHead title={s.settings.title} />
      <nav className="tabs" aria-label={s.settings.title}>
        {TABS.map((t) => (
          <NavLink key={t} to={`/settings/${t}`} aria-current={active === t ? "page" : undefined} className={active === t ? "active" : undefined}>
            {s.settings.tabs[t]}
          </NavLink>
        ))}
      </nav>
      {active === "tokens" && <TokensPanel />}
      {active === "sources" && <SourcesPanel />}
      {active === "ownership" && <OwnershipPanel />}
      {active === "locks" && <LocksPanel />}
      {active === "account" && <AccountPanel />}
    </div>
  );
}
