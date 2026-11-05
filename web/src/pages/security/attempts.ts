/** Persist identifiers before mutations so lost responses can be queried safely. */
export interface Attempt {
  requestId: string;
  reason: string;
  previewHash?: string;
}

/** The stored attempt, or null when nothing usable is stored. */
export function readAttempt(key: string): Attempt | null {
  try {
    const raw: unknown = JSON.parse(localStorage.getItem(key) ?? "null");
    if (!raw || typeof raw !== "object") return null;
    const value = raw as Record<string, unknown>;
    if (typeof value.requestId !== "string" || typeof value.reason !== "string") return null;
    return {
      requestId: value.requestId,
      reason: value.reason,
      ...(typeof value.previewHash === "string" ? { previewHash: value.previewHash } : {}),
    };
  } catch {
    return null;
  }
}

export function saveAttempt(key: string, value: Attempt): void {
  localStorage.setItem(key, JSON.stringify(value));
}

export function clearAttempt(key: string): void {
  localStorage.removeItem(key);
}
