"""Controlled execution: worktrees, mock agents, timeouts, cancellation, logs."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import time
from pathlib import Path

from conftest import git, make_repo, mock_task, plan_spec, register, write_plan_file


def _setup(guy, repo, tasks, alias="r", capsys=None):
    register(guy, repo, alias)
    spec = plan_spec(tasks)
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 0
    assert guy("approve", "PLAN-001") == 0
    if capsys is not None:
        capsys.readouterr()
    return None


def _tasks_by_id():
    from the_guy import config
    from the_guy.store import Store
    store = Store(config.db_path())
    try:
        return {t.id: t for t in store.list_tasks()}
    finally:
        store.close()


def test_two_concurrent_mock_agents_distinct_worktrees(guy_env, guy, py_repo):
    _setup(guy, py_repo, [
        mock_task(key="a", title="Write A", write=[("feature-a.txt", "alpha")],
                  allowed_paths=["feature-a.txt"]),
        mock_task(key="b", title="Write B", write=[("feature-b.txt", "beta")],
                  allowed_paths=["feature-b.txt"]),
    ])
    baseline_head = git(py_repo, "rev-parse", "HEAD").strip()
    assert guy("run", "--approve", "--max-workers", "2") == 0
    tasks = _tasks_by_id()
    assert tasks["TASK-001"].status == "needs_review"
    assert tasks["TASK-002"].status == "needs_review"
    assert tasks["TASK-001"].worktree != tasks["TASK-002"].worktree
    assert tasks["TASK-001"].branch != tasks["TASK-002"].branch
    assert (Path(tasks["TASK-001"].worktree) / "feature-a.txt").read_text(encoding="utf-8") == "alpha"
    assert (Path(tasks["TASK-002"].worktree) / "feature-b.txt").read_text(encoding="utf-8") == "beta"
    # no auto-commit: worktrees dirty, no new commits, default branch untouched
    for t in tasks.values():
        assert git(t.worktree, "log", "--oneline", f"{t.baseline_commit}..HEAD").strip() == ""
        assert git(t.worktree, "status", "--porcelain").strip() != ""
    assert git(py_repo, "rev-parse", "HEAD").strip() == baseline_head


def test_dry_run_launches_nothing(guy_env, guy, py_repo, capsys):
    _setup(guy, py_repo, [mock_task(key="a", write=[("x.txt", "x")])])
    baseline_head = git(py_repo, "rev-parse", "HEAD").strip()
    assert guy("run", "--dry-run") == 0
    out = capsys.readouterr().out
    assert "DRY RUN" in out and "--approve" in out
    assert "guy" in out  # sanitized command shown
    tasks = _tasks_by_id()
    assert tasks["TASK-001"].status == "approved_for_execution"
    assert tasks["TASK-001"].worktree is None
    worktrees = [l for l in git(py_repo, "worktree", "list").splitlines() if l.strip()]
    assert len(worktrees) == 1  # only the user's main worktree; dry runs create nothing
    assert git(py_repo, "rev-parse", "HEAD").strip() == baseline_head
    # bare `guy run` is also a dry run by default
    assert guy("run") == 0
    assert "DRY RUN" in capsys.readouterr().out


def test_dependency_blocks_until_integrated(guy_env, guy, py_repo, capsys):
    _setup(guy, py_repo, [
        mock_task(key="a", write=[("a.txt", "a")]),
        mock_task(key="b", write=[("b.txt", "b")], dependencies=["a"]),
    ])
    assert guy("run", "--approve") == 0
    tasks = _tasks_by_id()
    assert tasks["TASK-001"].status == "needs_review"
    assert tasks["TASK-002"].status == "blocked"
    assert "TASK-001" in tasks["TASK-002"].blocked_reason

    # integrate A, then B unblocks on the next run
    assert guy("approve", "TASK-001") == 0
    assert guy("integrate", "--approve", "--plan", "PLAN-001") == 0
    assert guy("run", "--approve") == 0
    tasks = _tasks_by_id()
    assert tasks["TASK-002"].status == "needs_review"


def test_path_collision_blocks_concurrent_write(guy_env, guy, node_repo):
    _setup(guy, node_repo, [
        mock_task(key="a", write=[("shared/x.txt", "a")], allowed_paths=["shared/**"]),
        mock_task(key="b", write=[("shared/y.txt", "b")], allowed_paths=["shared/**"]),
    ])
    assert guy("run", "--approve") == 0
    tasks = _tasks_by_id()
    statuses = sorted(t.status for t in tasks.values())
    # overlapping scopes must not run concurrently: one finished, the other blocked
    assert statuses == ["blocked", "needs_review"]
    blocked = next(t for t in tasks.values() if t.status == "blocked")
    assert "path collision" in blocked.blocked_reason


def test_disjoint_paths_run_concurrently(guy_env, guy, node_repo):
    _setup(guy, node_repo, [
        mock_task(key="a", write=[("a/x.txt", "a")], allowed_paths=["a/**"]),
        mock_task(key="b", write=[("b/y.txt", "b")], allowed_paths=["b/**"]),
    ])
    assert guy("run", "--approve") == 0
    tasks = _tasks_by_id()
    assert all(t.status == "needs_review" for t in tasks.values())


def test_prompt_transport_unicode_quotes_spaces(guy_env, guy, py_repo):
    fancy = ("Résumé — «l'agent» said \"hello\" & <bye>; 100% × y(z) — unicode ✓ "
             "path C:\\Users\\Some One\\file.txt")
    _setup(guy, py_repo, [mock_task(key="a", objective=fancy, write=[("x.txt", "x")])])
    assert guy("run", "--approve") == 0
    task = _tasks_by_id()["TASK-001"]
    received = (Path(task.worktree) / "mock-prompt-received.txt").read_text(encoding="utf-8")
    assert fancy in received  # prompt transported verbatim as a single argv element
    assert "README" not in received


def test_agent_failure_reported(guy_env, guy, py_repo, capsys):
    _setup(guy, py_repo, [mock_task(key="a", exit=7)])
    assert guy("run", "--approve") == 1
    tasks = _tasks_by_id()
    assert tasks["TASK-001"].status == "failed"
    assert tasks["TASK-001"].exit_code == 7
    out = capsys.readouterr().out
    assert "[failed] TASK-001" in out


def test_timeout_kills_and_fails(guy_env, guy, py_repo):
    _setup(guy, py_repo, [mock_task(key="a", sleep=30, timeout=1)])
    started = time.monotonic()
    assert guy("run", "--approve") == 1
    assert time.monotonic() - started < 25
    task = _tasks_by_id()["TASK-001"]
    assert task.status == "failed"
    assert "timed out" in (task.exit_reason or "")


def test_missing_cli_adapter(guy_env, guy, py_repo, capsys):
    from the_guy.adapters.base import Adapter

    class GhostAdapter(Adapter):
        name = "ghost"
        display = "Ghost (test)"
        candidates = ["definitely-not-a-real-cli-xyz"]
        verify_help_tokens = ["exec"]

        def build_command(self, prompt, cwd, options):
            return [self.candidates[0], "exec", prompt]

    from the_guy import adapters as adapters_mod
    saved = dict(adapters_mod._ADAPTERS)
    try:
        adapters_mod.register_custom(GhostAdapter())
        _setup(guy, py_repo, [mock_task(key="a", agent="ghost")])
        assert guy("run", "--approve") == 1
        task = _tasks_by_id()["TASK-001"]
        assert task.status == "failed"
        assert "adapter unavailable" in (task.exit_reason or "")
        # doctor reports it truthfully as not installed
        assert guy("doctor") == 0
        assert "no executable found" in capsys.readouterr().out
    finally:
        adapters_mod._ADAPTERS.clear()
        adapters_mod._ADAPTERS.update(saved)


def test_unsupported_flag_disables_adapter(guy_env, guy, py_repo, capsys):
    from the_guy.adapters.base import Adapter, CapabilityReport

    class StubbornAdapter(Adapter):
        name = "stubborn"
        display = "Stubborn (test)"
        candidates = []
        verify_help_tokens = ["--definitely-not-a-real-flag"]

        def probe(self, timeout=20.0):
            return CapabilityReport(adapter=self.name, display=self.display,
                                    installed=True, executable="C:/fake/stubborn.exe",
                                    headless_verified=False,
                                    headless_note="--help does not mention --definitely-not-a-real-flag; "
                                                  "refusing to guess CLI syntax (adapter disabled)")

        def build_command(self, prompt, cwd, options):
            raise NotImplementedError

    from the_guy import adapters as adapters_mod
    saved = dict(adapters_mod._ADAPTERS)
    try:
        adapters_mod.register_custom(StubbornAdapter())
        _setup(guy, py_repo, [mock_task(key="a", agent="stubborn")])
        assert guy("run", "--approve") == 1
        task = _tasks_by_id()["TASK-001"]
        assert task.status == "failed"
        assert "refusing to guess" in (task.exit_reason or "")
        assert guy("agents") == 0
        assert "headless unverified" in capsys.readouterr().out
    finally:
        adapters_mod._ADAPTERS.clear()
        adapters_mod._ADAPTERS.update(saved)


def test_spawn_error_retry_then_fail(guy_env, guy, py_repo):
    from the_guy.adapters.base import Adapter, CapabilityReport

    missing = Path(os.environ["GUY_DATA_DIR"]) / "missing-agent.exe"

    class VanishingAdapter(Adapter):
        name = "vanishing"
        display = "Vanishing (test)"
        candidates = []

        def probe(self, timeout=20.0):
            return CapabilityReport(adapter=self.name, display=self.display,
                                    installed=True, executable=str(missing),
                                    headless_verified=True, headless_note="test stub")

        def build_command(self, prompt, cwd, options):
            return [str(missing), "exec", prompt]

    from the_guy import adapters as adapters_mod
    saved = dict(adapters_mod._ADAPTERS)
    try:
        adapters_mod.register_custom(VanishingAdapter())
        register(guy, py_repo, "r")
        spec = plan_spec([dict(mock_task(key="a"), agent="vanishing", max_retries=1)])
        plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
        assert guy("plan", "--file", str(plan_file)) == 0
        assert guy("approve", "PLAN-001") == 0
        assert guy("run", "--approve") == 1
        task = _tasks_by_id()["TASK-001"]
        assert task.status == "failed"
        assert task.retry_count == 1  # retried exactly once, then gave up
        assert "failed to start" in (task.exit_reason or "")
    finally:
        adapters_mod._ADAPTERS.clear()
        adapters_mod._ADAPTERS.update(saved)


def test_cancel_kills_process_tree(guy_env, guy, py_repo):
    register(guy, py_repo, "r")
    spec = plan_spec([mock_task(key="a", sleep=60,
                                extra={"spawn_child": {"seconds": 120}})])
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 0
    assert guy("approve", "PLAN-001") == 0

    env = dict(os.environ)
    run_proc = subprocess.Popen(
        [sys.executable, "-m", "the_guy", "run", "--approve"],
        env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)

    from the_guy import config
    from the_guy.runner import pid_alive
    from the_guy.store import Store

    task = None
    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        time.sleep(0.4)
        store = Store(config.db_path())
        try:
            tasks = store.list_tasks()
            if tasks and tasks[0].status == "running":
                task = tasks[0]
                break
        finally:
            store.close()
    assert task is not None, "task never started"
    child_pid_file = Path(task.worktree) / "mock-child.pid"
    deadline = time.monotonic() + 30
    while not child_pid_file.exists() and time.monotonic() < deadline:
        time.sleep(0.2)
    child_pid = int(child_pid_file.read_text().strip())
    assert pid_alive(child_pid)

    assert guy("cancel", task.id) == 0
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        store = Store(config.db_path())
        try:
            fresh = store.get_task(task.id)
        finally:
            store.close()
        if fresh.status == "cancelled":
            break
        time.sleep(0.4)
    assert fresh.status == "cancelled"
    run_proc.wait(timeout=60)
    deadline = time.monotonic() + 20
    while pid_alive(child_pid) and time.monotonic() < deadline:
        time.sleep(0.5)
    assert not pid_alive(child_pid), "orphaned child process survived cancellation"


def test_logs_redaction_and_cap(guy_env, guy, py_repo, capsys):
    assert guy("config", "set", "log_max_bytes", "30000") == 0
    big = "x" * 40000
    _setup(guy, py_repo, [mock_task(key="a", extra={"leak_secret": True, "stdout": big})])
    assert guy("run", "--approve") == 0
    assert guy("logs", "TASK-001") == 0
    out = capsys.readouterr().out
    assert "AKIAIOSFODNN7EXAMPLE" not in out
    assert "SuperSecret123" not in out
    assert "[REDACTED]" in out
    task = _tasks_by_id()["TASK-001"]
    log = Path(_runs(task.id)[-1].log_path)
    assert log.stat().st_size < 60000  # bounded growth
    assert "SuperSecret123" not in log.read_text(encoding="utf-8", errors="replace")


def _runs(task_id):
    from the_guy import config
    from the_guy.store import Store
    store = Store(config.db_path())
    try:
        return store.runs_for_task(task_id)
    finally:
        store.close()


def test_restart_recovery_interrupted_run(guy_env, guy, py_repo):
    _setup(guy, py_repo, [mock_task(key="a", write=[("x.txt", "x")])])
    # fabricate a run whose owner process is dead, with the task stuck 'running'
    dead = subprocess.Popen([sys.executable, "-c", "print(1)"])
    dead.wait()
    assert dead.returncode == 0

    from the_guy import config
    from the_guy.models import RunRecord, utcnow
    from the_guy.store import Store
    store = Store(config.db_path())
    try:
        task = store.list_tasks()[0]
        task.status = "running"
        task.started_at = utcnow()
        store.update_task(task)
        store.insert_run(RunRecord(
            id="RUN-999", task_id=task.id, adapter="mock", argv=["mock"],
            cwd=str(py_repo), pid=999999999, owner_pid=dead.pid + 1,
            status="running", started_at=utcnow()))
    finally:
        store.close()

    from the_guy.runner import pid_alive
    assert not pid_alive(dead.pid + 1)
    assert guy("status") == 0  # reconcile runs on status
    store = Store(config.db_path())
    try:
        task = store.get_task(task.id)
        assert task.status == "failed"
        assert "unexpectedly" in (task.exit_reason or "")
        run = store.get_run("RUN-999")
        assert run.failure_class == "interrupted"
    finally:
        store.close()


def test_readonly_task_runs_in_repo_root(guy_env, guy, py_repo):
    _setup(guy, py_repo, [mock_task(key="a", read_only=True)])
    baseline_head = git(py_repo, "rev-parse", "HEAD").strip()
    assert guy("run", "--approve") == 0
    task = _tasks_by_id()["TASK-001"]
    assert task.status == "needs_review"
    assert task.worktree is None
    assert git(py_repo, "rev-parse", "HEAD").strip() == baseline_head
