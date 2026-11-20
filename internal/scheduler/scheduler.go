// Package scheduler implements dependency-gated, bounded-parallel execution.
// A dependent task only becomes runnable once its dependencies are
// `integrated` (human approval + integration — never mutual agent agreement).
// One write owner per worktree; overlapping allowed_paths block concurrency.
// Retries only for safe failure classes (spawn_error). `guy run` never
// commits, merges, or pushes.
package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dwkaswala/the-guy/internal/agents"
	"github.com/dwkaswala/the-guy/internal/config"
	"github.com/dwkaswala/the-guy/internal/gitops"
	"github.com/dwkaswala/the-guy/internal/process"
	"github.com/dwkaswala/the-guy/internal/redact"
	"github.com/dwkaswala/the-guy/internal/store"
	"github.com/dwkaswala/the-guy/internal/tasks"
)

type SchedulingError struct{ Msg string }

func (e *SchedulingError) Error() string { return e.Msg }

// Reconcile marks runs whose owning `guy` process died as interrupted
// (restart recovery). Returns descriptions of fixed runs.
func Reconcile(st *store.Store) []map[string]any {
	var fixed []map[string]any
	runs, err := st.ActiveRuns()
	if err != nil {
		return fixed
	}
	for _, run := range runs {
		if run.OwnerPid != 0 && process.PidAlive(run.OwnerPid) {
			continue
		}
		if run.Status == "cancel-requested" {
			run.Status = "cancelled"
			run.FailureClass = "cancelled"
		} else {
			run.Status = "failed"
			run.FailureClass = "interrupted"
		}
		run.FinishedAt = tasks.UTCNow()
		_ = st.UpdateRun(run)
		task, _ := st.GetTask(run.TaskID)
		if task != nil && task.Status == tasks.StatusRunning {
			_ = task.ApplyTransition(tasks.StatusFailed, "")
			task.ExitReason = "orchestrator restarted or runner process exited unexpectedly"
			task.FinishedAt = tasks.UTCNow()
			_ = st.UpdateTask(task)
		}
		repoID := ""
		if task != nil {
			repoID = task.RepoID
		}
		_ = st.AppendEvent("run_interrupted", repoID, run.TaskID, map[string]any{
			"run_id": run.ID, "failure_class": run.FailureClass,
		})
		fixed = append(fixed, map[string]any{"run": run.ID, "task": run.TaskID,
			"failure_class": run.FailureClass})
	}
	return fixed
}

type Scheduler struct {
	Store    *store.Store
	Settings map[string]any
	Repo     *tasks.Repo
	Root     string
}

func New(st *store.Store, settings map[string]any, repo *tasks.Repo) *Scheduler {
	return &Scheduler{Store: st, Settings: settings, Repo: repo, Root: repo.Path}
}

func (s *Scheduler) blockedReasonFor(task *tasks.Task, byID map[string]*tasks.Task) string {
	var pending []string
	for _, depID := range task.Dependencies {
		dep, ok := byID[depID]
		if !ok {
			pending = append(pending, depID+" (missing)")
		} else if dep.Status != tasks.StatusIntegrated {
			pending = append(pending, fmt.Sprintf("%s (%s)", depID, dep.Status))
		}
	}
	if len(pending) > 0 {
		return "waiting for dependencies: " + strings.Join(pending, ", ") +
			" — approve and integrate them first (`guy approve ...`, `guy integrate --approve`)"
	}
	return ""
}

func PathsOverlap(a, b []string) string {
	if len(a) == 0 || len(b) == 0 {
		return "write scope overlaps: empty allowed_paths means the whole repository"
	}
	for _, pa := range a {
		paNorm := strings.TrimSuffix(filepath.ToSlash(pa), "/")
		for _, pb := range b {
			pbNorm := strings.TrimSuffix(filepath.ToSlash(pb), "/")
			if paNorm == pbNorm || globMatch(paNorm, pbNorm) || globMatch(pbNorm, paNorm) {
				return fmt.Sprintf("allowed paths overlap: '%s' vs '%s'", pa, pb)
			}
			if strings.HasPrefix(pbNorm+"/", paNorm+"/") || strings.HasPrefix(paNorm+"/", pbNorm+"/") {
				return fmt.Sprintf("allowed paths overlap: '%s' vs '%s' (subdirectory)", pa, pb)
			}
		}
	}
	return ""
}

