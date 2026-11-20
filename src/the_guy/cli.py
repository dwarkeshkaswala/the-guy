"""the-guy CLI.

One guy. Every agent. Any repo.

Natural-language planning (`guy plan "..."`) produces a DRAFT plan requiring
human review — it is never permission to alter code. Write-capable work only
happens behind `--approve` flags, in isolated Git worktrees, and integration
never touches the default branch or pushes anywhere.
"""

from __future__ import annotations

import argparse
import json as jsonlib
import os
import shutil
import sys
import time
from pathlib import Path
from typing import Any, Dict, List, Optional, Tuple

from the_guy import __version__, adapters, config, events, git_ops, registry, review, runner
from the_guy import scheduler as scheduler_mod
from the_guy import security
from the_guy.models import (ApprovalRecord, Plan, PlanError, Task, TransitionError,
                            validate_dag, utcnow)
from the_guy.registry import RegistryError
from the_guy.store import Store

class CommandError(Exception):
    """User-facing command validation error."""


ERRORS = (CommandError, RegistryError, PlanError, TransitionError, git_ops.GitError,
          adapters.AdapterUnavailable, scheduler_mod.SchedulingError,
          review.ReviewError, config.ConfigError, security.SecurityError)

TERMINAL = ("integrated", "cancelled")


def _emit(args, payload: dict, text: str) -> None:
    if getattr(args, "json", False):
        print(jsonlib.dumps(payload, ensure_ascii=False, indent=2, default=str))
    else:
        print(text)


def _ok(args, message: str, payload: Optional[dict] = None) -> int:
    _emit(args, payload or {"ok": True}, message)
    return 0


# --------------------------------------------------------------------------
# planning helpers (shared by `guy plan` and `guy demo`)

def _normalize_task_spec(index: int, t: dict, default_agent: str) -> dict:
    if not isinstance(t, dict):
        raise PlanError(f"task #{index + 1} must be an object")
    key = str(t.get("key") or t.get("id") or "").strip()
    title = str(t.get("title") or "").strip()
    if not key:
        raise PlanError(f"task #{index + 1} is missing 'key'")
    if not title:
        raise PlanError(f"task '{key}' is missing 'title'")
    deps = t.get("dependencies") or []
    if not isinstance(deps, list) or not all(isinstance(d, str) for d in deps):
        raise PlanError(f"task '{key}': dependencies must be a list of task keys")
    tests = t.get("required_tests") or []
    if not isinstance(tests, list) or any(
            not isinstance(c, list) or not c or not all(isinstance(a, str) for a in c)
            for c in tests):
        raise PlanError(
            f"task '{key}': required_tests must be a list of argv lists, "
            f'e.g. [["python", "-m", "pytest", "-q"]]'
        )
    allowed = t.get("allowed_paths") or []
    if not isinstance(allowed, list) or not all(isinstance(a, str) for a in allowed):
        raise PlanError(f"task '{key}': allowed_paths must be a list of strings")
    agent = str(t.get("agent") or default_agent)
    try:
        adapters.get(agent)
    except adapters.AdapterUnavailable as exc:
        raise PlanError(f"task '{key}': {exc}")
    timeout = t.get("timeout_seconds")
    if timeout is not None:
        try:
            timeout = int(timeout)
        except (TypeError, ValueError):
            raise PlanError(f"task '{key}': timeout_seconds must be an integer")
    options = t.get("options") or {}
    if not isinstance(options, dict):
        raise PlanError(f"task '{key}': options must be an object")
    return {
        "key": key,
        "title": title,
        "objective": str(t.get("objective") or title),
        "agent": agent,
        "dependencies": [str(d) for d in deps],
        "read_only": bool(t.get("read_only", False)),
        "allowed_paths": [str(a) for a in allowed],
        "acceptance_criteria": [str(c) for c in (t.get("acceptance_criteria") or [])],
        "required_tests": tests,
        "timeout_seconds": timeout,
        "max_retries": int(t.get("max_retries", 0) or 0),
        "options": options,
    }


def _plan_from_spec(store: Store, repo, spec: dict, base_branch: Optional[str] = None,
                    default_agent: Optional[str] = None) -> Tuple[Plan, List[Task]]:
    if not isinstance(spec, dict):
        raise PlanError("plan spec must be a JSON object")
    raw_tasks = spec.get("tasks")
    if not isinstance(raw_tasks, list) or not raw_tasks:
        raise PlanError("plan spec must contain a non-empty 'tasks' list")
    normalized = [_normalize_task_spec(i, t, default_agent or "mock")
                  for i, t in enumerate(raw_tasks)]
    validate_dag(normalized)
    base = base_branch or spec.get("base_branch") or repo.default_branch
    baseline_commit = git_ops.rev_parse(Path(repo.path), base)
    plan = Plan(id=store.next_id("PLAN"), repo_id=repo.id,
                prompt=str(spec.get("prompt") or ""), baseline_branch=base,
                baseline_commit=baseline_commit, status="draft", created_at=utcnow())
    store.insert_plan(plan)
    id_by_key = {t["key"]: store.next_id("TASK") for t in normalized}
    tasks: List[Task] = []
    for t in normalized:
        task = Task(
            id=id_by_key[t["key"]], plan_id=plan.id, repo_id=repo.id,
            title=t["title"], objective=t["objective"], agent=t["agent"],
            dependencies=[id_by_key[d] for d in t["dependencies"]],
            read_only=t["read_only"], allowed_paths=t["allowed_paths"],
            acceptance_criteria=t["acceptance_criteria"],
            required_tests=t["required_tests"], timeout_seconds=t["timeout_seconds"],
            max_retries=t["max_retries"], options=t["options"],
            baseline_branch=base, baseline_commit=baseline_commit,
            created_at=utcnow(),
        )
        store.insert_task(task)
        tasks.append(task)
    events.log(store, "plan_created", repo_id=repo.id, plan=plan.id,
               tasks=len(tasks), baseline=f"{base}@{baseline_commit[:10]}")
    return plan, tasks


def _default_agent_name(cli_agent: Optional[str], settings: dict) -> Tuple[str, Optional[str]]:
    if cli_agent:
        adapters.get(cli_agent)  # raises if unknown
        return cli_agent, None
    configured = settings.get("default_agent")
    if configured:
        return str(configured), None
    for name in ("claude", "codex", "opencode", "droid"):
        try:
            report = adapters.get(name).probe()
        except adapters.AdapterUnavailable:
            continue
        if report.installed and report.headless_verified:
            return name, None
    return "mock", ("no verified real agent CLI found on PATH; plan defaults to the built-in "
                    "mock agent (deterministic, no model calls)")


PLANNER_INSTRUCTIONS = """You are a software planning assistant. Analyze this repository and produce a task plan for the requested goal.

Repository facts (read-only discovery):
{facts}

Available agents: {agents}

Output STRICT JSON only, no prose, matching exactly:
{{
  "prompt": "<the goal>",
  "tasks": [
    {{
      "key": "t1",
      "title": "<short title>",
      "objective": "<one-paragraph instruction for the coding agent>",
      "agent": "<one of the available agents>",
      "dependencies": [],
      "read_only": false,
      "allowed_paths": ["<repo-relative path or glob>"],
      "acceptance_criteria": ["<checkable criterion>"],
      "required_tests": [["<executable>", "<arg>"]]
    }}
  ]
}}

Rules:
- At most 6 tasks; keep file ownership non-overlapping between tasks.
- Order dependencies with task keys; no cycles.
- required_tests must be deterministic local commands (they will run in the task worktree).
"""


def _extract_json_object(text: str) -> Optional[dict]:
    start = text.find("{")
    if start < 0:
        return None
    depth = 0
    in_string = False
    escaped = False
    for i in range(start, len(text)):
        ch = text[i]
        if in_string:
            if escaped:
                escaped = False
            elif ch == "\\":
                escaped = True
            elif ch == '"':
                in_string = False
            continue
        if ch == '"':
            in_string = True
        elif ch == "{":
            depth += 1
        elif ch == "}":
            depth -= 1
            if depth == 0:
                candidate = text[start:i + 1]
                try:
                    parsed = jsonlib.loads(candidate)
                except ValueError:
                    return None
                return parsed if isinstance(parsed, dict) else None
    return None


# --------------------------------------------------------------------------
# command handlers

