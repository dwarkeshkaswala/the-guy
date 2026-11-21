// Package application is the service layer between the desktop GUI and the
// existing orchestrator core. Every method delegates to the same packages the
// `guy` CLI uses (store/tasks/scheduler/agents/gitops/review/config/process);
// there is no duplicated scheduler, Git, or agent logic here. Write operations
// revalidate in Go — the UI is never the security boundary.
//
// The service is Wails-free: UI events flow through the EventSink interface so
// the package stays unit-testable headless.
package application

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dwkaswala/the-guy/internal/agents"
	"github.com/dwkaswala/the-guy/internal/cli"
	"github.com/dwkaswala/the-guy/internal/config"
	"github.com/dwkaswala/the-guy/internal/gitops"
	"github.com/dwkaswala/the-guy/internal/redact"
	"github.com/dwkaswala/the-guy/internal/review"
	"github.com/dwkaswala/the-guy/internal/scheduler"
	"github.com/dwkaswala/the-guy/internal/store"
	"github.com/dwkaswala/the-guy/internal/tasks"
)

// EventSink receives live notifications. The Wails app adapts this to
// app.Event.Emit; tests capture events in memory.
type EventSink interface {
	Emit(name string, data ...any)
}

type noopSink struct{}

func (noopSink) Emit(string, ...any) {}

type Service struct {
	Store   *store.Store
	sink    EventSink
	mu      sync.Mutex // serializes run batches
	batch   bool
	started time.Time
}

func New(st *store.Store, sink EventSink) *Service {
	if sink == nil {
		sink = noopSink{}
	}
	return &Service{Store: st, sink: sink, started: time.Now().UTC()}
}

func (s *Service) emit(name string, data any) { s.sink.Emit(name, data) }

// Event envelope streamed to the UI. Seq is the DB sequence: monotonic,
// dedupable, replayable after restart.
type EventEnvelope struct {
	Seq     int            `json:"seq"`
	TS      string         `json:"ts"`
	Kind    string         `json:"kind"`
	RepoID  string         `json:"repo_id"`
	TaskID  string         `json:"task_id"`
	Payload map[string]any `json:"payload"`
	Version int            `json:"version"`
}

// StartEventStream polls the events table and pushes new envelopes plus a
// liveness tick. Runs until stop is closed. Sequence IDs make delivery
// at-least-once and orderable on the client.
func (s *Service) StartEventStream(stop <-chan struct{}) {
	// start from 0: the UI receives full history with monotonic seqs for
	// dedup, and replays cleanly after restart or reconnect
	lastSeq := 0
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	liveness := time.NewTicker(2 * time.Second)
	defer liveness.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			events, err := s.Store.EventsSince(lastSeq, 200)
			if err != nil {
				continue
			}
			for _, e := range events {
				s.emit("guy:event", EventEnvelope{
					Seq: e.Seq, TS: e.TS, Kind: e.Kind, RepoID: e.RepoID,
					TaskID: e.TaskID, Payload: e.Payload, Version: 1})
				lastSeq = e.Seq
			}
		case <-liveness.C:
			active, _ := s.Store.ActiveRuns()
			s.emit("guy:tick", map[string]any{
				"time": tasks.UTCNow(), "active_runs": len(active)})
		}
	}
}

// ---- health / overview ----

type HealthReport struct {
	OK            bool   `json:"ok"`
	Version       string `json:"version"`
	Pid           int    `json:"pid"`
	NumCPU        int    `json:"num_cpu"`
	GoMaxProcs    int    `json:"go_max_procs"`
	StartedAt     string `json:"started_at"`
	Now           string `json:"now"`
	DataDir       string `json:"data_dir"`
	PrefsDir      string `json:"prefs_dir"`
	LogsRoot      string `json:"logs_root"`
	WorktreesRoot string `json:"worktrees_root"`
	DBPath        string `json:"db_path"`
	DBSizeBytes   int64  `json:"db_size_bytes"`
	ActiveRuns    int    `json:"active_runs"`
	Os            string `json:"os"`
	Arch          string `json:"arch"`
}

