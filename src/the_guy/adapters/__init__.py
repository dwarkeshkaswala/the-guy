"""Adapter registry with a fingerprint+TTL probe cache.

The cache makes `guy agents` / `guy doctor` fast while still rediscovering
automatically: any change to PATH, PATHEXT, GUY_*_PATH env overrides,
persisted adapter config, the registered adapter set, the well-known roots,
or the the-guy version invalidates it; a short TTL catches installs that
changed no environment setting; and a cached "installed" report is
re-validated (executable still exists) before being trusted. Execution paths
(`guy run`) always probe live — the cache is only for display.
`guy agents rescan` clears it explicitly.
"""

from __future__ import annotations

import hashlib
import json
import os
import time
from pathlib import Path
from typing import Dict, List

from the_guy import __version__, config, discovery
from the_guy.adapters.base import Adapter, AdapterUnavailable, CapabilityReport
from the_guy.adapters.claude import ClaudeAdapter
from the_guy.adapters.codex import CodexAdapter
from the_guy.adapters.droid import DroidAdapter
from the_guy.adapters.mock import MockAdapter
from the_guy.adapters.opencode import OpenCodeAdapter

_ADAPTERS: Dict[str, Adapter] = {}

# Cache is display-only; keep it short-lived so fresh installs appear quickly.
CACHE_TTL_SECONDS = 300


def _register(adapter: Adapter) -> None:
    _ADAPTERS[adapter.name] = adapter


_register(MockAdapter())
_register(ClaudeAdapter())
_register(OpenCodeAdapter())
_register(CodexAdapter())
_register(DroidAdapter())


def get(name: str) -> Adapter:
    adapter = _ADAPTERS.get(name)
    if not adapter:
        raise AdapterUnavailable(
            f"unknown agent '{name}'; available: {', '.join(sorted(_ADAPTERS))}"
        )
    return adapter


def all_adapters() -> List[Adapter]:
    return list(_ADAPTERS.values())


def register_custom(adapter: Adapter) -> None:
    """Register an adapter at runtime (used by tests)."""
    _register(adapter)


def probe_all() -> List[CapabilityReport]:
    return [a.probe() for a in all_adapters()]


def _cache_path() -> Path:
    return config.data_dir() / "adapter-cache.json"


def _fingerprint() -> str:
    parts = {
        "guy_version": __version__,
        "path": os.environ.get("PATH", ""),
        "pathext": os.environ.get("PATHEXT", ""),
        "adapter_names": sorted(_ADAPTERS),
        "env_overrides": {a.name: os.environ.get(a.env_var) for a in all_adapters()},
        "config_overrides": config.adapter_overrides(),
        "well_known_roots": [str(p) for p in discovery.well_known_roots()],
        # upgrade-sensitive inputs: a new Factory app-* dir or a changed npm
        # prefix invalidates the cache even when PATH/config did not change
        "factory_droid_roots": [str(p) for p, _ in discovery.factory_droid_roots()],
        "npm_prefix": str(discovery.npm_global_prefix() or ""),
        "vscode_dir": str(discovery.vscode_extension_dir() or ""),
    }
    return hashlib.sha256(
        json.dumps(parts, sort_keys=True, default=str).encode("utf-8")
    ).hexdigest()


def clear_probe_cache() -> None:
    try:
        _cache_path().unlink()
    except OSError:
        pass


def probe_all_cached() -> List[CapabilityReport]:
    """probe_all with automatic rediscovery (fingerprint + TTL + staleness)."""
    cache_file = _cache_path()
    fingerprint = _fingerprint()
    now = time.time()
    try:
        cached = json.loads(cache_file.read_text(encoding="utf-8"))
    except (OSError, ValueError):
        cached = None
    if isinstance(cached, dict) and cached.get("fingerprint") == fingerprint:
        fresh = now - float(cached.get("generated_at_epoch", 0)) < CACHE_TTL_SECONDS
        reports = cached.get("reports")
        if fresh and isinstance(reports, list) and len(reports) == len(_ADAPTERS):
            out: List[CapabilityReport] = []
            stale = False
            wanted = {a.name for a in _ADAPTERS.values()}
            for data in reports:
                try:
                    report = CapabilityReport(**data)
                except TypeError:
                    stale = True
                    break
                if report.adapter not in wanted:
                    stale = True
                    break
                if report.installed and report.executable and not Path(report.executable).exists():
                    stale = True
                    break
                out.append(report)
            if not stale:
                return out
    live = probe_all()
    try:
        tmp = cache_file.with_suffix(".tmp")
        tmp.write_text(json.dumps({
            "fingerprint": fingerprint,
            "generated_at": time.strftime("%Y-%m-%dT%H:%M:%S"),
            "generated_at_epoch": now,
            "reports": [r.to_dict() for r in live],
        }, ensure_ascii=False, indent=2), encoding="utf-8")
        os.replace(tmp, cache_file)
    except OSError:
        pass
    return live


__all__ = [
    "Adapter", "AdapterUnavailable", "CapabilityReport",
    "get", "all_adapters", "register_custom", "probe_all", "probe_all_cached",
    "clear_probe_cache",
]
