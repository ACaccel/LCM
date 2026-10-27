import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { Link, useParams } from "react-router";
import { client, isApiError, unwrap } from "../api/client";
import { nodeTypesQuery, policyVersionQuery, qk, runQuery } from "../api/queries";
import type { Run, RunNode } from "../api/types";
import { ConfirmDialog, Dialog } from "../components/dialog";
import { PolicyGraph } from "../components/graph";
import { useToast } from "../components/toast";
import { ErrorBox, PageHead, Pill, Spinner, Tag } from "../components/ui";
import { formatDateTime, formatDuration, spanMs } from "../lib/format";
import { useNow } from "../hooks";
import { nodeStatusView, RUN_ACTIVE, runStatusView } from "../lib/status";
import { autoLayout } from "../policy/issues";
import { buildModel } from "../policy/model";
import { statusByTopLevelNode, takenEdgeIds } from "../policy/runview";
import { s } from "../strings";

function Json({ value }: { value: unknown }) {
  return <pre className="block">{JSON.stringify(value, null, 2)}</pre>;
}

function ResumeDialog({ run, node, onClose }: { run: Run; node: RunNode; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [payload, setPayload] = useState("");
  const [bad, setBad] = useState(false);
  const m = useMutation({
    mutationFn: () => {
      let parsed: unknown;
      if (payload.trim()) {
        try {
          parsed = JSON.parse(payload);
        } catch {
          setBad(true);
          throw new Error(s.common.invalidJson);
        }
      }
      setBad(false);
      // The scoped node id contains slashes; the client percent-encodes it.
      return unwrap(
        client.POST("/runs/{id}/nodes/{node}/resume", {
          params: { path: { id: run.id, node: node.id } },
          body: { key: node.suspendKey ?? "", ...(parsed === undefined ? {} : { payload: parsed }) },
        }),
      );
    },
    onSuccess: () => {
      toast.success(s.run.resumed);
      void qc.invalidateQueries({ queryKey: qk.run(run.id) });
      onClose();
    },
  });
  return (
    <Dialog
      title={s.run.resumeTitle}
      onClose={onClose}
      footer={
        <>
          <button type="button" className="btn" onClick={onClose}>
            {s.common.cancel}
          </button>
          <button type="button" className="btn primary" disabled={m.isPending} onClick={() => m.mutate()}>
            {s.run.resume}
          </button>
        </>
      }
    >
      <div className="stack">
        <p>
          <code>{node.id}</code>
        </p>
        <div className="field">
          <label htmlFor="resume-payload">{s.run.resumePayload}</label>
          <textarea id="resume-payload" rows={4} value={payload} aria-invalid={bad || undefined} onChange={(e) => setPayload(e.target.value)} spellCheck={false} />
        </div>
        <ErrorBox error={m.error} />
      </div>
    </Dialog>
  );
}

function NodeDetail({ run, node, onResume }: { run: Run; node: RunNode; onResume: () => void }) {
  const view = nodeStatusView(node.status);
  return (
    <div className="stack">
      <div className="row between">
        <h2 style={{ overflowWrap: "anywhere" }}>{node.id}</h2>
        <Pill view={view} />
      </div>
      <dl className="kv">
        <dt>{s.editor.nodeType}</dt>
        <dd>
          <code>{node.type}</code>
        </dd>
        {node.attempts !== undefined && (
          <>
            <dt>{s.run.attempts}</dt>
            <dd>{node.attempts}</dd>
          </>
        )}
        {node.port && (
          <>
            <dt>{s.run.port}</dt>
            <dd>
              <code>{node.port}</code>
            </dd>
          </>
        )}
        {node.startedAt && (
          <>
            <dt>{s.run.started}</dt>
            <dd>{formatDateTime(node.startedAt)}</dd>
          </>
        )}
        {node.finishedAt && (
          <>
            <dt>{s.run.finished}</dt>
            <dd>
              {formatDateTime(node.finishedAt)} ({formatDuration(spanMs(node.startedAt, node.finishedAt))})
            </dd>
          </>
        )}
        {node.suspendKey && (
          <>
            <dt>{s.run.suspendKey}</dt>
            <dd>
              <code>{node.suspendKey}</code>
            </dd>
          </>
        )}
        {node.resumeAt && (
          <>
            <dt>{s.run.resumeAt}</dt>
            <dd>{formatDateTime(node.resumeAt)}</dd>
          </>
        )}
      </dl>
      {node.error && <ErrorBox error={new Error(node.error)} />}
      {node.status === "suspended" && node.suspendKey && RUN_ACTIVE.has(run.status) && (
        <div>
          <button type="button" className="btn primary" onClick={onResume}>
            {s.run.resume}
          </button>
        </div>
      )}
      {node.props !== undefined && (
        <div>
          <h3 style={{ marginTop: 0 }}>{s.run.props}</h3>
          <Json value={node.props} />
        </div>
      )}
      {node.output !== undefined && (
        <div>
          <h3 style={{ marginTop: 0 }}>{s.run.output}</h3>
          <Json value={node.output} />
        </div>
      )}
    </div>
  );
}

export function RunDetailPage() {
  const { id = "" } = useParams();
  const runQ = useQuery(runQuery(id));
  const run = runQ.data;
  const now = useNow(!!run && RUN_ACTIVE.has(run.status));
  const version = useQuery({ ...policyVersionQuery(run?.policy ?? "", run?.version ?? 1), enabled: !!run });
  const types = useQuery(nodeTypesQuery());
  const [selected, setSelected] = useState<string | null>(null);
  const [cancelling, setCancelling] = useState(false);
  const [resuming, setResuming] = useState(false);
  const qc = useQueryClient();
  const toast = useToast();

  const cancel = useMutation({
    mutationFn: () => unwrap(client.POST("/runs/{id}/cancel", { params: { path: { id } } })),
    onSuccess: () => {
      toast.success(s.run.cancelRequested);
      setCancelling(false);
      void qc.invalidateQueries({ queryKey: qk.run(id) });
    },
  });

  const model = useMemo(() => {
    const m = version.data ? buildModel(version.data.document) : undefined;
    return typeof m === "string" ? undefined : m;
  }, [version.data]);
  const positions = useMemo(() => (model ? { ...autoLayout(model), ...model.layout } : {}), [model]);
  const schemas = useMemo(() => new Map((types.data ?? []).map((t) => [t.type, t])), [types.data]);
  const statusById = useMemo(() => (model && run ? statusByTopLevelNode(model, run.nodes) : undefined), [model, run]);
  const taken = useMemo(() => (model && run ? takenEdgeIds(model, run.nodes) : undefined), [model, run]);

  if (runQ.isPending) {
    return (
      <div className="page">
        <Spinner />
      </div>
    );
  }
  if (runQ.isError || !run) {
    const nf = isApiError(runQ.error) && runQ.error.status === 404;
    return (
      <div className="page">
        <ErrorBox error={nf ? new Error(s.run.notFound) : runQ.error} onRetry={() => void runQ.refetch()} />
      </div>
    );
  }

  const active = RUN_ACTIVE.has(run.status);
  const scoped = run.nodes.filter((n) => n.id.includes("/"));
  const node = run.nodes.find((n) => n.id === selected);
  // A module node has no entry of its own; show its parts instead.
  const parts = selected ? run.nodes.filter((n) => n.id.startsWith(`${selected}/`)) : [];

  return (
    <div className="page">
      <PageHead
        title={`${s.runs.title} ${run.id.slice(0, 8)}`}
        meta={
          <div className="pills">
            <Pill view={runStatusView(run.status)} />
            <Tag>{s.runMode[run.mode]}</Tag>
            <Link className="pill neutral" to={`/policies/${encodeURIComponent(run.policy)}`}>
              {run.policy} · {s.run.policyVersion} {run.version}
            </Link>
            <Tag>{formatDateTime(run.createdAt)}</Tag>
            {run.startedAt && <Tag>{formatDuration(spanMs(run.startedAt, run.finishedAt, now))}</Tag>}
          </div>
        }
      >
        {active && (
          <button type="button" className="btn danger" onClick={() => setCancelling(true)}>
            {s.run.cancel}
          </button>
        )}
      </PageHead>
      {run.error && <ErrorBox error={new Error(run.error)} />}

      <div className="editor-grid" style={{ gridTemplateColumns: "minmax(0, 1fr) 380px", height: "calc(100vh - 260px)" }}>
        <div style={{ minWidth: 0 }}>
          {version.isError ? (
            <ErrorBox error={version.error} onRetry={() => void version.refetch()} />
          ) : model ? (
            <PolicyGraph
              model={model}
              schemas={schemas}
              positions={positions}
              selected={selected}
              onSelect={setSelected}
              statusById={statusById}
              takenEdges={taken}
            />
          ) : (
            <Spinner />
          )}
        </div>
        <div className="panel side-pane">
          <div className="body">
            {node ? (
              <NodeDetail run={run} node={node} onResume={() => setResuming(true)} />
            ) : parts.length > 0 ? (
              <div className="stack">
                <h2>{selected}</h2>
                <p className="meta">{s.editor.moduleNote}</p>
                <ul>
                  {parts.map((p) => (
                    <li key={p.id}>
                      <button type="button" className="btn link" onClick={() => setSelected(p.id)}>
                        {p.id}
                      </button>{" "}
                      <Pill view={nodeStatusView(p.status)} />
                    </li>
                  ))}
                </ul>
              </div>
            ) : (
              <p className="meta">{s.run.selectNode}</p>
            )}
            {scoped.length > 0 && !selected && (
              <>
                <h3>{s.editor.moduleNote}</h3>
                <ul>
                  {scoped.map((p) => (
                    <li key={p.id}>
                      <button type="button" className="btn link" onClick={() => setSelected(p.id)}>
                        {p.id}
                      </button>{" "}
                      <Pill view={nodeStatusView(p.status)} />
                    </li>
                  ))}
                </ul>
              </>
            )}
            <h3>{s.run.event}</h3>
            <Json value={run.event} />
          </div>
        </div>
      </div>

      {cancelling && (
        <ConfirmDialog
          title={s.run.cancel}
          confirmLabel={s.run.cancel}
          body={s.run.confirmCancel}
          danger
          busy={cancel.isPending}
          error={cancel.error}
          onClose={() => setCancelling(false)}
          onConfirm={() => cancel.mutate()}
        />
      )}
      {resuming && node && <ResumeDialog run={run} node={node} onClose={() => setResuming(false)} />}
    </div>
  );
}
