package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dwarkeshkaswala/the-guy/internal/agents"
	"github.com/dwarkeshkaswala/the-guy/internal/config"
	"github.com/dwarkeshkaswala/the-guy/internal/gitops"
	"github.com/dwarkeshkaswala/the-guy/internal/process"
	"github.com/dwarkeshkaswala/the-guy/internal/redact"
	"github.com/dwarkeshkaswala/the-guy/internal/review"
	"github.com/dwarkeshkaswala/the-guy/internal/scheduler"
	"github.com/dwarkeshkaswala/the-guy/internal/store"
	"github.com/dwarkeshkaswala/the-guy/internal/tasks"
)

func cmdInit(c *Ctx, p parsed) error {
	if err := config.EnsureDirs(); err != nil {
		return err
	}
	if _, err := os.Stat(config.DBPath()); err != nil {
		st, err := store.Open(config.DBPath())
		if err != nil {
			return err
		}
		_ = st.Close()
	}
	return okErr(c, fmt.Sprintf(
		"the-guy initialized\ndata: %s\nprefs: %s\ndb: %s",
		config.DataDir(), config.PrefsDir(), config.DBPath()), nil)
}

// DoctorCheck is one doctor entry (status: ok | warn | fail).
type DoctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func cmdDoctor(c *Ctx, p parsed) error {
	checks, repoInfo, failed := BuildDoctor(c.Store, p.flag("repo"))
	if c.JSON {
		c.emit(map[string]any{"checks": checks, "repo": repoInfo}, "")
	} else {
		var lines []string
		for _, ck := range checks {
			lines = append(lines, fmt.Sprintf("[%-4s] %s: %s", strings.ToUpper(ck.Status), ck.Name, ck.Detail))
		}
		lines = append(lines, "", fmt.Sprintf("%d failure(s), the-guy is otherwise operational.", failed))
		fmt.Fprintln(c.Out, strings.Join(lines, "\n"))
	}
	if failed > 0 {
		return fmt.Errorf("doctor found %d failure(s)", failed)
	}
	return nil
}

// BuildDoctor assembles the doctor report (shared by the CLI command and the
// desktop GUI service). Returns checks, optional repo info, and the failure
// count.
func BuildDoctor(st *store.Store, repoRef string) ([]DoctorCheck, map[string]any, int) {
	var checks []DoctorCheck
	add := func(name, status, detail string) {
		checks = append(checks, DoctorCheck{name, status, detail})
	}
	add("go", "ok", "the-guy "+Version)
	if v, err := gitops.Version(); err == nil {
		level := "ok"
		detail := fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
		if v[0] == 2 && v[1] < 38 {
			level = "warn"
			detail += " — merge-tree conflict preview requires git >= 2.38 (integration still checks at merge time)"
		}
		if v[0] == 2 && v[1] < 17 || v[0] < 2 {
			level = "fail"
			detail += " — git worktrees require >= 2.17"
		}
		add("git", level, detail)
	} else {
		add("git", "fail", err.Error())
	}
	for label, dir := range map[string]string{"data dir": config.DataDir(), "prefs dir": config.PrefsDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			add(label, "fail", dir+": "+err.Error())
			continue
		}
		probe := filepath.Join(dir, ".guy-write-test")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			add(label, "fail", fmt.Sprintf("%s: %v", dir, err))
		} else {
			_ = os.Remove(probe)
			add(label, "ok", dir)
		}
	}
	repos, _ := st.ListRepos()
	add("state db", "ok", fmt.Sprintf("%s (%d repo(s) registered)", config.DBPath(), len(repos)))

	for _, report := range agents.ProbeAllCached() {
		if report.Kind == "mock" {
			add("agent:"+report.Adapter, "ok", "built-in mock adapter ready (tests/demo)")
			continue
		}
		level := "warn"
		if report.HeadlessVerified {
			level = "ok"
		}
		detail := report.HeadlessNote
		if report.Version != "" {
			detail = report.Version + " — " + report.HeadlessNote
		}
		if report.Source != "" && report.Installed {
			detail += " (source: " + report.Source + ")"
		}
		add("agent:"+report.Adapter, level, detail)
	}
	if pref := config.DefaultRepoRef(); pref != "" {
		if repo, _ := st.GetRepo(pref); repo != nil {
			add("default repo", "ok", pref)
		} else {
			add("default repo", "warn", pref+" (no longer registered)")
		}
	}
	var repoInfo map[string]any
	if repoRef != "" || config.DefaultRepoRef() != "" {
		if repo, err := resolveRepo(st, repoRef); err == nil {
			info := inspectRepo(st, repo)
			repoInfo = info
			if errText, ok := info["error"].(string); ok {
				add("repo.exists", "fail", errText)
			} else {
				add("repo.exists", "ok", info["path"].(string))
				if info["dirty"].(bool) {
					add("repo.dirty", "warn", fmt.Sprintf(
						"working tree has %d uncommitted change(s) (the-guy will never discard them; worktrees start from committed baselines)",
						info["dirty_file_count"]))
				} else {
					add("repo.dirty", "ok", "clean")
				}
				add("repo.default_branch", "ok", info["default_branch"].(string))
			}
		}
	}
	failed := 0
	for _, ck := range checks {
		if ck.Status == "fail" {
			failed++
		}
	}
	return checks, repoInfo, failed
}

