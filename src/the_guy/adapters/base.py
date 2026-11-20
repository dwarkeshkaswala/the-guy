"""Adapter base class: capability-check, never assume.

Discovery order per adapter: explicit override (``GUY_<NAME>_PATH`` env var or
``guy agents set``) > PATH > well-known user install locations. Windows
``.exe``/``.cmd``/``.bat`` shims are launchable; ``.ps1`` shims are reported
but never launched (execution policy may block them).

Before any execution is allowed, the *actual installed CLI* is probed with
read-only commands only (``--version`` / ``--help``): the noninteractive
command shape is verified against real ``--help`` output. If verification
fails the adapter stays disabled — the-guy never guesses CLI syntax and never
silently falls back to another agent. Commands are argv arrays, never shell
strings, and permission-bypass flags (--dangerously-skip-permissions, --yolo,
...) are never added.
"""

from __future__ import annotations

import subprocess
from dataclasses import asdict, dataclass, field
from pathlib import Path
from typing import Any, Dict, List, Optional

from the_guy import config, discovery


class AdapterUnavailable(Exception):
    pass


@dataclass
class CapabilityReport:
    adapter: str
    display: str
    kind: str = "real"  # real | mock
    installed: bool = False
    executable: Optional[str] = None
    source: Optional[str] = None  # env | config | PATH | well-known
    version: Optional[str] = None
    headless_verified: bool = False
    headless_note: str = ""
    notes: List[str] = field(default_factory=list)
    extension_hint: Optional[str] = None
    discovery: Dict[str, Any] = field(default_factory=dict)

    def to_dict(self) -> dict:
        return asdict(self)


def _run_capture(argv: List[str], timeout: float = 20.0) -> Optional[str]:
    try:
        proc = subprocess.run([str(a) for a in argv], capture_output=True, timeout=timeout)
    except (OSError, subprocess.TimeoutExpired):
        return None
    text = (proc.stdout + proc.stderr).decode("utf-8", errors="replace")
    return text if proc.returncode == 0 else None


class Adapter:
    name = "base"
    display = "Base adapter"
    description = ""
    candidates: List[str] = []
    version_args: List[str] = ["--version"]
    help_args: List[str] = ["--help"]
    # ALL verify_help_tokens must appear in real --help output for headless use.
    verify_help_tokens: List[str] = []
    # Optional subcommand (e.g. ["exec"]) checked with `<exe> exec --help`
    # as additional evidence (advisory: some CLIs don't support subcommand --help).
    subcommand: Optional[List[str]] = None
    install_hint: str = "install the CLI per its official docs, then run `guy agents rescan`"
    kind = "real"

    @property
    def env_var(self) -> str:
        return f"GUY_{self.name.upper()}_PATH"

    def probe(self, timeout: float = 20.0) -> CapabilityReport:
        overrides = config.adapter_overrides()
        result = discovery.resolve(self.candidates, self.env_var, overrides.get(self.name))
        report = CapabilityReport(adapter=self.name, display=self.display, kind=self.kind,
                                  discovery=result.to_dict())
        if self.kind == "real":
            ext_hit = discovery.vscode_extension_hit(self.name)
            if ext_hit:
                report.extension_hint = ext_hit
                report.notes.append(
                    f"VS Code extension '{ext_hit}' detected — an extension is NOT a global "
                    "CLI and never counts as one; install the CLI itself.")

        if result.override_error:
            report.headless_note = result.override_error
            return report
        if not result.chosen:
            if result.ps1_only:
                report.headless_note = discovery.PS1_NOTE
            else:
                report.headless_note = (
                    f"no executable found for '{self.candidates[0]}' on PATH or in common "
                    f"install locations — run `guy agents doctor {self.name}` for the "
                    "full search report")
            return report

        exe = result.chosen
        report.installed = True
        report.executable = str(exe)
        report.source = result.chosen_source
        version_text = _run_capture([exe, *self.version_args], timeout=timeout)
        if version_text is not None and version_text.strip():
            report.version = version_text.strip().splitlines()[0][:200]
        help_text = _run_capture([exe, *self.help_args], timeout=timeout) or ""
        missing = [tok for tok in self.verify_help_tokens if tok not in help_text]
        if not self.verify_help_tokens:
            report.headless_verified = False
            report.headless_note = "no verification tokens configured; adapter disabled"
        elif missing:
            report.headless_verified = False
            report.headless_note = (
                f"--help does not mention {', '.join(missing)}; refusing to guess CLI syntax "
                "(adapter disabled — update the adapter for this CLI version)")
        else:
            report.headless_verified = True
            report.headless_note = f"verified {', '.join(self.verify_help_tokens)} in --help"
            if self.subcommand:
                sub_help = _run_capture([exe, *self.subcommand, "--help"], timeout=timeout)
                if sub_help is None:
                    report.notes.append(
                        f"`{' '.join(self.subcommand)} --help` probe failed; verification "
                        "relies on top-level --help evidence")
        report.notes.extend(self.notes())
        return report

    def notes(self) -> List[str]:
        return [
            "Model/provider/auth stay exactly as configured in the CLI itself; the-guy never overrides them.",
            "the-guy never adds permission-bypass flags; if the CLI asks for permission the run will time out.",
        ]

    def verify_for_execution(self) -> CapabilityReport:
        report = self.probe()
        if not report.installed or not report.headless_verified:
            raise AdapterUnavailable(
                f"adapter '{self.name}' unavailable: {report.headless_note or 'not installed'}"
            )
        return report

    def build_command(self, prompt: str, cwd: Path, options: dict) -> List[str]:
        raise NotImplementedError

    def child_env(self, base_env: dict, options: dict) -> dict:
        return dict(base_env)

    @staticmethod
    def prompt_arg(prompt: str) -> str:
        # Prompts are passed as a single argv element, so shell metacharacters are
        # inert. A leading "-" would be parsed as a flag by some CLIs, so we
        # prefix it with a space in that (pathological) case.
        return prompt if not prompt.startswith("-") else " " + prompt
