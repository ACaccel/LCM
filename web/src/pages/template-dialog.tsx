import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { useNavigate } from "react-router";
import { client, unwrap } from "../api/client";
import { policiesQuery, qk, resourceQuery, resourcesQuery, templatesQuery } from "../api/queries";
import type { Template, TemplateInstance, TemplateParam } from "../api/types";
import { Dialog, useDialogGuard } from "../components/dialog";
import { ErrorBox, Field, Spinner } from "../components/ui";
import { WEEKDAYS, buildParams, initialValues, isRequired, isVisible, prefillValues, suggestName } from "../lib/template-form";
import type { FormValue, FormValues, WindowRow } from "../lib/template-form";
import { s } from "../strings";

function templateTitle(t: Template): string {
  return s.templates.items[t.id]?.title ?? t.title;
}

function templateDescription(t: Template): string {
  return s.templates.items[t.id]?.description ?? t.description;
}

/** Rows of deploy windows: the days each starts on, its start and its end. */
function WindowsEditor({ id, rows, onChange }: { id: string; rows: WindowRow[]; onChange: (rows: WindowRow[]) => void }) {
  const set = (i: number, row: WindowRow) => onChange(rows.map((r, j) => (j === i ? row : r)));
  return (
    <div className="stack" id={id}>
      {rows.length === 0 && <p className="meta">{s.templates.noWindows}</p>}
      {rows.map((row, i) => (
        <fieldset key={i} className="row window-row" aria-label={s.templates.window(i + 1)}>
          <span role="group" aria-label={s.templates.windowDays}>
            {WEEKDAYS.map((d) => (
              <label key={d} className="check">
                <input
                  type="checkbox"
                  checked={row.days.includes(d)}
                  onChange={(e) => set(i, { ...row, days: e.target.checked ? [...row.days, d] : row.days.filter((x) => x !== d) })}
                />
                {s.templates.weekdays[d]}
              </label>
            ))}
          </span>
          <label>
            {s.templates.windowStart}{" "}
            <input type="time" value={row.start} onChange={(e) => set(i, { ...row, start: e.target.value })} />
          </label>
          <label>
            {s.templates.windowEnd}{" "}
            <input type="time" value={row.end} onChange={(e) => set(i, { ...row, end: e.target.value })} />
          </label>
          <button type="button" className="btn small" onClick={() => onChange(rows.filter((_, j) => j !== i))}>
            {s.templates.removeWindow}
          </button>
        </fieldset>
      ))}
      <div>
        <button type="button" className="btn small" onClick={() => onChange([...rows, { days: [], start: "00:00", end: "00:00" }])}>
          {s.templates.addWindow}
        </button>
      </div>
    </div>
  );
}

interface FieldProps {
  param: TemplateParam;
  values: FormValues;
  services: string[];
  error?: string;
  onChange: (v: FormValue) => void;
}

function ParamField({ param, values, services, error, onChange }: FieldProps) {
  const id = `tpl-${param.name}`;
  const value = values[param.name];
  const text = typeof value === "string" ? value : "";
  const required = isRequired(param, values);
  const label = `${s.templates.params[param.name] ?? param.name}${required ? " *" : ""}`;
  const hint = (
    <>
      {s.templates.paramHelp[param.name] ?? param.description}
      {param.type === "strings" ? ` ${s.templates.listHint}` : ""}
      {error && <span className="error-text"> {error}</span>}
    </>
  );
  let input;
  if (param.enum?.length) {
    input = (
      <select id={id} value={text} onChange={(e) => onChange(e.target.value)} required={required}>
        {!param.enum.includes(text) && <option value={text}>{text}</option>}
        {param.enum.map((v) => (
          <option key={v} value={v}>
            {s.templates.enumLabels[v] ?? v}
          </option>
        ))}
      </select>
    );
  } else {
    switch (param.type) {
      case "boolean":
        input = <input id={id} type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} />;
        break;
      case "service":
        input = (
          <select id={id} value={text} onChange={(e) => onChange(e.target.value)} required={required}>
            <option value="">{s.templates.selectService}</option>
            {services.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </select>
        );
        break;
      case "windows":
        input = <WindowsEditor id={id} rows={Array.isArray(value) ? value : []} onChange={onChange} />;
        break;
      case "json":
        input = <textarea id={id} rows={3} spellCheck={false} value={text} onChange={(e) => onChange(e.target.value)} />;
        break;
      default:
        input = <input id={id} type="text" spellCheck={false} value={text} onChange={(e) => onChange(e.target.value)} />;
    }
  }
  return (
    <Field label={label} htmlFor={param.type === "windows" && !param.enum?.length ? undefined : id} hint={hint}>
      {input}
    </Field>
  );
}

