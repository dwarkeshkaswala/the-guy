"""Core data model: repos, plans, tasks, runs, approvals.

Includes the task lifecycle state machine and dependency-DAG validation.
A successful agent process exit is recorded, but never treated as proof that
the code is correct — transitions to `approved`/`integrated` are human-driven.
"""

from __future__ import annotations

import json
from dataclasses import dataclass, field
from datetime import datetime, timezone
from enum import Enum
from typing import Any, Dict, List, Optional


def utcnow() -> str:
    return datetime.now(timezone.utc).isoformat(timespec="seconds")


class TaskStatus(str, Enum):
    DRAFT = "draft"
    APPROVED_FOR_EXECUTION = "approved_for_execution"
    QUEUED = "queued"
    RUNNING = "running"
    NEEDS_REVIEW = "needs_review"
    APPROVED = "approved"
    INTEGRATED = "integrated"
    BLOCKED = "blocked"
    FAILED = "failed"
    CANCELLED = "cancelled"


TERMINAL_STATUSES = {TaskStatus.INTEGRATED, TaskStatus.CANCELLED}

# Deterministic transition table. `approve` is never implied by process exit.
TRANSITIONS: Dict[TaskStatus, set] = {
    TaskStatus.DRAFT: {TaskStatus.APPROVED_FOR_EXECUTION, TaskStatus.BLOCKED, TaskStatus.CANCELLED},
    TaskStatus.APPROVED_FOR_EXECUTION: {TaskStatus.QUEUED, TaskStatus.BLOCKED, TaskStatus.FAILED, TaskStatus.CANCELLED},
    TaskStatus.QUEUED: {TaskStatus.RUNNING, TaskStatus.BLOCKED, TaskStatus.FAILED, TaskStatus.CANCELLED},
    TaskStatus.RUNNING: {TaskStatus.NEEDS_REVIEW, TaskStatus.FAILED, TaskStatus.CANCELLED},
    TaskStatus.NEEDS_REVIEW: {TaskStatus.APPROVED, TaskStatus.FAILED, TaskStatus.CANCELLED},
    TaskStatus.APPROVED: {TaskStatus.INTEGRATED, TaskStatus.CANCELLED},
    TaskStatus.FAILED: {TaskStatus.QUEUED, TaskStatus.CANCELLED},
    TaskStatus.BLOCKED: {TaskStatus.QUEUED, TaskStatus.APPROVED_FOR_EXECUTION, TaskStatus.DRAFT, TaskStatus.FAILED, TaskStatus.CANCELLED},
    TaskStatus.INTEGRATED: set(),
    TaskStatus.CANCELLED: set(),
}


class TransitionError(Exception):
    pass


class PlanError(Exception):
    pass


def transition_allowed(current: str, new: str) -> bool:
    try:
        cur = TaskStatus(current)
        target = TaskStatus(new)
    except ValueError as exc:
        raise TransitionError(f"unknown status: {exc}") from exc
    return target in TRANSITIONS.get(cur, set())


@dataclass
class Repo:
    id: str
    path: str
    alias: str
    default_branch: str
    added_at: str = ""


@dataclass
class Plan:
    id: str
    repo_id: str
    prompt: str
    baseline_branch: str
    baseline_commit: str
    status: str = "draft"  # draft | approved | active | integrated | conflict | cancelled
    created_at: str = ""


