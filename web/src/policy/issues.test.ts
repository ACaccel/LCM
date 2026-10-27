import { describe, expect, it } from "vitest";
import { autoLayout, issuesAt, issuesExactlyAt, mapIssues, nextPosition, tokenizePath } from "./issues";
import { parsePolicyText } from "./model";

const text = `apiVersion: lcm/v1
kind: Policy
metadata: { name: p }
spec:
  nodes:
    - { id: start, type: trigger.manual, next: { out: [a, b] } }
    - { id: a, type: flow.switch, props: { cases: [ { port: x, when: "1" }, { port: y } ] }, next: { x: [c] } }
    - { id: b, type: http.get, props: { url: "" }, next: { out: [c] } }
    - { id: c, type: data.set }
`;

function model() {
  const r = parsePolicyText(text);
  if (!r.ok) throw new Error(r.error);
  return r.model;
}

describe("tokenizePath", () => {
  it("splits dots and indexes", () => {
    expect(tokenizePath("spec.nodes[3].next.true[0]")).toEqual(["spec", "nodes", 3, "next", "true", 0]);
    expect(tokenizePath("metadata.name")).toEqual(["metadata", "name"]);
    expect(tokenizePath("")).toEqual([]);
  });
});

describe("mapIssues", () => {
  it("assigns issues to nodes by index and keeps the field path", () => {
    const m = mapIssues(
      [
        { path: "spec.nodes[2].props.url", message: "required" },
        { path: "spec.nodes[1].props.cases[1].when", message: "missing" },
        { path: "spec.nodes[1].next.x[0]", message: "unknown node" },
        { path: "metadata.name", message: "bad" },
        { path: "spec.nodes[9].type", message: "ghost" },
        { path: "", message: "general" },
      ],
      model(),
    );
    expect(m.byNode.get("b")?.[0]?.field).toEqual(["props", "url"]);
    expect(m.byNode.get("a")).toHaveLength(2);
    expect(m.document.map((x) => x.issue.message)).toEqual(["bad", "ghost", "general"]);
    expect(m.all).toHaveLength(6);
  });

  it("finds issues for a field and below it", () => {
    const m = mapIssues(
      [
        { path: "spec.nodes[1].props.cases[1].when", message: "missing" },
        { path: "spec.nodes[1].props.cases", message: "too few" },
      ],
      model(),
    );
    const node = m.byNode.get("a");
    expect(issuesAt(node, ["props", "cases"])).toHaveLength(2);
    expect(issuesExactlyAt(node, ["props", "cases"])).toHaveLength(1);
    expect(issuesExactlyAt(node, ["props", "cases", 1, "when"])).toHaveLength(1);
    expect(issuesAt(node, ["props", "other"])).toHaveLength(0);
  });

  it("works without a model", () => {
    const m = mapIssues([{ path: "spec.nodes[0].id", message: "x" }], undefined);
    expect(m.document).toHaveLength(1);
  });
});

describe("autoLayout", () => {
  it("places nodes in layers by longest path", () => {
    const pos = autoLayout(model());
    expect(pos["start"]?.x).toBe(0);
    expect(pos["a"]?.x).toBe(280);
    expect(pos["b"]?.x).toBe(280);
    expect(pos["c"]?.x).toBe(560);
    expect(pos["a"]?.y).not.toBe(pos["b"]?.y);
  });

  it("terminates on cycles and gives every node a position", () => {
    const r = parsePolicyText(`metadata: { name: p }
spec:
  nodes:
    - { id: a, type: t, next: { out: [b] } }
    - { id: b, type: t, next: { out: [a] } }
`);
    if (!r.ok) throw new Error(r.error);
    const pos = autoLayout(r.model);
    expect(Object.keys(pos).sort()).toEqual(["a", "b"]);
  });
});

describe("nextPosition", () => {
  it("starts at the origin on an empty canvas", () => {
    expect(nextPosition({})).toEqual({ x: 0, y: 0 });
  });

  it("goes right of the anchor and steps down past occupied spots", () => {
    const pos = { a: { x: 0, y: 0 }, b: { x: 280, y: 0 }, c: { x: 280, y: 120 } };
    expect(nextPosition(pos, "a")).toEqual({ x: 280, y: 240 });
    expect(nextPosition(pos, "c")).toEqual({ x: 560, y: 120 });
  });

  it("without an anchor continues after the rightmost node", () => {
    expect(nextPosition({ a: { x: 0, y: 40 }, b: { x: 280, y: 40 } })).toEqual({ x: 560, y: 40 });
  });
});
