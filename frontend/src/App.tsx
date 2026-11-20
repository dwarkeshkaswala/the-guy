// Application shell: nav rail, top bar (repo switcher + service health),
// routed content, bottom log dock, toasts, command palette, global keys.
import { useEffect, useState } from "react";
import { useApp, type Screen } from "./state/app";
import { CommandPalette } from "./components/CommandPalette";
import { LogViewer } from "./components/LogViewer";
import { Button, IconButton, Tag } from "./components/ui";
import { desktop, api } from "./lib/api";
import {
  LayoutDashboard, FolderGit2, Bot, Workflow, ListChecks, GitPullRequestArrow,
  Settings2, X, ChevronUp, Command, CircleDot, CircleAlert,
} from "lucide-react";
import { Overview } from "./features/Overview";
import { Repos } from "./features/Repos";
import { Agents } from "./features/Agents";
import { Plans } from "./features/Plans";
import { Runs } from "./features/Runs";
import { Review } from "./features/Review";
import { Settings } from "./features/Settings";

const NAV: { id: Screen; label: string; icon: typeof LayoutDashboard; hint: string }[] = [
  { id: "overview", label: "Overview", icon: LayoutDashboard, hint: "Ctrl+1" },
  { id: "repos", label: "Repositories", icon: FolderGit2, hint: "Ctrl+2" },
  { id: "agents", label: "Agents", icon: Bot, hint: "Ctrl+3" },
  { id: "plans", label: "Plans", icon: Workflow, hint: "Ctrl+4" },
  { id: "runs", label: "Runs", icon: ListChecks, hint: "Ctrl+5" },
  { id: "review", label: "Review", icon: GitPullRequestArrow, hint: "Ctrl+6" },
  { id: "settings", label: "Settings", icon: Settings2, hint: "Ctrl+7" },
];

const SCREENS: Record<Screen, () => JSX.Element> = {
  overview: Overview,
  repos: Repos,
  agents: Agents,
  plans: Plans,
  runs: Runs,
  review: Review,
  settings: Settings,
};

