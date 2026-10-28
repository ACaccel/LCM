import type { Template, TemplateParam } from "../api/types";
import { s } from "../strings";

/** Form state: one string per field, booleans for checkboxes. */
export type FormValues = Record<string, string | boolean>;

/** The text a default value is shown as. */
export function defaultText(p: TemplateParam): string | boolean {
  const d = p.default;
  if (p.type === "boolean") return d === true;
  if (d === undefined || d === null) return "";
  if (p.type === "strings") return Array.isArray(d) ? d.join(", ") : "";
  if (p.type === "json") return JSON.stringify(d);
  return String(d);
}

/** Initial form values: defaults, with the target service preset. */
export function initialValues(t: Template, service?: string): FormValues {
  const out: FormValues = {};
  for (const p of t.params) out[p.name] = defaultText(p);
  if (service) out[t.serviceParam] = service;
  return out;
}

export interface BuiltParams {
  params: Record<string, unknown>;
  /** Field errors by parameter name, found without asking the server. */
  errors: Record<string, string>;
}

/**
 * Turns form values into the request's params: only values that differ from
 * the default are sent, so the server's defaults stay the single source.
 */
export function buildParams(t: Template, values: FormValues, service?: string): BuiltParams {
  const params: Record<string, unknown> = {};
  const errors: Record<string, string> = {};
  const initial = initialValues(t, service);
  for (const p of t.params) {
    const v = values[p.name];
    if (p.type === "boolean") {
      params[p.name] = v === true;
      continue;
    }
    const text = typeof v === "string" ? v.trim() : "";
    const gate = p.requiredIf ? values[p.requiredIf] === true : false;
    if ((p.required || gate) && text === "") {
      errors[p.name] = s.templates.required;
      continue;
    }
    if (p.name !== t.serviceParam && v === initial[p.name]) continue;
    switch (p.type) {
      case "strings":
        params[p.name] = text
          .split(",")
          .map((e) => e.trim())
          .filter((e) => e !== "");
        break;
      case "json":
        try {
          params[p.name] = text === "" ? null : (JSON.parse(text) as unknown);
        } catch {
          errors[p.name] = s.templates.invalidJson;
        }
        break;
      default:
        params[p.name] = text;
    }
  }
  return { params, errors };
}

/** Whether a field is currently required. */
export function isRequired(p: TemplateParam, values: FormValues): boolean {
  return p.required || (p.requiredIf ? values[p.requiredIf] === true : false);
}

/** A policy name suggestion for the template and service. */
export function suggestName(templateId: string, service?: string): string {
  return service ? `${service}-${templateId}`.slice(0, 63) : templateId;
}
