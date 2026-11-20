// Streaming read-only log viewer.
//
// Honest scope: the backend runs agents as pipes (no PTY), so this is a
// read-only log panel — never styled to look interactive. It polls byte
// ranges (ReadLogRange), keeps a bounded line window in memory, pauses
// fetching while the window is hidden (agents keep running in Go), and
// shows explicit truncation indicators. No fake progress or fake terminal.
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "../lib/api";
import { Button, IconButton, Tag } from "./ui";
import { ArrowDown, Pause, Play, WrapText, FileText } from "lucide-react";
import { desktop } from "../lib/api";

const WINDOW_LINES = 4000; // bounded buffer: older lines are dropped, full log stays on disk
const POLL_MS = 700;
const CHUNK_BYTES = 96 * 1024;

export function LogViewer({ taskID, compact = false }: { taskID: string; compact?: boolean }) {
  const [lines, setLines] = useState<string[]>([]);
  const [dropped, setDropped] = useState(0);
  const [following, setFollowing] = useState(true);
  const [wrapped, setWrapped] = useState(true);
  const [size, setSize] = useState(0);
  const [exists, setExists] = useState<boolean | null>(null);
  const [logPath, setLogPath] = useState("");
  const offsetRef = useRef(0);
  const visibleRef = useRef(true);
  const boxRef = useRef<HTMLDivElement>(null);
  const taskRef = useRef(taskID);

  useEffect(() => {
    taskRef.current = taskID;
    setLines([]);
    setDropped(0);
    setFollowing(true);
    offsetRef.current = 0;
    setSize(0);
    setExists(null);
    setLogPath("");
  }, [taskID]);

  useEffect(() => {
    const onVis = () => {
      visibleRef.current = document.visibilityState === "visible";
    };
    document.addEventListener("visibilitychange", onVis);
    return () => document.removeEventListener("visibilitychange", onVis);
  }, []);

  const poll = useCallback(async () => {
    if (!visibleRef.current) return; // paused for hidden windows; Go keeps running
    let id = taskRef.current;
    if (!id) return;
    try {
      const chunk = await api.readLogRange(id, offsetRef.current, CHUNK_BYTES);
      setExists(chunk.exists);
      setLogPath(chunk.path ?? "");
      setSize(chunk.size ?? 0);
      if (chunk.content) {
        const incoming = chunk.content.split(/\r?\n/);
        if (incoming[incoming.length - 1] === "") incoming.pop();
        setLines((prev) => {
          let next = prev.concat(incoming);
          let drop = 0;
          if (next.length > WINDOW_LINES) {
            drop = next.length - WINDOW_LINES;
            next = next.slice(drop);
          }
          if (drop) setDropped((d) => d + drop);
          return next;
        });
      }
      if (chunk.next_offset > offsetRef.current) {
        offsetRef.current = chunk.next_offset;
      }
    } catch {
      /* task may have vanished mid-poll */
    }
  }, []);

  useEffect(() => {
    poll();
    const iv = window.setInterval(poll, POLL_MS);
    return () => window.clearInterval(iv);
  }, [poll, taskID, following]);

  useEffect(() => {
    if (following && boxRef.current) {
      boxRef.current.scrollTop = boxRef.current.scrollHeight;
    }
  }, [lines, following]);

  return (
    <div className="flex flex-col min-h-0 h-full">
      <div className="flex items-center gap-2 px-2 py-1.5 border-b border-edge bg-raised rounded-t-lg">
        <FileText size={13} className="text-mut shrink-0" aria-hidden />
        <span className="text-xs text-mut font-mono truncate flex-1" title={logPath}>
          {logPath ? logPath : (exists === false ? "no log recorded yet" : "…")}
        </span>
        {dropped > 0 && (
          <Tag tone="warn">{dropped.toLocaleString()} older lines not shown — full log on disk</Tag>
        )}
        {size > 0 && <Tag tone="mono">{(size / 1024).toFixed(1)} KiB</Tag>}
        <IconButton label={wrapped ? "Disable line wrap" : "Enable line wrap"} onClick={() => setWrapped((w) => !w)}>
          <WrapText size={13} className={wrapped ? "text-accent-ink" : ""} />
        </IconButton>
        <Button size="sm" variant={following ? "default" : "primary"} onClick={() => setFollowing((f) => !f)}>
          {following ? <Pause size={12} /> : <Play size={12} />}
          {following ? "Following" : "Paused"}
        </Button>
        {!following && (
          <IconButton label="Jump to latest" onClick={() => setFollowing(true)}>
            <ArrowDown size={13} />
          </IconButton>
        )}
        {logPath && (
          <Button size="sm" variant="ghost" onClick={() => desktop.revealPath(logPath)}>
            Reveal
          </Button>
        )}
      </div>
      <div
        ref={boxRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
          if (!atBottom && following) setFollowing(false);
        }}
        className={`flex-1 min-h-0 overflow-auto bg-inset rounded-b-lg font-mono text-[12.5px] leading-[1.55] px-3 py-2 ${
          wrapped ? "whitespace-pre-wrap break-all" : "whitespace-pre"
        }`}
        role="log"
        aria-live="off"
      >
        {exists === false ? (
          <div className="text-faint font-sans text-sm p-4">No log recorded yet. Logs appear when the task starts running.</div>
        ) : lines.length === 0 ? (
          <div className="text-faint font-sans text-sm p-4">Waiting for output…</div>
        ) : (
          lines.map((l, i) => <div key={i}>{l || " "}</div>)
        )}
      </div>
      {compact && <div className="h-0" />}
    </div>
  );
}
