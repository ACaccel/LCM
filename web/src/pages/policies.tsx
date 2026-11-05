import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { policiesQuery } from "../api/queries";
import type { PolicySummary } from "../api/types";
import { ManagedBadges } from "../components/badges";
import { Dialog } from "../components/dialog";
import { SortTh, useSort } from "../components/sort-header";
import { Async, Empty, Field, PageHead, Pill, Tag } from "../components/ui";
import { formatTime } from "../lib/format";
import { nameMatches, sortRows } from "../lib/list";
import type { SortValue } from "../lib/list";
import { policyStateView, runStatusView } from "../lib/status";
import { s } from "../strings";
import { TemplateDialog } from "./template-dialog";

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
    <Dialog title={s.policies.create} onClose={onClose} dirty={name !== ""}>
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

const policySortKeys = ["name", "version", "state", "lastRun"] as const;
type PolicySortKey = (typeof policySortKeys)[number];
const policySort: Record<PolicySortKey, (p: PolicySummary) => SortValue> = {
  name: (p) => p.name,
  version: (p) => p.deployedVersion,
  // Running policies first, then paused, then those not deployed.
  state: (p) => (!p.deployed ? 2 : p.paused ? 1 : 0),
  lastRun: (p) => (p.lastRun ? Date.parse(p.lastRun.createdAt) : null),
};

export function PoliciesPage() {
  const q = useQuery(policiesQuery());
  const [params, setParams] = useSearchParams();
  const search = params.get("q") ?? "";
  const { sort, toggle } = useSort(policySortKeys, { key: "name", dir: "asc" });
  const [creating, setCreating] = useState(false);
  const [fromTemplate, setFromTemplate] = useState(false);
  function setSearch(value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set("q", value);
    else next.delete("q");
    setParams(next, { replace: true });
  }
  const th = (key: PolicySortKey, label: string, num?: boolean) => <SortTh sortKey={key} label={label} sort={sort} onSort={toggle} num={num} />;
  return (
    <div className="page">
      <PageHead title={s.policies.title}>
        <button type="button" className="btn" onClick={() => setFromTemplate(true)}>
          {s.templates.fromTemplate}
        </button>
        <button type="button" className="btn primary" onClick={() => setCreating(true)}>
          {s.policies.create}
        </button>
      </PageHead>
      <div className="filters">
        <div className="field">
          <label htmlFor="p-search">{s.policies.search}</label>
          <input id="p-search" type="search" value={search} onChange={(e) => setSearch(e.target.value)} />
        </div>
      </div>
      <Async q={q}>
        {(all) => {
          const items = sortRows(
            all.filter((p) => nameMatches(p.name, search)),
            sort,
            policySort,
          );
          return all.length === 0 ? (
            <Empty>{s.policies.empty}</Empty>
          ) : items.length === 0 ? (
            <Empty>{s.policies.noMatch}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    {th("name", s.policies.columns.name)}
                    {th("version", s.policies.columns.version, true)}
                    {th("state", s.policies.columns.state)}
                    <th>{s.policies.columns.triggers}</th>
                    {th("lastRun", s.policies.columns.lastRun)}
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
                        <Pill view={policyStateView(p)} />
                      </td>
                      <td>
                        <div className="row tight">
                          {p.triggers.map((t) => (
                            <Tag key={t.id} title={t.id}>
                              {t.type}
                            </Tag>
                          ))}
                        </div>
                      </td>
                      <td>
                        {p.lastRun ? (
                          <span className="row snug">
                            <Pill view={runStatusView(p.lastRun.status)} />
                            <Link to={`/runs/${encodeURIComponent(p.lastRun.id)}`}>{formatTime(p.lastRun.createdAt)}</Link>
                          </span>
                        ) : (
                          <span className="meta">{s.policies.noRun}</span>
                        )}
                      </td>
                      <td>
                        <div className="row tight">
                          <ManagedBadges managedBy={p.managedBy} />
                        </div>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          );
        }}
      </Async>
      {fromTemplate && <TemplateDialog onClose={() => setFromTemplate(false)} />}
      {creating && <CreateDialog existing={new Set((q.data ?? []).map((p) => p.name))} onClose={() => setCreating(false)} />}
    </div>
  );
}