func (s *Service) GetServiceHealth() HealthReport {
	active, _ := s.Store.ActiveRuns()
	var size int64
	if st, err := os.Stat(s.Store.Path()); err == nil {
		size = st.Size()
	}
	return HealthReport{
		OK: true, Version: cli.Version, Pid: os.Getpid(),
		NumCPU: runtime.NumCPU(), GoMaxProcs: runtime.GOMAXPROCS(0),
		StartedAt: s.started.Format(time.RFC3339), Now: tasks.UTCNow(),
		DataDir: config.DataDir(), PrefsDir: config.PrefsDir(),
		LogsRoot: config.LogsRoot(), WorktreesRoot: config.WorktreesRoot(),
		DBPath: s.Store.Path(), DBSizeBytes: size,
		ActiveRuns: len(active), Os: runtime.GOOS, Arch: runtime.GOARCH,
	}
}

type RepoView struct {
	tasks.Repo
	PathExists      bool   `json:"path_exists"`
	IsGitRoot       bool   `json:"is_git_root"`
	CurrentBranch   string `json:"current_branch"`
	Detached        bool   `json:"detached"`
	HeadCommit      string `json:"head_commit"`
	Dirty           bool   `json:"dirty"`
	DirtyFileCount  int    `json:"dirty_file_count"`
	Remote          string `json:"remote,omitempty"`
	WorktreeCount   int    `json:"worktree_count"`
	PlansCount      int    `json:"plans_count"`
	TasksCount      int    `json:"tasks_count"`
	NeedsReview     int    `json:"needs_review"`
	Drafts          int    `json:"drafts"`
	RunningTasks    int    `json:"running_tasks"`
	LastActivity    string `json:"last_activity,omitempty"`
	RegistrationErr string `json:"registration_error,omitempty"`
	IsDefault       bool   `json:"is_default"`
}

func (s *Service) repoView(r tasks.Repo) RepoView {
	v := RepoView{Repo: r, PathExists: dirExists(r.Path), IsDefault: isDefault(r)}
	if !v.PathExists {
		v.RegistrationErr = "path no longer exists"
		return v
	}
	if root, err := gitops.FindRoot(r.Path); err != nil || root != r.Path {
		v.RegistrationErr = "path is no longer a git repository root"
		return v
	}
	v.IsGitRoot = true
	if branch, _ := gitops.CurrentBranch(r.Path); branch != "" {
		v.CurrentBranch = branch
	} else {
		v.Detached = true
		v.CurrentBranch = "(detached HEAD)"
	}
	if head, err := gitops.HeadCommit(r.Path); err == nil {
		v.HeadCommit = head
	}
	if entries, err := gitops.StatusPorcelain(r.Path); err == nil {
		v.Dirty = len(entries) > 0
		v.DirtyFileCount = len(entries)
	}
	if res, _ := gitops.Git(r.Path, false, "remote", "get-url", "origin"); res.Code == 0 {
		v.Remote = redact.Redact(strings.TrimSpace(res.Stdout))
	}
	if wts, err := gitops.WorktreeList(r.Path); err == nil {
		v.WorktreeCount = len(wts)
	}
	plans, _ := s.Store.ListPlans(r.ID)
	v.PlansCount = len(plans)
	taskList, _ := s.Store.ListTasks(r.ID, "")
	v.TasksCount = len(taskList)
	for _, t := range taskList {
		switch t.Status {
		case tasks.StatusNeedsReview:
			v.NeedsReview++
		case tasks.StatusDraft:
			v.Drafts++
		case tasks.StatusRunning:
			v.RunningTasks++
		}
		if t.FinishedAt > v.LastActivity {
			v.LastActivity = t.FinishedAt
		}
		if t.StartedAt > v.LastActivity {
			v.LastActivity = t.StartedAt
		}
	}
	return v
}

