"""Git plumbing. All invocations use argument arrays (never shell strings).

the-guy never pushes, never rebases user branches, never resets or cleans the
user's working tree, and never writes to the default branch. Worktrees live in
orchestrator-controlled directories.
"""

from __future__ import annotations

import subprocess
from pathlib import Path
from typing import List, Optional, Tuple


class GitError(Exception):
    pass


def git(cwd, *args, check: bool = True, timeout: int = 120):
    argv = ["git", "-C", str(cwd), *[str(a) for a in args]]
    try:
        proc = subprocess.run(
            argv, capture_output=True, timeout=timeout,
        )
    except subprocess.TimeoutExpired as exc:
        raise GitError(f"git {' '.join(args)} timed out") from exc
    except OSError as exc:
        raise GitError(f"failed to run git: {exc}") from exc
    stdout = proc.stdout.decode("utf-8", errors="replace")
    stderr = proc.stderr.decode("utf-8", errors="replace")
    if check and proc.returncode != 0:
        detail = (stderr or stdout).strip()
        raise GitError(f"git {' '.join(str(a) for a in args)} failed (exit {proc.returncode}): {detail}")
    return subprocess.CompletedProcess(argv, proc.returncode, stdout, stderr)


def version() -> Tuple[int, int, int]:
    try:
        proc = subprocess.run(["git", "--version"], capture_output=True, timeout=15)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise GitError(f"git not available: {exc}") from exc
    if proc.returncode != 0:
        raise GitError("git --version failed")
    # parse "git version 2.45.1.windows.1"
    text = proc.stdout.decode("utf-8", errors="replace").strip()
    parts = text.split()[-1].split(".")
    nums = []
    for p in parts:
        if p.isdigit():
            nums.append(int(p))
        else:
            break
    nums += [0] * (3 - len(nums))
    return (nums[0], nums[1], nums[2])


def find_root(path) -> Optional[Path]:
    try:
        proc = git(path, "rev-parse", "--show-toplevel", check=False)
    except GitError:
        return None
    if proc.returncode != 0:
        return None
    return Path(proc.stdout.strip())


def current_branch(root) -> Optional[str]:
    proc = git(root, "symbolic-ref", "--short", "HEAD", check=False)
    if proc.returncode != 0:
        return None
    return proc.stdout.strip() or None


def rev_parse(root, ref: str) -> str:
    proc = git(root, "rev-parse", "--verify", f"{ref}^{{commit}}")
    return proc.stdout.strip()


def branch_exists(root, name: str) -> bool:
    proc = git(root, "rev-parse", "--verify", "--quiet", f"refs/heads/{name}", check=False)
    return proc.returncode == 0


def default_branch(root) -> Tuple[str, str]:
    """Detect the default branch. Returns (branch, method). Never assumes `main`."""
    proc = git(root, "symbolic-ref", "--short", "refs/remotes/origin/HEAD", check=False)
    if proc.returncode == 0 and proc.stdout.strip():
        name = proc.stdout.strip()
        if name.startswith("origin/"):
            name = name[len("origin/"):]
        if name:
            return name, "origin/HEAD"
    proc = git(root, "config", "--get", "init.defaultBranch", check=False)
    if proc.returncode == 0 and proc.stdout.strip() and branch_exists(root, proc.stdout.strip()):
        return proc.stdout.strip(), "init.defaultBranch"
    cur = current_branch(root)
    if cur and branch_exists(root, cur):
        return cur, "current branch"
    for candidate in ("main", "master", "trunk"):
        if branch_exists(root, candidate):
            return candidate, f"existing branch '{candidate}'"
    return ("master", "git fallback")


def is_dirty(root) -> bool:
    return bool(status_porcelain(root))


def status_porcelain(root) -> List[Tuple[str, str]]:
    proc = git(root, "status", "--porcelain")
    entries = []
    for line in proc.stdout.splitlines():
        if not line.strip():
            continue
        xy = line[:2]
        path = line[3:]
        if path.startswith('"') and path.endswith('"'):
            path = path[1:-1]
        entries.append((xy, path))
    return entries


def head_commit(root) -> str:
    return rev_parse(root, "HEAD")


def worktree_add(root, worktree_path, branch: str, base_commit: str) -> None:
    git(root, "worktree", "add", "-b", branch, str(worktree_path), base_commit)


def worktree_add_existing(root, worktree_path, branch: str) -> None:
    git(root, "worktree", "add", str(worktree_path), branch)


def worktree_list(root) -> List[dict]:
    proc = git(root, "worktree", "list", "--porcelain")
    out = []
    current: dict = {}
    for line in proc.stdout.splitlines():
        if not line.strip():
            if current:
                out.append(current)
                current = {}
            continue
        key, _, value = line.partition(" ")
        if key == "worktree":
            current = {"path": value}
        elif key == "branch":
            current["branch"] = value.replace("refs/heads/", "")
        elif key == "HEAD":
            current["head"] = value
        elif key == "detached":
            current["detached"] = True
        elif key == "bare":
            current["bare"] = True
    if current:
        out.append(current)
    return out


def worktree_remove(root, worktree_path, force: bool = False) -> None:
    args = ["worktree", "remove"]
    if force:
        args.append("--force")
    args.append(str(worktree_path))
    git(root, *args)


def untracked_and_modified(worktree) -> List[str]:
    return [path for _, path in status_porcelain(worktree)]


def diff_stat(worktree, baseline: str) -> str:
    proc = git(worktree, "diff", "--stat", baseline)
    return proc.stdout.strip()


def diff_name_only(worktree, baseline: str) -> List[str]:
    proc = git(worktree, "diff", "--name-only", baseline)
    return [l for l in proc.stdout.splitlines() if l.strip()]


def commits_between(worktree, baseline: str, head: str = "HEAD") -> List[str]:
    proc = git(worktree, "log", "--oneline", f"{baseline}..{head}")
    return [l for l in proc.stdout.splitlines() if l.strip()]


def commit_all(worktree, message: str) -> str:
    git(worktree, "add", "-A")
    proc = git(worktree, "commit", "-m", message)
    return head_commit(worktree)


def merge_branch(worktree, branch: str, message: str) -> None:
    git(worktree, "merge", "--no-ff", "-m", message, branch)


def merge_abort(worktree) -> None:
    git(worktree, "merge", "--abort", check=False)


def conflict_files(worktree) -> List[str]:
    files = []
    for xy, path in status_porcelain(worktree):
        if "U" in xy or xy in ("AA", "DD"):
            files.append(path)
    return files


def supports_merge_tree() -> bool:
    v = version()
    return v >= (2, 38, 0)


def merge_tree_clean(root, ours_ref: str, theirs_ref: str) -> Optional[bool]:
    """Predict merge cleanliness. True/False; None if this git can't do it."""
    if not supports_merge_tree():
        return None
    proc = git(root, "merge-tree", "--write-tree", ours_ref, theirs_ref, check=False)
    if proc.returncode == 0:
        return True
    if proc.returncode == 1:
        return False
    raise GitError(f"git merge-tree failed: {(proc.stderr or proc.stdout).strip()}")


def delete_branch(root, branch: str, force: bool = False) -> None:
    git(root, "branch", "-D" if force else "-d", branch)
