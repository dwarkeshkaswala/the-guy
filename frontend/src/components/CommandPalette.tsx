// Global command palette (Ctrl/Cmd+K). Keyboard-first navigation over real
// actions — every command executes a real operation or navigation.
import { useEffect, useMemo, useRef, useState } from "react";
import { useApp, type Screen } from "../state/app";
import { api } from "../lib/api";
import { CornerDownLeft, Search } from "lucide-react";

interface Command {
  id: string;
  label: string;
  hint?: string;
  run: () => void | Promise<void>;
}

export function CommandPalette() {
  const app = useApp();
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const open = app.paletteOpen;

  useEffect(() => {
    if (open) {
      setQ("");
      setSel(0);
      window.setTimeout(() => inputRef.current?.focus(), 20);
    }
  }, [open]);

  const nav = (s: Screen) => {
    app.setScreen(s);
    app.setPaletteOpen(false);
  };

  const commands = useMemo<Command[]>(() => {
    const list: Command[] = [
      { id: "overview", label: "Go to Overview", hint: "Ctrl+1", run: () => nav("overview") },
      { id: "repos", label: "Go to Repositories", hint: "Ctrl+2", run: () => nav("repos") },
      { id: "agents", label: "Go to Agents", hint: "Ctrl+3", run: () => nav("agents") },
      { id: "plans", label: "Go to Plans & Task Graph", hint: "Ctrl+4", run: () => nav("plans") },
      { id: "runs", label: "Go to Runs", hint: "Ctrl+5", run: () => nav("runs") },
      { id: "review", label: "Go to Review & Approvals", hint: "Ctrl+6", run: () => nav("review") },
      { id: "settings", label: "Go to Settings & Diagnostics", hint: "Ctrl+7", run: () => nav("settings") },
      {
        id: "refresh", label: "Refresh all data", run: async () => {
          await app.refresh();
          app.setPaletteOpen(false);
        },
      },
      {
        id: "rescan", label: "Re-probe agent adapters", run: async () => {
          await api.listAgents(false);
          app.toast("ok", "Agent re-probe finished");
          app.setPaletteOpen(false);
        },
      },
      {
        id: "display-auto", label: "Display mode: Auto", run: async () => {
          await api.updateSetting("display_mode", "auto");
          await app.refresh();
          app.setPaletteOpen(false);
        },
      },
      {
        id: "display-perf", label: "Display mode: Performance", run: async () => {
          await api.updateSetting("display_mode", "performance");
          await app.refresh();
          app.setPaletteOpen(false);
        },
      },
      {
        id: "display-eco", label: "Display mode: Efficiency", run: async () => {
          await api.updateSetting("display_mode", "efficiency");
          await app.refresh();
          app.setPaletteOpen(false);
        },
      },
      {
        id: "display-compat", label: "Display mode: Compatibility", run: async () => {
          await api.updateSetting("display_mode", "compatibility");
          await app.refresh();
          app.setPaletteOpen(false);
        },
      },
      {
        id: "theme-toggle", label: "Toggle light/dark theme", run: async () => {
          const darkNow =
            document.documentElement.dataset.theme === "dark";
          await api.updateSetting("ui_theme", darkNow ? "light" : "dark");
          await app.refresh();
          app.setPaletteOpen(false);
        },
      },
    ];
    if (app.currentRepo) {
      list.splice(1, 0, {
        id: "new-plan",
        label: `New plan in ${app.currentRepo.alias}…`,
        hint: "Ctrl+4",
        run: () => {
          nav("plans");
          window.dispatchEvent(new CustomEvent("guy:new-plan"));
        },
      });
      list.splice(1, 0, {
        id: "add-repo2", label: "Register a repository…", run: () => nav("repos"),
      });
    } else {
      list.splice(1, 0, {
        id: "add-repo", label: "Register a repository…", hint: "Ctrl+2", run: () => nav("repos"),
      });
    }
    return list;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [app.currentRepo?.id, app]);

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return commands;
    return commands.filter(
      (c) =>
        c.label.toLowerCase().includes(needle) ||
        (c.hint ?? "").toLowerCase().includes(needle),
    );
  }, [commands, q]);

  useEffect(() => {
    setSel((s) => Math.min(s, Math.max(0, filtered.length - 1)));
  }, [filtered.length]);

  if (!open) return null;

  return (
    <div className="fixed inset-0 z-[60] flex items-start justify-center pt-[12vh]">
      <div className="absolute inset-0 bg-black/50" onClick={() => app.setPaletteOpen(false)} aria-hidden />
      <div
        role="dialog"
        aria-label="Command palette"
        className="relative w-full max-w-xl rounded-xl border border-edge-strong bg-overlay shadow-2xl overflow-hidden"
      >
        <div className="flex items-center gap-2.5 px-4 py-3 border-b border-edge">
          <Search size={15} className="text-faint" aria-hidden />
          <input
            ref={inputRef}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault();
                setSel((s) => Math.min(s + 1, filtered.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setSel((s) => Math.max(s - 1, 0));
              } else if (e.key === "Enter") {
                e.preventDefault();
                filtered[sel]?.run();
              } else if (e.key === "Escape") {
                app.setPaletteOpen(false);
              }
            }}
            placeholder="Type a command…"
            className="flex-1 bg-transparent outline-none text-sm placeholder:text-faint"
            aria-label="Search commands"
          />
          <kbd className="text-[10px] text-faint border border-edge rounded px-1.5 py-0.5">esc</kbd>
        </div>
        <div className="max-h-80 overflow-y-auto py-1.5">
          {filtered.length === 0 && (
            <div className="px-4 py-6 text-sm text-faint text-center">No matching commands</div>
          )}
          {filtered.map((c, i) => (
            <button
              key={c.id}
              onMouseEnter={() => setSel(i)}
              onClick={() => c.run()}
              className={`w-full flex items-center gap-3 px-4 py-2 text-left text-sm ${
                i === sel ? "bg-accent-soft text-accent-ink" : "hover:bg-overlay"
              }`}
            >
              <span className="flex-1">{c.label}</span>
              {c.hint && (
                <kbd className="text-[10px] text-faint border border-edge rounded px-1.5 py-0.5">{c.hint}</kbd>
              )}
              {i === sel && <CornerDownLeft size={12} className="text-faint" />}
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
