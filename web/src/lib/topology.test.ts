import { describe, expect, it } from "vitest";
import type { Relation, ResourceDetail } from "../api/types";
import { COL_W, NODE_H, NODE_W, ROW_H, buildNeighbourhood, buildTopology, edgeRoute, relationDepths, roundedPath, uniqueEdges } from "./topology";

const res = (name: string, type: "service" | "machine" | "router" = "service", health: "healthy" | "down" | "degraded" | "unknown" = "healthy") => ({ name, type, health, phase: "managed" as const });
const rel = (type: Relation["type"], target: string): Relation => ({ type, target });

describe("relation depth", () => {
  it("counts the longest chain of dependencies", () => {
    const depths = relationDepths(["web", "db", "host", "lonely"], [
      { from: "web", to: "db" },
      { from: "db", to: "host" },
      { from: "web", to: "host" },
    ]);
    expect(Object.fromEntries(depths)).toEqual({ web: 2, db: 1, host: 0, lonely: 0 });
  });

  it("survives a cycle", () => {
    const depths = relationDepths(["a", "b"], [
      { from: "a", to: "b" },
      { from: "b", to: "a" },
    ]);
    expect(depths.size).toBe(2);
    expect([...depths.values()].every(Number.isFinite)).toBe(true);
  });
});

describe("topology layout", () => {
  const resources = [res("web"), res("db"), res("host", "machine", "down"), res("gateway", "router"), res("alone")];
  const relations = new Map<string, Relation[]>([
    ["web", [rel("dependsOn", "db"), rel("controlPath", "gateway")]],
    ["db", [rel("runsOn", "host")]],
  ]);

  it("creates one edge per declared relation, pointing at the target", () => {
    const t = buildTopology(resources, relations);
    expect(t.edges.map((e) => [e.from, e.type, e.to])).toEqual([
      ["web", "dependsOn", "db"],
      ["web", "controlPath", "gateway"],
      ["db", "runsOn", "host"],
    ]);
  });

  it("puts dependents left of what they depend on, one column per depth", () => {
    const t = buildTopology(resources, relations);
    const at = Object.fromEntries(t.nodes.map((n) => [n.name, n]));
    expect(at["host"]?.depth).toBe(0);
    expect(at["db"]?.depth).toBe(1);
    expect(at["web"]?.depth).toBe(2);
    expect(at["web"]?.x).toBe(0);
    expect(at["db"]?.x).toBe(COL_W);
    expect(at["host"]?.x).toBe(2 * COL_W);
    // Nodes of one column do not overlap.
    const rightColumn = t.nodes.filter((n) => n.x === 2 * COL_W).map((n) => n.y);
    expect(new Set(rightColumn).size).toBe(rightColumn.length);
    expect(Math.min(...t.nodes.map((n) => n.y))).toBe(0);
    expect(t.nodes.every((n) => n.y % ROW_H === 0)).toBe(true);
  });

  it("carries health so nodes can be coloured by it", () => {
    const t = buildTopology(resources, relations);
    expect(t.nodes.find((n) => n.name === "host")?.health).toBe("down");
  });

  it("can hide resources without relations", () => {
    expect(buildTopology(resources, relations).nodes.map((n) => n.name)).toContain("alone");
    expect(buildTopology(resources, relations, { onlyLinked: true }).nodes.map((n) => n.name)).not.toContain("alone");
  });

  it("adds a placeholder for a target that does not exist", () => {
    const t = buildTopology([res("web")], new Map([["web", [rel("dependsOn", "ghost")]]]));
    const ghost = t.nodes.find((n) => n.name === "ghost");
    expect(ghost).toMatchObject({ missing: true, type: "unknown" });
    expect(t.edges).toHaveLength(1);
  });

  it("drops duplicate and self relations", () => {
    expect(uniqueEdges([{ from: "a", to: "b", type: "dependsOn" }, { from: "a", to: "b", type: "dependsOn" }, { from: "a", to: "a", type: "runsOn" }, { from: "a", to: "b", type: "runsOn" }])).toHaveLength(2);
  });
});

