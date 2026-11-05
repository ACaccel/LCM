import { describe, expect, it } from "vitest";
import { reasonText, unavailableReason } from "./actions";

const message = (code: string, text: string, params: Record<string, string> = {}) => ({ code, params, message: text });

describe("reasonText", () => {
  it("keeps the service reason codes", () => {
    const base = { reasonMessage: message("", "") };
    expect(reasonText({ ...base, reason: "" }, "running")).toBe("");
    expect(reasonText({ ...base, reason: "busy" }, "running")).toBe("正在執行其他動作");
    expect(reasonText({ ...base, reason: "invalidState" }, "stopped")).toContain("已停止");
  });

  it("translates the coded reasons of machines and routers", () => {
    const text = "router is not managed";
    expect(reasonText({ reason: text, reasonMessage: message("action.notManaged", text, { type: "router" }) }, "unknown")).toBe("路由器不在納管中");
  });

  it("translates the shared Git ownership reason of every type, services included", () => {
    const text = "release Git ownership first";
    expect(reasonText({ reason: text, reasonMessage: message("action.releaseOwnershipFirst", text) }, "stopped")).toBe("請先解除 Git 擁有權");
  });

  it("falls back to the server's text for a code this console does not know", () => {
    const text = "a reason from a newer server";
    expect(reasonText({ reason: text, reasonMessage: message("action.fromTheFuture", text) }, "unknown")).toBe(text);
  });
});

describe("unavailableReason", () => {
  it("explains a disabled action in the console's language", () => {
    const text = "maintenance is already active";
    const detail = {
      state: "online",
      actions: [{ name: "maintenance-enter", available: false, reason: text, reasonMessage: message("action.maintenanceActive", text), risk: "reversible", policy: false, kind: "action" }],
    } as const;
    expect(unavailableReason({ ...detail, actions: [...detail.actions] }, "maintenance-enter")).toBe("已在維護中");
  });
});
