# the-guy command reference

Global: most commands accept `--json`; repo-scoped commands accept
`--repo <alias|path|id>` (falls back to the `guy use` default, then to the
single registered repo).

## Environment & adapters

```
guy doctor [--repo X]     # git, python, dirs, db, per-adapter capability report
guy agents                # adapter list: installed, version, headless verified
guy agents doctor [NAME]  # full discovery diagnostics for one or all adapters
guy agents set NAME PATH  # persist an explicit executable path override
guy agents clear NAME     # remove the override; auto-discovery resumes
guy agents rescan         # clear the probe cache and rediscover everything
guy agents smoke [NAME...] [--approve] [--timeout S]
                          # prove each agent can EDIT files headlessly
```

### `guy agents smoke`

`--version`/`--help` probes prove a CLI exists, not that it will change files
when the-guy runs it non-interactively (some CLIs need a permission /
auto-approve option for that). `smoke` runs one tiny real job per agent —
default: every verified real adapter, one at a time — in a throwaway git repo
under `<data>/smoke/`, asking it to create `guy-smoke.txt` with a random token.
Without `--approve` it is a dry run. These are real model calls (credits).

| Verdict | Meaning |
|---|---|
| `ok` | file created with the exact token (other changed files are listed) |
| `no_edit` | exited 0 but created nothing — usually a missing permission/auto-approve option |
| `wrong_content` | file created, content differs |
| `timeout` | no exit — typically waiting for an interactive prompt or login |
| `failed` | non-zero exit (auth, endpoint, rate limit…); last output lines shown |
| `unavailable` | adapter not installed / not headless-verified |

Exit code 1 if any agent is not `ok`. Failing runs keep their repo for
inspection; every run writes a log under `<data>/logs/smoke/` and an
`agent_smoke` event. `guy agents` shows each real agent's latest verdict.

### How discovery works

Per adapter, in strict order (explicit intent wins; **no silent fallback**):

1. **Override** — `GUY_<NAME>_PATH` env var (e.g. `GUY_CLAUDE_PATH`), or the
   persisted config set by `guy agents set claude "C:\path\claude.cmd"`.
2. **PATH scan** (directory order).
3. **Adapter-specific install locations** — Factory Droid:
   `%LOCALAPPDATA%\Factory\app-*\resources\bin\droid.exe`, where `app-*`
   directories are ordered by **numeric version comparison** (1.10.0 beats
   1.9.0, never alphabetical) and a broken newest install falls through to the
   next valid one.
4. **Well-known user install locations** — the global npm prefix (resolved by
   asking `npm config get prefix`, falling back to npm's Windows default
   `%APPDATA%\npm`), pnpm, Yarn, Volta, scoop shims, cargo, `~/.local/bin`,
   `~/bin`, homebrew prefixes.

Upgrades are picked up automatically: Factory `app-*` directory names and the
npm prefix are part of the probe-cache fingerprint, so a new version
invalidates the cache without `guy agents rescan`; a deleted executable is
re-validated on every cached read. Nothing here hardcodes version numbers,
usernames, or absolute install directories — everything derives from
environment variables and npm itself.

Windows shims: `.exe` > `.cmd` > `.bat` are launchable; `.ps1` shims are
*reported but never launched* (CreateProcess can't run them and PowerShell
execution policy may block them) — when only a `.ps1` exists the adapter stays
disabled with that explanation, and a sibling `.cmd`/`.exe` is preferred
automatically when present.

All capability checks are **read-only** (`--version`, `--help`); the
noninteractive command shape (e.g. `claude --print`, `codex exec`,
`opencode run`, `droid exec`) is taken only from actual `--help` evidence.
Unverified adapters stay disabled — the-guy never guesses flags and never
substitutes a different agent. A VS Code extension is only ever a *hint* in
diagnostics; it never counts as an installed CLI.

Probe results are cached with a fingerprint (PATH, PATHEXT, env overrides,
persisted config, adapter set, install roots, the-guy version) plus a short
TTL, so changing any of them triggers automatic rediscovery; `guy agents
rescan` forces it immediately. Execution (`guy run`) always probes live.