def cmd_doctor(args, store: Store) -> int:
    checks: List[dict] = []

    def add(name: str, status: str, detail: str) -> None:
        checks.append({"name": name, "status": status, "detail": detail})

    add("python", "ok", f"{sys.version.split()[0]} ({sys.executable})")
    try:
        v = git_ops.version()
        level = "ok" if v >= (2, 38, 0) else ("warn" if v >= (2, 17, 0) else "fail")
        detail = f"{v[0]}.{v[1]}.{v[2]}"
        if v < (2, 38, 0):
            detail += " — merge-tree conflict preview requires git >= 2.38 (integration still checks at merge time)"
        if v < (2, 17, 0):
            detail += " — git worktrees require >= 2.17"
        add("git", level, detail)
    except git_ops.GitError as exc:
        add("git", "fail", str(exc))
    for label, directory in (("data dir", config.data_dir()), ("prefs dir", config.prefs_dir())):
        try:
            directory.mkdir(parents=True, exist_ok=True)
            probe = directory / ".guy-write-test"
            probe.write_text("ok", encoding="utf-8")
            probe.unlink()
            add(label, "ok", str(directory))
        except OSError as exc:
            add(label, "fail", f"{directory}: {exc}")
    try:
        add("state db", "ok", f"{config.db_path()} ({len(store.list_repos())} repo(s) registered)")
    except Exception as exc:  # noqa: BLE001
        add("state db", "fail", str(exc))

    reports = adapters.probe_all_cached()
    for report in reports:
        if report.kind == "mock":
            add(f"agent:{report.adapter}", "ok", "built-in mock adapter ready (tests/demo)")
            continue
        if not report.installed:
            add(f"agent:{report.adapter}", "warn", report.headless_note)
        elif report.headless_verified:
            add(f"agent:{report.adapter}", "ok",
                f"{report.version or 'version unknown'} — {report.headless_note}"
                + (f" (source: {report.source})" if report.source else ""))
        else:
            add(f"agent:{report.adapter}", "warn", report.headless_note)

    default = config.default_repo_ref()
    if default:
        repo = store.get_repo(default)
        add("default repo", "ok" if repo else "warn",
            f"{default}" + ("" if repo else " (no longer registered)"))
    else:
        repos = store.list_repos()
        add("default repo", "warn" if len(repos) > 1 else "ok",
            "not set" if repos else "no repos registered yet"
            if not repos else "single repo registered; `guy use` not needed")

    repo_payload = None
    if getattr(args, "repo", None):
        repo = registry.resolve_repo(store, args.repo)
        info = registry.inspect_repo(store, repo)
        repo_payload = info
        add("repo.exists", "ok" if "error" not in info else "fail",
            info.get("error") or info["path"])
        if "error" not in info:
            if info["dirty"]:
                add("repo.dirty", "warn",
                    f"working tree has {info['dirty_file_count']} uncommitted change(s) "
                    "(the-guy will never discard them; worktrees start from committed baselines)")
            else:
                add("repo.dirty", "ok", "clean")
            add("repo.default_branch", "ok", info["default_branch"])

    failed = [c for c in checks if c["status"] == "fail"]
    text = "\n".join(
        f"[{c['status'].upper():4}] {c['name']}: {c['detail']}" for c in checks
    )
    text += f"\n\n{len(failed)} failure(s), the-guy is otherwise operational."
    _emit(args, {"checks": checks, "repo": repo_payload}, text)
    return 1 if failed else 0


def cmd_agents(args, store: Store) -> int:
    cmd = getattr(args, "agents_cmd", None) or "list"
    if cmd == "doctor":
        return _agents_doctor(args)
    if cmd == "set":
        return _agents_set(args)
    if cmd == "clear":
        return _agents_clear(args)
    if cmd == "rescan":
        return _agents_rescan(args)
    return _agents_list(args)


def _agents_list(args) -> int:
    reports = [r.to_dict() for r in adapters.probe_all_cached()]
    lines = ["Adapters (capability-checked against the installed CLIs — never assumed):", ""]
    for r in reports:
        state = ("ready" if r["headless_verified"]
                 else "installed, headless unverified" if r["installed"]
                 else "not installed")
        where = f" @ {r['executable']}" if r.get("executable") else ""
        lines.append(f"  {r['adapter']:<10} {r['display']:<24} [{r['kind']}] {state}{where}")
        lines.append(f"             {r['headless_note']}")
        if r["version"]:
            lines.append(f"             version: {r['version']}")
    lines.append("")
    lines.append("Discovery: override (GUY_<NAME>_PATH / `guy agents set`) > PATH > common install locations.")
    lines.append("Diagnostics: `guy agents doctor`; re-probe after installing: `guy agents rescan`.")
    lines.append("VS Code / Copilot is treated as the human review interface, not a headless worker.")
    _emit(args, {"adapters": reports}, "\n".join(lines))
    return 0


def _agents_doctor(args) -> int:
    name_filter = getattr(args, "adapter", None)
    if name_filter:
        adapters.get(name_filter)  # validate name (raises if unknown)
    reports = [r for r in adapters.probe_all_cached()
               if not name_filter or r.adapter == name_filter]
    cache_path = config.data_dir() / "adapter-cache.json"
    lines = [
        "Agent discovery diagnostics",
        "(read-only checks: --version / --help; nothing is installed or executed)",
        f"probe cache: {cache_path}",
        "",
    ]
    for r in reports:
        d = r.discovery or {}
        lines.append(f"{r.adapter} — {r.display} [{r.kind}]")
        if r.headless_verified:
            verdict = "READY"
        elif r.installed:
            verdict = "INSTALLED but headless UNVERIFIED — disabled"
        else:
            verdict = "DISABLED (no usable executable)"
        lines.append(f"  verdict: {verdict}")
        if r.executable:
            lines.append(f"  executable: {r.executable}"
                         + (f" (source: {r.source})" if r.source else ""))
        if r.version:
            lines.append(f"  version: {r.version}")
        if r.headless_note:
            lines.append(f"  note: {r.headless_note}")
        override = d.get("override")
        if override:
            lines.append(f"  override: {d.get('override_source')} -> {override}")
        elif d.get("env_var"):
            lines.append(f"  override: none (env {d['env_var']} not set; no `guy agents set` on record)")
        wk = d.get("well_known") or []
        if wk and not r.executable:
            lines.append("  common install locations:")
            for w in wk:
                state = f"hit: {w['hit']}" if w.get("hit") else ("present, no match" if w.get("present") else "absent")
                label = f"[{w['label']}] " if w.get("label") else ""
                lines.append(f"    {label}{w['root']}  —  {state}")
        lines.append(f"  PATH directories scanned: {d.get('path_dirs_scanned', 0)}")
        others = [h for h in (d.get("hits") or []) if h["path"] != r.executable]
        if others:
            lines.append("  other candidates seen: " + "; ".join(
                f"{h['path']} ({h['source']}, {h['kind']})" for h in others))
        if r.extension_hint:
            lines.append(f"  VS Code: extension '{r.extension_hint}' found — an extension "
                         "is NOT a global CLI and does not enable this adapter")
        if not r.headless_verified and r.kind == "real":
            lines.append(f"  to enable: {adapters.get(r.adapter).install_hint}")
        lines.append("")
    _emit(args, {"adapters": [r.to_dict() for r in reports]}, "\n".join(lines))
    return 0


def _agents_set(args) -> int:
    adapter = adapters.get(args.adapter)
    if adapter.kind == "mock":
        raise CommandError("the built-in mock agent needs no executable override")
    p = Path(args.path).expanduser()
    if not p.exists():
        raise CommandError(
            f"path does not exist: {p}\n"
            f"find the launcher first (e.g. claude.cmd / claude.exe), then retry. "
            "Discovery also scans PATH and common install locations automatically.")
    config.set_adapter_override(adapter.name, str(p))
    adapters.clear_probe_cache()
    report = adapter.probe()  # live read-only probe of the new override
    state = "verified, adapter ready" if report.headless_verified else (
        "FOUND but NOT verified — adapter stays disabled" if report.installed
        else "NOT usable")
    text = (f"set {adapter.name} -> {p}\n"
            f"  probe: {state}\n"
            f"  executable: {report.executable} (source: {report.source})\n"
            f"  {report.headless_note}")
    if report.extension_hint:
        text += f"\n  note: VS Code extension '{report.extension_hint}' present — irrelevant for CLI discovery"
    return _ok(args, text, {"adapter": adapter.name, "path": str(p),
                            "report": report.to_dict()})


