import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useSyncExternalStore } from "react";
import type { ReactNode } from "react";
import { NavLink, useNavigate } from "react-router";
import { client, unwrap } from "../api/client";
import { EventStream } from "../api/events";
import type { Actor } from "../api/types";
import { LiveContext } from "../live";
import { s } from "../strings";

const items = [
  { to: "/", label: s.nav.overview, end: true },
  { to: "/services", label: s.nav.services },
  { to: "/timeline", label: s.nav.timeline },
  { to: "/policies", label: s.nav.policies },
  { to: "/runs", label: s.nav.runs },
  { to: "/hosts", label: s.nav.hosts },
  { to: "/settings", label: s.nav.settings },
];

/** Runs the tab's single event connection for as long as the shell is mounted. */
function useEventStream() {
  const qc = useQueryClient();
  const stream = useMemo(() => new EventStream(qc), [qc]);
  useEffect(() => {
    stream.start();
    return () => stream.stop();
  }, [stream]);
  return useSyncExternalStore(stream.subscribe, stream.getSnapshot);
}

export function Shell({ actor, children }: { actor: Actor; children: ReactNode }) {
  const live = useEventStream();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const logout = useMutation({
    mutationFn: () => unwrap(client.POST("/auth/logout")),
    onSettled: () => {
      qc.clear();
      void navigate("/login", { replace: true });
    },
  });

  return (
    <div className="shell">
      <nav aria-label={s.nav.main}>
        <div className="brand">{s.appName}</div>
        {items.map((i) => (
          <NavLink key={i.to} to={i.to} end={i.end} className={({ isActive }) => `navlink${isActive ? " active" : ""}`}>
            {i.label}
          </NavLink>
        ))}
        <div className="nav-foot">
          <span title={live === "open" ? s.live.connected : s.live.reconnecting}>
            <span className={`live-dot ${live === "open" ? "on" : "retry"}`} aria-hidden="true" />
            {live === "open" ? s.live.connected : live === "connecting" ? s.live.connecting : s.live.reconnecting}
          </span>
          <span title={s.account.signedInAs}>{actor.name}</span>
          <button type="button" className="btn small" onClick={() => logout.mutate()}>
            {s.account.logout}
          </button>
        </div>
      </nav>
      <main>
        <LiveContext.Provider value={live}>{children}</LiveContext.Provider>
      </main>
    </div>
  );
}
