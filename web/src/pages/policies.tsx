import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { policiesQuery } from "../api/queries";
import { ManagedBadges } from "../components/badges";
import { Dialog } from "../components/dialog";
import { Async, Empty, Field, PageHead, Pill, Tag } from "../components/ui";
import { formatTime } from "../lib/format";
import { runStatusView } from "../lib/status";
import { s } from "../strings";

const NAME_RE = /^[a-z]([a-z0-9-]*[a-z0-9])?$/;

function CreateDialog({ existing, onClose }: { existing: Set<string>; onClose: () => void }) {
  const [name, setName] = useState("");
  const navigate = useNavigate();
  const problem = name && !NAME_RE.test(name) ? s.policies.newNameHelp : existing.has(name) ? s.policies.exists : "";
  function submit(e: FormEvent) {
    e.preventDefault();
    if (!name || problem) return;
    onClose();
    void navigate(`/policies/${encodeURIComponent(name)}?new=1`);
  }
  return (
    <Dialog title={s.policies.create} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Field label={s.policies.newName} htmlFor="p-name" hint={s.policies.newNameHelp}>
          <input id="p-name" type="text" value={name} onChange={(e) => setName(e.target.value)} data-autofocus required maxLength={63} />
        </Field>
        {problem && <div className="banner error">{problem}</div>}
        <div className="row end">
          <button type="button" className="btn" onClick={onClose}>
            {s.common.cancel}
          </button>
          <button type="submit" className="btn primary" disabled={!name || !!problem}>
            {s.policies.createFromTemplate}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

export function PoliciesPage() {
  const q = useQuery(policiesQuery());
  const [creating, setCreating] = useState(false);
  return (
    <div className="page">
      <PageHead title={s.policies.title}>
        <button type="button" className="btn primary" onClick={() => setCreating(true)}>
          {s.policies.create}
        </button>
      </PageHead>
      <Async q={q}>
        {(items) =>
          items.length === 0 ? (
            <Empty>{s.policies.empty}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.policies.columns.name}</th>
                    <th className="num">{s.policies.columns.version}</th>
                    <th>{s.policies.columns.state}</th>
                    <th>{s.policies.columns.triggers}</th>
                    <th>{s.policies.columns.lastRun}</th>
                    <th>{s.policies.columns.managed}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((p) => (
                    <tr key={p.name}>
                      <td>
                        <Link to={`/policies/${encodeURIComponent(p.name)}`}>{p.name}</Link>
                      </td>
                      <td className="num">
                        {p.deployedVersion ?? "-"}
                        {p.deployedVersion !== p.latestVersion && <span className="meta"> / {p.latestVersion}</span>}
                      </td>
                      <td>
                        {!p.deployed ? (
                          <Pill view={{ tone: "neutral", icon: "○", label: s.policies.notDeployed }} />
                        ) : p.paused ? (
                          <Pill view={{ tone: "warn", icon: "‖", label: s.policies.paused }} />
                        ) : (
                          <Pill view={{ tone: "ok", icon: "●", label: s.policies.deployed }} />
                        )}
                      </td>
                      <td>
                        <div className="row" style={{ gap: 4 }}>
                          {p.triggers.map((t) => (
                            <Tag key={t.id} title={t.id}>
                              {t.type}
                            </Tag>
                          ))}
                        </div>
                      </td>
                      <td>
                        {p.lastRun ? (
                          <span className="row" style={{ gap: 6 }}>
                            <Pill view={runStatusView(p.lastRun.status)} />
                            <Link to={`/runs/${encodeURIComponent(p.lastRun.id)}`}>{formatTime(p.lastRun.createdAt)}</Link>
                          </span>
                        ) : (
                          <span className="meta">{s.policies.noRun}</span>
                        )}
                      </td>
                      <td>
                        <div className="row" style={{ gap: 4 }}>
                          <ManagedBadges managedBy={p.managedBy} />
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
      {creating && <CreateDialog existing={new Set((q.data ?? []).map((p) => p.name))} onClose={() => setCreating(false)} />}
    </div>
  );
}