func isDefault(r tasks.Repo) bool {
	pref := config.DefaultRepoRef()
	return pref == r.Alias || pref == r.ID
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func (s *Service) ListRepositories() []RepoView {
	repos, _ := s.Store.ListRepos()
	out := make([]RepoView, 0, len(repos))
	for _, r := range repos {
		out = append(out, s.repoView(r))
	}
	return out
}

func (s *Service) AddRepository(path, alias string) (*RepoView, error) {
	repo, err := cli.RegisterRepo(s.Store, path, alias)
	if err != nil {
		return nil, err
	}
	view := s.repoView(*repo)
	return &view, nil
}

func (s *Service) RemoveRepository(ref string, purge bool) (map[string]any, error) {
	repo, err := cli.RemoveRepo(s.Store, ref, purge)
	if err != nil {
		return nil, err
	}
	return map[string]any{"removed": repo.ID, "alias": repo.Alias, "purged": purge}, nil
}

func (s *Service) SetDefaultRepository(ref string) error {
	repo, err := cli.ResolveRepo(s.Store, ref)
	if err != nil {
		return err
	}
	if err := config.SetDefaultRepo(repo.Alias); err != nil {
		return err
	}
	_ = s.Store.AppendEvent("default_repo_set", repo.ID, "", map[string]any{"alias": repo.Alias})
	return nil
}

func (s *Service) InspectRepository(ref string) (map[string]any, error) {
	repo, err := cli.ResolveRepo(s.Store, ref)
	if err != nil {
		return nil, err
	}
	info := cli.InspectRepo(s.Store, repo)
	if _, ok := info["error"]; !ok {
		info["view"] = s.repoView(*repo)
	}
	return info, nil
}

// ---- agents ----

func (s *Service) ListAgents(cached bool) []agents.CapabilityReport {
	if cached {
		return agents.ProbeAllCached()
	}
	agents.ClearProbeCache()
	return agents.ProbeAllCached()
}

// ConfigureAgent sets (path non-empty) or clears an executable override.
func (s *Service) ConfigureAgent(name, path string) error {
	if _, err := agents.Get(name); err != nil {
		return err
	}
	if path != "" {
		if !fileExists(path) {
			return fmt.Errorf("executable does not exist: %s", path)
		}
	}
	if err := config.SetAdapterOverride(name, path); err != nil {
		return err
	}
	agents.ClearProbeCache()
	_ = s.Store.AppendEvent("agent_configured", "", "", map[string]any{
		"agent": name, "override_set": path != ""})
	return nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

type DoctorReport struct {
	Checks []cli.DoctorCheck `json:"checks"`
	Repo   map[string]any    `json:"repo,omitempty"`
	Failed int               `json:"failed"`
}

func (s *Service) GetDoctorReport(repoRef string) DoctorReport {
	checks, repoInfo, failed := cli.BuildDoctor(s.Store, repoRef)
	return DoctorReport{Checks: checks, Repo: repoInfo, Failed: failed}
}

// ---- planning ----

type PlanValidation struct {
	OK           bool             `json:"ok"`
	Errors       []string         `json:"errors"`
	Tasks        []tasks.TaskSpec `json:"tasks"`
	TopoOrder    []string         `json:"topo_order"`
	DefaultAgent string           `json:"default_agent"`
	AgentWarning string           `json:"agent_warning,omitempty"`
	AgentChoices []string         `json:"agent_choices"`
}

func agentChoices() []string {
	var out []string
	for _, a := range agents.All() {
		out = append(out, a.Name())
	}
	sort.Strings(out)
	return out
}

// ValidatePlanSpec parses and validates a plan JSON without persisting
// anything. Mirrors exactly what CreatePlan will enforce.
func (s *Service) ValidatePlanSpec(specJSON, repoRef, baseBranch, defaultAgent string) PlanValidation {
	result := PlanValidation{AgentChoices: agentChoices()}
	var spec map[string]any
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		result.Errors = append(result.Errors, "invalid JSON: "+err.Error())
		return result
	}
	repo, _ := cli.ResolveRepo(s.Store, repoRef)
	repoPath := ""
	if repo != nil {
		repoPath = repo.Path
	}
	settings := config.EffectiveSettings(nil, repoPath)
	agentName, warning := cli.DefaultAgentName(defaultAgent, settings)
	if agentName == "" {
		result.Errors = append(result.Errors, warning)
		return result
	}
	result.DefaultAgent = agentName
	result.AgentWarning = warning
	rawTasks, _ := spec["tasks"].([]any)
	if len(rawTasks) == 0 {
		result.Errors = append(result.Errors, "plan spec must contain a non-empty 'tasks' list")
		return result
	}
	for i, raw := range rawTasks {
		m, ok := raw.(map[string]any)
		if !ok {
			result.Errors = append(result.Errors, fmt.Sprintf("task #%d must be an object", i+1))
			continue
		}
		spec1, err := cli.NormalizeTaskSpec(i, m, agentName)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			continue
		}
		result.Tasks = append(result.Tasks, spec1)
	}
	if len(result.Errors) > 0 {
		return result
	}
	order, err := tasks.ValidateDAG(result.Tasks)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result
	}
	result.TopoOrder = order
	if repo != nil {
		base := baseBranch
		if base == "" {
			if b, ok := spec["base_branch"].(string); ok {
				base = b
			}
		}
		if base == "" {
			base = repo.DefaultBranch
		}
		if _, err := gitops.RevParse(repo.Path, base); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf(
				"baseline branch '%s' not found in %s", base, repo.Alias))
			return result
		}
	}
	result.OK = true
	return result
}

