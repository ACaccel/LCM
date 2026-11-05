import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { ResourceSummary, Template } from "../api/types";
import { s as strings } from "../strings";
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
  relations: [],
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

describe("TemplateDialog preview and deployment", () => {
  interface Options {
    resource?: string;
    policies?: string[];
    templatesResponse?: () => Response;
    instantiate?: (body: { dryRun: boolean }) => Response;
  }

  function Where() {
    return <output aria-label="location">{useLocation().pathname}</output>;
  }

  function mount(opts: Options = {}) {
    const sent: { name: string; dryRun: boolean; params: Record<string, unknown> }[] = [];
    const closed = vi.fn();
    vi.stubGlobal("fetch", async (req: Request) => {
      const url = new URL(req.url);
      switch (url.pathname) {
        case "/api/v1/templates":
          return opts.templatesResponse?.() ?? jsonResponse({ items: [template] });
        case "/api/v1/resources":
          return jsonResponse({ items: [summaryFixture("web"), summaryFixture("api")], nextCursor: null });
        case "/api/v1/policies":
          return jsonResponse({ items: (opts.policies ?? []).map((name) => ({ name })), nextCursor: null });
        case "/api/v1/resources/web": {
          const doc = resourceFixture().document;
          return jsonResponse(resourceFixture({ document: { ...doc, spec: { ...doc.spec, config: { source: { url: "https://github.com/acme/web.git", branch: "main" } } } } }));
        }
        case "/api/v1/templates/auto-deploy/instantiate": {
          const body = (await req.json()) as { name: string; dryRun: boolean; params: Record<string, unknown> };
          sent.push(body);
          return opts.instantiate?.(body) ?? jsonResponse({ template: "auto-deploy", name: body.name, yaml: "kind: Policy\n# preview\n", version: body.dryRun ? null : 1, deployed: !body.dryRun, warnings: [] });
        }
      }
      return jsonResponse({ error: { code: "not_found", message: `unexpected ${url.pathname}`, details: {} } }, 404);
    });
    render(
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <MemoryRouter initialEntries={["/resources/web"]}>
          <Routes>
            <Route path="*" element={<TemplateDialog resource={opts.resource} onClose={closed} />} />
          </Routes>
          <Where />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    return { sent, closed };
  }

  const button = (name: string) => screen.getByRole("button", { name }) as HTMLButtonElement;
  async function fillAndPreview(user: ReturnType<typeof userEvent.setup>) {
    await user.selectOptions(await screen.findByLabelText(/服務/), "web");
    await waitFor(() => expect((screen.getByLabelText(/Git repo/) as HTMLInputElement).value).toBe("https://github.com/acme/web.git"));
    await user.click(button(strings.templates.preview));
    await screen.findByText(strings.templates.previewTitle);
  }

  it("keeps preview and deploy disabled until the required fields are filled", async () => {
    mount();
    expect(await screen.findByRole("option", { name: "自動部署" })).toBeTruthy();
    expect(button(strings.templates.preview).disabled).toBe(true);
    expect(button(strings.templates.deploy).disabled).toBe(true);
  });

  it("presets the target service when opened from a resource", async () => {
    mount({ resource: "web" });
    const service = (await screen.findByLabelText(/服務/)) as HTMLSelectElement;
    expect(service.value).toBe("web");
    expect((screen.getByRole("textbox", { name: new RegExp(strings.templates.policyName) }) as HTMLInputElement).value).toBe("web-auto-deploy");
  });

  it("enables deploying only for the previewed parameters and asks for a new preview after a change", async () => {
    const { sent } = mount();
    const user = userEvent.setup();
    await fillAndPreview(user);
    expect(sent.map((x) => x.dryRun)).toEqual([true]);
    expect(screen.getByText(/# preview/)).toBeTruthy();
    expect(button(strings.templates.deploy).disabled).toBe(false);
    await user.type(screen.getByLabelText(/分支/), "x");
    expect(screen.getByText(strings.templates.previewStale)).toBeTruthy();
    expect(button(strings.templates.deploy).disabled).toBe(true);
    await user.click(button(strings.templates.preview));
    await waitFor(() => expect(screen.queryByText(strings.templates.previewStale)).toBeNull());
    expect(button(strings.templates.deploy).disabled).toBe(false);
  });

  it("deploys for real, closes the dialog and opens the new policy", async () => {
    const { sent, closed } = mount();
    const user = userEvent.setup();
    await fillAndPreview(user);
    await user.click(button(strings.templates.deploy));
    await waitFor(() => expect(sent.map((x) => x.dryRun)).toEqual([true, false]));
    expect(sent[1]?.name).toBe("web-auto-deploy");
    expect(sent[1]?.params).toEqual(sent[0]?.params);
    await waitFor(() => expect(closed).toHaveBeenCalled());
    expect(screen.getByLabelText("location").textContent).toBe("/policies/web-auto-deploy");
  });

  it("warns about a policy name that is taken and blocks the preview", async () => {
    mount({ policies: ["web-auto-deploy"] });
    const user = userEvent.setup();
    await user.selectOptions(await screen.findByLabelText(/服務/), "web");
    await waitFor(() => expect(screen.getByText(strings.templates.exists)).toBeTruthy());
    expect(button(strings.templates.preview).disabled).toBe(true);
    const name = screen.getByRole("textbox", { name: new RegExp(strings.templates.policyName) });
    await user.clear(name);
    await user.type(name, "web-deploy-2");
    await waitFor(() => expect(button(strings.templates.preview).disabled).toBe(false));
  });

  it("shows the server's findings and compiler warnings without enabling deployment", async () => {
    mount({
      instantiate: (body) =>
        body.dryRun
          ? jsonResponse({ template: "auto-deploy", name: "web-auto-deploy", yaml: "kind: Policy\n", version: null, deployed: false, warnings: [{ path: "spec", message: "node unreachable" }] })
          : jsonResponse({ error: { code: "validation_failed", message: "Parameters are invalid", details: {} } }, 422),
    });
    const user = userEvent.setup();
    await fillAndPreview(user);
    expect(screen.getByText(/node unreachable/)).toBeTruthy();
    await user.click(button(strings.templates.deploy));
    expect((await screen.findByRole("alert")).textContent).toContain("Parameters are invalid");
  });

  it("reports a failed preview and keeps the form", async () => {
    mount({ instantiate: () => jsonResponse({ error: { code: "validation_failed", message: "Unknown repository", details: {} } }, 422) });
    const user = userEvent.setup();
    await user.selectOptions(await screen.findByLabelText(/服務/), "web");
    await waitFor(() => expect((screen.getByLabelText(/Git repo/) as HTMLInputElement).value).not.toBe(""));
    await user.click(button(strings.templates.preview));
    expect((await screen.findByRole("alert")).textContent).toContain("Unknown repository");
    expect(button(strings.templates.deploy).disabled).toBe(true);
    expect(screen.getByLabelText(/Git repo/)).toBeTruthy();
  });

  it("reports that the templates could not be loaded and loads again on retry", async () => {
    let failing = true;
    mount({ templatesResponse: () => (failing ? jsonResponse({ error: { code: "unavailable", message: "Templates unavailable", details: {} } }, 503) : jsonResponse({ items: [template] })) });
    expect((await screen.findByRole("alert")).textContent).toContain("Templates unavailable");
    failing = false;
    await userEvent.click(screen.getByRole("button", { name: strings.common.retry }));
    expect(await screen.findByRole("option", { name: "自動部署" })).toBeTruthy();
  });

  it("keeps the dialog open on a backdrop click once the form has been edited", async () => {
    const { closed } = mount();
    const user = userEvent.setup();
    await user.selectOptions(await screen.findByLabelText(/服務/), "web");
    const backdrop = screen.getByRole("dialog").parentElement!;
    await user.pointer({ target: backdrop, keys: "[MouseLeft]" });
    expect(closed).not.toHaveBeenCalled();
    await user.click(button(strings.common.cancel));
    expect(closed).toHaveBeenCalledTimes(1);
  });
});
