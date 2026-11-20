// UI primitives: buttons, badges, modals, fields, empty/skeleton states.
// Accessible by default: focus-visible rings, aria labels, keyboard dismissal.
import React, { useEffect, useRef, useState } from "react";
import { X, Copy, Check, Loader2, Inbox, TriangleAlert, Info, CircleCheck, CircleX } from "lucide-react";
import { statusMetaOf } from "../lib/format";

export function Button({
  variant = "default", size = "md", loading, children, className = "", ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: "default" | "primary" | "danger" | "ghost";
  size?: "sm" | "md";
  loading?: boolean;
}) {
  const base =
    "inline-flex items-center justify-center gap-1.5 rounded-md font-medium transition-colors select-none " +
    "disabled:opacity-50 disabled:pointer-events-none ";
  const sizes = size === "sm" ? "text-xs px-2.5 py-1" : "text-sm px-3 py-1.5";
  const variants = {
    default: "bg-raised text-ink border border-edge hover:bg-overlay",
    primary: "bg-accent text-white hover:brightness-110",
    danger: "bg-fail-soft text-fail border border-fail/40 hover:bg-fail/20",
    ghost: "text-mut hover:text-ink hover:bg-overlay",
  }[variant];
  return (
    <button className={`${base}${sizes} ${variants} ${className}`} disabled={loading || rest.disabled} {...rest}>
      {loading && <Loader2 size={14} className="spin" aria-hidden />}
      {children}
    </button>
  );
}

export function IconButton({
  label, children, className = "", ...rest
}: React.ButtonHTMLAttributes<HTMLButtonElement> & { label: string }) {
  return (
    <button
      aria-label={label}
      title={label}
      className={`inline-flex items-center justify-center h-7 w-7 rounded-md text-mut hover:text-ink hover:bg-overlay transition-colors ${className}`}
      {...rest}
    >
      {children}
    </button>
  );
}

const statusTones: Record<string, string> = {
  ok: "border-ok/35 bg-ok-soft text-ok",
  fail: "border-fail/35 bg-fail-soft text-fail",
  warn: "border-warn/35 bg-warn-soft text-warn",
  running: "border-running/35 bg-running-soft text-running",
  pending: "border-pending/35 bg-pending-soft text-pending",
  neutral: "border-neutral/35 bg-neutral-soft text-mut",
};

export function StatusBadge({ status, small }: { status: string; small?: boolean }) {
  const meta = statusMetaOf(status);
  const Icon = meta.icon;
  const live = status === "running";
  return (
    <span
      className={`inline-flex items-center gap-1.5 rounded-full border ${statusTones[meta.color] ?? statusTones.neutral} ${
        small ? "text-[11px] px-2 py-0.5" : "text-xs px-2.5 py-1"
      }`}
    >
      <Icon size={small ? 12 : 13} className={live ? "pulse-dot" : ""} aria-hidden />
      {meta.label}
    </span>
  );
}

export function Tag({
  children, tone = "neutral", title,
}: { children: React.ReactNode; tone?: string; title?: string }) {
  const tones: Record<string, string> = {
    neutral: "border-edge text-mut bg-overlay",
    accent: "border-accent/35 text-accent-ink bg-accent-soft",
    ok: "border-ok/35 text-ok bg-ok-soft",
    warn: "border-warn/35 text-warn bg-warn-soft",
    fail: "border-fail/35 text-fail bg-fail-soft",
    mono: "border-edge text-mut bg-inset font-mono",
  };
  return (
    <span title={title} className={`inline-flex items-center rounded border px-1.5 py-0.5 text-[11px] leading-4 ${tones[tone] ?? tones.neutral}`}>
      {children}
    </span>
  );
}

