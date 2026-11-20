"""Shared fixtures: isolated the-guy state and disposable git repositories.

All repositories live in tmp_path, use mock agents only, and never touch user
data (GUY_DATA_DIR / GUY_PREFS_DIR are redirected per test).
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path

import pytest

SRC = Path(__file__).resolve().parents[1] / "src"
if str(SRC) not in sys.path:
    sys.path.insert(0, str(SRC))


def git(cwd, *args, check=True):
    proc = subprocess.run(["git", "-C", str(cwd), *args], capture_output=True)
    if check and proc.returncode != 0:
        raise AssertionError(
            f"git {' '.join(args)} failed: {proc.stderr.decode(errors='replace')}"
        )
    return proc.stdout.decode("utf-8", errors="replace")


def make_repo(path: Path, branch: str = "main", files=None, dirty: bool = False) -> Path:
    path.mkdir(parents=True, exist_ok=True)
    git(path, "init", "-b", branch)
    git(path, "config", "user.email", "test@the-guy.local")
    git(path, "config", "user.name", "the-guy tests")
    (path / "README.md").write_text("# test repo\n", encoding="utf-8")
    for name, content in (files or {}).items():
        target = path / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(content, encoding="utf-8")
    git(path, "add", "-A")
    git(path, "commit", "-m", "init")
    if dirty:
        (path / "user-dirty.txt").write_text("user's uncommitted work\n", encoding="utf-8")
    return path


@pytest.fixture
def guy_env(tmp_path, monkeypatch):
    data = tmp_path / "guy-data"
    prefs = tmp_path / "guy-prefs"
    monkeypatch.setenv("GUY_DATA_DIR", str(data))
    monkeypatch.setenv("GUY_PREFS_DIR", str(prefs))
    from the_guy import config
    config.ensure_dirs()
    return data


@pytest.fixture
def hermetic(tmp_path, monkeypatch):
    """Hermetic discovery environment: fake HOME, minimal PATH (git + System32
    only), no APPDATA/LOCALAPPDATA, no adapter overrides, empty VS Code dir.

    Yields tmp_path so tests can place fake CLIs in directories (often with
    spaces in their names) and manipulate discovery inputs freely.
    """
    monkeypatch.setenv("GUY_DATA_DIR", str(tmp_path / "guy-data"))
    monkeypatch.setenv("GUY_PREFS_DIR", str(tmp_path / "guy-prefs"))
    from the_guy import config
    config.ensure_dirs()
    fake_home = tmp_path / "fakehome"
    (fake_home / ".local" / "bin").mkdir(parents=True)
    monkeypatch.setattr(Path, "home", lambda: fake_home)
    monkeypatch.delenv("APPDATA", raising=False)
    monkeypatch.delenv("LOCALAPPDATA", raising=False)

    keep = []
    git_exe = shutil.which("git")
    if git_exe:
        keep.append(str(Path(git_exe).parent))
    if os.name == "nt":
        keep.append(os.path.join(os.environ.get("SystemRoot", r"C:\Windows"), "System32"))
    empty = tmp_path / "path-empty"
    empty.mkdir()
    monkeypatch.setenv("PATH", os.pathsep.join([str(empty)] + keep))

    for var in ("GUY_CLAUDE_PATH", "GUY_OPENCODE_PATH", "GUY_CODEX_PATH",
                "GUY_DROID_PATH", "GUY_VSCODE_EXTENSIONS_DIR"):
        monkeypatch.delenv(var, raising=False)
    ext_dir = tmp_path / "extensions"
    ext_dir.mkdir()
    monkeypatch.setenv("GUY_VSCODE_EXTENSIONS_DIR", str(ext_dir))

    from the_guy import discovery as _discovery
    _discovery.npm_global_prefix.cache_clear()
    yield tmp_path
    _discovery.npm_global_prefix.cache_clear()


def write_fake_cli(directory, name, tokens=("exec",), version="fake-cli 1.2.3", ext=None):
    """Write a fake CLI launcher that answers --version/--help read-only probes.

    Windows default is a real .cmd batch shim; POSIX gets a shell script;
    ext=".exe" writes a non-launchable stub for discovery-only tests.
    """
    directory = Path(directory)
    directory.mkdir(parents=True, exist_ok=True)
    if ext is None:
        ext = ".cmd" if os.name == "nt" else ""
    path = directory / (name + ext)
    help_text = " ".join(list(tokens) + ["prompt"])
    if ext == ".cmd":
        content = "\r\n".join([
            "@echo off",
            f'if "%1"=="--version" echo {version} & exit /b 0',
            f'if "%1"=="--help" echo usage: {name} {help_text} & exit /b 0',
            "echo ok",
            "exit /b 0",
        ]) + "\r\n"
    elif ext == ".exe":
        content = "MZ fake executable (discovery-only stub)"
    else:
        content = (
            "#!/bin/sh\n"
            'case "$1" in\n'
            f'  --version) echo "{version}"; exit 0;;\n'
            f'  --help) echo "usage: {name} {help_text}"; exit 0;;\n'
            "esac\n"
            "echo ok\n"
            "exit 0\n"
        )
    path.write_text(content, encoding="utf-8")
    if os.name != "nt":
        path.chmod(0o755)
    return path


@pytest.fixture
def guy():
    from the_guy import cli

    def run(*args):
        return cli.main([str(a) for a in args])

    return run


@pytest.fixture
def adapter_registry():
    from the_guy import adapters
    saved = dict(adapters._ADAPTERS)
    yield adapters
    adapters._ADAPTERS.clear()
    adapters._ADAPTERS.update(saved)


@pytest.fixture
def py_repo(tmp_path):
    # Python-style layout, default branch deliberately NOT 'main', path has a space
    return make_repo(tmp_path / "py project", branch="trunk",
                     files={"pyproject.toml": "[project]\nname = 'sample'\n",
                            "src/sample/__init__.py": "",
                            "src/sample/app.py": "def main():\n    return 'hi'\n"})


@pytest.fixture
def node_repo(tmp_path):
    # Node-style layout on 'main'
    return make_repo(tmp_path / "node-repo", branch="main",
                     files={"package.json": '{"name": "sample", "scripts": {"test": "node test.js"}}',
                            "test.js": "console.log('ok');"})


@pytest.fixture
def weird_repo(tmp_path):
    # Unrecognized layout, 'develop' branch, dirty working tree
    return make_repo(tmp_path / "weird stuff", branch="develop",
                     files={"notes/ideas.txt": "hello"}, dirty=True)


def register(guy, repo_path, alias):
    assert guy("repo", "add", str(repo_path), "--alias", alias) == 0


def plan_spec(tasks):
    return {"prompt": "test plan", "tasks": tasks}


def mock_task(key="t1", title="Mock task", objective="Do the thing.",
              write=None, dependencies=None, tests=None, agent="mock",
              read_only=False, allowed_paths=None, timeout=60, exit=None,
              sleep=None, extra=None):
    task = {"key": key, "title": title, "objective": objective, "agent": agent,
            "timeout_seconds": timeout}
    if dependencies:
        task["dependencies"] = dependencies
    if read_only:
        task["read_only"] = True
    if allowed_paths is not None:
        task["allowed_paths"] = allowed_paths
    if tests:
        task["required_tests"] = tests
    spec = {}
    if write is not None:
        spec["write"] = [{"path": w[0], "content": w[1]} for w in write]
    if exit is not None:
        spec["exit"] = exit
    if sleep is not None:
        spec["sleep"] = sleep
    if extra:
        spec.update(extra)
    if spec:
        task["options"] = {"mock": spec}
    return task


def write_plan_file(tmp_path, spec) -> Path:
    path = tmp_path / "plan.json"
    path.write_text(json.dumps(spec, ensure_ascii=False), encoding="utf-8")
    return path


def task_ids_from(capsys) -> dict:
    import the_guy.store as store_mod
    from the_guy import config
    store = store_mod.Store(config.db_path())
    try:
        return {t.id: t for t in store.list_tasks()}
    finally:
        store.close()
