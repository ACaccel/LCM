import { describe, expect, it } from "vitest";
import { translate, translateAll } from "./messages";

describe("server messages", () => {
  it("translates by code and keeps the server's values", () => {
    expect(translate({ code: "health.threshold", params: { metric: "disk", target: "/var" }, message: "disk:/var" })).toBe("磁碟 /var 使用率超過門檻");
    expect(translate({ code: "health.checkDown", params: { check: "http" }, message: "check http is down" })).toBe("檢查 http 失敗");
    expect(translate({ code: "step.restart", params: { runtime: "systemd", name: "app.service", scope: "user" }, message: "restart …" })).toBe(
      "重啟 systemd unit app.service（使用者 scope）",
    );
    expect(
      translate({ code: "timeline.lifecycle", params: { previous: "stopped", state: "running", action: "start" }, message: "stopped -> running (start)" }),
    ).toBe("已停止 → 執行中（啟動）");
    expect(translate({ code: "timeline.action", params: { action: "deploy", outcome: "succeeded", job: "", version: "abc" }, message: "deploy succeeded at abc" })).toBe(
      "部署成功，版本 abc",
    );
  });

  it("labels enumerated parameters it knows and shows unknown ones as they are", () => {
    expect(translate({ code: "operation.state", params: { state: "observing" }, message: "observing" })).toBe("觀測中");
    expect(translate({ code: "operation.state", params: { state: "brand-new" }, message: "brand-new" })).toBe("brand-new");
  });

  it("falls back to the server's English text for free-form text and unknown codes", () => {
    expect(translate({ code: "", params: {}, message: "free text" })).toBe("free text");
    expect(translate({ code: "health.fromTheFuture", params: {}, message: "a newer reason" })).toBe("a newer reason");
  });

  it("joins several messages, or says none", () => {
    expect(translateAll([])).toBe("無");
    expect(
      translateAll([
        { code: "health.stale", params: {}, message: "observations missing or stale" },
        { code: "", params: {}, message: "other" },
      ]),
    ).toBe("觀測資料過期或缺少；other");
  });
});
