"""Security helpers: secret redaction, path containment, safe identifiers.

All untrusted text (agent output, repo content, plan files) passes through
``redact`` before being written to logs or echoed to the terminal.
"""

from __future__ import annotations

import re
import subprocess
from pathlib import Path


class SecurityError(Exception):
    pass


_REDACTION_PATTERNS = [
    # OpenAI-style keys
    re.compile(r"\b(sk|rk)-[A-Za-z0-9_-]{8,}\b"),
    # GitHub tokens
    re.compile(r"\bghp_[A-Za-z0-9]{20,}\b"),
    re.compile(r"\bgithub_pat_[A-Za-z0-9_]{20,}\b"),
    # GitLab tokens
    re.compile(r"\bglpat-[A-Za-z0-9_-]{10,}\b"),
    # Slack tokens
    re.compile(r"\bxox[baprs]-[A-Za-z0-9-]{10,}\b"),
    # AWS access key ids
    re.compile(r"\bAKIA[0-9A-Z]{16}\b"),
    # PEM private key blocks
    re.compile(r"-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----"),
    # Bearer headers
    re.compile(r"(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{12,}"),
    # key/token/secret/password assignments
    re.compile(r"(?i)\b(api[_-]?key|apikey|secret|token|password|passwd|pwd|authorization)\b['\"]?\s*[:=]\s*\S+"),
    # URLs with embedded credentials (any scheme)
    re.compile(r"(?i)\b[a-z][a-z0-9+.-]*://[^\s/@:]+:[^\s/@]+@"),
]
_REPLACEMENT = "[REDACTED]"


def redact(text: str) -> str:
    """Replace common secret formats with [REDACTED]."""
    for pattern in _REDACTION_PATTERNS:
        text = pattern.sub(_REPLACEMENT, text)
    return text


def ensure_within(parent: Path | str, child: Path | str) -> Path:
    """Return ``child`` resolved, raising SecurityError if it escapes ``parent``."""
    parent = Path(parent).resolve()
    child = Path(child).resolve()
    if child != parent and parent not in child.parents:
        raise SecurityError(f"path {child} escapes boundary {parent}")
    return child


_SLUG_RE = re.compile(r"[^a-zA-Z0-9._-]+")


def slug(text: str, max_len: int = 40) -> str:
    """Turn arbitrary text into a safe filesystem-name slug."""
    s = _SLUG_RE.sub("-", str(text).strip()).strip("-.").lower()
    s = s[:max_len].rstrip("-.")
    return s or "task"


_REF_RE = re.compile(r"[^A-Za-z0-9._/-]+")


def safe_ref(name: str) -> str:
    """Sanitize a string for use as one component of a git ref name."""
    s = _REF_RE.sub("-", str(name).strip())
    s = re.sub(r"-{2,}", "-", s).strip("-./")
    if not s:
        s = "branch"
    if s.startswith("-"):
        s = s[1:] or "branch"
    if s.endswith(".lock"):
        s = s[:-5] or "branch"
    return s


def display_argv(argv) -> str:
    """Render an argv list as a redacted, copy-safe command string."""
    parts = [str(a) for a in argv]
    try:
        rendered = subprocess.list2cmdline(parts)
    except Exception:
        rendered = " ".join(parts)
    return redact(rendered)