func cmdRepo(c *Ctx, p parsed) error {
	sub := p.arg(0)
	switch sub {
	case "add":
		path := p.arg(1)
		if path == "" {
			return usage("usage: guy repo add <path> [--alias name]")
		}
		repo, err := registerRepo(c.Store, path, p.flag("alias"))
		if err != nil {
			return err
		}
		extra := ""
		if settings, err := config.RepoSettings(repo.Path); err == nil && len(settings) > 0 {
			keys := make([]string, 0, len(settings))
			for k := range settings {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			extra = "\n  repo config found: " + strings.Join(keys, ", ")
		}
		text := fmt.Sprintf("registered repo '%s' (%s)\n  path: %s\n  default branch: %s (detected, never assumed)\n  state lives outside the repo: %s%s",
			repo.Alias, repo.ID, repo.Path, repo.DefaultBranch, config.DataDir(), extra)
		return okErr(c, text, map[string]any{"repo": repo})
	case "list":
		repos, _ := c.Store.ListRepos()
		var lines []string
		for _, r := range repos {
			marker := ""
			if config.DefaultRepoRef() == r.Alias || config.DefaultRepoRef() == r.ID {
				marker = " (default)"
			}
			exists := ""
			if !pathExists(r.Path) {
				exists = "  [PATH MISSING]"
			}
			lines = append(lines, fmt.Sprintf("  %s%s\n    id: %s  path: %s  default branch: %s%s",
				r.Alias, marker, r.ID, r.Path, r.DefaultBranch, exists))
		}
		text := "No repositories registered. Run: guy repo add <path> --alias <name>"
		if len(lines) > 0 {
			text = "Registered repositories:\n" + strings.Join(lines, "\n")
		}
		return okErr(c, text, map[string]any{"repos": repos, "default": config.DefaultRepoRef()})
	case "remove":
		ref := p.arg(1)
		if ref == "" {
			return usage("usage: guy repo remove <alias|path|id> [--purge]")
		}
		repo, err := removeRepo(c.Store, ref, p.bool("purge"))
		if err != nil {
			return err
		}
		text := fmt.Sprintf("removed repo '%s' (%s)", repo.Alias, repo.ID)
		if p.bool("purge") {
			text += " and purged orchestrator-managed worktrees (clean ones only)"
		} else {
			text += fmt.Sprintf("\n  kept: worktrees under %s and all logs (audit trail)",
				filepath.Join(config.WorktreesRoot(), repo.ID))
		}
		return okErr(c, text, map[string]any{"removed": repo.ID, "purged": p.bool("purge")})
	case "use":
		return useRepo(c, p.arg(1))
	case "":
		return usage("usage: guy repo add|list|remove|use")
	}
	return usage("unknown repo subcommand %q", sub)
}

func cmdUse(c *Ctx, p parsed) error { return useRepo(c, p.arg(0)) }

func useRepo(c *Ctx, alias string) error {
	if alias == "" {
		current := config.DefaultRepoRef()
		if current == "" {
			return okErr(c, "no default repo set; run: guy use <alias>", nil)
		}
		repo, _ := c.Store.GetRepo(current)
		text := "default repo: " + current
		if repo != nil {
			text += " -> " + repo.Path
		} else {
			text += " (not registered anymore)"
		}
		return okErr(c, text, nil)
	}
	repo, err := c.Store.GetRepo(alias)
	if err != nil || repo == nil {
		return fmt.Errorf("no registered repository matches '%s'", alias)
	}
	if err := config.SetDefaultRepo(repo.Alias); err != nil {
		return err
	}
	_ = c.Store.AppendEvent("default_repo_set", repo.ID, "", map[string]any{"alias": repo.Alias})
	text := fmt.Sprintf("default repo set to '%s'\n  stored per-user in preferences (%s); it does not modify your shell/terminal state.",
		repo.Alias, config.PrefsPath())
	return okErr(c, text, map[string]any{"default_repo": repo.Alias})
}

func cmdInspect(c *Ctx, p parsed) error {
	repo, err := resolveRepo(c.Store, p.flag("repo"))
	if err != nil {
		return err
	}
	info := inspectRepo(c.Store, repo)
	if errText, ok := info["error"].(string); ok {
		return fmt.Errorf("%s", errText)
	}
	text := fmt.Sprintf("repo '%s' (%s)\n  path: %s\n  current branch: %s  default branch: %s\n  head: %.12s\n",
		info["alias"], info["id"], info["path"], info["current_branch"], info["default_branch"], fmt.Sprint(info["head_commit"]))
	if info["dirty"].(bool) {
		text += fmt.Sprintf("  working tree: DIRTY (%d change(s), never discarded)\n", info["dirty_file_count"])
	} else {
		text += "  working tree: clean\n"
	}
	text += "  project files: " + strings.Join(toStringSlice(info["project_files"]), ", ") + "\n"
	text += "  test command hints (NOT executed): " + strings.Join(toStringSlice(info["test_command_hints"]), "; ") + "\n"
	text += "  agent config files present: " + strings.Join(toStringSlice(info["agent_config_files"]), ", ") + "\n"
	managed, _ := info["managed_worktrees"].([]map[string]any)
	text += fmt.Sprintf("  guy plans: %v, managed worktrees: %d", info["plans_count"], len(managed))
	return okErr(c, text, info)
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		var out []string
		for _, item := range t {
			out = append(out, fmt.Sprint(item))
		}
		return out
	}
	return nil
}

