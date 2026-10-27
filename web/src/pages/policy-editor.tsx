import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useDeferredValue, useEffect, useMemo, useState } from "react";
import { Link, useBlocker, useParams, useSearchParams } from "react-router";
import { ApiError, client, isApiError, unwrap, yamlBody } from "../api/client";
import { modulesQuery, nodeTypesQuery, policyQuery, qk } from "../api/queries";
import type { Issue, PolicyDetail, ValidationResult } from "../api/types";
import { ManagedBadges } from "../components/badges";
import { ConflictDialog } from "../components/conflict-dialog";
import { ConfirmDialog } from "../components/dialog";
import { PolicyGraph } from "../components/graph";
import { useToast } from "../components/toast";
import { ErrorBox, PageHead, Pill, Spinner, Tag } from "../components/ui";
import { IssueList } from "../components/validation";
import { YamlEditor } from "../components/yaml-editor-lazy";
import { docToYaml } from "../lib/yamldoc";
import { policyTemplate } from "../lib/templates";
import { addNode, connect, disconnect, removeNode, setLayout, uniqueNodeId } from "../policy/edit";
import { autoLayout, mapIssues, nextPosition } from "../policy/issues";
import type { Position } from "../policy/model";
import { s } from "../strings";
import { useEditorText } from "./policy/editor-state";
import { HistoryDialog } from "./policy/history-dialog";
import { NodePanel, PolicySettings } from "./policy/node-panel";
import { Palette } from "./policy/palette";
import { ManualRunDialog } from "./policy/run-dialog";

const NO_ISSUES: Issue[] = [];

interface Init {
  text: string;
  /** Stored version the text was loaded from; null for a policy that does not exist yet. */
  version: number | null;
}

async function fetchPolicyYaml(name: string): Promise<string> {
  return (await unwrap(client.GET("/export", { params: { query: { kind: "policy", name } }, parseAs: "text" }))) as string;
}

export function PolicyEditorPage() {
  const { name = "" } = useParams();
  const [params] = useSearchParams();
  const isNew = params.get("new") === "1";
  const detail = useQuery({ ...policyQuery(name), retry: false });
  const missing = detail.isError && isApiError(detail.error) && detail.error.status === 404;
  const yamlQ = useQuery({
    queryKey: ["policy", name, "yaml", detail.data?.version ?? 0],
    queryFn: () => fetchPolicyYaml(name),
    enabled: !!detail.data,
    staleTime: Infinity,
    placeholderData: keepPreviousData,
  });
  const [init, setInit] = useState<Init | null>(null);

  // Latch the loaded text once: later refetches (after a save, or a change by
  // someone else) must not replace what is being edited.
  if (!init) {
    if (detail.data && yamlQ.data !== undefined) setInit({ text: yamlQ.data, version: detail.data.version });
    else if (missing && isNew) setInit({ text: policyTemplate(name), version: null });
  }

  if (init) return <EditorBody key={name} name={name} init={init} />;
  if (detail.isError && !missing) {
    return (
      <div className="page">
        <ErrorBox error={detail.error} onRetry={() => void detail.refetch()} />
      </div>
    );
  }
  if (missing) {
    return (
      <div className="page">
        <ErrorBox error={new Error(s.editor.notFound)} />
        <p>
          <Link to="/policies">{s.policies.title}</Link>
        </p>
      </div>
    );
  }
  return (
    <div className="page">
      {yamlQ.isError ? <ErrorBox error={yamlQ.error} onRetry={() => void yamlQ.refetch()} /> : <Spinner />}
    </div>
  );
}

function StatusPills({ name, detail, version }: { name: string; detail: PolicyDetail | undefined; version: number | null }) {
  return (
    <div className="pills">
      <Tag>{name}</Tag>
      {version !== null ? <Tag>{s.config.version(version)}</Tag> : <Tag>{s.editor.newPolicy}</Tag>}
      {detail &&
        (detail.deployed ? (
          detail.paused ? (
            <Pill view={{ tone: "warn", icon: "‖", label: s.editor.statePaused }} />
          ) : (
            <Pill view={{ tone: "ok", icon: "●", label: s.editor.stateDeployed }} />
          )
        ) : (
          <Pill view={{ tone: "neutral", icon: "○", label: s.editor.stateNotDeployed }} />
        ))}
      {detail && <ManagedBadges managedBy={detail.managedBy} />}
    </div>
  );
}