def _agents_clear(args) -> int:
    adapter = adapters.get(args.adapter)
    had = adapter.name in config.adapter_overrides()
    config.set_adapter_override(adapter.name, None)
    adapters.clear_probe_cache()
    report = adapter.probe()  # live probe with auto-discovery restored
    where = f"rediscovered: {report.executable}" if report.executable else "no executable found (auto discovery)"
    return _ok(args, f"cleared override for {adapter.name}\n  {where}",
               {"adapter": adapter.name, "had_override": had,
                "report": report.to_dict()})


def _agents_rescan(args) -> int:
    adapters.clear_probe_cache()
    reports = adapters.probe_all_cached()
    lines = ["adapter rescan (cache cleared, full discovery + read-only capability checks):"]
    for r in reports:
        state = "ready" if r.headless_verified else ("installed, unverified" if r.installed else "not installed")
        where = ""
        if r.executable:
            where = f" @ {r.executable}" + (f" ({r.source})" if r.source else "")
        lines.append(f"  {r.adapter:<10} {state}{where}")
    _emit(args, {"adapters": [r.to_dict() for r in reports]}, "\n".join(lines))
    return 0


def cmd_repo_add(args, store: Store) -> int:
    repo = registry.register_repo(store, args.path, alias=args.alias)
    settings = config.repo_settings(repo.path)
    text = (f"registered repo '{repo.alias}' ({repo.id})\n"
            f"  path: {repo.path}\n"
            f"  default branch: {repo.default_branch} (detected, never assumed)\n"
            f"  state lives outside the repo: {config.data_dir()}")
    if settings:
        text += f"\n  repo config found: {', '.join(sorted(settings))}"
    return _ok(args, text, {"repo": {"id": repo.id, "alias": repo.alias,
                                     "path": repo.path, "default_branch": repo.default_branch}})


def cmd_repo_list(args, store: Store) -> int:
    repos = store.list_repos()
    default = config.default_repo_ref()
    lines = []
    for r in repos:
        marker = " (default)" if default in (r.alias, r.id) else ""
        exists = Path(r.path).exists()
        lines.append(f"  {r.alias}{marker}\n    id: {r.id}  path: {r.path}"
                     f"  default branch: {r.default_branch}"
                     + ("" if exists else "  [PATH MISSING]"))
    text = ("Registered repositories:\n" + "\n".join(lines)) if repos else \
        "No repositories registered. Run: guy repo add <path> --alias <name>"
    _emit(args, {"repos": [r.__dict__ for r in repos], "default": default}, text)
    return 0


def cmd_repo_remove(args, store: Store) -> int:
    repo = registry.remove_repo(store, args.ref, purge=args.purge)
    text = f"removed repo '{repo.alias}' ({repo.id})"
    if args.purge:
        text += " and purged orchestrator-managed worktrees (clean ones only)"
    else:
        text += f"\n  kept: worktrees under {config.worktrees_root() / repo.id} and all logs (audit trail)"
    return _ok(args, text, {"removed": repo.id, "purged": args.purge})


def cmd_use(args, store: Store) -> int:
    if not args.alias:
        current = config.default_repo_ref()
        if not current:
            print("no default repo set; run: guy use <alias>")
            return 0
        repo = store.get_repo(current)
        print(f"default repo: {current}" + (f" -> {repo.path}" if repo else " (not registered anymore)"))
        return 0
    repo = store.get_repo(args.alias)
    if not repo:
        raise RegistryError(f"no registered repository matches '{args.alias}'")
    config.set_default_repo(repo.alias)
    events.log(store, "default_repo_set", repo_id=repo.id, alias=repo.alias)
    return _ok(
        args,
        f"default repo set to '{repo.alias}'\n"
        "  stored per-user in preferences ("
        f"{config.prefs_path()}); it does not modify your shell/terminal state.",
        {"default_repo": repo.alias},
    )


def cmd_inspect(args, store: Store) -> int:
    repo = registry.resolve_repo(store, getattr(args, "repo", None))
    info = registry.inspect_repo(store, repo)
    if "error" in info:
        _emit(args, info, f"error: {info['error']}")
        return 1
    text = (
        f"repo '{info['alias']}' ({info['id']})\n"
        f"  path: {info['path']}\n"
        f"  current branch: {info['current_branch']}  default branch: {info['default_branch']}\n"
        f"  head: {info['head_commit'][:12]}\n"
        f"  working tree: {'DIRTY (' + str(info['dirty_file_count']) + ' change(s), never discarded)' if info['dirty'] else 'clean'}\n"
        f"  project files: {', '.join(info['project_files']) or '(none recognized — that is fine)'}\n"
        f"  test command hints (NOT executed): {('; '.join(info['test_command_hints'])) or 'none found'}\n"
        f"  agent config files present: {', '.join(info['agent_config_files']) or 'none'}\n"
        f"  guy plans: {info['plans_count']}, managed worktrees: {len(info['managed_worktrees'])}"
    )
    _emit(args, info, text)
    return 0


def cmd_plan(args, store: Store) -> int:
    repo = registry.resolve_repo(store, getattr(args, "repo", None))
    settings = config.effective_settings(repo_path=repo.path)
    agent_name, warning = _default_agent_name(args.agent, settings)
    if args.file:
        try:
            spec = jsonlib.loads(Path(args.file).read_text(encoding="utf-8"))
        except (OSError, ValueError) as exc:
            raise PlanError(f"cannot read plan file {args.file}: {exc}")
        plan, tasks = _plan_from_spec(store, repo, spec, base_branch=args.base,
                                      default_agent=agent_name)
        source = f"file {args.file}"
    elif args.planner:
        plan, tasks, source, warning = _plan_with_adapter(args, store, repo, settings)
    else:
        if not args.prompt:
            raise PlanError("provide a prompt, a --file plan.json, or --planner <agent>")
        title = args.prompt.strip().splitlines()[0][:60]
        spec = {"prompt": args.prompt,
                "tasks": [{"key": "t1", "title": title, "objective": args.prompt}]}
        plan, tasks = _plan_from_spec(store, repo, spec, base_branch=args.base,
                                      default_agent=agent_name)
        source = "prompt (single-task draft)"
    lines = [
        f"created DRAFT plan {plan.id} ({source}) — nothing has been executed or modified",
        f"  baseline: {plan.baseline_branch} @ {plan.baseline_commit[:10]}",
        "  tasks:",
    ]
    for t in tasks:
        deps = f"  deps: {', '.join(t.dependencies)}" if t.dependencies else ""
        ro = " [read-only]" if t.read_only else ""
        lines.append(f"    {t.id}: {t.title} (agent: {t.agent}{ro}){deps}")
    lines.append("  next: review with `guy tasks`, then `guy approve " + plan.id + "` (draft → approved_for_execution)")
    if warning:
        lines.append(f"  note: {warning}")
    payload = {"plan": plan.__dict__, "tasks": [t.__dict__ for t in tasks], "warning": warning}
    _emit(args, payload, "\n".join(lines))
    return 0


def _plan_with_adapter(args, store: Store, repo, settings: dict):
    adapter = adapters.get(args.planner)
    adapter.verify_for_execution()
    facts = registry.inspect_repo(store, repo)
    facts_subset = {k: facts.get(k) for k in
                    ("path", "default_branch", "current_branch", "project_files",
                     "test_command_hints", "dirty_file_count", "agent_config_files")}
    prompt = PLANNER_INSTRUCTIONS.format(
        facts=jsonlib.dumps(facts_subset, indent=2, ensure_ascii=False),
        agents=", ".join(n for n in sorted(adapters._ADAPTERS) if n != "mock") + ", mock",
    )
    log_path = config.logs_root() / repo.id / f"planner-{int(time.time())}.log"
    outcome = runner.execute(
        adapter.build_command(prompt, Path(repo.path), {}),
        Path(repo.path), adapter.child_env(dict(os.environ), {}),
        settings.get("planner_timeout_seconds"), log_path,
        max_log_bytes=int(settings.get("log_max_bytes", 1_000_000)),
    )
    if outcome.failure_class or outcome.exit_code != 0:
        raise PlanError(
            f"planner adapter '{args.planner}' failed "
            f"({outcome.failure_class or f'exit {outcome.exit_code}'}); log: {log_path}"
        )
    text = log_path.read_text(encoding="utf-8", errors="replace")
    spec = _extract_json_object(text)
    if spec is None:
        raise PlanError(f"planner output contained no JSON object; log: {log_path}")
    agent_name, _ = _default_agent_name(args.agent, settings)
    plan, tasks = _plan_from_spec(store, repo, spec, base_branch=args.base,
                                  default_agent=agent_name)
    return plan, tasks, f"planner adapter {args.planner} (experimental; output validated)", None


