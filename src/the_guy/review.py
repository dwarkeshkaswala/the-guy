"""Review, handoffs, test evidence, and gated integration.

`approve` is never synonymous with commit/merge/push. Integration targets a
separate integration branch; the default branch is never modified. Commits of
agent changes happen only inside `guy integrate --approve` (a separate,
explicitly approved step) and are recorded in the audit trail.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Dict, List, Optional, Tuple

from the_guy import config, events, git_ops, runner, security
from the_guy.models import Plan, Repo, Task, utcnow
from the_guy.store import Store


class ReviewError(Exception):
    pass


def integration_ref(plan: Plan, repo: Repo) -> Tuple[str, Path]:
    branch = f"guy/plan-{plan.id}-integration"
    worktree = config.worktrees_root() / repo.id / f"plan-{plan.id}"
    return branch, worktree


def build_handoff(store: Store, repo: Repo, task: Task) -> dict:
    runs = store.runs_for_task(task.id)
    last_run = runs[-1] if runs else None
    concerns = list(task.concerns)
    concerns.append("A successful agent exit code is not proof that the code is correct.")
    if not task.read_only and task.worktree and git_ops.is_dirty(Path(task.worktree)):
        concerns.append(
            "Worktree contains uncommitted changes; `guy integrate --approve` will commit them "
            "on the task branch before merging."
        )
    if task.required_tests and not task.test_results:
        concerns.append("Required tests are defined but have not been run "
                        "(`guy review --run-tests <task>`).")
    criteria_lines = [f"  - {c}" for c in task.acceptance_criteria] or [
        "  (none defined — consider defining some)"]
    needs_human = [
        "Review the diff and changed files.",
        "Verify each acceptance criterion:",
        *criteria_lines,
        "Decide: `guy approve <task>` or `guy reject <task> --reason ...`.",
    ]
    return {
        "task_id": task.id,
        "title": task.title,
        "repo": {"alias": repo.alias, "path": repo.path},
        "branch": task.branch,
        "worktree": task.worktree,
        "baseline": {"branch": task.baseline_branch, "commit": task.baseline_commit},
        "changed_files": task.changed_files,
        "commits": task.commits,
        "agent": task.agent,
        "exit_code": task.exit_code,
        "exit_reason": task.exit_reason,
        "required_tests": task.required_tests,
        "test_results": task.test_results,
        "acceptance_criteria": task.acceptance_criteria,
        "concerns": concerns,
        "dependencies": task.dependencies,
        "needs_human_judgment": needs_human,
        "last_run": {"id": last_run.id, "log_path": last_run.log_path} if last_run else None,
        "generated_at": utcnow(),
    }


def render_handoff_md(h: dict) -> str:
    lines = [
        f"# Handoff: {h['task_id']} — {h['title']}",
        "",
        f"- Repo: {h['repo']['alias']} ({h['repo']['path']})",
        f"- Branch: {h['branch'] or '(read-only task, no branch)'}",
        f"- Worktree: {h['worktree'] or '(read-only task, ran in repo root)'}",
        f"- Baseline: {h['baseline']['branch']} @ {h['baseline']['commit'][:10]}",
        f"- Agent: {h['agent']} (exit {h['exit_code']}; {h['exit_reason']})",
        "",
        "## Changed files",
    ]
    lines.extend(f"- {f}" for f in h["changed_files"]) or lines.append("- (none)")
    if h["commits"]:
        lines += ["", "## Commits on task branch"]
        lines.extend(f"- {c}" for c in h["commits"])
    lines += ["", "## Tests"]
    if h["test_results"]:
        for r in h["test_results"]:
            status = "PASS" if r.get("ok") else "FAIL"
            lines.append(f"- [{status}] {r.get('command')} (exit {r.get('exit_code')})")
    else:
        lines.append("- No test results recorded yet.")
    lines += ["", "## Concerns"]
    lines.extend(f"- {c}" for c in h["concerns"])
    lines += ["", "## Needs human judgment"]
    lines.extend(f"- {n}" for n in h["needs_human_judgment"])
    return "\n".join(lines)


def collect(store: Store, repo: Repo, task: Task) -> dict:
    payload: Dict = {"task": task.id, "status": task.status, "title": task.title}
    if task.worktree and Path(task.worktree).exists():
        wt = Path(task.worktree)
        dirty = [p for _, p in git_ops.status_porcelain(wt)]
        diffed = git_ops.diff_name_only(wt, task.baseline_commit)
        payload["changed_files"] = sorted(set(dirty) | set(diffed))
        payload["dirty_in_worktree"] = dirty
        payload["commits"] = git_ops.commits_between(wt, task.baseline_commit)
        try:
            payload["diff_stat"] = git_ops.diff_stat(wt, task.baseline_commit) or "(no diff)"
        except git_ops.GitError as exc:
            payload["diff_stat"] = f"(diff unavailable: {exc})"
    else:
        payload["changed_files"] = list(task.changed_files)
        payload["commits"] = list(task.commits)
        payload["diff_stat"] = "(worktree not present; showing recorded data)"
    runs = store.runs_for_task(task.id)
    payload["runs"] = [
        {"id": r.id, "exit_code": r.exit_code, "failure_class": r.failure_class,
         "log_path": r.log_path, "duration_ms": r.duration_ms}
        for r in runs
    ]
    handoff = build_handoff(store, repo, task)
    task.handoff = handoff
    store.update_task(task)
    payload["handoff"] = handoff
    payload["handoff_md"] = render_handoff_md(handoff)
    return payload


def run_tests(store: Store, repo: Repo, task: Task, settings: dict) -> List[dict]:
    """Execute the task's user-defined test commands in its worktree.

    These are commands the user wrote into the plan — never guessed commands.
    This runs code; it is only invoked through the explicit --run-tests flag.
    """
    if not task.required_tests:
        raise ReviewError(f"{task.id} defines no required_tests")
    cwd = Path(task.worktree) if (task.worktree and Path(task.worktree).exists()) else Path(repo.path)
    results = []
    for idx, cmd in enumerate(task.required_tests):
        if not isinstance(cmd, list) or not cmd or not all(isinstance(a, str) for a in cmd):
            results.append({"command": json.dumps(cmd), "ok": False, "exit_code": None,
                            "error": "invalid test command (must be a list of strings)"})
            continue
        log_path = config.logs_root() / repo.id / task.id / f"test-{idx + 1}.log"
        outcome = runner.execute(cmd, cwd, dict(os.environ),
                                 settings.get("run_timeout_seconds"), log_path,
                                 max_log_bytes=int(settings.get("log_max_bytes", 1_000_000)))
        results.append({
            "command": json.dumps(cmd),
            "ok": outcome.exit_code == 0 and not outcome.failure_class,
            "exit_code": outcome.exit_code,
            "failure_class": outcome.failure_class,
            "duration_ms": outcome.duration_ms,
            "log_path": str(log_path),
        })
    task.test_results = results
    store.update_task(task)
    events.log(store, "tests_run", repo_id=repo.id, task_id=task.id,
               results=[{"ok": r["ok"], "command": r["command"]} for r in results])
    return results


def _tests_gate(task: Task) -> dict:
    required = list(task.required_tests)
    if not required:
        return {"required": False, "ok": True, "missing": []}
    passed = {r.get("command") for r in task.test_results if r.get("ok")}
    missing = [json.dumps(c) for c in required if json.dumps(c) not in passed]
    return {"required": True, "ok": not missing, "missing": missing}


def _topo_order(tasks: List[Task]) -> List[Task]:
    by_id = {t.id: t for t in tasks}
    visited: Dict[str, int] = {}
    order: List[Task] = []

    def visit(t: Task):
        state = visited.get(t.id)
        if state == 2:
            return
        if state == 1:
            raise ReviewError(f"dependency cycle at {t.id}")
        visited[t.id] = 1
        for dep in t.dependencies:
            if dep in by_id:
                visit(by_id[dep])
        visited[t.id] = 2
        order.append(t)

    for t in tasks:
        visit(t)
    return order


def integration_preview(store: Store, repo: Repo, plan: Plan, tasks: List[Task],
                        skip_tests: bool = False) -> dict:
    write_tasks = [t for t in tasks if not t.read_only]
    integration_branch, integration_wt = integration_ref(plan, repo)
    actions: List[str] = []
    conflicts: List[dict] = []
    gate_failures: List[str] = []
    pending: List[str] = []
    if not git_ops.branch_exists(Path(repo.path), integration_branch):
        actions.append(
            f"create branch {integration_branch} from {plan.baseline_branch} @ {plan.baseline_commit[:10]} "
            f"and worktree {integration_wt}"
        )
    else:
        actions.append(f"reuse existing integration branch {integration_branch} and worktree {integration_wt}")
    approved_write = [t for t in write_tasks if t.status == "approved"]
    per_task = []
    for task in _topo_order(write_tasks):
        entry = {"task": task.id, "status": task.status, "actions": [], "conflict": None}
        if task.status == "integrated":
            per_task.append(entry)
            continue
        if task.status != "approved":
            pending.append(f"{task.id} ({task.status})")
            entry["actions"].append(f"pending: task is '{task.status}'; not integrated in this step")
            per_task.append(entry)
            continue
        if not task.worktree or not task.branch:
            gate_failures.append(f"{task.id} has no worktree/branch — run it first")
            per_task.append(entry)
            continue
        if git_ops.is_dirty(Path(task.worktree)):
            entry["actions"].append(
                f"commit uncommitted changes on {task.branch} (commit message: "
                f"\"guy: commit task {task.id} — {task.title}\")"
            )
            actions.append(f"commit {task.id} changes on {task.branch}")
        ours = integration_branch if git_ops.branch_exists(Path(repo.path), integration_branch) else plan.baseline_commit
        clean = git_ops.merge_tree_clean(Path(repo.path), ours, task.branch)
        if clean is False:
            entry["conflict"] = f"predicted merge conflict with {integration_branch}"
            conflicts.append({"task": task.id, "detail": entry["conflict"]})
        elif clean is None:
            entry["actions"].append("conflict pre-check unavailable (git < 2.38); conflict checked at merge time")
        entry["actions"].append(f"merge --no-ff {task.branch} into {integration_branch}")
        actions.append(f"merge {task.branch} into {integration_branch}")
        per_task.append(entry)
        gate = _tests_gate(task)
        if gate["required"] and not gate["ok"] and not skip_tests:
            gate_failures.append(
                f"{task.id} required tests not passing yet: {', '.join(gate['missing'])} "
                "(run `guy review --run-tests` or pass --skip-tests explicitly)"
            )
    if not approved_write and not any(t.read_only and t.status == "approved" for t in tasks):
        gate_failures.append("no approved tasks to integrate (approve tasks with `guy approve <TASK>`)")
    actions.append("NOT performed: any push, any change to the default branch, any deployment")
    return {
        "plan": plan.id,
        "baseline": {"branch": plan.baseline_branch, "commit": plan.baseline_commit},
        "integration_branch": integration_branch,
        "integration_worktree": str(integration_wt),
        "actions": actions,
        "per_task": per_task,
        "pending": pending,
        "predicted_conflicts": conflicts,
        "gate_failures": gate_failures,
        "skip_tests": skip_tests,
        "ready": bool(approved_write) and not gate_failures,
    }


def perform_integration(store: Store, repo: Repo, plan: Plan, tasks: List[Task],
                        skip_tests: bool = False) -> dict:
    preview = integration_preview(store, repo, plan, tasks, skip_tests=skip_tests)
    if preview["gate_failures"]:
        raise ReviewError("integration gates failed:\n  - " + "\n  - ".join(preview["gate_failures"]))
    root = Path(repo.path)
    integration_branch, integration_wt = integration_ref(plan, repo)
    if not integration_wt.exists():
        if git_ops.branch_exists(root, integration_branch):
            git_ops.worktree_add_existing(root, integration_wt, integration_branch)
        else:
            git_ops.worktree_add(root, integration_wt, integration_branch, plan.baseline_commit)
            events.log(store, "integration_branch_created", repo_id=repo.id,
                       branch=integration_branch, baseline=plan.baseline_commit)
    integrated: List[str] = []
    write_ordered = _topo_order([t for t in tasks if not t.read_only])
    for task in write_ordered:
        if task.status == "integrated":
            integrated.append(task.id)
            continue
        if task.status != "approved":
            continue  # pending task: not part of this integration step
        worktree = Path(task.worktree) if task.worktree else None
        if worktree is None or not worktree.exists():
            raise ReviewError(f"{task.id} has no worktree; run it first (`guy run --approve --task {task.id}`)")
        if git_ops.is_dirty(worktree):
            message = f"guy: commit task {task.id} — {task.title}"
            sha = git_ops.commit_all(worktree, message)
            task.commits = list(task.commits) + [f"{sha[:10]} {message}"]
            store.update_task(task)
            events.log(store, "task_changes_committed", repo_id=repo.id, task_id=task.id,
                       commit=sha, note="committed by explicit `guy integrate --approve`")
        has_commits = bool(git_ops.commits_between(worktree, task.baseline_commit))
        if not has_commits:
            task.concerns = list(task.concerns) + ["integration: no changes were produced"]
            task.apply_transition("integrated")
            store.update_task(task)
            events.log(store, "task_integrated", repo_id=repo.id, task_id=task.id,
                       note="no changes to merge")
            integrated.append(task.id)
            continue
        clean = git_ops.merge_tree_clean(root, integration_branch, task.branch)
        if clean is False:
            plan.status = "conflict"
            store.update_plan(plan)
            events.log(store, "integration_conflict", repo_id=repo.id, task_id=task.id,
                       integration_branch=integration_branch, task_branch=task.branch)
            return {
                "status": "conflict",
                "task": task.id,
                "integration_branch": integration_branch,
                "message": (
                    f"Predicted conflict merging {task.branch} into {integration_branch}. "
                    "Nothing was merged for this task; earlier tasks (if any) are already integrated. "
                    f"Resolve manually in {integration_wt} (git merge --abort if needed), "
                    "or fix the task and re-run."
                ),
                "integrated": integrated,
            }
        try:
            git_ops.merge_branch(integration_wt, task.branch,
                                 f"guy: integrate {task.id} ({task.title})")
        except git_ops.GitError:
            files = git_ops.conflict_files(integration_wt)
            git_ops.merge_abort(integration_wt)
            plan.status = "conflict"
            store.update_plan(plan)
            events.log(store, "integration_conflict", repo_id=repo.id, task_id=task.id,
                       files=files)
            return {
                "status": "conflict",
                "task": task.id,
                "conflict_files": files,
                "integration_branch": integration_branch,
                "message": (
                    f"Conflict merging {task.branch} into {integration_branch}. "
                    f"Conflicted files: {', '.join(files) or '(see worktree status)'}. "
                    f"The merge was aborted; resolve manually in {integration_wt} and re-run "
                    "`guy integrate --approve`. The default branch was not touched."
                ),
                "integrated": integrated,
            }
        task.apply_transition("integrated")
        store.update_task(task)
        events.log(store, "task_integrated", repo_id=repo.id, task_id=task.id,
                   branch=task.branch, into=integration_branch)
        integrated.append(task.id)
    for task in tasks:  # read-only tasks integrate trivially once approved
        if task.read_only and task.status == "approved":
            task.apply_transition("integrated")
            store.update_task(task)
            events.log(store, "task_integrated", repo_id=repo.id, task_id=task.id,
                       note="read-only task")
    pending = [f"{t.id} ({t.status})" for t in write_ordered
               if t.status not in ("integrated",)]
    if write_ordered and all(t.status == "integrated" for t in write_ordered):
        plan.status = "integrated"
        store.update_plan(plan)
        events.log(store, "plan_integrated", repo_id=repo.id, plan=plan.id,
                   branch=integration_branch)
    elif plan.status != "conflict":
        plan.status = "active"
        store.update_plan(plan)
    return {
        "status": "ok",
        "integration_branch": integration_branch,
        "integration_worktree": str(integration_wt),
        "integrated": integrated,
        "pending": pending,
        "note": "Nothing was pushed and the default branch was not modified.",
    }
