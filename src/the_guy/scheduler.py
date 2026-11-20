"""Task scheduler: dependency gating, bounded concurrency, collision detection.

Policy:
- A dependent task only becomes runnable once its dependencies are
  `integrated` (explicit human approval + integration, never mutual agent
  agreement).
- One write owner per worktree; tasks whose allowed_paths overlap are blocked
  from running concurrently on the same repo.
- Retries only for "safe" failure classes (default: process spawn errors).
- `guy run` never merges, commits, or pushes — integration is a separate,
  explicitly approved step.
"""

from __future__ import annotations

import os
import threading
from collections import deque
from concurrent.futures import FIRST_COMPLETED, ThreadPoolExecutor, wait
from dataclasses import dataclass
from fnmatch import fnmatch
from pathlib import Path
from typing import Dict, List, Optional

from the_guy import adapters, config, events, git_ops, runner, security
from the_guy.models import Repo, RunRecord, Task, utcnow
from the_guy.store import Store


class SchedulingError(Exception):
    pass


@dataclass
class PlannedLaunch:
    task_id: str
    title: str
    agent: str
    adapter_ok: bool
    adapter_note: str
    argv_display: str
    cwd: str
    worktree: Optional[str]
    branch: Optional[str]
    baseline_branch: str
    baseline_commit: str
    timeout_seconds: Optional[int]
    read_only: bool
    pending_dependencies: List[str]

    def to_dict(self) -> dict:
        return dict(self.__dict__)


def reconcile(store: Store) -> List[dict]:
    """Mark runs whose owning `guy` process died as interrupted (restart recovery)."""
    fixed = []
    for run in store.active_runs():
        if run.owner_pid and runner.pid_alive(run.owner_pid):
            continue
        if run.status == "cancel-requested":
            run.status = "cancelled"
            run.failure_class = "cancelled"
        else:
            run.status = "failed"
            run.failure_class = "interrupted"
        run.finished_at = utcnow()
        store.update_run(run)
        task = store.get_task(run.task_id)
        if task and task.status == "running":
            task.apply_transition("failed")
            task.exit_reason = "orchestrator restarted or runner process exited unexpectedly"
            task.finished_at = utcnow()
            store.update_task(task)
        events.log(store, "run_interrupted", repo_id=task.repo_id if task else None,
                   task_id=run.task_id, run_id=run.id, failure_class=run.failure_class)
        fixed.append({"run": run.id, "task": run.task_id, "failure_class": run.failure_class})
    return fixed