func cmdTasks(c *Ctx, p parsed) error {
	repo, err := resolveRepo(c.Store, p.flag("repo"))
	if err != nil {
		return err
	}
	list, err := c.Store.ListTasks(repo.ID, p.flag("plan"))
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, t := range list {
		counts[t.Status]++
	}
	var lines []string
	lines = append(lines, fmt.Sprintf("tasks for repo '%s'%s:", repo.Alias, planSuffix(p.flag("plan"))))
	if len(list) == 0 {
		lines = append(lines, "  (none — create one with `guy plan \"...\"`)")
	}
	plans, _ := c.Store.ListPlans(repo.ID)
	planMap := map[string]tasks.Plan{}
	for _, pl := range plans {
		planMap[pl.ID] = pl
	}
	currentPlan := ""
	for _, t := range list {
		if t.PlanID != currentPlan {
			currentPlan = t.PlanID
			pl := planMap[currentPlan]
			lines = append(lines, fmt.Sprintf("  plan %s (status: %s, baseline %s@%.8s)",
				currentPlan, pl.Status, pl.BaselineBranch, pl.BaselineCommit))
		}
		deps := "-"
		if len(t.Dependencies) > 0 {
			deps = strings.Join(shortIDs(t.Dependencies), ",")
		}
		ro := "W"
		if t.ReadOnly {
			ro = "R"
		}
		lines = append(lines, fmt.Sprintf("  %-10s [%-24s] %s %-8s deps:%-8s %s",
			t.ID, t.Status, ro, t.Agent, deps, t.Title))
	}
	var countParts []string
	for _, k := range sortedKeys(counts) {
		countParts = append(countParts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	lines = append(lines, "", "  status counts: "+strings.Join(countParts, ", "))
	return okErr(c, strings.Join(lines, "\n"), map[string]any{
		"repo": repo.Alias, "counts": counts, "tasks": list})
}

func shortIDs(ids []string) []string {
	var out []string
	for _, id := range ids {
		parts := strings.SplitN(id, "-", 2)
		if len(parts) == 2 {
			out = append(out, parts[1])
		} else {
			out = append(out, id)
		}
	}
	return out
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func planSuffix(planID string) string {
	if planID == "" {
		return " (all plans)"
	}
	return ", plan " + planID
}

func cmdShow(c *Ctx, p parsed) error {
	id := p.arg(0)
	if id == "" {
		return usage("usage: guy show <TASK-ID>")
	}
	task, err := c.Store.GetTask(id)
	if err != nil || task == nil {
		return fmt.Errorf("unknown task '%s'", id)
	}
	runs, _ := c.Store.RunsForTask(task.ID)
	if c.JSON {
		return okErr(c, "", map[string]any{"task": task, "runs": runs})
	}
	exit := ""
	if task.ExitCode != nil {
		exit = strconv.Itoa(*task.ExitCode)
	}
	lines := []string{
		fmt.Sprintf("%s: %s  [%s]", task.ID, task.Title, task.Status),
		fmt.Sprintf("  plan: %s  repo: %s  agent: %s%s", task.PlanID, task.RepoID, task.Agent, readOnlySuffix(task)),
		"  objective: " + truncate(task.Objective, 200),
		fmt.Sprintf("  baseline: %s@%.10s", task.BaselineBranch, task.BaselineCommit),
		fmt.Sprintf("  branch: %s  worktree: %s", orDash2(task.Branch), orDash2(task.Worktree)),
		"  dependencies: " + strings.Join(task.Dependencies, ", "),
		"  acceptance criteria: " + strings.Join(task.AcceptanceCriteria, " | "),
		"  required tests: " + strconv.Itoa(len(task.RequiredTests)),
		"  changed files: " + strings.Join(task.ChangedFiles, ", "),
		fmt.Sprintf("  exit: %s (%s)", exit, task.ExitReason),
		"  blocked reason: " + orDash2(task.BlockedReason),
	}
	for _, r := range task.TestResults {
		status := "FAIL"
		if r.OK {
			status = "PASS"
		}
		lines = append(lines, fmt.Sprintf("  test [%s] %s", status, r.Command))
	}
	fmt.Fprintln(c.Out, strings.Join(lines, "\n"))
	_ = runs
	return nil
}

func readOnlySuffix(t *tasks.Task) string {
	if t.ReadOnly {
		return "  (read-only)"
	}
	return ""
}

func orDash2(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// doApprove implements the approval workflow (shared with demo).
func doApprove(st *store.Store, target, note string) (map[string]any, error) {
	if strings.HasPrefix(target, "PLAN-") {
		plan, err := st.GetPlan(target)
		if err != nil || plan == nil {
			return nil, fmt.Errorf("unknown plan '%s'", target)
		}
		list, _ := st.ListTasks("", plan.ID)
		var drafts []*tasks.Task
		statuses := map[string]bool{}
		for _, t := range list {
			statuses[t.Status] = true
			if t.Status == tasks.StatusDraft {
				drafts = append(drafts, t)
			}
		}
		if len(drafts) == 0 {
			var names []string
			for s := range statuses {
				names = append(names, s)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("plan %s has no draft tasks (statuses: %s)", plan.ID, strings.Join(names, ", "))
		}
		var ids []string
		for _, t := range drafts {
			_ = t.ApplyTransition(tasks.StatusApprovedForExecution, "approved via plan approval")
			_ = st.UpdateTask(t)
			_ = st.AppendEvent("task_approved_for_execution", t.RepoID, t.ID, nil)
			ids = append(ids, t.ID)
		}
		approvalID, _ := st.NextID("APR")
		_ = st.AddApproval(tasks.ApprovalRecord{
			ID: approvalID, TargetKind: "plan", TargetID: plan.ID, Kind: "execute",
			Note: "plan approval (draft -> approved_for_execution)", CreatedAt: tasks.UTCNow()})
		plan.Status = "approved"
		_ = st.UpdatePlan(*plan)
		_ = st.AppendEvent("plan_approved", plan.RepoID, "", map[string]any{"tasks": ids})
		return map[string]any{"kind": "plan", "target": plan.ID,
			"approved_for_execution": ids,
			"note":                   "approves EXECUTION of these tasks only — not any merge, commit, or push"}, nil
	}
	task, err := st.GetTask(target)
	if err != nil || task == nil {
		return nil, fmt.Errorf("unknown task '%s'", target)
	}
	var kind, msg string
	switch task.Status {
	case tasks.StatusDraft:
		_ = task.ApplyTransition(tasks.StatusApprovedForExecution, "explicit approval")
		kind = "execute"
		msg = task.ID + ": draft -> approved_for_execution. This approves launching the agent in an isolated worktree — it is NOT a merge, commit, or push."
	case tasks.StatusNeedsReview:
		_ = task.ApplyTransition(tasks.StatusApproved, "explicit review approval")
		kind = "review"
		msg = task.ID + ": needs_review -> approved. This records human review of the changes — it is NOT a merge, commit, or push. Run `guy integrate --dry-run` next."
	default:
		return nil, fmt.Errorf("%s is '%s'; approve applies to draft or needs_review tasks", task.ID, task.Status)
	}
	if err := st.UpdateTask(task); err != nil {
		return nil, err
	}
	approvalID, _ := st.NextID("APR")
	_ = st.AddApproval(tasks.ApprovalRecord{
		ID: approvalID, TargetKind: "task", TargetID: task.ID, Kind: kind,
		Note: note, CreatedAt: tasks.UTCNow()})
	eventKind := "task_approved_for_execution"
	if kind == "review" {
		eventKind = "task_review_approved"
	}
	_ = st.AppendEvent(eventKind, task.RepoID, task.ID, nil)
	return map[string]any{"kind": kind, "target": task.ID, "message": msg}, nil
}

func cmdApprove(c *Ctx, p parsed) error {
	target := p.arg(0)
	if target == "" {
		return usage("usage: guy approve <TASK-ID|PLAN-ID> [--note ...]")
	}
	result, err := doApprove(c.Store, target, p.flag("note"))
	if err != nil {
		return err
	}
	if kind, _ := result["kind"].(string); kind == "plan" {
		ids := toStringSlice(result["approved_for_execution"])
		text := fmt.Sprintf("plan %s: approved tasks for execution: %s\n  %s",
			result["target"], strings.Join(ids, ", "), result["note"])
		return okErr(c, text, result)
	}
	return okErr(c, result["message"].(string), result)
}

func cmdReject(c *Ctx, p parsed) error {
	id := p.arg(0)
	if id == "" {
		return usage("usage: guy reject <TASK-ID> --reason ...")
	}
	task, err := c.Store.GetTask(id)
	if err != nil || task == nil {
		return fmt.Errorf("unknown task '%s'", id)
	}
	if task.Status != tasks.StatusNeedsReview {
		return fmt.Errorf("%s is '%s'; reject applies to needs_review tasks", task.ID, task.Status)
	}
	reason := p.flag("reason")
	if reason == "" {
		reason = "no reason given"
	}
	_ = task.ApplyTransition(tasks.StatusFailed, reason)
	task.Concerns = append(task.Concerns, "rejected: "+reason)
	if task.FinishedAt == "" {
		task.FinishedAt = tasks.UTCNow()
	}
	_ = c.Store.UpdateTask(task)
	_ = c.Store.AppendEvent("task_rejected", task.RepoID, task.ID, map[string]any{"reason": reason})
	return okErr(c, task.ID+": needs_review -> failed (rejected: "+reason+")",
		map[string]any{"task": task.ID, "status": task.Status})
}

func selectRunTasks(st *store.Store, repo *tasks.Repo, planID string, taskIDs []string) ([]*tasks.Task, error) {
	list, err := st.ListTasks(repo.ID, planID)
	if err != nil {
		return nil, err
	}
	if len(taskIDs) > 0 {
		known := map[string]*tasks.Task{}
		for _, t := range list {
			known[t.ID] = t
		}
		var missing []string
		for _, id := range taskIDs {
			if _, ok := known[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			return nil, &scheduler.SchedulingError{Msg: "unknown task id(s) for this repo: " + strings.Join(missing, ", ")}
		}
		var out []*tasks.Task
		for _, id := range taskIDs {
			t := known[id]
			switch t.Status {
			case tasks.StatusApprovedForExecution, tasks.StatusQueued, tasks.StatusBlocked, tasks.StatusFailed:
				out = append(out, t)
			}
		}
		return out, nil
	}
	var out []*tasks.Task
	for _, t := range list {
		switch t.Status {
		case tasks.StatusApprovedForExecution, tasks.StatusQueued, tasks.StatusBlocked:
			out = append(out, t)
		}
	}
	return out, nil
}

func cmdRun(c *Ctx, p parsed) error {
	repo, err := resolveRepo(c.Store, p.flag("repo"))
	if err != nil {
		return err
	}
	scheduler.Reconcile(c.Store)
	overrides := map[string]any{}
	if p.flag("max-workers") != "" {
		if n, err := strconv.Atoi(p.flag("max-workers")); err == nil {
			overrides["max_workers"] = n
		}
	}
	settings := config.EffectiveSettings(overrides, repo.Path)
	var taskIDs []string
	if p.flag("task") != "" {
		taskIDs = strings.Split(p.flag("task"), ",")
	}
	list, err := selectRunTasks(c.Store, repo, p.flag("plan"), taskIDs)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		var drafts []string
		all, _ := c.Store.ListTasks(repo.ID, p.flag("plan"))
		for _, t := range all {
			if t.Status == tasks.StatusDraft {
				drafts = append(drafts, t.ID)
			}
		}
		hint := ""
		if len(drafts) > 0 {
			hint = " — approve first: `guy approve " + strings.Join(drafts, " ") + "`"
		}
		return okErr(c, "no tasks ready to run"+hint, map[string]any{"launched": []string{}, "note": "no tasks ready" + hint})
	}
	s := scheduler.New(c.Store, settings, repo)
	if !p.bool("approve") {
		launches := s.DryRun(list)
		text := "DRY RUN — nothing will be launched; no worktrees are created. Pass --approve to execute.\n\nProposed launches:"
		for _, l := range launches {
			scope := fmt.Sprintf("worktree: %s\n  branch: %s", l.Worktree, l.Branch)
			if l.ReadOnly {
				scope = "read-only (runs in repo root)"
			}
			text += fmt.Sprintf("\n  %s: %s\n  agent: %s  timeout: %ds  baseline: %s@%.10s\n  %s\n  cwd: %s\n  command: %s",
				l.TaskID, l.Title, l.Agent, l.TimeoutSeconds, l.BaselineBranch, l.BaselineCommit, scope, l.Cwd, l.ArgvDisplay)
			if !l.AdapterOK {
				text += "\n  ADAPTER PROBLEM: " + l.AdapterNote
			}
			if len(l.PendingDependents) > 0 {
				text += "\n  pending deps: " + strings.Join(l.PendingDependents, ", ")
			}
		}
		return okErr(c, text, map[string]any{"dry_run": true, "launches": launches})
	}
	summary := s.Execute(list, config.SettingsInt(settings, "max_workers", 2))
	var lines []string
	lines = append(lines, fmt.Sprintf("run complete (max %d concurrent workers):", config.SettingsInt(settings, "max_workers", 2)))
	for _, bucket := range []string{"launched", "needs_review", "failed", "cancelled", "blocked", "retried"} {
		for _, entry := range summaryBucket(summary, bucket) {
			reason := ""
			if r, ok := entry["reason"].(string); ok && r != "" {
				reason = " — " + r
			}
			lines = append(lines, fmt.Sprintf("  [%s] %v%s", bucket, entry["task"], reason))
		}
	}
	lines = append(lines, "", "next: `guy status`, `guy logs <TASK>`, `guy review <TASK>` (nothing has been committed or merged)")
	c.emit(summaryToMap(summary), strings.Join(lines, "\n"))
	if len(summary.Failed) > 0 {
		return errRunWithFailures
	}
	return nil
}

var errRunWithFailures = fmt.Errorf("one or more tasks failed")

func summaryBucket(s scheduler.Summary, bucket string) []map[string]any {
	switch bucket {
	case "launched":
		return s.Launched
	case "needs_review":
		return s.NeedsReview
	case "failed":
		return s.Failed
	case "cancelled":
		return s.Cancelled
	case "blocked":
		return s.Blocked
	case "retried":
		return s.Retried
	}
	return nil
}

func summaryToMap(s scheduler.Summary) map[string]any {
	return map[string]any{
		"launched": s.Launched, "needs_review": s.NeedsReview, "failed": s.Failed,
		"cancelled": s.Cancelled, "blocked": s.Blocked, "retried": s.Retried,
	}
}

func cmdStatus(c *Ctx, p parsed) error {
	repo, err := resolveRepo(c.Store, p.flag("repo"))
	if err != nil {
		return err
	}
	scheduler.Reconcile(c.Store)
	info := inspectRepo(c.Store, repo)
	plans, _ := c.Store.ListPlans(repo.ID)
	taskList, _ := c.Store.ListTasks(repo.ID, "")
	active, _ := c.Store.ActiveRuns()
	lines := []string{fmt.Sprintf("status for '%s' (%s)", repo.Alias, repo.Path)}
	if errText, ok := info["error"].(string); ok {
		lines = append(lines, "  ERROR: "+errText)
	} else {
		dirty := "clean"
		if info["dirty"].(bool) {
			dirty = "DIRTY"
		}
		lines = append(lines, fmt.Sprintf("  branch: %s (default: %s) %s",
			info["current_branch"], info["default_branch"], dirty))
	}
	lines = append(lines, fmt.Sprintf("  plans: %d  tasks: %d", len(plans), len(taskList)))
	for _, pl := range plans {
		counts := map[string]int{}
		for _, t := range taskList {
			if t.PlanID == pl.ID {
				counts[t.Status]++
			}
		}
		var countParts []string
		for _, k := range sortedKeys(counts) {
			countParts = append(countParts, fmt.Sprintf("%s=%d", k, counts[k]))
		}
		lines = append(lines, fmt.Sprintf("    %s [%s] baseline %s@%.8s  %s",
			pl.ID, pl.Status, pl.BaselineBranch, pl.BaselineCommit, strings.Join(countParts, ", ")))
	}
	if len(active) > 0 {
		lines = append(lines, "  active runs:")
		for _, r := range active {
			lines = append(lines, fmt.Sprintf("    %s task %s adapter %s pid %d (%s)",
				r.ID, r.TaskID, r.Adapter, r.Pid, r.Status))
		}
	} else {
		lines = append(lines, "  active runs: none")
	}
	managed, _ := info["managed_worktrees"].([]map[string]any)
	lines = append(lines, fmt.Sprintf("  managed worktrees: %d", len(managed)))
	lines = append(lines, "  recent events:")
	events, _ := c.Store.RecentEvents(8, "", repo.ID)
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		lines = append(lines, fmt.Sprintf("    %s %s %s", e.TS, e.Kind, e.TaskID))
	}
	payload := map[string]any{"repo": info, "plans": plans, "tasks": taskList, "active_runs": active}
	return okErr(c, strings.Join(lines, "\n"), payload)
}

func cmdLogs(c *Ctx, p parsed) error {
	id := p.arg(0)
	if id == "" {
		return usage("usage: guy logs <TASK-ID> [--follow] [--lines N]")
	}
	task, err := c.Store.GetTask(id)
	if err != nil || task == nil {
		return fmt.Errorf("unknown task '%s'", id)
	}
	runs, _ := c.Store.RunsForTask(task.ID)
	var logPath string
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].LogPath != "" && fileExists(runs[i].LogPath) {
			logPath = runs[i].LogPath
			break
		}
	}
	if logPath == "" {
		return okErr(c, "no logs recorded for "+task.ID+" yet", map[string]any{"task": task.ID, "logs": []string{}})
	}
	lines := 40
	if p.flag("lines") != "" {
		if n, err := strconv.Atoi(p.flag("lines")); err == nil {
			lines = n
		}
	}
	body := redact.Redact(tailFile(logPath, lines))
	if c.JSON {
		return okErr(c, "", map[string]any{"task": task.ID, "log": logPath, "content": body})
	}
	fmt.Fprintf(c.Out, "log: %s\n%s\n", logPath, body)
	if p.bool("follow") {
		offset := int64(fileSize(logPath))
		quiet := 0
		for quiet < 2 {
			time.Sleep(500 * time.Millisecond)
			fresh, _ := c.Store.GetTask(task.ID)
			size := fileSize(logPath)
			if size > offset {
				if chunk := readFileFrom(logPath, offset); chunk != "" {
					fmt.Fprint(c.Out, redact.Redact(chunk))
				}
				offset = size
				quiet = 0
			} else {
				quiet++
			}
			if fresh != nil && (fresh.Status == tasks.StatusIntegrated || fresh.Status == tasks.StatusCancelled) && quiet >= 1 {
				break
			}
		}
	}
	return nil
}

func fileSize(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size()
}

func readFileFrom(p string, offset int64) string {
	f, err := os.Open(p)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 64*1024)
	n, _ := f.ReadAt(buf, offset)
	return string(buf[:n])
}

func tailFile(path string, lines int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return "(log unreadable)"
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}
	return strings.Join(all, "\n")
}

