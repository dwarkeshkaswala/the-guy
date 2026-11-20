// Formatting helpers and status metadata (label + color + icon semantics).
// Status is always communicated with icon AND text, never color alone.
import {
  CircleDashed, CircleCheck, CircleX, CircleAlert, Clock, Eye, Layers,
  PlayCircle, PauseCircle, CircleDot, type LucideIcon,
} from "lucide-react";

export function fmtDuration(ms: number | null | undefined): string {
  if (ms == null || ms < 0) return "—";
  if (ms < 1000) return `${ms} ms`;
  const s = Math.floor(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${s % 60}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

export function fmtElapsedSince(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  return fmtDuration(now - t);
}

export function fmtBytes(n: number | null | undefined): string {
  if (n == null || n < 0) return "—";
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  return `${(n / (1024 * 1024)).toFixed(1)} MiB`;
}

export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return new Date(t).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function fmtDate(iso: string | null | undefined): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  return new Date(t).toLocaleString([], {
    year: "numeric", month: "short", day: "numeric",
    hour: "2-digit", minute: "2-digit",
  });
}

export function shortId(id: string): string {
  const idx = id.indexOf("-");
  return idx > 0 ? id.slice(idx + 1) : id;
}

export function truncate(s: string, n: number): string {
  return s.length > n ? s.slice(0, n - 1) + "…" : s;
}

export interface StatusMeta {
  label: string;
  color: "ok" | "fail" | "warn" | "running" | "pending" | "neutral";
  icon: LucideIcon;
}

const statusMeta: Record<string, StatusMeta> = {
  draft: { label: "Draft", color: "neutral", icon: CircleDashed },
  approved_for_execution: { label: "Approved to run", color: "pending", icon: PlayCircle },
  queued: { label: "Queued", color: "pending", icon: Clock },
  running: { label: "Running", color: "running", icon: CircleDot },
  needs_review: { label: "Needs review", color: "warn", icon: Eye },
  approved: { label: "Reviewed", color: "ok", icon: CircleCheck },
  integrated: { label: "Integrated", color: "ok", icon: Layers },
  blocked: { label: "Blocked", color: "fail", icon: CircleAlert },
  failed: { label: "Failed", color: "fail", icon: CircleX },
  cancelled: { label: "Cancelled", color: "neutral", icon: PauseCircle },
  // run statuses
  starting: { label: "Starting", color: "pending", icon: Clock },
  finished: { label: "Finished", color: "ok", icon: CircleCheck },
  "cancel-requested": { label: "Cancelling", color: "warn", icon: PauseCircle },
};

export function statusMetaOf(status: string): StatusMeta {
  return statusMeta[status] ?? { label: status || "unknown", color: "neutral", icon: CircleDashed };
}
