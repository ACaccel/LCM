import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { NodeType } from "../../api/types";
import { Palette } from "./palette";

const type = (name: string, description?: string): NodeType => ({ type: name, category: name.split(".")[0] ?? "", ...(description ? { description } : {}), effect: "none", ports: ["out"], props: [] });

describe("node palette", () => {
  it("shows the server's description under each node type", () => {
    render(<Palette types={[type("flow.wait", "Waits for a duration"), type("flow.poll")]} onAdd={() => undefined} />);
    expect(screen.getByText("Waits for a duration")).toBeTruthy();
    // A type without a description is just its name.
    expect(screen.getByRole("button", { name: "flow.poll" })).toBeTruthy();
  });

  it("adds the clicked type and finds types by their name", () => {
    const added: string[] = [];
    render(<Palette types={[type("flow.wait", "Waits"), type("notify.http", "Sends")]} onAdd={(t) => added.push(t)} />);
    fireEvent.change(screen.getByRole("searchbox"), { target: { value: "notify" } });
    expect(screen.queryByText("Waits")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /notify\.http/ }));
    expect(added).toEqual(["notify.http"]);
  });
});
