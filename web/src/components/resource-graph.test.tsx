import { fireEvent, render, screen } from "@testing-library/react";
import { beforeAll, describe, expect, it } from "vitest";
import { buildTopology } from "../lib/topology";
import { stubGraphEnvironment } from "../test-graph-env";
import { ResourceGraph, TopologyLegend } from "./resource-graph";

beforeAll(stubGraphEnvironment);

const resources = [
  { name: "web", type: "service" as const, health: "healthy" as const, phase: "managed" as const },
  { name: "host", type: "machine" as const, health: "down" as const, phase: "managed" as const },
];
const topology = buildTopology(resources, new Map([["web", [{ type: "runsOn" as const, target: "host" }, { type: "dependsOn" as const, target: "ghost" }]]]));

describe("resource graph", () => {
  it("renders each resource as a button naming its type and health", () => {
    render(<ResourceGraph topology={topology} onOpen={() => undefined} />);
    expect(screen.getByRole("button", { name: "開啟資源 web，服務，健康" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "開啟資源 host，機器，異常" })).toBeTruthy();
  });

  it("shows a relation target that does not exist as unavailable", () => {
    render(<ResourceGraph topology={topology} onOpen={() => undefined} />);
    const ghost = screen.getByRole("button", { name: /ghost，目標不存在/ });
    expect((ghost as HTMLButtonElement).disabled).toBe(true);
  });

  it("colours the node by health and outlines the focused resource", () => {
    const { container } = render(<ResourceGraph topology={topology} focus="web" onOpen={() => undefined} />);
    const classes = (name: string) => screen.getByRole("button", { name: new RegExp(`^開啟資源 ${name}，`) }).closest(".topo-node")?.className ?? "";
    expect(classes("web")).toContain("ok");
    expect(classes("web")).toContain("focus");
    expect(classes("host")).toContain("bad");
    expect(container.querySelectorAll(".react-flow__node")).toHaveLength(3);
  });

  it("opens the resource that is clicked", () => {
    const opened: string[] = [];
    render(<ResourceGraph topology={topology} onOpen={(n) => opened.push(n)} />);
    fireEvent.click(screen.getByRole("button", { name: /^開啟資源 host/ }));
    expect(opened).toEqual(["host"]);
  });

  it("explains the three edge styles", () => {
    render(<TopologyLegend />);
    expect(screen.getByText("依賴")).toBeTruthy();
    expect(screen.getByText("承載於")).toBeTruthy();
    expect(screen.getByText("控制路徑")).toBeTruthy();
  });
});