func (s *Service) CreatePlan(repoRef, specJSON, baseBranch, defaultAgent string) (map[string]any, error) {
	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		return nil, err
	}
	var spec map[string]any
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return nil, fmt.Errorf("invalid plan JSON: %w", err)
	}
	settings := config.EffectiveSettings(nil, repo.Path)
	agentName, warning := cli.DefaultAgentName(defaultAgent, settings)
	if agentName == "" {
		return nil, fmt.Errorf("%s", warning)
	}
	plan, created, err := cli.CreatePlanFromSpec(s.Store, repo, spec, baseBranch, agentName)
	if err != nil {
		return nil, err
	}
	return map[string]any{"plan": plan, "tasks": created, "warning": warning}, nil
}

// CreatePlanWithPlanner asks an installed agent CLI to break goal into a
// draft task DAG (same code path and safety checks as `guy plan --planner`).
// It can take minutes; progress is reported via guy:planner events.
func (s *Service) CreatePlanWithPlanner(repoRef, goal, planner, baseBranch, defaultAgent string) (map[string]any, error) {
	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		return nil, err
	}
	settings := config.EffectiveSettings(nil, repo.Path)
	agentName, warning := cli.DefaultAgentName(defaultAgent, settings)
	if agentName == "" {
		return nil, fmt.Errorf("%s", warning)
	}
	s.sink.Emit("guy:planner", map[string]any{"repo": repo.ID, "planner": planner, "state": "running"})
	plan, created, info, err := cli.PlanWithPlanner(s.Store, repo, planner, goal, baseBranch, agentName, settings)
	if err != nil {
		s.sink.Emit("guy:planner", map[string]any{"repo": repo.ID, "planner": planner, "state": "failed", "error": err.Error()})
		return nil, err
	}
	s.sink.Emit("guy:planner", map[string]any{"repo": repo.ID, "planner": planner, "state": "done", "plan": plan.ID})
	return map[string]any{"plan": plan, "tasks": created, "warning": warning, "planner": info}, nil
}