// globMatch implements fnmatch-style matching ('*' crosses '/'; tests use it
// for allowed_paths overlap approximation, deliberately conservative).
func globMatch(pattern, s string) bool {
	if pattern == s {
		return true
	}
	if !strings.Contains(pattern, "*") {
		return false
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i := 1; i < len(parts); i++ {
		idx := strings.Index(s, parts[i])
		if idx < 0 {
			return false
		}
		s = s[idx+len(parts[i]):]
	}
	if !strings.HasSuffix(s, parts[len(parts)-1]) && parts[len(parts)-1] != "" {
		return false
	}
	return true
}

func (s *Scheduler) CollisionWith(task *tasks.Task, running []*tasks.Task) string {
	if task.ReadOnly {
		return ""
	}
	for _, other := range running {
		if other.ReadOnly || other.ID == task.ID {
			continue
		}
		if msg := PathsOverlap(task.AllowedPaths, other.AllowedPaths); msg != "" {
			return fmt.Sprintf("path collision with running task %s: %s", other.ID, msg)
		}
	}
	return ""
}

func (s *Scheduler) PromptFor(task *tasks.Task, workdir string) string {
	lines := []string{
		"You are an autonomous coding agent executing exactly one task in an isolated Git worktree,",
		"coordinated by the-guy.",
		"",
		"Task: " + task.Title,
		"Objective: " + task.Objective,
	}
	if len(task.AcceptanceCriteria) > 0 {
		lines = append(lines, "", "Acceptance criteria:")
		for _, c := range task.AcceptanceCriteria {
			lines = append(lines, "- "+c)
		}
	}
	if len(task.AllowedPaths) > 0 {
		lines = append(lines, "", "Allowed paths (stay within these): "+strings.Join(task.AllowedPaths, ", "))
	}
	lines = append(lines, "",
		"Rules:",
		"- Work only inside this directory: "+workdir,
		"- Do not run git push, git rebase, git reset, or modify branches other than this worktree's.",
		"- Do not commit your changes; a human reviews them first and the orchestrator commits after approval.")
	return strings.Join(lines, "\n")
}

func (s *Scheduler) ResolveWorkdir(task *tasks.Task) (string, error) {
	if task.ReadOnly {
		return s.Root, nil
	}
	repoWTRoot := filepath.Join(config.WorktreesRoot(), s.Repo.ID)
	wt := filepath.Join(repoWTRoot, task.ID)
	if task.Worktree != "" {
		wt = task.Worktree
	}
	if _, err := redact.EnsureWithin(repoWTRoot, wt); err != nil {
		return "", err
	}
	return wt, nil
}

// PlannedLaunch describes a dry-run proposal.
type PlannedLaunch struct {
	TaskID            string   `json:"task_id"`
	Title             string   `json:"title"`
	Agent             string   `json:"agent"`
	AdapterOK         bool     `json:"adapter_ok"`
	AdapterNote       string   `json:"adapter_note"`
	ArgvDisplay       string   `json:"argv_display"`
	Cwd               string   `json:"cwd"`
	Worktree          string   `json:"worktree"`
	Branch            string   `json:"branch"`
	BaselineBranch    string   `json:"baseline_branch"`
	BaselineCommit    string   `json:"baseline_commit"`
	TimeoutSeconds    int      `json:"timeout_seconds"`
	ReadOnly          bool     `json:"read_only"`
	PendingDependents []string `json:"pending_dependencies"`
}

func (s *Scheduler) timeoutFor(task *tasks.Task) int {
	if task.TimeoutSeconds != nil && *task.TimeoutSeconds > 0 {
		return *task.TimeoutSeconds
	}
	return config.SettingsInt(s.Settings, "run_timeout_seconds", 1800)
}

func (s *Scheduler) DryRun(list []*tasks.Task) []PlannedLaunch {
	var out []PlannedLaunch
	byID := map[string]*tasks.Task{}
	all, _ := s.Store.ListTasks(s.Repo.ID, "")
	for _, t := range all {
		byID[t.ID] = t
	}
	for _, task := range list {
		adapterOK, adapterNote, argvDisplay := true, "", "(unavailable)"
		var workdir string
		if wd, err := s.ResolveWorkdir(task); err == nil {
			workdir = wd
		} else {
			workdir = "(error: " + err.Error() + ")"
		}
		if adapter, err := agents.Get(task.Agent); err == nil {
			report := adapter.Probe(20 * time.Second)
			adapterOK = report.Installed && report.HeadlessVerified
			adapterNote = report.HeadlessNote
			if adapterOK {
				if argv, err := adapter.BuildCommand(s.PromptFor(task, workdir), workdir, task.Options); err == nil {
					argvDisplay = redact.DisplayArgv(argv)
				}
			}
		} else {
			adapterOK, adapterNote = false, err.Error()
		}
		branch := task.Branch
		if branch == "" && !task.ReadOnly {
			branch = fmt.Sprintf("guy/%s-%s", task.ID, redact.Slug(task.Title, 40))
		}
		var pending []string
		for _, dep := range task.Dependencies {
			if t, ok := byID[dep]; !ok || t.Status != tasks.StatusIntegrated {
				pending = append(pending, dep)
			}
		}
		wt := ""
		if !task.ReadOnly {
			wt = workdir
		}
		out = append(out, PlannedLaunch{
			TaskID: task.ID, Title: task.Title, Agent: task.Agent,
			AdapterOK: adapterOK, AdapterNote: adapterNote, ArgvDisplay: argvDisplay,
			Cwd: workdir, Worktree: wt, Branch: branch,
			BaselineBranch: task.BaselineBranch, BaselineCommit: task.BaselineCommit,
			TimeoutSeconds: s.timeoutFor(task), ReadOnly: task.ReadOnly,
			PendingDependents: pending,
		})
	}
	return out
}

type Summary struct {
	Launched    []map[string]any `json:"launched"`
	NeedsReview []map[string]any `json:"needs_review"`
	Failed      []map[string]any `json:"failed"`
	Cancelled   []map[string]any `json:"cancelled"`
	Blocked     []map[string]any `json:"blocked"`
	Retried     []map[string]any `json:"retried"`
}

type launchResult struct {
	task        *tasks.Task
	finalStatus string
	reason      string
	requeue     bool
}

func (s *Scheduler) Execute(list []*tasks.Task, maxWorkers int) Summary {
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if maxWorkers > 8 {
		maxWorkers = 8
	}
	summary := Summary{}
	byID := map[string]*tasks.Task{}
	all, _ := s.Store.ListTasks(s.Repo.ID, "")
	for _, t := range all {
		byID[t.ID] = t
	}

	queue := append([]*tasks.Task{}, list...)
	// First pass: dependency gating and unblocking.
	var runnable []*tasks.Task
	for _, task := range queue {
		fresh, err := s.Store.GetTask(task.ID)
		if err != nil || fresh == nil {
			fresh = task
		}
		if reason := s.blockedReasonFor(fresh, byID); reason != "" {
			if fresh.Status == tasks.StatusApprovedForExecution || fresh.Status == tasks.StatusQueued {
				_ = fresh.ApplyTransition(tasks.StatusBlocked, reason)
				_ = s.Store.UpdateTask(fresh)
				_ = s.Store.AppendEvent("task_blocked", s.Repo.ID, fresh.ID, map[string]any{"reason": reason})
			}
			summary.Blocked = append(summary.Blocked, map[string]any{"task": fresh.ID, "reason": reason})
			continue
		}
		if fresh.Status == tasks.StatusBlocked {
			_ = fresh.ApplyTransition(tasks.StatusQueued, "dependencies satisfied")
			_ = s.Store.UpdateTask(fresh)
		}
		runnable = append(runnable, fresh)
	}

	var mu sync.Mutex
	running := map[string]*tasks.Task{}
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxWorkers)

	var spawn func(task *tasks.Task)
	collect := func(res launchResult) {
		switch {
		case res.requeue:
			if fresh, _ := s.Store.GetTask(res.task.ID); fresh != nil && fresh.Status == tasks.StatusQueued {
				mu.Lock()
				summary.Retried = append(summary.Retried, map[string]any{
					"task": fresh.ID, "attempt": fresh.RetryCount})
				mu.Unlock()
				spawn(fresh)
				return
			}
			mu.Lock()
			summary.Failed = append(summary.Failed, map[string]any{"task": res.task.ID, "reason": res.reason})
			mu.Unlock()
		case res.finalStatus == tasks.StatusNeedsReview:
			mu.Lock()
			summary.NeedsReview = append(summary.NeedsReview, map[string]any{"task": res.task.ID, "reason": res.reason})
			mu.Unlock()
		case res.finalStatus == tasks.StatusCancelled:
			mu.Lock()
			summary.Cancelled = append(summary.Cancelled, map[string]any{"task": res.task.ID, "reason": res.reason})
			mu.Unlock()
		case res.finalStatus == tasks.StatusBlocked:
			mu.Lock()
			summary.Blocked = append(summary.Blocked, map[string]any{"task": res.task.ID, "reason": res.reason})
			mu.Unlock()
		default:
			mu.Lock()
			summary.Failed = append(summary.Failed, map[string]any{"task": res.task.ID, "reason": res.reason})
			mu.Unlock()
		}
	}
	spawn = func(task *tasks.Task) {
		wg.Add(1)
		go func() {
			sem <- struct{}{} // acquire a worker slot asynchronously (deadlock-safe for retries)
			defer wg.Done()
			defer func() { <-sem }()
			res := s.launch(task)
			mu.Lock()
			delete(running, task.ID)
			mu.Unlock()
			collect(res)
		}()
	}

	for _, task := range runnable {
		mu.Lock()
		var runningList []*tasks.Task
		for _, t := range running {
			runningList = append(runningList, t)
		}
		collision := s.CollisionWith(task, runningList)
		if collision != "" {
			mu.Unlock()
			if task.Status == tasks.StatusApprovedForExecution || task.Status == tasks.StatusQueued {
				_ = task.ApplyTransition(tasks.StatusBlocked, collision)
				_ = s.Store.UpdateTask(task)
				_ = s.Store.AppendEvent("task_blocked", s.Repo.ID, task.ID, map[string]any{"reason": collision})
			}
			summary.Blocked = append(summary.Blocked, map[string]any{"task": task.ID, "reason": collision})
			continue
		}
		running[task.ID] = task
		mu.Unlock()
		summary.Launched = append(summary.Launched, map[string]any{"task": task.ID, "agent": task.Agent})
		spawn(task)
	}
	wg.Wait()
	return summary
}

