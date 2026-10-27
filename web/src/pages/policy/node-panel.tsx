import { useState } from "react";
import type { NodeType } from "../../api/types";
import { PropsForm } from "../../components/props-form";
import { IssueList } from "../../components/validation";
import { renameNode, setNodeField, setPath, setProps, setRetry } from "../../policy/edit";
import type { MappedIssue } from "../../policy/issues";
import type { ModelNode, PolicyModel } from "../../policy/model";
import { formatAny, parseAny } from "../../policy/values";
import { s } from "../../strings";

type Apply = (edit: (text: string) => string) => void;

/** A text input that commits on blur or Enter, for fields that rewrite structure. */
function CommitInput({ id, value, onCommit, placeholder, mono }: { id: string; value: string; onCommit: (v: string) => void; placeholder?: string; mono?: boolean }) {
  const [draft, setDraft] = useState<string | null>(null);
  const commit = () => {
    if (draft !== null && draft !== value) onCommit(draft);
    setDraft(null);
  };
  return (
    <input
      id={id}
      type="text"
      value={draft ?? value}
      placeholder={placeholder}
      spellCheck={false}
      style={mono ? { fontFamily: "var(--font-mono)" } : undefined}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === "Enter") commit();
        if (e.key === "Escape") setDraft(null);
      }}
    />
  );
}

function RawProps({ node, apply }: { node: ModelNode; apply: Apply }) {
  const [bad, setBad] = useState(false);
  return (
    <div className="field">
      <label htmlFor="np-raw">{s.editor.propsJson}</label>
      <textarea
        id="np-raw"
        rows={6}
        defaultValue={formatAny(node.props)}
        aria-invalid={bad || undefined}
        spellCheck={false}
        onChange={(e) => {
          const r = parseAny(e.target.value);
          const v = r.ok ? r.value : undefined;
          if (v !== undefined && typeof v === "object" && !Array.isArray(v)) {
            setBad(false);
            apply((t) => setProps(t, node.id, v as Record<string, unknown>));
          } else setBad(e.target.value.trim() !== "");
        }}
      />
      {bad && <span className="hint">{s.editor.invalidValue}</span>}
    </div>
  );
}

export function NodePanel({
  node,
  schema,
  issues,
  allIds,
  apply,
  onRename,
  onDelete,
}: {
  node: ModelNode;
  schema: NodeType | undefined;
  issues: readonly MappedIssue[];
  allIds: readonly string[];
  apply: Apply;
  onRename: (from: string, to: string) => void;
  onDelete: () => void;
}) {
  const [renameError, setRenameError] = useState("");
  const rename = (to: string) => {
    const next = to.trim();
    if (!next) return setRenameError(s.common.required);
    if (next !== node.id && allIds.includes(next)) return setRenameError(`${s.editor.idTaken}: ${next}`);
    setRenameError("");
    if (next !== node.id) {
      apply((t) => renameNode(t, node.id, next));
      onRename(node.id, next);
    }
  };
  const idIssues = issues.filter((m) => m.field.length === 1 && m.field[0] === "id");
  return (
    <div className="stack" key={node.id}>
      <div className="row between">
        <h2>{node.id}</h2>
        <button type="button" className="btn small danger" onClick={onDelete}>
          {s.editor.deleteNode}
        </button>
      </div>
      {issues.length > 0 && (
        <div className="banner error" role="alert">
          {s.editor.nodeIssues}
          <IssueList issues={issues.map((m) => m.issue)} />
        </div>
      )}
      <div className="field">
        <label htmlFor="np-id">{s.editor.nodeId}</label>
        <CommitInput id="np-id" value={node.id} onCommit={rename} mono />
        <span className="hint">{s.editor.idHint}</span>
        {(renameError || idIssues.length > 0) && <span className="field-issues">{renameError || idIssues[0]?.issue.message}</span>}
      </div>
      <div className="field">
        <span className="label">{s.editor.nodeType}</span>
        <code>{node.type}</code>
        {schema?.description && <span className="hint">{schema.description}</span>}
      </div>
      <h3 style={{ margin: "4px 0" }}>{s.editor.props}</h3>
      {schema ? (
        <PropsForm schema={schema} value={node.props} issues={issues} idPrefix={`np-${node.index}`} onChange={(next) => apply((t) => setProps(t, node.id, next))} />
      ) : (
        <>
          <div className="banner warn">{s.editor.unknownType}</div>
          <RawProps node={node} apply={apply} />
        </>
      )}
      <div className="row" style={{ alignItems: "flex-start" }}>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="np-timeout">{s.editor.timeout}</label>
          <CommitInput id="np-timeout" value={node.timeout ?? ""} placeholder="30s" onCommit={(v) => apply((t) => setNodeField(t, node.id, "timeout", v || undefined))} />
        </div>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="np-join">{s.editor.joinMode}</label>
          <select id="np-join" value={node.joinMode ?? ""} onChange={(e) => apply((t) => setNodeField(t, node.id, "joinMode", e.target.value || undefined))}>
            <option value="">{s.common.unset}</option>
            <option value="all">{s.editor.joinAll}</option>
            <option value="any">{s.editor.joinAny}</option>
          </select>
        </div>
      </div>
      <div className="row" style={{ alignItems: "flex-start" }}>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="np-retry">{s.editor.retryMax}</label>
          <CommitInput
            id="np-retry"
            value={node.retry?.max !== undefined ? String(node.retry.max) : ""}
            onCommit={(v) => apply((t) => setRetry(t, node.id, { ...node.retry, max: v === "" || Number.isNaN(Number(v)) ? undefined : Number(v) }))}
          />
        </div>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="np-backoff">{s.editor.retryBackoff}</label>
          <CommitInput id="np-backoff" value={node.retry?.backoff ?? ""} placeholder="2s" onCommit={(v) => apply((t) => setRetry(t, node.id, { ...node.retry, backoff: v || undefined }))} />
        </div>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="np-maxbackoff">{s.editor.retryMaxBackoff}</label>
          <CommitInput id="np-maxbackoff" value={node.retry?.maxBackoff ?? ""} placeholder="1m" onCommit={(v) => apply((t) => setRetry(t, node.id, { ...node.retry, maxBackoff: v || undefined }))} />
        </div>
      </div>
    </div>
  );
}

