"""SQLite-backed durable state: repos, plans, tasks, runs, events, approvals.

A single connection guarded by a re-entrant lock; WAL journaling. All writes
are transactional. Threads in the scheduler share one Store instance.
"""

from __future__ import annotations

import sqlite3
import threading
from pathlib import Path
from typing import Any, Dict, List, Optional

from the_guy import config
from the_guy.models import ApprovalRecord, Plan, Repo, RunRecord, Task, utcnow

SCHEMA = """
CREATE TABLE IF NOT EXISTS meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS repos(
    id TEXT PRIMARY KEY,
    path TEXT NOT NULL UNIQUE,
    alias TEXT NOT NULL,
    default_branch TEXT NOT NULL DEFAULT '',
    added_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS plans(
    id TEXT PRIMARY KEY,
    repo_id TEXT NOT NULL,
    prompt TEXT NOT NULL DEFAULT '',
    baseline_branch TEXT NOT NULL DEFAULT '',
    baseline_commit TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'draft',
    created_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS tasks(
    id TEXT PRIMARY KEY,
    plan_id TEXT NOT NULL,
    repo_id TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    objective TEXT NOT NULL DEFAULT '',
    agent TEXT NOT NULL DEFAULT '',
    dependencies TEXT NOT NULL DEFAULT '[]',
    read_only INTEGER NOT NULL DEFAULT 0,
    allowed_paths TEXT NOT NULL DEFAULT '[]',
    acceptance_criteria TEXT NOT NULL DEFAULT '[]',
    required_tests TEXT NOT NULL DEFAULT '[]',
    timeout_seconds INTEGER,
    max_retries INTEGER NOT NULL DEFAULT 0,
    retry_count INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL DEFAULT 'draft',
    blocked_reason TEXT,
    baseline_branch TEXT NOT NULL DEFAULT '',
    baseline_commit TEXT NOT NULL DEFAULT '',
    worktree TEXT,
    branch TEXT,
    created_at TEXT NOT NULL DEFAULT '',
    started_at TEXT,
    finished_at TEXT,
    exit_code INTEGER,
    exit_reason TEXT,
    changed_files TEXT NOT NULL DEFAULT '[]',
    commits TEXT NOT NULL DEFAULT '[]',
    test_results TEXT NOT NULL DEFAULT '[]',
    concerns TEXT NOT NULL DEFAULT '[]',
    options TEXT,
    handoff TEXT
);
CREATE TABLE IF NOT EXISTS runs(
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    adapter TEXT NOT NULL DEFAULT '',
    argv TEXT NOT NULL DEFAULT '[]',
    cwd TEXT NOT NULL DEFAULT '',
    pid INTEGER,
    owner_pid INTEGER,
    status TEXT NOT NULL DEFAULT 'starting',
    exit_code INTEGER,
    failure_class TEXT,
    started_at TEXT NOT NULL DEFAULT '',
    finished_at TEXT,
    duration_ms INTEGER,
    log_path TEXT
);
CREATE TABLE IF NOT EXISTS events(
    seq INTEGER PRIMARY KEY AUTOINCREMENT,
    ts TEXT NOT NULL,
    kind TEXT NOT NULL,
    repo_id TEXT,
    task_id TEXT,
    payload TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS approvals(
    id TEXT PRIMARY KEY,
    target_kind TEXT NOT NULL,
    target_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    note TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL DEFAULT ''
);
"""


def _row_to_dict(row: sqlite3.Row) -> Dict[str, Any]:
    return {k: row[k] for k in row.keys()}