class Scheduler:
    def __init__(self, store: Store, settings: dict, repo: Repo):
        self.store = store
        self.settings = settings
        self.repo = repo
        self.root = Path(repo.path)

    # --- selection helpers -------------------------------------------------
    def blocked_reason_for(self, task: Task, tasks_by_id: Dict[str, Task]) -> Optional[str]:
        pending = []
        for dep_id in task.dependencies:
            dep = tasks_by_id.get(dep_id)
            if dep is None:
                pending.append(f"{dep_id} (missing)")
            elif dep.status != "integrated":
                pending.append(f"{dep_id} ({dep.status})")
        if pending:
            return (
                "waiting for dependencies: " + ", ".join(pending)
                + " — approve and integrate them first (`guy approve ...`, `guy integrate --approve`)"
            )
        return None

    @staticmethod
    def _paths_overlap(a: List[str], b: List[str]) -> Optional[str]:
        if not a or not b:
            return "write scope overlaps: empty allowed_paths means the whole repository"
        for pa in a:
            pa_norm = pa.replace("\\", "/").rstrip("/")
            for pb in b:
                pb_norm = pb.replace("\\", "/").rstrip("/")
                if pa_norm == pb_norm or fnmatch(pa_norm, pb_norm) or fnmatch(pb_norm, pa_norm):
                    return f"allowed paths overlap: '{pa}' vs '{pb}'"
                if pb_norm.startswith(pa_norm + "/") or pa_norm.startswith(pb_norm + "/"):
                    return f"allowed paths overlap: '{pa}' vs '{pb}' (subdirectory)"
        return None

    def collision_with(self, task: Task, running: List[Task]) -> Optional[str]:
        if task.read_only:
            return None
        for other in running:
            if other.read_only or other.id == task.id:
                continue
            overlap = self._paths_overlap(task.allowed_paths, other.allowed_paths)
            if overlap:
                return f"path collision with running task {other.id}: {overlap}"
        return None

    def _prompt_for(self, task: Task, workdir: Path) -> str:
        lines = [
            "You are an autonomous coding agent executing exactly one task in an isolated Git worktree,",
            "coordinated by the-guy.",
            "",
            f"Task: {task.title}",
            f"Objective: {task.objective}",
        ]
        if task.acceptance_criteria:
            lines.append("")
            lines.append("Acceptance criteria:")
            lines.extend(f"- {c}" for c in task.acceptance_criteria)
        if task.allowed_paths:
            lines.append("")
            lines.append("Allowed paths (stay within these): " + ", ".join(task.allowed_paths))
        lines += [
            "",
            "Rules:",
            f"- Work only inside this directory: {workdir}",
            "- Do not run git push, git rebase, git reset, or modify branches other than this worktree's.",
            "- Do not commit your changes; a human reviews them first and the orchestrator commits after approval.",
        ]
        return "\n".join(lines)

    def _resolve_workdir(self, task: Task) -> Path:
        if task.read_only:
            return self.root
        repo_wt_root = config.worktrees_root() / self.repo.id
        wt = Path(task.worktree) if task.worktree else repo_wt_root / task.id
        security.ensure_within(repo_wt_root, wt)
        return wt

    # --- dry run -------------------------------------------------------------
    def dry_run(self, tasks: List[Task]) -> List[PlannedLaunch]:
        launches: List[PlannedLaunch] = []
        tasks_by_id = {t.id: t for t in self.store.list_tasks(repo_id=self.repo.id)}
        for task in tasks:
            adapter_note, adapter_ok, argv_display = "", True, "(unavailable)"
            try:
                adapter = adapters.get(task.agent)
                report = adapter.probe()
                adapter_ok = report.installed and report.headless_verified
                adapter_note = report.headless_note or ("ok" if adapter_ok else "unavailable")
                if adapter_ok:
                    workdir = self._resolve_workdir(task)
                    argv = adapter.build_command(self._prompt_for(task, workdir), workdir, task.options or {})
                    argv_display = security.display_argv(argv)
            except adapters.AdapterUnavailable as exc:
                adapter_ok, adapter_note = False, str(exc)
            workdir = self._resolve_workdir(task)
            pending = [d for d in task.dependencies
                       if tasks_by_id.get(d) is None or tasks_by_id[d].status != "integrated"]
            launches.append(PlannedLaunch(
                task_id=task.id,
                title=task.title,
                agent=task.agent,
                adapter_ok=adapter_ok,
                adapter_note=adapter_note,
                argv_display=argv_display,
                cwd=str(workdir),
                worktree=None if task.read_only else str(workdir),
                branch=task.branch or (None if task.read_only else f"guy/{task.id}-{security.slug(task.title)}"),
                baseline_branch=task.baseline_branch,
                baseline_commit=task.baseline_commit,
                timeout_seconds=task.timeout_seconds or self.settings.get("run_timeout_seconds"),
                read_only=task.read_only,
                pending_dependencies=pending,
            ))
        return launches

    # --- execution -------------------------------------------------------------
    def execute(self, tasks: List[Task], max_workers: int) -> dict:
        max_workers = max(1, min(int(max_workers), 8))
        summary: Dict[str, List[dict]] = {
            "launched": [], "needs_review": [], "failed": [],
            "cancelled": [], "blocked": [], "retried": [],
        }
        tasks_by_id = {t.id: t for t in self.store.list_tasks(repo_id=self.repo.id)}
        queue: deque = deque()
        for task in tasks:
            fresh = self.store.get_task(task.id) or task
            reason = self.blocked_reason_for(fresh, tasks_by_id)
            if reason:
                if fresh.status in ("approved_for_execution", "queued"):
                    fresh.apply_transition("blocked", reason)
                    self.store.update_task(fresh)
                    events.log(self.store, "task_blocked", repo_id=self.repo.id,
                               task_id=fresh.id, reason=reason)
                summary["blocked"].append({"task": fresh.id, "reason": reason})
            else:
                if fresh.status == "blocked":
                    fresh.apply_transition("queued", "dependencies satisfied")
                    self.store.update_task(fresh)
                queue.append(fresh)

        if not queue:
            return summary

        pool = ThreadPoolExecutor(max_workers=max_workers, thread_name_prefix="guy-run")
        futures: Dict = {}
        running_ids: List[str] = []
        lock = threading.Lock()
        try:
            while queue or futures:
                while queue and len(futures) < max_workers:
                    task = queue.popleft()
                    running_tasks = [tasks_by_id[tid] for tid in running_ids if tid in tasks_by_id]
                    collision = self.collision_with(task, running_tasks)
                    if collision:
                        if task.status in ("approved_for_execution", "queued"):
                            task.apply_transition("blocked", collision)
                            self.store.update_task(task)
                            events.log(self.store, "task_blocked", repo_id=self.repo.id,
                                       task_id=task.id, reason=collision)
                        summary["blocked"].append({"task": task.id, "reason": collision})
                        continue
                    futures[pool.submit(self._launch, task)] = task
                    with lock:
                        running_ids.append(task.id)
                    summary["launched"].append({"task": task.id, "agent": task.agent})
                if not futures:
                    break
                done, _ = wait(list(futures.keys()), timeout=1.0, return_when=FIRST_COMPLETED)
                for future in done:
                    task = futures.pop(future)
                    with lock:
                        if task.id in running_ids:
                            running_ids.remove(task.id)
                    try:
                        result = future.result()
                    except Exception as exc:  # defensive: scheduler bug must not wedge the loop
                        result = {"task": task.id, "final_status": "failed",
                                  "reason": f"orchestrator error: {exc}", "requeue": False}
                        summary["failed"].append(result)
                        continue
                    if result.get("requeue"):
                        fresh = self.store.get_task(task.id)
                        if fresh and fresh.status == "queued":
                            queue.append(fresh)
                            summary["retried"].append({"task": task.id,
                                                       "attempt": fresh.retry_count})
                        else:
                            summary[result.get("final_status") or "failed"].append(result)
                    else:
                        bucket = result.get("final_status") or "failed"
                        summary.setdefault(bucket, []).append(result)
        finally:
            pool.shutdown(wait=True)
        return summary

    def _launch(self, task: Task) -> dict:
        store, repo = self.store, self.repo
        result = {"task": task.id, "title": task.title,
                  "final_status": "failed", "reason": None}
        try:
            adapter = adapters.get(task.agent)
            adapter.verify_for_execution()
        except adapters.AdapterUnavailable as exc:
            task.exit_reason = f"adapter unavailable: {exc}"
            self._finish(task, "failed", result)
            return result

        try:
            workdir = self._prepare_workdir(task)
        except Exception as exc:
            task.exit_reason = f"worktree setup failed: {exc}"
            self._finish(task, "failed", result)
            return result

        if task.status in ("approved_for_execution", "failed"):
            task.apply_transition("queued")
            store.update_task(task)
            events.log(store, "task_queued", repo_id=repo.id, task_id=task.id,
                       note="manual re-run" if task.retry_count else "")
        task.apply_transition("running")
        task.started_at = utcnow()
        store.update_task(task)
        events.log(store, "task_started", repo_id=repo.id, task_id=task.id,
                   agent=task.agent, cwd=str(workdir))

        prompt = self._prompt_for(task, workdir)
        env = adapter.child_env(dict(os.environ), task.options or {})
        argv = adapter.build_command(prompt, workdir, task.options or {})
        timeout = task.timeout_seconds or self.settings.get("run_timeout_seconds")
        run_id = store.next_id("RUN")
        log_path = config.logs_root() / repo.id / task.id / f"run-{run_id}.log"
        run = RunRecord(id=run_id, task_id=task.id, adapter=task.agent, argv=argv,
                        cwd=str(workdir), status="starting", started_at=utcnow(),
                        log_path=str(log_path), owner_pid=os.getpid())
        store.insert_run(run)
        events.log(store, "run_started", repo_id=repo.id, task_id=task.id, run_id=run_id)

        def status_poll():
            current = store.get_run(run_id)
            return current.status if current else None

        def on_start(pid: int) -> None:
            run.pid = pid
            run.status = "running"
            store.update_run(run)

        outcome = runner.execute(
            argv, workdir, env, timeout, log_path,
            max_log_bytes=int(self.settings.get("log_max_bytes", 1_000_000)),
            status_poll=status_poll, on_start=on_start,
        )
        run.exit_code = outcome.exit_code
        run.duration_ms = outcome.duration_ms
        run.finished_at = utcnow()
        run.failure_class = outcome.failure_class
        run.status = ("cancelled" if outcome.failure_class == "cancelled"
                      else "failed" if outcome.failure_class else "finished")
        store.update_run(run)
        task.exit_code = outcome.exit_code
        task.finished_at = utcnow()
        events.log(store, "run_finished", repo_id=repo.id, task_id=task.id, run_id=run_id,
                   exit_code=outcome.exit_code, failure_class=outcome.failure_class)

        if outcome.failure_class == "cancelled":
            task.exit_reason = "cancelled by user"
            self._finish(task, "cancelled", result)
            return result
        if outcome.failure_class == "spawn_error":
            task.exit_reason = f"failed to start agent process: {outcome.spawn_error}"
            safe = self.settings.get("safe_retry_failure_classes", [])
            if task.retry_count < task.max_retries and outcome.failure_class in safe:
                task.retry_count += 1
                task.apply_transition("failed")
                store.update_task(task)
                task.apply_transition("queued", "automatic retry (spawn_error is a safe failure class)")
                store.update_task(task)
                events.log(store, "task_retry_queued", repo_id=repo.id, task_id=task.id,
                           attempt=task.retry_count)
                result.update(final_status="failed", reason=task.exit_reason, requeue=True)
                return result
            self._finish(task, "failed", result)
            return result
        if outcome.failure_class == "timeout":
            task.exit_reason = f"timed out after {timeout}s"
            self._finish(task, "failed", result)
            return result
        if outcome.exit_code == 0:
            if not task.read_only and task.worktree:
                wt = Path(task.worktree)
                dirty = [p for _, p in git_ops.status_porcelain(wt)]
                diffed = git_ops.diff_name_only(wt, task.baseline_commit)
                task.changed_files = sorted(set(dirty) | set(diffed))
                task.commits = git_ops.commits_between(wt, task.baseline_commit)
            task.exit_reason = "ok (exit 0 — human review still required)"
            self._finish(task, "needs_review", result)
            return result
        task.exit_reason = f"agent exited with code {outcome.exit_code}; see logs"
        self._finish(task, "failed", result)
        return result

    def _prepare_workdir(self, task: Task) -> Path:
        if task.read_only:
            return self.root
        repo_wt_root = config.worktrees_root() / self.repo.id
        wt = Path(task.worktree) if task.worktree else repo_wt_root / task.id
        security.ensure_within(repo_wt_root, wt)
        if wt.exists():
            if not task.worktree:
                task.worktree = str(wt)
            return wt
        branch = task.branch or f"guy/{task.id}-{security.slug(task.title)}"
        if git_ops.branch_exists(self.root, branch):
            raise SchedulingError(
                f"branch '{branch}' already exists without a managed worktree; "
                f"run `guy clean --task {task.id} --branch` or remove it manually first"
            )
        git_ops.worktree_add(self.root, wt, branch, task.baseline_commit)
        task.worktree = str(wt)
        task.branch = branch
        return wt

    def _finish(self, task: Task, status: str, result: dict) -> None:
        reason = task.exit_reason
        try:
            task.apply_transition(status, reason)
        except Exception as exc:
            reason = f"{reason or ''} [transition error: {exc}]".strip()
            task.status = status
        task.finished_at = task.finished_at or utcnow()
        self.store.update_task(task)
        events.log(self.store, f"task_{status}", repo_id=task.repo_id, task_id=task.id,
                   reason=reason)
        result.update(final_status=status, reason=reason)
