import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen } from "@testing-library/react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { sec } from "../strings";
import { SecurityPage } from "./security";

function Where() {
  const loc = useLocation();
  return <output aria-label="location">{loc.pathname + loc.search}</output>;
}

function setup(initial: string) {
  // Every read answers with an error: the tab wiring is under test, not the panels.
  vi.stubGlobal("fetch", async () => new Response(JSON.stringify({ error: { code: "unavailable", message: "stub", details: {} } }), { status: 503 }));
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={[initial]}>
        <Routes>
          <Route path="/security/:tab?" element={<SecurityPage />} />
        </Routes>
        <Where />
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const location = () => screen.getByLabelText("location").textContent;

describe("security tabs", () => {
  it("offers incidents, traffic, host control and notifications", () => {
    setup("/security/incidents");
    const tabs = screen.getAllByRole("link").map((a) => a.textContent);
    expect(tabs).toEqual([sec.tabIncidents, sec.traffic, sec.tabHost, sec.tabNotifications]);
    expect(screen.getByRole("link", { name: sec.tabIncidents }).className).toBe("active");
  });

  it.each([
    ["/security", "/security/incidents"],
    ["/security/overview", "/security/incidents"],
    ["/security/rules", "/security/host"],
    ["/security/checks", "/security/host"],
  ])("sends the old address %s to %s and keeps its filters", (from, to) => {
    setup(`${from}?machineId=machine-1`);
    expect(location()).toBe(`${to}?machineId=machine-1`);
  });

  it("keeps the new addresses where they are", () => {
    for (const tab of ["incidents", "traffic", "host", "notifications"]) {
      cleanup();
      setup(`/security/${tab}`);
      expect(location()).toBe(`/security/${tab}`);
    }
  });

  it("moves the notification settings out of host control", () => {
    setup("/security/notifications");
    expect(screen.getByRole("heading", { name: sec.notifications })).toBeTruthy();
    cleanup();
    setup("/security/host");
    expect(screen.queryByRole("heading", { name: sec.notifications })).toBeNull();
    expect(screen.getByRole("heading", { name: sec.hostScope })).toBeTruthy();
  });

  it("names an unknown tab instead of showing another one", () => {
    setup("/security/nope");
    expect(screen.getByText(sec.unknown)).toBeTruthy();
    expect(location()).toBe("/security/nope");
  });
});
