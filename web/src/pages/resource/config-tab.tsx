import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { ApiError, client, isApiError, unwrap, yamlBody } from "../../api/client";
import { qk, resourceVersionQuery, resourceVersionsQuery, resourceYamlQuery } from "../../api/queries";
import type { ResourceDetail } from "../../api/types";
import { ManagedBadges } from "../../components/badges";
import { ConflictDialog } from "../../components/conflict-dialog";
import { ConfirmDialog, Dialog } from "../../components/dialog";
import { DiffView } from "../../components/diff-view";
import { useToast } from "../../components/toast";
import { Async, ErrorBox, Spinner } from "../../components/ui";
import { ValidationReport } from "../../components/validation";
import { YamlEditor } from "../../components/yaml-editor-lazy";
import { formatTime } from "../../lib/format";
import { docToYaml } from "../../lib/yamldoc";
import { s } from "../../strings";

/** How the console addresses a resource: archives only by their immutable ID. */
export function resourceRef(detail: Pick<ResourceDetail, "id" | "name" | "phase">): string {
  return detail.phase === "unmanaged" ? `@${detail.id}` : detail.name;
}

function exportYaml(name: string): Promise<string> {
  return unwrap(client.GET("/export", { params: { query: { kind: "resource", name } }, parseAs: "text" })) as Promise<string>;
}

function Ownership({ detail }: { detail: ResourceDetail }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [confirm, setConfirm] = useState<"git" | "current" | "release" | null>(null);
  const [exported, setExported] = useState<string | null>(null);
  const m = detail.managedBy;
  const name = detail.name;

  const done = () => {
    void qc.invalidateQueries({ queryKey: qk.resource(name) });
    void qc.invalidateQueries({ queryKey: qk.resourcesAll() });
    void qc.invalidateQueries({ queryKey: qk.ownership() });
    setConfirm(null);
  };
  const resolve = useMutation({
    mutationFn: (use: "git" | "current") => unwrap(client.POST("/config/ownership/{kind}/{name}/resolve", { params: { path: { kind: "resource", name } }, body: { use } })),
    onSuccess: () => {
      toast.success(s.config.saved);
      done();
    },
  });
  const release = useMutation({
    mutationFn: () => unwrap(client.DELETE("/config/ownership/{kind}/{name}", { params: { path: { kind: "resource", name } } })),
    onSuccess: done,
  });
  const exportM = useMutation({
    mutationFn: () => exportYaml(name),
    onSuccess: setExported,
  });

  return (
    <div className="panel">
      <div className="title">
        <span>{s.config.ownershipTitle}</span>
      </div>
      <div className="stack">
        <div className="row" style={{ gap: 4 }}>
          <ManagedBadges managedBy={m} />
        </div>
        {m && (
          <dl className="kv">
            <dt>{s.config.source}</dt>
            <dd>{m.source}</dd>
            <dt>{s.config.path}</dt>
            <dd>
              <code>{m.path}</code>
            </dd>
            <dt>{s.config.commit}</dt>
            <dd>
              <code>{m.commit.slice(0, 7)}</code>
            </dd>
          </dl>
        )}
        {m?.conflict ? <div className="banner warn">{s.config.conflictNote}</div> : m?.drifted ? <div className="banner">{s.config.driftNote}</div> : null}
        <div className="row">
          {m && (m.drifted || m.conflict) && (
            <>
              <button type="button" className="btn" onClick={() => setConfirm("git")}>
                {s.config.resolveGit}
              </button>
              <button type="button" className="btn" onClick={() => setConfirm("current")}>
                {s.config.resolveCurrent}
              </button>
            </>
          )}
          {m && (
            <button type="button" className="btn danger" onClick={() => setConfirm("release")}>
              {s.config.release}
            </button>
          )}
          <button type="button" className="btn" disabled={exportM.isPending} onClick={() => exportM.mutate()}>
            {exportM.isPending ? s.config.exporting : s.config.exportYaml}
          </button>
        </div>
        <ErrorBox error={exportM.error} />
      </div>
      {confirm && (
        <ConfirmDialog
          title={confirm === "git" ? s.config.resolveGit : confirm === "current" ? s.config.resolveCurrent : s.config.release}
          confirmLabel={confirm === "git" ? s.config.resolveGit : confirm === "current" ? s.config.resolveCurrent : s.config.release}
          body={confirm === "git" ? s.config.confirmGit : confirm === "current" ? s.config.confirmCurrent : s.config.confirmRelease}
          danger={confirm !== "current"}
          busy={resolve.isPending || release.isPending}
          error={resolve.error ?? release.error}
          onClose={() => setConfirm(null)}
          onConfirm={() => (confirm === "release" ? release.mutate() : resolve.mutate(confirm))}
        />
      )}
      {exported !== null && (
        <Dialog
          title={s.config.exportYaml}
          wide
          onClose={() => setExported(null)}
          footer={
            <>
              <button type="button" className="btn" onClick={() => void navigator.clipboard?.writeText(exported).then(() => toast.success(s.common.copied))}>
                {s.common.copy}
              </button>
              <button type="button" className="btn primary" onClick={() => setExported(null)}>
                {s.common.close}
              </button>
            </>
          }
        >
          <pre className="block" style={{ maxHeight: "60vh" }}>
            {exported}
          </pre>
        </Dialog>
      )}
    </div>
  );
}

