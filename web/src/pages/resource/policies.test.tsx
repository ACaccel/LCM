import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { PolicySummary, ResourceDetail } from "../../api/types";
import { actionLabel } from "../../lib/actions";
import { s } from "../../strings";
import { PoliciesTab } from "./policies";
import { freshMachine, jsonResponse, resourceFixture } from "./test-fixtures";

const policy = (name: string, relation: PolicySummary["relation"], over: Partial<PolicySummary> = {}): PolicySummary => ({
  name,
  labels: {},
  latestVersion: 1,
  deployedVersion: 1,
  deployed: true,
  paused: false,
  deployedAt: "2026-11-01T00:00:00Z",
  updatedAt: "2026-11-01T00:00:00Z",
  triggers: [],
  targets: [],
  lastRun: null,
  managedBy: null,
  relation,
  ...over,
});

function mount(detail: ResourceDetail, items: PolicySummary[]) {
  const urls: URL[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    if (url.pathname === "/api/v1/policies") {
      urls.push(url);
      return jsonResponse({ items, nextCursor: null });
    }
    return jsonResponse({ items: [], nextCursor: null });
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <PoliciesTab detail={detail} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return urls;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("resource policies tab", () => {
  it("asks the server for the policies of this resource and shows how each concerns it", async () => {
    const urls = mount(resourceFixture({ name: "web" }), [
      policy("web-restart", { kind: "acts", types: ["lifecycle.restart", "notify.log"] }),
      policy("watch-web", { kind: "watches", types: [] }),
      policy("pick-at-run", { kind: "dynamic", types: [] }, { paused: true }),
    ]);
    const rows = (await screen.findAllByRole("row")).slice(1);
    expect(urls[0]?.searchParams.get("resource")).toBe("web");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]!).getByRole("link", { name: "web-restart" }).getAttribute("href")).toBe("/policies/web-restart");
    expect(within(rows[0]!).getByText(s.resourcePolicies.relation.acts(`${actionLabel("restart")}、notify.log`))).toBeTruthy();
    expect(within(rows[1]!).getByText(s.resourcePolicies.relation.watches)).toBeTruthy();
    expect(within(rows[2]!).getByText(s.resourcePolicies.relation.dynamic)).toBeTruthy();
    expect(within(rows[2]!).getByText(s.policies.paused)).toBeTruthy();
  });

  it("says so when no policy concerns the resource", async () => {
    mount(resourceFixture({ name: "web" }), []);
    expect(await screen.findByText(s.resourcePolicies.empty)).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("offers a template only for a managed service", async () => {
    mount(resourceFixture({ name: "web", phase: "managed" }), []);
    expect(await screen.findByRole("button", { name: s.resourcePolicies.applyTemplate })).toBeTruthy();
    cleanup();
    mount(resourceFixture({ name: "web", phase: "retired" }), []);
    await screen.findByText(s.resourcePolicies.empty);
    expect(screen.queryByRole("button", { name: s.resourcePolicies.applyTemplate })).toBeNull();
    cleanup();
    mount(freshMachine(), []);
    await screen.findByText(s.resourcePolicies.empty);
    expect(screen.queryByRole("button", { name: s.resourcePolicies.applyTemplate })).toBeNull();
  });
});
