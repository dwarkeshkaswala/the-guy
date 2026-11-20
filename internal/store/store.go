// Package store provides SQLite persistence with a schema identical to the
// Python implementation (same tables and column layout), so both CLIs can
// share one state file during migration. Uses the pure-Go driver
// modernc.org/sqlite (CGo-free → cross-compiles to windows/darwin/linux,
// amd64/arm64).
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/dwkaswala/the-guy/internal/pathutil"
	"github.com/dwkaswala/the-guy/internal/tasks"
)

const schema = `
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
`

type Store struct {
	db   *sql.DB
	mu   sync.Mutex
	path string
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	// SQLite URI options: WAL + busy timeout keep multi-process use safe
	// (e.g. `guy run` and `guy cancel` at the same time).
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db, path: path}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Path() string { return s.path }

// SchemaCompatible reports whether an existing DB carries the expected tables
// (used by `guy migrate` when adopting Python state).
func (s *Store) SchemaCompatible() error {
	for _, table := range []string{"meta", "repos", "plans", "tasks", "runs", "events", "approvals"} {
		var name string
		err := s.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			return fmt.Errorf("state db is missing table %q (found at %s)", table, s.path)
		}
	}
	return nil
}

func (s *Store) NextID(prefix string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := "seq_" + strings.ToLower(prefix)
	var value string
	err := s.db.QueryRow("SELECT value FROM meta WHERE key=?", key).Scan(&value)
	n := 1
	if err == nil {
		if parsed, perr := strconv.Atoi(value); perr == nil {
			n = parsed + 1
		}
	} else if err != sql.ErrNoRows {
		return "", err
	}
	if _, err := s.db.Exec(
		"INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value",
		key, strconv.Itoa(n)); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s-%03d", prefix, n), nil
}

// ---- repos ----

func (s *Store) UpsertRepo(r tasks.Repo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		"INSERT OR REPLACE INTO repos(id, path, alias, default_branch, added_at) VALUES(?,?,?,?,?)",
		r.ID, r.Path, r.Alias, r.DefaultBranch, r.AddedAt)
	return err
}

