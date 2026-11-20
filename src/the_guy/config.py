"""Configuration: OS-aware paths, user preferences, per-repo optional config.

Precedence (highest wins): CLI arguments > repo config (``.guy/project.json``
or a tiny YAML subset in ``.guy/project.yaml``) > user preferences > built-in
defaults.

Runtime state lives OUTSIDE target repositories:
- Windows: data in ``%LOCALAPPDATA%\\the-guy``, preferences in ``%APPDATA%\\the-guy``
- POSIX:   data in ``$XDG_DATA_HOME/the-guy`` (default ``~/.local/share/the-guy``),
           preferences in ``$XDG_CONFIG_HOME/the-guy`` (default ``~/.config/the-guy``)
- Tests isolate everything via ``GUY_DATA_DIR`` / ``GUY_PREFS_DIR``.
"""

from __future__ import annotations

import json
import os
from pathlib import Path
from typing import Any, Dict, List, Optional

APP_NAME = "the-guy"

BUILTIN_DEFAULTS: Dict[str, Any] = {
    "max_workers": 2,  # conservative: providers may share one billing allowance
    "default_agent": None,  # None = auto-detect first verified real adapter
    "run_timeout_seconds": 1800,
    "planner_timeout_seconds": 300,
    "log_max_bytes": 1_000_000,
    "log_retention_days": 30,
    "safe_retry_failure_classes": ["spawn_error"],
}


class ConfigError(Exception):
    pass


def data_dir() -> Path:
    env = os.environ.get("GUY_DATA_DIR")
    if env:
        return Path(env)
    if os.name == "nt":
        base = os.environ.get("LOCALAPPDATA") or str(Path.home() / "AppData" / "Local")
        return Path(base) / APP_NAME
    xdg = os.environ.get("XDG_DATA_HOME")
    if xdg:
        return Path(xdg) / APP_NAME
    return Path.home() / ".local" / "share" / APP_NAME


def prefs_dir() -> Path:
    env = os.environ.get("GUY_PREFS_DIR")
    if env:
        return Path(env)
    if os.name == "nt":
        base = os.environ.get("APPDATA") or str(Path.home() / "AppData" / "Roaming")
        return Path(base) / APP_NAME
    xdg = os.environ.get("XDG_CONFIG_HOME")
    if xdg:
        return Path(xdg) / APP_NAME
    return Path.home() / ".config" / APP_NAME


def db_path() -> Path:
    return data_dir() / "state.db"


def logs_root() -> Path:
    return data_dir() / "logs"


def worktrees_root() -> Path:
    return data_dir() / "worktrees"


def ensure_dirs() -> None:
    for p in (data_dir(), prefs_dir(), logs_root(), worktrees_root()):
        p.mkdir(parents=True, exist_ok=True)


def prefs_path() -> Path:
    return prefs_dir() / "prefs.json"


def read_prefs() -> Dict[str, Any]:
    try:
        return json.loads(prefs_path().read_text(encoding="utf-8"))
    except (OSError, ValueError):
        return {}


def write_prefs(prefs: Dict[str, Any]) -> None:
    path = prefs_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    tmp = path.with_suffix(".tmp")
    tmp.write_text(json.dumps(prefs, indent=2, ensure_ascii=False), encoding="utf-8")
    os.replace(tmp, path)  # atomic


def default_repo_ref() -> Optional[str]:
    return read_prefs().get("default_repo")


def set_default_repo(ref: Optional[str]) -> None:
    prefs = read_prefs()
    if ref is None:
        prefs.pop("default_repo", None)
    else:
        prefs["default_repo"] = ref
    write_prefs(prefs)


def user_settings() -> Dict[str, Any]:
    merged = dict(BUILTIN_DEFAULTS)
    merged.update(read_prefs().get("settings", {}))
    return merged


def set_user_setting(key: str, value: Any) -> None:
    if key not in BUILTIN_DEFAULTS:
        raise ConfigError(
            f"unknown setting {key!r}; known settings: {', '.join(sorted(BUILTIN_DEFAULTS))}"
        )
    prefs = read_prefs()
    prefs.setdefault("settings", {})[key] = value
    write_prefs(prefs)


