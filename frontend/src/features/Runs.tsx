// Runs / Workstream monitor: live task states (real process status, PIDs,
// worktrees, exit codes) plus run history. Actions: open live log, cancel
// (graceful → force with confirmation), retry failed tasks. No invented
// progress — phase and elapsed time only.
import { useCallback, useEffect, useState } from "react";
import { useApp } from "../state/app";
import { api } from "../lib/api";
import {
  Banner, Button, Card, ConfirmDialog, EmptyState, SectionHeader, StatusBadge,
  Tag, Spinner, CopyButton,
} from "../components/ui";
import type { Task, RunView } from "../lib/types";
import { fmtDate, fmtDuration, fmtElapsedSince, shortId, truncate } from "../lib/format";
import { Ban, FileText, ListChecks, RotateCcw, Terminal } from "lucide-react";

export function Runs() {
  const app = useApp();
  const [tasks, setTasks] = useState<Task[] | null>(null);
  const [runs, setRuns] = useState<RunView[] | null>(null);
  const [cancelTarget, setCancelTarget] = useState<Task | null>(null);
  const [cancelling, setCancelling] = useState(false);
  const [retryingTask, setRetryingTask] = useState<string | null>(null);

  const repo = app.currentRepo;

  const reload = useCallback(async () => {
    if (!repo) {
      setTasks([]);
      setRuns([]);
      return;
    }
    const [t, r] = await Promise.all([
      api.listTasks(repo.alias, "").catch(() => []),
      api.listRuns(repo.alias, 60).catch(() => []),
    ]);
    setTasks(t);
    setRuns(r);
  }, [repo?.alias]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    reload();
  }, [reload, app.refreshTick]);

  if (!repo) {
    return (
      <EmptyState
        icon={<ListChecks size={28} />}
        title="No repository selected"
        body="Select a repository to monitor its runs."
        action={<Button variant="primary" onClick={() => app.setScreen("repos")}>Go to repositories</Button>}
      />
    );
  }

  const active = (tasks ?? []).filter((t) => ["queued", "running"].includes(t.status));
  const watchable = (tasks ?? []).filter((t) =>
    ["running", "queued", "needs_review", "failed", "approved", "integrated", "cancelled"].includes(t.status),
  );

  const confirmCancel = async () => {
    if (!cancelTarget) return;
    setCancelling(true);
    try {
      const res = await api.cancelTask(cancelTarget.id);
      app.toast("ok", `${res.task}: cancelled (final status ${res.status}) — process tree killed`);
      setCancelTarget(null);
      await reload();
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setCancelling(false);
    }
  };

  const retryFailed = async (task: Task) => {
    setRetryingTask(task.id);
    try {
      const receipt = await api.startRun(repo.alias, "", [task.id], 0);
      if (receipt.started) {
        app.toast("ok", `Re-running ${task.id}`);
        app.openLog(task.id);
      } else {
        app.toast("info", receipt.reason ?? "task not retryable");
      }
    } catch (e) {
      app.toast("fail", String(e));
    } finally {
      setRetryingTask(null);
    }
  };

  return (
    <div className="space-y-4">
      <SectionHeader
        title="Runs"
        subtitle={`${repo.alias} — live task states and run history`}
        actions={
          <>
            {active.length > 0 && (
              <span className="inline-flex items-center gap-1.5 text-xs text-running">
                <span className="h-1.5 w-1.5 rounded-full bg-running pulse-dot" aria-hidden />
                {active.length} active
              </span>
            )}
            <Button onClick={() => reload()}>Refresh</Button>
          </>
        }
      />

      {active.length > 0 && (
        <Banner kind="info">
          Scheduling note: pause/resume of the scheduler is not implemented — this build launches
          batches and supports per-task cancellation. Multiple running jobs always use separate
          worktrees; overlapping write scopes are blocked before launch.
        </Banner>
      )}

      {tasks === null ? (
        <Spinner label="Loading tasks…" />
      ) : watchable.length === 0 ? (
        <EmptyState
          icon={<ListChecks size={28} />}
          title="No runs yet"
          body="Approve a plan and launch it — tasks appear here with real process status, PIDs and logs."
          action={<Button variant="primary" onClick={() => app.setScreen("plans")}>Go to plans</Button>}
        />
      ) : (
        <div className="space-y-2.5">
          {watchable.map((t) => (
            <TaskRunRow
              key={t.id}
              task={t}
              now={app.now}
              onOpenLog={() => app.openLog(t.id)}
              onCancel={() => setCancelTarget(t)}
              onRetry={() => retryFailed(t)}
              retrying={retryingTask === t.id}
              logActive={app.logTaskId === t.id}
            />
          ))}
        </div>
      )}

      {/* run history */}
      <Card className="overflow-hidden">
        <div className="px-3.5 py-2.5 border-b border-edge text-sm font-medium flex items-center gap-2">
          <Terminal size={13} className="text-mut" aria-hidden /> Run history
        </div>
        {runs === null ? (
          <div className="p-4"><Spinner /></div>
        ) : runs.length === 0 ? (
          <div className="px-4 py-6 text-sm text-faint text-center">No runs recorded yet.</div>
        ) : (
          <table className="w-full text-xs">
            <thead>
              <tr className="text-left text-faint border-b border-edge">
                <th className="px-3.5 py-2 font-medium">Run</th>
                <th className="px-3 py-2 font-medium">Task</th>
                <th className="px-3 py-2 font-medium">Adapter</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-3 py-2 font-medium">Exit</th>
                <th className="px-3 py-2 font-medium">Duration</th>
                <th className="px-3 py-2 font-medium">Started</th>
                <th className="px-3 py-2 font-medium">Log</th>
              </tr>
            </thead>
            <tbody>
              {runs.map((r) => (
                <tr key={r.id} className="border-b border-edge last:border-b-0 hover:bg-overlay">
                  <td className="px-3.5 py-1.5 font-mono">{r.id}</td>
                  <td className="px-3 py-1.5 font-mono" title={r.task_title}>{shortId(r.task_id)}</td>
                  <td className="px-3 py-1.5 font-mono">{r.adapter}</td>
                  <td className="px-3 py-1.5"><StatusBadge status={r.status} small /></td>
                  <td className="px-3 py-1.5 font-mono">{r.exit_code != null ? r.exit_code : "—"}</td>
                  <td className="px-3 py-1.5">{fmtDuration(r.duration_ms)}</td>
                  <td className="px-3 py-1.5">{fmtDate(r.started_at)}</td>
                  <td className="px-3 py-1.5">
                    <button className="text-accent-ink hover:underline" onClick={() => app.openLog(r.task_id)}>
                      open
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      <ConfirmDialog
        open={!!cancelTarget}
        onClose={() => setCancelTarget(null)}
        onConfirm={confirmCancel}
        loading={cancelling}
        danger
        title={`Cancel ${cancelTarget?.id ?? ""}`}
        confirmLabel="Cancel task (kill process tree)"
        body={
          <p>
            This asks the runner to stop and kills the agent's entire process tree
            (taskkill /T on Windows, process groups on POSIX). Uncommitted changes in the
            task's worktree are kept. If the process is unresponsive, it is marked
            cancelled after a 20s grace period.
          </p>
        }
      />
    </div>
  );
}

function TaskRunRow({
  task: t, now, onOpenLog, onCancel, onRetry, retrying, logActive,
}: {
  task: Task;
  now: number;
  onOpenLog: () => void;
  onCancel: () => void;
  onRetry: () => void;
  retrying: boolean;
  logActive: boolean;
}) {
  const running = t.status === "running";
  return (
    <Card className={`p-3.5 ${running ? "border-running/40" : ""}`}>
      <div className="flex items-center gap-2.5 flex-wrap">
        <StatusBadge status={t.status} />
        <span className="font-medium">{truncate(t.title, 70)}</span>
        <span className="font-mono text-xs text-faint">{shortId(t.id)}</span>
        <Tag tone="mono">{t.agent}</Tag>
        {t.read_only && <Tag>read-only</Tag>}
        <div className="flex-1" />
        {running && (
          <span className="text-xs text-mut font-mono" title="elapsed since start">
            {t.started_at ? fmtElapsedSince(t.started_at, now) : "—"} elapsed
          </span>
        )}
        {t.exit_code != null && (
          <Tag tone={t.exit_code === 0 ? "ok" : "fail"}>exit {t.exit_code}</Tag>
        )}
        <Button
          size="sm"
          variant={logActive ? "default" : "ghost"}
          onClick={onOpenLog}
        >
          <FileText size={12} /> {running ? "Live log" : "Log"}
        </Button>
        {["queued", "running", "approved_for_execution"].includes(t.status) && (
          <Button size="sm" variant="danger" onClick={onCancel}>
            <Ban size={11} /> Cancel
          </Button>
        )}
        {t.status === "failed" && (
          <Button size="sm" onClick={onRetry} loading={retrying}>
            <RotateCcw size={11} /> Re-run
          </Button>
        )}
      </div>
      {(running || t.status === "needs_review" || t.status === "failed") && (
        <div className="mt-2 text-xs text-faint font-mono flex items-center gap-3 flex-wrap">
          {running && <span>phase: executing agent · no invented progress — watch the log</span>}
          {t.worktree && (
            <span className="inline-flex items-center gap-1 truncate max-w-120">
              worktree: {t.worktree}
              <CopyButton text={t.worktree} label="Copy worktree path" />
            </span>
          )}
          {t.branch && <span>branch: {t.branch}</span>}
          {t.blocked_reason && <span className="text-warn">{t.blocked_reason}</span>}
        </div>
      )}
      {t.status === "failed" && t.exit_reason && (
        <div className="mt-1.5 text-xs text-fail">{t.exit_reason}</div>
      )}
    </Card>
  );
}
