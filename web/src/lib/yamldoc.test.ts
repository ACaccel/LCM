import { describe, expect, it } from "vitest";
import { dashboardUrl, docToYaml } from "./yamldoc";

describe("dashboardUrl", () => {
  const doc = (url: string) => ({ metadata: { annotations: { "lcm.dev/dashboard-url": url } } });
  it("accepts http and https links", () => {
    expect(dashboardUrl(doc("https://grafana.example/d/abc"))).toBe("https://grafana.example/d/abc");
    expect(dashboardUrl(doc("http://localhost:3000/"))).toBe("http://localhost:3000/");
  });
  it("rejects other schemes and garbage", () => {
    expect(dashboardUrl(doc("javascript:alert(1)"))).toBeUndefined();
    expect(dashboardUrl(doc("not a url"))).toBeUndefined();
    expect(dashboardUrl(undefined)).toBeUndefined();
    expect(dashboardUrl({ metadata: {} })).toBeUndefined();
  });
});

describe("docToYaml", () => {
  it("keeps key order and does not wrap long lines", () => {
    const out = docToYaml({ b: 1, a: "x".repeat(200) });
    expect(out.startsWith("b: 1\n")).toBe(true);
    expect(out.split("\n")[1]).toHaveLength(203);
  });
});
