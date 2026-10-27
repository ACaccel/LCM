import { useState } from "react";
import type { NodeProp, NodeType } from "../api/types";
import { issuesAt, issuesExactlyAt } from "../policy/issues";
import type { MappedIssue, PathToken } from "../policy/issues";
import { formatAny, formatNumber, parseAny, parseNumber, prune, setIn } from "../policy/values";
import { s } from "../strings";

type Path = readonly PathToken[];

interface Ctx {
  idPrefix: string;
  issues: readonly MappedIssue[];
  /** Replaces the value at a path inside the node's props. */
  set: (path: Path, value: unknown) => void;
}

const idFor = (ctx: Ctx, path: Path) => `${ctx.idPrefix}-${path.join("-")}`;

function FieldIssues({ ctx, path, deep }: { ctx: Ctx; path: Path; deep?: boolean }) {
  const found = (deep ? issuesAt : issuesExactlyAt)(ctx.issues, ["props", ...path]);
  if (found.length === 0) return null;
  return (
    <ul className="field-issues" role="alert">
      {found.map((m, i) => (
        <li key={i}>{m.issue.message}</li>
      ))}
    </ul>
  );
}

/** Text input whose parsed value is reported while valid; invalid text stays visible until blur. */
function ParsedInput({
  id,
  value,
  format,
  parse,
  onValue,
  placeholder,
  mono,
  multiline,
}: {
  id: string;
  value: unknown;
  format: (v: unknown) => string;
  parse: (text: string) => { ok: true; value: unknown } | { ok: false };
  onValue: (v: unknown) => void;
  placeholder?: string;
  mono?: boolean;
  multiline?: boolean;
}) {
  const [draft, setDraft] = useState<string | null>(null);
  const text = draft ?? format(value);
  const invalid = draft !== null && !parse(draft).ok;
  const common = {
    id,
    value: text,
    placeholder,
    spellCheck: false,
    "aria-invalid": invalid || undefined,
    style: mono ? { fontFamily: "var(--font-mono)" } : undefined,
    onBlur: () => setDraft(null),
  };
  const change = (t: string) => {
    setDraft(t);
    const r = parse(t);
    if (r.ok) onValue(r.value);
  };
  return (
    <>
      {multiline ? <textarea rows={1} {...common} style={{ ...common.style, fieldSizing: "content", minHeight: 32 } as React.CSSProperties} onChange={(e) => change(e.target.value)} /> : <input type="text" {...common} onChange={(e) => change(e.target.value)} />}
      {invalid && <span className="hint" role="alert">{s.editor.invalidValue}</span>}
    </>
  );
}

function Scalar({ schema, value, ctx, path }: { schema: NodeProp; value: unknown; ctx: Ctx; path: Path }) {
  const id = idFor(ctx, path);
  const onValue = (v: unknown) => ctx.set(path, v);
  const placeholder = schema.default !== undefined ? String(schema.default) : undefined;

  if (schema.enum && schema.enum.length > 0 && (value === undefined || typeof value === "string")) {
    const custom = typeof value === "string" && value !== "" && !schema.enum.includes(value);
    return (
      <select id={id} value={typeof value === "string" ? value : ""} onChange={(e) => onValue(e.target.value || undefined)}>
        <option value="">{schema.default !== undefined ? `(${String(schema.default)})` : s.common.unset}</option>
        {schema.enum.map((o) => (
          <option key={o} value={o}>
            {o}
          </option>
        ))}
        {custom && <option value={value}>{value}</option>}
      </select>
    );
  }
  switch (schema.type) {
    case "boolean":
      if (schema.expr || typeof value === "string") {
        return <ParsedInput id={id} value={value} mono format={(v) => (typeof v === "string" ? v : v === undefined ? "" : String(v))} parse={(t) => ({ ok: true, value: t === "" ? undefined : t === "true" ? true : t === "false" ? false : t })} onValue={onValue} placeholder={s.editor.exprHint} />;
      }
      return (
        <select id={id} value={value === undefined ? "" : String(value)} onChange={(e) => onValue(e.target.value === "" ? undefined : e.target.value === "true")}>
          <option value="">{schema.default !== undefined ? `(${String(schema.default)})` : s.common.unset}</option>
          <option value="true">true</option>
          <option value="false">false</option>
        </select>
      );
    case "integer":
    case "number":
      return <ParsedInput id={id} value={value} format={formatNumber} parse={(t) => parseNumber(t, schema.type === "integer")} onValue={onValue} placeholder={placeholder} />;
    case "any":
      return <ParsedInput id={id} value={value} mono multiline format={formatAny} parse={parseAny} onValue={onValue} placeholder={s.editor.jsonValue} />;
    default: {
      const mono = !!schema.expr;
      return (
        <input
          id={id}
          type="text"
          spellCheck={false}
          value={typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value)}
          placeholder={mono ? s.editor.exprHint : placeholder}
          style={mono ? { fontFamily: "var(--font-mono)" } : undefined}
          onChange={(e) => onValue(e.target.value === "" ? undefined : e.target.value)}
        />
      );
    }
  }
}

