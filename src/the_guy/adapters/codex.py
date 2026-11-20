"""Codex CLI adapter (noninteractive `codex exec`)."""

from __future__ import annotations

from pathlib import Path
from typing import List

from the_guy.adapters.base import Adapter


class CodexAdapter(Adapter):
    name = "codex"
    display = "Codex CLI"
    description = "OpenAI Codex CLI exec mode (uses the CLI's own sandbox defaults)."
    candidates = ["codex"]
    verify_help_tokens = ["exec"]
    subcommand = ["exec"]
    install_hint = (
        "install the Codex CLI (e.g. `npm install -g @openai/codex` or brew), then run "
        "`guy agents rescan` — or `guy agents set codex <path>`")

    def build_command(self, prompt: str, cwd: Path, options: dict) -> List[str]:
        exe = self.verify_for_execution().executable
        return [exe, "exec", self.prompt_arg(prompt)]
