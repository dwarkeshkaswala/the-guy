# the-guy architecture

```
PowerShell / VS Code terminal
        |
        v
    guy CLI  (src/the_guy/cli.py)
        |
   +----+------------------+------------------+
   |                       |                  |
Repo Registry        Task Scheduler      Event/Log Store
(registry.py,        (scheduler.py)      (events.py,
 store.py SQLite)                        store.py)
   |                       |                  |
   +---------- Coordinator(scheduler)+Runner)-+
                           |
                    Adapter Registry (adapters/)
             -------------+------------------
             |            |            |     |
          claude       opencode      codex  droid   (+ built-in mock)
             |            |            |     |
   agent subprocesses in isolated Git worktrees
   (runner.py — argv lists, redacted capped logs, timeout, tree-kill)
                           |
              git diffs + test evidence + review gates
                           |
        approved integration branch (guy/plan-<PLAN>-integration)
```

## Modules

| Module | Responsibility |
|---|---|
| `cli.py` | Argument parsing, structured output (`--json`), exit codes (0 ok / 1 error / 2 usage), the scripted `guy demo`. |
| `config.py` | OS-aware paths, atomic user prefs, optional per-repo `.guy/project.json` / `.guy/project.yaml` (tiny subset), precedence: CLI > repo > user > builtin. `GUY_DATA_DIR` / `GUY_PREFS_DIR` override everything (used by tests). |
| `security.py` | Secret redaction, path containment (`ensure_within`), slug/ref sanitization, redacted argv rendering. |
| `models.py` | `Repo`, `Plan`, `Task`, `RunRecord`, `ApprovalRecord` dataclasses; the task-status state machine (`TRANSITIONS`); DAG validation (Kahn). |
| `store.py` | SQLite (WAL) persistence for repos/plans/tasks/runs/events/approvals; sequence-based IDs (`TASK-001`...); one lock-guarded connection. |
| `git_ops.py` | Every git call, argv-only. Default-branch *detection*, worktree add/remove/list, merge-tree conflict prediction (git >= 2.38), commit/merge helpers. Never pushes, never resets, never touches the default branch. |
| `registry.py` | Register/resolve/remove repos by id/alias/path; duplicate-root refusal; read-only `inspect`. |
| `runner.py` | Subprocess execution: cwd, timeout, streaming redacted log with size cap, process-tree kill (`taskkill /T` on Windows, process groups on POSIX), cross-process cancel via DB status polling, liveness checks. |
| `adapters/` | `base.Adapter` (probe → `CapabilityReport`; verify against real `--help`; argv construction) + claude (`-p`), opencode (`run`), codex (`exec`), droid (`exec`), mock (built-in, `python -m the_guy.mock_agent`). |
| `scheduler.py` | Bounded concurrency (default 2), dependency gating (deps must be `integrated`), allowed-paths collision detection, safe-class retries (`spawn_error`), restart reconciliation (`reconcile`). |
| `review.py` | Handoff building, test evidence (`--run-tests`), integration preview + gated `perform_integration` (commits + `--no-ff` merges into the integration branch only; conflict abort with diagnostics). |
| `events.py` | Thin audit-trail helpers over the events table. |

## Task lifecycle

```
draft -> approved_for_execution -> queued -> running -> needs_review -> approved -> integrated
                    |                |          |            |
                    v                v          v            v
                 blocked          blocked    failed/cancelled  failed (reject)
```

- Transitions are validated by `models.TRANSITIONS`; every change is an event.
- `approve` (execution or review) is a *human* action recorded in `approvals`.
- A dependency only unblocks its dependent once it is `integrated` — agent
  agreement between tasks is never sufficient.
- Exit code 0 moves a task to `needs_review`, never to `approved`.

## State layout

- `%LOCALAPPDATA%\the-guy` (or `$XDG_DATA_HOME/the-guy`): `state.db`, `logs/`,
  `worktrees/<repo-id>/<task-id>`, `worktrees/<repo-id>/plan-<PLAN>-integration`.
- `%APPDATA%\the-guy` (or `$XDG_CONFIG_HOME/the-guy`): `prefs.json` (default
  repo, user settings).
- Target repos receive nothing; `.guy/project.*` is optional.

## Concurrency & recovery

- `guy run` drives a bounded thread pool; each launch owns one worktree.
- Run rows record `pid` and `owner_pid`; `guy cancel` flips the DB row to
  `cancel-requested` and kills the tree — the owning runner (or, if it died,
  the next command's `reconcile()`) finalizes the task.
- Restart recovery: any command that touches state reconciles runs whose owner
  process no longer exists → `failed(interrupted)`.

## Why no Factory SDK / no API keys

All agent work goes through installed CLIs and their existing logins. The
Factory Droid CLI adapter shells out to `droid exec` exactly like the other
adapters; no SDK, daemon, or API key is required anywhere in the codebase.
