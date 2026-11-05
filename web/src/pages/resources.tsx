import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { client, unwrap, yamlBody } from "../api/client";
import { archivesQuery, qk, resourcesQuery, resourceTypesQuery } from "../api/queries";
import type { Health, ResourceSummary, ResourceType, ResourceTypeName } from "../api/types";
import { HealthPill, ManagedBadges, PhasePill, StatePill, TypeTag } from "../components/badges";
import { Dialog } from "../components/dialog";
import { SortTh, useSort } from "../components/sort-header";
import { useToast } from "../components/toast";
import { Async, Empty, ErrorBox, Field, PageHead, Tag } from "../components/ui";
import { ValidationReport } from "../components/validation";
import { YamlEditor } from "../components/yaml-editor-lazy";
import { formatDateTime, shortVersion } from "../lib/format";
import { nameMatches, sortRows } from "../lib/list";
import { resourceSkeleton } from "../lib/templates";
import { s } from "../strings";

const healths: Health[] = ["healthy", "degraded", "down", "unknown"];
const phases = ["managed", "retired"] as const;

const resourceSortKeys = ["name", "type", "phase", "state", "health", "version"] as const;
type ResourceSortKey = (typeof resourceSortKeys)[number];
// Ascending health puts the resources that need attention first.
const healthRank: Record<Health, number> = { down: 0, degraded: 1, unknown: 2, healthy: 3 };
const resourceSort: Record<ResourceSortKey, (r: ResourceSummary) => string | number> = {
  name: (r) => r.name,
  type: (r) => s.resourceType[r.type],
  phase: (r) => s.phase[r.phase],
  state: (r) => (r.state ? (s.state[r.state] ?? r.state) : ""),
  health: (r) => healthRank[r.health],
  version: (r) => r.version,
};

function isType(types: readonly ResourceType[] | undefined, v: string): v is ResourceTypeName {
  return !!types?.some((t) => t.type === v);
}

function ResourceRow({ r }: { r: ResourceSummary }) {
  return (
    <tr>
      <td>
        <Link to={`/resources/${encodeURIComponent(r.name)}`}>{r.name}</Link>
      </td>
      <td>
        <TypeTag type={r.type} adapter={r.adapter} />
      </td>
      <td>
        <PhasePill phase={r.phase} />
      </td>
      <td>
        <StatePill state={r.state} />
      </td>
      <td>
        <HealthPill health={r.health} />
      </td>
      <td>
        {s.config.version(r.version)}
        {r.deployedVersion && (
          <div className="meta">
            <code>{shortVersion(r.deployedVersion)}</code>
          </div>
        )}
      </td>
      <td>
        <div className="row" style={{ gap: 4 }}>
          <ManagedBadges managedBy={r.managedBy} />
        </div>
      </td>
      <td>
        <div className="row" style={{ gap: 4 }}>
          {Object.entries(r.labels).map(([k, v]) => (
            <Tag key={k}>{`${k}=${v}`}</Tag>
          ))}
        </div>
      </td>
    </tr>
  );
}

