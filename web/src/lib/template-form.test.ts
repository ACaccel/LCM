import { describe, expect, it } from "vitest";
import type { Template } from "../api/types";
import { buildParams, initialValues, isRequired, isVisible, prefillValues, suggestName, windowRows, windowsValue } from "./template-form";

const tpl: Template = {
  id: "auto-deploy",
  title: "Auto deploy",
  description: "",
  resourceParam: "resource",
  params: [
    { name: "resource", type: "service", description: "", required: true },
    { name: "repo", type: "string", description: "", required: true, prefill: "source.url" },
    { name: "branch", type: "string", description: "", required: false, default: "main", prefill: "source.branch" },
    { name: "requireCI", type: "boolean", description: "", required: false, default: true },
    { name: "ciRepo", type: "string", description: "", required: false, requiredIf: "requireCI" },
    { name: "ciWorkflows", type: "strings", description: "", required: false, default: [] },
    { name: "windows", type: "windows", description: "", required: false, default: [{ start: "00:00", end: "24:00" }] },
    { name: "extra", type: "json", description: "", required: false, default: { a: 1 } },
  ],
};

describe("template form", () => {
  it("presets the resource and shows defaults", () => {
    const v = initialValues(tpl, "web");
    expect(v).toMatchObject({ resource: "web", repo: "", branch: "main", requireCI: true, ciWorkflows: "" });
    expect(v["windows"]).toEqual([{ days: [], start: "00:00", end: "00:00" }]);
    expect(v["extra"]).toBe('{"a":1}');
  });

  it("sends only changed values, the resource and booleans", () => {
    const v = { ...initialValues(tpl, "web"), repo: "git@h:me/web.git", ciRepo: "me/web", ciWorkflows: "ci.yml, secret-scan.yml" };
    const { params, errors } = buildParams(tpl, v, "web");
    expect(errors).toEqual({});
    expect(params).toEqual({
      resource: "web",
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

  it("edits windows as rows and sends them in the server's format", () => {
    const rows = [
      { days: ["fri", "mon"], start: "22:00", end: "00:00" },
      { days: [], start: "02:00", end: "05:00" },
    ];
    const v = { ...initialValues(tpl, "web"), repo: "x", requireCI: false, windows: rows };
    expect(buildParams(tpl, v, "web").params["windows"]).toEqual([
      { days: ["mon", "fri"], start: "22:00", end: "24:00" },
      { start: "02:00", end: "05:00" },
    ]);
    // Rows read back the same way, and unchanged rows are not sent.
    expect(windowRows(windowsValue(rows))).toEqual([
      { days: ["mon", "fri"], start: "22:00", end: "00:00" },
      { days: [], start: "02:00", end: "05:00" },
    ]);
    expect(buildParams(tpl, { ...v, windows: initialValues(tpl)["windows"]! }, "web").params).not.toHaveProperty("windows");
    expect(windowRows("not a list")).toEqual([]);
  });

  it("rejects JSON that does not parse", () => {
    const v = { ...initialValues(tpl, "web"), repo: "x", requireCI: false, extra: "{" };
    expect(buildParams(tpl, v, "web").errors["extra"]).toBeTruthy();
    expect(buildParams(tpl, { ...v, extra: '{"b":2}' }, "web").params["extra"]).toEqual({ b: 2 });
  });

  it("prefills parameters from the selected resource's configuration", () => {
    expect(prefillValues(tpl, { source: { url: "https://github.com/acme/web.git", branch: "release" } })).toEqual({
      repo: "https://github.com/acme/web.git",
      branch: "release",
    });
    expect(prefillValues(tpl, { source: { branch: "" } })).toEqual({});
    expect(prefillValues(tpl, {})).toEqual({});
    expect(prefillValues(tpl, undefined)).toEqual({});
  });

  it("hides and omits the fields of another notification target", () => {
    type Param = Template["params"][number];
    const target: Param = { name: "notifyTarget", type: "string", description: "", required: false, default: "discord", enum: ["discord", "konata"] };
    const secret: Param = { name: "notifySecret", type: "secret", description: "", required: false, default: "", visibleIf: { param: "notifyTarget", equals: "discord" } };
    const url: Param = { name: "notifyUrl", type: "string", description: "", required: false, default: "", visibleIf: { param: "notifyTarget", equals: "konata" } };
    const t: Template = { ...tpl, params: [{ name: "resource", type: "service", description: "", required: true }, target, secret, url] };
    const values = { ...initialValues(t, "web"), notifySecret: "hook", notifyUrl: "http://x" };
    expect([isVisible(target, values), isVisible(secret, values), isVisible(url, values)]).toEqual([true, true, false]);
    expect(buildParams(t, values, "web").params).toEqual({ resource: "web", notifySecret: "hook" });
    const konata = { ...values, notifyTarget: "konata" };
    expect([isVisible(secret, konata), isVisible(url, konata)]).toEqual([false, true]);
    expect(buildParams(t, konata, "web").params).toEqual({ resource: "web", notifyTarget: "konata", notifyUrl: "http://x" });
  });

  it("suggests a valid name", () => {
    expect(suggestName("restart-on-down", "web")).toBe("web-restart-on-down");
    expect(suggestName("restart-on-down")).toBe("restart-on-down");
  });
});
