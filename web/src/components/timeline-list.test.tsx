import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { TimelineEntry } from "../api/types";
import { TimelineEvents } from "./timeline-list";

const entry = (over: Partial<TimelineEntry>): TimelineEntry => ({
  id: 1,
  resource: "web",
  time: "2026-11-01T00:00:00Z",
  kind: "deployment",
  summary: "",
  summaryMessage: { code: "", params: {}, message: "" },
  data: {},
  ref: "",
  ...over,
});

describe("TimelineEvents", () => {
  it("translates summaries by code and keeps older entries' English text", () => {
    render(
      <MemoryRouter>
        <TimelineEvents
          entries={[
            entry({
              summary: "deploy failed at abc",
              summaryMessage: { code: "timeline.action", params: { action: "deploy", outcome: "failed", job: "", version: "abc" }, message: "deploy failed at abc" },
              data: { actor: "admin", outcome: "failed" },
            }),
            entry({
              id: 2,
              kind: "config.conflict",
              summary: "commit abc of source infra changes Resource/web",
              summaryMessage: { code: "", params: {}, message: "commit abc of source infra changes Resource/web" },
            }),
          ]}
        />
      </MemoryRouter>,
    );
    expect(screen.getByText("部署失敗，版本 abc")).toBeTruthy();
    expect(screen.getByText("admin · 失敗")).toBeTruthy();
    expect(screen.getByText("設定衝突")).toBeTruthy();
    expect(screen.getByText("commit abc of source infra changes Resource/web")).toBeTruthy();
  });

  it("labels an observation event with its source, whether it was recovered later and a gap", () => {
    const { container } = render(
      <MemoryRouter>
        <TimelineEvents
          entries={[
            entry({ kind: "observation", summary: "事件缺口：journal cursor expired", summaryMessage: { code: "", params: {}, message: "事件缺口：journal cursor expired" }, data: { severity: "warning", source: "journal", unit: "", historical: true, gap: true } }),
            entry({ kind: "observation", summary: "重新開機", summaryMessage: { code: "", params: {}, message: "重新開機" }, data: { severity: "info", source: "poll", unit: "app.service", historical: false, gap: false } }),
          ]}
        />
      </MemoryRouter>,
    );
    expect(screen.getByText("系統日誌 · 歷史補讀 · 事件缺口")).toBeTruthy();
    expect(screen.getByText("輪詢 · 即時事件 · app.service")).toBeTruthy();
    expect([...container.querySelectorAll(".kind")].map((k) => k.textContent)).toEqual(["觀測事件", "觀測事件"]);
    expect(container.querySelector(".dot.d-warn")).toBeTruthy();
  });
});