function ArchiveList({ type, search }: { type: ResourceTypeName | undefined; search: string }) {
  const q = useQuery(archivesQuery(type));
  return (
    <Async q={q}>
      {(items) => {
        const shown = items.filter((r) => nameMatches(r.name, search));
        return shown.length === 0 ? (
          <Empty>{s.resources.emptyArchives}</Empty>
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th>{s.resources.columns.name}</th>
                  <th>{s.resources.columns.type}</th>
                  <th>{s.resources.columns.version}</th>
                  <th>{s.resources.columns.archivedAt}</th>
                </tr>
              </thead>
              <tbody>
                {shown.map((r) => (
                  <tr key={r.id}>
                    <td>
                      <Link to={`/resources/${encodeURIComponent(`@${r.id}`)}`}>{r.name}</Link>
                      <div className="meta">
                        <code>{r.id}</code>
                      </div>
                    </td>
                    <td>
                      <TypeTag type={r.type} adapter={r.adapter} />
                    </td>
                    <td>{s.config.version(r.version)}</td>
                    <td>{formatDateTime(r.updatedAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        );
      }}
    </Async>
  );
}

/**
 * Two steps: the type and adapter come from `/resource-types`, then the
 * document is edited as YAML and validated by the server. Nothing about the
 * config fields is decided here.
 */
function CreateDialog({ types, initialType, onClose }: { types: ResourceType[]; initialType: ResourceTypeName | undefined; onClose: () => void }) {
  const first = types.find((t) => t.type === initialType) ?? types[0];
  const [type, setType] = useState<ResourceTypeName | undefined>(first?.type);
  const [adapter, setAdapter] = useState(first?.adapters[0] ?? "");
  const [text, setText] = useState<string | null>(null);
  const qc = useQueryClient();
  const navigate = useNavigate();
  const toast = useToast();
  const descriptor = types.find((t) => t.type === type);

  const validate = useMutation({
    mutationFn: () => unwrap(client.POST("/validate", { body: (text ?? "") as never, ...yamlBody })),
  });
  const create = useMutation({
    mutationFn: () => unwrap(client.POST("/resources", { body: (text ?? "") as never, ...yamlBody })),
    onSuccess: (doc) => {
      void qc.invalidateQueries({ queryKey: qk.resourcesAll() });
      toast.success(s.resources.created);
      onClose();
      void navigate(`/resources/${encodeURIComponent(doc.name)}`);
    },
  });

  function pickType(next: string) {
    const t = types.find((x) => x.type === next);
    setType(t?.type);
    setAdapter(t?.adapters[0] ?? "");
  }

  const editing = text !== null;
  return (
    <Dialog
      title={s.resources.createTitle}
      wide
      onClose={onClose}
      dirty={editing || type !== first?.type || adapter !== (first?.adapters[0] ?? "")}
      pending={create.isPending}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {s.common.cancel}
          </button>
          {editing ? (
            <>
              <button
                type="button"
                className="btn"
                onClick={() => {
                  setText(null);
                  validate.reset();
                  create.reset();
                }}
              >
                {s.common.back}
              </button>
              <button type="button" className="btn" disabled={validate.isPending} onClick={() => validate.mutate()}>
                {s.resources.validate}
              </button>
              <button type="button" className="btn primary" disabled={create.isPending} onClick={() => create.mutate()}>
                {s.common.create}
              </button>
            </>
          ) : (
            <button
              type="button"
              className="btn primary"
              disabled={!descriptor || !adapter}
              onClick={() => descriptor && setText(resourceSkeleton(descriptor.type, adapter, descriptor.needsConnection))}
            >
              {s.common.next}
            </button>
          )}
        </>
      }
    >
      {editing ? (
        <div className="stack">
          <p className="meta">{s.resources.createHelp}</p>
          {descriptor?.needsConnection && <p className="meta">{s.resources.connectionHelp}</p>}
          <YamlEditor value={text} onChange={setText} ariaLabel={s.common.resourceYaml} />
          {validate.data && <ValidationReport {...validate.data} />}
          <ErrorBox error={validate.error ?? create.error} />
        </div>
      ) : (
        <div className="stack">
          <Field label={s.resources.chooseType} htmlFor="new-type" hint={descriptor?.description}>
            <select id="new-type" value={type ?? ""} onChange={(e) => pickType(e.target.value)} data-autofocus>
              {types.map((t) => (
                <option key={t.type} value={t.type}>
                  {s.resourceType[t.type]} ({t.type})
                </option>
              ))}
            </select>
          </Field>
          <Field label={s.resources.chooseAdapter} htmlFor="new-adapter">
            <select id="new-adapter" value={adapter} onChange={(e) => setAdapter(e.target.value)}>
              {(descriptor?.adapters ?? []).map((a) => (
                <option key={a} value={a}>
                  {a}
                </option>
              ))}
            </select>
          </Field>
        </div>
      )}
    </Dialog>
  );
}

export function ResourcesPage() {
  const [params, setParams] = useSearchParams();
  const types = useQuery(resourceTypesQuery());
  const rawType = params.get("type") ?? "";
  const type = isType(types.data, rawType) ? rawType : undefined;
  const search = params.get("q") ?? "";
  const label = params.get("label") ?? "";
  const phase = (phases as readonly string[]).includes(params.get("phase") ?? "") ? (params.get("phase") as (typeof phases)[number]) : undefined;
  const health = (params.get("health") ?? "") as Health | "";
  const archived = params.get("archived") === "1";
  const creating = params.get("create") === "1";
  const { sort, toggle: toggleSort } = useSort(resourceSortKeys, { key: "name", dir: "asc" });

  const labels = label
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
  // The type filter waits for the descriptors so that an unknown type in the URL is not sent.
  const q = useQuery({
    ...resourcesQuery({ type, label: labels, phase, health: health || undefined }),
    enabled: !archived && (!rawType || types.isSuccess),
  });

  function set(key: string, value: string) {
    const next = new URLSearchParams(params);
    if (value) next.set(key, value);
    else next.delete(key);
    setParams(next, { replace: true });
  }

  return (
    <div className="page">
      <PageHead title={s.resources.title}>
        <button type="button" className="btn primary" disabled={!types.isSuccess} onClick={() => set("create", "1")}>
          {s.resources.create}
        </button>
      </PageHead>
      <ErrorBox error={types.error ? new Error(s.resources.typeLoadFailed) : null} onRetry={() => void types.refetch()} />
      <div className="row chips" role="group" aria-label={s.resources.filterType}>
        <button type="button" className="btn small" aria-pressed={!type} onClick={() => set("type", "")}>
          {s.resources.allTypes}
        </button>
        {(types.data ?? []).map((t) => (
          <button key={t.type} type="button" className="btn small" aria-pressed={type === t.type} title={t.description} onClick={() => set("type", t.type)}>
            {s.resourceType[t.type]}
          </button>
        ))}
      </div>
      <div className="filters">
        <div className="field">
          <label htmlFor="f-search">{s.resources.search}</label>
          <input id="f-search" type="search" value={search} onChange={(e) => set("q", e.target.value)} />
        </div>
        {!archived && (
          <>
            <div className="field">
              <label htmlFor="f-label">{s.resources.filterLabel}</label>
              <input id="f-label" type="search" value={label} placeholder="tier=web" onChange={(e) => set("label", e.target.value)} />
            </div>
            <div className="field">
              <label htmlFor="f-phase">{s.resources.filterPhase}</label>
              <select id="f-phase" value={phase ?? ""} onChange={(e) => set("phase", e.target.value)}>
                <option value="">{s.common.all}</option>
                {phases.map((x) => (
                  <option key={x} value={x}>
                    {s.phase[x]}
                  </option>
                ))}
              </select>
            </div>
            <div className="field">
              <label htmlFor="f-health">{s.resources.filterHealth}</label>
              <select id="f-health" value={health} onChange={(e) => set("health", e.target.value)}>
                <option value="">{s.common.all}</option>
                {healths.map((x) => (
                  <option key={x} value={x}>
                    {s.health[x]}
                  </option>
                ))}
              </select>
            </div>
          </>
        )}
        <label className="check">
          <input type="checkbox" checked={archived} onChange={(e) => set("archived", e.target.checked ? "1" : "")} />
          {s.resources.archived}
        </label>
      </div>
      {archived ? (
        <>
          <p className="meta">{s.resources.archivedNote}</p>
          <ArchiveList type={type} search={search} />
        </>
      ) : (
        <Async q={q}>
          {(items) => {
            const shown = sortRows(
              items.filter((r) => nameMatches(r.name, search)),
              sort,
              resourceSort,
            );
            const th = (key: ResourceSortKey, label: string) => <SortTh sortKey={key} label={label} sort={sort} onSort={toggleSort} />;
            return shown.length === 0 ? (
              <Empty>{s.resources.empty}</Empty>
            ) : (
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      {th("name", s.resources.columns.name)}
                      {th("type", s.resources.columns.type)}
                      {th("phase", s.resources.columns.phase)}
                      {th("state", s.resources.columns.state)}
                      {th("health", s.resources.columns.health)}
                      {th("version", s.resources.columns.version)}
                      <th>{s.resources.columns.managed}</th>
                      <th>{s.resources.columns.labels}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {shown.map((r) => (
                      <ResourceRow key={r.id} r={r} />
                    ))}
                  </tbody>
                </table>
              </div>
            );
          }}
        </Async>
      )}
      {creating && types.data && <CreateDialog types={types.data} initialType={type} onClose={() => set("create", "")} />}
    </div>
  );
}
