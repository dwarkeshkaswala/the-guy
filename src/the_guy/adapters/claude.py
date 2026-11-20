"""Claude Code adapter (noninteractive `claude -p`)."""

from __future__ import annotations

from pathlib import Path
from typing import List

from the_guy.adapters.base import Adapter


class ClaudeAdapter(Adapter):
    name = "claude"
    display = "Claude Code"
    description = "Anthropic Claude Code CLI in noninteractive print mode."
    candidates = ["claude"]
    verify_help_tokens = ["--print"]
    install_hint = (
        "install the Claude Code CLI (e.g. `npm install -g @anthropic-ai/claude-code` "
        "or the official native installer), then run `guy agents rescan` — or point "
        "directly at it with `guy agents set claude <path-to-claude.exe>`")

    def build_command(self, prompt: str, cwd: Path, options: dict) -> List[str]:
        exe = self.verify_for_execution().executable
        return [exe, "-p", self.prompt_arg(prompt)]