func cmdReview(c *Ctx, p parsed) error {
	id := p.arg(0)
	if id == "" {
		return usage("usage: guy review <TASK-ID> [--run-tests]")
	}
	task, err := c.Store.GetTask(id)
	if err != nil || task == nil {
		return fmt.Errorf("unknown task '%s'", id)
	}
	repo, err := c.Store.GetRepo(task.RepoID)
	if err != nil || repo == nil {
		return fmt.Errorf("task %s references unregistered repo %s", task.ID, task.RepoID)
	}
	settings := config.EffectiveSettings(nil, repo.Path)
	payload := map[string]any{}
	if p.bool("run-tests") {
		switch task.Status {
		case tasks.StatusNeedsReview, tasks.StatusFailed, tasks.StatusApproved:
		default:
			return &review.ReviewError{Msg: fmt.Sprintf(
				"--run-tests applies to finished tasks (%s is '%s'); tests execute code — run them only on work you have reviewed",
				task.ID, task.Status)}
		}
		results, err := review.RunTests(c.Store, repo, task, settings)
		if err != nil {
			return err
		}
		payload["test_results"] = results
	}
	collected := review.Collect(c.Store, repo, task)
	for k, v := range collected {
		payload[k] = v
	}
	text := fmt.Sprint(payload["handoff_md"])
	if stat, ok := payload["diff_stat"].(string); ok && stat != "" {
		text += "\n\n## Diff stat (vs baseline)\n" + stat
	}
	return okErr(c, text, payload)
}

