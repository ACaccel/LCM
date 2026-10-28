import { describe, expect, it } from "vitest";
import type { Template } from "../api/types";
import { buildParams, initialValues, isRequired, suggestName } from "./template-form";

const tpl: Template = {
  id: "auto-deploy",
  title: "Auto deploy",
  description: "",
  serviceParam: "service",
  params: [
    { name: "service", type: "service", description: "", required: true },
    { name: "repo", type: "string", description: "", required: true },
    { name: "branch", type: "string", description: "", required: false, default: "main" },
    { name: "requireCI", type: "boolean", description: "", required: false, default: true },
    { name: "ciRepo", type: "string", description: "", required: false, requiredIf: "requireCI" },
    { name: "ciWorkflows", type: "strings", description: "", required: false, default: [] },
    { name: "windows", type: "json", description: "", required: false, default: [{ start: "00:00", end: "24:00" }] },
  ],
};

describe("template form", () => {
  it("presets the service and shows defaults", () => {
    const v = initialValues(tpl, "web");
    expect(v).toMatchObject({ service: "web", repo: "", branch: "main", requireCI: true, ciWorkflows: "" });
    expect(v["windows"]).toBe('[{"start":"00:00","end":"24:00"}]');
  });

  it("sends only changed values, the service and booleans", () => {
    const v = { ...initialValues(tpl, "web"), repo: "git@h:me/web.git", ciRepo: "me/web", ciWorkflows: "ci.yml, secret-scan.yml" };
    const { params, errors } = buildParams(tpl, v, "web");
    expect(errors).toEqual({});
    expect(params).toEqual({
      service: "web",
      repo: "git@h:me/web.git",
      requireCI: true,
      ciRepo: "me/web",
      ciWorkflows: ["ci.yml", "secret-scan.yml"],
    });
  });

  it("reports missing required fields, including conditional ones", () => {
    const { errors } = buildParams(tpl, initialValues(tpl, "web"), "web");
    expect(Object.keys(errors).sort()).toEqual(["ciRepo", "repo"]);
    const off = { ...initialValues(tpl, "web"), repo: "x", requireCI: false };
    expect(buildParams(tpl, off, "web").errors).toEqual({});
    const ciRepo = tpl.params[4];
    expect(isRequired(ciRepo!, off)).toBe(false);
    expect(isRequired(ciRepo!, { ...off, requireCI: true })).toBe(true);
  });

  it("rejects JSON that does not parse", () => {
    const v = { ...initialValues(tpl, "web"), repo: "x", requireCI: false, windows: "[{" };
    expect(buildParams(tpl, v, "web").errors["windows"]).toBeTruthy();
    const ok = { ...v, windows: '[{"start":"02:00","end":"05:00"}]' };
    expect(buildParams(tpl, ok, "web").params["windows"]).toEqual([{ start: "02:00", end: "05:00" }]);
  });

  it("suggests a valid name", () => {
    expect(suggestName("restart-on-down", "web")).toBe("web-restart-on-down");
    expect(suggestName("restart-on-down")).toBe("restart-on-down");
  });
});
