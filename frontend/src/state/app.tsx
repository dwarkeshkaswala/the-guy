// Global app state: health, repositories, settings, live events, toasts.
// The event stream drives refreshes (no fake polling of business data).
import React, {
  createContext, useCallback, useContext, useEffect, useMemo, useRef, useState,
} from "react";
import { api, onGuyEvent } from "../lib/api";
import type { RepoView, HealthReport, EventEnvelope, RunSummary } from "../lib/types";

export type Screen = "overview" | "repos" | "agents" | "plans" | "runs" | "review" | "settings";

export interface Toast {
  id: number;
  kind: "ok" | "fail" | "info" | "warn";
  text: string;
}

interface AppState {
  ready: boolean;
  fatal: string | null;
  health: HealthReport | null;
  repos: RepoView[];
  currentRepo: RepoView | null;
  setCurrentRepo: (r: RepoView | null) => void;
  settings: Record<string, unknown>;
  events: EventEnvelope[]; // newest last, bounded
  lastEventSeq: number;
  now: number; // updated by ticks, for elapsed timers
  refresh: () => Promise<void>;
  refreshTick: number; // bump to signal screens to refetch their data
  screen: Screen;
  setScreen: (s: Screen) => void;
  paletteOpen: boolean;
  setPaletteOpen: (b: boolean) => void;
  logTaskId: string | null; // bottom dock
  openLog: (taskID: string | null) => void;
  toasts: Toast[];
  toast: (kind: Toast["kind"], text: string) => void;
  dismissToast: (id: number) => void;
}

const Ctx = createContext<AppState | null>(null);

export function useApp(): AppState {
  const v = useContext(Ctx);
  if (!v) throw new Error("useApp outside provider");
  return v;
}

const MAX_EVENTS = 200;

export function AppProvider({ children }: { children: React.ReactNode }) {
  const [ready, setReady] = useState(false);
  const [fatal, setFatal] = useState<string | null>(null);
  const [health, setHealth] = useState<HealthReport | null>(null);
  const [repos, setRepos] = useState<RepoView[]>([]);
  const [settings, setSettings] = useState<Record<string, unknown>>({});
  const [events, setEvents] = useState<EventEnvelope[]>([]);
  const [lastEventSeq, setLastEventSeq] = useState(0);
  const [now, setNow] = useState(Date.now());
  const [refreshTick, setRefreshTick] = useState(0);
  const [screen, setScreen] = useState<Screen>("overview");
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [logTaskId, setLogTaskId] = useState<string | null>(null);
  const [toasts, setToasts] = useState<Toast[]>([]);
  const [currentRepoId, setCurrentRepoId] = useState<string | null>(null);
  const toastSeq = useRef(1);

  const toast = useCallback((kind: Toast["kind"], text: string) => {
    const id = toastSeq.current++;
    setToasts((t) => [...t.slice(-4), { id, kind, text }]);
    window.setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), 6000);
  }, []);
  const dismissToast = useCallback((id: number) => {
    setToasts((t) => t.filter((x) => x.id !== id));
  }, []);

  const refresh = useCallback(async () => {
    try {
      const [h, r, s] = await Promise.all([
        api.health(),
        api.listRepositories(),
        api.getSettings(),
      ]);
      setHealth(h);
      setRepos(r);
      setSettings(s.settings ?? {});
      setCurrentRepoId((prev) => {
        if (prev && r.some((x) => x.id === prev)) return prev;
        const def = r.find((x) => x.is_default) ?? r[0] ?? null;
        return def ? def.id : null;
      });
      setFatal(null);
    } catch (e) {
      setFatal(String(e));
    }
  }, []);

  useEffect(() => {
    (async () => {
      await refresh();
      setReady(true);
    })();
  }, [refresh]);

  // live events → bounded buffer + refresh signal
  useEffect(() => {
    const offEvent = onGuyEvent<EventEnvelope>("guy:event", (env) => {
      setEvents((prev) => {
        if (prev.some((e) => e.seq === env.seq)) return prev;
        const next = [...prev, env];
        return next.length > MAX_EVENTS ? next.slice(next.length - MAX_EVENTS) : next;
      });
      setLastEventSeq((s) => Math.max(s, env.seq));
      setRefreshTick((t) => t + 1);
    });
    const offTick = onGuyEvent<{ time: string; active_runs: number }>("guy:tick", () => {
      setNow(Date.now());
    });
    const offSummary = onGuyEvent<RunSummary>("guy:run-summary", (s) => {
      const failed = s.summary?.failed?.length ?? 0;
      const review = s.summary?.needs_review?.length ?? 0;
      const parts: string[] = [];
      if (review) parts.push(`${review} task(s) need review`);
      if (failed) parts.push(`${failed} failed`);
      if (!parts.length) parts.push("all tasks finished");
      toast(failed ? "fail" : "ok", `Run batch finished — ${parts.join(", ")}`);
      setRefreshTick((t) => t + 1);
    });
    const offReconciled = onGuyEvent<{ recovered: unknown[] }>("guy:reconciled", (r) => {
      toast("info", `Recovered ${r.recovered?.length ?? 0} interrupted run(s) from a previous session`);
    });
    return () => {
      offEvent();
      offTick();
      offSummary();
      offReconciled();
    };
  }, [toast]);

  // periodic refresh while any run is active (safety net alongside events)
  useEffect(() => {
    const anyActive = repos.some((r) => r.running_tasks > 0);
    if (!anyActive) return;
    const iv = window.setInterval(() => setRefreshTick((t) => t + 1), 2500);
    return () => window.clearInterval(iv);
  }, [repos]);

  // theme / density / font / display mode application
  useEffect(() => {
    const root = document.documentElement;
    const theme = String(settings.ui_theme ?? "system");
    const apply = () => {
      const dark =
        theme === "dark" ||
        (theme === "system" && window.matchMedia("(prefers-color-scheme: dark)").matches);
      root.dataset.theme = dark ? "dark" : "light";
    };
    apply();
    const mq = window.matchMedia("(prefers-color-scheme: dark)");
    mq.addEventListener("change", apply);
    root.dataset.density = String(settings.ui_density ?? "comfortable");
    root.dataset.display = String(settings.display_mode ?? "auto");
    const size = Number(settings.ui_font_size ?? 14);
    root.style.setProperty("--ui-font-size", `${Number.isFinite(size) && size >= 12 && size <= 18 ? size : 14}px`);
    return () => mq.removeEventListener("change", apply);
  }, [settings.ui_theme, settings.ui_density, settings.ui_font_size, settings.display_mode]);

  const currentRepo = useMemo(
    () => repos.find((r) => r.id === currentRepoId) ?? null,
    [repos, currentRepoId],
  );

  const value: AppState = {
    ready, fatal, health, repos, currentRepo,
    setCurrentRepo: (r) => setCurrentRepoId(r ? r.id : null),
    settings, events, lastEventSeq, now,
    refresh, refreshTick,
    screen, setScreen,
    paletteOpen, setPaletteOpen,
    logTaskId, openLog: setLogTaskId,
    toasts, toast, dismissToast,
  };

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}