func planForIntegrate(st *store.Store, repo *tasks.Repo, planID string) (*tasks.Plan, error) {
	plans, _ := st.ListPlans(repo.ID)
	if planID != "" {
		plan, err := st.GetPlan(planID)
		if err != nil || plan == nil || plan.RepoID != repo.ID {
			return nil, &review.ReviewError{Msg: fmt.Sprintf("plan '%s' not found in repo '%s'", planID, repo.Alias)}
		}
		return plan, nil
	}
	var candidates []*tasks.Plan
	for i := range plans {
		list, _ := st.ListTasks("", plans[i].ID)
		for _, t := range list {
			if t.Status == tasks.StatusApproved || t.Status == tasks.StatusIntegrated {
				candidates = append(candidates, &plans[i])
				break
			}
		}
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) == 0 {
		return nil, &review.ReviewError{Msg: "no plan with approved tasks; approve tasks first (`guy review`, `guy approve`)"}
	}
	names := make([]string, 0, len(candidates))
	for _, pl := range candidates {
		names = append(names, pl.ID)
	}
	return nil, &review.ReviewError{Msg: "multiple candidate plans; pass --plan: " + strings.Join(names, ", ")}
}

func cmdIntegrate(c *Ctx, p parsed) error {
	repo, err := resolveRepo(c.Store, p.flag("repo"))
	if err != nil {
		return err
	}
	scheduler.Reconcile(c.Store)
	plan, err := planForIntegrate(c.Store, repo, p.flag("plan"))
	if err != nil {
		return err
	}
	list, _ := c.Store.ListTasks("", plan.ID)
	if p.bool("approve") {
		approvalID, _ := c.Store.NextID("APR")
		_ = c.Store.AddApproval(tasks.ApprovalRecord{
			ID: approvalID, TargetKind: "plan", TargetID: plan.ID, Kind: "integration",
			Note: "explicit integration approval", CreatedAt: tasks.UTCNow()})
		result, err := review.PerformIntegration(c.Store, repo, plan, list, p.bool("skip-tests"), p.bool("allow-out-of-scope"))
		if err != nil {
			return err
		}
		status := fmt.Sprint(result["status"])
		text := ""
		if status == "conflict" {
			text = fmt.Sprintf("integration CONFLICT — plan %s\n  %s", plan.ID, result["message"])
		} else {
			text = fmt.Sprintf("integration complete — plan %s\n  integration branch: %s\n  integrated tasks: %s\n  %s",
				plan.ID, result["integration_branch"], strings.Join(toStringSlice(result["integrated"]), ", "), result["note"])
		}
		_ = c.ok(text, result)
		if status == "conflict" {
			return fmt.Errorf("integration stopped on conflict")
		}
		return nil
	}
	preview := review.IntegrationPreview(c.Store, repo, plan, list, p.bool("skip-tests"), p.bool("allow-out-of-scope"))
	text := "INTEGRATION DRY RUN — nothing will be done. Pass --approve to execute.\n" +
		fmt.Sprintf("plan %s baseline %s@%.10s\n", plan.ID, preview["baseline"].(map[string]any)["branch"], preview["baseline"].(map[string]any)["commit"]) +
		fmt.Sprintf("integration branch: %s\n", preview["integration_branch"]) +
		"planned actions:\n  " + strings.Join(toStringSlice(preview["actions"]), "\n  ")
	if conflicts, ok := preview["predicted_conflicts"].([]map[string]any); ok && len(conflicts) > 0 {
		text += "\nPREDICTED CONFLICTS:\n"
		for _, cf := range conflicts {
			text += fmt.Sprintf("  %v: %v\n", cf["task"], cf["detail"])
		}
	}
	if failures, ok := preview["gate_failures"].([]string); ok && len(failures) > 0 {
		text += "\nGATE FAILURES (integration would refuse):\n  - " + strings.Join(failures, "\n  - ")
	} else {
		text += "\nall gates pass — ready for `guy integrate --approve`"
	}
	return okErr(c, text, preview)
}