@dataclass
class Task:
    id: str
    plan_id: str
    repo_id: str
    title: str
    objective: str
    agent: str
    dependencies: List[str] = field(default_factory=list)
    read_only: bool = False
    allowed_paths: List[str] = field(default_factory=list)
    acceptance_criteria: List[str] = field(default_factory=list)
    required_tests: List[List[str]] = field(default_factory=list)  # list of argv lists
    timeout_seconds: Optional[int] = None
    max_retries: int = 0
    retry_count: int = 0
    status: str = TaskStatus.DRAFT.value
    blocked_reason: Optional[str] = None
    baseline_branch: str = ""
    baseline_commit: str = ""
    worktree: Optional[str] = None
    branch: Optional[str] = None
    created_at: str = ""
    started_at: str = ""
    finished_at: str = ""
    exit_code: Optional[int] = None
    exit_reason: Optional[str] = None
    changed_files: List[str] = field(default_factory=list)
    commits: List[str] = field(default_factory=list)
    test_results: List[Dict[str, Any]] = field(default_factory=list)
    concerns: List[str] = field(default_factory=list)
    options: Dict[str, Any] = field(default_factory=dict)
    handoff: Optional[Dict[str, Any]] = None

    def apply_transition(self, new_status: str, reason: Optional[str] = None) -> None:
        cur = self.status
        if cur == new_status:
            return
        if not transition_allowed(cur, new_status):
            raise TransitionError(
                f"illegal transition for {self.id}: {cur} -> {new_status}"
                + (f" ({reason})" if reason else "")
            )
        self.status = new_status
        if new_status == "blocked":
            self.blocked_reason = reason or "blocked"
        elif cur == "blocked" and new_status != "blocked":
            self.blocked_reason = None

    def to_row(self) -> Dict[str, Any]:
        d = dict(self.__dict__)
        for key in ("dependencies", "allowed_paths", "acceptance_criteria", "required_tests",
                    "changed_files", "commits", "test_results", "concerns", "options", "handoff"):
            d[key] = json.dumps(d[key], ensure_ascii=False)
        d["read_only"] = int(bool(self.read_only))
        return d

    @classmethod
    def from_row(cls, row: Dict[str, Any]) -> "Task":
        d = dict(row)
        for key in ("dependencies", "allowed_paths", "acceptance_criteria", "required_tests",
                    "changed_files", "commits", "test_results", "concerns", "options", "handoff"):
            raw = d.get(key)
            if isinstance(raw, str):
                try:
                    d[key] = json.loads(raw) if raw else ([] if key != "handoff" and key != "options" else None)
                except ValueError:
                    d[key] = [] if key not in ("handoff", "options") else None
            if d.get(key) is None and key in ("dependencies", "allowed_paths", "acceptance_criteria",
                                              "required_tests", "changed_files", "commits",
                                              "test_results", "concerns"):
                d[key] = []
            if d.get(key) is None and key == "options":
                d[key] = {}
        d["read_only"] = bool(d.get("read_only"))
        fields = set(cls.__dataclass_fields__.keys())
        return cls(**{k: v for k, v in d.items() if k in fields})


@dataclass
class RunRecord:
    id: str
    task_id: str
    adapter: str
    argv: List[str]
    cwd: str
    pid: Optional[int] = None
    owner_pid: Optional[int] = None
    status: str = "running"  # starting|running|finished|failed|cancelled|cancel-requested
    exit_code: Optional[int] = None
    failure_class: Optional[str] = None  # spawn_error|timeout|nonzero|cancelled|interrupted
    started_at: str = ""
    finished_at: str = ""
    duration_ms: Optional[int] = None
    log_path: Optional[str] = None

    def to_row(self) -> Dict[str, Any]:
        d = dict(self.__dict__)
        d["argv"] = json.dumps(d["argv"], ensure_ascii=False)
        return d

    @classmethod
    def from_row(cls, row: Dict[str, Any]) -> "RunRecord":
        d = dict(row)
        raw = d.get("argv")
        if isinstance(raw, str):
            try:
                d["argv"] = json.loads(raw) if raw else []
            except ValueError:
                d["argv"] = []
        fields = set(cls.__dataclass_fields__.keys())
        return cls(**{k: v for k, v in d.items() if k in fields})


@dataclass
class ApprovalRecord:
    id: str
    target_kind: str  # task | plan
    target_id: str
    kind: str  # execute | review | integration | commit | tests
    note: str = ""
    created_at: str = ""


def validate_dag(tasks: List[Dict[str, Any]]) -> List[str]:
    """Validate dependency DAG over plan-task dicts with a `key` field.

    Returns topological order of keys. Raises PlanError on missing deps or cycles.
    """
    keys = [t["key"] for t in tasks]
    if len(set(keys)) != len(keys):
        raise PlanError("duplicate task keys in plan")
    keyset = set(keys)
    deps: Dict[str, List[str]] = {}
    for t in tasks:
        task_deps = t.get("dependencies") or []
        for dep in task_deps:
            if dep not in keyset:
                raise PlanError(f"task '{t['key']}' depends on unknown task '{dep}'")
        deps[t["key"]] = list(task_deps)
    # Kahn's algorithm
    order: List[str] = []
    ready = sorted(k for k in keys if not deps[k])
    remaining = {k: set(v) for k, v in deps.items()}
    while ready:
        node = ready.pop(0)
        order.append(node)
        for other, dset in remaining.items():
            if node in dset:
                dset.discard(node)
                if not dset and other not in order and other not in ready:
                    ready.append(other)
    if len(order) != len(keys):
        unresolved = sorted(set(keys) - set(order))
        raise PlanError(f"dependency cycle detected involving: {', '.join(unresolved)}")
    return order