def _task_row(t: Task) -> str:
    deps = ",".join(d.split("-")[-1] for d in t.dependencies) if t.dependencies else "-"
    ro = "R" if t.read_only else "W"
    return f"  {t.id:<10} [{t.status:<24}] {ro} {t.agent:<8} deps:{deps:<8} {t.title}"


def cmd_tasks(args, store: Store) -> int:
    repo = registry.resolve_repo(store, getattr(args, "repo", None))
    tasks = store.list_tasks(repo_id=repo.id, plan_id=args.plan)
    counts: Dict[str, int] = {}
    for t in tasks:
        counts[t.status] = counts.get(t.status, 0) + 1
    lines = [f"tasks for repo '{repo.alias}'"
             + (f", plan {args.plan}" if args.plan else " (all plans)") + ":"]
    if not tasks:
        lines.append("  (none — create one with `guy plan \"...\"`)")
    plans = {p.id: p for p in store.list_plans(repo.id)}
    current_plan = None
    for t in tasks:
        if t.plan_id != current_plan:
            current_plan = t.plan_id
            p = plans.get(current_plan)
            base = f"{p.baseline_branch}@{p.baseline_commit[:8]}" if p else "?"
            lines.append(f"  plan {current_plan} (status: {p.status if p else '?'}, baseline {base})")
        lines.append(_task_row(t))
    lines.append("")
    lines.append("  status counts: " + (", ".join(f"{k}={v}" for k, v in sorted(counts.items())) or "none"))
    payload = {"repo": repo.alias, "counts": counts,
               "tasks": [{k: v for k, v in t.__dict__.items()} for t in tasks]}
    _emit(args, payload, "\n".join(lines))
    return 0


def cmd_show(args, store: Store) -> int:
    task = store.get_task(args.task)
    if not task:
        raise PlanError(f"unknown task '{args.task}'")
    if getattr(args, "repo", None):
        repo = registry.resolve_repo(store, args.repo)
        if task.repo_id != repo.id:
            raise PlanError(f"task {task.id} does not belong to repo '{repo.alias}'")
    payload = {"task": task.__dict__}
    runs = store.runs_for_task(task.id)
    payload["runs"] = [r.__dict__ for r in runs]
    t = task
    lines = [
        f"{t.id}: {t.title}  [{t.status}]",
        f"  plan: {t.plan_id}  repo: {t.repo_id}  agent: {t.agent}"
        + ("  (read-only)" if t.read_only else ""),
        f"  objective: {t.objective[:200]}",
        f"  baseline: {t.baseline_branch}@{t.baseline_commit[:10]}",
        f"  branch: {t.branch}  worktree: {t.worktree}",
        f"  dependencies: {', '.join(t.dependencies) or 'none'}",
        f"  acceptance criteria: {' | '.join(t.acceptance_criteria) or 'none'}",
        f"  required tests: {t.required_tests or 'none'}",
        f"  changed files: {', '.join(t.changed_files) or 'none recorded'}",
        f"  exit: {t.exit_code} ({t.exit_reason})",
        f"  blocked reason: {t.blocked_reason or '-'}",
    ]
    if t.test_results:
        lines.append("  test results:")
        for r in t.test_results:
            lines.append(f"    [{'PASS' if r.get('ok') else 'FAIL'}] {r.get('command')}")
    _emit(args, payload, "\n".join(lines))
    return 0


def _do_approve(store: Store, target: str, note: str = "") -> dict:
    if target.startswith("PLAN-"):
        plan = store.get_plan(target)
        if not plan:
            raise PlanError(f"unknown plan '{target}'")
        tasks = store.list_tasks(plan_id=plan.id)
        drafts = [t for t in tasks if t.status == "draft"]
        if not drafts:
            raise PlanError(f"plan {plan.id} has no draft tasks (statuses: "
                            + ", ".join(sorted({t.status for t in tasks})) + ")")
        for t in drafts:
            t.apply_transition("approved_for_execution", "approved via plan approval")
            store.update_task(t)
            events.log(store, "task_approved_for_execution", repo_id=t.repo_id, task_id=t.id)
        store.add_approval(ApprovalRecord(
            id=store.next_id("APR"), target_kind="plan", target_id=plan.id,
            kind="execute", note=note or "plan approval (draft -> approved_for_execution)",
            created_at=utcnow()))
        plan.status = "approved"
        store.update_plan(plan)
        events.log(store, "plan_approved", repo_id=plan.repo_id, plan=plan.id,
                   tasks=[t.id for t in drafts])
        return {"kind": "plan", "target": plan.id,
                "approved_for_execution": [t.id for t in drafts],
                "note": "approves EXECUTION of these tasks only — not any merge, commit, or push"}
    task = store.get_task(target)
    if not task:
        raise PlanError(f"unknown task '{target}'")
    if task.status == "draft":
        task.apply_transition("approved_for_execution", "explicit approval")
        kind = "execute"
        msg = (f"{task.id}: draft -> approved_for_execution. This approves launching the agent "
               "in an isolated worktree — it is NOT a merge, commit, or push.")
    elif task.status == "needs_review":
        task.apply_transition("approved", "explicit review approval")
        kind = "review"
        msg = (f"{task.id}: needs_review -> approved. This records human review of the changes "
               "— it is NOT a merge, commit, or push. Run `guy integrate --dry-run` next.")
    else:
        raise PlanError(
            f"{task.id} is '{task.status}'; approve applies to draft or needs_review tasks")
    store.update_task(task)
    store.add_approval(ApprovalRecord(
        id=store.next_id("APR"), target_kind="task", target_id=task.id, kind=kind,
        note=note, created_at=utcnow()))
    events.log(store, f"task_{'approved_for_execution' if kind == 'execute' else 'review_approved'}",
               repo_id=task.repo_id, task_id=task.id)
    return {"kind": kind, "target": task.id, "message": msg}


def cmd_approve(args, store: Store) -> int:
    result = _do_approve(store, args.target, note=args.note or "")
    if result["kind"] == "plan":
        text = (f"plan {result['target']}: approved tasks for execution: "
                + ", ".join(result["approved_for_execution"]) + "\n  " + result["note"])
    else:
        text = result["message"]
    return _ok(args, text, result)


def cmd_reject(args, store: Store) -> int:
    task = store.get_task(args.task)
    if not task:
        raise PlanError(f"unknown task '{args.task}'")
    if task.status != "needs_review":
        raise PlanError(f"{task.id} is '{task.status}'; reject applies to needs_review tasks")
    task.apply_transition("failed", args.reason or "rejected in review")
    task.concerns = list(task.concerns) + [f"rejected: {args.reason or 'no reason given'}"]
    task.finished_at = task.finished_at or utcnow()
    store.update_task(task)
    events.log(store, "task_rejected", repo_id=task.repo_id, task_id=task.id,
               reason=args.reason)
    return _ok(args, f"{task.id}: needs_review -> failed (rejected: {args.reason or 'no reason'})",
               {"task": task.id, "status": task.status})


def _select_run_tasks(store: Store, repo, plan_id: Optional[str],
                      task_ids: Optional[List[str]]) -> List[Task]:
    tasks = store.list_tasks(repo_id=repo.id)
    if plan_id:
        tasks = [t for t in tasks if t.plan_id == plan_id]
    if task_ids:
        known = {t.id: t for t in tasks}
        missing = [tid for tid in task_ids if tid not in known]
        if missing:
            raise scheduler_mod.SchedulingError(
                f"unknown task id(s) for this repo: {', '.join(missing)}")
        explicit = set(task_ids)
        tasks = [t for t in tasks if t.id in explicit]
        selectable = [t for t in tasks
                      if t.status in ("approved_for_execution", "queued", "blocked", "failed")]
    else:
        selectable = [t for t in tasks
                      if t.status in ("approved_for_execution", "queued", "blocked")]
    return selectable


