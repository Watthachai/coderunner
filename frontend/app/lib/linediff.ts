// Minimal line-oriented diff for the skill version viewer. Computes an LCS of
// the two line sequences, then walks both to emit removed / added / context
// lines. Kept dependency-free and readable — this is display-only, not a patch
// format.

export type DiffKind = "add" | "del" | "ctx" | "gap";

export interface DiffLine {
  kind: DiffKind;
  text: string;
}

/**
 * collapse hides long runs of unchanged context so the diff reads like a git
 * hunk view: it keeps `context` lines around every change and replaces the rest
 * with a single "gap" marker line. Returns [] when there is nothing changed.
 */
export function collapse(lines: DiffLine[], context = 3): DiffLine[] {
  const changed: number[] = [];
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].kind !== "ctx") changed.push(i);
  }
  if (changed.length === 0) return [];
  const keep = new Set<number>();
  for (const idx of changed) {
    const lo = Math.max(0, idx - context);
    const hi = Math.min(lines.length - 1, idx + context);
    for (let j = lo; j <= hi; j++) keep.add(j);
  }
  const out: DiffLine[] = [];
  let last = -1;
  for (let i = 0; i < lines.length; i++) {
    if (!keep.has(i)) continue;
    if (last >= 0 && i - last > 1) {
      out.push({ kind: "gap", text: `⋯ ${i - last - 1} unchanged` });
    }
    out.push(lines[i]);
    last = i;
  }
  return out;
}

function splitLines(text: string): string[] {
  // Normalize CRLF, then split. A trailing newline yields a trailing "" which
  // we drop so an N-line file is N lines, not N+1.
  const norm = text.replace(/\r\n/g, "\n");
  const lines = norm.split("\n");
  if (lines.length > 0 && lines[lines.length - 1] === "") lines.pop();
  return lines;
}

// Standard LCS length table over line arrays.
function lcsTable(a: string[], b: string[]): number[][] {
  const m = a.length;
  const n = b.length;
  const dp: number[][] = Array.from({ length: m + 1 }, () =>
    new Array<number>(n + 1).fill(0),
  );
  for (let i = m - 1; i >= 0; i--) {
    for (let j = n - 1; j >= 0; j--) {
      dp[i][j] =
        a[i] === b[j]
          ? dp[i + 1][j + 1] + 1
          : Math.max(dp[i + 1][j], dp[i][j + 1]);
    }
  }
  return dp;
}

/**
 * lineDiff(oldText, newText) returns an ordered list of lines: removed lines
 * ("del", only in old), added lines ("add", only in new), and unchanged
 * context lines ("ctx"). Removals for a hunk precede additions.
 */
export function lineDiff(oldText: string, newText: string): DiffLine[] {
  const a = splitLines(oldText);
  const b = splitLines(newText);
  const dp = lcsTable(a, b);
  const out: DiffLine[] = [];
  let i = 0;
  let j = 0;
  while (i < a.length && j < b.length) {
    if (a[i] === b[j]) {
      out.push({ kind: "ctx", text: a[i] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push({ kind: "del", text: a[i] });
      i++;
    } else {
      out.push({ kind: "add", text: b[j] });
      j++;
    }
  }
  while (i < a.length) {
    out.push({ kind: "del", text: a[i] });
    i++;
  }
  while (j < b.length) {
    out.push({ kind: "add", text: b[j] });
    j++;
  }
  return out;
}

export interface FileChanges {
  added: string[];
  removed: string[];
  modified: string[]; // same path, different content
}

/**
 * fileChanges compares two file maps: paths only in `next` are added, paths only
 * in `prev` are removed, and paths in both with different content are modified
 * (the caller can line-diff those). This is what surfaces a change to a reference
 * file (e.g. prisma-setup.md) even when SKILL.md itself is unchanged.
 */
export function fileChanges(
  prev: Record<string, string>,
  next: Record<string, string>,
): FileChanges {
  const added: string[] = [];
  const removed: string[] = [];
  const modified: string[] = [];
  for (const path of Object.keys(next)) {
    if (!Object.prototype.hasOwnProperty.call(prev, path)) {
      added.push(path);
    } else if (prev[path] !== next[path]) {
      modified.push(path);
    }
  }
  for (const path of Object.keys(prev)) {
    if (!Object.prototype.hasOwnProperty.call(next, path)) removed.push(path);
  }
  added.sort();
  removed.sort();
  modified.sort();
  return { added, removed, modified };
}
