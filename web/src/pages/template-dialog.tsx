import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { useNavigate } from "react-router";
import { client, unwrap } from "../api/client";
import { qk, servicesQuery, templatesQuery } from "../api/queries";
import type { Template, TemplateInstance, TemplateParam } from "../api/types";
import { Dialog } from "../components/dialog";
import { ErrorBox, Field, Spinner } from "../components/ui";
import { buildParams, initialValues, isRequired, suggestName } from "../lib/template-form";
import type { FormValues } from "../lib/template-form";
import { s } from "../strings";

const NAME_RE = /^[a-z]([a-z0-9-]*[a-z0-9])?$/;

function templateTitle(t: Template): string {
  return s.templates.items[t.id]?.title ?? t.title;
}

function templateDescription(t: Template): string {
  return s.templates.items[t.id]?.description ?? t.description;
}

interface FieldProps {
  param: TemplateParam;
  values: FormValues;
  services: string[];
  error?: string;
  onChange: (v: string | boolean) => void;
}

function ParamField({ param, values, services, error, onChange }: FieldProps) {
  const id = `tpl-${param.name}`;
  const value = values[param.name];
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
  switch (param.type) {
    case "boolean":
      input = (
        <input id={id} type="checkbox" checked={value === true} onChange={(e) => onChange(e.target.checked)} />
      );
      break;
    case "service":
      input = (
        <select id={id} value={typeof value === "string" ? value : ""} onChange={(e) => onChange(e.target.value)} required={required}>
          <option value="">{s.templates.selectService}</option>
          {services.map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
      );
      break;
    case "json":
      input = (
        <textarea id={id} rows={3} spellCheck={false} value={typeof value === "string" ? value : ""} onChange={(e) => onChange(e.target.value)} />
      );
      break;
    default:
      input = (
        <input id={id} type="text" spellCheck={false} value={typeof value === "string" ? value : ""} onChange={(e) => onChange(e.target.value)} />
      );
  }
  return (
    <Field label={label} htmlFor={id} hint={hint}>
      {input}
    </Field>
  );
}

function Form({ templates, services, service, onClose }: { templates: Template[]; services: string[]; service?: string; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [templateId, setTemplateId] = useState(templates[0]?.id ?? "");
  const template = templates.find((t) => t.id === templateId);
  const [values, setValues] = useState<FormValues>(() => (templates[0] ? initialValues(templates[0], service) : {}));
  // The name follows the template and service until the user types one.
  const [typedName, setTypedName] = useState<string | null>(null);
  const [touched, setTouched] = useState(false);
  const serviceValue = template ? values[template.serviceParam] : undefined;
  const name = typedName ?? suggestName(templateId, typeof serviceValue === "string" && serviceValue !== "" ? serviceValue : undefined);
  const [preview, setPreview] = useState<{ yaml: string; warnings: TemplateInstance["warnings"]; key: string } | null>(null);

  const existing = useQuery({ queryKey: qk.policies(), queryFn: async () => (await unwrap(client.GET("/policies", { params: { query: { limit: 500 } } }))).items });
  const taken = useMemo(() => new Set((existing.data ?? []).map((p) => p.name)), [existing.data]);

  const built = useMemo(() => (template ? buildParams(template, values, service) : { params: {}, errors: {} }), [template, values, service]);
  const nameProblem = name === "" ? "" : !NAME_RE.test(name) ? s.templates.policyNameHelp : taken.has(name) ? s.templates.exists : "";
  const key = JSON.stringify([templateId, name, built.params]);
  const hasErrors = Object.keys(built.errors).length > 0 || name === "" || nameProblem !== "";

  function pick(id: string) {
    const t = templates.find((x) => x.id === id);
    if (!t) return;
    setTemplateId(id);
    setValues(initialValues(t, service));
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
      {template.params.map((p) => (
        <ParamField
          key={`${template.id}-${p.name}`}
          param={p}
          values={values}
          services={services}
          error={touched || preview || previewM.isError ? built.errors[p.name] : undefined}
          onChange={(v) => setValues((cur) => ({ ...cur, [p.name]: v }))}
        />
      ))}
      <ErrorBox error={previewM.error ?? deployM.error} />
      {previewM.isPending && <Spinner label={s.templates.previewing} />}
      {preview && (
        <div>
          <h3 style={{ marginTop: 0 }}>{s.templates.previewTitle}</h3>
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
 * With `service` the target service is preset.
 */
export function TemplateDialog({ service, onClose }: { service?: string; onClose: () => void }) {
  const templates = useQuery(templatesQuery());
  const services = useQuery(servicesQuery());
  return (
    <Dialog title={s.templates.title} onClose={onClose} wide>
      {templates.isError ? (
        <ErrorBox error={templates.error} onRetry={() => void templates.refetch()} />
      ) : !templates.data || !services.data ? (
        <Spinner />
      ) : (
        <Form templates={templates.data} services={services.data.map((x) => x.name)} service={service} onClose={onClose} />
      )}
    </Dialog>
  );
}
