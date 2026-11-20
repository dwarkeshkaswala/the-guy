"""Built-in deterministic mock agent adapter.

Used by tests and `guy demo`. Runs `python -m the_guy.mock_agent` with a JSON
spec delivered via environment — zero model cost, fully deterministic, never
touches anything outside its worktree.
"""

from __future__ import annotations

import json
import sys
from pathlib import Path
from typing import List

from the_guy import __version__
from the_guy.adapters.base import Adapter, CapabilityReport


class MockAdapter(Adapter):
    name = "mock"
    display = "Mock agent (built-in)"
    description = "Deterministic no-model agent for tests and demos."
    kind = "mock"
    install_hint = "built-in; nothing to install"

    def probe(self, timeout: float = 20.0) -> CapabilityReport:
        return CapabilityReport(
            adapter=self.name,
            display=self.display,
            kind="mock",
            installed=True,
            executable=f"{sys.executable} -m the_guy.mock_agent",
            version=__version__,
            headless_verified=True,
            headless_note="built-in; deterministic",
            notes=["Tests/demos only — does not call any model provider."],
        )

    def build_command(self, prompt: str, cwd: Path, options: dict) -> List[str]:
        return [sys.executable, "-m", "the_guy.mock_agent", "--prompt", self.prompt_arg(prompt)]

    def child_env(self, base_env: dict, options: dict) -> dict:
        env = dict(base_env)
        spec = options.get("mock", options) if isinstance(options, dict) else {}
        env["GUY_MOCK_SPEC"] = json.dumps(spec, ensure_ascii=False)
        return env