function History({ detail, readOnly, onRestored }: { detail: ResourceDetail; readOnly: boolean; onRestored: () => void }) {
  const ref = resourceRef(detail);
  const q = useQuery(resourceVersionsQuery(ref));
  const [compare, setCompare] = useState<number | null>(null);
  // The version the confirmation was shown for: a change underneath it is a conflict, not a silent restore over it.
  const [restore, setRestore] = useState<{ version: number; base: number } | null>(null);
  const qc = useQueryClient();
  const toast = useToast();

  const restoreM = useMutation({
    mutationFn: (r: { version: number; base: number }) =>
      unwrap(client.POST("/resources/{resource}/restore", { params: { path: { resource: ref }, header: { "If-Match": String(r.base) } }, body: { version: r.version } })),
    onSuccess: () => {
      toast.success(s.config.restored);
      setRestore(null);
      void qc.invalidateQueries({ queryKey: qk.resourceAll(ref) });
      void qc.invalidateQueries({ queryKey: qk.resourcesAll() });
      onRestored();
    },
  });

  return (
    <div className="panel">
      <div className="title">
        <span>{s.config.history}</span>
      </div>
      <Async q={q}>
        {(data) => (
          <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
            {data.items.map((v) => (
              <li key={v.version} className="row between" style={{ padding: "4px 0", borderBottom: "1px solid var(--border)" }}>
                <span>
                  <b>{s.config.version(v.version)}</b>{" "}
                  <span className="meta">
                    {formatTime(v.createdAt)} · {v.createdBy.name}
                    {v.restoredFrom ? ` · ${s.config.restoredFrom(v.restoredFrom)}` : ""}
                  </span>
                </span>
                {v.version === detail.version ? (
                  <span className="meta">{s.config.current}</span>
                ) : (
                  <span className="row">
                    <button type="button" className="btn small" onClick={() => setCompare(v.version)}>
                      {s.config.compare}
                    </button>
                    {!readOnly && detail.phase === "managed" && (
                      <button type="button" className="btn small" onClick={() => setRestore({ version: v.version, base: detail.version })}>
                        {s.config.restore}
                      </button>
                    )}
                  </span>
                )}
              </li>
            ))}
          </ul>
        )}
      </Async>
      {compare !== null && <CompareDialog resource={ref} version={compare} current={detail} onClose={() => setCompare(null)} />}
      {restore !== null && (
        <ConfirmDialog
          title={s.config.restore}
          confirmLabel={s.config.restore}
          body={s.config.confirmRestore(restore.version)}
          busy={restoreM.isPending}
          error={restoreM.error}
          onClose={() => !restoreM.isPending && setRestore(null)}
          onConfirm={() => restoreM.mutate(restore)}
        />
      )}
    </div>
  );
}

function CompareDialog({ resource, version, current, onClose }: { resource: string; version: number; current: ResourceDetail; onClose: () => void }) {
  const q = useQuery(resourceVersionQuery(resource, version));
  return (
    <Dialog title={s.config.diffAgainst(version)} wide onClose={onClose} footer={<button type="button" className="btn primary" onClick={onClose}>{s.common.close}</button>}>
      {q.isPending ? <Spinner /> : q.isError ? <ErrorBox error={q.error} /> : <DiffView before={docToYaml(q.data.document)} after={docToYaml(current.document)} />}
    </Dialog>
  );
}

/** Read-only document and versions of an archived resource. */
function ArchivedConfig({ detail }: { detail: ResourceDetail }) {
  return (
    <div className="two">
      <div className="stack">
        <h2>
          {s.config.yaml} · {s.config.version(detail.version)}
        </h2>
        <p className="meta">{s.config.readOnly}</p>
        <pre className="block">{docToYaml(detail.document)}</pre>
      </div>
      <History detail={detail} readOnly onRestored={() => undefined} />
    </div>
  );
}