type PlanView struct {
	tasks.Plan
	TaskCounts map[string]int `json:"task_counts"`
}

func (s *Service) ListPlans(repoRef string) ([]PlanView, error) {
	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		return nil, err
	}
	plans, err := s.Store.ListPlans(repo.ID)
	if err != nil {
		return nil, err
	}
	all, _ := s.Store.ListTasks(repo.ID, "")
	out := make([]PlanView, 0, len(plans))
	for _, p := range plans {
		counts := map[string]int{}
		for _, t := range all {
			if t.PlanID == p.ID {
				counts[t.Status]++
			}
		}
		out = append(out, PlanView{Plan: p, TaskCounts: counts})
	}
	return out, nil
}

func (s *Service) GetPlan(planID string) (map[string]any, error) {
	plan, err := s.Store.GetPlan(planID)
	if err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, fmt.Errorf("unknown plan '%s'", planID)
	}
	list, err := s.Store.ListTasks("", plan.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"plan": plan, "tasks": list}, nil
}

func (s *Service) ListTasks(repoRef, planID string) ([]*tasks.Task, error) {
	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		return nil, err
	}
	return s.Store.ListTasks(repo.ID, planID)
}

func (s *Service) GetTask(taskID string) (map[string]any, error) {
	task, err := s.Store.GetTask(taskID)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("unknown task '%s'", taskID)
	}
	runs, _ := s.Store.RunsForTask(task.ID)
	return map[string]any{"task": task, "runs": runs}, nil
}

// ---- execution ----

func (s *Service) Approve(target, note string) (map[string]any, error) {
	return cli.DoApprove(s.Store, target, note)
}

func (s *Service) RejectTask(taskID, reason string) (map[string]any, error) {
	if reason == "" {
		reason = "no reason given"
	}
	task, err := s.Store.GetTask(taskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("unknown task '%s'", taskID)
	}
	if task.Status != tasks.StatusNeedsReview {
		return nil, fmt.Errorf("%s is '%s'; reject applies to needs_review tasks", task.ID, task.Status)
	}
	_ = task.ApplyTransition(tasks.StatusFailed, reason)
	task.Concerns = append(task.Concerns, "rejected: "+reason)
	if task.FinishedAt == "" {
		task.FinishedAt = tasks.UTCNow()
	}
	_ = s.Store.UpdateTask(task)
	_ = s.Store.AppendEvent("task_rejected", task.RepoID, task.ID, map[string]any{"reason": reason})
	return map[string]any{"task": task.ID, "status": task.Status}, nil
}

func (s *Service) DryRun(repoRef, planID string, taskIDs []string) ([]scheduler.PlannedLaunch, error) {
	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		return nil, err
	}
	scheduler.Reconcile(s.Store)
	settings := config.EffectiveSettings(nil, repo.Path)
	list, err := cli.SelectRunTasks(s.Store, repo, planID, taskIDs)
	if err != nil {
		return nil, err
	}
	sched := scheduler.New(s.Store, settings, repo)
	return sched.DryRun(list), nil
}

