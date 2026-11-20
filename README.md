# the-guy

> One guy. Every agent. Any repo.

A local-first, repository-agnostic multi-agent software-engineering orchestrator.
Install once, register any local Git repository, and coordinate independently
installed coding agents — while you keep control over every change.

**This is the Go implementation** (`guy` v0.2.x): a single static binary per
platform, no Python runtime required. The original Python implementation
(v0.1.x) is preserved in this repository's history and on `master`; both CLIs
share the same SQLite state format, and `guy migrate` adopts existing state
backup-safely (see `docs/go-migration.md` for migration and rollback).

## Install

Requires Git 2.17+ (2.38+ recommended). No other runtime dependencies — the
binary is statically linked, including the embedded SQLite (pure Go).

```powershell
# Windows — build from source (needs Go 1.26+)
git clone <this-repo>; cd the-guy
go build -o guy.exe ./cmd/guy
.\guy.exe --version

# or install into GOBIN
go install ./cmd/guy
```

```bash
# macOS / Linux
go build -o guy ./cmd/guy && sudo mv guy /usr/local/bin/
guy --version
```

Release artifacts (windows/linux/darwin, amd64/arm64, with SHA256SUMS) are
produced by CI on version tags. Uninstall = delete the binary plus the data
directories below (nothing else is written outside repos' optional `.guy/`).

## Where state lives

- Windows: data `%LOCALAPPDATA%\the-guy` (SQLite `state.db`, redacted logs,
  agent worktrees), prefs `%APPDATA%\the-guy`
- macOS/Linux: `~/.local/share/the-guy` and `~/.config/the-guy` (XDG honored)
- Override with `GUY_DATA_DIR` / `GUY_PREFS_DIR` (used heavily by tests)
- Target repositories get nothing except an *optional* `.guy/project.json`

## the-guy CLI adapters vs paid Factory Workstreams

the-guy's Droid support is the **Factory Droid CLI you already have installed**
(`droid exec`), using its existing local login — no Factory API key, no SDK,
and no paid "Factory Workstreams" product is used, bundled, or unlocked by
using the-guy. The same is true for the other adapters: the-guy drives your
locally installed Claude Code / OpenCode / Codex CLIs with their own auth and
model settings. Anything billed happens through those CLIs exactly as if you
ran them yourself.

## What it does

- **Universal**: works with any local Git repository — any language, layout,
  package manager, or remote host. Default branches are *detected*, never
  assumed to be `main`.
- **Adapters, not assumptions**: Claude Code, OpenCode, Codex CLI, and Factory
  Droid CLI adapters probe the *actual installed CLI* (version, `--help`,
  noninteractive flags) before anything runs. Unsupported or missing tools are
  reported and disabled, never guessed at. Discovery checks explicit
  overrides (`GUY_<NAME>_PATH`, `guy agents set`) > PATH > adapter-specific
  locations (Factory Droid: `%LOCALAPPDATA%\Factory\app-*` with numeric
  version ordering) > common install locations (npm global prefix resolved
  from `npm config get prefix`, pnpm, yarn, volta, scoop, cargo,
  `~/.local/bin`, ...), with `guy agents doctor` showing the full search
  report. Windows `.cmd` shims are handled; `.ps1` shims are never launched
  (execution policy), and a VS Code extension never counts as a global CLI.
  Upgrades re-detect automatically via cache fingerprinting.
- **Human gates everywhere**: `plan` only drafts. `run` needs `--approve`.
  `approve` records review decisions — it is never a merge. `integrate` needs
  its own `--approve`, targets a separate integration branch, and never touches
  the default branch. the-guy never pushes, rebases, resets, or discards your
  uncommitted work.
- **Isolation**: every write-capable task runs in its own Git worktree under
  `%LOCALAPPDATA%\the-guy\worktrees`, from a recorded baseline commit.
- **Truthful state**: `draft → approved_for_execution → queued → running →
  needs_review → approved → integrated`, with `blocked`/`failed`/`cancelled`.
  A process exit code is evidence, not proof; approvals are human actions.
- **No credential handling**: agents reuse their own CLI logins. the-guy never
  stores or prints secrets; all logs are redacted and size-capped.
- **Model-neutral**: each agent keeps its existing model/provider config. All
  agents running the same model is *not* treated as independent verification —
  deterministic tests and human review are.

## Install

Requires Python 3.9+ and Git 2.17+ (2.38+ recommended for conflict pre-checks).

```powershell
# from the project root
python -m venv .venv
.venv\Scripts\pip install -e .
.venv\Scripts\guy --version

# or user-wide
pipx install --editable "C:\path\to\the-guy"
```

No third-party runtime dependencies. Tests need `pip install -e ".[dev]"`.

## Quickstart

```powershell
guy doctor                       # git, python, dirs, agent CLIs (no secrets shown)
guy agents                       # adapter capability report
guy repo add "C:\Projects\sample" --alias sample
guy repo list
guy use sample                   # per-user default (stored in prefs, not your shell)
guy inspect                      # read-only discovery

guy plan "Implement searchable catalog with tests"   # DRAFT — changes nothing
guy plan --file my-plan.json                         # full DAG (see docs/command-reference.md)
guy tasks                        # statuses + dependencies
guy approve PLAN-001             # draft -> approved_for_execution (execution only!)

guy run --dry-run                # exact worktrees + sanitized commands; launches nothing
guy run --approve                # launch in isolated worktrees (max 2 workers by default)

guy status                       # tasks, branches, running PIDs, worktrees
guy logs TASK-001 [--follow]
guy review TASK-001 [--run-tests]
guy approve TASK-001             # needs_review -> approved (review approval, NOT a merge)

guy integrate --dry-run          # exact git actions, conflicts, gates
guy integrate --approve          # commit (approved) task changes + merge into
                                 # guy/plan-<PLAN>-integration — never the default branch
guy cancel TASK-001
guy demo                         # full self-contained demo: disposable repo + mock agents
```

All repo-scoped commands accept `--repo <alias|path|id>`. Most commands accept
`--json`.

## Where state lives

- Windows: data `%LOCALAPPDATA%\the-guy` (SQLite state, logs, worktrees),
  prefs `%APPDATA%\the-guy`
- POSIX: `~/.local/share/the-guy` and `~/.config/the-guy`
- Override with `GUY_DATA_DIR` / `GUY_PREFS_DIR` (used heavily by the tests)
- Target repositories get nothing except an *optional* `.guy/project.json`

## Safety model (short version)

1. Nothing write-capable launches without an explicit `--approve`.
2. Agent changes are never auto-committed; commits happen only inside
   `guy integrate --approve` with a full audit trail.
3. Integration goes to `guy/plan-<PLAN>-integration`, never your default branch.
4. the-guy never pushes anywhere — publishing stays a human action.
5. Dirty worktrees are never deleted; `guy clean` refuses them.
6. Conflicts stop integration with actionable diagnostics.
7. Repo/issue/agent text is untrusted data; it can never override these rules.

See `docs/security.md` for the full policy and known limitations.

## Development

```bash
gofmt -l ./cmd ./internal . && go vet ./cmd/... ./internal/...   # quality gates (CI enforces both)
go test ./cmd/... ./internal/...    # full suite: disposable repos + mock agents, zero model cost
go build -o guy.exe ./cmd/guy       # CLI (Windows example)
```

### Desktop GUI (The Guy Desktop)

A Wails v3 desktop control center over the same Go core — no second
orchestrator, and the CLI keeps working without it.

```bash
cd frontend && npm install && npm run build && cd ..
go build -tags production -ldflags "-s -w" -o the-guy-desktop.exe .
./the-guy-desktop.exe               # or: wails3 dev for live-reload development
```

Screens: Overview, Repositories, Agents, Plans (JSON validation + task DAG),
Runs (live read-only logs, cancel), Review (real diffs, test evidence, gated
integration), Settings (display modes, doctor, redacted diagnostics export).
Docs: `docs/desktop-architecture.md`, `docs/user-guide.md`, `docs/packaging.md`.

The Python implementation and its suite still exist (pip install -e ".[dev]";
pytest) on `master` for parity checking during migration; the Go port is the
active implementation on `go-migration` and later mainline releases.

Docs: `docs/architecture.md`, `docs/command-reference.md`, `docs/security.md`,
`docs/troubleshooting.md`, `docs/go-migration.md`.

## Status

Go port v0.2.0 + The Guy Desktop (Wails v3.1.0, Windows-tested). CLI: functional
parity with the Python MVP for the whole command surface except the
experimental `--planner` flag (deliberately not ported; plan files are the
supported path). Desktop: same core and on-disk state; runs are owned by the
window (closing interrupts them; reconcile-on-start matches CLI crash recovery —
no daemon is claimed). Known limitations: `docs/troubleshooting.md`,
`docs/go-migration.md`, `docs/packaging.md`. VS Code integration is documented
manual usage plus machine-readable `--json` output — no unsupported automation
is claimed.
