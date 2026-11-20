"""Planning: schemas, DAG validation, lifecycle transitions, durability."""

from __future__ import annotations

import json
import os
from pathlib import Path

import pytest

from conftest import mock_task, plan_spec, register, write_plan_file


def _plan(guy, repo, tasks, alias="r"):
    register(guy, repo, alias)
    spec = plan_spec(tasks)
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 0


def test_plan_from_prompt_single_draft(guy_env, guy, py_repo, capsys):
    register(guy, py_repo, "py")
    assert guy("plan", "Add a searchable catalog with tests") == 0
    out = capsys.readouterr().out
    assert "DRAFT plan PLAN-001" in out
    assert "TASK-001" in out
    assert "nothing has been executed" in out
    assert guy("tasks", "--json") == 0
    data = json.loads(capsys.readouterr().out)
    assert data["tasks"][0]["status"] == "draft"
    assert data["tasks"][0]["baseline_branch"] == "trunk"


def test_plan_file_cycle_rejected(guy_env, guy, py_repo, capsys):
    register(guy, py_repo, "py")
    spec = plan_spec([
        mock_task(key="a", dependencies=["b"]),
        mock_task(key="b", dependencies=["a"]),
    ])
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 1
    assert "cycle" in capsys.readouterr().err


def test_plan_file_missing_dep_rejected(guy_env, guy, py_repo, capsys):
    register(guy, py_repo, "py")
    spec = plan_spec([mock_task(key="a", dependencies=["ghost"])])
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 1
    assert "unknown task 'ghost'" in capsys.readouterr().err


def test_plan_file_unknown_agent_rejected(guy_env, guy, py_repo, capsys):
    register(guy, py_repo, "py")
    spec = plan_spec([mock_task(key="a", agent="hal9000")])
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 1
    assert "hal9000" in capsys.readouterr().err


def test_plan_file_bad_tests_rejected(guy_env, guy, py_repo, capsys):
    register(guy, py_repo, "py")
    spec = plan_spec([mock_task(key="a", tests=[["python", "-m", "pytest"], "not-a-list"])])
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 1
    assert "required_tests" in capsys.readouterr().err


def test_dag_and_transition_units():
    from the_guy.models import Task, TaskStatus, TransitionError, validate_dag

    order = validate_dag([{"key": "a"}, {"key": "b", "dependencies": ["a"]}])
    assert order == ["a", "b"]
    with pytest.raises(Exception):
        validate_dag([{"key": "a", "dependencies": ["a"]}])

    t = Task(id="TASK-001", plan_id="PLAN-001", repo_id="R-1", title="t",
             objective="o", agent="mock")
    for step in ("approved_for_execution", "queued", "running", "needs_review",
                 "approved", "integrated"):
        t.apply_transition(step)
    assert t.status == "integrated"
    with pytest.raises(TransitionError):
        t.apply_transition("running")

    t2 = Task(id="TASK-002", plan_id="PLAN-001", repo_id="R-1", title="t",
              objective="o", agent="mock", status="running")
    t2.apply_transition("failed")
    t2.apply_transition("queued")  # retry path
    assert t2.status == "queued"

    t3 = Task(id="TASK-003", plan_id="PLAN-001", repo_id="R-1", title="t",
              objective="o", agent="mock", status="needs_review")
    with pytest.raises(TransitionError):
        t3.apply_transition("integrated")  # review approval is mandatory


def test_illegal_run_of_draft_blocked_by_selection(guy_env, guy, py_repo, capsys):
    register(guy, py_repo, "py")
    guy("plan", "something")
    capsys.readouterr()
    assert guy("run", "--approve") == 0  # nothing ready
    assert guy("run", "--approve", "--task", "TASK-001") == 0
    out = capsys.readouterr().out
    assert "no tasks ready" in out or "unknown" in out


def test_approve_plan_and_task_semantics(guy_env, guy, py_repo, capsys):
    _plan(guy, py_repo, [mock_task(key="a"), mock_task(key="b", dependencies=["a"])])
    capsys.readouterr()
    assert guy("approve", "PLAN-001") == 0
    out = capsys.readouterr().out
    assert "approved tasks for execution" in out
    assert "not any merge, commit, or push" in out

    # plan-level approval is idempotent-rejecting when nothing is draft
    assert guy("approve", "PLAN-001") == 1

    # task-level approve from needs_review happens in execution tests


def test_state_survives_restart(guy_env, guy, py_repo):
    register(guy, py_repo, "py")
    spec = plan_spec([mock_task(key="a"), mock_task(key="b", dependencies=["a"])])
    plan_file = write_plan_file(Path(os.environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 0
    assert guy("approve", "PLAN-001") == 0

    from the_guy.store import Store
    from the_guy import config as cfg
    store = Store(cfg.db_path())  # fresh connection = simulated restart
    try:
        tasks = store.list_tasks()
        assert [t.status for t in tasks] == ["approved_for_execution", "approved_for_execution"]
        assert tasks[1].dependencies == [tasks[0].id]
        plan = store.get_plan("PLAN-001")
        assert plan.status == "approved"
        assert len(store.list_approvals()) == 1
    finally:
        store.close()


def test_extract_json_object():
    from the_guy.cli import _extract_json_object
    assert _extract_json_object('noise {"a": 1} trailing') == {"a": 1}
    assert _extract_json_object('{"a": {"b": "c{x}"}}') == {"a": {"b": "c{x}"}}
    assert _extract_json_object("no json here") is None
    assert _extract_json_object('{"broken') is None
