import { describe, expect, it } from "vitest";
import { nameMatches, nextSort, parseSort, sortRows } from "./list";

interface Row {
  name: string;
  n: number | null;
}

const rows: Row[] = [
  { name: "web-10", n: 2 },
  { name: "Api", n: null },
  { name: "web-9", n: 1 },
  { name: "db", n: 2 },
];
const by = { name: (r: Row) => r.name, n: (r: Row) => r.n };

describe("list helpers", () => {
  it("sorts names naturally and ignoring case", () => {
    expect(sortRows(rows, { key: "name", dir: "asc" }, by).map((r) => r.name)).toEqual(["Api", "db", "web-9", "web-10"]);
    expect(sortRows(rows, { key: "name", dir: "desc" }, by).map((r) => r.name)).toEqual(["web-10", "web-9", "db", "Api"]);
  });

  it("keeps equal rows in their order and missing values last in both directions", () => {
    expect(sortRows(rows, { key: "n", dir: "asc" }, by).map((r) => r.name)).toEqual(["web-9", "web-10", "db", "Api"]);
    expect(sortRows(rows, { key: "n", dir: "desc" }, by).map((r) => r.name)).toEqual(["web-10", "db", "web-9", "Api"]);
  });

  it("reads the sort from the URL and toggles it", () => {
    const initial = { key: "name", dir: "asc" } as const;
    expect(parseSort(new URLSearchParams("sort=n&dir=desc"), ["name", "n"], initial)).toEqual({ key: "n", dir: "desc" });
    expect(parseSort(new URLSearchParams("sort=bogus&dir=desc"), ["name", "n"], initial)).toEqual(initial);
    expect(nextSort({ key: "name", dir: "asc" }, "name")).toEqual({ key: "name", dir: "desc" });
    expect(nextSort({ key: "name", dir: "desc" }, "n")).toEqual({ key: "n", dir: "asc" });
  });

  it("matches names regardless of case and surrounding spaces", () => {
    expect(nameMatches("Web-API", " api ")).toBe(true);
    expect(nameMatches("web", "WEB")).toBe(true);
    expect(nameMatches("web", "db")).toBe(false);
  });
});
