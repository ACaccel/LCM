import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ResourceSummary, Template } from "../api/types";
import { jsonResponse, resourceFixture } from "./resource/test-fixtures";
import { TemplateDialog } from "./template-dialog";

const summaryFixture = (name: string): ResourceSummary => ({
  id: `id-${name}`,
  name,
  type: "service",
  adapter: "systemd",
  phase: "managed",
  labels: {},
  state: "running",
  stateSince: null,
  health: "healthy",
  version: 1,
  updatedAt: "2026-11-01T00:00:00Z",
  deployedVersion: "",
  managedBy: null,
});

const template: Template = {
  id: "auto-deploy",
  title: "Auto deploy",
  description: "Deploys every new commit.",
  resourceParam: "resource",
  params: [
    { name: "resource", type: "service", description: "", required: true },
    { name: "repo", type: "string", description: "", required: true, prefill: "source.url" },
    { name: "branch", type: "string", description: "", required: false, default: "main", prefill: "source.branch" },
    { name: "windows", type: "windows", description: "", required: false, default: [{ start: "00:00", end: "24:00" }] },
    { name: "notifyTarget", type: "string", description: "", required: false, default: "discord", enum: ["discord", "konata"] },
    { name: "notifySecret", type: "secret", description: "", required: false, default: "", visibleIf: { param: "notifyTarget", equals: "discord" } },
    { name: "notifyUrl", type: "string", description: "", required: false, default: "", visibleIf: { param: "notifyTarget", equals: "konata" } },
    { name: "notifyTokenSecret", type: "secret", description: "", required: false, default: "", visibleIf: { param: "notifyTarget", equals: "konata" } },
  ],
};

function setup() {
  const sent: unknown[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    const url = new URL(req.url);
    switch (url.pathname) {
      case "/api/v1/templates":
        return jsonResponse({ items: [template] });
      case "/api/v1/resources":
        return jsonResponse({ items: [summaryFixture("web"), summaryFixture("api")], nextCursor: null });
      case "/api/v1/policies":
        return jsonResponse({ items: [], nextCursor: null });
      case "/api/v1/resources/web": {
        const doc = resourceFixture().document;
        return jsonResponse(resourceFixture({ document: { ...doc, spec: { ...doc.spec, config: { unit: "web.service", source: { url: "https://github.com/acme/web.git", branch: "release" } } } } }));
      }
      case "/api/v1/resources/api":
        return jsonResponse(resourceFixture({ name: "api" }));
      case "/api/v1/templates/auto-deploy/instantiate": {
        const body = (await req.json()) as { name: string; params: unknown; dryRun: boolean };
        sent.push(body);
        return jsonResponse({ template: "auto-deploy", name: body.name, yaml: "kind: Policy\n", version: null, deployed: false, warnings: [] });
      }
    }
    return jsonResponse({ error: { code: "not_found", message: `unexpected ${url.pathname}`, details: {} } }, 404);
  });
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <TemplateDialog onClose={() => {}} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return sent;
}

afterEach(() => vi.unstubAllGlobals());

describe("TemplateDialog", () => {
  it("builds the form from the descriptor: enum select, window rows and prefill", async () => {
    const sent = setup();
    const user = userEvent.setup();

    // Titles and enumerated values are translated; the submit button says what it does.
    expect(await screen.findByRole("option", { name: "自動部署" })).toBeTruthy();
    const target = screen.getByLabelText(/通知管道/) as HTMLSelectElement;
    expect(target.value).toBe("discord");
    expect(within(target).getByRole("option", { name: "konata 通知轉送" })).toBeTruthy();
    expect(screen.getByRole("button", { name: "建立並啟用" })).toBeTruthy();

    // The default window shows as a row; 24:00 shows as 00:00.
    const first = screen.getByRole("group", { name: "時段 1" });
    expect((within(first).getByLabelText("開始") as HTMLInputElement).value).toBe("00:00");
    expect((within(first).getByLabelText("結束") as HTMLInputElement).value).toBe("00:00");

    // Only the fields of the chosen notification target are shown.
    expect(screen.getByLabelText(/Discord webhook/)).toBeTruthy();
    expect(screen.queryByLabelText(/konata 通知路由/)).toBeNull();
    expect(screen.queryByLabelText(/konata 金鑰/)).toBeNull();

    // Choosing a service fills the repository and branch from its source, until the user edits them.
    await user.selectOptions(screen.getByLabelText(/服務/), "web");
    const branch = screen.getByLabelText(/分支/) as HTMLInputElement;
    const repo = screen.getByLabelText(/Git repo/) as HTMLInputElement;
    await waitFor(() => expect(branch.value).toBe("release"));
    expect(repo.value).toBe("https://github.com/acme/web.git");
    await user.selectOptions(screen.getByLabelText(/服務/), "api");
    await waitFor(() => expect(branch.value).toBe("main"));
    expect(repo.value).toBe("");
    await user.selectOptions(screen.getByLabelText(/服務/), "web");
    await waitFor(() => expect(branch.value).toBe("release"));
    expect(repo.value).toBe("https://github.com/acme/web.git");

    // Edit the window: Monday and Friday, 22:00 to midnight; add a second row.
    await user.click(within(first).getByLabelText("一"));
    await user.click(within(first).getByLabelText("五"));
    await user.clear(within(first).getByLabelText("開始"));
    await user.type(within(first).getByLabelText("開始"), "22:00");
    await user.click(screen.getByRole("button", { name: "新增時段" }));
    const second = screen.getByRole("group", { name: "時段 2" });
    await user.clear(within(second).getByLabelText("結束"));
    await user.type(within(second).getByLabelText("結束"), "05:00");
    // A value typed for one target is not sent once another target is chosen.
    await user.type(screen.getByLabelText(/Discord webhook/), "ops-hook");
    await user.selectOptions(target, "konata");
    expect(screen.queryByLabelText(/Discord webhook/)).toBeNull();
    await user.type(screen.getByLabelText(/konata 通知路由/), "http://127.0.0.1:4100/n");
    expect(screen.getByLabelText(/konata 金鑰/)).toBeTruthy();
    await user.clear(repo);
    await user.type(repo, "https://example.com/web.git");

    await user.click(screen.getByRole("button", { name: "預覽 YAML" }));
    await waitFor(() => expect(sent).toHaveLength(1));
    expect((sent[0] as { params: Record<string, unknown> }).params).not.toHaveProperty("notifySecret");
    expect(sent[0]).toMatchObject({
      name: "web-auto-deploy",
      dryRun: true,
      params: {
        resource: "web",
        repo: "https://example.com/web.git",
        branch: "release",
        notifyTarget: "konata",
        notifyUrl: "http://127.0.0.1:4100/n",
        windows: [
          { days: ["mon", "fri"], start: "22:00", end: "24:00" },
          { start: "00:00", end: "05:00" },
        ],
      },
    });
  });
});