export function SectionHeader({
  title, subtitle, actions,
}: { title: string; subtitle?: string; actions?: React.ReactNode }) {
  return (
    <div className="flex items-end justify-between gap-4 mb-4">
      <div>
        <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
        {subtitle && <p className="text-mut text-sm mt-0.5">{subtitle}</p>}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

export function Card({ children, className = "", ...rest }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div className={`rounded-lg border border-edge bg-raised ${className}`} {...rest}>
      {children}
    </div>
  );
}

export function EmptyState({
  icon, title, body, action,
}: { icon?: React.ReactNode; title: string; body?: string; action?: React.ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center text-center py-14 px-6 rounded-lg border border-dashed border-edge">
      <div className="text-faint mb-3">{icon ?? <Inbox size={28} />}</div>
      <div className="font-medium">{title}</div>
      {body && <p className="text-sm text-mut mt-1 max-w-md">{body}</p>}
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export function Skeleton({ className = "" }: { className?: string }) {
  return <div className={`skeleton rounded ${className}`} />;
}

export function Banner({
  kind = "info", children, onClose,
}: { kind?: "info" | "warn" | "fail" | "ok"; children: React.ReactNode; onClose?: () => void }) {
  const map = {
    info: { cls: "border-accent/40 bg-accent-soft text-accent-ink", Icon: Info },
    warn: { cls: "border-warn/40 bg-warn-soft text-warn", Icon: TriangleAlert },
    fail: { cls: "border-fail/40 bg-fail-soft text-fail", Icon: CircleX },
    ok: { cls: "border-ok/40 bg-ok-soft text-ok", Icon: CircleCheck },
  }[kind];
  const Icon = map.Icon;
  return (
    <div className={`flex items-start gap-2.5 rounded-md border px-3.5 py-2.5 text-sm ${map.cls}`} role={kind === "fail" ? "alert" : "status"}>
      <Icon size={15} className="mt-0.5 shrink-0" aria-hidden />
      <div className="min-w-0 flex-1">{children}</div>
      {onClose && (
        <button onClick={onClose} aria-label="Dismiss" className="opacity-70 hover:opacity-100">
          <X size={14} />
        </button>
      )}
    </div>
  );
}

export function Modal({
  open, onClose, title, children, footer, wide,
}: {
  open: boolean; onClose: () => void; title: string;
  children: React.ReactNode; footer?: React.ReactNode; wide?: boolean;
}) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    const t = window.setTimeout(() => ref.current?.focus(), 30);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.clearTimeout(t);
    };
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-6">
      <div className="absolute inset-0 bg-black/55" onClick={onClose} aria-hidden />
      <div
        ref={ref}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={`relative w-full ${wide ? "max-w-3xl" : "max-w-lg"} max-h-[85vh] flex flex-col rounded-xl border border-edge-strong bg-overlay shadow-2xl outline-none`}
      >
        <div className="flex items-center justify-between px-5 py-3.5 border-b border-edge">
          <h3 className="font-semibold">{title}</h3>
          <IconButton label="Close dialog" onClick={onClose}>
            <X size={15} />
          </IconButton>
        </div>
        <div className="px-5 py-4 overflow-y-auto">{children}</div>
        {footer && <div className="px-5 py-3.5 border-t border-edge flex justify-end gap-2">{footer}</div>}
      </div>
    </div>
  );
}

export function ConfirmDialog({
  open, onClose, onConfirm, title, body, confirmLabel = "Confirm",
  danger, loading,
}: {
  open: boolean; onClose: () => void; onConfirm: () => void; title: string;
  body: React.ReactNode; confirmLabel?: string; danger?: boolean; loading?: boolean;
}) {
  return (
    <Modal
      open={open}
      onClose={loading ? () => undefined : onClose}
      title={title}
      footer={
        <>
          <Button onClick={onClose} disabled={loading}>Cancel</Button>
          <Button variant={danger ? "danger" : "primary"} onClick={onConfirm} loading={loading}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="text-sm text-mut leading-relaxed">{body}</div>
    </Modal>
  );
}

export function Field({
  label, hint, children,
}: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <label className="block">
      <span className="block text-xs font-medium text-mut mb-1.5">{label}</span>
      {children}
      {hint && <span className="block text-xs text-faint mt-1.5">{hint}</span>}
    </label>
  );
}

export const inputCls =
  "w-full rounded-md border border-edge bg-inset px-2.5 py-1.5 text-sm text-ink placeholder:text-faint focus:border-accent outline-none";

export function Input(props: React.InputHTMLAttributes<HTMLInputElement>) {
  return <input {...props} className={`${inputCls} ${props.className ?? ""}`} />;
}

export function Select(props: React.SelectHTMLAttributes<HTMLSelectElement>) {
  return <select {...props} className={`${inputCls} ${props.className ?? ""}`} />;
}

export function TextArea(props: React.TextareaHTMLAttributes<HTMLTextAreaElement>) {
  return <textarea {...props} className={`${inputCls} font-mono leading-relaxed ${props.className ?? ""}`} />;
}

export function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <IconButton
      label={label}
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(text);
        } catch {
          /* clipboard may be unavailable in the WebView */
        }
        setDone(true);
        window.setTimeout(() => setDone(false), 1200);
      }}
    >
      {done ? <Check size={13} className="text-ok" /> : <Copy size={13} />}
    </IconButton>
  );
}

export function KeyValue({ items }: { items: [string, React.ReactNode][] }) {
  return (
    <dl className="grid gap-x-6 gap-y-1.5 text-sm" style={{ gridTemplateColumns: "max-content 1fr" }}>
      {items.map(([k, v]) => (
        <React.Fragment key={k}>
          <dt className="text-faint whitespace-nowrap">{k}</dt>
          <dd className="min-w-0 break-all font-mono text-[13px]">{v}</dd>
        </React.Fragment>
      ))}
    </dl>
  );
}

export function Spinner({ label }: { label?: string }) {
  return (
    <div className="flex items-center justify-center gap-2 py-10 text-mut" role="status">
      <Loader2 size={16} className="spin" />
      {label && <span className="text-sm">{label}</span>}
    </div>
  );
}
