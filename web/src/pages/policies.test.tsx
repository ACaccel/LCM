import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { PolicySummary } from "../api/types";
import { s } from "../strings";
import { PoliciesPage } from "./policies";
import { jsonResponse } from "./resource/test-fixtures";

const policy = (name: string, over: Partial<PolicySummary> = {}): PolicySummary => ({
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
  ...over,
});

const names = () =>
  screen
    .getAllByRole("row")
    .slice(1)
    .map((r) => within(r).getAllByRole("cell")[0]?.textContent);

afterEach(() => vi.unstubAllGlobals());

describe("policy list", () => {
  it("searches names ignoring case and sorts by state", async () => {
    vi.stubGlobal("fetch", async () =>
      jsonResponse({
        items: [policy("Backup-Nightly", { paused: true }), policy("deploy-web"), policy("restart-db", { deployed: false, deployedVersion: null })],
        nextCursor: null,
      }),
    );
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter initialEntries={["/policies"]}>
          <PoliciesPage />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    await screen.findByRole("link", { name: "deploy-web" });
    expect(names()).toEqual(["Backup-Nightly", "deploy-web", "restart-db"]);

    await userEvent.type(screen.getByLabelText(s.policies.search), "NIGHT");
    expect(names()).toEqual(["Backup-Nightly"]);
    await userEvent.type(screen.getByLabelText(s.policies.search), "x");
    expect(screen.getByText(s.policies.noMatch)).toBeTruthy();
    await userEvent.clear(screen.getByLabelText(s.policies.search));

    await userEvent.click(within(screen.getByRole("columnheader", { name: s.policies.columns.state })).getByRole("button"));
    expect(names()).toEqual(["deploy-web", "Backup-Nightly", "restart-db"]);
  });
});
