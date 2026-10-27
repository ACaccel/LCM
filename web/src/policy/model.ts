import { parseDocument } from "yaml";
import type { Document } from "yaml";

export const LAYOUT_ANNOTATION = "lcm.dev/layout";

export interface Position {
  x: number;
  y: number;
}

export interface ModelNode {
  id: string;
  type: string;
  /** Index in spec.nodes; the issue paths of the server use it. */
  index: number;
  props: Record<string, unknown>;
  timeout?: string;
  retry?: { max?: number; backoff?: string; maxBackoff?: string };
  joinMode?: string;
}

export interface ModelEdge {
  id: string;
  from: string;
  port: string;
  to: string;
}

export interface PolicyModel {
  name: string;
  nodes: ModelNode[];
  edges: ModelEdge[];
  layout: Record<string, Position>;
  /** Edges by the node on either end, so lookups never scan all edges. */
  edgesByNode: Map<string, ModelEdge[]>;
  /** Ports in use per node (keys of `next`), in document order. */
  portsByNode: Map<string, string[]>;
}

export type ParseResult = { ok: true; doc: Document; model: PolicyModel } | { ok: false; error: string };

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export function parseLayout(raw: unknown): Record<string, Position> {
  if (typeof raw !== "string" || raw === "") return {};
  try {
    const parsed: unknown = JSON.parse(raw);
    if (!isRecord(parsed)) return {};
    const out: Record<string, Position> = {};
    for (const [id, p] of Object.entries(parsed)) {
      if (isRecord(p) && Number.isFinite(p["x"]) && Number.isFinite(p["y"])) out[id] = { x: Number(p["x"]), y: Number(p["y"]) };
    }
    return out;
  } catch {
    return {};
  }
}

export function edgeId(from: string, port: string, to: string): string {
  return `${from}\u0001${port}\u0001${to}`;
}

/** Builds the graph model from a plain document value (YAML.toJS or JSON). */
export function buildModel(value: unknown): PolicyModel | string {
  if (!isRecord(value)) return "document must be a mapping";
  const spec = value["spec"];
  if (!isRecord(spec) || !Array.isArray(spec["nodes"])) return "spec.nodes must be a list";
  const meta = isRecord(value["metadata"]) ? value["metadata"] : {};
  const annotations = isRecord(meta["annotations"]) ? meta["annotations"] : {};

  const nodes: ModelNode[] = [];
  const edges: ModelEdge[] = [];
  const known = new Set<string>();
  spec["nodes"].forEach((raw, index) => {
    if (!isRecord(raw) || typeof raw["id"] !== "string") return;
    known.add(raw["id"]);
    nodes.push({
      id: raw["id"],
      type: typeof raw["type"] === "string" ? raw["type"] : "",
      index,
      props: isRecord(raw["props"]) ? raw["props"] : {},
      timeout: typeof raw["timeout"] === "string" ? raw["timeout"] : undefined,
      retry: isRecord(raw["retry"]) ? (raw["retry"] as ModelNode["retry"]) : undefined,
      joinMode: typeof raw["joinMode"] === "string" ? raw["joinMode"] : undefined,
    });
  });

  const edgesByNode = new Map<string, ModelEdge[]>();
  const portsByNode = new Map<string, string[]>();
  const link = (id: string, e: ModelEdge) => {
    const list = edgesByNode.get(id);
    if (list) list.push(e);
    else edgesByNode.set(id, [e]);
  };
  spec["nodes"].forEach((raw) => {
    if (!isRecord(raw) || typeof raw["id"] !== "string" || !isRecord(raw["next"])) return;
    const from = raw["id"];
    portsByNode.set(from, Object.keys(raw["next"]));
    for (const [port, targets] of Object.entries(raw["next"])) {
      if (!Array.isArray(targets)) continue;
      for (const to of targets) {
        // Dangling targets stay out of the graph; validation reports them.
        if (typeof to !== "string" || !known.has(to)) continue;
        const e: ModelEdge = { id: edgeId(from, port, to), from, port, to };
        edges.push(e);
        link(from, e);
        if (to !== from) link(to, e);
      }
    }
  });

  return {
    name: typeof meta["name"] === "string" ? meta["name"] : "",
    nodes,
    edges,
    layout: parseLayout(annotations[LAYOUT_ANNOTATION]),
    edgesByNode,
    portsByNode,
  };
}

/** Parses policy text into an editable YAML document and its graph model. */
export function parsePolicyText(text: string): ParseResult {
  const doc = parseDocument(text, { keepSourceTokens: false });
  if (doc.errors.length > 0) return { ok: false, error: doc.errors[0]?.message ?? "invalid YAML" };
  const model = buildModel(doc.toJS());
  if (typeof model === "string") return { ok: false, error: model };
  return { ok: true, doc, model };
}
