"""OpenCode adapter (noninteractive `opencode run`)."""

from __future__ import annotations

from pathlib import Path
from typing import List

from the_guy.adapters.base import Adapter


class OpenCodeAdapter(Adapter):
    name = "opencode"
    display = "OpenCode"
    description = "OpenCode CLI headless run mode."
    candidates = ["opencode"]
    verify_help_tokens = ["run"]
    subcommand = ["run"]
    install_hint = (
        "install the OpenCode CLI (see opencode.ai docs), then run "
        "`guy agents rescan` — or `guy agents set opencode <path>`")

    def build_command(self, prompt: str, cwd: Path, options: dict) -> List[str]:
        exe = self.verify_for_execution().executable
        return [exe, "run", self.prompt_arg(prompt)]
