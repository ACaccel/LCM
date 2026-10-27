import { describe, expect, it } from "vitest";
import { addNode, connect, disconnect, removeNode, renameNode, setLayout, setNodeField, setPath, setProps, setRetry, uniqueNodeId } from "./edit";
import { buildModel, parsePolicyText } from "./model";

// Unknown fields, comments, a layout annotation and a future field on a node
// must all come through every edit unchanged.
const SRC = `apiVersion: lcm/v1
kind: Policy
metadata:
  name: demo
  labels: { team: ops }
  annotations:
    lcm.dev/layout: '{"start":{"x":10,"y":20},"check":{"x":300,"y":20}}'
    example.com/owner: me
x-future: keep-me
spec:
  concurrency: { mode: queue }
  nodes:
    # entry point
    - id: start
      type: trigger.manual
      futureNodeField: 42
      next:
        out: [check]
    - id: check
      type: flow.if
      props:
        condition: "trigger.ok == true" # keep this comment
        extra: { deep: 1 }
      next:
        "true": [restart]
        "false": []
    - id: restart
      type: lifecycle.restart
      props: { service: web }
`;

function model(text: string) {
  const r = parsePolicyText(text);
  if (!r.ok) throw new Error(r.error);
  return r.model;
}

describe("policy model", () => {
  it("derives nodes, edges, ports, layout and an edge index", () => {
    const m = model(SRC);
    expect(m.nodes.map((n) => n.id)).toEqual(["start", "check", "restart"]);
    expect(m.edges.map((e) => `${e.from}:${e.port}>${e.to}`)).toEqual(["start:out>check", "check:true>restart"]);
    expect(m.layout["check"]).toEqual({ x: 300, y: 20 });
    expect(m.portsByNode.get("check")).toEqual(["true", "false"]);
    expect(m.edgesByNode.get("check")?.length).toBe(2);
    expect(m.edgesByNode.get("restart")?.length).toBe(1);
  });

  it("reports unusable documents instead of throwing", () => {
    expect(parsePolicyText("a: [").ok).toBe(false);
    expect(buildModel("text")).toMatch(/mapping/);
    expect(buildModel({ spec: {} })).toMatch(/spec.nodes/);
  });

  it("ignores a broken layout annotation", () => {
    const text = SRC.replace(/lcm.dev\/layout: .*/, "lcm.dev/layout: 'not json'");
    expect(model(text).layout).toEqual({});
  });
});

