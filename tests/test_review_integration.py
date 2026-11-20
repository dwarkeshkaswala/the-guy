"""Review, approvals, integration gates, conflicts."""

from __future__ import annotations

import json
import os
import sys
from pathlib import Path

from conftest import git, mock_task, plan_spec, register, write_plan_file


def _setup(guy, repo, tasks, alias="r"):
    register(guy, repo, alias)
    spec = plan_spec(tasks)
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 0
    assert guy("approve", "PLAN-001") == 0
    assert guy("run", "--approve") == 0


def _tasks_by_id():
    from the_guy import config
    from the_guy.store import Store
    store = Store(config.db_path())
    try:
        return {t.id: t for t in store.list_tasks()}
    finally:
        store.close()


def test_review_shows_diff_and_handoff(guy_env, guy, py_repo, capsys):
    _setup(guy, py_repo, [mock_task(key="a", write=[("src/new.txt", "brand new")])])
    assert guy("review", "TASK-001") == 0
    out = capsys.readouterr().out
    assert "Handoff: TASK-001" in out
    assert "src/new.txt" in out
    assert "Needs human judgment" in out
    assert "not proof that the code is correct" in out

    assert guy("review", "TASK-001", "--json") == 0
    data = json.loads(capsys.readouterr().out)
    assert data["handoff"]["task_id"] == "TASK-001"
    assert "src/new.txt" in data["changed_files"]
    assert data["handoff"]["needs_human_judgment"]


def test_tests_evidence_flow(guy_env, guy, py_repo, capsys):
    passing = [sys.executable, "-c",
               "from pathlib import Path; assert Path('out.txt').read_text(encoding='utf-8') == 'ok'"]
    failing = [sys.executable, "-c", "raise SystemExit(3)"]
    _setup(guy, py_repo, [mock_task(key="a", write=[("out.txt", "ok")], tests=[passing])])
    assert guy("approve", "TASK-001") == 0
    capsys.readouterr()

    # gate: required tests not yet run -> integrate refuses
    assert guy("integrate", "--approve", "--plan", "PLAN-001") == 1
    assert "required tests not passing" in capsys.readouterr().err

    # failing test evidence recorded when the command fails
    from the_guy import config
    from the_guy.store import Store
    store = Store(config.db_path())
    try:
        task = store.get_task("TASK-001")
        task.required_tests = [failing]
        store.update_task(task)
    finally:
        store.close()
    capsys.readouterr()
    assert guy("review", "TASK-001", "--run-tests", "--json") == 0
    data = json.loads(capsys.readouterr().out)
    assert data["test_results"][0]["ok"] is False

    # --skip-tests is the explicit escape hatch
    assert guy("integrate", "--approve", "--plan", "PLAN-001", "--skip-tests") == 0
    assert _tasks_by_id()["TASK-001"].status == "integrated"


def test_happy_path_integration(guy_env, guy, py_repo, capsys):
    test_cmd = [sys.executable, "-c",
                "from pathlib import Path; assert Path('feature.txt').exists()"]
    _setup(guy, py_repo, [mock_task(key="a", write=[("feature.txt", "hello")],
                                    tests=[test_cmd])])
    baseline_head = git(py_repo, "rev-parse", "HEAD").strip()

    capsys.readouterr()
    assert guy("review", "TASK-001", "--run-tests", "--json") == 0
    data = json.loads(capsys.readouterr().out)
    assert data["test_results"][0]["ok"] is True

    assert guy("approve", "TASK-001") == 0
    assert guy("integrate", "--dry-run", "--plan", "PLAN-001") == 0
    out = capsys.readouterr().out
    assert "INTEGRATION DRY RUN" in out
    assert "all gates pass" in out
    assert "NOT performed: any push" in out

    assert guy("integrate", "--approve", "--plan", "PLAN-001") == 0
    out = capsys.readouterr().out
    assert "integration complete" in out
    assert "Nothing was pushed" in out

    tasks = _tasks_by_id()
    assert tasks["TASK-001"].status == "integrated"
    # integration branch got the merge; default branch untouched; worktree has the file
    ib = "guy/plan-PLAN-001-integration"
    assert git(py_repo, "rev-parse", "--verify", f"refs/heads/{ib}").strip()
    assert git(py_repo, "rev-parse", "HEAD").strip() == baseline_head
    merge_log = git(py_repo, "log", "--oneline", f"{py_repo and 'trunk'}..{ib}")
    assert "guy: integrate TASK-001" in merge_log
    # the integration commit exists on the task branch too (committed via approval)
    assert git(tasks["TASK-001"].worktree, "status", "--porcelain").strip() == ""
    assert (Path(tasks["TASK-001"].worktree) / "feature.txt").exists()


