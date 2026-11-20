"""CLI entry points, doctor, agents, demo."""

from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path

import pytest

from conftest import SRC, make_repo, register


def test_help_in_process(capsys):
    from the_guy import cli
    with pytest.raises(SystemExit) as exc:
        cli.main(["--help"])
    assert exc.value.code == 0
    out = capsys.readouterr().out
    assert "the-guy" in out and "doctor" in out


def test_module_entrypoint(guy_env):
    proc = subprocess.run([sys.executable, "-m", "the_guy", "--help"],
                          capture_output=True, env=None)
    assert proc.returncode == 0
    assert b"the-guy" in proc.stdout


def test_console_script_installed(guy_env):
    script = Path(sys.executable).parent / ("guy.exe" if sys.platform == "win32" else "guy")
    if not script.exists():
        script = Path(sys.executable).parent / "guy"
    assert script.exists(), f"console script missing next to {sys.executable}"
    proc = subprocess.run([str(script), "--version"], capture_output=True)
    assert proc.returncode == 0
    assert b"the-guy" in proc.stdout


def test_doctor_healthy(guy_env, guy, capsys):
    assert guy("doctor") == 0
    out = capsys.readouterr().out
    assert "[OK  ] git:" in out
    assert "agent:mock" in out
    assert "state db" in out


def test_doctor_repo_specific(guy_env, guy, py_repo, capsys):
    register(guy, py_repo, "py")
    assert guy("doctor", "--repo", "py") == 0
    out = capsys.readouterr().out
    assert "default branch: trunk" in out or "trunk" in out


def test_agents_json(guy_env, guy, capsys):
    assert guy("agents", "--json") == 0
    data = json.loads(capsys.readouterr().out)
    by_name = {a["adapter"]: a for a in data["adapters"]}
    assert by_name["mock"]["headless_verified"] is True
    assert by_name["mock"]["kind"] == "mock"
    for name in ("claude", "opencode", "codex", "droid"):
        assert name in by_name  # present and probed, whatever the local truth is
        # real adapters must never claim headless without verified --help evidence
        if not by_name[name]["installed"]:
            assert by_name[name]["headless_verified"] is False


def test_version_flag(guy_env, guy, capsys):
    with pytest.raises(SystemExit) as exc:
        guy("--version")
    assert exc.value.code == 0


def test_unknown_command(guy_env, guy):
    import pytest as _pytest
    with _pytest.raises(SystemExit) as exc:
        guy("frobnicate")
    assert exc.value.code == 2


def test_demo_end_to_end(guy_env, guy, capsys):
    assert guy("demo") == 0
    out = capsys.readouterr().out
    assert "demo complete" in out
    assert "unchanged: True" in out
    assert "blocked" in out  # task B waits for A's integration


def test_demo_keep_then_rerun(guy_env, guy, tmp_path):
    assert guy("demo", "--keep") == 0
    # rerunning must clean up the previous demo repo and succeed again
    assert guy("demo") == 0
