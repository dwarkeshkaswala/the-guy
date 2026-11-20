# The Guy Desktop — User Guide

One guy. Every agent. Any repo. The desktop app is a control center over the
same orchestrator the `guy` CLI drives — both can be used interchangeably on
the same state.

## Install / run

```
# build (from the repo root, Go 1.26+ and Node 18+ required)
cd frontend && npm install && npm run build && cd ..
go build -tags production -ldflags "-s -w" -o the-guy-desktop.exe .

# or with the Wails toolchain (also generates bindings + icons)
wails3 build

# run
./the-guy-desktop.exe
```

The `guy` CLI keeps working independently (`go build -o guy.exe ./cmd/guy`).

## The screens

- **Overview** — service health (version, pid, detected CPUs, data paths, DB
  size), stat tiles, recent audit activity, first-run onboarding.
- **Repositories** — register any local Git repo (native folder picker or typed
  path; paths with spaces and detached HEADs are fine). Live branch/dirty state,
  remote, worktrees, per-repo task counts. Removing a registration never deletes
  your directory; purging orchestrator worktrees is a separate explicit choice.
- **Agents** — capability-checked adapters (`claude`, `opencode`, `codex`,
  `droid`, built-in `mock`). Version/executable only when provable; model and
  auth stay inside each CLI (`unknown` is shown rather than guessed).
  Re-probe any time; set per-adapter executable overrides.
- **Plans** — create plans from JSON with live schema/DAG validation (validated
  in Go, never only in the browser). Inspect the task DAG, then:
  **Approve plan → Dry run → Launch** as three explicit steps. A dry run shows
  exact argv, worktrees, branches, timeouts, adapter readiness and pending
  dependencies without launching anything.
- **Runs** — live task states with real process status, elapsed time (no
  invented progress), worktree/branch, exit codes, and the streaming read-only
  log dock (Ctrl+J). Cancel kills the agent's whole process tree; failed tasks
  can be re-run.
- **Review** — the needs-review queue with the real diff, per-task test
  evidence (run explicitly; tests execute code), handoff summary and concerns.
  Approving a review records human judgment only. **Integration** is separate:
  a preview shows the exact integration branch, planned actions, predicted
  conflicts and gate failures before an explicit confirmation. The default
  branch is never touched; nothing is ever pushed.
- **Settings** — theme/density/font/display mode, concurrency and timeouts
  (persisted in the Go prefs), `guy doctor` parity, paths, and a **redacted**
  diagnostics export.

## Keyboard

- `Ctrl/Cmd+K` — command palette (navigation, refresh, re-probe, display modes,
  theme toggle)
- `Ctrl+1..7` — jump between screens
- `Ctrl+J` — toggle the log dock
- `Esc` — close dialogs/palette

## Honesty guarantees

- Closing the window interrupts active runs (they are marked interrupted and
  re-runnable — the same recovery as a CLI crash). No background daemon exists,
  and none is claimed.
- Agent output is a read-only log panel — the backend runs pipes, not PTYs, so
  no interactive terminal is faked.
- Anything the backend cannot verify (model, auth, remote GPU state) displays
  "unknown", never a guess.

## Sharing state with the CLI

The desktop app and `guy` use the same SQLite database and preferences. Events
from a CLI run appear live in the GUI's activity feed and counts (and vice
versa). One caveat: both processes can own runs simultaneously, but each run
belongs to the process that launched it.
