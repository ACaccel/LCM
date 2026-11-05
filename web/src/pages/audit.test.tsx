import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { AuditEntry } from "../api/types";
import { s } from "../strings";
import { AuditPage } from "./audit";
import { jsonResponse } from "./resource/test-fixtures";

const entry = (id: number, over: Partial<AuditEntry> = {}): AuditEntry => ({
  id,
  time: "2026-11-01T10:00:00Z",
  actor: { kind: "user", id: "1", name: "admin" },
  action: "service.restart",
  target: "web",
  details: { actionId: "act-1" },
  outcome: "ok",
  ...over,
});

function setup(initial = "/audit") {
  const urls: URL[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    if (url.pathname !== "/api/v1/audit") return jsonResponse({ error: { code: "not_found", message: url.pathname, details: {} } }, 404);
    urls.push(url);
    if (url.searchParams.get("before") === "9") return jsonResponse({ items: [entry(8, { action: "token.create", target: "token:3", details: {}, outcome: "denied" })], nextBefore: null });
    return jsonResponse({ items: [entry(10), entry(9, { actor: { kind: "token", id: "2", name: "ci" }, action: "service.deploy", outcome: "error" })], nextBefore: 9 });
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[initial]}>
        <AuditPage />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return urls;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("audit page", () => {
  it("lists entries newest first with actor, action, target, outcome and details", async () => {
    const urls = setup();
    const [link] = await screen.findAllByRole("link", { name: "web" });
    expect(link?.getAttribute("href")).toBe("/resources/web");
    expect(screen.getByText("ci")).toBeTruthy();
    expect(screen.getByText(s.audit.outcomes["error"]!)).toBeTruthy();
    expect(Object.fromEntries(urls[0]!.searchParams)).toEqual({ limit: "100" });

    const row = link!.closest("tr")!;
    await userEvent.click(within(row).getByText(s.audit.showDetails));
    expect(within(row).getByText(/"actionId": "act-1"/)).toBeTruthy();
  });

  it("sends every filter to the server and keeps them in the next page request", async () => {
    const urls = setup();
    await screen.findAllByRole("link", { name: "web" });
    await userEvent.type(screen.getByLabelText(s.audit.actor), "admin");
    await userEvent.type(screen.getByLabelText(s.audit.action), "service");
    await userEvent.type(screen.getByLabelText(s.audit.target), "web");
    await userEvent.type(screen.getByLabelText(s.audit.from), "2026-11-01T00:00");
    await userEvent.type(screen.getByLabelText(s.audit.to), "2026-11-02T00:00");
    await userEvent.click(screen.getByRole("button", { name: s.common.confirm }));
    await waitFor(() => expect(urls.at(-1)?.searchParams.get("actor")).toBe("admin"));
    const filtered = Object.fromEntries(urls.at(-1)!.searchParams);
    expect(filtered).toMatchObject({ actor: "admin", action: "service", target: "web" });
    expect(Date.parse(filtered["from"]!)).toBe(new Date("2026-11-01T00:00").getTime());
    expect(Date.parse(filtered["to"]!)).toBe(new Date("2026-11-02T00:00").getTime());

    await userEvent.click(await screen.findByRole("button", { name: s.common.more }));
    await waitFor(() => expect(urls.at(-1)?.searchParams.get("before")).toBe("9"));
    expect(Object.fromEntries(urls.at(-1)!.searchParams)).toMatchObject({ before: "9", actor: "admin", action: "service", target: "web" });
    expect(await screen.findByText("token.create")).toBeTruthy();
    // The last page has no cursor, and a target such as token:3 is not a resource.
    expect(screen.queryByRole("button", { name: s.common.more })).toBeNull();
    expect(screen.queryByRole("link", { name: "token:3" })).toBeNull();
  });

  it("refuses a reversed time range without asking the server", async () => {
    const urls = setup();
    await screen.findAllByRole("link", { name: "web" });
    const before = urls.length;
    await userEvent.type(screen.getByLabelText(s.audit.from), "2026-11-02T00:00");
    await userEvent.type(screen.getByLabelText(s.audit.to), "2026-11-01T00:00");
    await userEvent.click(screen.getByRole("button", { name: s.common.confirm }));
    expect((await screen.findByRole("alert")).textContent).toBe(s.audit.invalidRange);
    expect(urls).toHaveLength(before);
  });

  it("starts from the filters in the address and clears them", async () => {
    const urls = setup("/audit?actor=ci&action=service.deploy");
    await screen.findAllByRole("link", { name: "web" });
    expect(Object.fromEntries(urls[0]!.searchParams)).toMatchObject({ actor: "ci", action: "service.deploy" });
    expect((screen.getByLabelText(s.audit.actor) as HTMLInputElement).value).toBe("ci");
    await userEvent.click(screen.getByRole("button", { name: s.audit.clear }));
    await waitFor(() => expect(urls.at(-1)?.searchParams.has("actor")).toBe(false));
    expect((screen.getByLabelText(s.audit.actor) as HTMLInputElement).value).toBe("");
  });
});