def test_integrate_refuses_unapproved(guy_env, guy, py_repo, capsys):
    _setup(guy, py_repo, [mock_task(key="a", write=[("x.txt", "x")])])
    # task is needs_review (not approved) -> gates refuse
    assert guy("integrate", "--approve", "--plan", "PLAN-001") == 1
    err = capsys.readouterr().err
    assert "no approved tasks to integrate" in err

    assert guy("integrate", "--dry-run", "--plan", "PLAN-001") == 0
    out = capsys.readouterr().out
    assert "no approved tasks to integrate" in out


def test_conflict_detected_and_default_branch_untouched(guy_env, guy, node_repo, capsys):
    register(guy, node_repo, "r")
    spec = plan_spec([
        mock_task(key="a", title="Claim file", write=[("shared.txt", "version A")]),
        mock_task(key="b", title="Competing claim", write=[("shared.txt", "version B")]),
    ])
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 0
    assert guy("approve", "PLAN-001") == 0
    baseline_head = git(node_repo, "rev-parse", "HEAD").strip()
    # run sequentially to avoid the (intended) path-collision block
    assert guy("run", "--approve", "--task", "TASK-001") == 0
    assert guy("run", "--approve", "--task", "TASK-002") == 0
    assert guy("approve", "TASK-001") == 0
    assert guy("approve", "TASK-002") == 0

    assert guy("integrate", "--approve", "--plan", "PLAN-001") == 1
    out = capsys.readouterr().out
    assert "CONFLICT" in out.upper()
    tasks = _tasks_by_id()
    # first task integrated, conflicting task still approved, nothing forced
    assert tasks["TASK-001"].status == "integrated"
    assert tasks["TASK-002"].status == "approved"
    assert git(node_repo, "rev-parse", "HEAD").strip() == baseline_head
    ib = "guy/plan-PLAN-001-integration"
    merged = git(node_repo, "show", f"{ib}:shared.txt").strip()
    assert merged == "version A"
    # both task worktrees still hold their own versions (nothing discarded)
    assert (Path(tasks["TASK-001"].worktree) / "shared.txt").read_text(encoding="utf-8").strip() == "version A"
    assert (Path(tasks["TASK-002"].worktree) / "shared.txt").read_text(encoding="utf-8").strip() == "version B"


def test_clean_refuses_dirty_worktree(guy_env, guy, py_repo, capsys):
    _setup(guy, py_repo, [mock_task(key="a", write=[("x.txt", "x")])])
    task = _tasks_by_id()["TASK-001"]
    # cancel to reach a cleanable state, but worktree still dirty
    assert guy("cancel", "TASK-001") == 0
    assert guy("clean", "--task", "TASK-001") == 1
    assert "refuses" in capsys.readouterr().err
    assert Path(task.worktree).exists()


def test_clean_after_integration(guy_env, guy, py_repo):
    _setup(guy, py_repo, [mock_task(key="a", write=[("x.txt", "x")])])
    assert guy("approve", "TASK-001") == 0
    assert guy("integrate", "--approve", "--plan", "PLAN-001") == 0
    task = _tasks_by_id()["TASK-001"]
    assert guy("clean", "--task", "TASK-001", "--branch") == 0
    assert not Path(task.worktree).exists()
    # branch deleted safely: its commits are preserved in the integration branch
    out = git(py_repo, "branch", "--list", task.branch)
    assert out.strip() == ""
    ib = "guy/plan-PLAN-001-integration"
    assert git(py_repo, "show", f"{ib}:x.txt").strip() == "x"


def test_reject_needs_review_task(guy_env, guy, py_repo):
    _setup(guy, py_repo, [mock_task(key="a", write=[("x.txt", "x")])])
    assert guy("reject", "TASK-001", "--reason", "not what I asked for") == 0
    task = _tasks_by_id()["TASK-001"]
    assert task.status == "failed"
    assert "not what I asked for" in task.concerns[0] or any(
        "not what I asked for" in c for c in task.concerns)
