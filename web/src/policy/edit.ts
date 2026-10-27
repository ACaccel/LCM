import { isMap, isSeq, parseDocument } from "yaml";
import type { Document, Scalar, YAMLMap, YAMLSeq } from "yaml";
import { LAYOUT_ANNOTATION } from "./model";
import type { Position } from "./model";

/**
 * Text-to-text edits of a policy document. Each edit parses the text with the
 * YAML library, changes only what it must in the document tree and writes it
 * back, so comments, key order and fields this UI does not know survive.
 */

function mutate(text: string, fn: (doc: Document) => void): string {
  const doc = parseDocument(text);
  if (doc.errors.length > 0) throw new Error(doc.errors[0]?.message ?? "invalid YAML");
  fn(doc);
  return doc.toString({ lineWidth: 0 });
}

function nodesSeq(doc: Document): YAMLSeq | undefined {
  const seq = doc.getIn(["spec", "nodes"], true);
  return isSeq(seq) ? seq : undefined;
}

function findNode(doc: Document, id: string): { map: YAMLMap; index: number } | undefined {
  const seq = nodesSeq(doc);
  if (!seq) return undefined;
  for (let i = 0; i < seq.items.length; i++) {
    const item = seq.items[i];
    if (isMap(item) && item.get("id") === id) return { map: item, index: i };
  }
  return undefined;
}

