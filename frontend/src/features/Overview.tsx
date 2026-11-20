// Overview / Home: real global state, recent activity, first-run onboarding.
import { useEffect, useState } from "react";
import { useApp } from "../state/app";
import { api } from "../lib/api";
import { Button, Card, EmptyState, SectionHeader, Skeleton, Tag, KeyValue, Banner } from "../components/ui";
import { fmtBytes, fmtDate, fmtTime } from "../lib/format";
import type { CapabilityReport } from "../lib/types";
import { Activity, Bot, FolderGit2, Plus, RefreshCw, Cpu, PlayCircle } from "lucide-react";

export function Overview() {
  const app = useApp();
  const [agents, setAgents] = useState<CapabilityReport[] | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.listAgents(true).then(setAgents).catch(() => setAgents([]));
  }, [app.refreshTick]);

  if (!app.ready) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-24" />
        <Skeleton className="h-48" />
      </div>
    );
  }

  const h = app.health;
  const totalTasks = app.repos.reduce((n, r) => n + r.tasks_count, 0);
  const activeRuns = app.repos.reduce((n, r) => n + r.running_tasks, 0);
  const needsReview = app.repos.reduce((n, r) => n + r.needs_review, 0);
  const drafts = app.repos.reduce((n, r) => n + r.drafts, 0);
  const verified = (agents ?? []).filter((a) => a.headless_verified && a.kind !== "mock").length;
  const recentEvents = app.events.slice(-14).reverse();

  return (
    <div className="space-y-5">
      <SectionHeader
        title="Overview"
        subtitle="One guy. Every agent. Any repo."
        actions={
          <Button onClick={async () => { setBusy(true); await app.refresh(); setBusy(false); }} loading={busy}>
            <RefreshCw size={13} /> Refresh
          </Button>
        }
      />

      {app.repos.length === 0 && (
        <Card className="p-5">
          <h3 className="font-semibold mb-1">Welcome to The Guy</h3>
          <p className="text-sm text-mut mb-4 max-w-xl">
            Register a Git repository, verify your agent CLIs, then create a plan. Nothing runs
            until you explicitly approve it — planning, running, and integration are separate,
            individually approved steps.
          </p>
          <div className="flex gap-2 flex-wrap">
            <Button variant="primary" onClick={() => app.setScreen("repos")}>
              <Plus size={13} /> Register a repository
            </Button>
            <Button onClick={() => app.setScreen("agents")}>
              <Bot size={13} /> Check agent CLIs
            </Button>
          </div>
        </Card>
      )}

      {/* stat tiles */}
      <div className="grid grid-cols-2 lg:grid-cols-5 gap-3">
        {[
          { label: "Repositories", value: app.repos.length, icon: FolderGit2, screen: "repos" as const, tone: "text-ink" },
          { label: "Active runs", value: activeRuns, icon: PlayCircle, screen: "runs" as const, tone: activeRuns ? "text-running" : "text-mut" },
          { label: "Needs review", value: needsReview, icon: Activity, screen: "review" as const, tone: needsReview ? "text-warn" : "text-mut" },
          { label: "Draft tasks", value: drafts, icon: Activity, screen: "plans" as const, tone: drafts ? "text-pending" : "text-mut" },
          { label: "Verified agents", value: `${verified}/${(agents ?? []).filter((a) => a.kind !== "mock").length}`, icon: Bot, screen: "agents" as const, tone: "text-ink" },
        ].map((t) => {
          const Icon = t.icon;
          return (
            <button
              key={t.label}
              onClick={() => app.setScreen(t.screen)}
              className="text-left rounded-lg border border-edge bg-raised p-3.5 hover:border-edge-strong transition-colors"
            >
              <div className="flex items-center gap-2 text-mut text-xs">
                <Icon size={13} aria-hidden /> {t.label}
              </div>
              <div className={`text-2xl font-semibold mt-1 ${t.tone}`}>{t.value}</div>
            </button>
          );
        })}
      </div>

      <div className="grid lg:grid-cols-2 gap-4">
        {/* service health */}
        <Card className="p-4">
          <div className="flex items-center gap-2 mb-3">
            <Cpu size={14} className="text-mut" aria-hidden />
            <h3 className="font-medium text-sm">Service</h3>
            <Tag tone="mono">the-guy {h?.version}</Tag>
          </div>
          {h ? (
            <KeyValue
              items={[
                ["Process", `pid ${h.pid} · ${h.os}/${h.arch}`],
                ["CPUs detected", `${h.num_cpu} logical (${h.go_max_procs} Go procs)`],
                ["State DB", `${h.db_path} (${fmtBytes(h.db_size_bytes)})`],
                ["Data dir", h.data_dir],
                ["Worktrees", h.worktrees_root],
                ["Logs", h.logs_root],
                ["Started", fmtDate(h.started_at)],
              ]}
            />
          ) : (
            <Skeleton className="h-40" />
          )}
          <div className="mt-3 text-xs text-faint leading-relaxed">
            Runs are owned by this window. Closing it interrupts active tasks — they are marked
            interrupted and can be re-run (same recovery as a CLI crash). No background daemon.
          </div>
        </Card>

        {/* recent activity */}
        <Card className="p-4 min-h-64">
          <h3 className="font-medium text-sm mb-3">Recent activity</h3>
          {recentEvents.length === 0 ? (
            <EmptyState title="No activity yet" body="Events appear here as you register repos, create plans and run tasks." />
          ) : (
            <ul className="space-y-1.5 text-sm">
              {recentEvents.map((e) => (
                <li key={e.seq} className="flex items-baseline gap-2.5">
                  <span className="text-faint text-xs font-mono w-16 shrink-0">{fmtTime(e.ts)}</span>
                  <span className="font-mono text-xs text-accent-ink truncate">{e.kind}</span>
                  <span className="text-faint text-xs font-mono truncate">
                    {e.task_id || e.repo_id || ""}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>

      {app.repos.length > 0 && (
        <Card className="p-4">
          <h3 className="font-medium text-sm mb-3">Repositories</h3>
          <ul className="divide-y divide-edge">
            {app.repos.map((r) => (
              <li key={r.id} className="py-2 flex items-center gap-3 text-sm">
                <FolderGit2 size={14} className="text-mut shrink-0" aria-hidden />
                <span className="font-medium">{r.alias}</span>
                <span className="text-faint text-xs font-mono truncate flex-1">{r.path}</span>
                {r.registration_error ? (
                  <Tag tone="fail">{r.registration_error}</Tag>
                ) : (
                  <>
                    <Tag tone="mono">{r.current_branch}</Tag>
                    {r.dirty ? <Tag tone="warn">dirty</Tag> : <Tag tone="ok">clean</Tag>}
                    {r.running_tasks > 0 && <Tag tone="accent">{r.running_tasks} running</Tag>}
                    {r.needs_review > 0 && <Tag tone="warn">{r.needs_review} to review</Tag>}
                  </>
                )}
              </li>
            ))}
          </ul>
        </Card>
      )}

      {totalTasks > 0 && activeRuns === 0 && drafts === 0 && needsReview === 0 && (
        <Banner kind="info">
          {totalTasks} task(s) recorded. Open <b>Plans</b> to approve and run the next step, or <b>Runs</b> for history.
        </Banner>
      )}
    </div>
  );
}