export function Shell() {
  const app = useApp();
  const [logOpen, setLogOpen] = useState(false);
  const Screen = SCREENS[app.screen];

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const mod = e.ctrlKey || e.metaKey;
      if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        app.setPaletteOpen(!app.paletteOpen);
      } else if (mod && e.key.toLowerCase() === "j" && app.logTaskId) {
        e.preventDefault();
        setLogOpen((v) => !v);
      } else if (mod && /^[1-7]$/.test(e.key)) {
        e.preventDefault();
        app.setScreen(NAV[Number(e.key) - 1].id);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [app]);

  // opening a log task from any screen also reveals the dock
  useEffect(() => {
    if (app.logTaskId) setLogOpen(true);
  }, [app.logTaskId]);

  if (app.fatal && !app.health) {
    return (
      <div className="h-full flex flex-col items-center justify-center gap-3 p-8 text-center">
        <CircleAlert size={28} className="text-fail" />
        <h1 className="text-lg font-semibold">Cannot reach the orchestrator service</h1>
        <p className="text-sm text-mut max-w-lg">{app.fatal}</p>
        <Button onClick={() => app.refresh()}>Retry</Button>
      </div>
    );
  }

  const needsReview = app.repos.reduce((n, r) => n + r.needs_review, 0);
  const active = app.repos.reduce((n, r) => n + r.running_tasks, 0);

  return (
    <div className="h-full flex flex-col bg-bg text-ink">
      {/* top bar */}
      <header className="flex items-center gap-3 h-11 px-3 border-b border-edge bg-raised shrink-0">
        <div className="flex items-center gap-2 font-semibold tracking-tight select-none">
          <span className="inline-flex items-center justify-center h-6 w-6 rounded-md bg-accent-soft text-accent-ink text-xs font-bold">G</span>
          The Guy
        </div>
        <span className="text-faint text-xs hidden lg:inline">One guy. Every agent. Any repo.</span>
        <div className="flex-1" />
        {app.currentRepo && (
          <Tag tone="mono">
            {app.currentRepo.alias} · {app.currentRepo.current_branch || "—"}
            {app.currentRepo.dirty ? " · dirty" : ""}
          </Tag>
        )}
        {needsReview > 0 && (
          <button onClick={() => app.setScreen("review")} className="focus:outline-none">
            <Tag tone="warn">{needsReview} to review</Tag>
          </button>
        )}
        {active > 0 && (
          <span className="inline-flex items-center gap-1.5 text-xs text-running">
            <CircleDot size={12} className="pulse-dot" aria-hidden />
            {active} running
          </span>
        )}
        <span className="inline-flex items-center gap-1.5 text-xs text-mut" title={`the-guy ${app.health?.version ?? ""} · pid ${app.health?.pid ?? ""}`}>
          <span className={`h-2 w-2 rounded-full ${app.fatal ? "bg-fail" : "bg-ok"}`} aria-hidden />
          {app.fatal ? "service error" : "service online"}
        </span>
        <button
          onClick={() => app.setPaletteOpen(true)}
          className="hidden md:inline-flex items-center gap-1.5 text-xs text-faint border border-edge rounded-md px-2 py-1 hover:text-mut hover:border-edge-strong"
        >
          <Command size={11} /> K
        </button>
      </header>

      <div className="flex flex-1 min-h-0">
        {/* nav rail */}
        <nav className="w-14 shrink-0 border-r border-edge bg-raised flex flex-col items-center py-2 gap-1" aria-label="Primary">
          {NAV.map((n) => {
            const Icon = n.icon;
            const isActive = app.screen === n.id;
            return (
              <button
                key={n.id}
                onClick={() => app.setScreen(n.id)}
                title={`${n.label} (${n.hint})`}
                aria-label={n.label}
                aria-current={isActive ? "page" : undefined}
                className={`relative flex flex-col items-center justify-center w-12 h-12 rounded-lg transition-colors ${
                  isActive ? "bg-accent-soft text-accent-ink" : "text-mut hover:text-ink hover:bg-overlay"
                }`}
              >
                <Icon size={18} aria-hidden />
                {n.id === "review" && needsReview > 0 && (
                  <span className="absolute top-1.5 right-1.5 h-1.5 w-1.5 rounded-full bg-warn" aria-hidden />
                )}
              </button>
            );
          })}
          <div className="flex-1" />
          <button
            onClick={async () => {
              const { count } = await desktop.closeWarning();
              app.toast(
                count > 0 ? "warn" : "info",
                count > 0
                  ? `${count} run(s) active — closing this window would interrupt them; they will be marked interrupted and can be re-run.`
                  : "No active runs — closing is safe. Use the window's close button to quit.",
              );
            }}
            title="Close disclosure"
            className="flex items-center justify-center w-12 h-12 rounded-lg text-faint hover:text-mut hover:bg-overlay"
            aria-label="Quit disclosure"
          >
            <CircleAlert size={16} />
          </button>
        </nav>

        {/* content */}
        <main className="flex-1 min-w-0 min-h-0 flex flex-col">
          <div className={`flex-1 min-h-0 overflow-y-auto ${logOpen && app.logTaskId ? "pb-0" : ""}`}>
            <div className="p-5 max-w-[1200px] mx-auto">
              <Screen />
            </div>
          </div>
          {logOpen && app.logTaskId && (
            <section
              aria-label="Task log"
              className="h-72 shrink-0 border-t border-edge-strong bg-raised px-3 pt-2 pb-3"
            >
              <div className="flex items-center justify-between mb-1.5">
                <div className="flex items-center gap-2 text-xs text-mut">
                  <ChevronUp size={12} aria-hidden />
                  Live log — <span className="font-mono text-ink">{app.logTaskId}</span>
                  <span className="text-faint">(read-only; agents run as subprocesses, not a terminal)</span>
                </div>
                <div className="flex items-center gap-1">
                  <Button size="sm" variant="ghost" onClick={() => api.listRuns(app.currentRepo?.id ?? "", 100).then((runs) => {
                    const last = runs.find((r) => r.task_id === app.logTaskId);
                    if (last?.log_path) desktop.revealPath(last.log_path);
                  })}>
                    Reveal log file
                  </Button>
                  <IconButton label="Close log panel" onClick={() => setLogOpen(false)}>
                    <X size={14} />
                  </IconButton>
                </div>
              </div>
              <div className="h-[calc(100%-1.75rem)]">
                <LogViewer taskID={app.logTaskId} />
              </div>
            </section>
          )}
        </main>
      </div>

      {/* toasts */}
      <div className="fixed bottom-4 right-4 z-[70] flex flex-col gap-2 w-96" role="status" aria-live="polite">
        {app.toasts.map((t) => (
          <button
            key={t.id}
            onClick={() => app.dismissToast(t.id)}
            className={`text-left rounded-lg border px-3.5 py-2.5 text-sm shadow-lg bg-overlay ${
              t.kind === "ok"
                ? "border-ok/40 text-ok"
                : t.kind === "fail"
                  ? "border-fail/40 text-fail"
                  : t.kind === "warn"
                    ? "border-warn/40 text-warn"
                    : "border-accent/40 text-accent-ink"
            }`}
          >
            {t.text}
          </button>
        ))}
      </div>

      <CommandPalette />
    </div>
  );
}
