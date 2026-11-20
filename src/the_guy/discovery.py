"""Cross-platform CLI discovery for agent executables.

Order of precedence (explicit user intent wins; no silent fallback):
1. Explicit override — ``GUY_<NAME>_PATH`` env var, or the persisted user
   config set via ``guy agents set <name> <path>``.
2. PATH scan (directory order).
3. Well-known user-level install locations (npm global, pnpm, yarn, volta,
   scoop, cargo, ~/.local/bin, homebrew, ...).

Windows specifics: ``.exe`` > ``.cmd`` > ``.bat`` > bare file are launchable;
``.ps1`` shims are DISCOVERED but never launched directly (CreateProcess
cannot run them and PowerShell execution policy may block them anyway) —
they produce a clear diagnostic instead. A stale configured override disables
the adapter with instructions; the-guy never falls back to another executable
or another agent on its own.

All checks are read-only (``--version`` / ``--help``); nothing here installs
software or modifies PATH.
"""

from __future__ import annotations

import os
import re
import shutil
import subprocess
from dataclasses import dataclass, field
from functools import lru_cache
from pathlib import Path
from typing import Dict, List, Optional, Tuple

if os.name == "nt":
    _EXT_PRIORITY: Tuple[Tuple[str, int], ...] = (
        (".exe", 1), (".cmd", 2), (".bat", 3), ("", 4), (".ps1", 99),
    )
    _SUPPORTED_SUFFIXES = {".exe", ".cmd", ".bat", ""}
else:
    _EXT_PRIORITY = (("", 1),)
    _SUPPORTED_SUFFIXES = {""}

PS1_NOTE = (
    "only a PowerShell shim (.ps1) was found; the-guy does not launch .ps1 "
    "files directly (PowerShell execution policy may also block them). Use "
    "the .cmd/.exe shim in the same folder or reinstall the CLI so one exists."
)

_SOURCE_PRIORITY = {"env": 0, "config": 0, "PATH": 1, "well-known": 2}


@dataclass
class DiscoveryResult:
    names: Tuple[str, ...] = ()
    env_var: Optional[str] = None
    override: Optional[str] = None
    override_source: Optional[str] = None  # env | config
    override_error: Optional[str] = None
    chosen: Optional[Path] = None
    chosen_source: Optional[str] = None  # env | config | PATH | well-known
    ps1_only: bool = False
    ps1_path: Optional[Path] = None
    path_dirs_scanned: int = 0
    well_known: List[Dict[str, object]] = field(default_factory=list)
    hits: List[Dict[str, object]] = field(default_factory=list)

    def to_dict(self) -> Dict[str, object]:
        return {
            "names": list(self.names),
            "env_var": self.env_var,
            "override": self.override,
            "override_source": self.override_source,
            "override_error": self.override_error,
            "chosen": str(self.chosen) if self.chosen else None,
            "chosen_source": self.chosen_source,
            "ps1_only": self.ps1_only,
            "ps1_path": str(self.ps1_path) if self.ps1_path else None,
            "path_dirs_scanned": self.path_dirs_scanned,
            "well_known": self.well_known,
            "hits": self.hits,
        }


def path_directories() -> List[Path]:
    out: List[Path] = []
    for part in os.environ.get("PATH", "").split(os.pathsep):
        if not part:
            continue
        try:
            out.append(Path(part))
        except (ValueError, OSError):
            continue
    return out


_FACTORY_APP_RE = re.compile(r"^app-(\d+(?:\.\d+)*)$")


@lru_cache(maxsize=1)
def npm_global_prefix() -> Optional[Path]:
    """Resolve the global npm prefix: ask npm (read-only), else npm's default.

    ``npm config get prefix`` is the authoritative answer for where ``npm i -g``
    puts shims, so npm-installed agents (claude/opencode/codex) are found even
    when that directory is not on PATH. Falls back to npm's documented Windows
    default (``%APPDATA%\\npm``) when npm itself is not callable.
    """
    names = ("npm", "npm.cmd", "npm.exe") if os.name == "nt" else ("npm",)
    for name in names:
        exe = shutil.which(name)
        if not exe:
            continue
        try:
            proc = subprocess.run([exe, "config", "get", "prefix"],
                                  capture_output=True, timeout=20)
        except (OSError, subprocess.TimeoutExpired):
            continue
        if proc.returncode != 0:
            continue
        lines = [line.strip().strip('"')
                 for line in proc.stdout.decode("utf-8", errors="replace").splitlines()
                 if line.strip()]
        if not lines:
            continue
        try:
            prefix = Path(lines[-1])
        except ValueError:
            continue
        if prefix.is_dir():
            return prefix
    appdata = os.environ.get("APPDATA")
    if appdata and (Path(appdata) / "npm").is_dir():
        return Path(appdata) / "npm"
    return None