describe("lossless edits", () => {
  const keeps = (out: string) => {
    expect(out).toContain("x-future: keep-me");
    expect(out).toContain("futureNodeField: 42");
    expect(out).toContain("# entry point");
    expect(out).toContain("# keep this comment");
    expect(out).toContain("example.com/owner: me");
    expect(out).toContain("labels: { team: ops }");
  };

  it("stores a moved node without touching anything else", () => {
    const out = setLayout(SRC, { restart: { x: 620.4, y: 99.6 } });
    keeps(out);
    expect(model(out).layout).toEqual({ start: { x: 10, y: 20 }, check: { x: 300, y: 20 }, restart: { x: 620, y: 100 } });
    expect(model(out).edges).toEqual(model(SRC).edges);
  });

  it("round-trips layout when replaced and drops entries of removed nodes", () => {
    const out = setLayout(SRC, { start: { x: 1, y: 2 } }, true);
    expect(model(out).layout).toEqual({ start: { x: 1, y: 2 } });
    const cleared = setLayout(setLayout(SRC, {}, true), {});
    expect(cleared).not.toContain("lcm.dev/layout");
    expect(cleared).toContain("example.com/owner: me");
  });

  it("changes one prop and keeps its siblings and comments", () => {
    const before = model(SRC).nodes[1]?.props ?? {};
    const out = setProps(SRC, "check", { ...before, condition: "trigger.ok == false" });
    keeps(out);
    expect(out).toContain("extra: { deep: 1 }");
    expect(model(out).nodes[1]?.props).toEqual({ condition: "trigger.ok == false", extra: { deep: 1 } });
  });

  it("removes cleared props and the empty props map", () => {
    const out = setProps(SRC, "restart", {});
    expect(model(out).nodes[2]?.props).toEqual({});
    expect(out).not.toMatch(/id: restart[\s\S]*props:/);
    keeps(out);
  });

  it("adds a node with props", () => {
    const out = addNode(SRC, { id: "wait", type: "flow.wait", props: { duration: "5s" } });
    expect(model(out).nodes.at(-1)).toMatchObject({ id: "wait", type: "flow.wait", props: { duration: "5s" } });
    keeps(out);
  });

  it("connects and disconnects without duplicating edges", () => {
    let out = connect(SRC, "check", "false", "restart");
    out = connect(out, "check", "false", "restart");
    expect(model(out).edges.map((e) => `${e.from}:${e.port}>${e.to}`)).toContain("check:false>restart");
    expect(model(out).edges.filter((e) => e.port === "false")).toHaveLength(1);
    out = connect(out, "restart", "error", "start");
    expect(model(out).portsByNode.get("restart")).toEqual(["error"]);
    out = disconnect(out, "restart", "error", "start");
    expect(model(out).portsByNode.has("restart")).toBe(false);
    keeps(out);
  });

  it("removes a node with its edges and layout entry", () => {
    const out = removeNode(SRC, "restart");
    const m = model(out);
    expect(m.nodes.map((n) => n.id)).toEqual(["start", "check"]);
    expect(m.edges.map((e) => e.id)).toEqual(model(SRC).edges.filter((e) => e.to !== "restart").map((e) => e.id));
    expect(m.portsByNode.get("check")).toEqual(["false"]);
    keeps(out);
    const withoutCheck = removeNode(SRC, "check");
    expect(model(withoutCheck).layout).toEqual({ start: { x: 10, y: 20 } });
    expect(model(withoutCheck).portsByNode.has("start")).toBe(false);
  });

  it("renames a node, its edges and its layout key", () => {
    const out = renameNode(SRC, "check", "decide");
    const m = model(out);
    expect(m.nodes.map((n) => n.id)).toEqual(["start", "decide", "restart"]);
    expect(m.edges.map((e) => `${e.from}:${e.port}>${e.to}`)).toEqual(["start:out>decide", "decide:true>restart"]);
    expect(m.layout["decide"]).toEqual({ x: 300, y: 20 });
    expect(m.layout["check"]).toBeUndefined();
    keeps(out);
  });

  it("sets node timeout, join mode, retry and spec fields", () => {
    let out = setNodeField(SRC, "check", "timeout", "30s");
    out = setNodeField(out, "check", "joinMode", "any");
    out = setRetry(out, "check", { max: 3, backoff: "2s" });
    out = setPath(out, ["spec", "timeout"], "10m");
    const doc = parsePolicyText(out);
    expect(doc.ok && doc.doc.toJS().spec.timeout).toBe("10m");
    expect(model(out).nodes[1]).toMatchObject({ timeout: "30s", joinMode: "any", retry: { max: 3, backoff: "2s" } });
    out = setNodeField(out, "check", "timeout", undefined);
    out = setRetry(out, "check", undefined);
    out = setPath(out, ["spec", "timeout"], undefined);
    expect(model(out).nodes[1]?.timeout).toBeUndefined();
    expect(model(out).nodes[1]?.retry).toBeUndefined();
    keeps(out);
  });

  it("returns the text unchanged in meaning when an edit finds no target", () => {
    expect(model(removeNode(SRC, "ghost")).nodes).toHaveLength(3);
    expect(model(connect(SRC, "ghost", "out", "start")).edges).toHaveLength(2);
  });

  it("refuses to edit text that does not parse", () => {
    expect(() => addNode("a: [", { id: "x", type: "y" })).toThrow();
  });
});

describe("uniqueNodeId", () => {
  it("derives a valid id from the type and avoids collisions", () => {
    expect(uniqueNodeId("http.get", [])).toBe("http_get");
    expect(uniqueNodeId("http.get", ["http_get"])).toBe("http_get_2");
    expect(uniqueNodeId("http.get", ["http_get", "http_get_2"])).toBe("http_get_3");
    expect(uniqueNodeId("9lives", [])).toMatch(/^[a-z][a-z0-9_]*$/);
    expect(uniqueNodeId("", [])).toBe("node");
  });
});