/**
 * The document editor of any resource type: YAML validated by the server,
 * saved with If-Match against the version the draft started from, plus the
 * Git ownership and the version history.
 */
export function ConfigTab({ detail }: { detail: ResourceDetail }) {
  if (detail.phase === "unmanaged") return <ArchivedConfig detail={detail} />;
  return <Editor detail={detail} />;
}

function Editor({ detail }: { detail: ResourceDetail }) {
  const name = detail.name;
  const qc = useQueryClient();
  const toast = useToast();
  const yamlQ = useQuery(resourceYamlQuery(name, detail.version));
  // The version a draft started from is kept so that saving after someone
  // else's change is detected by the server instead of overwriting it.
  const [draft, setDraft] = useState<{ text: string; version: number } | null>(null);
  const [conflict, setConflict] = useState<{ current: number | undefined; stored: string } | null>(null);

  const base = yamlQ.data ?? "";
  const text = draft?.text ?? base;
  const baseVersion = draft?.version ?? detail.version;
  const dirty = draft !== null && (draft.text !== base || draft.version !== detail.version);

  const validate = useMutation({ mutationFn: () => unwrap(client.POST("/validate", { body: text as never, ...yamlBody })) });
  const save = useMutation({
    mutationFn: (ifMatch: number) =>
      unwrap(client.PUT("/resources/{resource}", { params: { path: { resource: name }, header: { "If-Match": String(ifMatch) } }, body: text as never, ...yamlBody })),
    onSuccess: () => {
      toast.success(s.config.saved);
      setDraft(null);
      setConflict(null);
      void qc.invalidateQueries({ queryKey: qk.resourceAll(name) });
      void qc.invalidateQueries({ queryKey: qk.resourcesAll() });
    },
    onError: async (e) => {
      if (isApiError(e) && e.isConflict) {
        // Fetch the stored text so the dialog can show what changed underneath the edit.
        try {
          setConflict({ current: e.currentVersion, stored: await exportYaml(name) });
        } catch (err) {
          setConflict({ current: e.currentVersion, stored: err instanceof ApiError ? err.message : "" });
        }
      }
    },
  });

  function reloadLatest() {
    setDraft(null);
    setConflict(null);
    save.reset();
    void qc.invalidateQueries({ queryKey: qk.resource(name) });
  }

  return (
    <div className="stack">
      <div className="two">
        <div className="stack">
          <div className="row between">
            <h2>
              {s.config.yaml} · {s.config.version(detail.version)}
            </h2>
            {dirty && <span className="meta">{s.config.unsaved}</span>}
          </div>
          {yamlQ.isPending ? (
            <Spinner />
          ) : yamlQ.isError ? (
            <ErrorBox error={yamlQ.error} onRetry={() => void yamlQ.refetch()} />
          ) : (
            <YamlEditor value={text} onChange={(t) => setDraft((d) => ({ text: t, version: d?.version ?? detail.version }))} ariaLabel={s.common.resourceYaml} minHeight={320} />
          )}
          {validate.data && <ValidationReport {...validate.data} />}
          <ErrorBox error={validate.error ?? (conflict ? null : save.error)} />
          <div className="row">
            <button type="button" className="btn" disabled={!dirty || validate.isPending} onClick={() => validate.mutate()}>
              {s.resources.validate}
            </button>
            <button type="button" className="btn primary" disabled={!dirty || save.isPending} onClick={() => save.mutate(baseVersion)}>
              {s.config.saveVersion}
            </button>
            {dirty && (
              <button type="button" className="btn" onClick={() => setDraft(null)}>
                {s.common.cancel}
              </button>
            )}
          </div>
        </div>
        <div className="stack">
          <Ownership detail={detail} />
          <History detail={detail} readOnly={false} onRestored={() => setDraft(null)} />
        </div>
      </div>
      {conflict && (
        <ConflictDialog
          baseVersion={baseVersion}
          currentVersion={conflict.current}
          stored={conflict.stored}
          mine={text}
          busy={save.isPending}
          error={save.error && !(isApiError(save.error) && save.error.isConflict) ? save.error : undefined}
          onReload={reloadLatest}
          onOverwrite={() => conflict.current !== undefined && save.mutate(conflict.current)}
          onClose={() => setConflict(null)}
        />
      )}
    </div>
  );
}