def adapter_overrides() -> Dict[str, str]:
    """Persisted executable overrides set via `guy agents set` (prefs.json)."""
    raw = read_prefs().get("adapters") or {}
    return {str(k): str(v) for k, v in raw.items()} if isinstance(raw, dict) else {}


def set_adapter_override(name: str, path: Optional[str]) -> None:
    prefs = read_prefs()
    overrides = prefs.setdefault("adapters", {})
    if path is None:
        overrides.pop(name, None)
    else:
        overrides[name] = path
    write_prefs(prefs)


def _parse_scalar(token: str) -> Any:
    token = token.strip()
    if len(token) >= 2 and token[0] == token[-1] and token[0] in "\"'":
        return token[1:-1]
    low = token.lower()
    if low in ("true", "yes"):
        return True
    if low in ("false", "no"):
        return False
    if low in ("null", "~", ""):
        return None
    try:
        return int(token)
    except ValueError:
        return token


def load_yaml_subset(text: str) -> Dict[str, Any]:
    """Parse the tiny documented YAML subset allowed in .guy/project.yaml.

    Supports: comments, ``key: value``, one level of nesting, and
    ``key:`` followed by ``- item`` lists. Anything else raises ConfigError;
    use .guy/project.json for anything richer.
    """
    root: Dict[str, Any] = {}
    # stack entries: [indent, container, parent, key_in_parent]
    stack: List[Any] = [[-1, root, None, None]]
    for raw in text.splitlines():
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        indent = len(raw) - len(raw.lstrip(" "))
        line = raw.strip()
        while len(stack) > 1 and indent <= stack[-1][0]:
            stack.pop()
        container = stack[-1][1]
        if line.startswith("- "):
            value = _parse_scalar(line[2:])
            if isinstance(container, list):
                container.append(value)
            elif isinstance(container, dict) and not container:
                parent, key = stack[-1][2], stack[-1][3]
                if parent is None or key is None:
                    raise ConfigError(f"unsupported list item in .guy config: {raw!r}")
                parent[key] = [value]  # `key:` block becomes a list
                stack[-1][1] = parent[key]
            else:
                raise ConfigError(f"unsupported list item in .guy config: {raw!r}")
        elif ":" in line:
            key, _, rest = line.partition(":")
            key = key.strip()
            if not key:
                raise ConfigError(f"bad line in .guy config: {raw!r}")
            if rest.strip() == "":
                container[key] = {}
                stack.append([indent, container[key], container, key])
            else:
                container[key] = _parse_scalar(rest)
        else:
            raise ConfigError(
                f"unsupported line in .guy config: {raw!r} "
                "(only 'key: value' and '- item' lines are supported; use project.json)"
            )
    return root


def repo_settings(repo_path: Path | str) -> Dict[str, Any]:
    """Read optional per-repo config from .guy/project.json or .guy/project.yaml."""
    guy_dir = Path(repo_path) / ".guy"
    json_file = guy_dir / "project.json"
    if json_file.is_file():
        try:
            data = json.loads(json_file.read_text(encoding="utf-8"))
        except ValueError as exc:
            raise ConfigError(f"invalid {json_file}: {exc}") from exc
        if not isinstance(data, dict):
            raise ConfigError(f"{json_file} must contain a JSON object")
        return data
    yaml_file = guy_dir / "project.yaml"
    if yaml_file.is_file():
        return load_yaml_subset(yaml_file.read_text(encoding="utf-8"))
    return {}


def effective_settings(cli_overrides: Optional[Dict[str, Any]] = None,
                       repo_path: Optional[Path | str] = None) -> Dict[str, Any]:
    """Merge settings: CLI > repo config > user prefs > built-in defaults."""
    merged = user_settings()
    if repo_path is not None:
        repo_cfg = repo_settings(repo_path)
        for key in BUILTIN_DEFAULTS:
            if key in repo_cfg:
                merged[key] = repo_cfg[key]
    if cli_overrides:
        for key, value in cli_overrides.items():
            if value is not None:
                merged[key] = value
    return merged