describe("neighbourhood of one resource", () => {
  const detail = { name: "db", type: "service", phase: "managed", document: { spec: { relations: [rel("runsOn", "host")] } } } as unknown as ResourceDetail;
  const known = [res("web"), res("db"), res("host", "machine"), res("cache")];

  it("joins declared relations with the dependents the server computed", () => {
    const t = buildNeighbourhood(
      detail,
      {
        dependents: [{ name: "web", type: "service", relation: "dependsOn", via: "db", depth: 1 }],
        controlDependents: [{ name: "cache", type: "service", relation: "controlPath", via: "host", depth: 2 }],
      },
      known,
    );
    expect(t.edges.map((e) => `${e.from}>${e.type}>${e.to}`).sort()).toEqual(["cache>controlPath>host", "db>runsOn>host", "web>dependsOn>db"]);
    expect(t.nodes.map((n) => n.name).sort()).toEqual(["cache", "db", "host", "web"]);
  });

  it("shows only the resource itself when it has no relations", () => {
    const t = buildNeighbourhood({ ...detail, document: { spec: {} } } as unknown as ResourceDetail, { dependents: [], controlDependents: [] }, known);
    expect(t.edges).toEqual([]);
    expect(t.nodes.map((n) => n.name)).toEqual(["db"]);
  });
});

describe("edges that skip a column", () => {
  // api depends on db and runs on workstation, which db runs on too: all
  // three share a row, so the api -> workstation edge would cross db.
  const t = buildTopology(
    [res("api"), res("db"), res("workstation", "machine")],
    new Map([
      ["api", [rel("dependsOn", "db"), rel("runsOn", "workstation")]],
      ["db", [rel("runsOn", "workstation")]],
    ]),
  );
  const at = Object.fromEntries(t.nodes.map((n) => [n.name, n]));

  it("puts the three resources on one row", () => {
    expect(new Set(t.nodes.map((n) => n.y)).size).toBe(1);
  });

  it("gives only the edge that skips a column a lane", () => {
    const lanes = Object.fromEntries(t.edges.map((e) => [`${e.from}>${e.to}`, e.lane]));
    expect(lanes["api>db"]).toBeUndefined();
    expect(lanes["db>workstation"]).toBeUndefined();
    expect(lanes["api>workstation"]).toBeDefined();
  });

  it("routes it through the gutter between rows, clear of every node", () => {
    const edge = t.edges.find((e) => e.from === "api" && e.to === "workstation")!;
    const from = at["api"]!;
    const to = at["workstation"]!;
    const route = edgeRoute({ x: from.x + NODE_W, y: from.y + NODE_H / 2 }, { x: to.x, y: to.y + NODE_H / 2 }, edge.lane);
    for (const node of t.nodes) {
      for (let i = 0; i + 1 < route.length; i++) {
        const a = route[i]!;
        const b = route[i + 1]!;
        const hits = Math.max(Math.min(a.x, b.x), node.x) < Math.min(Math.max(a.x, b.x), node.x + NODE_W) && Math.max(Math.min(a.y, b.y), node.y) < Math.min(Math.max(a.y, b.y), node.y + NODE_H);
        // The first and last segments leave and enter their own node at its edge.
        expect(hits, `${node.name} segment ${i}`).toBe(false);
      }
    }
  });

  it("keeps edges that share a gutter apart", () => {
    const wide = buildTopology(
      [res("a"), res("b"), res("c"), res("d")],
      new Map([
        ["a", [rel("dependsOn", "b"), rel("runsOn", "c"), rel("controlPath", "d")]],
        ["b", [rel("dependsOn", "c")]],
        ["c", [rel("dependsOn", "d")]],
      ]),
    );
    const lanes = wide.edges.flatMap((e) => (e.lane === undefined ? [] : [e.lane]));
    expect(lanes.length).toBe(2);
    expect(new Set(lanes).size).toBe(lanes.length);
    for (const lane of lanes) expect((lane % ROW_H) > NODE_H || lane < 0).toBe(true);
  });
});

describe("rounded paths", () => {
  it("passes through every corner with a curve", () => {
    const d = roundedPath([{ x: 0, y: 0 }, { x: 20, y: 0 }, { x: 20, y: 40 }, { x: 60, y: 40 }]);
    expect(d.startsWith("M 0 0")).toBe(true);
    expect(d.match(/Q/g)).toHaveLength(2);
    expect(d.endsWith("L 60 40")).toBe(true);
  });

  it("is empty without points", () => {
    expect(roundedPath([])).toBe("");
  });
});
