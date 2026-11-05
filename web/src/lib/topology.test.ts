import { describe, expect, it } from "vitest";
import type { Relation, ResourceDetail } from "../api/types";
import { COL_W, ROW_H, buildNeighbourhood, buildTopology, relationDepths, uniqueEdges } from "./topology";

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