## Repositories

```
guy repo add <path> [--alias A]   # verifies git root, detects default branch, refuses duplicates
guy repo list                     # id / alias / path / default branch / default marker
guy repo remove <ref> [--purge]   # unregister; --purge also removes *clean* managed worktrees
guy use [ALIAS]                   # set/show per-user default repo (prefs, not shell state)
guy inspect [--repo X]            # read-only: branches, dirty files, project hints, test hints,
                                  # agent config files, plans/worktrees
```

`guy use` deliberately persists a per-user preference instead of modifying
your terminal session. Detected test commands are hints only — never executed.

## Planning

```
guy plan "objective" [--base BRANCH] [--agent NAME]       # single-task DRAFT
guy plan --file plan.json [--base BRANCH] [--agent NAME]  # full DAG (below)
guy plan "objective" --planner <agent> [--agent NAME]    # agent breaks the goal into
                                                          # a DRAFT DAG (real CLI call;
                                                          # uses that CLI's credits)
```

### `--planner`

The named agent CLI (`claude`, `codex`, `opencode`, `droid`, or `mock`) is
asked to split the goal into at most `planner_max_tasks` tasks with
dependencies, `allowed_paths`, acceptance criteria and test commands. Its
answer is treated as a proposal, never as policy:

- It runs in a **throwaway detached worktree** at the baseline commit, never
  in your working copy. Files it writes there are listed and discarded.
- Your working copy's `HEAD` and `git status` are compared before and after;
  any change is a hard error and no plan is created.
- Only the agent's own output is parsed (not the log header, which echoes the
  prompt); the **last** JSON object with a non-empty `tasks` list wins.
- Agent choices and `options` in the planner's output are ignored — task
  agents come from `--agent`, `default_agent`, or the first verified CLI. The
  goal and baseline are yours, not the planner's.
- The result goes through the same validation as a plan file (keys, DAG,
  test argv). Review notes flag tasks with no `allowed_paths` and tasks whose
  paths overlap without a dependency (they will run one at a time).
- Planner-proposed test commands run only when you call
  `guy review <TASK> --run-tests`.
- Log: `<data>/logs/<repo-id>/planner/`. Events: `planner_started`,
  `planner_finished`, `planner_touched_working_copy`.

Planning changes nothing. A plan records a baseline branch + commit. If the
baseline branch is dirty you are warned — worktrees start from the committed
baseline and your uncommitted work is never touched.

### plan.json schema

```json
{
  "prompt": "optional overall goal",
  "base_branch": "optional; default = detected default branch",
  "tasks": [
    {
      "key": "t1",
      "title": "Add catalog model",
      "objective": "Create src/catalog.py with search(), plus tests.",
      "agent": "claude",
      "dependencies": [],
      "read_only": false,
      "allowed_paths": ["src/**"],
      "acceptance_criteria": ["search() returns matches case-insensitively"],
      "required_tests": [["python", "-m", "pytest", "-q"]],
      "timeout_seconds": 900,
      "max_retries": 0,
      "options": {"mock": {"write": [{"path": "x.txt", "content": "hi"}]}}
    }
  ]
}
```

- `agent` must be a registered adapter (`guy agents`).
- `required_tests` is a list of **argv lists** (no shell strings).
- `options.mock` configures the built-in mock agent (deterministic, free).
- Unknown keys on tasks, cycles, dangling dependencies, unknown agents, and
  malformed tests are rejected at plan time.

## Approving & running

```
guy approve PLAN-001        # all its draft tasks -> approved_for_execution
guy approve TASK-001        # draft -> approved_for_execution, or needs_review -> approved
guy reject TASK-001 --reason "..."   # needs_review -> failed
guy run [--dry-run]         # proposed worktrees + exact sanitized commands; launches nothing
guy run --approve [--task T ...] [--plan P] [--max-workers N]
```