func (s *Store) ListRepos() ([]tasks.Repo, error) {
	rows, err := s.db.Query("SELECT id, path, alias, default_branch, added_at FROM repos ORDER BY alias")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tasks.Repo
	for rows.Next() {
		var r tasks.Repo
		if err := rows.Scan(&r.ID, &r.Path, &r.Alias, &r.DefaultBranch, &r.AddedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRepo resolves by id, alias (case-insensitive), or path. Paths are
// normalized (long-path expanded, slash/case-folded on Windows) because git
// reports forward-slash long roots while Go joins may use 8.3 short names.
func (s *Store) GetRepo(ref string) (*tasks.Repo, error) {
	if ref == "" {
		return nil, nil
	}
	abs, _ := filepath.Abs(ref)
	norm := pathutil.Normalize(abs)
	rows, err := s.db.Query("SELECT id, path, alias, default_branch, added_at FROM repos")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var byAlias, byPath *tasks.Repo
	for rows.Next() {
		var r tasks.Repo
		if err := rows.Scan(&r.ID, &r.Path, &r.Alias, &r.DefaultBranch, &r.AddedAt); err != nil {
			return nil, err
		}
		if r.ID == ref {
			return &r, nil
		}
		if byAlias == nil && strings.EqualFold(r.Alias, ref) {
			byAlias = &r
		}
		if byPath == nil && pathutil.Normalize(r.Path) == norm {
			byPath = &r
		}
	}
	if byPath != nil {
		return byPath, nil
	}
	return byAlias, nil
}

func (s *Store) DeleteRepo(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec("DELETE FROM repos WHERE id=?", id)
	return err
}

// ---- plans ----

func (s *Store) InsertPlan(p tasks.Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		"INSERT INTO plans(id, repo_id, prompt, baseline_branch, baseline_commit, status, created_at) VALUES(?,?,?,?,?,?,?)",
		p.ID, p.RepoID, p.Prompt, p.BaselineBranch, p.BaselineCommit, p.Status, p.CreatedAt)
	return err
}

func (s *Store) UpdatePlan(p tasks.Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		"UPDATE plans SET prompt=?, baseline_branch=?, baseline_commit=?, status=? WHERE id=?",
		p.Prompt, p.BaselineBranch, p.BaselineCommit, p.Status, p.ID)
	return err
}

func (s *Store) GetPlan(id string) (*tasks.Plan, error) {
	var p tasks.Plan
	err := s.db.QueryRow(
		"SELECT id, repo_id, prompt, baseline_branch, baseline_commit, status, created_at FROM plans WHERE id=?",
		id).Scan(&p.ID, &p.RepoID, &p.Prompt, &p.BaselineBranch, &p.BaselineCommit, &p.Status, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) ListPlans(repoID string) ([]tasks.Plan, error) {
	var rows *sql.Rows
	var err error
	if repoID != "" {
		rows, err = s.db.Query("SELECT id, repo_id, prompt, baseline_branch, baseline_commit, status, created_at FROM plans WHERE repo_id=? ORDER BY id", repoID)
	} else {
		rows, err = s.db.Query("SELECT id, repo_id, prompt, baseline_branch, baseline_commit, status, created_at FROM plans ORDER BY id")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tasks.Plan
	for rows.Next() {
		var p tasks.Plan
		if err := rows.Scan(&p.ID, &p.RepoID, &p.Prompt, &p.BaselineBranch, &p.BaselineCommit, &p.Status, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ---- tasks ----

func (s *Store) InsertTask(t *tasks.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO tasks(
		id, plan_id, repo_id, title, objective, agent, dependencies, read_only,
		allowed_paths, acceptance_criteria, required_tests, timeout_seconds,
		max_retries, retry_count, status, blocked_reason, baseline_branch,
		baseline_commit, worktree, branch, created_at, started_at, finished_at,
		exit_code, exit_reason, changed_files, commits, test_results, concerns,
		options, handoff)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		t.ID, t.PlanID, t.RepoID, t.Title, t.Objective, t.Agent,
		tasks.MarshalColumn(t.Dependencies), boolToInt(t.ReadOnly),
		tasks.MarshalColumn(t.AllowedPaths), tasks.MarshalColumn(t.AcceptanceCriteria),
		tasks.MarshalColumn(t.RequiredTests), nullableInt(t.TimeoutSeconds),
		t.MaxRetries, t.RetryCount, t.Status, nullableString(t.BlockedReason),
		t.BaselineBranch, t.BaselineCommit, nullableString(t.Worktree), nullableString(t.Branch),
		t.CreatedAt, nullableString(t.StartedAt), nullableString(t.FinishedAt),
		nullableIntPtr(t.ExitCode), nullableString(t.ExitReason),
		tasks.MarshalColumn(t.ChangedFiles), tasks.MarshalColumn(t.Commits),
		tasks.MarshalColumn(t.TestResults), tasks.MarshalColumn(t.Concerns),
		nullableString(tasks.MarshalColumn(t.Options)), nullableString(tasks.MarshalColumn(t.Handoff)))
	return err
}

func (s *Store) UpdateTask(t *tasks.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE tasks SET
		plan_id=?, repo_id=?, title=?, objective=?, agent=?, dependencies=?, read_only=?,
		allowed_paths=?, acceptance_criteria=?, required_tests=?, timeout_seconds=?,
		max_retries=?, retry_count=?, status=?, blocked_reason=?, baseline_branch=?,
		baseline_commit=?, worktree=?, branch=?, created_at=?, started_at=?, finished_at=?,
		exit_code=?, exit_reason=?, changed_files=?, commits=?, test_results=?, concerns=?,
		options=?, handoff=? WHERE id=?`,
		t.PlanID, t.RepoID, t.Title, t.Objective, t.Agent,
		tasks.MarshalColumn(t.Dependencies), boolToInt(t.ReadOnly),
		tasks.MarshalColumn(t.AllowedPaths), tasks.MarshalColumn(t.AcceptanceCriteria),
		tasks.MarshalColumn(t.RequiredTests), nullableInt(t.TimeoutSeconds),
		t.MaxRetries, t.RetryCount, t.Status, nullableString(t.BlockedReason),
		t.BaselineBranch, t.BaselineCommit, nullableString(t.Worktree), nullableString(t.Branch),
		t.CreatedAt, nullableString(t.StartedAt), nullableString(t.FinishedAt),
		nullableIntPtr(t.ExitCode), nullableString(t.ExitReason),
		tasks.MarshalColumn(t.ChangedFiles), tasks.MarshalColumn(t.Commits),
		tasks.MarshalColumn(t.TestResults), tasks.MarshalColumn(t.Concerns),
		nullableString(tasks.MarshalColumn(t.Options)), nullableString(tasks.MarshalColumn(t.Handoff)),
		t.ID)
	return err
}

const taskColumns = `id, plan_id, repo_id, title, objective, agent, dependencies, read_only,
	allowed_paths, acceptance_criteria, required_tests, timeout_seconds,
	max_retries, retry_count, status, blocked_reason, baseline_branch,
	baseline_commit, worktree, branch, created_at, started_at, finished_at,
	exit_code, exit_reason, changed_files, commits, test_results, concerns,
	options, handoff`

func scanTask(row interface{ Scan(...any) error }) (*tasks.Task, error) {
	var t tasks.Task
	var deps, allowed, criteria, tests, changed, commits, results, concerns string
	var timeout, exitCode sql.NullInt64
	var blockedReason, worktree, branch, startedAt, finishedAt, exitReason, options, handoff sql.NullString
	err := row.Scan(&t.ID, &t.PlanID, &t.RepoID, &t.Title, &t.Objective, &t.Agent, &deps, &t.ReadOnly,
		&allowed, &criteria, &tests, &timeout,
		&t.MaxRetries, &t.RetryCount, &t.Status, &blockedReason, &t.BaselineBranch,
		&t.BaselineCommit, &worktree, &branch, &t.CreatedAt, &startedAt, &finishedAt,
		&exitCode, &exitReason, &changed, &commits, &results, &concerns,
		&options, &handoff)
	if err != nil {
		return nil, err
	}
	t.Dependencies = unmarshalStrings(deps)
	t.AllowedPaths = unmarshalStrings(allowed)
	t.AcceptanceCriteria = unmarshalStrings(criteria)
	_ = json.Unmarshal([]byte(orDefaultStr(tests, "[]")), &t.RequiredTests)
	_ = json.Unmarshal([]byte(orDefaultStr(results, "[]")), &t.TestResults)
	t.ChangedFiles = unmarshalStrings(changed)
	t.Commits = unmarshalStrings(commits)
	t.Concerns = unmarshalStrings(concerns)
	t.BlockedReason = blockedReason.String
	t.Worktree = worktree.String
	t.Branch = branch.String
	t.StartedAt = startedAt.String
	t.FinishedAt = finishedAt.String
	t.ExitReason = exitReason.String
	if timeout.Valid {
		v := int(timeout.Int64)
		t.TimeoutSeconds = &v
	}
	if exitCode.Valid {
		v := int(exitCode.Int64)
		t.ExitCode = &v
	}
	if options.Valid && options.String != "" {
		_ = json.Unmarshal([]byte(options.String), &t.Options)
	}
	if handoff.Valid && handoff.String != "" {
		_ = json.Unmarshal([]byte(handoff.String), &t.Handoff)
	}
	return &t, nil
}

func (s *Store) GetTask(id string) (*tasks.Task, error) {
	row := s.db.QueryRow("SELECT "+taskColumns+" FROM tasks WHERE id=?", id)
	t, err := scanTask(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return t, err
}

func (s *Store) ListTasks(repoID, planID string) ([]*tasks.Task, error) {
	query := "SELECT " + taskColumns + " FROM tasks"
	var clauses []string
	var args []any
	if repoID != "" {
		clauses = append(clauses, "repo_id=?")
		args = append(args, repoID)
	}
	if planID != "" {
		clauses = append(clauses, "plan_id=?")
		args = append(args, planID)
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY id"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*tasks.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---- runs ----

func (s *Store) InsertRun(r *tasks.RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`INSERT INTO runs(id, task_id, adapter, argv, cwd, pid, owner_pid,
		status, exit_code, failure_class, started_at, finished_at, duration_ms, log_path)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.ID, r.TaskID, r.Adapter, tasks.MarshalColumn(r.Argv), r.Cwd, r.Pid, r.OwnerPid,
		r.Status, nullableIntPtr(r.ExitCode), r.FailureClass, r.StartedAt, nullableString(r.FinishedAt),
		r.DurationMS, nullableString(r.LogPath))
	return err
}

func (s *Store) UpdateRun(r *tasks.RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE runs SET task_id=?, adapter=?, argv=?, cwd=?, pid=?, owner_pid=?,
		status=?, exit_code=?, failure_class=?, started_at=?, finished_at=?, duration_ms=?, log_path=? WHERE id=?`,
		r.TaskID, r.Adapter, tasks.MarshalColumn(r.Argv), r.Cwd, r.Pid, r.OwnerPid,
		r.Status, nullableIntPtr(r.ExitCode), r.FailureClass, r.StartedAt, nullableString(r.FinishedAt),
		r.DurationMS, nullableString(r.LogPath), r.ID)
	return err
}

func scanRun(row interface{ Scan(...any) error }) (*tasks.RunRecord, error) {
	var r tasks.RunRecord
	var argv string
	var pid, ownerPid, exitCode, duration sql.NullInt64
	var finishedAt, logPath sql.NullString
	err := row.Scan(&r.ID, &r.TaskID, &r.Adapter, &argv, &r.Cwd, &pid, &ownerPid,
		&r.Status, &exitCode, &r.FailureClass, &r.StartedAt, &finishedAt, &duration, &logPath)
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(orDefaultStr(argv, "[]")), &r.Argv)
	r.Pid = int(pid.Int64)
	r.OwnerPid = int(ownerPid.Int64)
	if exitCode.Valid {
		v := int(exitCode.Int64)
		r.ExitCode = &v
	}
	r.FinishedAt = finishedAt.String
	r.DurationMS = int(duration.Int64)
	r.LogPath = logPath.String
	return &r, nil
}

func (s *Store) GetRun(id string) (*tasks.RunRecord, error) {
	row := s.db.QueryRow(
		"SELECT id, task_id, adapter, argv, cwd, pid, owner_pid, status, exit_code, failure_class, started_at, finished_at, duration_ms, log_path FROM runs WHERE id=?",
		id)
	r, err := scanRun(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return r, err
}

func (s *Store) RunsForTask(taskID string) ([]*tasks.RunRecord, error) {
	rows, err := s.db.Query(
		"SELECT id, task_id, adapter, argv, cwd, pid, owner_pid, status, exit_code, failure_class, started_at, finished_at, duration_ms, log_path FROM runs WHERE task_id=? ORDER BY id",
		taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*tasks.RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) ActiveRuns() ([]*tasks.RunRecord, error) {
	rows, err := s.db.Query(
		"SELECT id, task_id, adapter, argv, cwd, pid, owner_pid, status, exit_code, failure_class, started_at, finished_at, duration_ms, log_path FROM runs WHERE status IN ('starting','running','cancel-requested') ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*tasks.RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRunsForRepo returns the most recent runs across all tasks of one repo
// (join on tasks.repo_id), newest first, bounded by limit.
func (s *Store) ListRunsForRepo(repoID string, limit int) ([]*tasks.RunRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(
		"SELECT r.id, r.task_id, r.adapter, r.argv, r.cwd, r.pid, r.owner_pid, r.status, r.exit_code, r.failure_class, r.started_at, r.finished_at, r.duration_ms, r.log_path "+
			"FROM runs r JOIN tasks t ON r.task_id = t.id WHERE t.repo_id=? ORDER BY r.started_at DESC, r.id DESC LIMIT ?",
		repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*tasks.RunRecord
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- events ----

func (s *Store) AppendEvent(kind, repoID, taskID string, payload map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if payload == nil {
		payload = map[string]any{}
	}
	data, _ := json.Marshal(payload)
	_, err := s.db.Exec("INSERT INTO events(ts, kind, repo_id, task_id, payload) VALUES(?,?,?,?,?)",
		tasks.UTCNow(), kind, repoID, taskID, string(data))
	return err
}

func (s *Store) RecentEvents(limit int, taskID, repoID string) ([]tasks.EventRecord, error) {
	query := "SELECT seq, ts, kind, repo_id, task_id, payload FROM events"
	var clauses []string
	var args []any
	if taskID != "" {
		clauses = append(clauses, "task_id=?")
		args = append(args, taskID)
	}
	if repoID != "" {
		clauses = append(clauses, "repo_id=?")
		args = append(args, repoID)
	}
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY seq DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tasks.EventRecord
	for rows.Next() {
		var e tasks.EventRecord
		var repoIDNull, taskIDNull, payload sql.NullString
		if err := rows.Scan(&e.Seq, &e.TS, &e.Kind, &repoIDNull, &taskIDNull, &payload); err != nil {
			return nil, err
		}
		e.RepoID = repoIDNull.String
		e.TaskID = taskIDNull.String
		_ = json.Unmarshal([]byte(orDefault(payload, "{}")), &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// EventsSince returns events with seq > afterSeq in ascending order (for
// GUI/daemon event streaming with replay after reconnect). Limit bounds the
// batch; callers detect gaps by comparing returned seqs.
func (s *Store) EventsSince(afterSeq int, limit int) ([]tasks.EventRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.Query(
		"SELECT seq, ts, kind, repo_id, task_id, payload FROM events WHERE seq > ? ORDER BY seq ASC LIMIT ?",
		afterSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tasks.EventRecord
	for rows.Next() {
		var e tasks.EventRecord
		var repoIDNull, taskIDNull, payload sql.NullString
		if err := rows.Scan(&e.Seq, &e.TS, &e.Kind, &repoIDNull, &taskIDNull, &payload); err != nil {
			return nil, err
		}
		e.RepoID = repoIDNull.String
		e.TaskID = taskIDNull.String
		_ = json.Unmarshal([]byte(orDefault(payload, "{}")), &e.Payload)
		out = append(out, e)
	}
	return out, rows.Err()
}

// ---- approvals ----

func (s *Store) AddApproval(a tasks.ApprovalRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(
		"INSERT INTO approvals(id, target_kind, target_id, kind, note, created_at) VALUES(?,?,?,?,?,?)",
		a.ID, a.TargetKind, a.TargetID, a.Kind, a.Note, a.CreatedAt)
	return err
}

func (s *Store) HasApproval(targetKind, targetID, kind string) (bool, error) {
	var one int
	err := s.db.QueryRow(
		"SELECT 1 FROM approvals WHERE target_kind=? AND target_id=? AND kind=? LIMIT 1",
		targetKind, targetID, kind).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) ListApprovals(targetID string) ([]tasks.ApprovalRecord, error) {
	var rows *sql.Rows
	var err error
	if targetID != "" {
		rows, err = s.db.Query("SELECT id, target_kind, target_id, kind, note, created_at FROM approvals WHERE target_id=? ORDER BY id", targetID)
	} else {
		rows, err = s.db.Query("SELECT id, target_kind, target_id, kind, note, created_at FROM approvals ORDER BY id")
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tasks.ApprovalRecord
	for rows.Next() {
		var a tasks.ApprovalRecord
		if err := rows.Scan(&a.ID, &a.TargetKind, &a.TargetID, &a.Kind, &a.Note, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- helpers ----

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableIntPtr(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func orDefault(ns sql.NullString, def string) string {
	if !ns.Valid || ns.String == "" {
		return def
	}
	return ns.String
}

func orDefaultStr(s string, def string) string {
	if s == "" {
		return def
	}
	return s
}

func unmarshalStrings(s string) []string {
	var out []string
	_ = json.Unmarshal([]byte(s), &out)
	if out == nil {
		out = []string{}
	}
	return out
}
