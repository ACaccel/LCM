import { describe, expect, it } from "vitest";
import { collapseContext, diffLines, hasChanges } from "./diff";

const render = (a: string, b: string) =>
  diffLines(a, b)
    .map((l) => (l.kind === "add" ? "+" : l.kind === "del" ? "-" : " ") + l.text)
    .join("|");

describe("diffLines", () => {
  it("reports no change for equal texts", () => {
    expect(hasChanges(diffLines("a\nb\n", "a\nb\n"))).toBe(false);
  });

  it("marks replaced, added and removed lines", () => {
    expect(render("a\nb\nc", "a\nB\nc")).toBe(" a|-b|+B| c");
    expect(render("a\nc", "a\nb\nc")).toBe(" a|+b| c");
    expect(render("a\nb\nc", "a\nc")).toBe(" a|-b| c");
  });

  it("handles empty sides", () => {
    expect(render("", "x\ny")).toBe("+x|+y");
    expect(render("x\ny", "")).toBe("-x|-y");
    expect(diffLines("", "")).toEqual([]);
  });

  it("finds the shortest script across interleaved edits", () => {
    expect(render("1\n2\n3\n4\n5", "1\n3\n4\nx\n5")).toBe(" 1|-2| 3| 4|+x| 5");
  });

  it("ignores a trailing newline", () => {
    expect(hasChanges(diffLines("a\nb", "a\nb\n"))).toBe(false);
  });
});

describe("collapseContext", () => {
  it("keeps context around changes and separates distant hunks", () => {
    const lines = Array.from({ length: 20 }, (_, i) => `l${i}`);
    const changed = [...lines];
    changed[2] = "X";
    changed[17] = "Y";
    const out = collapseContext(diffLines(lines.join("\n"), changed.join("\n")), 1);
    expect(out.filter((l) => l.kind === "sep")).toHaveLength(1);
    expect(out.some((l) => l.text === "l10")).toBe(false);
    expect(out.some((l) => l.text === "l1")).toBe(true);
  });
});