def _render_launch(l) -> str:
    pending = f"  pending deps: {', '.join(l.pending_dependencies)}\n" if l.pending_dependencies else ""
    adapter = "" if l.adapter_ok else f"  ADAPTER PROBLEM: {l.adapter_note}\n"
    scope = "read-only (runs in repo root)" if l.read_only else f"worktree: {l.worktree}\n  branch: {l.branch}"
    return (f"  {l.task_id}: {l.title}\n"
            f"  agent: {l.agent}  timeout: {l.timeout_seconds}s  baseline: "
            f"{l.baseline_branch}@{l.baseline_commit[:10]}\n"
            f"  {scope}\n"
            f"  cwd: {l.cwd}\n"
            f"  command: {l.argv_display}\n"
            f"{pending}{adapter}")


def cmd_run(args, store: Store) -> int:
    repo = registry.resolve_repo(store, getattr(args, "repo", None))
    scheduler_mod.reconcile(store)
    settings = config.effective_settings(
        cli_overrides={"max_workers": args.max_workers}, repo_path=repo.path)
    tasks = _select_run_tasks(store, repo, args.plan, args.task)
    if not tasks:
        drafts = [t.id for t in store.list_tasks(repo_id=repo.id)
                  if t.status == "draft" and (not args.plan or t.plan_id == args.plan)]
        hint = (f" — approve first: `guy approve {' '.join(drafts)}`" if drafts else "")
        return _ok(args, f"no tasks ready to run{hint}",
                   {"launched": [], "note": f"no tasks ready{hint}"})
    scheduler = scheduler_mod.Scheduler(store, settings, repo)
    if not args.approve:
        launches = scheduler.dry_run(tasks)
        text = ("DRY RUN — nothing will be launched; no worktrees are created. "
                "Pass --approve to execute.\n\nProposed launches:\n"
                + "\n".join(_render_launch(l) for l in launches))
        return _ok(args, text, {"dry_run": True, "launches": [l.to_dict() for l in launches]})
    summary = scheduler.execute(tasks, settings["max_workers"])
    lines = [f"run complete (max {settings['max_workers']} concurrent workers):"]
    for bucket in ("launched", "needs_review", "failed", "cancelled", "blocked", "retried"):
        for entry in summary.get(bucket, []):
            reason = f" — {entry['reason']}" if entry.get("reason") else ""
            lines.append(f"  [{bucket}] {entry['task']}{reason}")
    failed = bool(summary.get("failed"))
    lines.append("\nnext: `guy status`, `guy logs <TASK>`, `guy review <TASK>` "
                 "(nothing has been committed or merged)")
    _emit(args, summary, "\n".join(lines))
    return 1 if failed else 0


def cmd_status(args, store: Store) -> int:
    repo = registry.resolve_repo(store, getattr(args, "repo", None))
    scheduler_mod.reconcile(store)
    info = registry.inspect_repo(store, repo)
    plans = store.list_plans(repo.id)
    tasks = store.list_tasks(repo_id=repo.id)
    active = [r for r in store.active_runs()]
    lines = [f"status for '{repo.alias}' ({repo.path})"]
    if "error" in info:
        lines.append(f"  ERROR: {info['error']}")
    else:
        lines.append(f"  branch: {info['current_branch']} (default: {info['default_branch']}) "
                     f"{'DIRTY' if info['dirty'] else 'clean'}")
    lines.append(f"  plans: {len(plans)}  tasks: {len(tasks)}")
    for p in plans:
        p_tasks = [t for t in tasks if t.plan_id == p.id]
        counts = {}
        for t in p_tasks:
            counts[t.status] = counts.get(t.status, 0) + 1
        lines.append(f"    {p.id} [{p.status}] baseline {p.baseline_branch}@{p.baseline_commit[:8]}"
                     + ("  " + ", ".join(f"{k}={v}" for k, v in sorted(counts.items())) if counts else ""))
    if active:
        lines.append("  active runs:")
        for r in active:
            lines.append(f"    {r.id} task {r.task_id} adapter {r.adapter} pid {r.pid} ({r.status})")
    else:
        lines.append("  active runs: none")
    wts = info.get("managed_worktrees", []) if "error" not in info else []
    lines.append(f"  managed worktrees: {len(wts)}")
    lines.append("  recent events:")
    for e in store.recent_events(limit=8, repo_id=repo.id):
        lines.append(f"    {e['ts']} {e['kind']} {e.get('task_id') or ''}")
    payload = {"repo": info, "plans": [p.__dict__ for p in plans],
               "tasks": [{k: v for k, v in t.__dict__.items()} for t in tasks],
               "active_runs": [r.__dict__ for r in active]}
    _emit(args, payload, "\n".join(lines))
    return 0


def _tail_text(path: Path, lines: int) -> str:
    try:
        content = path.read_text(encoding="utf-8", errors="replace")
    except OSError:
        return "(log unreadable)"
    all_lines = content.splitlines()
    return "\n".join(all_lines[-lines:]) if len(all_lines) > lines else content


def cmd_logs(args, store: Store) -> int:
    task = store.get_task(args.task)
    if not task:
        raise PlanError(f"unknown task '{args.task}'")
    runs = store.runs_for_task(task.id)
    logs = [Path(r.log_path) for r in runs if r.log_path and Path(r.log_path).exists()]
    if not logs:
        return _ok(args, f"no logs recorded for {task.id} yet", {"task": task.id, "logs": []})
    log = logs[-1]
    body = security.redact(_tail_text(log, args.lines))
    if getattr(args, "json", False):
        _emit(args, {"task": task.id, "log": str(log), "content": body}, "")
        return 0
    print(f"log: {log}")
    print(body)
    if args.follow:
        offset = log.stat().st_size
        quiet = 0
        while quiet < 2:
            time.sleep(0.5)
            fresh = store.get_task(task.id)
            size = log.stat().st_size
            if size > offset:
                with open(log, "rb") as fh:
                    fh.seek(offset)
                    chunk = fh.read().decode("utf-8", errors="replace")
                print(security.redact(chunk), end="")
                offset = size
                quiet = 0
            else:
                quiet += 1
            if fresh.status in TERMINAL and quiet >= 1:
                break
    return 0


def cmd_review(args, store: Store) -> int:
    task = store.get_task(args.task)
    if not task:
        raise PlanError(f"unknown task '{args.task}'")
    repo = store.get_repo(task.repo_id)
    if not repo:
        raise PlanError(f"task {task.id} references unregistered repo {task.repo_id}")
    settings = config.effective_settings(repo_path=repo.path)
    payload: Dict[str, Any] = {}
    if args.run_tests:
        if task.status not in ("needs_review", "failed", "approved"):
            raise review.ReviewError(
                f"--run-tests applies to finished tasks ({task.id} is '{task.status}'); "
                "tests execute code — run them only on work you have reviewed")
        payload["test_results"] = review.run_tests(store, repo, task, settings)
    payload.update(review.collect(store, repo, task))
    text = payload["handoff_md"]
    if payload.get("diff_stat"):
        text += "\n\n## Diff stat (vs baseline)\n" + payload["diff_stat"]
    _emit(args, payload, text)
    return 0


def _plan_for_integrate(store: Store, repo, plan_id: Optional[str]) -> Plan:
    plans = store.list_plans(repo.id)
    if plan_id:
        plan = store.get_plan(plan_id)
        if not plan or plan.repo_id != repo.id:
            raise review.ReviewError(f"plan '{plan_id}' not found in repo '{repo.alias}'")
        return plan
    candidates = [p for p in plans
                  if any(t.status in ("approved", "integrated")
                         for t in store.list_tasks(plan_id=p.id))]
    if len(candidates) == 1:
        return candidates[0]
    if not candidates:
        raise review.ReviewError(
            "no plan with approved tasks; approve tasks first (`guy review`, `guy approve`)")
    raise review.ReviewError(
        "multiple candidate plans; pass --plan: " + ", ".join(p.id for p in candidates))


