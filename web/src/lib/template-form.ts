import type { Template, TemplateParam } from "../api/types";
import { s } from "../strings";

/** One row of a `windows` parameter, as the form edits it. */
export interface WindowRow {
  /** Days the window starts on (mon..sun); empty means every day. */
  days: string[];
  /** HH:MM, as a time input shows it. */
  start: string;
  end: string;
}

/** Form state: text per field, booleans for checkboxes, rows for windows. */
export type FormValue = string | boolean | WindowRow[];
export type FormValues = Record<string, FormValue>;

/** Day names of the `windows` parameter type, Monday first. */
export const WEEKDAYS = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"] as const;

/**
 * A time input cannot show 24:00, which ends a window at midnight. 00:00 as
 * an end means the same window (an end before the start runs into the next
 * day), so the form shows 24:00 as 00:00 and sends 00:00 back as 24:00.
 */
const MIDNIGHT = "24:00";

/** Rows of a windows value; anything that is not a list of windows gives none. */
export function windowRows(v: unknown): WindowRow[] {
  if (!Array.isArray(v)) return [];
  return v.map((w: unknown) => {
    const o = (typeof w === "object" && w !== null ? w : {}) as Record<string, unknown>;
    const end = typeof o.end === "string" ? o.end : "";
    return {
      days: Array.isArray(o.days) ? o.days.filter((d): d is string => typeof d === "string") : [],
      start: typeof o.start === "string" ? o.start : "",
      end: end === MIDNIGHT ? "00:00" : end,
    };
  });
}

/** The windows value of form rows. */
export function windowsValue(rows: readonly WindowRow[]): Record<string, unknown>[] {
  return rows.map((r) => ({
    ...(r.days.length ? { days: WEEKDAYS.filter((d) => r.days.includes(d)) } : {}),
    start: r.start,
    end: r.end === "00:00" ? MIDNIGHT : r.end,
  }));
}

/** The form value a default is shown as. */
export function defaultText(p: TemplateParam): FormValue {
  const d = p.default;
  if (p.type === "boolean") return d === true;
  if (p.type === "windows") return windowRows(d);
  if (d === undefined || d === null) return "";
  if (p.type === "strings") return Array.isArray(d) ? d.join(", ") : "";
  if (p.type === "json") return JSON.stringify(d);
  return String(d);
}

/** Initial form values: defaults, with the target resource preset. */
export function initialValues(t: Template, resource?: string): FormValues {
  const out: FormValues = {};
  for (const p of t.params) out[p.name] = defaultText(p);
  if (resource) out[t.resourceParam] = resource;
  return out;
}

/**
 * Values that parameters with a `prefill` path take from the selected
 * resource's spec.config; only text values that are present count.
 */
export function prefillValues(t: Template, config: unknown): FormValues {
  const out: FormValues = {};
  for (const p of t.params) {
    if (!p.prefill) continue;
    let v: unknown = config;
    for (const key of p.prefill.split(".")) {
      v = typeof v === "object" && v !== null ? (v as Record<string, unknown>)[key] : undefined;
    }
    if (typeof v === "string" && v !== "") out[p.name] = v;
  }
  return out;
}

export interface BuiltParams {
  params: Record<string, unknown>;
  /** Field errors by parameter name, found without asking the server. */
  errors: Record<string, string>;
}

const same = (a: FormValue | undefined, b: FormValue | undefined) => JSON.stringify(a) === JSON.stringify(b);

/**
 * Turns form values into the request's params: only values that differ from
 * the default are sent, so the server's defaults stay the single source.
 */
export function buildParams(t: Template, values: FormValues, resource?: string): BuiltParams {
  const params: Record<string, unknown> = {};
  const errors: Record<string, string> = {};
  const initial = initialValues(t, resource);
  for (const p of t.params) {
    // A hidden field keeps the server's default, whatever it held before.
    if (!isVisible(p, values)) continue;
    const v = values[p.name];
    if (p.type === "boolean") {
      params[p.name] = v === true;
      continue;
    }
    if (p.type === "windows") {
      if (Array.isArray(v) && !same(v, initial[p.name])) params[p.name] = windowsValue(v);
      continue;
    }
    const text = typeof v === "string" ? v.trim() : "";
    const gate = p.requiredIf ? values[p.requiredIf] === true : false;
    if ((p.required || gate) && text === "") {
      errors[p.name] = s.templates.required;
      continue;
    }
    if (p.name !== t.resourceParam && same(v, initial[p.name])) continue;
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

/** Whether a field applies to the current values, such as the chosen notification target. */
export function isVisible(p: TemplateParam, values: FormValues): boolean {
  return !p.visibleIf || values[p.visibleIf.param] === p.visibleIf.equals;
}

/** Whether a field is currently required. */
export function isRequired(p: TemplateParam, values: FormValues): boolean {
  return p.required || (p.requiredIf ? values[p.requiredIf] === true : false);
}

/** A policy name suggestion for the template and resource. */
export function suggestName(templateId: string, resource?: string): string {
  return resource ? `${resource}-${templateId}`.slice(0, 63) : templateId;
}
