"""Thin helpers over the persistent event log (audit trail)."""

from __future__ import annotations

from typing import Any, Dict, List, Optional

from the_guy.store import Store


def log(store: Store, kind: str, repo_id: Optional[str] = None,
        task_id: Optional[str] = None, **payload: Any) -> None:
    store.append_event(kind, repo_id=repo_id, task_id=task_id, payload=payload)


def recent(store: Store, limit: int = 50, task_id: Optional[str] = None,
           repo_id: Optional[str] = None) -> List[Dict[str, Any]]:
    return store.recent_events(limit=limit, task_id=task_id, repo_id=repo_id)
