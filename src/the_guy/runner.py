"""Subprocess runner: bounded redacted logs, timeouts, process-tree kill.

Agent processes are launched with argv lists (no shell), the worktree as cwd,
and the ambient environment preserved (agents reuse their own CLI login;
the-guy never persists or prints credentials). Output is redacted before it
reaches any log file.
"""

from __future__ import annotations

import os
import subprocess
import sys
import threading
import time
from dataclasses import dataclass, field
from pathlib import Path
from typing import Callable, List, Optional

from the_guy import security


def kill_tree(pid: int) -> None:
    """Kill an entire process tree. Windows: taskkill /T. POSIX: process group."""
    if pid is None or pid <= 0:
        return
    try:
        if os.name == "nt":
            subprocess.run(
                ["taskkill", "/F", "/T", "/PID", str(pid)],
                capture_output=True, timeout=30,
            )
        else:
            import signal
            os.killpg(os.getpgid(pid), signal.SIGKILL)
    except (OSError, subprocess.TimeoutExpired, ProcessLookupError, PermissionError):
        try:
            os.kill(pid, 9 if os.name != "nt" else 9)
        except OSError:
            pass


def pid_alive(pid: Optional[int]) -> bool:
    if not pid or pid <= 0:
        return False
    if os.name == "nt":
        try:
            proc = subprocess.run(
                ["tasklist", "/FI", f"PID eq {pid}", "/FO", "CSV", "/NH"],
                capture_output=True, timeout=15,
            )
        except (OSError, subprocess.TimeoutExpired):
            return False
        out = proc.stdout.decode("utf-8", errors="replace")
        return f'"{pid}"' in out
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


class CappedLog:
    """Append-only log with redaction and a hard size cap (keeps the tail)."""

    TRUNCATION_NOTE = "\n[... log truncated to keep the most recent output ...]\n"

    def __init__(self, path: Path, max_bytes: int):
        self.path = Path(path)
        self.max_bytes = max_bytes
        self._fh = None

    def open(self) -> "CappedLog":
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._fh = open(self.path, "a", encoding="utf-8", errors="replace")
        return self

    def write(self, text: str) -> None:
        if self._fh is None:
            self.open()
        assert self._fh is not None
        self._fh.write(security.redact(text))
        self._fh.flush()
        self._shrink_if_needed()

    def _shrink_if_needed(self) -> None:
        try:
            size = self.path.stat().st_size
        except OSError:
            return
        if size <= self.max_bytes:
            return
        keep = self.max_bytes // 2
        try:
            raw = self.path.read_bytes()
        except OSError:
            return
        tail = raw[-keep:]
        self._fh.close()
        with open(self.path, "wb") as fh:
            fh.write(self.TRUNCATION_NOTE.encode("utf-8"))
            fh.write(tail)
        self._fh = open(self.path, "a", encoding="utf-8", errors="replace")

    def close(self) -> None:
        if self._fh:
            self._fh.close()
            self._fh = None


@dataclass
class RunOutcome:
    exit_code: Optional[int] = None
    timed_out: bool = False
    cancelled: bool = False
    spawn_error: Optional[str] = None
    duration_ms: int = 0
    failure_class: Optional[str] = None
    log_path: Optional[Path] = None
    notes: List[str] = field(default_factory=list)


def execute(argv: List[str],
            cwd: Path,
            env: dict,
            timeout_s: Optional[float],
            log_path: Path,
            max_log_bytes: int = 1_000_000,
            status_poll: Optional[Callable[[], Optional[str]]] = None,
            on_start: Optional[Callable[[int], None]] = None) -> RunOutcome:
    """Run a subprocess to completion with timeout/cancel support.

    ``status_poll`` is consulted every ~0.3s; returning "cancel-requested"
    kills the process tree (used by `guy cancel` across processes).
    """
    log = CappedLog(log_path, max_log_bytes).open()
    started = time.monotonic()
    header = [
        f"=== the-guy run ===",
        f"time: {time.strftime('%Y-%m-%dT%H:%M:%S %z')}",
        f"cwd: {cwd}",
        f"command: {security.display_argv(argv)}",
        f"timeout: {timeout_s if timeout_s else 'none'}s",
    ]
    log.write("\n".join(header) + "\n\n")
    popen_kwargs = {}
    if os.name != "nt":
        popen_kwargs["start_new_session"] = True
    try:
        proc = subprocess.Popen(
            [str(a) for a in argv],
            cwd=str(cwd),
            env=env,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            **popen_kwargs,
        )
    except OSError as exc:
        log.write(f"\n[the-guy] failed to start process: {exc}\n")
        log.close()
        return RunOutcome(spawn_error=str(exc), failure_class="spawn_error",
                          duration_ms=int((time.monotonic() - started) * 1000),
                          log_path=log_path)

    if on_start:
        try:
            on_start(proc.pid)
        except Exception:
            pass

    pump_done = threading.Event()

    def pump() -> None:
        try:
            for raw_line in proc.stdout:
                log.write(raw_line.decode("utf-8", errors="replace"))
        except (OSError, ValueError):
            pass
        finally:
            pump_done.set()

    reader = threading.Thread(target=pump, daemon=True)
    reader.start()

    deadline = (time.monotonic() + timeout_s) if timeout_s else None
    timed_out = cancelled = False
    while True:
        rc = proc.poll()
        if rc is not None:
            break
        if status_poll is not None and status_poll() == "cancel-requested":
            kill_tree(proc.pid)
            cancelled = True
            break
        if deadline is not None and time.monotonic() > deadline:
            kill_tree(proc.pid)
            timed_out = True
            break
        time.sleep(0.3)

    try:
        rc = proc.wait(timeout=30)
    except subprocess.TimeoutExpired:
        kill_tree(proc.pid)
        rc = proc.wait(timeout=10)
    pump_done.wait(timeout=15)
    reader.join(timeout=5)

    # `guy cancel` kills the tree itself after marking cancel-requested, so the
    # process may exit before the poll above sees the request. Re-check once
    # so a user cancel is never misclassified as a crash.
    if not cancelled and not timed_out and status_poll is not None \
            and status_poll() == "cancel-requested":
        cancelled = True

    duration_ms = int((time.monotonic() - started) * 1000)
    if cancelled:
        failure_class, note = "cancelled", "cancelled by user; process tree killed"
    elif timed_out:
        failure_class, note = "timeout", f"timed out after {timeout_s}s; process tree killed"
    elif rc != 0:
        failure_class, note = "nonzero", f"agent exited with code {rc}"
    else:
        failure_class, note = None, "agent exited 0 (note: exit 0 is not proof the work is correct)"
    log.write(f"\n[the-guy] {note}\n[the-guy] duration: {duration_ms} ms\n")
    log.close()
    return RunOutcome(exit_code=rc, timed_out=timed_out, cancelled=cancelled,
                      duration_ms=duration_ms, failure_class=failure_class,
                      log_path=log_path)