// StartRun launches the selected (already approved) tasks. It validates
// statuses in Go exactly like `guy run --approve`, then executes in the
// background; progress reaches the UI via the event stream (persisted by the
// scheduler) and a final guy:run-summary event.
func (s *Service) StartRun(repoRef, planID string, taskIDs []string, maxWorkers int) (map[string]any, error) {
	s.mu.Lock()
	if s.batch {
		s.mu.Unlock()
		return nil, fmt.Errorf("a run batch is already active in this session; wait for it to finish or cancel its tasks first")
	}
	s.batch = true
	s.mu.Unlock()

	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		s.mu.Lock()
		s.batch = false
		s.mu.Unlock()
		return nil, err
	}
	scheduler.Reconcile(s.Store)
	overrides := map[string]any{}
	if maxWorkers > 0 {
		overrides["max_workers"] = maxWorkers
	}
	settings := config.EffectiveSettings(overrides, repo.Path)
	list, err := cli.SelectRunTasks(s.Store, repo, planID, taskIDs)
	if err != nil {
		s.mu.Lock()
		s.batch = false
		s.mu.Unlock()
		return nil, err
	}
	if len(list) == 0 {
		s.mu.Lock()
		s.batch = false
		s.mu.Unlock()
		return map[string]any{"started": false, "reason": "no tasks ready to run (approve draft tasks first)"}, nil
	}
	ids := make([]string, len(list))
	for i, t := range list {
		ids[i] = t.ID
	}
	sched := scheduler.New(s.Store, settings, repo)
	workers := config.SettingsInt(settings, "max_workers", 2)
	go func() {
		defer func() {
			s.mu.Lock()
			s.batch = false
			s.mu.Unlock()
		}()
		summary := sched.Execute(list, workers)
		s.emit("guy:run-summary", map[string]any{
			"summary": summaryToMap(summary), "finished_at": tasks.UTCNow()})
	}()
	return map[string]any{"started": true, "tasks": ids, "max_workers": workers}, nil
}

func summaryToMap(sm scheduler.Summary) map[string]any {
	return map[string]any{
		"launched": sm.Launched, "needs_review": sm.NeedsReview, "failed": sm.Failed,
		"cancelled": sm.Cancelled, "blocked": sm.Blocked, "retried": sm.Retried,
	}
}

func (s *Service) CancelTask(taskID string) (map[string]any, error) {
	task, err := cli.DoCancel(s.Store, taskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task.ID, "status": task.Status}, nil
}

func (s *Service) CleanTask(taskID string, deleteBranch bool) (map[string]any, error) {
	task, err := s.Store.GetTask(taskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("unknown task '%s'", taskID)
	}
	switch task.Status {
	case tasks.StatusFailed, tasks.StatusCancelled, tasks.StatusIntegrated:
	default:
		return nil, fmt.Errorf("%s is '%s'; only failed/cancelled/integrated tasks can be cleaned", task.ID, task.Status)
	}
	repo, _ := s.Store.GetRepo(task.RepoID)
	if repo == nil {
		return nil, fmt.Errorf("repo %s not registered", task.RepoID)
	}
	done, err := cli.CleanTask(s.Store, repo, task, deleteBranch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"actions": done}, nil
}

// ---- runs / logs ----

type RunView struct {
	tasks.RunRecord
	TaskTitle string `json:"task_title"`
	RepoID    string `json:"repo_id"`
}