def cmd_integrate(args, store: Store) -> int:
    repo = registry.resolve_repo(store, getattr(args, "repo", None))
    scheduler_mod.reconcile(store)
    plan = _plan_for_integrate(store, repo, args.plan)
    tasks = store.list_tasks(plan_id=plan.id)
    if args.approve:
        store.add_approval(ApprovalRecord(
            id=store.next_id("APR"), target_kind="plan", target_id=plan.id,
            kind="integration", note="explicit integration approval",
            created_at=utcnow()))
        result = review.perform_integration(store, repo, plan, tasks,
                                            skip_tests=args.skip_tests)
        status = result.get("status")
        text = (f"integration {status.upper()} — plan {plan.id}\n  {result.get('message', '')}"
                if status == "conflict" else
                "integration complete — plan {}\n  integration branch: {}\n  integrated tasks: {}\n  {}"
                .format(plan.id, result["integration_branch"],
                        ", ".join(result["integrated"]), result["note"]))
        _emit(args, result, text)
        return 1 if status == "conflict" else 0
    preview = review.integration_preview(store, repo, plan, tasks,
                                         skip_tests=args.skip_tests)
    text = ("INTEGRATION DRY RUN — nothing will be done. Pass --approve to execute.\n"
            f"plan {plan.id} baseline {preview['baseline']['branch']}"
            f"@{preview['baseline']['commit'][:10]}\n"
            f"integration branch: {preview['integration_branch']}\n"
            "planned actions:\n  " + "\n  ".join(preview["actions"]))
    if preview["predicted_conflicts"]:
        text += "\nPREDICTED CONFLICTS:\n  " + "\n  ".join(
            f"{c['task']}: {c['detail']}" for c in preview["predicted_conflicts"])
    if preview["gate_failures"]:
        text += "\nGATE FAILURES (integration would refuse):\n  - " + "\n  - ".join(
            preview["gate_failures"])
    else:
        text += "\nall gates pass — ready for `guy integrate --approve`"
    _emit(args, preview, text)
    return 0


def _do_cancel(store: Store, task_id: str) -> Task:
    task = store.get_task(task_id)
    if not task:
        raise PlanError(f"unknown task '{task_id}'")
    if task.status == "running":
        active = [r for r in store.runs_for_task(task.id)
                  if r.status in ("running", "starting")]
        for run in active:
            run.status = "cancel-requested"
            store.update_run(run)
            if run.pid:
                runner.kill_tree(run.pid)
        deadline = time.time() + 20
        while time.time() < deadline:
            fresh = store.get_task(task.id)
            if fresh.status != "running":
                events.log(store, "task_cancelled", repo_id=task.repo_id, task_id=task.id)
                return fresh
            time.sleep(0.3)
        task.apply_transition("cancelled", "cancelled by user (runner unresponsive)")
        task.exit_reason = "cancelled"
        for run in active:
            run.status = "cancelled"
            run.failure_class = "cancelled"
            store.update_run(run)
    else:
        task.apply_transition("cancelled", "cancelled by user")
    task.finished_at = task.finished_at or utcnow()
    store.update_task(task)
    events.log(store, "task_cancelled", repo_id=task.repo_id, task_id=task.id)
    return store.get_task(task.id)


def cmd_cancel(args, store: Store) -> int:
    task = _do_cancel(store, args.task)
    return _ok(args, f"{task.id}: cancelled (final status: {task.status})",
               {"task": task.id, "status": task.status})


def _clean_task(store: Store, repo, task: Task, delete_branch: bool) -> List[str]:
    done: List[str] = []
    root = Path(repo.path)
    if task.worktree and Path(task.worktree).exists():
        if git_ops.is_dirty(Path(task.worktree)):
            dirty = [p for _, p in git_ops.status_porcelain(Path(task.worktree))]
            raise review.ReviewError(
                f"{task.id}: worktree has uncommitted changes ({', '.join(dirty[:5])}); "
                "the-guy refuses to delete them — inspect "
                f"{task.worktree} manually or integrate/approve first")
        git_ops.worktree_remove(root, task.worktree)
        done.append(f"removed worktree {task.worktree}")
    if delete_branch and task.branch:
        # -d only accepts branches merged into HEAD; a task branch merged into its
        # integration branch is safe to force-delete (its commits are preserved there).
        ancestor_of_integration = False
        try:
            plan = store.get_plan(task.plan_id)
            if plan:
                ib, _ = review.integration_ref(plan, repo)
                proc = git_ops.git(root, "merge-base", "--is-ancestor", task.branch, ib,
                                   check=False)
                ancestor_of_integration = proc.returncode == 0
        except (git_ops.GitError, Exception):
            ancestor_of_integration = False
        try:
            git_ops.delete_branch(root, task.branch, force=ancestor_of_integration)
            done.append(
                f"deleted branch {task.branch}"
                + (" (commits preserved in the integration branch)"
                   if ancestor_of_integration else " (was fully merged)")
            )
        except git_ops.GitError as exc:
            done.append(f"branch {task.branch} NOT deleted: {exc}")
    events.log(store, "task_cleaned", repo_id=repo.id, task_id=task.id,
               removed=done, branch_deleted=delete_branch)
    return done


def cmd_clean(args, store: Store) -> int:
    actions: List[str] = []
    if args.task:
        task = store.get_task(args.task)
        if not task:
            raise PlanError(f"unknown task '{args.task}'")
        if task.status not in ("failed", "cancelled", "integrated"):
            raise review.ReviewError(
                f"{task.id} is '{task.status}'; only failed/cancelled/integrated tasks can be cleaned")
        repo = store.get_repo(task.repo_id)
        actions += _clean_task(store, repo, task, args.branch)
    elif args.plan:
        plan = store.get_plan(args.plan)
        if not plan:
            raise PlanError(f"unknown plan '{args.plan}'")
        repo = store.get_repo(plan.repo_id)
        for task in store.list_tasks(plan_id=plan.id):
            if task.status in ("failed", "cancelled", "integrated"):
                actions += _clean_task(store, repo, task, args.branch)
        branch, wt = review.integration_ref(plan, repo)
        if wt.exists():
            if git_ops.is_dirty(wt):
                raise review.ReviewError(
                    f"integration worktree {wt} is dirty; refusing to remove it")
            git_ops.worktree_remove(Path(repo.path), wt)
            actions.append(f"removed integration worktree {wt}")
    else:
        raise PlanError("pass --task <ID> or --plan <ID>")
    text = "cleaned:\n  " + "\n  ".join(actions) if actions else "nothing to clean"
    return _ok(args, text, {"actions": actions})


def cmd_config(args, store: Store) -> int:
    if args.config_cmd == "set":
        value: Any = args.value
        if isinstance(value, str):
            if value.lower() in ("true", "false"):
                value = value.lower() == "true"
            else:
                try:
                    value = int(value)
                except ValueError:
                    pass
        config.set_user_setting(args.key, value)
        return _ok(args, f"set {args.key} = {value!r}", {"key": args.key, "value": value})
    if args.config_cmd == "get":
        settings = config.user_settings()
        if args.key not in settings:
            raise config.ConfigError(f"unknown setting {args.key!r}")
        print(settings[args.key])
        return 0
    settings = config.user_settings()
    lines = ["settings (precedence: CLI > repo .guy/project.* > user prefs > defaults):"]
    for key in sorted(settings):
        lines.append(f"  {key} = {settings[key]!r}")
    _emit(args, {"settings": settings}, "\n".join(lines))
    return 0


def cmd_events(args, store: Store) -> int:
    repo_id = None
    if getattr(args, "repo", None):
        repo = store.get_repo(args.repo)
        if repo:
            repo_id = repo.id
    items = events.recent(store, limit=args.limit, repo_id=repo_id)
    lines = [f"{e['ts']}  {e['kind']:<32} {e.get('task_id') or '':<10} "
             f"{jsonlib.dumps(e['payload'], ensure_ascii=False, default=str)}"
             for e in reversed(items)]
    _emit(args, {"events": list(reversed(items))}, "\n".join(lines) or "(no events)")
    return 0


# --------------------------------------------------------------------------
# demo: disposable repo + mock agents, zero model cost

def _rmtree(path: Path) -> None:
    """rmtree that clears Windows read-only bits (git marks object files read-only)."""
    import stat

    def onerror(func, p, _exc_info):
        try:
            os.chmod(p, stat.S_IWRITE)
            func(p)
        except OSError:
            pass

    shutil.rmtree(path, onerror=onerror)