def factory_droid_roots() -> List[Tuple[Path, str]]:
    """Factory Droid install bins, newest version first.

    Scans ``%LOCALAPPDATA%\\Factory\\app-*\\resources\\bin`` and orders the
    ``app-*`` directories by NUMERIC version comparison (1.10.0 > 1.9.0),
    never alphabetically. Directories without a ``resources\\bin`` are skipped,
    so a broken newest install falls through to the next valid one.
    """
    if os.name != "nt":
        return []
    local = os.environ.get("LOCALAPPDATA")
    if not local:
        return []
    base = Path(local) / "Factory"
    try:
        entries = list(base.iterdir())
    except OSError:
        return []
    versioned: List[Tuple[Tuple[int, ...], str, Path]] = []
    for entry in entries:
        match = _FACTORY_APP_RE.match(entry.name)
        if not match or not entry.is_dir():
            continue
        version = tuple(int(part) for part in match.group(1).split("."))
        while version and version[-1] == 0:  # normalize 1.16.0 == 1.16
            version = version[:-1]
        versioned.append((version, entry.name, entry))
    versioned.sort(key=lambda t: (t[0], t[1]), reverse=True)
    roots: List[Tuple[Path, str]] = []
    for _version, name, entry in versioned:
        bin_dir = entry / "resources" / "bin"
        if bin_dir.is_dir():
            roots.append((bin_dir, f"Factory Droid install ({name})"))
    return roots


def adapter_roots(names: List[str]) -> List[Tuple[Path, str]]:
    """Adapter-specific dynamic install locations (scanned after PATH)."""
    primary = (names[0] if names else "").lower()
    if primary == "droid":
        return factory_droid_roots()
    return []


def labeled_well_known_roots() -> List[Tuple[Path, str]]:
    """Common user-level install locations with diagnostic labels, in priority order.

    The npm global prefix is resolved from npm itself when possible; everything
    is derived from environment variables (no hardcoded usernames/dirs).
    """
    entries: List[Tuple[Path, str]] = []
    prefix = npm_global_prefix()
    if prefix is not None:
        entries.append((prefix, "npm global prefix"))
    home = Path.home()
    if os.name == "nt":
        appdata = os.environ.get("APPDATA")
        local = os.environ.get("LOCALAPPDATA")
        if appdata and Path(appdata) / "npm" != prefix:
            entries.append((Path(appdata) / "npm", "npm default prefix (%APPDATA%\\npm)"))
        if local:
            entries += [
                (Path(local) / "pnpm", "pnpm global"),
                (Path(local) / "Yarn" / "bin", "yarn global"),
                (Path(local) / "Volta" / "bin", "volta"),
            ]
        entries += [
            (home / ".volta" / "bin", "volta (home)"),
            (home / "scoop" / "shims", "scoop shims"),
            (home / ".cargo" / "bin", "cargo"),
            (home / ".local" / "bin", "user local bin"),
            (home / "bin", "personal bin"),
        ]
    else:
        entries += [
            (home / ".local" / "bin", "user local bin"),
            (home / "bin", "personal bin"),
            (home / ".volta" / "bin", "volta"),
            (home / ".cargo" / "bin", "cargo"),
            (Path("/usr/local/bin"), ""),
            (Path("/opt/homebrew/bin"), "homebrew"),
            (Path("/opt/local/bin"), "macports"),
        ]
    seen = set()
    unique: List[Tuple[Path, str]] = []
    for path, label in entries:
        key = str(path).lower() if os.name == "nt" else str(path)
        if key not in seen:
            seen.add(key)
            unique.append((path, label))
    return unique


def well_known_roots() -> List[Path]:
    return [path for path, _label in labeled_well_known_roots()]


def vscode_extension_dir() -> Optional[Path]:
    env = os.environ.get("GUY_VSCODE_EXTENSIONS_DIR")
    if env:
        return Path(env)
    return Path.home() / ".vscode" / "extensions"


_VSCODE_KEYWORDS = {
    "claude": ("claude",),
    "opencode": ("opencode",),
    "codex": ("codex", "chatgpt"),
    "droid": ("droid", "factory"),
}


