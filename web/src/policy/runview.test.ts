import { describe, expect, it } from "vitest";
import type { RunNode } from "../api/types";
import { buildModel } from "./model";
import type { PolicyModel } from "./model";
import { statusByTopLevelNode, takenEdgeIds } from "./runview";

const model = buildModel({
  metadata: { name: "p" },
  spec: {
    nodes: [
      { id: "start", type: "trigger.manual", next: { out: ["check"] } },
      { id: "check", type: "flow.if", next: { true: ["deploy"], false: ["notify"] } },
      { id: "deploy", type: "module", props: { module: "m" }, next: { out: ["notify"] } },
      { id: "notify", type: "notify" },
    ],
  },
}) as PolicyModel;

const rn = (id: string, status: RunNode["status"], port?: string): RunNode => ({ id, type: "t", status, ...(port ? { port } : {}) });

describe("run view helpers", () => {
  it("colours top-level nodes from their own entries", () => {
    const m = statusByTopLevelNode(model, [rn("start", "completed", "out"), rn("check", "running")]);
    expect(m.get("start")).toBe("completed");
    expect(m.get("check")).toBe("running");
    expect(m.has("notify")).toBe(false);
  });

  it("derives a module node's status from its scoped parts", () => {
    const parts = (a: RunNode["status"], b: RunNode["status"]) => [rn("deploy/inner/one", a), rn("deploy/inner/two", b)];
    expect(statusByTopLevelNode(model, parts("completed", "completed")).get("deploy")).toBe("completed");
    expect(statusByTopLevelNode(model, parts("completed", "running")).get("deploy")).toBe("running");
    expect(statusByTopLevelNode(model, parts("failed", "running")).get("deploy")).toBe("failed");
    expect(statusByTopLevelNode(model, parts("completed", "pending")).get("deploy")).toBe("pending");
  });

  it("prefers a module node's own entry over its parts", () => {
    const m = statusByTopLevelNode(model, [rn("deploy", "suspended"), rn("deploy/x", "completed")]);
    expect(m.get("deploy")).toBe("suspended");
  });

  it("marks only the edges whose source took that port", () => {
    const taken = takenEdgeIds(model, [rn("start", "completed", "out"), rn("check", "completed", "false")]);
    const named = model.edges.filter((e) => taken.has(e.id)).map((e) => `${e.from}:${e.port}>${e.to}`);
    expect(named.sort()).toEqual(["check:false>notify", "start:out>check"]);
  });
});
