import createClient from "openapi-fetch";
import type { paths } from "./schema";
import type { Issue } from "./types";

/** An error answer of the API, or a network failure (status 0). */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly details: Record<string, unknown>;

  constructor(status: number, code: string, message: string, details: Record<string, unknown> = {}) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.details = details;
  }

  /** Validation problems located by document path; empty for other errors. */
  get issues(): Issue[] {
    const raw = this.details["issues"];
    return Array.isArray(raw) ? (raw as Issue[]) : [];
  }

  /** The stored version reported by a 409 version_conflict. */
  get currentVersion(): number | undefined {
    const v = this.details["currentVersion"];
    return typeof v === "number" ? v : undefined;
  }

  get isConflict(): boolean {
    return this.status === 409 && this.code === "version_conflict";
  }
}

export function isApiError(e: unknown): e is ApiError {
  return e instanceof ApiError;
}

let onUnauthorized: (() => void) | undefined;

/** Registers what happens when any call answers 401 (except a failed login). */
export function setUnauthorizedHandler(fn: (() => void) | undefined): void {
  onUnauthorized = fn;
}

export const client = createClient<paths>({
  baseUrl: `${globalThis.location?.origin ?? "http://localhost"}/api/v1`,
  // Resolved on every call so that tests can substitute fetch.
  fetch: (request) => globalThis.fetch(request),
});

client.use({
  onResponse({ response, schemaPath }) {
    // Login and the session probe answer 401 as a normal result.
    if (response.status === 401 && schemaPath !== "/auth/login" && schemaPath !== "/auth/me") onUnauthorized?.();
    return undefined;
  },
});

interface Settled<T> {
  data?: T;
  error?: unknown;
  response: Response;
}

/**
 * Resolves with the response data and turns every error answer into an
 * ApiError that carries the envelope's code, message and details. Network
 * failures become ApiError with status 0.
 */
export async function unwrap<T>(call: Promise<Settled<T>>): Promise<T> {
  let res: Settled<T>;
  try {
    res = await call;
  } catch (e) {
    if (e instanceof DOMException && e.name === "AbortError") throw e;
    throw new ApiError(0, "network", e instanceof Error ? e.message : String(e));
  }
  if (res.response.ok) return res.data as T;
  throw errorFrom(res.response.status, res.error);
}

export function errorFrom(status: number, body: unknown): ApiError {
  const env = (body as { error?: { code?: string; message?: string; details?: Record<string, unknown> } } | undefined)?.error;
  if (env && typeof env.message === "string") {
    return new ApiError(status, env.code ?? "error", env.message, env.details ?? {});
  }
  const text = typeof body === "string" && body ? body : `HTTP ${status}`;
  return new ApiError(status, "error", text);
}

/** Options for sending a YAML text where the API accepts a document. */
export const yamlBody = {
  headers: { "Content-Type": "application/yaml" },
  bodySerializer: (b: unknown) => b as string,
} as const;

/** Collects every page of a cursor-paged list. */
export async function fetchAll<T>(page: (cursor: string | undefined) => Promise<{ items: T[]; nextCursor: string | null }>): Promise<T[]> {
  const out: T[] = [];
  let cursor: string | undefined;
  // The loop is bounded so a misbehaving server cannot hang the page.
  for (let i = 0; i < 50; i++) {
    const p = await page(cursor);
    out.push(...p.items);
    if (!p.nextCursor) break;
    cursor = p.nextCursor;
  }
  return out;
}
