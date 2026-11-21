# the-guy: Python → Go migration notes

## Phase 0 — audit of the Python implementation (behavioral baseline)

Audited against `src/the_guy/` (commit `e2bac43`, 81 passing tests).

**Commands** (exit codes: 0 ok, 1 error, 2 usage; `--json` on most commands;
`--repo <alias|path|id>` on repo-scoped commands):
`doctor, agents [list|doctor|set|clear|rescan], repo add/list/remove,
use (repo use is a Go addition), inspect, plan (prompt | --file | --planner),
tasks, show, approve, reject, run (--dry-run/--approve, --task, --plan,
--max-workers), status, logs (--follow), review (--run-tests), integrate
(--dry-run/--approve, --plan, --skip-tests), cancel, clean (--task/--plan,
--branch), config list/get/set, events, demo (--keep)`.

**State**: SQLite (`%LOCALAPPDATA%/the-guy/state.db`, `GUY_DATA_DIR` override;
WAL) with tables `meta, repos, plans, tasks, runs, events, approvals` and
zero-padded sequence IDs (`TASK-001`). Preferences: `%APPDATA%/the-guy/prefs.json`
(`GUY_PREFS_DIR` override) holding `default_repo`, `settings`, `adapters`
(executable overrides). Settings precedence: CLI > repo `.guy/project.json` /
`.guy/project.yaml` (tiny subset) > user prefs > built-in defaults
(`max_workers=2`, `run_timeout_seconds=1800`, `log_max_bytes=1_000_000`,
`log_retention_days=30`, `safe_retry_failure_classes=["spawn_error"]`, …).

**Task lifecycle**: `draft → approved_for_execution → queued → running →
needs_review → approved → integrated`, with `blocked/failed/cancelled`;
strict transition table; dependencies gate on `integrated`; approvals are
human actions recorded in `approvals`; exit code 0 ⇒ `needs_review`, never
`approved`. Plan-level `approve` bulk-approves drafts. `reject` moves
`needs_review → failed`.

**Adapters & discovery** (capability-checked, never assumed): `claude -p`,
`opencode run`, `codex exec`, `droid exec`, plus a built-in deterministic
mock agent. Discovery precedence: override (`GUY_<NAME>_PATH` env >
`guy agents set` config) > PATH scan > adapter-specific roots (Factory Droid
`%LOCALAPPDATA%\Factory\app-*\resources\bin`, numeric version order,
newest-valid) > well-known roots (npm global prefix resolved from npm itself,
`%APPDATA%\npm`, pnpm, yarn, volta, scoop, cargo, `~/.local/bin`, `~/bin`,
homebrew). Windows priority `.exe > .cmd > .bat > bare`; `.ps1` discovered but
never launched. Verification = read-only `--version`/`--help` with required
tokens; failure ⇒ disabled, no silent fallback. Probe cache at
`data/adapter-cache.json` with fingerprint (PATH, PATHEXT, overrides, adapter
set, roots, npm prefix, Factory dirs, version) + 300 s TTL + deleted-exe
revalidation; `agents rescan` forces. VS Code extensions are hints only.

**Execution**: argv arrays only; worktree per write task under
`data/worktrees/<repo>/<TASK>` from the plan's baseline commit, branch
`guy/<TASK>-<slug>`; read-only tasks run in the repo root; bounded workers
(default 2, max 8); allowed-paths collision blocking (empty = whole repo);
timeouts; cancel via DB row `cancel-requested` + process-tree kill
(`taskkill /F /T` Windows, process groups POSIX); `spawn_error` auto-retry;
restart reconciliation via `owner_pid` liveness; redacted, size-capped logs
under `data/logs/<repo>/<TASK>/run-<RUN>.log`. Never commits during a run.

**Review & integration**: handoff (JSON + Markdown) with changed files,
commits, test evidence, concerns; `review --run-tests` runs the plan's
`required_tests` argv lists in the worktree. `integrate --dry-run/--approve`
gates on approvals + test evidence (+ `--skip-tests` escape hatch), predicts
conflicts via `git merge-tree` (git ≥ 2.38), commits task changes only inside
the approved integration step, merges `--no-ff` into
`guy/plan-<PLAN>-integration`; never touches the default branch, never pushes.
`clean` refuses dirty worktrees.