def vscode_extension_hit(adapter_name: str) -> Optional[str]:
    """Return an installed VS Code extension name that looks related, or None.

    This is a HINT only: a VS Code extension never counts as a global CLI.
    """
    base = vscode_extension_dir()
    if base is None or not base.is_dir():
        return None
    keywords = _VSCODE_KEYWORDS.get(adapter_name, (adapter_name,))
    try:
        entries = sorted(e.name for e in base.iterdir())
    except OSError:
        return None
    for name in entries:
        low = name.lower()
        if any(k in low for k in keywords):
            return name
    return None


def _dir_variants(directory: Path, name: str) -> List[Tuple[Path, int]]:
    found: List[Tuple[Path, int]] = []
    for ext, rank in _EXT_PRIORITY:
        try:
            p = directory / (name + ext)
            if p.is_file():
                found.append((p, rank))
        except (OSError, ValueError):
            continue
    return found


def _kind_of(path: Path) -> str:
    return path.suffix.lower().lstrip(".") or "binary"


def resolve(names: List[str], env_var: Optional[str] = None,
            config_path: Optional[str] = None) -> DiscoveryResult:
    """Find the best executable for an adapter. Read-only; never launches it."""
    primary = names[0]
    result = DiscoveryResult(names=tuple(names), env_var=env_var)
    all_hits: List[Tuple[Path, str, int]] = []
    seen_paths = set()

    def record(directory: Path, source: str) -> None:
        for name in names:
            for path, rank in _dir_variants(directory, name):
                key = str(path).lower() if os.name == "nt" else str(path)
                if key in seen_paths:
                    continue
                seen_paths.add(key)
                all_hits.append((path, source, rank))

    env_value = os.environ.get(env_var) if env_var else None
    override = env_value or config_path or None
    if override:
        result.override = override
        result.override_source = "env" if env_value else "config"
        p = Path(override).expanduser()
        variants: List[Tuple[Path, int]] = []
        if p.is_dir():
            variants = _dir_variants(p, primary)
            if not variants:
                result.override_error = (
                    f"configured path is a directory with no '{primary}' executable in it: {p}")
        elif p.is_file():
            suffix = p.suffix.lower()
            if suffix in _SUPPORTED_SUFFIXES:
                rank = dict(_EXT_PRIORITY).get(suffix, 4)
                variants = [(p, rank)]
            else:
                result.override_error = (
                    f"configured path is a '{suffix or 'plain'}' file the-guy cannot launch "
                    f"directly: {p} (use the .exe/.cmd launcher; .ps1 cannot be launched "
                    "and may be blocked by PowerShell execution policy)")
        else:
            result.override_error = (
                f"configured executable path no longer exists: {p} "
                f"(set via {result.override_source}; fix with "
                f"`guy agents set {primary} <path>` or clear it with "
                f"`guy agents clear {primary}`)")
        if variants:
            best = min(variants, key=lambda t: t[1])
            if best[1] >= 99:
                result.ps1_only, result.ps1_path = True, best[0]
                result.override_error = PS1_NOTE
            else:
                result.chosen, result.chosen_source = best[0], result.override_source
        else:
            # informational scan only (diagnostics); chosen stays None — no fallback
            _scan(result, record, all_hits)
        _finalize(result, all_hits)
        return result

    _scan(result, record, all_hits)
    _finalize(result, all_hits)
    return result


def _scan(result: DiscoveryResult, record, all_hits: List[Tuple[Path, str, int]]) -> None:
    seen_dirs = set()
    for d in path_directories():
        key = str(d).lower() if os.name == "nt" else str(d)
        if key in seen_dirs:
            continue
        seen_dirs.add(key)
        record(d, "PATH")
        result.path_dirs_scanned += 1
    roots = adapter_roots(list(result.names)) + labeled_well_known_roots()
    for root, label in roots:
        present = root.is_dir()
        before = len(all_hits)
        if present:
            record(root, "well-known")
        result.well_known.append({
            "root": str(root),
            "label": label,
            "present": present,
            "hit": str(all_hits[-1][0]) if present and len(all_hits) > before else None,
        })


def _finalize(result: DiscoveryResult, all_hits: List[Tuple[Path, str, int]]) -> None:
    result.hits = [
        {"path": str(p), "source": s, "kind": _kind_of(p)}
        for p, s, _rank in all_hits
    ]
    usable = [h for h in all_hits if h[2] < 99]
    ps1s = [h for h in all_hits if h[2] >= 99]
    if usable:
        best = min(usable, key=lambda h: (_SOURCE_PRIORITY.get(h[1], 9), h[2]))
        result.chosen, result.chosen_source = best[0], best[1]
    elif ps1s:
        result.ps1_only, result.ps1_path = True, ps1s[0][0]
