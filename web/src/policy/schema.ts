import type { NodeProp, NodeType } from "../api/types";
import type { ModelNode } from "./model";

const PALETTE = 6;

/** A stable chart colour per category; categories come from the server. */
export function categoryColor(category: string): string {
  let h = 0;
  for (const ch of category) h = (h * 31 + ch.charCodeAt(0)) >>> 0;
  return `var(--chart-${(h % PALETTE) + 1})`;
}

/** Triggers only start runs, so they get no input handle. */
export function hasInputPort(schema: NodeType | undefined): boolean {
  return schema?.category !== "trigger";
}

function collectPortValues(props: readonly NodeProp[] | undefined, value: unknown, out: Set<string>): void {
  if (!props || typeof value !== "object" || value === null) return;
  const rec = value as Record<string, unknown>;
  for (const p of props) {
    const v = rec[p.name];
    if (v === undefined) continue;
    if (p.name === "port" && typeof v === "string" && v) out.add(v);
    if (p.type === "array" && Array.isArray(v) && p.items) {
      for (const it of v) collectPortValues(p.items.props, it, out);
    } else if (p.type === "object") {
      collectPortValues(p.props, v, out);
    }
  }
}

/**
 * Output ports of a node: the fixed ones from its schema, those named by its
 * props when the type declares dynamic ports, those already wired in the
 * document, and the implicit `error` port.
 */
export function portsOf(schema: NodeType | undefined, node: ModelNode, wired: readonly string[] = []): string[] {
  const out = new Set<string>(schema?.ports ?? []);
  if (schema?.dynamicPorts) collectPortValues(schema.props, node.props, out);
  for (const p of wired) out.add(p);
  if (out.size === 0) out.add("out");
  out.delete("error");
  return [...out, "error"];
}

export interface PaletteGroup {
  category: string;
  types: NodeType[];
}

/** Node types grouped by category and filtered by a search text. */
export function groupNodeTypes(types: readonly NodeType[], search: string): PaletteGroup[] {
  const q = search.trim().toLowerCase();
  const groups = new Map<string, NodeType[]>();
  for (const t of types) {
    if (q && !t.type.toLowerCase().includes(q) && !(t.description ?? "").toLowerCase().includes(q) && !t.category.toLowerCase().includes(q)) continue;
    const list = groups.get(t.category);
    if (list) list.push(t);
    else groups.set(t.category, [t]);
  }
  return [...groups.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([category, list]) => ({ category, types: [...list].sort((a, b) => a.type.localeCompare(b.type)) }));
}