**Known gaps carried into Go deliberately unchanged**: dependent tasks run
from the shared plan baseline (integrate the dependency first, then run the
dependent); conflict *prediction* needs git ≥ 2.38.

## Migration decisions

- **Go owns everything**; no Python runtime needed. The Python tree stays on
  `master` until parity is demonstrated; Go work lives on `go-migration`.
- **Storage**: same SQLite schema, read and written by Go through the pure-Go
  driver `modernc.org/sqlite` (CGo-free → cross-compilation works; verified by
  `go test` on Windows here and by CI builds for linux/darwin).
  `guy migrate` adopts the existing DB/prefs **non-destructively**: schema is
  compatible by construction, and a timestamped backup copy of `state.db` is
  taken first. Rollback = restore the backup (or simply keep using the Python
  CLI — both understand the same state).
- **Mock agent** becomes a subcommand of the `guy` binary itself
  (`guy mock-agent`, hidden), so demos/tests never need Python.
- **Process trees**: Windows `taskkill /F /T` (proven in the Python suite);
  POSIX process groups. Job Objects are documented as a possible enhancement.
- **No localhost API in this milestone** (spec says optional; CLI + JSON
  output are the machine-readable surface for a future VS Code extension).

## Rollback

1. `guy` (Go) and the Python CLI can point at the same `GUY_DATA_DIR`.
2. Before adopting Python state, `guy migrate` writes
   `state.db.pre-go-backup-<timestamp>`.
3. To roll back: restore that file over `state.db` and use the Python CLI.

## Implementation status (go-migration branch, v0.2.0-go)

Implemented and tested (`go test ./...` green on windows/amd64):

- Full command surface from the Python MVP: `init, doctor, agents
  (list/doctor/set/clear/rescan), repo add/list/remove/use, use, inspect,
  plan (prompt + --file), tasks, show, approve, reject, run (--dry-run /
  --approve / --task / --plan / --max-workers), status, logs (--follow),
  review (--run-tests), integrate (--dry-run / --approve / --skip-tests),
  cancel, clean, config, events, demo, migrate`, plus `repo use` as an alias
  of `use` and the hidden `mock-agent` subcommand.
- Same SQLite schema, IDs, statuses, transitions, settings, prefs.json and
  log/worktree layouts as Python — verified by black-box tests and the
  `migration-parity` CI workflow (Python writes state, Go adopts it, both
  read it afterwards).
- Discovery parity incl. Factory `app-*` numeric versioning, npm prefix
  resolution, `.ps1` refusal, stale overrides, VS Code hint, fingerprint+TTL
  probe cache.
- Process supervision: argv-only, streaming redacted capped logs, timeout,
  DB-driven cross-process cancel, process-tree kill, restart reconcile.
- Scheduler: dependency gating, bounded workers (default 2), allowed-path
  collisions, safe-class retries.
- Review/integration: handoffs, test evidence, gates, merge-tree conflict
  prediction, integration branch only, never push / never default branch.

`plan --planner` was ported later with stricter safety than Python (scratch
worktree instead of the working copy, prompt-echo-proof JSON extraction,
planner agent/options/base ignored); see docs/command-reference.md.

Deliberately not ported (documented, non-silent):

- Optional localhost API / dashboard (spec says optional; `--json` output is
  the machine-readable surface for a future VS Code extension).
- `logs --follow` polls the file (same as Python; no fsnotify dependency).

Verification on this machine (windows/amd64): `go vet ./...` clean,
`gofmt` clean, `go test ./...` all packages green, `guy.exe` (11.7 MB) built,
cross-compiles for windows/linux/darwin amd64+arm64 (CGO_ENABLED=0), and the
real binary discovers + verifies all four installed CLIs (claude 2.1.329,
opencode 1.19.6, codex 0.171.0, droid 1.16.0). `-race` requires a C toolchain
absent on this Windows box — race runs happen in CI (linux/macos jobs).
macOS/Linux runtime claims rest on CI execution, not cross-compilation alone.