def cmd_demo(args, store: Store) -> int:
    print("the-guy demo — disposable repository, mock agents, zero model cost, nothing pushed")
    demo_base = config.data_dir() / "demo"
    demo_base.mkdir(parents=True, exist_ok=True)
    repo_path = demo_base / "demo repo"  # path contains a space on purpose
    existing = store.get_repo("demo")
    if existing:
        if Path(existing.path).parent == demo_base:
            registry.remove_repo(store, "demo", purge=True)  # previous demo run
        else:
            raise RegistryError(
                "alias 'demo' is used by one of your own repositories; "
                "remove or re-alias it before running the demo")
    if repo_path.exists():
        _rmtree(repo_path)
    repo_path.mkdir(parents=True)

    def step(n: int, title: str, cmd: str) -> None:
        print(f"\n[{n}] {title}\n    $ {cmd}")

    step(1, "create disposable git repository (default branch 'trunk', not 'main')", "git init -b trunk")
    git_ops.git(repo_path, "init", "-b", "trunk")
    git_ops.git(repo_path, "config", "user.email", "demo@the-guy.local")
    git_ops.git(repo_path, "config", "user.name", "the-guy demo")
    (repo_path / "README.md").write_text("# the-guy demo repo\n", encoding="utf-8")
    (repo_path / ".gitignore").write_text(
        "mock-prompt-received.txt\nmock-child.pid\n", encoding="utf-8")
    git_ops.git(repo_path, "add", "-A")
    git_ops.git(repo_path, "commit", "-m", "init")
    baseline_head = git_ops.head_commit(repo_path)

    step(2, "register the repo", f'guy repo add "{repo_path}" --alias demo')
    repo = registry.register_repo(store, str(repo_path), alias="demo")
    print(f"    default branch detected: {repo.default_branch}")

    step(3, "create a draft two-task plan (mock agents, tiny deterministic tests)",
         'guy plan --file plan.json')
    check_file = "from pathlib import Path; assert Path('{name}').read_text(encoding='utf-8') == '{content}'"
    spec = {
        "prompt": "Demo: two tiny features, second depends on the first",
        "tasks": [
            {"key": "a", "title": "Write feature A", "objective": "Create feature-a.txt containing 'alpha'.",
             "agent": "mock", "acceptance_criteria": ["feature-a.txt contains 'alpha'"],
             "required_tests": [[sys.executable, "-c", check_file.format(name="feature-a.txt", content="alpha")]],
             "options": {"mock": {"write": [{"path": "feature-a.txt", "content": "alpha"}]}},
             "timeout_seconds": 60},
            {"key": "b", "title": "Write feature B (depends on A)",
             "objective": "Create feature-b.txt containing 'beta'.",
             "agent": "mock", "dependencies": ["a"],
             "acceptance_criteria": ["feature-b.txt contains 'beta'"],
             "required_tests": [[sys.executable, "-c", check_file.format(name="feature-b.txt", content="beta")]],
             "options": {"mock": {"write": [{"path": "feature-b.txt", "content": "beta"}]}},
             "timeout_seconds": 60},
        ],
    }
    plan, tasks = _plan_from_spec(store, repo, spec)
    print(f"    created {plan.id}: " + ", ".join(f"{t.id}({t.title})" for t in tasks))

    step(4, "approve the plan (draft -> approved_for_execution)", f"guy approve {plan.id}")
    result = _do_approve(store, plan.id)
    print("    approved for execution: " + ", ".join(result["approved_for_execution"]))

    settings = config.effective_settings(repo_path=repo.path)
    scheduler = scheduler_mod.Scheduler(store, settings, repo)

    step(5, "dry-run (never launches anything)", "guy run --dry-run")
    for launch in scheduler.dry_run(_select_run_tasks(store, repo, None, None)):
        print(f"    would run {launch.task_id} ({launch.agent}) in {launch.worktree or 'repo root'}")

    step(6, "execute (task B is blocked until A is integrated)", "guy run --approve")
    summary = scheduler.execute(_select_run_tasks(store, repo, None, None), settings["max_workers"])
    for t in tasks:
        fresh = store.get_task(t.id)
        note = f" — {fresh.blocked_reason}" if fresh.status == "blocked" else ""
        print(f"    {fresh.id}: {fresh.status}{note}")
    task_a, task_b = store.get_task(tasks[0].id), store.get_task(tasks[1].id)

    step(7, "review A, run its tests, approve, integrate",
         f"guy review {task_a.id} --run-tests && guy approve {task_a.id} && guy integrate --approve")
    repo_obj = store.get_repo(task_a.repo_id)
    review.run_tests(store, repo_obj, task_a, settings)
    print("    tests: " + ", ".join(
        f"{'PASS' if r['ok'] else 'FAIL'} {r['command']}" for r in task_a.test_results))
    _do_approve(store, task_a.id)
    preview = review.integration_preview(store, repo_obj, store.get_plan(task_a.plan_id),
                                         store.list_tasks(plan_id=task_a.plan_id))
    print(f"    dry-run gates ready: {preview['ready']}")
    integ = review.perform_integration(store, repo_obj, store.get_plan(task_a.plan_id),
                                       store.list_tasks(plan_id=task_a.plan_id))
    print(f"    integrate: {integ['status']} on {integ['integration_branch']}")

    step(8, "now B is unblocked — execute, review, approve, integrate", "guy run --approve ...")
    scheduler_b = scheduler_mod.Scheduler(store, settings, repo)
    scheduler_b.execute(_select_run_tasks(store, repo, None, [task_b.id]), settings["max_workers"])
    fresh_b = store.get_task(task_b.id)
    review.run_tests(store, repo_obj, fresh_b, settings)
    print(f"    {fresh_b.id}: {fresh_b.status}; tests: "
          + ", ".join(f"{'PASS' if r['ok'] else 'FAIL'}" for r in fresh_b.test_results))
    _do_approve(store, fresh_b.id)
    integ2 = review.perform_integration(store, repo_obj, store.get_plan(fresh_b.plan_id),
                                        store.list_tasks(plan_id=fresh_b.plan_id))
    print(f"    integrate: {integ2['status']} — integrated: {', '.join(integ2['integrated'])}")

    step(9, "verify safety invariants", "git -C <repo> log --oneline trunk")
    final_head = git_ops.head_commit(repo_path)
    safe = final_head == baseline_head
    branch_log = git_ops.git(Path(integ2["integration_worktree"]),
                             "log", "--oneline", "-5").stdout.strip()
    print("\n".join("    " + l for l in branch_log.splitlines()))
    print(f"    default branch '{repo.default_branch}' unchanged: {safe}")
    print("    nothing pushed (the-guy never pushes); agent commits only via integrate --approve")

    if not args.keep:
        print("\n[cleanup] removing demo repo, worktrees, and registration")
        registry.remove_repo(store, repo.alias, purge=True)
        _rmtree(repo_path)
        wt_root = config.worktrees_root() / repo.id
        if wt_root.exists():
            _rmtree(wt_root)
        print("    done (event history kept for audit; `guy events --limit 50` to view)")
    else:
        print(f"\n[--keep] demo repo left at {repo_path} (alias '{repo.alias}')")
    print("\ndemo complete.")
    return 0 if safe else 1


# --------------------------------------------------------------------------
# parser

