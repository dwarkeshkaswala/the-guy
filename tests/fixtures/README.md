# Test fixtures

All fixtures in this suite are generated dynamically inside `tests/conftest.py`
(disposable git repositories in `tmp_path`), so nothing here needs to be kept
on disk. Three repo shapes are provided:

- `py_repo` — Python/pyproject layout on branch `trunk`, path contains a space
- `node_repo` — Node/package.json layout on `main`
- `weird_repo` — unrecognized layout on `develop`, with a dirty working tree

Agent work is performed exclusively by the built-in mock agent
(`the_guy.mock_agent`) — no real model calls, no cost, no user files touched.