function isRecord(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

export function deepEqual(a: unknown, b: unknown): boolean {
  if (a === b) return true;
  if (Array.isArray(a) && Array.isArray(b)) return a.length === b.length && a.every((x, i) => deepEqual(x, b[i]));
  if (isRecord(a) && isRecord(b)) {
    const ka = Object.keys(a);
    return ka.length === Object.keys(b).length && ka.every((k) => k in b && deepEqual(a[k], b[k]));
  }
  return false;
}

/** Applies the difference between two plain values at a path, leaving equal parts untouched. */
function applyDiff(doc: Document, path: (string | number)[], before: unknown, after: unknown): void {
  if (deepEqual(before, after)) return;
  if (after === undefined) {
    doc.deleteIn(path);
    return;
  }
  if (isRecord(before) && isRecord(after)) {
    for (const k of new Set([...Object.keys(before), ...Object.keys(after)])) {
      applyDiff(doc, [...path, k], before[k], after[k]);
    }
    return;
  }
  doc.setIn(path, after);
}

function pruneEmpty(doc: Document, path: (string | number)[]): void {
  const node = doc.getIn(path, true);
  if (isMap(node) && node.items.length === 0) doc.deleteIn(path);
}

/** Lowercase identifier for a node id derived from its type, unique among `taken`. */
export function uniqueNodeId(type: string, taken: Iterable<string>): string {
  const used = new Set(taken);
  const base = type.replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "node";
  const start = /^[a-z]/.test(base) ? base : `n_${base}`;
  if (!used.has(start)) return start;
  for (let n = 2; ; n++) {
    const id = `${start}_${n}`;
    if (!used.has(id)) return id;
  }
}

export function addNode(text: string, node: { id: string; type: string; props?: Record<string, unknown> }): string {
  return mutate(text, (doc) => {
    let seq = nodesSeq(doc);
    if (!seq) {
      doc.setIn(["spec", "nodes"], []);
      seq = nodesSeq(doc);
    }
    seq?.add(doc.createNode({ id: node.id, type: node.type, ...(node.props && Object.keys(node.props).length ? { props: node.props } : {}) }));
  });
}

function dropTarget(next: YAMLMap, id: string, onlyPort?: string): void {
  for (const pair of [...next.items]) {
    const port = String((pair.key as Scalar).value);
    if (onlyPort !== undefined && port !== onlyPort) continue;
    const list = pair.value;
    if (!isSeq(list)) continue;
    const before = list.items.length;
    list.items = list.items.filter((it) => (it as { value?: unknown }).value !== id);
    if (list.items.length === 0 && before > 0) next.items = next.items.filter((p) => p !== pair);
  }
}

export function removeNode(text: string, id: string): string {
  return mutate(text, (doc) => {
    const seq = nodesSeq(doc);
    const found = findNode(doc, id);
    if (!seq || !found) return;
    seq.items.splice(found.index, 1);
    for (const item of seq.items) {
      if (!isMap(item)) continue;
      const next = item.get("next", true);
      if (!isMap(next)) continue;
      dropTarget(next, id);
      if (next.items.length === 0) item.delete("next");
    }
    const layout = readLayout(doc);
    if (layout && id in layout) {
      delete layout[id];
      writeLayout(doc, layout);
    }
  });
}

export function connect(text: string, from: string, port: string, to: string): string {
  return mutate(text, (doc) => {
    const found = findNode(doc, from);
    if (!found) return;
    const list = found.map.getIn(["next", port], true);
    if (isSeq(list)) {
      if (list.items.some((it) => (it as { value?: unknown }).value === to)) return;
      list.add(to);
      return;
    }
    found.map.setIn(["next", port], [to]);
  });
}

export function disconnect(text: string, from: string, port: string, to: string): string {
  return mutate(text, (doc) => {
    const found = findNode(doc, from);
    if (!found) return;
    const next = found.map.get("next", true);
    if (!isMap(next)) return;
    dropTarget(next, to, port);
    if (next.items.length === 0) found.map.delete("next");
  });
}

/**
 * Replaces the props of a node. The new value is compared with what the
 * document holds now and only the differing leaves are written, so untouched
 * props keep their comments and formatting.
 */
export function setProps(text: string, id: string, after: Record<string, unknown>): string {
  return mutate(text, (doc) => {
    const found = findNode(doc, id);
    if (!found) return;
    const base: (string | number)[] = ["spec", "nodes", found.index, "props"];
    const current = found.map.get("props", true);
    const before = isMap(current) ? (current.toJSON() as Record<string, unknown>) : {};
    applyDiff(doc, base, before, after);
    pruneEmpty(doc, base);
  });
}

/** Sets or removes a scalar field of a node (timeout, joinMode). */
export function setNodeField(text: string, id: string, field: "timeout" | "joinMode", value: string | undefined): string {
  return mutate(text, (doc) => {
    const found = findNode(doc, id);
    if (!found) return;
    if (value === undefined || value === "") found.map.delete(field);
    else found.map.set(field, value);
  });
}

export function setRetry(text: string, id: string, retry: { max?: number; backoff?: string; maxBackoff?: string } | undefined): string {
  return mutate(text, (doc) => {
    const found = findNode(doc, id);
    if (!found) return;
    const clean = retry ? Object.fromEntries(Object.entries(retry).filter(([, v]) => v !== undefined && v !== "")) : {};
    if (Object.keys(clean).length === 0) {
      found.map.delete("retry");
      return;
    }
    const current = found.map.get("retry", true);
    if (isMap(current)) {
      applyDiff(doc, ["spec", "nodes", found.index, "retry"], current.toJSON(), clean);
    } else {
      found.map.set("retry", doc.createNode(clean));
    }
  });
}

/** Renames a node and every edge that points at it. Expressions are not rewritten. */
export function renameNode(text: string, oldId: string, newId: string): string {
  return mutate(text, (doc) => {
    const seq = nodesSeq(doc);
    const found = findNode(doc, oldId);
    if (!seq || !found) return;
    found.map.set("id", newId);
    for (const item of seq.items) {
      if (!isMap(item)) continue;
      const next = item.get("next", true);
      if (!isMap(next)) continue;
      for (const pair of next.items) {
        if (!isSeq(pair.value)) continue;
        for (const it of pair.value.items) {
          const scalar = it as { value?: unknown };
          if (scalar.value === oldId) scalar.value = newId;
        }
      }
    }
    const layout = readLayout(doc);
    if (layout && oldId in layout) {
      layout[newId] = layout[oldId] as Position;
      delete layout[oldId];
      writeLayout(doc, layout);
    }
  });
}

function readLayout(doc: Document): Record<string, Position> | undefined {
  const raw = doc.getIn(["metadata", "annotations", LAYOUT_ANNOTATION]);
  if (typeof raw !== "string") return undefined;
  try {
    const v: unknown = JSON.parse(raw);
    return isRecord(v) ? (v as Record<string, Position>) : undefined;
  } catch {
    return undefined;
  }
}

function writeLayout(doc: Document, layout: Record<string, Position>): void {
  const ids: string[] = [];
  const seq = nodesSeq(doc);
  for (const item of seq?.items ?? []) if (isMap(item) && typeof item.get("id") === "string") ids.push(item.get("id") as string);
  const ordered: Record<string, Position> = {};
  // Only nodes that exist keep a position; the document order keeps diffs stable.
  for (const id of ids) {
    const p = layout[id];
    if (p) ordered[id] = { x: Math.round(p.x), y: Math.round(p.y) };
  }
  if (Object.keys(ordered).length === 0) {
    doc.deleteIn(["metadata", "annotations", LAYOUT_ANNOTATION]);
    pruneEmpty(doc, ["metadata", "annotations"]);
    return;
  }
  doc.setIn(["metadata", "annotations", LAYOUT_ANNOTATION], JSON.stringify(ordered));
}

/** Stores node positions in metadata.annotations; unmentioned nodes keep theirs. */
export function setLayout(text: string, positions: Record<string, Position>, replace = false): string {
  return mutate(text, (doc) => {
    const current = replace ? {} : (readLayout(doc) ?? {});
    writeLayout(doc, { ...current, ...positions });
  });
}

/** Sets or removes a value at a path below the document root (for example spec.timeout). */
export function setPath(text: string, path: (string | number)[], value: unknown): string {
  return mutate(text, (doc) => {
    if (value === undefined || value === "") {
      doc.deleteIn(path);
      for (let n = path.length - 1; n > 0; n--) pruneEmpty(doc, path.slice(0, n));
    } else doc.setIn(path, value);
  });
}