function Form({ templates, services, resource, onClose }: { templates: Template[]; services: string[]; resource?: string; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [templateId, setTemplateId] = useState(templates[0]?.id ?? "");
  const template = templates.find((t) => t.id === templateId);
  const [entered, setEntered] = useState<FormValues>(() => (templates[0] ? initialValues(templates[0], resource) : {}));
  // Fields the user changed; prefilled values never replace them.
  const [edited, setEdited] = useState<ReadonlySet<string>>(new Set());
  const target = template ? entered[template.resourceParam] : undefined;
  const service = typeof target === "string" && target !== "" ? target : undefined;
  const prefills = template?.params.some((p) => p.prefill) ?? false;
  const targetDetail = useQuery({ ...resourceQuery(service ?? ""), enabled: prefills && service !== undefined });
  const values = useMemo<FormValues>(() => {
    if (!template || !targetDetail.data || targetDetail.data.name !== service) return entered;
    const filled = prefillValues(template, targetDetail.data.document?.spec.config);
    const out = { ...entered };
    for (const [k, v] of Object.entries(filled)) if (!edited.has(k)) out[k] = v;
    return out;
  }, [template, targetDetail.data, service, entered, edited]);
  // The name follows the template and target resource until the user types one.
  const [typedName, setTypedName] = useState<string | null>(null);
  const [touched, setTouched] = useState(false);
  const name = typedName ?? suggestName(templateId, service);
  const [preview, setPreview] = useState<{ yaml: string; warnings: TemplateInstance["warnings"]; key: string } | null>(null);

  // The server checks the name; the list only warns about a taken one early.
  const existing = useQuery(policiesQuery());
  const taken = useMemo(() => new Set((existing.data ?? []).map((p) => p.name)), [existing.data]);

  const built = useMemo(() => (template ? buildParams(template, values, resource) : { params: {}, errors: {} }), [template, values, resource]);
  const nameProblem = taken.has(name) ? s.templates.exists : "";
  const key = JSON.stringify([templateId, name, built.params]);
  const hasErrors = Object.keys(built.errors).length > 0 || name === "" || nameProblem !== "";

  function pick(id: string) {
    const t = templates.find((x) => x.id === id);
    if (!t) return;
    setTemplateId(id);
    setEntered(initialValues(t, resource));
    setEdited(new Set());
    setPreview(null);
  }

  const send = (dryRun: boolean) =>
    unwrap(
      client.POST("/templates/{t}/instantiate", { params: { path: { t: templateId } }, body: { name, params: built.params, dryRun } }),
    );
  const previewM = useMutation({
    mutationFn: () => send(true),
    onSuccess: (r) => setPreview({ yaml: r.yaml, warnings: r.warnings, key }),
  });
  const deployM = useMutation({
    mutationFn: () => send(false),
    onSuccess: (r) => {
      void qc.invalidateQueries({ queryKey: qk.policies() });
      onClose();
      void navigate(`/policies/${encodeURIComponent(r.name)}`);
    },
  });

  const [initial] = useState(() => JSON.stringify([templateId, values]));
  useDialogGuard(typedName !== null || JSON.stringify([templateId, values]) !== initial || previewM.isPending || deployM.isPending);

  const fresh = preview?.key === key;
  function submit(e: FormEvent) {
    e.preventDefault();
    if (!hasErrors) previewM.mutate();
  }

  if (!template) return <ErrorBox error={new Error(s.templates.noService)} />;
  return (
    <form className="stack" onSubmit={submit}>
      <Field label={s.templates.pick} htmlFor="tpl-pick" hint={templateDescription(template)}>
        <select id="tpl-pick" value={templateId} onChange={(e) => pick(e.target.value)} data-autofocus>
          {templates.map((t) => (
            <option key={t.id} value={t.id}>
              {templateTitle(t)}
            </option>
          ))}
        </select>
      </Field>
      <Field label={s.templates.policyName} htmlFor="tpl-name" hint={nameProblem || s.templates.policyNameHelp}>
        <input
          id="tpl-name"
          type="text"
          value={name}
          maxLength={63}
          onChange={(e) => {
            setTouched(true);
            setTypedName(e.target.value);
          }}
        />
      </Field>
      {template.params.filter((p) => isVisible(p, values)).map((p) => (
        <ParamField
          key={`${template.id}-${p.name}`}
          param={p}
          values={values}
          services={services}
          error={touched || preview || previewM.isError ? built.errors[p.name] : undefined}
          onChange={(v) => {
            setEntered((cur) => ({ ...cur, [p.name]: v }));
            setEdited((cur) => new Set(cur).add(p.name));
          }}
        />
      ))}
      <ErrorBox error={previewM.error ?? deployM.error} />
      {previewM.isPending && <Spinner label={s.templates.previewing} />}
      {preview && (
        <div>
          <h3 className="flat-top">{s.templates.previewTitle}</h3>
          {!fresh && <p className="meta">{s.templates.previewStale}</p>}
          {preview.warnings.length > 0 && (
            <div className="banner warn">
              {s.templates.warnings}: {preview.warnings.map((w) => w.message).join("; ")}
            </div>
          )}
          <pre className="block">{preview.yaml}</pre>
        </div>
      )}
      <div className="row end">
        <button type="button" className="btn" onClick={onClose}>
          {s.common.cancel}
        </button>
        <button type="submit" className="btn" disabled={hasErrors || previewM.isPending} onClick={() => setTouched(true)}>
          {previewM.isPending ? s.templates.previewing : s.templates.preview}
        </button>
        <button type="button" className="btn primary" disabled={!fresh || deployM.isPending} onClick={() => deployM.mutate()}>
          {deployM.isPending ? s.templates.deploying : s.templates.deploy}
        </button>
      </div>
    </form>
  );
}

/**
 * Creates a policy from a built-in template: a form generated from the
 * parameter descriptors, a YAML preview from a dry run, then the deployment.
 * With `resource` the target service is preset; template targets are services.
 */
export function TemplateDialog({ resource, onClose }: { resource?: string; onClose: () => void }) {
  const templates = useQuery(templatesQuery());
  const services = useQuery(resourcesQuery({ type: "service" }));
  return (
    <Dialog title={s.templates.title} onClose={onClose} wide>
      {templates.isError ? (
        <ErrorBox error={templates.error} onRetry={() => void templates.refetch()} />
      ) : !templates.data || !services.data ? (
        <Spinner />
      ) : (
        <Form templates={templates.data} services={services.data.filter((x) => x.phase === "managed").map((x) => x.name)} resource={resource} onClose={onClose} />
      )}
    </Dialog>
  );
}
