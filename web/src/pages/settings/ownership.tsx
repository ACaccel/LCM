import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { client, unwrap } from "../../api/client";
import { ownershipQuery, qk } from "../../api/queries";
import type { Ownership } from "../../api/types";
import { ConfirmDialog } from "../../components/dialog";
import { Async, Empty, Pill, Tag } from "../../components/ui";
import { s } from "../../strings";

type Pending = { kind: "git" | "current" | "release"; item: Ownership };

function StateBadges({ o }: { o: Ownership }) {
  const out = [];
  if (o.conflict) out.push(<Pill key="c" view={{ tone: "bad", icon: "✕", label: s.ownership.conflict }} />);
  if (o.drifted) out.push(<Pill key="d" view={{ tone: "warn", icon: "▲", label: s.ownership.drifted }} />);
  if (o.orphaned) out.push(<Pill key="o" view={{ tone: "warn", icon: "▲", label: s.ownership.orphaned }} />);
  if (o.missing) out.push(<Tag key="m">{s.settings.ownership.missing}</Tag>);
  if (out.length === 0) out.push(<Pill key="ok" view={{ tone: "ok", icon: "✓", label: s.settings.ownership.inSync }} />);
  return <div className="row" style={{ gap: 4 }}>{out}</div>;
}

export function OwnershipPanel() {
  const q = useQuery(ownershipQuery());
  const qc = useQueryClient();
  const [pending, setPending] = useState<Pending | null>(null);
  const invalidate = () => {
    setPending(null);
    void qc.invalidateQueries({ queryKey: qk.ownership() });
    void qc.invalidateQueries({ queryKey: qk.servicesAll() });
    void qc.invalidateQueries({ queryKey: qk.policies() });
  };
  const kindPath = (o: Ownership) => o.kind.toLowerCase() as "service" | "policy" | "module";
  const resolve = useMutation({
    mutationFn: ({ item, use }: { item: Ownership; use: "git" | "current" }) =>
      unwrap(client.POST("/config/ownership/{kind}/{name}/resolve", { params: { path: { kind: kindPath(item), name: item.name } }, body: { use } })),
    onSuccess: invalidate,
  });
  const release = useMutation({
    mutationFn: (item: Ownership) => unwrap(client.DELETE("/config/ownership/{kind}/{name}", { params: { path: { kind: kindPath(item), name: item.name } } })),
    onSuccess: invalidate,
  });

  return (
    <div className="stack">
      <p className="meta">{s.settings.ownership.help}</p>
      <Async q={q}>
        {(items) =>
          items.length === 0 ? (
            <Empty>{s.settings.ownership.empty}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.settings.ownership.kind}</th>
                    <th>{s.common.name}</th>
                    <th>{s.settings.ownership.source}</th>
                    <th>{s.settings.ownership.commit}</th>
                    <th>{s.settings.ownership.state}</th>
                    <th>{s.common.actions}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((o) => (
                    <tr key={`${o.kind}/${o.name}`}>
                      <td>{o.kind}</td>
                      <td>
                        {o.kind === "Service" && !o.missing ? (
                          <Link to={`/services/${encodeURIComponent(o.name)}/config`}>{o.name}</Link>
                        ) : o.kind === "Policy" && !o.missing ? (
                          <Link to={`/policies/${encodeURIComponent(o.name)}`}>{o.name}</Link>
                        ) : (
                          o.name
                        )}
                      </td>
                      <td>
                        {o.source}
                        <div className="meta">
                          <code>{o.path}</code>
                        </div>
                      </td>
                      <td>
                        <code>{o.commit.slice(0, 7)}</code>
                      </td>
                      <td>
                        <StateBadges o={o} />
                      </td>
                      <td>
                        <div className="row" style={{ gap: 4 }}>
                          {(o.drifted || o.conflict) && (
                            <>
                              <button type="button" className="btn small" onClick={() => setPending({ kind: "git", item: o })}>
                                {s.config.resolveGit}
                              </button>
                              <button type="button" className="btn small" onClick={() => setPending({ kind: "current", item: o })}>
                                {s.config.resolveCurrent}
                              </button>
                            </>
                          )}
                          <button type="button" className="btn small danger" onClick={() => setPending({ kind: "release", item: o })}>
                            {s.config.release}
                          </button>
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </Async>
      {pending && (
        <ConfirmDialog
          title={`${pending.kind === "git" ? s.config.resolveGit : pending.kind === "current" ? s.config.resolveCurrent : s.config.release}: ${pending.item.name}`}
          confirmLabel={pending.kind === "git" ? s.config.resolveGit : pending.kind === "current" ? s.config.resolveCurrent : s.config.release}
          body={pending.kind === "git" ? s.config.confirmGit : pending.kind === "current" ? s.config.confirmCurrent : s.config.confirmRelease}
          danger={pending.kind !== "current"}
          busy={resolve.isPending || release.isPending}
          error={resolve.error ?? release.error}
          onClose={() => setPending(null)}
          onConfirm={() => (pending.kind === "release" ? release.mutate(pending.item) : resolve.mutate({ item: pending.item, use: pending.kind }))}
        />
      )}
    </div>
  );
}
