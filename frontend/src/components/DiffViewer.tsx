// Read-only unified diff viewer over real Git output (GetTaskDiff).
import { useMemo, useState } from "react";
import { parseUnifiedDiff, displayName } from "../lib/diff";
import type { DiffView as DiffViewType } from "../lib/types";
import { Tag } from "./ui";
import { FileDiff } from "lucide-react";

export function DiffViewer({ diff }: { diff: DiffViewType }) {
  const parsed = useMemo(() => parseUnifiedDiff(diff.patch ?? ""), [diff.patch]);
  const [openFile, setOpenFile] = useState<string | null>(null);

  if (!diff.available) {
    return (
      <div className="rounded-lg border border-edge bg-raised p-4 text-sm text-mut">
        {diff.note || "No diff available."}
      </div>
    );
  }

  return (
    <div className="rounded-lg border border-edge bg-raised overflow-hidden">
      <div className="flex items-center gap-2 flex-wrap px-3.5 py-2.5 border-b border-edge">
        <FileDiff size={14} className="text-mut" aria-hidden />
        <span className="text-sm font-medium">Diff vs baseline</span>
        <Tag tone="mono">{diff.base_commit?.slice(0, 10) || "—"}</Tag>
        {parsed.files.length > 0 && <Tag>{parsed.files.length} file(s)</Tag>}
        <Tag tone="ok">+{parsed.additions}</Tag>
        <Tag tone="fail">−{parsed.deletions}</Tag>
        {parsed.truncated && <Tag tone="warn">truncated — full diff is on disk</Tag>}
      </div>

      {parsed.files.length === 0 ? (
        <div className="px-4 py-3 text-sm text-mut">
          No textual changes detected{diff.stat ? ` — stat: ${diff.stat}` : ""}.
        </div>
      ) : (
        <div className="max-h-[520px] overflow-auto">
          {parsed.files.map((f) => {
            const name = displayName(f);
            const open = openFile === null || openFile === name;
            return (
              <div key={name} className="border-b border-edge last:border-b-0">
                <button
                  className="w-full flex items-center gap-2 px-3.5 py-2 text-left hover:bg-overlay"
                  onClick={() => setOpenFile(open && openFile !== null ? null : name)}
                >
                  <span className="font-mono text-xs truncate flex-1 text-ink">{name}</span>
                  {f.binary ? (
                    <Tag>binary</Tag>
                  ) : (
                    <>
                      <Tag tone="ok">+{f.additions}</Tag>
                      <Tag tone="fail">−{f.deletions}</Tag>
                    </>
                  )}
                </button>
                {open && !f.binary && (
                  <div className="font-mono text-[12.5px] leading-[1.5] bg-inset">
                    {f.lines.map((l, i) => (
                      <div
                        key={i}
                        className={
                          l.kind === "add"
                            ? "text-ok bg-ok/8"
                            : l.kind === "del"
                              ? "text-fail bg-fail/8"
                              : l.kind === "hunk"
                                ? "text-accent-ink bg-accent-soft"
                                : "text-mut"
                        }
                      >
                        <span className="inline-block w-10 pr-1 text-right text-faint select-none border-r border-edge mr-2">
                          {l.kind === "add" ? l.newNo ?? "" : l.oldNo ?? ""}
                        </span>
                        <span className="inline-block w-10 pr-1 text-right text-faint select-none border-r border-edge mr-2">
                          {l.kind === "add" ? "" : l.newNo ?? ""}
                        </span>
                        <span className="whitespace-pre-wrap break-all">
                          {l.kind === "add" ? "+ " : l.kind === "del" ? "− " : l.kind === "hunk" ? "" : "  "}
                          {l.text}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
