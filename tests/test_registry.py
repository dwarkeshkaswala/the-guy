"""Repository registry: multi-repo isolation, branches, dirty trees."""

from __future__ import annotations

import json
from pathlib import Path

from conftest import git, make_repo, mock_task, plan_spec, register, write_plan_file


def test_register_list_use_default(guy_env, guy, py_repo, node_repo, capsys):
    register(guy, py_repo, "py")
    register(guy, node_repo, "node")
    assert guy("repo", "list") == 0
    out = capsys.readouterr().out
    assert "py" in out and "node" in out

    assert guy("use", "py") == 0
    guy("repo", "list")
    assert "(default)" in capsys.readouterr().out

    # `guy use` persists per-user; no shell state is modified (just prefs file)
    prefs = Path(__import__("os").environ["GUY_PREFS_DIR"]) / "prefs.json"
    assert prefs.exists()
    assert json.loads(prefs.read_text())["default_repo"] == "py"


def test_duplicate_registration_refused(guy_env, guy, py_repo, capsys):
    register(guy, py_repo, "py")
    # register a subdirectory of the same repo — must resolve to same root
    sub = py_repo / "src"
    assert guy("repo", "add", str(sub), "--alias", "py2") == 1
    captured = capsys.readouterr()
    assert "already registered" in captured.err


def test_alias_conflict_refused(guy_env, guy, py_repo, node_repo, capsys):
    register(guy, py_repo, "sample")
    assert guy("repo", "add", str(node_repo), "--alias", "sample") == 1
    assert "already in use" in capsys.readouterr().err


def test_default_branch_not_main(guy_env, guy, py_repo, weird_repo, capsys):
    register(guy, py_repo, "py")
    capsys.readouterr()
    assert guy("inspect", "--json") == 0
    info = json.loads(capsys.readouterr().out)
    assert info["default_branch"] == "trunk"

    register(guy, weird_repo, "weird")
    capsys.readouterr()
    assert guy("inspect", "--repo", "weird", "--json") == 0
    info = json.loads(capsys.readouterr().out)
    assert info["default_branch"] == "develop"


def test_dirty_detection_and_preservation(guy_env, guy, weird_repo, capsys):
    register(guy, weird_repo, "weird")
    capsys.readouterr()
    assert guy("inspect", "--repo", "weird", "--json") == 0
    info = json.loads(capsys.readouterr().out)
    assert info["dirty"] is True
    assert "user-dirty.txt" in info["dirty_files"]

    # run a mock task end-to-end; the user's uncommitted file must survive untouched
    spec = plan_spec([mock_task(write=[("out.txt", "x")])])
    plan_file = write_plan_file(Path(__import__("os").environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--repo", "weird", "--file", str(plan_file)) == 0
    assert guy("approve", "PLAN-001") == 0
    assert guy("run", "--repo", "weird", "--approve") == 0
    dirty_file = weird_repo / "user-dirty.txt"
    assert dirty_file.read_text(encoding="utf-8") == "user's uncommitted work\n"
    assert git(weird_repo, "status", "--porcelain").strip().splitlines()[-1].endswith("user-dirty.txt")


def test_no_state_leakage_between_repos(guy_env, guy, py_repo, node_repo, capsys):
    register(guy, py_repo, "py")
    register(guy, node_repo, "node")
    guy("use", "py")
    spec = plan_spec([mock_task(write=[("a.txt", "a")])])
    plan_file = write_plan_file(Path(__import__("os").environ["GUY_DATA_DIR"]).parent, spec)
    assert guy("plan", "--file", str(plan_file)) == 0

    capsys.readouterr()
    assert guy("tasks", "--repo", "node", "--json") == 0
    data = json.loads(capsys.readouterr().out)
    assert data["tasks"] == []

    assert guy("tasks", "--json") == 0  # default repo is py
    data = json.loads(capsys.readouterr().out)
    assert len(data["tasks"]) == 1

    # remove py; node must be fully usable afterwards
    assert guy("repo", "remove", "py") == 0
    capsys.readouterr()
    assert guy("tasks", "--repo", "node", "--json") == 0
    assert json.loads(capsys.readouterr().out)["tasks"] == []


def test_remove_missing_path_reported(guy_env, guy, capsys):
    assert guy("repo", "remove", "nope") == 1
    assert "no registered repository" in capsys.readouterr().err


def test_inspect_unknown_layout(guy_env, guy, weird_repo, capsys):
    register(guy, weird_repo, "weird")
    capsys.readouterr()
    assert guy("inspect", "--repo", "weird", "--json") == 0
    info = json.loads(capsys.readouterr().out)
    assert info["project_files"] == []  # hints only; unfamiliar ecosystems are fine
    assert info["test_command_hints"] == []
