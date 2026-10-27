import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { client, unwrap } from "../../api/client";
import { qk, sourcesQuery } from "../../api/queries";
import type { ConfigSource, ConfigSourceRequest } from "../../api/types";
import { ConfirmDialog, Dialog } from "../../components/dialog";
import { useToast } from "../../components/toast";
import { Async, Empty, ErrorBox, Field, Pill } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { s } from "../../strings";

function SourceDialog({ source, onClose }: { source: ConfigSource | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [form, setForm] = useState<Required<ConfigSourceRequest>>({
    name: source?.name ?? "",
    repo: source?.repo ?? "",
    branch: source?.branch ?? "main",
    path: source?.path ?? ".",
    interval: source?.interval ?? "1m",
    enabled: source?.enabled ?? true,
  });
  const set = <K extends keyof typeof form>(k: K, v: (typeof form)[K]) => setForm((f) => ({ ...f, [k]: v }));
  const m = useMutation({
    mutationFn: () =>
      source
        ? unwrap(client.PUT("/config/sources/{name}", { params: { path: { name: source.name } }, body: form }))
        : unwrap(client.POST("/config/sources", { body: form })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.sources() });
      onClose();
    },
  });
  function submit(e: FormEvent) {
    e.preventDefault();
    m.mutate();
  }
  return (
    <Dialog title={source ? s.settings.sources.edit : s.settings.sources.add} onClose={onClose}>
      <form className="stack" onSubmit={submit}>
        <Field label={s.settings.sources.name} htmlFor="src-name">
          <input id="src-name" type="text" value={form.name} disabled={!!source} onChange={(e) => set("name", e.target.value)} required data-autofocus />
        </Field>
        <Field label={s.settings.sources.repo} htmlFor="src-repo">
          <input id="src-repo" type="text" value={form.repo} onChange={(e) => set("repo", e.target.value)} required spellCheck={false} />
        </Field>
        <div className="row">
          <Field label={s.settings.sources.branch} htmlFor="src-branch">
            <input id="src-branch" type="text" value={form.branch} onChange={(e) => set("branch", e.target.value)} />
          </Field>
          <Field label={s.settings.sources.path} htmlFor="src-path">
            <input id="src-path" type="text" value={form.path} onChange={(e) => set("path", e.target.value)} />
          </Field>
          <Field label={s.settings.sources.interval} htmlFor="src-interval">
            <input id="src-interval" type="text" value={form.interval} onChange={(e) => set("interval", e.target.value)} />
          </Field>
        </div>
        <label className="check">
          <input type="checkbox" checked={form.enabled} onChange={(e) => set("enabled", e.target.checked)} />
          {s.settings.sources.enabled}
        </label>
        <ErrorBox error={m.error} />
        <div className="row end">
          <button type="button" className="btn" onClick={onClose}>
            {s.common.cancel}
          </button>
          <button type="submit" className="btn primary" disabled={m.isPending}>
            {source ? s.common.save : s.common.create}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

export function SourcesPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const q = useQuery({
    ...sourcesQuery(),
    // A sync runs in the background and has no event; poll only while one is running.
    refetchInterval: (query) => (query.state.data?.some((x) => x.syncing) ? 1500 : false),
  });
  const [editing, setEditing] = useState<ConfigSource | "new" | null>(null);
  const [deleting, setDeleting] = useState<ConfigSource | null>(null);
  const requested = useRef(new Set<string>());

  const sync = useMutation({
    mutationFn: (name: string) => unwrap(client.POST("/config/sources/{name}/sync", { params: { path: { name } } })),
    onMutate: (name) => {
      requested.current.add(name);
    },
    onSuccess: (src) => qc.setQueryData<ConfigSource[]>(qk.sources(), (list) => list?.map((x) => (x.name === src.name ? src : x))),
  });
  const del = useMutation({
    mutationFn: (name: string) => unwrap(client.DELETE("/config/sources/{name}", { params: { path: { name } } })),
    onSuccess: () => {
      setDeleting(null);
      void qc.invalidateQueries({ queryKey: qk.sources() });
      void qc.invalidateQueries({ queryKey: qk.ownership() });
    },
  });

  // Report the outcome of syncs started here once they stop running.
  useEffect(() => {
    for (const src of q.data ?? []) {
      if (!requested.current.has(src.name) || src.syncing) continue;
      requested.current.delete(src.name);
      if (src.lastError) toast.error(`${s.settings.sources.syncFailed}: ${src.lastError}`);
      else toast.success(s.settings.sources.syncDone);
      void qc.invalidateQueries({ queryKey: qk.ownership() });
      void qc.invalidateQueries({ queryKey: qk.servicesAll() });
      void qc.invalidateQueries({ queryKey: qk.policies() });
    }
  }, [q.data, toast, qc]);

  return (
    <div className="stack">
      <div className="row between">
        <p className="meta" style={{ margin: 0 }}>
          {s.settings.sources.help}
        </p>
        <button type="button" className="btn primary" onClick={() => setEditing("new")}>
          {s.settings.sources.add}
        </button>
      </div>
      <ErrorBox error={sync.error} />
      <Async q={q}>
        {(items) =>
          items.length === 0 ? (
            <Empty>{s.settings.sources.empty}</Empty>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th>{s.settings.sources.name}</th>
                    <th>{s.settings.sources.repo}</th>
                    <th>{s.settings.sources.lastCommit}</th>
                    <th>{s.settings.sources.lastSync}</th>
                    <th>{s.common.status}</th>
                    <th>{s.common.actions}</th>
                  </tr>
                </thead>
                <tbody>
                  {items.map((src) => (
                    <tr key={src.name}>
                      <td>{src.name}</td>
                      <td>
                        <code>{src.repo}</code>
                        <div className="meta">
                          {src.branch} · <code>{src.path}</code> · {src.interval}
                          {!src.enabled && ` · ${s.common.none}`}
                        </div>
                      </td>
                      <td>
                        <code>{src.lastCommit ? src.lastCommit.slice(0, 7) : "-"}</code>
                      </td>
                      <td>{src.lastSyncAt ? formatDateTime(src.lastSyncAt) : s.settings.sources.neverSynced}</td>
                      <td>
                        {src.syncing ? (
                          <Pill view={{ tone: "info", icon: "↻", label: s.settings.sources.syncing }} />
                        ) : src.lastError ? (
                          <Pill view={{ tone: "bad", icon: "✕", label: s.settings.sources.syncFailed }} title={src.lastError} />
                        ) : src.lastSyncAt ? (
                          <Pill view={{ tone: "ok", icon: "✓", label: s.settings.sources.syncDone }} />
                        ) : (
                          <Pill view={{ tone: "neutral", icon: "○", label: s.settings.sources.neverSynced }} />
                        )}
                        {src.lastError && !src.syncing && <div className="meta">{src.lastError}</div>}
                      </td>
                      <td>
                        <div className="row" style={{ gap: 4 }}>
                          <button type="button" className="btn small" disabled={src.syncing || sync.isPending} onClick={() => sync.mutate(src.name)}>
                            {s.settings.sources.sync}
                          </button>
                          <button type="button" className="btn small" onClick={() => setEditing(src)}>
                            {s.common.edit}
                          </button>
                          <button type="button" className="btn small danger" onClick={() => setDeleting(src)}>
                            {s.common.delete}
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
      {editing && <SourceDialog source={editing === "new" ? null : editing} onClose={() => setEditing(null)} />}
      {deleting && (
        <ConfirmDialog
          title={s.common.delete}
          confirmLabel={s.common.delete}
          body={s.settings.sources.confirmDelete(deleting.name)}
          danger
          busy={del.isPending}
          error={del.error}
          onClose={() => setDeleting(null)}
          onConfirm={() => del.mutate(deleting.name)}
        />
      )}
    </div>
  );
}