// doCancel implements cancellation (shared with tests).
func doCancel(st *store.Store, taskID string) (*tasks.Task, error) {
	task, err := st.GetTask(taskID)
	if err != nil || task == nil {
		return nil, fmt.Errorf("unknown task '%s'", taskID)
	}
	if task.Status == tasks.StatusRunning {
		runs, _ := st.RunsForTask(task.ID)
		var active []*tasks.RunRecord
		for _, r := range runs {
			if r.Status == "running" || r.Status == "starting" {
				active = append(active, r)
			}
		}
		for _, run := range active {
			run.Status = "cancel-requested"
			_ = st.UpdateRun(run)
		}
		// Give the owning process's poll loop (~300ms cadence) a chance to
		// perform the cooperative cancel itself, so the outcome is classified
		// "cancelled" rather than a forced-kill "nonzero" failure. Force-kill
		// only if the owner does not react.
		softDeadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(softDeadline) {
			fresh, _ := st.GetTask(task.ID)
			if fresh != nil && fresh.Status != tasks.StatusRunning {
				_ = st.AppendEvent("task_cancelled", task.RepoID, task.ID, nil)
				return fresh, nil
			}
			time.Sleep(150 * time.Millisecond)
		}
		for _, run := range active {
			if run.Pid != 0 {
				process.KillTree(run.Pid)
			}
		}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			fresh, _ := st.GetTask(task.ID)
			if fresh != nil && fresh.Status != tasks.StatusRunning {
				_ = st.AppendEvent("task_cancelled", task.RepoID, task.ID, nil)
				return fresh, nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		_ = task.ApplyTransition(tasks.StatusCancelled, "cancelled by user (runner unresponsive)")
		task.ExitReason = "cancelled"
		for _, run := range active {
			run.Status = "cancelled"
			run.FailureClass = "cancelled"
			_ = st.UpdateRun(run)
		}
	} else {
		if err := task.ApplyTransition(tasks.StatusCancelled, "cancelled by user"); err != nil {
			return nil, err
		}
	}
	if task.FinishedAt == "" {
		task.FinishedAt = tasks.UTCNow()
	}
	_ = st.UpdateTask(task)
	_ = st.AppendEvent("task_cancelled", task.RepoID, task.ID, nil)
	fresh, _ := st.GetTask(task.ID)
	return fresh, nil
}

func cmdCancel(c *Ctx, p parsed) error {
	id := p.arg(0)
	if id == "" {
		return usage("usage: guy cancel <TASK-ID>")
	}
	task, err := doCancel(c.Store, id)
	if err != nil {
		return err
	}
	return okErr(c, fmt.Sprintf("%s: cancelled (final status: %s)", task.ID, task.Status),
		map[string]any{"task": task.ID, "status": task.Status})
}

func cleanTask(st *store.Store, repo *tasks.Repo, task *tasks.Task, deleteBranch bool) ([]string, error) {
	var done []string
	root := repo.Path
	if task.Worktree != "" && dirExists(task.Worktree) {
		if gitops.IsDirty(task.Worktree) {
			entries, _ := gitops.StatusPorcelain(task.Worktree)
			var names []string
			for i, e := range entries {
				if i < 5 {
					names = append(names, e[1])
				}
			}
			return nil, &review.ReviewError{Msg: fmt.Sprintf(
				"%s: worktree has uncommitted changes (%s); the-guy refuses to delete them — inspect %s manually or integrate/approve first",
				task.ID, strings.Join(names, ", "), task.Worktree)}
		}
		if err := gitops.WorktreeRemove(root, task.Worktree, false); err != nil {
			return nil, err
		}
		done = append(done, "removed worktree "+task.Worktree)
	}
	if deleteBranch && task.Branch != "" {
		ancestorOfIntegration := false
		if plan, _ := st.GetPlan(task.PlanID); plan != nil {
			ib, _ := review.IntegrationRef(plan, repo)
			res, _ := gitops.Git(root, false, "merge-base", "--is-ancestor", task.Branch, ib)
			ancestorOfIntegration = res.Code == 0
		}
		if err := gitops.DeleteBranch(root, task.Branch, ancestorOfIntegration); err != nil {
			done = append(done, fmt.Sprintf("branch %s NOT deleted: %v", task.Branch, err))
		} else {
			note := " (was fully merged)"
			if ancestorOfIntegration {
				note = " (commits preserved in the integration branch)"
			}
			done = append(done, "deleted branch "+task.Branch+note)
		}
	}
	_ = st.AppendEvent("task_cleaned", repo.ID, task.ID, map[string]any{
		"removed": done, "branch_deleted": deleteBranch})
	return done, nil
}

func cmdClean(c *Ctx, p parsed) error {
	taskID := p.flag("task")
	planID := p.flag("plan")
	if taskID == "" && planID == "" {
		return usage("pass --task <ID> or --plan <ID>")
	}
	var actions []string
	if taskID != "" {
		task, err := c.Store.GetTask(taskID)
		if err != nil || task == nil {
			return fmt.Errorf("unknown task '%s'", taskID)
		}
		switch task.Status {
		case tasks.StatusFailed, tasks.StatusCancelled, tasks.StatusIntegrated:
		default:
			return &review.ReviewError{Msg: fmt.Sprintf(
				"%s is '%s'; only failed/cancelled/integrated tasks can be cleaned", task.ID, task.Status)}
		}
		repo, _ := c.Store.GetRepo(task.RepoID)
		if repo == nil {
			return fmt.Errorf("repo %s not registered", task.RepoID)
		}
		done, err := cleanTask(c.Store, repo, task, p.bool("branch"))
		if err != nil {
			return err
		}
		actions = append(actions, done...)
	} else {
		plan, err := c.Store.GetPlan(planID)
		if err != nil || plan == nil {
			return fmt.Errorf("unknown plan '%s'", planID)
		}
		repo, _ := c.Store.GetRepo(plan.RepoID)
		if repo == nil {
			return fmt.Errorf("repo %s not registered", plan.RepoID)
		}
		list, _ := c.Store.ListTasks("", plan.ID)
		for _, task := range list {
			switch task.Status {
			case tasks.StatusFailed, tasks.StatusCancelled, tasks.StatusIntegrated:
				done, err := cleanTask(c.Store, repo, task, p.bool("branch"))
				if err != nil {
					return err
				}
				actions = append(actions, done...)
			}
		}
		branch, wt := review.IntegrationRef(plan, repo)
		if dirExists(wt) {
			if gitops.IsDirty(wt) {
				return &review.ReviewError{Msg: "integration worktree " + wt + " is dirty; refusing to remove it"}
			}
			if err := gitops.WorktreeRemove(repo.Path, wt, false); err != nil {
				return err
			}
			actions = append(actions, "removed integration worktree "+wt+" (branch "+branch+" kept)")
		}
	}
	text := "nothing to clean"
	if len(actions) > 0 {
		text = "cleaned:\n  " + strings.Join(actions, "\n  ")
	}
	return okErr(c, text, map[string]any{"actions": actions})
}

func cmdConfig(c *Ctx, p parsed) error {
	sub := p.arg(0)
	switch sub {
	case "set":
		key, value := p.arg(1), p.arg(2)
		if key == "" || value == "" {
			return usage("usage: guy config set <key> <value>")
		}
		var typed any = value
		if value == "true" || value == "false" {
			typed = value == "true"
		} else if n, err := strconv.Atoi(value); err == nil {
			typed = n
		}
		if err := config.SetUserSetting(key, typed); err != nil {
			return err
		}
		return okErr(c, fmt.Sprintf("set %s = %v", key, typed), map[string]any{"key": key, "value": typed})
	case "get":
		key := p.arg(1)
		if key == "" {
			return usage("usage: guy config get <key>")
		}
		settings := config.UserSettings()
		if _, ok := settings[key]; !ok {
			return fmt.Errorf("unknown setting %q", key)
		}
		fmt.Fprintln(c.Out, settings[key])
		return nil
	case "", "list":
		settings := config.UserSettings()
		var lines []string
		lines = append(lines, "settings (precedence: CLI > repo .guy/project.* > user prefs > defaults):")
		for _, key := range sortedAnyKeys(settings) {
			lines = append(lines, fmt.Sprintf("  %s = %v", key, settings[key]))
		}
		return okErr(c, strings.Join(lines, "\n"), map[string]any{"settings": settings})
	}
	return usage("unknown config subcommand %q", sub)
}

func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cmdEvents(c *Ctx, p parsed) error {
	limit := 30
	if p.flag("limit") != "" {
		if n, err := strconv.Atoi(p.flag("limit")); err == nil {
			limit = n
		}
	}
	repoID := ""
	if ref := p.flag("repo"); ref != "" {
		if repo, _ := c.Store.GetRepo(ref); repo != nil {
			repoID = repo.ID
		}
	}
	events, err := c.Store.RecentEvents(limit, "", repoID)
	if err != nil {
		return err
	}
	var lines []string
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		payload, _ := json.Marshal(e.Payload)
		lines = append(lines, fmt.Sprintf("%s  %-32s %-10s %s", e.TS, e.Kind, e.TaskID, string(payload)))
	}
	text := "(no events)"
	if len(lines) > 0 {
		text = strings.Join(lines, "\n")
	}
	return okErr(c, text, map[string]any{"events": events})
}

// okErr lets handlers emit success output and return nil.
func okErr(c *Ctx, text string, payload map[string]any) error {
	c.ok(text, payload)
	return nil
}