class Store:
    def __init__(self, path: Optional[Path] = None):
        self.path = Path(path) if path else config.db_path()
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self._lock = threading.RLock()
        self._conn = sqlite3.connect(str(self.path), check_same_thread=False, timeout=30)
        self._conn.row_factory = sqlite3.Row
        self._conn.execute("PRAGMA journal_mode=WAL")
        self._conn.execute("PRAGMA foreign_keys=ON")
        with self._lock:
            self._conn.executescript(SCHEMA)
            self._conn.commit()

    def close(self) -> None:
        with self._lock:
            self._conn.close()

    # --- ids ---------------------------------------------------------------
    def next_id(self, prefix: str) -> str:
        with self._lock:
            key = f"seq_{prefix.lower()}"
            row = self._conn.execute("SELECT value FROM meta WHERE key=?", (key,)).fetchone()
            n = (int(row["value"]) + 1) if row else 1
            self._conn.execute(
                "INSERT INTO meta(key, value) VALUES(?,?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (key, str(n)),
            )
            self._conn.commit()
            return f"{prefix}-{n:03d}"

    # --- repos ---------------------------------------------------------------
    def upsert_repo(self, repo: Repo) -> None:
        with self._lock:
            self._conn.execute(
                "INSERT OR REPLACE INTO repos(id, path, alias, default_branch, added_at)"
                " VALUES(?,?,?,?,?)",
                (repo.id, repo.path, repo.alias, repo.default_branch, repo.added_at),
            )
            self._conn.commit()

    def list_repos(self) -> List[Repo]:
        with self._lock:
            rows = self._conn.execute("SELECT * FROM repos ORDER BY alias").fetchall()
        return [Repo(**_row_to_dict(r)) for r in rows]

    def get_repo(self, ref: Optional[str] = None) -> Optional[Repo]:
        """Resolve by id, alias (case-insensitive), or path."""
        if not ref:
            return None
        with self._lock:
            row = self._conn.execute("SELECT * FROM repos WHERE id=?", (ref,)).fetchone()
            if row:
                return Repo(**_row_to_dict(row))
            row = self._conn.execute("SELECT * FROM repos WHERE alias=? COLLATE NOCASE", (ref,)).fetchone()
            if row:
                return Repo(**_row_to_dict(row))
            row = self._conn.execute(
                "SELECT * FROM repos WHERE path=? COLLATE NOCASE", (str(Path(ref).resolve()),)
            ).fetchone()
            return Repo(**_row_to_dict(row)) if row else None

    def delete_repo(self, repo_id: str) -> None:
        with self._lock:
            self._conn.execute("DELETE FROM repos WHERE id=?", (repo_id,))
            self._conn.commit()

    # --- plans ---------------------------------------------------------------
    def insert_plan(self, plan: Plan) -> None:
        with self._lock:
            self._conn.execute(
                "INSERT INTO plans(id, repo_id, prompt, baseline_branch, baseline_commit, status, created_at)"
                " VALUES(?,?,?,?,?,?,?)",
                (plan.id, plan.repo_id, plan.prompt, plan.baseline_branch,
                 plan.baseline_commit, plan.status, plan.created_at),
            )
            self._conn.commit()

    def update_plan(self, plan: Plan) -> None:
        with self._lock:
            self._conn.execute(
                "UPDATE plans SET prompt=?, baseline_branch=?, baseline_commit=?, status=? WHERE id=?",
                (plan.prompt, plan.baseline_branch, plan.baseline_commit, plan.status, plan.id),
            )
            self._conn.commit()

    def get_plan(self, plan_id: str) -> Optional[Plan]:
        with self._lock:
            row = self._conn.execute("SELECT * FROM plans WHERE id=?", (plan_id,)).fetchone()
        return Plan(**_row_to_dict(row)) if row else None

    def list_plans(self, repo_id: Optional[str] = None) -> List[Plan]:
        with self._lock:
            if repo_id:
                rows = self._conn.execute(
                    "SELECT * FROM plans WHERE repo_id=? ORDER BY id", (repo_id,)).fetchall()
            else:
                rows = self._conn.execute("SELECT * FROM plans ORDER BY id").fetchall()
        return [Plan(**_row_to_dict(r)) for r in rows]

    # --- tasks ---------------------------------------------------------------
    def insert_task(self, task: Task) -> None:
        row = task.to_row()
        cols = ",".join(row.keys())
        marks = ",".join("?" for _ in row)
        with self._lock:
            self._conn.execute(f"INSERT INTO tasks({cols}) VALUES({marks})", tuple(row.values()))
            self._conn.commit()

    def update_task(self, task: Task) -> None:
        row = task.to_row()
        assignments = ",".join(f"{k}=?" for k in row.keys() if k != "id")
        with self._lock:
            self._conn.execute(
                f"UPDATE tasks SET {assignments} WHERE id=?",
                tuple(v for k, v in row.items() if k != "id") + (task.id,),
            )
            self._conn.commit()

    def get_task(self, task_id: str) -> Optional[Task]:
        with self._lock:
            row = self._conn.execute("SELECT * FROM tasks WHERE id=?", (task_id,)).fetchone()
        return Task.from_row(_row_to_dict(row)) if row else None

    def list_tasks(self, repo_id: Optional[str] = None, plan_id: Optional[str] = None) -> List[Task]:
        query = "SELECT * FROM tasks"
        clauses, params = [], []
        if repo_id:
            clauses.append("repo_id=?")
            params.append(repo_id)
        if plan_id:
            clauses.append("plan_id=?")
            params.append(plan_id)
        if clauses:
            query += " WHERE " + " AND ".join(clauses)
        query += " ORDER BY id"
        with self._lock:
            rows = self._conn.execute(query, tuple(params)).fetchall()
        return [Task.from_row(_row_to_dict(r)) for r in rows]

    # --- runs ---------------------------------------------------------------
    def insert_run(self, run: RunRecord) -> None:
        row = run.to_row()
        cols = ",".join(row.keys())
        marks = ",".join("?" for _ in row)
        with self._lock:
            self._conn.execute(f"INSERT INTO runs({cols}) VALUES({marks})", tuple(row.values()))
            self._conn.commit()

    def update_run(self, run: RunRecord) -> None:
        row = run.to_row()
        assignments = ",".join(f"{k}=?" for k in row.keys() if k != "id")
        with self._lock:
            self._conn.execute(
                f"UPDATE runs SET {assignments} WHERE id=?",
                tuple(v for k, v in row.items() if k != "id") + (run.id,),
            )
            self._conn.commit()

    def get_run(self, run_id: str) -> Optional[RunRecord]:
        with self._lock:
            row = self._conn.execute("SELECT * FROM runs WHERE id=?", (run_id,)).fetchone()
        return RunRecord.from_row(_row_to_dict(row)) if row else None

    def runs_for_task(self, task_id: str) -> List[RunRecord]:
        with self._lock:
            rows = self._conn.execute(
                "SELECT * FROM runs WHERE task_id=? ORDER BY id", (task_id,)).fetchall()
        return [RunRecord.from_row(_row_to_dict(r)) for r in rows]

    def active_runs(self) -> List[RunRecord]:
        with self._lock:
            rows = self._conn.execute(
                "SELECT * FROM runs WHERE status IN ('starting','running','cancel-requested')"
                " ORDER BY id").fetchall()
        return [RunRecord.from_row(_row_to_dict(r)) for r in rows]

    # --- events ---------------------------------------------------------------
    def append_event(self, kind: str, repo_id: Optional[str] = None,
                     task_id: Optional[str] = None, payload: Optional[Dict[str, Any]] = None) -> None:
        import json as _json
        with self._lock:
            self._conn.execute(
                "INSERT INTO events(ts, kind, repo_id, task_id, payload) VALUES(?,?,?,?,?)",
                (utcnow(), kind, repo_id, task_id,
                 _json.dumps(payload or {}, ensure_ascii=False, default=str)),
            )
            self._conn.commit()

    def recent_events(self, limit: int = 50, task_id: Optional[str] = None,
                      repo_id: Optional[str] = None) -> List[Dict[str, Any]]:
        import json as _json
        query = "SELECT * FROM events"
        clauses, params = [], []
        if task_id:
            clauses.append("task_id=?")
            params.append(task_id)
        if repo_id:
            clauses.append("repo_id=?")
            params.append(repo_id)
        if clauses:
            query += " WHERE " + " AND ".join(clauses)
        query += " ORDER BY seq DESC LIMIT ?"
        params.append(limit)
        with self._lock:
            rows = self._conn.execute(query, tuple(params)).fetchall()
        out = []
        for r in rows:
            d = _row_to_dict(r)
            try:
                d["payload"] = _json.loads(d.get("payload") or "{}")
            except ValueError:
                d["payload"] = {}
            out.append(d)
        return out

    # --- approvals ---------------------------------------------------------------
    def add_approval(self, approval: ApprovalRecord) -> None:
        with self._lock:
            self._conn.execute(
                "INSERT INTO approvals(id, target_kind, target_id, kind, note, created_at)"
                " VALUES(?,?,?,?,?,?)",
                (approval.id, approval.target_kind, approval.target_id,
                 approval.kind, approval.note, approval.created_at),
            )
            self._conn.commit()

    def has_approval(self, target_kind: str, target_id: str, kind: str) -> bool:
        with self._lock:
            row = self._conn.execute(
                "SELECT 1 FROM approvals WHERE target_kind=? AND target_id=? AND kind=? LIMIT 1",
                (target_kind, target_id, kind)).fetchone()
        return row is not None

    def list_approvals(self, target_id: Optional[str] = None) -> List[ApprovalRecord]:
        with self._lock:
            if target_id:
                rows = self._conn.execute(
                    "SELECT * FROM approvals WHERE target_id=? ORDER BY id", (target_id,)).fetchall()
            else:
                rows = self._conn.execute("SELECT * FROM approvals ORDER BY id").fetchall()
        return [ApprovalRecord(**_row_to_dict(r)) for r in rows]
