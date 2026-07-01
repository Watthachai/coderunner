// Minimal line-oriented diff for the skill version viewer. Computes an LCS of
// the two line sequences, then walks both to emit removed / added / context
// lines. Kept dependency-free and readable — this is display-only, not a patch
// format.

export type DiffKind = "add" | "del" | "ctx";

export interface DiffLine {
  kind: DiffKind;
  text: string;
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
}

/**
 * fileChanges compares two file maps by key: paths present only in `next` are
 * added, paths present only in `prev` are removed. Content changes are not
 * reported here (the diff view focuses on SKILL.md body).
 */
export function fileChanges(
  prev: Record<string, string>,
  next: Record<string, string>,
): FileChanges {
  const added: string[] = [];
  const removed: string[] = [];
  for (const path of Object.keys(next)) {
    if (!Object.prototype.hasOwnProperty.call(prev, path)) added.push(path);
  }
  for (const path of Object.keys(prev)) {
    if (!Object.prototype.hasOwnProperty.call(next, path)) removed.push(path);
  }
  added.sort();
  removed.sort();
  return { added, removed };
}
