"""Repository registry: registration, alias resolution, read-only inspection."""

from __future__ import annotations

from pathlib import Path
from typing import Any, Dict, List, Optional
from uuid import uuid4

from the_guy import config, events, git_ops
from the_guy.models import Repo, utcnow
from the_guy.store import Store

PROJECT_FILE_HINTS = [
    "package.json", "pyproject.toml", "requirements.txt", "setup.py", "Cargo.toml",
    "go.mod", "pom.xml", "build.gradle", "build.gradle.kts", "composer.json",
    "Gemfile", "mix.exs", "CMakeLists.txt", "Makefile", "deno.json", "bun.lockb",
]
SOLUTION_GLOBS = ["*.sln", "*.csproj"]
AGENT_CONFIG_HINTS = [
    "AGENTS.md", "CLAUDE.md", ".claude", ".github/copilot-instructions.md",
    ".guy", ".codex", ".opencode",
]


class RegistryError(Exception):
    pass


def register_repo(store: Store, path: str, alias: Optional[str] = None) -> Repo:
    p = Path(path).expanduser()
    if not p.exists():
        raise RegistryError(f"path does not exist: {p}")
    root = git_ops.find_root(p)
    if root is None:
        raise RegistryError(f"not a git repository: {p}")
    existing = store.get_repo(str(root))
    if existing:
        raise RegistryError(
            f"repository already registered as '{existing.alias}' ({existing.id})"
        )
    alias = (alias or root.name).strip()
    if not alias:
        raise RegistryError("alias must not be empty")
    if store.get_repo(alias):
        raise RegistryError(f"alias '{alias}' is already in use")
    default_branch, method = git_ops.default_branch(root)
    repo = Repo(
        id=f"R-{uuid4().hex[:8]}",
        path=str(root),
        alias=alias,
        default_branch=default_branch,
        added_at=utcnow(),
    )
    store.upsert_repo(repo)
    events.log(store, "repo_added", repo_id=repo.id,
               path=repo.path, alias=alias, default_branch=default_branch,
               default_branch_method=method)
    return repo


def remove_repo(store: Store, ref: str, purge: bool = False) -> Repo:
    repo = store.get_repo(ref)
    if not repo:
        raise RegistryError(f"no registered repository matches '{ref}'")
    if purge:
        wt_root = config.worktrees_root() / repo.id
        if wt_root.exists():
            dirty = []
            root = git_ops.find_root(repo.path)
            for wt in git_ops.worktree_list(root or repo.path):
                try:
                    wt_path = Path(wt["path"])
                except KeyError:
                    continue
                if config.worktrees_root().resolve() not in wt_path.resolve().parents:
                    continue
                if wt_path.exists() and git_ops.is_dirty(wt_path):
                    dirty.append(str(wt_path))
            if dirty:
                raise RegistryError(
                    "refusing to purge worktrees with uncommitted changes: "
                    + "; ".join(dirty)
                    + " — review them manually, then retry"
                )
            for wt in git_ops.worktree_list(root or repo.path):
                wt_path = Path(wt.get("path", ""))
                if wt_path.exists() and config.worktrees_root().resolve() in wt_path.resolve().parents:
                    git_ops.worktree_remove(root or repo.path, wt_path, force=True)
    store.delete_repo(repo.id)
    if config.default_repo_ref() in (repo.alias, repo.id):
        config.set_default_repo(None)
    events.log(store, "repo_removed", path=repo.path, alias=repo.alias, purged=purge)
    return repo


def resolve_repo(store: Store, ref: Optional[str] = None) -> Repo:
    """Resolve the repo for a command: --repo ref > user default > single repo."""
    if ref:
        repo = store.get_repo(ref)
        if not repo:
            raise RegistryError(f"no registered repository matches '{ref}'")
        return repo
    pref = config.default_repo_ref()
    if pref:
        repo = store.get_repo(pref)
        if not repo:
            raise RegistryError(
                f"default repo '{pref}' is no longer registered; run `guy repo list` and `guy use <alias>`"
            )
        return repo
    repos = store.list_repos()
    if len(repos) == 1:
        return repos[0]
    if not repos:
        raise RegistryError("no repositories registered; run `guy repo add <path> --alias <name>`")
    raise RegistryError(
        "multiple repositories registered; pass --repo <alias|path|id> or set a default with `guy use <alias>` "
        "(candidates: " + ", ".join(r.alias for r in repos) + ")"
    )


def _test_hints(root: Path) -> List[str]:
    hints: List[str] = []
    pkg = root / "package.json"
    if pkg.is_file():
        try:
            import json
            data = json.loads(pkg.read_text(encoding="utf-8", errors="replace"))
            script = (data.get("scripts") or {}).get("test")
            if script:
                hints.append(f"package.json scripts.test: {script}")
        except ValueError:
            hints.append("package.json present (unreadable; scripts not parsed)")
    pyproject = root / "pyproject.toml"
    if pyproject.is_file():
        text = pyproject.read_text(encoding="utf-8", errors="replace")
        if "[tool.pytest" in text or "pytest" in text:
            hints.append("pyproject.toml mentions pytest (e.g. python -m pytest)")
    makefile = root / "Makefile"
    if makefile.is_file() and "test" in makefile.read_text(encoding="utf-8", errors="replace"):
        hints.append("Makefile contains a test target")
    return hints


def inspect_repo(store: Store, repo: Repo) -> Dict[str, Any]:
    """Read-only discovery. Never modifies the repository, never executes discovered commands."""
    root = Path(repo.path)
    if git_ops.find_root(root) != root.resolve():
        return {"repo": repo.alias, "error": f"registered path is no longer a git repository root: {root}"}
    dirty = git_ops.status_porcelain(root)
    project_files = [f for f in PROJECT_FILE_HINTS if (root / f).exists()]
    for pattern in SOLUTION_GLOBS:
        project_files.extend(p.name for p in root.glob(pattern))
    agent_configs = [f for f in AGENT_CONFIG_HINTS if (root / f).exists()]
    plans = store.list_plans(repo.id)
    tasks = store.list_tasks(repo_id=repo.id)
    status_counts: Dict[str, int] = {}
    for t in tasks:
        status_counts[t.status] = status_counts.get(t.status, 0) + 1
    managed_worktrees = []
    wt_root = config.worktrees_root() / repo.id
    for wt in git_ops.worktree_list(root):
        wt_path = Path(wt.get("path", ""))
        if wt_root.resolve() in wt_path.resolve().parents or wt_path.resolve() == wt_root.resolve():
            managed_worktrees.append({"path": str(wt_path), "branch": wt.get("branch")})
    current = git_ops.current_branch(root)
    return {
        "alias": repo.alias,
        "id": repo.id,
        "path": str(root),
        "current_branch": current,
        "default_branch": repo.default_branch,
        "head_commit": git_ops.head_commit(root),
        "dirty": bool(dirty),
        "dirty_files": [p for _, p in dirty[:10]],
        "dirty_file_count": len(dirty),
        "project_files": project_files,
        "test_command_hints": _test_hints(root),
        "agent_config_files": agent_configs,
        "plans_count": len(plans),
        "task_status_counts": status_counts,
        "managed_worktrees": managed_worktrees,
    }
