// Unified-diff parser: turns `git diff` output into structured files/hunks/
// lines for the read-only diff viewer. Pure function, unit-testable.

export interface DiffLine {
  kind: "add" | "del" | "ctx" | "hunk" | "meta";
  text: string;
  oldNo?: number;
  newNo?: number;
}

export interface DiffFile {
  oldPath: string;
  newPath: string;
  binary: boolean;
  lines: DiffLine[];
  additions: number;
  deletions: number;
}

export interface ParsedDiff {
  files: DiffFile[];
  truncated: boolean;
  additions: number;
  deletions: number;
}

const fromPrefixes = new Set(["diff --git a/", "diff --git "]);
const devNull = "/dev/null";
const nulWin = "NUL";

function cleanPath(p: string): string {
  p = p.trim();
  if (p === devNull || p === nulWin) return devNull;
  return p;
}

export function parseUnifiedDiff(patch: string): ParsedDiff {
  const files: DiffFile[] = [];
  let current: DiffFile | null = null;
  let oldNo = 0;
  let newNo = 0;
  let truncated = false;

  for (const raw of patch.split(/\r?\n/)) {
    if (raw.startsWith("diff --git ")) {
      const m = raw.match(/^diff --git a\/(.*) b\/(.*)$/);
      current = {
        oldPath: m ? cleanPath(m[1]) : raw.slice(11),
        newPath: m ? cleanPath(m[2]) : raw.slice(11),
        binary: false,
        lines: [],
        additions: 0,
        deletions: 0,
      };
      files.push(current);
      continue;
    }
    if (!current) {
      // header noise before the first file block
      continue;
    }
    if (raw.startsWith("Binary files ") || raw.startsWith("GIT binary patch")) {
      current.binary = true;
      continue;
    }
    if (raw.startsWith("index ") || raw.startsWith("old mode") || raw.startsWith("new mode") ||
        raw.startsWith("similarity index") || raw.startsWith("rename from") ||
        raw.startsWith("rename to") || raw.startsWith("deleted file mode") ||
        raw.startsWith("new file mode") || raw.startsWith("--- ") || raw.startsWith("+++ ")) {
      current.lines.push({ kind: "meta", text: raw });
      continue;
    }
    const hunk = raw.match(/^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@/);
    if (hunk) {
      oldNo = parseInt(hunk[1], 10);
      newNo = parseInt(hunk[2], 10);
      current.lines.push({ kind: "hunk", text: raw });
      continue;
    }
    if (raw.startsWith("+")) {
      current.lines.push({ kind: "add", text: raw.slice(1), newNo: newNo++ });
      current.additions++;
    } else if (raw.startsWith("-")) {
      current.lines.push({ kind: "del", text: raw.slice(1), oldNo: oldNo++ });
      current.deletions++;
    } else if (raw.startsWith(" ")) {
      current.lines.push({ kind: "ctx", text: raw.slice(1), oldNo: oldNo++, newNo: newNo++ });
    } else if (raw.startsWith("\\")) {
      current.lines.push({ kind: "meta", text: raw });
    } else if (raw === "") {
      // trailing empty line inside a file block
      current.lines.push({ kind: "ctx", text: "", oldNo: oldNo, newNo: newNo });
    }
  }

  let additions = 0;
  let deletions = 0;
  for (const f of files) {
    additions += f.additions;
    deletions += f.deletions;
  }
  // The backend truncates at a byte bound; a final unterminated hunk is
  // detected heuristically by an unparseable tail — flagged, never hidden.
  if (patch.length > 0 && !patch.endsWith("\n")) truncated = true;
  return { files, truncated, additions, deletions };
}

export function displayName(f: DiffFile): string {
  if (f.oldPath === devNull && f.newPath !== devNull) return f.newPath;
  if (f.newPath === devNull && f.oldPath !== devNull) return f.oldPath + " (deleted)";
  if (f.oldPath === f.newPath) return f.newPath;
  return `${f.oldPath} → ${f.newPath}`;
}

export const _fromPrefixes = fromPrefixes; // exported for tests