const MODES = ["queue", "drop", "replace", "parallel"] as const;

/** Policy-wide settings, shown when no node is selected. */
export function PolicySettings({ model, spec, apply, issues }: { model: PolicyModel; spec: Record<string, unknown>; apply: Apply; issues: readonly MappedIssue[] }) {
  const conc = (spec["concurrency"] ?? {}) as { mode?: string; limit?: number };
  return (
    <div className="stack">
      <h2>{s.editor.metaName}</h2>
      <dl className="kv">
        <dt>{s.common.name}</dt>
        <dd>{model.name}</dd>
      </dl>
      {issues.length > 0 && (
        <div className="banner error" role="alert">
          {s.editor.docIssues}
          <IssueList issues={issues.map((m) => m.issue)} />
        </div>
      )}
      <div className="row" style={{ alignItems: "flex-start" }}>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="ps-mode">{s.editor.concurrency}</label>
          <select id="ps-mode" value={conc.mode ?? ""} onChange={(e) => apply((t) => setPath(t, ["spec", "concurrency", "mode"], e.target.value || undefined))}>
            <option value="">{s.common.unset}</option>
            {MODES.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </div>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="ps-limit">{s.editor.concurrencyLimit}</label>
          <CommitInput id="ps-limit" value={conc.limit ? String(conc.limit) : ""} onCommit={(v) => apply((t) => setPath(t, ["spec", "concurrency", "limit"], v === "" ? undefined : Number(v)))} />
        </div>
      </div>
      <div className="row" style={{ alignItems: "flex-start" }}>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="ps-timeout">{s.editor.policyTimeout}</label>
          <CommitInput id="ps-timeout" value={typeof spec["timeout"] === "string" ? spec["timeout"] : ""} placeholder="30m" onCommit={(v) => apply((t) => setPath(t, ["spec", "timeout"], v || undefined))} />
        </div>
        <div className="field" style={{ flex: 1 }}>
          <label htmlFor="ps-interrupt">{s.editor.onInterrupt}</label>
          <select id="ps-interrupt" value={typeof spec["onInterrupt"] === "string" ? spec["onInterrupt"] : ""} onChange={(e) => apply((t) => setPath(t, ["spec", "onInterrupt"], e.target.value || undefined))}>
            <option value="">{s.common.unset}</option>
            <option value="resume">resume</option>
            <option value="abort">abort</option>
          </select>
        </div>
      </div>
      <p className="meta">{s.editor.layoutNote}</p>
      <p className="meta">{s.editor.deleteHint}</p>
    </div>
  );
}