func (s *Scheduler) launch(task *tasks.Task) launchResult {
	result := launchResult{task: task, finalStatus: tasks.StatusFailed}
	adapter, err := agents.Get(task.Agent)
	if err == nil {
		_, err = adapter.VerifyForExecution()
	}
	if err != nil {
		task.ExitReason = "adapter unavailable: " + err.Error()
		s.finish(task, tasks.StatusFailed, &result)
		return result
	}

	workdir, err := s.prepareWorkdir(task)
	if err != nil {
		task.ExitReason = "worktree setup failed: " + err.Error()
		s.finish(task, tasks.StatusFailed, &result)
		return result
	}

	if task.Status == tasks.StatusApprovedForExecution || task.Status == tasks.StatusFailed {
		_ = task.ApplyTransition(tasks.StatusQueued, "")
		_ = s.Store.UpdateTask(task)
		_ = s.Store.AppendEvent("task_queued", s.Repo.ID, task.ID, nil)
	}
	_ = task.ApplyTransition(tasks.StatusRunning, "")
	task.StartedAt = tasks.UTCNow()
	_ = s.Store.UpdateTask(task)
	_ = s.Store.AppendEvent("task_started", s.Repo.ID, task.ID, map[string]any{
		"agent": task.Agent, "cwd": workdir})

	prompt := s.PromptFor(task, workdir)
	env := adapter.ChildEnv(os.Environ(), task.Options)
	argv, err := adapter.BuildCommand(prompt, workdir, task.Options)
	if err != nil {
		task.ExitReason = "adapter unavailable: " + err.Error()
		s.finish(task, tasks.StatusFailed, &result)
		return result
	}
	timeout := s.timeoutFor(task)
	runID, _ := s.Store.NextID("RUN")
	logPath := filepath.Join(config.LogsRoot(), s.Repo.ID, task.ID, fmt.Sprintf("run-%s.log", runID))
	run := &tasks.RunRecord{
		ID: runID, TaskID: task.ID, Adapter: task.Agent, Argv: argv, Cwd: workdir,
		Status: "starting", StartedAt: tasks.UTCNow(), LogPath: logPath, OwnerPid: os.Getpid(),
	}
	_ = s.Store.InsertRun(run)
	_ = s.Store.AppendEvent("run_started", s.Repo.ID, task.ID, map[string]any{"run_id": runID})

	outcome := process.Execute(argv, workdir, env, timeout, logPath,
		int64(config.SettingsInt(s.Settings, "log_max_bytes", 1_000_000)),
		func() string {
			if current, err := s.Store.GetRun(runID); err == nil && current != nil {
				return current.Status
			}
			return ""
		},
		func(pid int) {
			run.Pid = pid
			run.Status = "running"
			_ = s.Store.UpdateRun(run)
		})

	run.ExitCode = &outcome.ExitCode
	run.DurationMS = int(outcome.DurationMS)
	run.FinishedAt = tasks.UTCNow()
	run.FailureClass = outcome.FailureClass
	switch outcome.FailureClass {
	case "cancelled":
		run.Status = "cancelled"
	case "":
		run.Status = "finished"
	default:
		run.Status = "failed"
	}
	_ = s.Store.UpdateRun(run)
	task.ExitCode = &outcome.ExitCode
	task.FinishedAt = tasks.UTCNow()
	_ = s.Store.AppendEvent("run_finished", s.Repo.ID, task.ID, map[string]any{
		"run_id": runID, "failure_class": outcome.FailureClass})

	switch {
	case outcome.FailureClass == "cancelled":
		task.ExitReason = "cancelled by user"
		s.finish(task, tasks.StatusCancelled, &result)
	case outcome.FailureClass == "spawn_error":
		task.ExitReason = "failed to start agent process: " + outcome.SpawnError
		safe := false
		if classes, ok := s.Settings["safe_retry_failure_classes"].([]any); ok {
			for _, c := range classes {
				if cs, ok := c.(string); ok && cs == "spawn_error" {
					safe = true
				}
			}
		}
		if task.RetryCount < task.MaxRetries && safe {
			task.RetryCount++
			_ = task.ApplyTransition(tasks.StatusFailed, "")
			_ = s.Store.UpdateTask(task)
			_ = task.ApplyTransition(tasks.StatusQueued, "automatic retry (spawn_error is a safe failure class)")
			_ = s.Store.UpdateTask(task)
			_ = s.Store.AppendEvent("task_retry_queued", s.Repo.ID, task.ID,
				map[string]any{"attempt": task.RetryCount})
			result.finalStatus = tasks.StatusFailed
			result.reason = task.ExitReason
			result.requeue = true
			return result
		}
		s.finish(task, tasks.StatusFailed, &result)
	case outcome.FailureClass == "timeout":
		task.ExitReason = fmt.Sprintf("timed out after %ds", timeout)
		s.finish(task, tasks.StatusFailed, &result)
	case outcome.ExitCode == 0:
		if !task.ReadOnly && task.Worktree != "" {
			entries, _ := gitops.StatusPorcelain(task.Worktree)
			var changed []string
			for _, e := range entries {
				changed = append(changed, e[1])
			}
			if diffed, err := gitops.DiffNameOnly(task.Worktree, task.BaselineCommit); err == nil {
				changed = append(changed, diffed...)
			}
			task.ChangedFiles = dedupeSorted(changed)
			if commits, err := gitops.CommitsBetween(task.Worktree, task.BaselineCommit, "HEAD"); err == nil {
				task.Commits = commits
			}
		}
		task.ExitReason = "ok (exit 0 — human review still required)"
		s.finish(task, tasks.StatusNeedsReview, &result)
	default:
		task.ExitReason = fmt.Sprintf("agent exited with code %d; see logs", outcome.ExitCode)
		s.finish(task, tasks.StatusFailed, &result)
	}
	return result
}

