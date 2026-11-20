"""Factory Droid CLI adapter (noninteractive `droid exec`).

Uses only the installed CLI — no Factory SDK, no Factory API key, no paid SDK
requirement.
"""

from __future__ import annotations

from pathlib import Path
from typing import List

from the_guy.adapters.base import Adapter


class DroidAdapter(Adapter):
    name = "droid"
    display = "Factory Droid CLI"
    description = "Factory Droid CLI exec mode (no Factory SDK/API key required)."
    candidates = ["droid"]
    verify_help_tokens = ["exec"]
    subcommand = ["exec"]
    install_hint = (
        "install the Factory Droid CLI per Factory's docs (a `droid` launcher on PATH "
        "or under a common location), then run `guy agents rescan`")

    def build_command(self, prompt: str, cwd: Path, options: dict) -> List[str]:
        exe = self.verify_for_execution().executable
        return [exe, "exec", self.prompt_arg(prompt)]