func (s *Service) ListRuns(repoRef string, limit int) ([]RunView, error) {
	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	runs, err := s.Store.ListRunsForRepo(repo.ID, limit)
	if err != nil {
		return nil, err
	}
	titles := map[string][2]string{}
	all, _ := s.Store.ListTasks(repo.ID, "")
	for _, t := range all {
		titles[t.ID] = [2]string{t.Title, t.RepoID}
	}
	out := make([]RunView, 0, len(runs))
	for _, r := range runs {
		v := RunView{RunRecord: *r, RepoID: repo.ID}
		if info, ok := titles[r.TaskID]; ok {
			v.TaskTitle = info[0]
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) GetRun(runID string) (*tasks.RunRecord, error) {
	run, err := s.Store.GetRun(runID)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("unknown run '%s'", runID)
	}
	return run, nil
}

type LogChunk struct {
	RunID      string `json:"run_id"`
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`
	Size       int64  `json:"size"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	Content    string `json:"content"`
	Truncated  bool   `json:"truncated"`
}

// ReadLogRange streams a task's latest run log from byte offset, bounded to
// maxBytes (default 128 KiB, cap 1 MiB). The UI keeps a sliding window — the
// full (already redacted) log stays on disk, never fully in renderer memory.
func (s *Service) ReadLogRange(taskID string, offset int64, maxBytes int32) (LogChunk, error) {
	task, err := s.Store.GetTask(taskID)
	if err != nil || task == nil {
		return LogChunk{}, fmt.Errorf("unknown task '%s'", taskID)
	}
	runs, _ := s.Store.RunsForTask(task.ID)
	var logPath, runID string
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].LogPath != "" {
			runID = runs[i].ID
			if fileExists(runs[i].LogPath) {
				logPath = runs[i].LogPath
				break
			}
		}
	}
	chunk := LogChunk{RunID: runID, Path: logPath, Offset: offset}
	if logPath == "" {
		return chunk, nil
	}
	st, err := os.Stat(logPath)
	if err != nil {
		return chunk, nil
	}
	chunk.Exists = true
	chunk.Size = st.Size()
	if maxBytes <= 0 {
		maxBytes = 128 * 1024
	}
	if maxBytes > 1024*1024 {
		maxBytes = 1024 * 1024
	}
	if offset < 0 || offset > st.Size() {
		offset = 0
	}
	f, err := os.Open(logPath)
	if err != nil {
		return chunk, nil
	}
	defer f.Close()
	buf := make([]byte, int64(maxBytes))
	n, _ := f.ReadAt(buf, offset)
	chunk.Content = string(buf[:n])
	chunk.NextOffset = offset + int64(n)
	chunk.Truncated = n == int(maxBytes) && chunk.NextOffset < st.Size()
	return chunk, nil
}

// ---- review / integration ----

func (s *Service) GetReview(taskID string) (map[string]any, error) {
	task, err := s.Store.GetTask(taskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("unknown task '%s'", taskID)
	}
	repo, err := s.Store.GetRepo(task.RepoID)
	if err != nil || repo == nil {
		return nil, fmt.Errorf("task %s references unregistered repo %s", task.ID, task.RepoID)
	}
	return review.Collect(s.Store, repo, task), nil
}

type DiffView struct {
	Available    bool     `json:"available"`
	Branch       string   `json:"branch"`
	Worktree     string   `json:"worktree"`
	BaseCommit   string   `json:"base_commit"`
	ChangedFiles []string `json:"changed_files"`
	Stat         string   `json:"stat"`
	Patch        string   `json:"patch"`
	Truncated    bool     `json:"truncated"`
	Note         string   `json:"note,omitempty"`
}

func (s *Service) GetTaskDiff(taskID string, maxKB int32) (DiffView, error) {
	task, err := s.Store.GetTask(taskID)
	if err != nil || task == nil {
		return DiffView{}, fmt.Errorf("unknown task '%s'", taskID)
	}
	view := DiffView{
		Branch: task.Branch, Worktree: task.Worktree,
		BaseCommit: task.BaselineCommit, ChangedFiles: task.ChangedFiles,
	}
	if task.ReadOnly || task.Worktree == "" {
		view.Note = "read-only task — no worktree diff"
		return view, nil
	}
	if !dirExists(task.Worktree) {
		view.Note = "worktree no longer present; showing recorded changed files"
		return view, nil
	}
	view.Available = true
	if maxKB <= 0 {
		maxKB = 256
	}
	if maxKB > 2048 {
		maxKB = 2048
	}
	if stat, err := gitops.DiffStat(task.Worktree, task.BaselineCommit); err == nil {
		view.Stat = stat
	}
	if files, err := gitops.DiffNameOnly(task.Worktree, task.BaselineCommit); err == nil {
		seen := map[string]bool{}
		var merged []string
		for _, f := range append(append([]string{}, task.ChangedFiles...), files...) {
			if !seen[f] {
				seen[f] = true
				merged = append(merged, f)
			}
		}
		sort.Strings(merged)
		view.ChangedFiles = merged
	}
	patch, truncated, err := gitops.DiffPatch(task.Worktree, task.BaselineCommit, int64(maxKB)*1024)
	if err != nil {
		view.Note = "diff unavailable: " + err.Error()
		return view, nil
	}
	view.Patch = redact.Redact(patch)
	view.Truncated = truncated
	return view, nil
}

