export type DiffKind = "eq" | "add" | "del" | "sep";

export interface DiffLine {
  kind: DiffKind;
  text: string;
}

function splitLines(text: string): string[] {
  if (text === "") return [];
  const lines = text.split("\n");
  // A trailing newline does not make an extra empty line.
  if (lines[lines.length - 1] === "") lines.pop();
  return lines;
}

/**
 * Line diff of two texts (shortest edit script via LCS). Common leading and
 * trailing lines are cut off first, which keeps the quadratic core small for
 * the usual case of a few edited lines in a long document.
 */
export function diffLines(before: string, after: string): DiffLine[] {
  const a = splitLines(before);
  const b = splitLines(after);
  let start = 0;
  while (start < a.length && start < b.length && a[start] === b[start]) start++;
  let endA = a.length;
  let endB = b.length;
  while (endA > start && endB > start && a[endA - 1] === b[endB - 1]) {
    endA--;
    endB--;
  }

  const out: DiffLine[] = a.slice(0, start).map((text) => ({ kind: "eq", text }));
  const midA = a.slice(start, endA);
  const midB = b.slice(start, endB);
  const n = midA.length;
  const m = midB.length;

  // lcs[i][j]: length of the LCS of midA[i:] and midB[j:].
  const lcs: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      const row = lcs[i] as Uint32Array;
      const below = lcs[i + 1] as Uint32Array;
      row[j] = midA[i] === midB[j] ? (below[j + 1] ?? 0) + 1 : Math.max(below[j] ?? 0, row[j + 1] ?? 0);
    }
  }
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (midA[i] === midB[j]) {
      out.push({ kind: "eq", text: midA[i] as string });
      i++;
      j++;
    } else if (((lcs[i + 1] as Uint32Array)[j] ?? 0) >= ((lcs[i] as Uint32Array)[j + 1] ?? 0)) {
      out.push({ kind: "del", text: midA[i++] as string });
    } else {
      out.push({ kind: "add", text: midB[j++] as string });
    }
  }
  while (i < n) out.push({ kind: "del", text: midA[i++] as string });
  while (j < m) out.push({ kind: "add", text: midB[j++] as string });

  for (const text of a.slice(endA)) out.push({ kind: "eq", text });
  return out;
}

export function hasChanges(lines: readonly DiffLine[]): boolean {
  return lines.some((l) => l.kind === "add" || l.kind === "del");
}

/** Keeps changed lines with `context` lines around them; gaps become separators. */
export function collapseContext(lines: readonly DiffLine[], context = 3): DiffLine[] {
  const keep = new Array<boolean>(lines.length).fill(false);
  lines.forEach((l, i) => {
    if (l.kind === "eq") return;
    for (let k = Math.max(0, i - context); k <= Math.min(lines.length - 1, i + context); k++) keep[k] = true;
  });
  const out: DiffLine[] = [];
  let skipped = false;
  lines.forEach((l, i) => {
    if (keep[i]) {
      if (skipped && out.length > 0) out.push({ kind: "sep", text: "…" });
      skipped = false;
      out.push(l);
    } else {
      skipped = true;
    }
  });
  return out;
}