function Label({ schema, id, labelId }: { schema: NodeProp; id?: string; labelId?: string }) {
  const Tag = id ? "label" : "span";
  return (
    <Tag htmlFor={id} id={labelId} className={id ? undefined : "label"}>
      {schema.name}
      {schema.required && (
        <span aria-label={s.common.required} title={s.common.required} style={{ color: "var(--destructive)" }}>
          {" "}
          *
        </span>
      )}
      {schema.expr && <span className="meta"> · {s.editor.expr}</span>}
    </Tag>
  );
}

function ArrayField({ schema, value, ctx, path }: { schema: NodeProp; value: unknown; ctx: Ctx; path: Path }) {
  const list = Array.isArray(value) ? value : [];
  const item = schema.items;
  if (!item || item.type === "any" || (item.type === "array")) {
    return <Scalar schema={{ ...schema, type: "any" }} value={value} ctx={ctx} path={path} />;
  }
  const blank = item.type === "object" ? {} : item.type === "boolean" ? false : item.type === "integer" || item.type === "number" ? 0 : "";
  const add = () => ctx.set(path, [...list, blank]);
  return (
    <div className="stack" style={{ gap: 6 }}>
      {list.map((el, i) => (
        <div key={i} className={item.type === "object" ? "panel" : "row"} style={{ padding: item.type === "object" ? 8 : 0, flexWrap: "nowrap" }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            {item.type === "object" && item.props ? (
              <ObjectFields props={item.props} value={el} ctx={ctx} path={[...path, i]} />
            ) : (
              <PropInput schema={{ ...item, name: `${schema.name}[${i}]` }} value={el} ctx={ctx} path={[...path, i]} />
            )}
          </div>
          <button type="button" className="btn small" aria-label={`${s.editor.removeItem} ${schema.name}[${i}]`} onClick={() => ctx.set(path, list.filter((_, j) => j !== i))}>
            ✕
          </button>
        </div>
      ))}
      <div>
        <button type="button" className="btn small" onClick={add}>
          + {s.editor.addItem}
        </button>
      </div>
    </div>
  );
}

function MapField({ schema, value, ctx, path }: { schema: NodeProp; value: unknown; ctx: Ctx; path: Path }) {
  const rec = typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : {};
  const entries = Object.entries(rec);
  const item: NodeProp = schema.items ?? { name: "", type: "any" };
  const [pending, setPending] = useState("");
  return (
    <div className="stack" style={{ gap: 6 }}>
      {entries.map(([k, v]) => (
        <div key={k} className="row" style={{ flexWrap: "nowrap", alignItems: "flex-start" }}>
          <code style={{ minWidth: 80, paddingTop: 6 }}>{k}</code>
          <div style={{ flex: 1, minWidth: 0 }}>
            <PropInput schema={{ ...item, name: k }} value={v} ctx={ctx} path={[...path, k]} />
          </div>
          <button
            type="button"
            className="btn small"
            aria-label={`${s.editor.removeItem} ${k}`}
            onClick={() => {
              const { [k]: _gone, ...rest } = rec;
              ctx.set(path, Object.keys(rest).length ? rest : undefined);
            }}
          >
            ✕
          </button>
        </div>
      ))}
      <div className="row">
        <input type="text" aria-label={s.editor.keyName} placeholder={s.editor.keyName} value={pending} onChange={(e) => setPending(e.target.value)} style={{ maxWidth: 160 }} />
        <button
          type="button"
          className="btn small"
          disabled={!pending.trim() || pending.trim() in rec}
          onClick={() => {
            ctx.set(path, { ...rec, [pending.trim()]: item.type === "object" ? {} : "" });
            setPending("");
          }}
        >
          + {s.editor.addEntry}
        </button>
      </div>
    </div>
  );
}

function ObjectFields({ props, value, ctx, path }: { props: readonly NodeProp[]; value: unknown; ctx: Ctx; path: Path }) {
  const rec = typeof value === "object" && value !== null && !Array.isArray(value) ? (value as Record<string, unknown>) : {};
  return (
    <div className="stack" style={{ gap: 4 }}>
      {props.map((p) => (
        <PropField key={p.name} schema={p} value={rec[p.name]} ctx={ctx} path={[...path, p.name]} />
      ))}
    </div>
  );
}

/** The editing control of one value, without its label. */
function PropInput({ schema, value, ctx, path }: { schema: NodeProp; value: unknown; ctx: Ctx; path: Path }) {
  if (schema.type === "array") return <ArrayField schema={schema} value={value} ctx={ctx} path={path} />;
  if (schema.type === "object") {
    if (schema.props && schema.props.length > 0) return <ObjectFields props={schema.props} value={value} ctx={ctx} path={path} />;
    if (schema.items && schema.items.type !== "any") return <MapField schema={schema} value={value} ctx={ctx} path={path} />;
    if (schema.items) return <MapField schema={schema} value={value} ctx={ctx} path={path} />;
    return <Scalar schema={{ ...schema, type: "any" }} value={value} ctx={ctx} path={path} />;
  }
  return <Scalar schema={schema} value={value} ctx={ctx} path={path} />;
}

function PropField({ schema, value, ctx, path }: { schema: NodeProp; value: unknown; ctx: Ctx; path: Path }) {
  const id = idFor(ctx, path);
  const composite = schema.type === "array" || (schema.type === "object" && (schema.props?.length || schema.items));
  return (
    <div className="field">
      {composite ? <Label schema={schema} labelId={`${id}-label`} /> : <Label schema={schema} id={id} />}
      {composite ? (
        <div role="group" aria-labelledby={`${id}-label`}>
          <PropInput schema={schema} value={value} ctx={ctx} path={path} />
        </div>
      ) : (
        <PropInput schema={schema} value={value} ctx={ctx} path={path} />
      )}
      {schema.description && <span className="hint">{schema.description}</span>}
      <FieldIssues ctx={ctx} path={path} deep={!!composite} />
    </div>
  );
}

export interface PropsFormProps {
  schema: NodeType;
  value: Record<string, unknown>;
  onChange: (next: Record<string, unknown>) => void;
  issues?: readonly MappedIssue[];
  idPrefix?: string;
}

/**
 * Property form generated from a node type's schema: types, enums, required
 * markers, defaults, descriptions and nested lists and objects all come from
 * the server. Keys the schema does not list are kept as they are.
 */
export function PropsForm({ schema, value, onChange, issues = [], idPrefix = "prop" }: PropsFormProps) {
  const ctx: Ctx = {
    idPrefix,
    issues,
    set: (path, v) => {
      const next = (prune(setIn(value, path, v)) ?? {}) as Record<string, unknown>;
      onChange(next);
    },
  };
  const known = new Set(schema.props.map((p) => p.name));
  const extra = Object.keys(value).filter((k) => !known.has(k));
  return (
    <div className="props-form">
      {schema.additionalProps ? (
        <PropField schema={{ name: "props", type: "object", items: { name: "", type: "any" } }} value={value} ctx={{ ...ctx, set: (p, v) => onChange((prune(setIn(value, p.slice(1), v)) ?? {}) as Record<string, unknown>) }} path={["props"]} />
      ) : (
        schema.props.map((p) => <PropField key={p.name} schema={p} value={value[p.name]} ctx={ctx} path={[p.name]} />)
      )}
      {!schema.additionalProps && extra.length > 0 && (
        <p className="meta">
          {s.common.otherFields}: {extra.map((k) => <code key={k}>{k} </code>)}
        </p>
      )}
    </div>
  );
}