// RunTests executes the task's declared test commands (they execute code —
// the UI must present this as an explicit action, never automatic).
func (s *Service) RunTests(taskID string) ([]tasks.TestResult, error) {
	task, err := s.Store.GetTask(taskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("unknown task '%s'", taskID)
	}
	switch task.Status {
	case tasks.StatusNeedsReview, tasks.StatusFailed, tasks.StatusApproved:
	default:
		return nil, fmt.Errorf("tests apply to finished tasks (%s is '%s')", task.ID, task.Status)
	}
	repo, err := s.Store.GetRepo(task.RepoID)
	if err != nil || repo == nil {
		return nil, fmt.Errorf("task %s references unregistered repo %s", task.ID, task.RepoID)
	}
	settings := config.EffectiveSettings(nil, repo.Path)
	return review.RunTests(s.Store, repo, task, settings)
}

func (s *Service) PreviewIntegration(repoRef, planID string, skipTests bool) (map[string]any, error) {
	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		return nil, err
	}
	scheduler.Reconcile(s.Store)
	plan, err := cli.PlanForIntegrate(s.Store, repo, planID)
	if err != nil {
		return nil, err
	}
	list, _ := s.Store.ListTasks("", plan.ID)
	return review.IntegrationPreview(s.Store, repo, plan, list, skipTests), nil
}

// Integrate performs the explicitly approved integration step (records the
// approval like `guy integrate --approve`, then merges approved task branches
// into the plan's integration branch — never the default branch, never a push).
func (s *Service) Integrate(repoRef, planID string, skipTests bool) (map[string]any, error) {
	repo, err := cli.ResolveRepo(s.Store, repoRef)
	if err != nil {
		return nil, err
	}
	scheduler.Reconcile(s.Store)
	plan, err := cli.PlanForIntegrate(s.Store, repo, planID)
	if err != nil {
		return nil, err
	}
	list, _ := s.Store.ListTasks("", plan.ID)
	approvalID, _ := s.Store.NextID("APR")
	_ = s.Store.AddApproval(tasks.ApprovalRecord{
		ID: approvalID, TargetKind: "plan", TargetID: plan.ID, Kind: "integration",
		Note: "explicit integration approval (desktop GUI)", CreatedAt: tasks.UTCNow()})
	return review.PerformIntegration(s.Store, repo, plan, list, skipTests)
}

// ---- settings / diagnostics ----

func (s *Service) GetSettings() (map[string]any, error) {
	settings := config.UserSettings()
	keys := make([]string, 0, len(config.Defaults))
	for k := range config.Defaults {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return map[string]any{"settings": settings, "known_keys": keys,
		"defaults": config.Defaults}, nil
}

func (s *Service) UpdateSetting(key string, value any) error {
	if err := config.SetUserSetting(key, value); err != nil {
		return err
	}
	_ = s.Store.AppendEvent("setting_changed", "", "", map[string]any{"key": key})
	return nil
}

// ExportDiagnostics returns a redacted diagnostic bundle (JSON text). It
// contains no environment variables, tokens, or file contents.
func (s *Service) ExportDiagnostics() string {
	bundle := map[string]any{
		"generated_at": tasks.UTCNow(),
		"health":       s.GetServiceHealth(),
		"doctor":       s.GetDoctorReport(""),
		"agents":       agents.ProbeAllCached(),
		"settings":     config.UserSettings(),
	}
	if events, err := s.Store.RecentEvents(100, "", ""); err == nil {
		bundle["recent_events"] = events
	}
	if repos, err := s.Store.ListRepos(); err == nil {
		bundle["repos"] = repos
	}
	data, _ := json.MarshalIndent(bundle, "", "  ")
	return redact.Redact(string(data))
}
