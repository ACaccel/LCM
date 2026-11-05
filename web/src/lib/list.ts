export type SortDir = "asc" | "desc";

export interface SortState<K extends string> {
  key: K;
  dir: SortDir;
}

/** A sortable value of a row; null and undefined sort last in either direction. */
export type SortValue = string | number | null | undefined;

const collator = new Intl.Collator("zh-TW", { numeric: true, sensitivity: "base" });

function compare(a: SortValue, b: SortValue): number {
  if (typeof a === "number" && typeof b === "number") return a - b;
  return collator.compare(String(a), String(b));
}

/** A sorted copy of `rows`; the sort is stable, so equal rows keep the server's order. */
export function sortRows<T, K extends string>(rows: readonly T[], sort: SortState<K>, by: Readonly<Record<K, (row: T) => SortValue>>): T[] {
  const value = by[sort.key];
  const sign = sort.dir === "asc" ? 1 : -1;
  return rows
    .map((row, index) => ({ row, index, v: value(row) }))
    .sort((x, y) => {
      const xMissing = x.v === null || x.v === undefined;
      const yMissing = y.v === null || y.v === undefined;
      if (xMissing || yMissing) return xMissing === yMissing ? x.index - y.index : xMissing ? 1 : -1;
      return sign * compare(x.v, y.v) || x.index - y.index;
    })
    .map((x) => x.row);
}

/** Reads a sort from URL parameters, falling back to `initial` for unknown keys. */
export function parseSort<K extends string>(params: URLSearchParams, keys: readonly K[], initial: SortState<K>): SortState<K> {
  const key = params.get("sort");
  const dir = params.get("dir");
  if (!key || !(keys as readonly string[]).includes(key)) return initial;
  return { key: key as K, dir: dir === "desc" ? "desc" : "asc" };
}

/** Clicking the sorted column flips its direction; another column starts ascending. */
export function nextSort<K extends string>(current: SortState<K>, key: K): SortState<K> {
  return current.key === key ? { key, dir: current.dir === "asc" ? "desc" : "asc" } : { key, dir: "asc" };
}

/** Whether a name contains the search text, ignoring case and surrounding spaces. */
export function nameMatches(name: string, search: string): boolean {
  return name.toLowerCase().includes(search.trim().toLowerCase());
}
