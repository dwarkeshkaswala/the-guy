# The Guy Desktop — Architecture

The Guy Desktop (`the-guy-desktop.exe`) is a Wails v3 application that embeds the
exact Go core used by the `guy` CLI. It adds a native window and a typed service
layer — **not** a second orchestrator.

```
┌────────────────────────────────────────────────────┐
│ The Guy Desktop (Wails v3.1.0, WebView2/WebKit)    │
│ React 18 + TypeScript + Vite + Tailwind v4         │
│ Overview | Repos | Agents | Plans(DAG) | Runs |    │
│ Review | Settings · command palette · log dock     │
└───────────────┬────────────────────────────────────┘
                │ generated bindings (in-process IPC, no ports)
┌───────────────▼────────────────────────────────────┐
│ internal/application.Service  (Wails-free, tested) │
│ typed ops + event envelopes + log streaming        │
└───────────────┬────────────────────────────────────┘
                │ exported wrappers (internal/cli/export.go)
┌───────────────▼────────────────────────────────────┐
│ EXISTING Go CORE (unchanged behavior)              │
│ store(SQLite) tasks scheduler agents gitops review │
│ process redact config pathutil                     │
└────────────────────────────────────────────────────┘
guy CLI ── same packages, same on-disk state (SQLite + prefs.json)
```

## Key decisions

| Decision | Rationale |
| --- | --- |
| Wails v3.1.0 (pinned) | Stable release, pure-Go Windows build (CGO_ENABLED=0), system WebView, no bundled browser |
| Service layer in `internal/application` | UI never touches core internals; every write is revalidated in Go (the UI is not the security boundary) |
| Reuse via `internal/cli/export.go` | Repo registry, plan validation, approvals, cancellation, integration and doctor reuse the *same* functions the CLI uses — zero duplicated business logic |
| No daemon | Runs are owned by the desktop process, exactly like `guy run` owns its runs. Closing the window interrupts active tasks; on next start `scheduler.Reconcile` marks them interrupted (same recovery as a CLI crash). The UI states this honestly; no background persistence is claimed |
| Single instance | `SingleInstanceOptions` (unique id `dev.theguy.desktop`) focuses the existing window instead of opening a second writer against the same SQLite file |
| Event stream | The Go poller pushes `guy:event` envelopes (`seq, ts, kind, repo_id, task_id, payload, version`) from the shared `events` table every 400 ms; sequence IDs make delivery at-least-once, ordered, and replayable from 0 on reconnect/restart. A 2 s `guy:tick` provides liveness |
| Logs | Agents run as pipes (no PTY), so the GUI streams a **read-only** log panel via `ReadLogRange` byte-range polling: bounded window (4000 lines) in the renderer, full redacted log on disk, fetch paused while the window is hidden (agents keep running) |
| Diffs | `gitops.DiffPatch` (tracked diff + untracked files via `git diff --no-index`, never `git add -N`), rendered by a custom viewer — real Git output, byte-bounded |
| Persistence | All durable state stays in the existing SQLite/prefs layer. Only cosmetic session state (current screen) lives in the renderer |

## Event/lifecycle model

- `guy:event` — persisted audit event, monotonic `seq`
- `guy:tick` — liveness + active run count
- `guy:run-summary` — batch completion summary
- `guy:reconciled` — emitted at startup when interrupted runs were recovered

Because events live in the shared database, a `guy` CLI process working on the
same state is visible in the GUI in near-real time (demonstrated in acceptance
testing) and vice versa.

## Security boundaries (unchanged from the CLI)

- Approvals are explicit and recorded (`approvals` table); approving execution
  never merges; integration is a separate, gated step that never touches the
  default branch and never pushes
- No permission-bypass flags, ever; adapter verification refuses to guess CLI
  syntax; models/auth remain inside each agent CLI
- Subprocess argv arrays only (no shell interpolation), worktree-scoped cwd,
  cross-process cancellation via run-status polling + process-tree kill
- All exported log/diagnostic output passes `internal/redact`

## Rendering modes

Auto / Performance / Efficiency / Compatibility (`display_mode` setting) adjust
presentation only — animation budgets, transition suppression, polling cadence
of decorative updates. They never affect agent runs, security, or approval
policy. WebGL2 capability is probed and reported in Settings; since the backend
provides pipes rather than PTYs, no interactive terminal is offered and no GPU
dependency exists.

## What is deliberately *not* implemented

- `guy daemon` with authenticated loopback IPC (backlog; see packaging doc)
- Scheduler pause/resume (cancellation and dependency gating cover the flow)
- Monaco diff editor (custom viewer covers the need without a heavy dependency)
- xterm.js PTY (no PTY backend exists; a read-only log viewer is used instead —
  never a fake interactive terminal)