function EditorBody({ name, init }: { name: string; init: Init }) {
  const qc = useQueryClient();
  const toast = useToast();
  const types = useQuery(nodeTypesQuery());
  // Warm the module list so module nodes validate without a later request.
  useQuery(modulesQuery());
  const detail = useQuery({ ...policyQuery(name), retry: false });

  const { state, setText, apply } = useEditorText(init.text);
  const [baseline, setBaseline] = useState(init.text);
  const [baseVersion, setBaseVersion] = useState<number | null>(init.version);
  const [selected, setSelected] = useState<string | null>(null);
  const [tab, setTab] = useState<"props" | "yaml">("props");
  const [result, setResult] = useState<{ issues: Issue[]; warnings: Issue[]; valid?: boolean; sig: string } | null>(null);
  const [dialog, setDialog] = useState<"run" | "history" | "undeploy" | null>(null);
  const [conflict, setConflict] = useState<{ current: number | undefined; stored: string } | null>(null);
  const [loadVersion, setLoadVersion] = useState<{ text: string; version: number } | null>(null);

  const dirty = state.text !== baseline;
  const model = useDeferredValue(state.model);
  const schemas = useMemo(() => new Map((types.data ?? []).map((t) => [t.type, t])), [types.data]);
  const positions = useMemo(() => (model ? { ...autoLayout(model), ...model.layout } : {}), [model]);

  // Server findings point into the node list by index, so they only apply
  // while the list is unchanged.
  const sig = model?.nodes.map((n) => n.id).join("\u0000") ?? "";
  const shown = result && result.sig === sig ? result : null;
  const compileWarnings = detail.data?.warnings ?? NO_ISSUES;
  const issueMap = useMemo(() => mapIssues(shown?.issues ?? [], model), [shown, model]);
  const warningMap = useMemo(() => mapIssues([...(shown?.warnings ?? []), ...compileWarnings], model), [shown, compileWarnings, model]);

  // Leaving with unsaved changes asks first, both inside the app and on tab close.
  const blocker = useBlocker(dirty);
  useEffect(() => {
    if (!dirty) return;
    const h = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener("beforeunload", h);
    return () => window.removeEventListener("beforeunload", h);
  }, [dirty]);

  const validate = useMutation({
    mutationFn: (): Promise<ValidationResult> => unwrap(client.POST("/validate", { body: state.text as never, ...yamlBody })),
    onSuccess: (r) => setResult({ issues: r.issues, warnings: r.warnings, valid: r.valid, sig }),
  });

  const save = useMutation({
    mutationFn: (ifMatch: number | null) =>
      unwrap(
        client.PUT("/policies/{p}", {
          params: { path: { p: name }, header: ifMatch === null ? {} : { "If-Match": String(ifMatch) } },
          body: state.text as never,
          ...yamlBody,
        }),
      ),
    onSuccess: (res) => {
      toast.success(s.editor.saved);
      setBaseline(state.text);
      setBaseVersion(res.version);
      setConflict(null);
      setResult({ issues: [], warnings: res.warnings, valid: true, sig });
      void qc.invalidateQueries({ queryKey: qk.policies() });
      void qc.invalidateQueries({ queryKey: qk.policy(name) });
      void qc.invalidateQueries({ queryKey: qk.policyVersions(name) });
    },
    onError: async (e) => {
      if (isApiError(e) && e.isConflict) {
        try {
          setConflict({ current: e.currentVersion, stored: await fetchPolicyYaml(name) });
        } catch (err) {
          setConflict({ current: e.currentVersion, stored: err instanceof ApiError ? err.message : "" });
        }
      } else if (isApiError(e) && e.issues.length > 0) {
        setResult({ issues: e.issues, warnings: [], valid: false, sig });
      }
    },
  });

  const pause = useMutation({
    mutationFn: (paused: boolean) => unwrap(client.POST(paused ? "/policies/{p}/resume" : "/policies/{p}/pause", { params: { path: { p: name } } })),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.policy(name) });
      void qc.invalidateQueries({ queryKey: qk.policies() });
    },
  });
  const undeploy = useMutation({
    mutationFn: () => unwrap(client.DELETE("/policies/{p}/deployment", { params: { path: { p: name } } })),
    onSuccess: () => {
      setDialog(null);
      void qc.invalidateQueries({ queryKey: qk.policy(name) });
      void qc.invalidateQueries({ queryKey: qk.policies() });
    },
  });

  function loadStored(text: string, version: number) {
    setText(text);
    setBaseline(text);
    setBaseVersion(version);
    setConflict(null);
    setResult(null);
    setSelected(null);
    save.reset();
  }

  function addNodeOf(type: string, at?: Position) {
    if (!model) return;
    const id = uniqueNodeId(type, model.nodes.map((n) => n.id));
    const pos = at ?? nextPosition(positions, selected ?? undefined);
    apply((t) => setLayout(addNode(t, { id, type }), { ...positions, [id]: pos }));
    setSelected(id);
    setTab("props");
  }

  const spec = state.parsed.ok ? ((state.parsed.doc.toJS() as { spec?: Record<string, unknown> }).spec ?? {}) : {};
  const selectedNode = model?.nodes.find((n) => n.id === selected);
  const triggers = model?.nodes.filter((n) => schemas.get(n.type)?.category === "trigger").map((n) => n.id) ?? [];
  const serverNewer = detail.data && baseVersion !== null && detail.data.version > baseVersion ? detail.data.version : null;
  const current = state.parsed.ok ? docToYaml(state.parsed.doc.toJS()) : "";

  const saveError = save.error && !(isApiError(save.error) && (save.error.isConflict || save.error.issues.length > 0)) ? save.error : null;

  return (
    <div className="page">
      <PageHead title={name} meta={<StatusPills name={name} detail={detail.data} version={baseVersion} />}>
        <button type="button" className="btn" disabled={validate.isPending || !state.parsed.ok} onClick={() => validate.mutate()}>
          {validate.isPending ? s.editor.validating : s.editor.validate}
        </button>
        <button type="button" className="btn primary" disabled={save.isPending || !state.parsed.ok || (!dirty && baseVersion !== null && !!detail.data?.deployed)} onClick={() => save.mutate(baseVersion)}>
          {save.isPending ? s.editor.saving : s.editor.save}
        </button>
        {baseVersion !== null && (
          <>
            <button type="button" className="btn" onClick={() => setDialog("run")}>
              {s.editor.run}
            </button>
            <button type="button" className="btn" onClick={() => setDialog("history")}>
              {s.editor.history}
            </button>
            {detail.data?.deployed && (
              <>
                <button type="button" className="btn" disabled={pause.isPending} onClick={() => pause.mutate(detail.data.paused)}>
                  {detail.data.paused ? s.editor.resume : s.editor.pause}
                </button>
                <button type="button" className="btn danger" onClick={() => setDialog("undeploy")}>
                  {s.editor.undeploy}
                </button>
              </>
            )}
          </>
        )}
      </PageHead>

      <div className="stack" style={{ marginBottom: 10 }}>
        {baseVersion === null && <div className="banner">{s.editor.newBanner}</div>}
        {dirty && <div className="banner warn">{s.editor.unsaved}</div>}
        {serverNewer !== null && <div className="banner warn">{s.editor.newerVersion(serverNewer)}</div>}
        {!state.parsed.ok && (
          <div className="banner error" role="alert">
            {s.editor.yamlError} <code>{state.parsed.error}</code>
          </div>
        )}
        <ErrorBox error={saveError ?? validate.error ?? pause.error ?? undeploy.error} />
      </div>

      <div className="editor-grid">
        {types.isPending ? <Spinner label={s.editor.schemaLoading} /> : <Palette types={types.data ?? []} onAdd={(t) => addNodeOf(t)} />}
        <div>
          {model ? (
            <PolicyGraph
              model={model}
              schemas={schemas}
              positions={positions}
              selected={selected}
              onSelect={setSelected}
              issuesByNode={issueMap.byNode}
              onMove={(moved) => apply((t) => setLayout(t, { ...positions, ...moved }))}
              onConnect={(from, port, to) => apply((t) => connect(t, from, port, to))}
              onDeleteEdge={(e) => apply((t) => disconnect(t, e.from, e.port, e.to))}
              onDeleteNode={(id) => {
                apply((t) => removeNode(t, id));
                setSelected((cur) => (cur === id ? null : cur));
              }}
              onDropType={(type, at) => addNodeOf(type, at)}
            />
          ) : (
            <ErrorBox error={new Error(state.parsed.ok ? s.editor.yamlError : state.parsed.error)} />
          )}
        </div>
        <div className="panel side-pane">
          <div className="tabs" style={{ margin: "0 0 8px" }}>
            <button type="button" aria-selected={tab === "props"} onClick={() => setTab("props")}>
              {s.editor.tabProps}
            </button>
            <button type="button" aria-selected={tab === "yaml"} onClick={() => setTab("yaml")}>
              {s.editor.tabYaml}
            </button>
          </div>
          <div className="body">
            {tab === "yaml" ? (
              <YamlEditor value={state.text} onChange={setText} ariaLabel={s.common.policyYaml} minHeight={380} />
            ) : selectedNode ? (
              <NodePanel
                node={selectedNode}
                schema={schemas.get(selectedNode.type)}
                issues={issueMap.byNode.get(selectedNode.id) ?? []}
                allIds={model?.nodes.map((n) => n.id) ?? []}
                apply={apply}
                onRename={(from, to) => setSelected((cur) => (cur === from ? to : cur))}
                onDelete={() => {
                  apply((t) => removeNode(t, selectedNode.id));
                  setSelected(null);
                }}
              />
            ) : model ? (
              <PolicySettings model={model} spec={spec} apply={apply} issues={issueMap.document} />
            ) : null}
          </div>
        </div>
      </div>

      <h3>{s.editor.results}</h3>
      <div className="stack" aria-live="polite">
        {shown && shown.valid !== undefined && <div className={`banner ${shown.valid ? "ok" : "error"}`}>{shown.valid ? s.editor.valid : s.editor.invalid}</div>}
        {issueMap.all.length > 0 && (
          <div className="banner error">
            {s.editor.issues}
            <ul>
              {issueMap.all.map((m, i) => (
                <li key={i}>
                  {m.nodeId && (
                    <button type="button" className="btn link" title={s.editor.clickToSelect} onClick={() => setSelected(m.nodeId ?? null)}>
                      {m.nodeId}
                    </button>
                  )}{" "}
                  {m.issue.path && <code>{m.issue.path}</code>}
                  {m.issue.path ? ": " : ""}
                  {m.issue.message}
                </li>
              ))}
            </ul>
          </div>
        )}
        {warningMap.all.length > 0 && (
          <div className="banner warn">
            {s.editor.warnings}
            <IssueList issues={warningMap.all.map((m) => m.issue)} />
          </div>
        )}
        {!shown && compileWarnings.length === 0 && <p className="meta">{s.editor.noIssues}</p>}
      </div>

      {dialog === "run" && <ManualRunDialog policy={name} triggers={triggers} deployed={!!detail.data?.deployed} onClose={() => setDialog(null)} />}
      {dialog === "history" && (
        <HistoryDialog
          name={name}
          deployedVersion={detail.data?.deployedVersion ?? null}
          current={current}
          onClose={() => setDialog(null)}
          onLoad={(text, version) => {
            setLoadVersion({ text, version });
            setDialog(null);
          }}
        />
      )}
      {dialog === "undeploy" && (
        <ConfirmDialog title={s.editor.undeploy} confirmLabel={s.editor.undeploy} body={s.editor.confirmUndeploy} danger busy={undeploy.isPending} error={undeploy.error} onClose={() => setDialog(null)} onConfirm={() => undeploy.mutate()} />
      )}
      {loadVersion && (
        <ConfirmDialog
          title={s.editor.loadVersion}
          confirmLabel={s.editor.loadVersion}
          body={s.editor.loadConfirm(loadVersion.version)}
          onClose={() => setLoadVersion(null)}
          onConfirm={() => {
            // The loaded text becomes an unsaved edit on top of the current stored version.
            setText(loadVersion.text);
            setSelected(null);
            setLoadVersion(null);
          }}
        />
      )}
      {conflict && (
        <ConflictDialog
          baseVersion={baseVersion ?? 0}
          currentVersion={conflict.current}
          stored={conflict.stored}
          mine={state.text}
          busy={save.isPending}
          error={saveError}
          onReload={() => conflict.current !== undefined && loadStored(conflict.stored, conflict.current)}
          onOverwrite={() => conflict.current !== undefined && save.mutate(conflict.current)}
          onClose={() => setConflict(null)}
        />
      )}
      {blocker.state === "blocked" && (
        <ConfirmDialog title={s.editor.unsaved} confirmLabel={s.editor.leave} body={s.editor.unsavedLeave} danger onClose={() => blocker.reset()} onConfirm={() => blocker.proceed()} />
      )}
    </div>
  );
}
