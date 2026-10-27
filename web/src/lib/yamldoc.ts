import { stringify } from "yaml";

/** Canonical-looking YAML for comparing two stored versions of a document. */
export function docToYaml(doc: unknown): string {
  return stringify(doc, { lineWidth: 0, sortMapEntries: false });
}

export const ANNOTATION_LAYOUT = "lcm.dev/layout";
export const ANNOTATION_DASHBOARD = "lcm.dev/dashboard-url";

/** The dashboard link of a service document, only if it is a plain http(s) URL. */
export function dashboardUrl(doc: { metadata?: { annotations?: Record<string, string> } } | undefined): string | undefined {
  const raw = doc?.metadata?.annotations?.[ANNOTATION_DASHBOARD];
  if (!raw) return undefined;
  try {
    const u = new URL(raw);
    return u.protocol === "http:" || u.protocol === "https:" ? u.toString() : undefined;
  } catch {
    return undefined;
  }
}
