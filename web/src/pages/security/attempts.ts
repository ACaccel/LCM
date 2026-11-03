/** Persist identifiers before mutations so lost responses can be queried safely. */
export interface Attempt { requestId: string; reason: string; previewHash?: string }
export function readAttempt(key: string): Attempt | null {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(key) ?? "null");
    if (!raw || typeof raw !== "object") return null;
    const value = raw as Record<string, unknown>;
    if (typeof value.requestId !== "string" || !/^[a-zA-Z0-9-]{1,100}$/.test(value.requestId) || typeof value.reason !== "string" || value.reason.length > 500) return null;
    if (value.previewHash !== undefined && (typeof value.previewHash !== "string" || value.previewHash.length > 256)) return null;
    return { requestId: value.requestId, reason: value.reason, ...(typeof value.previewHash === "string" ? { previewHash: value.previewHash } : {}) };
  } catch { return null; }
}
export function saveAttempt(key: string, value: Attempt): void { localStorage.setItem(key, JSON.stringify(value)); }
export function clearAttempt(key: string): void { localStorage.removeItem(key); }