def build_parser() -> argparse.ArgumentParser:
    json_parent = argparse.ArgumentParser(add_help=False)
    json_parent.add_argument("--json", action="store_true", help="emit machine-readable JSON")

    repo_parent = argparse.ArgumentParser(add_help=False)
    repo_parent.add_argument("--json", action="store_true", help="emit machine-readable JSON")
    repo_parent.add_argument("--repo", metavar="ALIAS|PATH|ID",
                             help="target repo (default: user default or single registered repo)")

    parser = argparse.ArgumentParser(
        prog="guy",
        description="the-guy — One guy. Every agent. Any repo. "
                    "A local-first, repo-agnostic multi-agent orchestrator with human control.")
    parser.add_argument("--version", action="version", version=f"the-guy {__version__}")
    sub = parser.add_subparsers(dest="command", required=True)

    p = sub.add_parser("doctor", parents=[repo_parent], help="environment and agent health check")
    p.set_defaults(handler=cmd_doctor)

    p = sub.add_parser("agents", parents=[json_parent],
                       help="agent adapters: capabilities, discovery diagnostics, configuration")
    agent_sub = p.add_subparsers(dest="agents_cmd")
    lp = agent_sub.add_parser("list", parents=[json_parent],
                              help="list adapters and capabilities (default)")
    lp.set_defaults(agents_cmd="list")
    dp = agent_sub.add_parser("doctor", parents=[json_parent],
                              help="detailed discovery diagnostics (overrides, PATH, locations)")
    dp.add_argument("adapter", nargs="?", help="limit to one adapter")
    dp.set_defaults(agents_cmd="doctor")
    sp = agent_sub.add_parser("set", parents=[json_parent],
                              help="persist an explicit executable path for an adapter")
    sp.add_argument("adapter")
    sp.add_argument("path", help="path to the CLI launcher (.exe/.cmd/.bat or POSIX binary)")
    sp.set_defaults(agents_cmd="set")
    cp = agent_sub.add_parser("clear", parents=[json_parent],
                              help="remove the persisted path override (auto discovery resumes)")
    cp.add_argument("adapter")
    cp.set_defaults(agents_cmd="clear")
    rp = agent_sub.add_parser("rescan", parents=[json_parent],
                              help="clear the probe cache and rediscover all adapters")
    rp.set_defaults(agents_cmd="rescan")
    p.set_defaults(handler=cmd_agents, agents_cmd="list")

    repo = sub.add_parser("repo", help="manage the repository registry")
    repo_sub = repo.add_subparsers(dest="repo_cmd", required=True)
    p = repo_sub.add_parser("add", parents=[json_parent], help="register a git repository")
    p.add_argument("path")
    p.add_argument("--alias")
    p.set_defaults(handler=cmd_repo_add)
    p = repo_sub.add_parser("list", parents=[json_parent], help="list registered repositories")
    p.set_defaults(handler=cmd_repo_list)
    p = repo_sub.add_parser("remove", parents=[json_parent], help="unregister a repository")
    p.add_argument("ref")
    p.add_argument("--purge", action="store_true",
                   help="also remove orchestrator-managed worktrees (clean ones only)")
    p.set_defaults(handler=cmd_repo_remove)

    p = sub.add_parser("use", parents=[json_parent],
                       help="set/show default repo (persisted per-user; does not touch your shell)")
    p.add_argument("alias", nargs="?")
    p.set_defaults(handler=cmd_use)

    p = sub.add_parser("inspect", parents=[repo_parent],
                       help="read-only repository discovery")
    p.set_defaults(handler=cmd_inspect)

    p = sub.add_parser("plan", parents=[repo_parent],
                       help="create a DRAFT plan (never executes or modifies anything)")
    p.add_argument("prompt", nargs="?", default="")
    p.add_argument("--file", help="plan JSON file (see docs/command-reference.md)")
    p.add_argument("--base", help="baseline branch (default: detected default branch)")
    p.add_argument("--agent", help="default agent for tasks")
    p.add_argument("--planner", help="use a read-only planner adapter (experimental; invokes a "
                                     "real CLI and may incur model costs)")
    p.set_defaults(handler=cmd_plan)

    p = sub.add_parser("tasks", parents=[repo_parent], help="list tasks and dependencies")
    p.add_argument("--plan")
    p.set_defaults(handler=cmd_tasks)

    p = sub.add_parser("show", parents=[json_parent], help="full task detail")
    p.add_argument("task", metavar="TASK-ID")
    p.add_argument("--repo", dest="repo_scope", default=None)
    p.set_defaults(handler=lambda a, s: _show_with_repo(a, s))

    p = sub.add_parser("approve", parents=[json_parent],
                       help="approve a draft/plan for execution, or a reviewed task (NOT a merge)")
    p.add_argument("target", metavar="TASK-ID|PLAN-ID")
    p.add_argument("--note", default="")
    p.set_defaults(handler=cmd_approve)

    p = sub.add_parser("reject", parents=[json_parent], help="reject a task in needs_review")
    p.add_argument("task", metavar="TASK-ID")
    p.add_argument("--reason", default="")
    p.set_defaults(handler=cmd_reject)

    p = sub.add_parser("run", parents=[repo_parent], help="launch approved agent tasks")
    mode = p.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true", help="print proposed worktrees and commands; launch nothing")
    mode.add_argument("--approve", action="store_true", help="explicit consent to launch write-capable tasks")
    p.add_argument("--task", action="append", metavar="TASK-ID")
    p.add_argument("--plan")
    p.add_argument("--max-workers", type=int)
    p.set_defaults(handler=cmd_run)

    p = sub.add_parser("status", parents=[repo_parent], help="tasks, branches, logs, running PIDs")
    p.set_defaults(handler=cmd_status)

    p = sub.add_parser("logs", parents=[json_parent], help="show task run logs")
    p.add_argument("task", metavar="TASK-ID")
    p.add_argument("--follow", action="store_true")
    p.add_argument("--lines", type=int, default=40)
    p.set_defaults(handler=cmd_logs)

    p = sub.add_parser("review", parents=[json_parent],
                       help="diff + test evidence + handoff for a task")
    p.add_argument("task", metavar="TASK-ID")
    p.add_argument("--run-tests", action="store_true",
                   help="explicitly run the task's required_tests in its worktree (executes code)")
    p.set_defaults(handler=cmd_review)

    p = sub.add_parser("integrate", parents=[repo_parent],
                       help="merge approved tasks into an integration branch (never the default branch)")
    mode = p.add_mutually_exclusive_group()
    mode.add_argument("--dry-run", action="store_true", help="preview merges and gates; do nothing")
    mode.add_argument("--approve", action="store_true",
                      help="perform integration (may commit task changes on task branches)")
    p.add_argument("--plan")
    p.add_argument("--skip-tests", action="store_true",
                   help="explicitly bypass the required-tests gate")
    p.set_defaults(handler=cmd_integrate)

    p = sub.add_parser("cancel", parents=[json_parent], help="cancel a task and kill its process tree")
    p.add_argument("task", metavar="TASK-ID")
    p.set_defaults(handler=cmd_cancel)

    p = sub.add_parser("clean", parents=[json_parent],
                       help="remove worktrees/branches of finished tasks (refuses dirty worktrees)")
    group = p.add_mutually_exclusive_group(required=True)
    group.add_argument("--task", metavar="TASK-ID")
    group.add_argument("--plan", metavar="PLAN-ID")
    p.add_argument("--branch", action="store_true", help="also delete the task branch (if merged)")
    p.set_defaults(handler=cmd_clean)

    p = sub.add_parser("config", parents=[json_parent], help="view/change user settings")
    config_sub = p.add_subparsers(dest="config_cmd")
    config_sub.add_parser("list").set_defaults(handler_cmd="list")
    gp = config_sub.add_parser("get")
    gp.add_argument("key")
    gp.set_defaults(handler_cmd="get")
    sp = config_sub.add_parser("set")
    sp.add_argument("key")
    sp.add_argument("value")
    sp.set_defaults(handler_cmd="set")
    p.set_defaults(handler=cmd_config, config_cmd="list")

    p = sub.add_parser("events", parents=[json_parent], help="audit trail")
    p.add_argument("--limit", type=int, default=30)
    p.add_argument("--repo")
    p.set_defaults(handler=cmd_events)

    p = sub.add_parser("demo", parents=[json_parent],
                       help="end-to-end demo on a disposable repo with mock agents (zero model cost)")
    p.add_argument("--keep", action="store_true", help="keep the demo repo registered")
    p.set_defaults(handler=cmd_demo)

    return parser


def _show_with_repo(args, store: Store) -> int:
    args.repo = args.repo_scope
    return cmd_show(args, store)


def main(argv=None) -> int:
    for stream in (sys.stdout, sys.stderr):
        try:
            stream.reconfigure(errors="replace")
        except (AttributeError, OSError):
            pass
    parser = build_parser()
    args = parser.parse_args(argv)
    config.ensure_dirs()
    store = Store()
    try:
        rc = args.handler(args, store)
    except ERRORS as exc:
        message = str(exc)
        if getattr(args, "json", False):
            print(jsonlib.dumps({"error": message}, ensure_ascii=False, indent=2))
        else:
            print(f"error: {message}", file=sys.stderr)
        rc = 1
    finally:
        store.close()
    return rc if isinstance(rc, int) else 0


if __name__ == "__main__":
    sys.exit(main())
