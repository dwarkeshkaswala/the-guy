# the-guy security & privacy policy

## Principles

1. **Human authority**: write-capable work launches only behind explicit
   `--approve` flags; merges/commits of agent work happen only inside
   `guy integrate --approve`; nothing is ever pushed by the-guy.
2. **Least movement**: worktrees live in orchestrator-controlled directories;
   the user's working tree, uncommitted changes, and default branch are never
   modified, reset, cleaned, or rebased.
3. **Untrusted data stays data**: repository text, issue content, agent output,
   and plan files are untrusted input. They can never alter this policy —
   statuses are driven by exit codes, git results, and human decisions, not by
   anything an agent prints. Prompts are passed as single argv elements, so
   shell metacharacters in them are inert.
4. **No credential handling**: agents reuse their own CLI logins. the-guy
   persists no model credentials, passes the ambient environment through
   without logging it, and never prints environment variables.

## Specific controls

| Area | Control |
|---|---|
| Command injection | Every subprocess (git and agents) uses argv lists; no shell-string concatenation anywhere. Prompts starting with `-` are prefixed with a space so they cannot parse as flags. |
| Path traversal | Worktree paths are generated from server-side IDs and re-validated with `security.ensure_within` against the orchestrator workspace root. |
| Secret exposure | All agent output is redacted before it reaches logs or the terminal: API keys (`sk-`, `ghp_`, `glpat-`, `xox*`, AWS `AKIA...`), Bearer tokens, `key/token/secret/password =` assignments, URLs with embedded credentials, and PEM private-key blocks. |
| Log growth | Per-run logs are capped (`log_max_bytes`, default 1 MB) with tail retention; retention pruning window `log_retention_days`. |
| Process escape | Timeouts and cancellation kill the whole process tree (`taskkill /F /T` on Windows, process groups on POSIX); orphan detection via `pid`/`owner_pid` bookkeeping and `reconcile()`. |
| Destructive ops | `guy clean` refuses dirty worktrees; `repo remove --purge` refuses dirty managed worktrees; branch deletion uses safe `-d` unless the branch is provably contained in its integration branch. |
| Network | the-guy opens no listeners and makes no network calls itself. Agents' own network usage is governed by their CLIs. |
| Model consensus | Multiple agents sharing one model is treated as one opinion. Only deterministic test evidence plus human review counts. |

## Honest limitations (please read)

- **Sandboxing is the CLI's, not ours.** the-guy cannot sandbox an external
  agent CLI; adapters run CLIs with their own defaults and NEVER add bypass
  flags (`--dangerously-skip-permissions`, `--yolo`, etc.). If a CLI prompts
  for permission non-interactively it will hit the timeout and fail. Check
  each CLI's sandbox settings yourself; `guy agents` shows what we verified.
- **Broad filesystem access.** Agent CLIs can, in principle, read anything the
  invoking user can. `.env` exclusion is the CLI's responsibility; the-guy
  never reads or forwards repo secrets itself, and log redaction is
  pattern-based (it catches common formats, not all possible secrets).
- **Tests execute code.** `required_tests` commands run only via the explicit
  `--run-tests` flag or the integration gate; commands come from your plan
  file, never guessed. Run them on disposable work first.
- **Redaction is best-effort.** Unusual secret formats may pass through; treat
  logs as sensitive anyway.

## VS Code

VS Code / Copilot is a human review interface, not an automated worker. No
unsupported headless Copilot automation is claimed or implemented.
