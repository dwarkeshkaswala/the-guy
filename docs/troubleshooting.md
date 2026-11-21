# the-guy troubleshooting

## Quick diagnosis

```powershell
guy doctor            # environment, dirs, db, per-adapter capability truth
guy doctor --repo X   # + repo existence, dirty state, default branch
guy events --limit 50 # what actually happened, in order
```

## Common issues

**"adapter 'X' unavailable: no executable found ..."**
The CLI isn't installed, or lives somewhere discovery didn't look. Run
`guy agents doctor <name>` to see exactly what was searched (overrides, PATH
directories, common install locations) and every candidate file seen. Fixes,
in order of preference:
- install the CLI normally (npm global, brew, ...) and run `guy agents rescan`;
- point at it explicitly: `guy agents set <name> "C:\path with spaces\x.cmd"`;
- or set `GUY_<NAME>_PATH` for a session-scoped override.

**"configured executable path no longer exists"**
A `guy agents set` override points at a file that was removed (e.g. an
upgraded npm prefix). The adapter stays disabled — no fallback. Fix with
`guy agents set <name> <new-path>` or `guy agents clear <name>`.

**"only a PowerShell shim (.ps1) was found"**
Discovery refuses to launch `.ps1` files (PowerShell execution policy may
block them, and the-guy does not spawn a shell to run scripts). Use the
`.cmd`/`.exe` shim in the same folder — npm installs always create one — via
`guy agents set <name> <path-to-cmd>`.

**"adapter 'X' unavailable: --help does not mention ..."**
The installed CLI version doesn't match the adapter's verified command shape.
The adapter disables itself rather than guessing syntax. Fix: upgrade/downgrade
the CLI to a supported version, or extend the adapter in
`src/the_guy/adapters/` and re-verify with `guy agents`.

**Task is `blocked` with "waiting for dependencies"**
Dependencies must reach `integrated` before dependents run:
`guy approve DEP` (after review) then `guy integrate --approve`, then re-run
`guy run --approve`.

**Task is `blocked` with "path collision"**
Two concurrent write tasks claim overlapping (or whole-repo) scopes. Give
tasks disjoint `allowed_paths` in the plan, or run them one at a time
(`guy run --approve --task TASK-001`). Empty `allowed_paths` means the whole
repository.

**Task `failed` with `timed out after Ns`**
Agent hit the task/`run_timeout_seconds` limit (this includes a CLI waiting
for an interactive permission prompt — the-guy never auto-approves). Raise
`timeout_seconds` in the plan or configure the CLI's headless permission mode.

**Task `failed` with `orchestrator restarted or runner process exited unexpectedly`**
The `guy run` process died mid-run (reboot, closed terminal). Re-run the task:
`guy run --approve --task TASK-001`. Worktrees are preserved; a retry reuses
the existing worktree.

**`integrate --approve` says "required tests not passing"**
Record passing evidence first (`guy review --run-tests TASK`), or pass
`--skip-tests` deliberately. Evidence from a *failing* run is recorded and
shown in review.

**Integration reported a conflict**
Nothing was force-merged. Fix one of the tasks (e.g. re-run it so it builds on
newer state), or resolve manually inside the integration worktree printed by
the command, then re-run `guy integrate --approve`.

**`git worktree add` failed: branch already exists**
A branch survived from an earlier attempt: `guy clean --task T --branch`
(only for finished tasks), then re-run.

**Logs are empty for a failed task**
`guy logs TASK` shows the latest run; check `guy show TASK` → runs list for
`log_path`s of earlier attempts. Spawn errors print the OS error in the log.

**PowerShell says `guy` is not recognized**
Use the venv's Scripts dir (`.venv\Scripts\guy`), activate the venv, or
`pipx install --editable <path>` for a user-wide `guy`.

## Known limitations (MVP)

- Dependent tasks run from the plan's shared baseline; they do NOT see their
  dependencies' code unless you integrate the dependency first and re-plan.
  This is deliberate (explicit integration steps), documented in
  `docs/architecture.md`.
- `--planner` invokes a real agent CLI (uses its credits). It runs in a
  scratch worktree; on failure, garbage output, an invalid DAG, too many
  tasks, or any change to your working copy, nothing is created and the
  error names the planner log under `<data>/logs/<repo-id>/planner/`.
- No TUI/dashboard yet (Phase 5); the CLI is the interface.
- Conflict *prediction* requires git >= 2.38; older gits check at merge time.
- `guy logs --follow` polls the file (no OS file-watch dependency).