- Bare `guy run` is a dry run. Only `--approve` launches.
- Default concurrency: 2 (all agents may share one provider allowance). `--max-workers` or `max_workers` setting overrides (max 8).
- Write tasks get `worktrees/<repo>/<TASK>/` and branch `guy/<TASK>-<slug>` at
  the baseline commit. Read-only tasks run in the repo root.
- Empty `allowed_paths` means "whole repository" and collides with any other
  concurrent write task on that repo (the later one is `blocked`).
- Failed tasks can be re-run explicitly: `guy run --approve --task TASK-001`.
- Safe-class failures (`spawn_error`) auto-retry up to `max_retries`.

## Monitoring

```
guy status [--repo X]       # tasks by plan, active runs with PIDs, worktrees, recent events
guy tasks [--plan P]        # task table + status counts
guy show TASK-001           # full task detail
guy logs TASK-001 [--follow] [--lines N]
guy events [--limit N] [--repo X]   # audit trail
```

## Review & integration

```
guy review TASK-001                 # diff stat, changed files, handoff (md + json)
guy review TASK-001 --run-tests     # explicitly run required_tests in the worktree
guy integrate [--dry-run] [--plan P] [--skip-tests] [--allow-out-of-scope]
guy integrate --approve [--plan P] [--skip-tests] [--allow-out-of-scope]
```

### Work-product checks (automatic, after every successful write run)

- **No changes**: a write task whose agent exited 0 but changed no file still
  goes to `needs_review`, with exit reason "exit 0 but NO files changed", a
  `[no-changes]` concern in the review handoff, and a `task_no_changes` event.
- **Out of scope**: files changed outside the task's `allowed_paths` (dirs,
  exact files, globs; `*` crosses `/`) give an `[out-of-scope]` concern and a
  `task_out_of_scope` event. An empty `allowed_paths` means the whole repo.
- Adapter bookkeeping files (the mock agent's `mock-prompt-received.txt`) are
  ignored by both checks. Concerns are replaced, not duplicated, on re-runs.

- `approve` is never a commit/merge/push.
- `integrate --dry-run` prints the exact git actions, predicted conflicts
  (git >= 2.38), and gate results.
- `integrate --approve` will, per approved task: commit uncommitted changes on
  the task branch (audit-logged), then `git merge --no-ff` into
  `guy/plan-<PLAN>-integration`. Tasks not yet approved stay pending — this is
  how a dependent task unblocks incrementally.
- Gate: every approved task with `required_tests` needs recorded passing
  results, unless you pass `--skip-tests` explicitly.
- Gate: no approved task may have changed files outside its `allowed_paths`.
  This is re-checked against the worktree at integration time, so reverting
  the stray files clears it; `--allow-out-of-scope` overrides after review.
  (The desktop app does not offer the override; use the CLI.)
- On conflict the merge is aborted, the plan is marked `conflict`, conflicted
  files are listed, and the default branch remains untouched.
- Nothing is ever pushed; publishing stays a human action.

## Maintenance

```
guy cancel TASK-001          # kill process tree + mark cancelled
guy clean --task T [--branch]        # remove worktree (refuses if dirty); optionally delete branch
guy clean --plan P [--branch]        # same for all finished tasks + integration worktree
guy config list|get KEY|set KEY VALUE
guy demo [--keep]            # disposable repo + mock agents, zero model cost
```

## Settings (config)

| Key | Default | Meaning |
|---|---|---|
| `max_workers` | 2 | concurrent agent processes (capped 8) |
| `default_agent` | auto-detect | agent for plans that don't specify one |
| `run_timeout_seconds` | 1800 | per-task timeout unless the task overrides |
| `planner_timeout_seconds` | 300 | `--planner` adapter timeout |
| `planner_max_tasks` | 8 | reject planner output with more tasks than this |
| `log_max_bytes` | 1,000,000 | per-run log cap (keeps tail) |
| `log_retention_days` | 30 | prune window |
| `safe_retry_failure_classes` | `["spawn_error"]` | failures eligible for auto-retry |

Per-repo overrides: `.guy/project.json` (full JSON) or `.guy/project.yaml`
(small subset: `key: value`, one nesting level, `- item` lists).
