import { useSearchParams } from "react-router";
import { nextSort, parseSort } from "../lib/list";
import type { SortState } from "../lib/list";

/** Column sort kept in the `sort` and `dir` URL parameters, so a sorted list can be linked and reloaded. */
export function useSort<K extends string>(keys: readonly K[], initial: SortState<K>) {
  const [params, setParams] = useSearchParams();
  const sort = parseSort(params, keys, initial);
  function toggle(key: K) {
    const n = nextSort(sort, key);
    const next = new URLSearchParams(params);
    next.set("sort", n.key);
    next.set("dir", n.dir);
    setParams(next, { replace: true });
  }
  return { sort, toggle };
}

/** A table header cell that sorts by its column; the order is announced through aria-sort. */
export function SortTh<K extends string>({ label, sortKey, sort, onSort, num }: { label: string; sortKey: K; sort: SortState<K>; onSort: (key: K) => void; num?: boolean }) {
  const active = sort.key === sortKey;
  return (
    <th className={num ? "num" : undefined} aria-sort={active ? (sort.dir === "asc" ? "ascending" : "descending") : "none"}>
      <button type="button" className="sort" onClick={() => onSort(sortKey)}>
        {label}
        <span aria-hidden="true">{active ? (sort.dir === "asc" ? " ▲" : " ▼") : ""}</span>
      </button>
    </th>
  );
}