func (s *Scheduler) prepareWorkdir(task *tasks.Task) (string, error) {
	if task.ReadOnly {
		return s.Root, nil
	}
	repoWTRoot := filepath.Join(config.WorktreesRoot(), s.Repo.ID)
	wt := filepath.Join(repoWTRoot, task.ID)
	if task.Worktree != "" {
		wt = task.Worktree
	}
	if _, err := redact.EnsureWithin(repoWTRoot, wt); err != nil {
		return "", err
	}
	if dirExists(wt) {
		if task.Worktree == "" {
			task.Worktree = wt
		}
		return wt, nil
	}
	branch := task.Branch
	if branch == "" {
		branch = fmt.Sprintf("guy/%s-%s", task.ID, redact.Slug(task.Title, 40))
	}
	if gitops.BranchExists(s.Root, branch) {
		return "", &SchedulingError{Msg: fmt.Sprintf(
			"branch '%s' already exists without a managed worktree; run `guy clean --task %s --branch` or remove it manually first",
			branch, task.ID)}
	}
	if err := gitops.WorktreeAdd(s.Root, wt, branch, task.BaselineCommit); err != nil {
		return "", err
	}
	task.Worktree = wt
	task.Branch = branch
	return wt, nil
}

func (s *Scheduler) finish(task *tasks.Task, status string, result *launchResult) {
	reason := task.ExitReason
	if err := task.ApplyTransition(status, reason); err != nil {
		reason = strings.TrimSpace(reason + " [transition error: " + err.Error() + "]")
		task.Status = status
	}
	if task.FinishedAt == "" {
		task.FinishedAt = tasks.UTCNow()
	}
	_ = s.Store.UpdateTask(task)
	_ = s.Store.AppendEvent("task_"+status, task.RepoID, task.ID, map[string]any{"reason": reason})
	result.finalStatus = status
	result.reason = reason
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
