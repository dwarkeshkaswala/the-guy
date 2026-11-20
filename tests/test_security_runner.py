"""Security helpers, runner primitives, config precedence."""

from __future__ import annotations

import os
import subprocess
import sys
from pathlib import Path

import pytest


def test_redact_common_secrets():
    from the_guy.security import redact
    text = (
        "key sk-ABCDEF1234567890abcd token ghp_" + "A" * 30 + "\n"
        "aws AKIAIOSFODNN7EXAMPLE\n"
        "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig\n"
        "api_key = hunter2secret\n"
        "postgres://admin:SuperSecret123@db.example.com/x\n"
        "-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----\n"
    )
    out = redact(text)
    assert "sk-ABCDEF1234567890abcd" not in out
    assert "AKIAIOSFODNN7EXAMPLE" not in out
    assert "SuperSecret123" not in out
    assert "PRIVATE KEY-----\nMIIE" not in out
    assert out.count("[REDACTED]") >= 5


def test_redact_leaves_normal_text():
    from the_guy.security import redact
    assert redact("git checkout -b guy/TASK-001-mock-task") == "git checkout -b guy/TASK-001-mock-task"


def test_slug_and_safe_ref():
    from the_guy.security import safe_ref, slug
    assert slug("Implement searchable catalog!!!", 20) == "implement-searchable"
    assert safe_ref("guy/TASK-001 Write A & B") == "guy/TASK-001-Write-A-B"
    assert safe_ref("--force") == "force"
    assert safe_ref("") == "branch"


def test_ensure_within(guy_env):
    from the_guy.security import SecurityError, ensure_within
    parent = Path(os.environ.get("GUY_DATA_DIR", ".")) / "worktrees" / "R-1"
    parent.mkdir(parents=True, exist_ok=True)
    assert ensure_within(parent, parent / "TASK-001") == (parent / "TASK-001").resolve()
    with pytest.raises(SecurityError):
        ensure_within(parent, parent / ".." / ".." / "escape")


def test_display_argv_redacts():
    from the_guy.security import display_argv
    out = display_argv(["claude", "-p", "use api_key=ABCDEF123456 please"])
    assert "ABCDEF123456" not in out
    assert "[REDACTED]" in out


def test_prompt_arg_never_looks_like_flag():
    from the_guy.adapters.base import Adapter
    assert Adapter.prompt_arg("--version") == " --version"
    assert Adapter.prompt_arg("normal prompt") == "normal prompt"


def test_capped_log(guy_env):
    from the_guy.runner import CappedLog
    log_path = Path(os.environ["GUY_DATA_DIR"]) / "cap.log"
    log = CappedLog(log_path, max_bytes=5000).open()
    for i in range(200):
        log.write(f"line {i} " + "y" * 100 + "\n")
    log.close()
    content = log_path.read_text(encoding="utf-8", errors="replace")
    assert log_path.stat().st_size < 8000
    assert "log truncated" in content
    assert "line 199" in content  # tail kept


def test_pid_alive():
    from the_guy.runner import pid_alive
    assert pid_alive(os.getpid()) is True
    proc = subprocess.Popen([sys.executable, "-c", "print(1)"])
    proc.wait()
    assert proc.returncode == 0
    assert pid_alive(proc.pid) is False  # exited subprocess
    assert pid_alive(None) is False


def test_yaml_subset_and_precedence(guy_env, tmp_path, monkeypatch):
    from the_guy import config
    repo = tmp_path / "cfg-repo"
    (repo / ".guy").mkdir(parents=True)
    (repo / ".guy" / "project.yaml").write_text(
        "# repo config\nmax_workers: 3\ndefault_agent: mock\n",
        encoding="utf-8")
    merged = config.effective_settings(cli_overrides={"max_workers": 4}, repo_path=repo)
    assert merged["max_workers"] == 4  # CLI wins
    merged2 = config.effective_settings(repo_path=repo)
    assert merged2["max_workers"] == 3  # repo config beats defaults
    assert merged2["default_agent"] == "mock"

    # lists and nesting parse in the documented subset
    (repo / ".guy" / "project.yaml").write_text(
        "default_agent: mock\nsafe_retry_failure_classes:\n  - spawn_error\n  - timeout\n",
        encoding="utf-8")
    assert config.repo_settings(repo)["safe_retry_failure_classes"] == ["spawn_error", "timeout"]

    with pytest.raises(config.ConfigError):
        config.load_yaml_subset("just a bare line with no colon")


def test_user_prefs_override_builtin(guy_env):
    from the_guy import config
    config.set_user_setting("max_workers", 5)
    assert config.user_settings()["max_workers"] == 5
    assert config.effective_settings()["run_timeout_seconds"] == 1800


def test_store_json_roundtrip(guy_env):
    from the_guy.models import Task, utcnow
    from the_guy.store import Store
    from the_guy import config
    store = Store(config.db_path())
    try:
        task = Task(id="TASK-001", plan_id="PLAN-001", repo_id="R-1", title="t",
                    objective="o", agent="mock", dependencies=["TASK-000"],
                    required_tests=[["python", "-c", "pass"]],
                    options={"mock": {"write": [{"path": "x", "content": "ünïcode"}]}},
                    created_at=utcnow())
        store.insert_task(task)
        loaded = store.get_task("TASK-001")
        assert loaded.dependencies == ["TASK-000"]
        assert loaded.required_tests == [["python", "-c", "pass"]]
        assert loaded.options["mock"]["write"][0]["content"] == "ünïcode"
        assert loaded.read_only is False
        # next_id sequences are stable and zero-padded
        assert store.next_id("PLAN") == "PLAN-001"
    finally:
        store.close()


def test_git_default_branch_detection(guy_env, tmp_path):
    from conftest import make_repo
    from the_guy import git_ops
    repo = make_repo(tmp_path / "b1", branch="release-1.0")
    branch, method = git_ops.default_branch(repo)
    assert branch == "release-1.0"
    assert method == "current branch"


def test_merge_tree_available():
    from the_guy import git_ops
    assert git_ops.supports_merge_tree() is True  # git 2.45 on this machine
