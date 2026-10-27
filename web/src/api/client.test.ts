import { afterEach, describe, expect, it, vi } from "vitest";
import { ApiError, client, errorFrom, fetchAll, unwrap } from "./client";

function stubFetch(handler: (req: Request) => Response | Promise<Response>) {
  const calls: Request[] = [];
  vi.stubGlobal("fetch", async (req: Request) => {
    calls.push(req);
    return handler(req);
  });
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

const json = (status: number, body: unknown) => new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

describe("path parameters", () => {
  it("percent-encodes slashes in lock names and module-scoped node ids", async () => {
    const calls = stubFetch(() => new Response(null, { status: 204 }));
    await unwrap(client.DELETE("/locks/{name}", { params: { path: { name: "service:web/deploy" } } }));
    await unwrap(client.POST("/runs/{id}/nodes/{node}/resume", { params: { path: { id: "r1", node: "outer/inner/check" } }, body: { key: "k" } }).catch(() => undefined) as Promise<never>);
    expect(new URL(calls[0]?.url ?? "").pathname).toBe("/api/v1/locks/service%3Aweb%2Fdeploy");
    expect(new URL(calls[1]?.url ?? "").pathname).toBe("/api/v1/runs/r1/nodes/outer%2Finner%2Fcheck/resume");
  });
});

describe("error handling", () => {
  it("turns the error envelope into an ApiError with code, message and issues", async () => {
    stubFetch(() =>
      json(422, { error: { code: "validation_failed", message: "invalid document", details: { issues: [{ path: "spec.nodes[0].id", message: "required" }] } } }),
    );
    const err = await unwrap(client.POST("/validate", { body: {} })).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const e = err as ApiError;
    expect([e.status, e.code, e.message]).toEqual([422, "validation_failed", "invalid document"]);
    expect(e.issues).toEqual([{ path: "spec.nodes[0].id", message: "required" }]);
  });

  it("exposes the stored version of a version conflict", () => {
    const e = errorFrom(409, { error: { code: "version_conflict", message: "stale", details: { currentVersion: 7 } } });
    expect(e.isConflict).toBe(true);
    expect(e.currentVersion).toBe(7);
    expect(errorFrom(409, { error: { code: "lock_held", message: "busy", details: {} } }).isConflict).toBe(false);
  });

  it("reports network failures and non-envelope bodies instead of swallowing them", async () => {
    vi.stubGlobal("fetch", async () => {
      throw new TypeError("Failed to fetch");
    });
    const net = (await unwrap(client.GET("/hosts")).catch((e: unknown) => e)) as ApiError;
    expect([net.status, net.code, net.message]).toEqual([0, "network", "Failed to fetch"]);
    expect(errorFrom(502, "bad gateway").message).toBe("bad gateway");
    expect(errorFrom(500, undefined).message).toBe("HTTP 500");
  });

  it("returns undefined data for 204 answers", async () => {
    stubFetch(() => new Response(null, { status: 204 }));
    await expect(unwrap(client.DELETE("/tokens/{id}", { params: { path: { id: 3 } } }))).resolves.toBeUndefined();
  });
});

describe("fetchAll", () => {
  it("follows cursors until the last page", async () => {
    const pages = [{ items: [1, 2], nextCursor: "a" }, { items: [3], nextCursor: "b" }, { items: [4], nextCursor: null }];
    const seen: (string | undefined)[] = [];
    const out = await fetchAll(async (cursor) => {
      seen.push(cursor);
      return pages[seen.length - 1] as (typeof pages)[number];
    });
    expect(out).toEqual([1, 2, 3, 4]);
    expect(seen).toEqual([undefined, "a", "b"]);
  });
});
