// Package review builds handoffs, test evidence, and gated integration.
// `approve` is never synonymous with commit/merge/push. Integration targets a
// separate integration branch; the default branch is never modified. Commits
// of agent changes happen only inside `guy integrate --approve` and are
// recorded in the audit trail.
package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dwarkeshkaswala/the-guy/internal/config"
	"github.com/dwarkeshkaswala/the-guy/internal/gitops"
	"github.com/dwarkeshkaswala/the-guy/internal/process"
	"github.com/dwarkeshkaswala/the-guy/internal/store"
	"github.com/dwarkeshkaswala/the-guy/internal/tasks"
)

type ReviewError struct{ Msg string }

func (e *ReviewError) Error() string { return e.Msg }

func IntegrationRef(plan *tasks.Plan, repo *tasks.Repo) (branch, worktree string) {
	branch = fmt.Sprintf("guy/plan-%s-integration", plan.ID)
	worktree = filepath.Join(config.WorktreesRoot(), repo.ID, fmt.Sprintf("plan-%s", plan.ID))
	return branch, worktree
}

func BuildHandoff(st *store.Store, repo *tasks.Repo, task *tasks.Task) map[string]any {
	runs, _ := st.RunsForTask(task.ID)
	concerns := append([]string{}, task.Concerns...)
	concerns = append(concerns, "A successful agent exit code is not proof that the code is correct.")
	if !task.ReadOnly && task.Worktree != "" && gitops.IsDirty(task.Worktree) {
		concerns = append(concerns,
			"Worktree contains uncommitted changes; `guy integrate --approve` will commit them on the task branch before merging.")
	}
	if len(task.RequiredTests) > 0 && len(task.TestResults) == 0 {
		concerns = append(concerns, "Required tests are defined but have not been run (`guy review --run-tests <task>`).")
	}
	criteria := append([]string{}, task.AcceptanceCriteria...)
	criteriaLines := []string{"  (none defined — consider defining some)"}
	if len(criteria) > 0 {
		criteriaLines = criteriaLines[:0]
		for _, c := range criteria {
			criteriaLines = append(criteriaLines, "  - "+c)
		}
	}
	needsHuman := append([]string{
		"Review the diff and changed files.",
		"Verify each acceptance criterion:",
	}, criteriaLines...)
	needsHuman = append(needsHuman, "Decide: `guy approve <task>` or `guy reject <task> --reason ...`.")
	lastRun := map[string]any(nil)
	if len(runs) > 0 {
		lastRun = map[string]any{"id": runs[len(runs)-1].ID, "log_path": runs[len(runs)-1].LogPath}
	}
	exitCode := any(nil)
	if task.ExitCode != nil {
		exitCode = *task.ExitCode
	}
	return map[string]any{
		"task_id": task.ID, "title": task.Title,
		"repo":   map[string]any{"alias": repo.Alias, "path": repo.Path},
		"branch": task.Branch, "worktree": task.Worktree,
		"baseline":      map[string]any{"branch": task.BaselineBranch, "commit": task.BaselineCommit},
		"changed_files": task.ChangedFiles, "commits": task.Commits,
		"agent": task.Agent, "exit_code": exitCode, "exit_reason": task.ExitReason,
		"required_tests": task.RequiredTests, "test_results": task.TestResults,
		"acceptance_criteria": task.AcceptanceCriteria,
		"concerns":            concerns, "dependencies": task.Dependencies,
		"needs_human_judgment": needsHuman,
		"last_run":             lastRun,
		"generated_at":         tasks.UTCNow(),
	}
}

func RenderHandoffMD(h map[string]any) string {
	repo := map[string]string{}
	if m, ok := h["repo"].(map[string]any); ok {
		if v, ok := m["alias"].(string); ok {
			repo["alias"] = v
		}
		if v, ok := m["path"].(string); ok {
			repo["path"] = v
		}
	}
	baseline := map[string]string{}
	if m, ok := h["baseline"].(map[string]any); ok {
		if v, ok := m["branch"].(string); ok {
			baseline["branch"] = v
		}
		if v, ok := m["commit"].(string); ok {
			baseline["commit"] = v
		}
	}
	lines := []string{
		fmt.Sprintf("# Handoff: %v — %v", h["task_id"], h["title"]),
		"",
		fmt.Sprintf("- Repo: %s (%s)", repo["alias"], repo["path"]),
		fmt.Sprintf("- Branch: %v", orDash(h["branch"])),
		fmt.Sprintf("- Worktree: %v", orDash(h["worktree"])),
		fmt.Sprintf("- Baseline: %s @ %s", baseline["branch"], short(baseline["commit"], 10)),
		fmt.Sprintf("- Agent: %v (exit %v; %v)", h["agent"], h["exit_code"], h["exit_reason"]),
		"",
		"## Changed files",
	}
	if files, ok := h["changed_files"].([]string); ok && len(files) > 0 {
		for _, f := range files {
			lines = append(lines, "- "+f)
		}
	} else {
		lines = append(lines, "- (none)")
	}
	if commits, ok := h["commits"].([]string); ok && len(commits) > 0 {
		lines = append(lines, "", "## Commits on task branch")
		for _, c := range commits {
			lines = append(lines, "- "+c)
		}
	}
	lines = append(lines, "", "## Tests")
	if results, ok := h["test_results"].([]tasks.TestResult); ok && len(results) > 0 {
		for _, r := range results {
			status := "FAIL"
			if r.OK {
				status = "PASS"
			}
			exit := ""
			if r.ExitCode != nil {
				exit = fmt.Sprintf(" (exit %d)", *r.ExitCode)
			}
			lines = append(lines, fmt.Sprintf("- [%s] %s%s", status, r.Command, exit))
		}
	} else {
		lines = append(lines, "- No test results recorded yet.")
	}
	lines = append(lines, "", "## Concerns")
	if concerns, ok := h["concerns"].([]string); ok {
		for _, c := range concerns {
			lines = append(lines, "- "+c)
		}
	}
	lines = append(lines, "", "## Needs human judgment")
	if needs, ok := h["needs_human_judgment"].([]string); ok {
		for _, n := range needs {
			lines = append(lines, "- "+n)
		}
	}
	return strings.Join(lines, "\n")
}

func orDash(v any) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return "(read-only task, no branch)"
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Collect gathers review payloads for a task and stores its handoff.
func Collect(st *store.Store, repo *tasks.Repo, task *tasks.Task) map[string]any {
	payload := map[string]any{"task": task.ID, "status": task.Status, "title": task.Title}
	if task.Worktree != "" {
		if dirExists(task.Worktree) {
			var changed []string
			entries, _ := gitops.StatusPorcelain(task.Worktree)
			for _, e := range entries {
				changed = append(changed, e[1])
			}
			if diffed, err := gitops.DiffNameOnly(task.Worktree, task.BaselineCommit); err == nil {
				changed = append(changed, diffed...)
			}
			payload["changed_files"] = dedupe(changed)
			payload["dirty_in_worktree"] = dirtyList(entries)
			payload["commits"], _ = gitops.CommitsBetween(task.Worktree, task.BaselineCommit, "HEAD")
			if stat, err := gitops.DiffStat(task.Worktree, task.BaselineCommit); err == nil {
				payload["diff_stat"] = stat
			} else {
				payload["diff_stat"] = "(diff unavailable: " + err.Error() + ")"
			}
		} else {
			payload["changed_files"] = task.ChangedFiles
			payload["commits"] = task.Commits
			payload["diff_stat"] = "(worktree not present; showing recorded data)"
		}
	} else {
		payload["changed_files"] = task.ChangedFiles
		payload["commits"] = task.Commits
		payload["diff_stat"] = "(read-only task; no worktree)"
	}
	runs, _ := st.RunsForTask(task.ID)
	runList := []map[string]any{}
	for _, r := range runs {
		exit := any(nil)
		if r.ExitCode != nil {
			exit = *r.ExitCode
		}
		runList = append(runList, map[string]any{
			"id": r.ID, "exit_code": exit, "failure_class": r.FailureClass,
			"log_path": r.LogPath, "duration_ms": r.DurationMS})
	}
	payload["runs"] = runList
	handoff := BuildHandoff(st, repo, task)
	task.Handoff = handoff
	_ = st.UpdateTask(task)
	payload["handoff"] = handoff
	payload["handoff_md"] = RenderHandoffMD(handoff)
	return payload
}

// RunTests executes the task's user-defined test commands in its worktree.
// Commands come from the plan file — never guessed — and only run behind the
// explicit --run-tests flag. They execute code.
func RunTests(st *store.Store, repo *tasks.Repo, task *tasks.Task, settings map[string]any) ([]tasks.TestResult, error) {
	if len(task.RequiredTests) == 0 {
		return nil, &ReviewError{Msg: task.ID + " defines no required_tests"}
	}
	cwd := repo.Path
	if task.Worktree != "" && dirExists(task.Worktree) {
		cwd = task.Worktree
	}
	var results []tasks.TestResult
	for idx, cmd := range task.RequiredTests {
		if len(cmd) == 0 {
			results = append(results, tasks.TestResult{Command: "[]", OK: false,
				Error: "invalid test command (must be a list of strings)"})
			continue
		}
		command, _ := json.Marshal(cmd)
		logPath := filepath.Join(config.LogsRoot(), repo.ID, task.ID, fmt.Sprintf("test-%d.log", idx+1))
		outcome := process.Execute(cmd, cwd, os.Environ(),
			config.SettingsInt(settings, "run_timeout_seconds", 1800), logPath,
			int64(config.SettingsInt(settings, "log_max_bytes", 1_000_000)), nil, nil)
		exit := any(nil)
		exitInt := outcome.ExitCode
		if outcome.ExitCodeSet {
			exit = exitInt
		}
		_ = exit
		results = append(results, tasks.TestResult{
			Command:  string(command),
			OK:       outcome.ExitCode == 0 && outcome.FailureClass == "",
			ExitCode: &outcome.ExitCode, FailureClass: outcome.FailureClass,
			DurationMS: int(outcome.DurationMS), LogPath: logPath,
		})
	}
	task.TestResults = results
	_ = st.UpdateTask(task)
	_ = st.AppendEvent("tests_run", repo.ID, task.ID, map[string]any{
		"results": results})
	return results, nil
}

// scopeGate re-checks, against the worktree as it is now, which work files
// fall outside the task's allowed_paths (so reverting them clears the gate).
func scopeGate(task *tasks.Task) []string {
	if task.ReadOnly || task.Worktree == "" || len(task.AllowedPaths) == 0 {
		return nil
	}
	changed, err := gitops.ChangedSince(task.Worktree, task.BaselineCommit)
	if err != nil {
		return nil
	}
	return tasks.OutOfScope(tasks.WorkFiles(task.Agent, changed), task.AllowedPaths)
}

func testsGate(task *tasks.Task) (bool, []string) {
	if len(task.RequiredTests) == 0 {
		return true, nil
	}
	passed := map[string]bool{}
	for _, r := range task.TestResults {
		if r.OK {
			passed[r.Command] = true
		}
	}
	var missing []string
	for _, cmd := range task.RequiredTests {
		b, _ := json.Marshal(cmd)
		if !passed[string(b)] {
			missing = append(missing, string(b))
		}
	}
	return len(missing) == 0, missing
}

func topoOrder(list []*tasks.Task) ([]*tasks.Task, error) {
	byID := map[string]*tasks.Task{}
	for _, t := range list {
		byID[t.ID] = t
	}
	state := map[string]int{}
	var order []*tasks.Task
	var visit func(t *tasks.Task) error
	visit = func(t *tasks.Task) error {
		switch state[t.ID] {
		case 2:
			return nil
		case 1:
			return &ReviewError{Msg: "dependency cycle at " + t.ID}
		}
		state[t.ID] = 1
		for _, dep := range t.Dependencies {
			if d, ok := byID[dep]; ok {
				if err := visit(d); err != nil {
					return err
				}
			}
		}
		state[t.ID] = 2
		order = append(order, t)
		return nil
	}
	for _, t := range list {
		if err := visit(t); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// IntegrationPreview describes exactly what --approve would do.
func IntegrationPreview(st *store.Store, repo *tasks.Repo, plan *tasks.Plan, list []*tasks.Task, skipTests, allowOutOfScope bool) map[string]any {
	var writeTasks []*tasks.Task
	for _, t := range list {
		if !t.ReadOnly {
			writeTasks = append(writeTasks, t)
		}
	}
	integrationBranch, integrationWT := IntegrationRef(plan, repo)
	var actions, gateFailures, pending []string
	var conflicts []map[string]any
	if !gitops.BranchExists(repo.Path, integrationBranch) {
		actions = append(actions, fmt.Sprintf(
			"create branch %s from %s @ %s and worktree %s",
			integrationBranch, plan.BaselineBranch, short(plan.BaselineCommit, 10), integrationWT))
	} else {
		actions = append(actions, fmt.Sprintf("reuse existing integration branch %s and worktree %s",
			integrationBranch, integrationWT))
	}
	ordered, _ := topoOrder(writeTasks)
	perTask := []map[string]any{}
	approvedWrite := 0
	for _, task := range ordered {
		entry := map[string]any{"task": task.ID, "status": task.Status,
			"actions": []string{}, "conflict": nil}
		if task.Status == tasks.StatusIntegrated {
			perTask = append(perTask, entry)
			continue
		}
		if task.Status != tasks.StatusApproved {
			pending = append(pending, fmt.Sprintf("%s (%s)", task.ID, task.Status))
			entry["actions"] = []string{fmt.Sprintf("pending: task is '%s'; not integrated in this step", task.Status)}
			perTask = append(perTask, entry)
			continue
		}
		approvedWrite++
		if task.Worktree == "" || task.Branch == "" {
			gateFailures = append(gateFailures, task.ID+" has no worktree/branch — run it first")
			perTask = append(perTask, entry)
			continue
		}
		var entryActions []string
		if gitops.IsDirty(task.Worktree) {
			entryActions = append(entryActions, fmt.Sprintf(
				"commit uncommitted changes on %s (commit message: \"guy: commit task %s — %s\")",
				task.Branch, task.ID, task.Title))
			actions = append(actions, fmt.Sprintf("commit %s changes on %s", task.ID, task.Branch))
		}
		ours := integrationBranch
		if !gitops.BranchExists(repo.Path, integrationBranch) {
			ours = plan.BaselineCommit
		}
		clean, err := gitops.MergeTreeClean(repo.Path, ours, task.Branch)
		if err == nil && clean != nil {
			if !*clean {
				entry["conflict"] = fmt.Sprintf("predicted merge conflict with %s", integrationBranch)
				conflicts = append(conflicts, map[string]any{"task": task.ID, "detail": entry["conflict"]})
			}
		} else if clean == nil {
			entryActions = append(entryActions, "conflict pre-check unavailable (git < 2.38); conflict checked at merge time")
		}
		entryActions = append(entryActions, fmt.Sprintf("merge --no-ff %s into %s", task.Branch, integrationBranch))
		actions = append(actions, fmt.Sprintf("merge %s into %s", task.Branch, integrationBranch))
		entry["actions"] = entryActions
		perTask = append(perTask, entry)
		if outside := scopeGate(task); len(outside) > 0 {
			entry["out_of_scope"] = outside
			if !allowOutOfScope {
				gateFailures = append(gateFailures, fmt.Sprintf(
					"%s changed files outside its allowed_paths %v: %s (revert them in %s, or pass --allow-out-of-scope after reviewing)",
					task.ID, task.AllowedPaths, strings.Join(outside, ", "), task.Worktree))
			}
		}
		if ok, missing := testsGate(task); !ok && !skipTests {
			gateFailures = append(gateFailures, fmt.Sprintf(
				"%s required tests not passing yet: %s (run `guy review --run-tests` or pass --skip-tests explicitly)",
				task.ID, strings.Join(missing, ", ")))
		}
	}
	if approvedWrite == 0 {
		readOnlyApproved := false
		for _, t := range list {
			if t.ReadOnly && t.Status == tasks.StatusApproved {
				readOnlyApproved = true
			}
		}
		if !readOnlyApproved {
			gateFailures = append(gateFailures, "no approved tasks to integrate (approve tasks with `guy approve <TASK>`)")
		}
	}
	actions = append(actions, "NOT performed: any push, any change to the default branch, any deployment")
	return map[string]any{
		"plan":                 plan.ID,
		"baseline":             map[string]any{"branch": plan.BaselineBranch, "commit": plan.BaselineCommit},
		"integration_branch":   integrationBranch,
		"integration_worktree": integrationWT,
		"actions":              actions, "per_task": perTask, "pending": pending,
		"predicted_conflicts": conflicts, "gate_failures": gateFailures,
		"skip_tests": skipTests, "allow_out_of_scope": allowOutOfScope, "ready": approvedWrite > 0 && len(gateFailures) == 0,
	}
}

// PerformIntegration executes the approved integration step. Commits happen
// only here, with approval, on task branches; merges are --no-ff into the
// integration branch. Never pushes; never touches the default branch.
func PerformIntegration(st *store.Store, repo *tasks.Repo, plan *tasks.Plan, list []*tasks.Task, skipTests, allowOutOfScope bool) (map[string]any, error) {
	preview := IntegrationPreview(st, repo, plan, list, skipTests, allowOutOfScope)
	if failures, ok := preview["gate_failures"].([]string); ok && len(failures) > 0 {
		return nil, &ReviewError{Msg: "integration gates failed:\n  - " + strings.Join(failures, "\n  - ")}
	}
	root := repo.Path
	integrationBranch, integrationWT := IntegrationRef(plan, repo)
	if !dirExists(integrationWT) {
		if gitops.BranchExists(root, integrationBranch) {
			if err := gitops.WorktreeAddExisting(root, integrationWT, integrationBranch); err != nil {
				return nil, err
			}
		} else {
			if err := gitops.WorktreeAdd(root, integrationWT, integrationBranch, plan.BaselineCommit); err != nil {
				return nil, err
			}
			_ = st.AppendEvent("integration_branch_created", repo.ID, "", map[string]any{
				"branch": integrationBranch, "baseline": plan.BaselineCommit})
		}
	}
	ordered, err := topoOrder(filterWrite(list))
	if err != nil {
		return nil, err
	}
	var integrated []string
	conflict := func(task *tasks.Task, files []string, predicted bool) map[string]any {
		plan.Status = "conflict"
		_ = st.UpdatePlan(*plan)
		_ = st.AppendEvent("integration_conflict", repo.ID, task.ID, map[string]any{
			"integration_branch": integrationBranch, "task_branch": task.Branch, "files": files})
		msg := ""
		if predicted {
			msg = fmt.Sprintf("Predicted conflict merging %s into %s. Nothing was merged for this task; "+
				"earlier tasks (if any) are already integrated. Resolve manually in %s (git merge --abort if needed), "+
				"or fix the task and re-run.", task.Branch, integrationBranch, integrationWT)
		} else {
			msg = fmt.Sprintf("Conflict merging %s into %s. Conflicted files: %s. The merge was aborted; "+
				"resolve manually in %s and re-run `guy integrate --approve`. The default branch was not touched.",
				task.Branch, integrationBranch, strings.Join(files, ", "), integrationWT)
		}
		return map[string]any{"status": "conflict", "task": task.ID,
			"conflict_files": files, "integration_branch": integrationBranch,
			"message": msg, "integrated": integrated}
	}

	for _, task := range ordered {
		if task.Status == tasks.StatusIntegrated {
			integrated = append(integrated, task.ID)
			continue
		}
		if task.Status != tasks.StatusApproved {
			continue // pending task: not part of this integration step
		}
		if task.Worktree == "" || !dirExists(task.Worktree) {
			return nil, &ReviewError{Msg: fmt.Sprintf(
				"%s has no worktree; run it first (`guy run --approve --task %s`)", task.ID, task.ID)}
		}
		if gitops.IsDirty(task.Worktree) {
			message := fmt.Sprintf("guy: commit task %s — %s", task.ID, task.Title)
			sha, err := gitops.CommitAll(task.Worktree, message)
			if err != nil {
				return nil, err
			}
			task.Commits = append(task.Commits, fmt.Sprintf("%s %s", short(sha, 10), message))
			_ = st.UpdateTask(task)
			_ = st.AppendEvent("task_changes_committed", repo.ID, task.ID, map[string]any{
				"commit": sha, "note": "committed by explicit `guy integrate --approve`"})
		}
		commits, _ := gitops.CommitsBetween(task.Worktree, task.BaselineCommit, "HEAD")
		if len(commits) == 0 {
			task.Concerns = append(task.Concerns, "integration: no changes were produced")
			_ = task.ApplyTransition(tasks.StatusIntegrated, "")
			_ = st.UpdateTask(task)
			_ = st.AppendEvent("task_integrated", repo.ID, task.ID, map[string]any{"note": "no changes to merge"})
			integrated = append(integrated, task.ID)
			continue
		}
		clean, err := gitops.MergeTreeClean(root, integrationBranch, task.Branch)
		if err == nil && clean != nil && !*clean {
			return conflict(task, nil, true), nil
		}
		if err := gitops.MergeBranch(integrationWT, task.Branch,
			fmt.Sprintf("guy: integrate %s (%s)", task.ID, task.Title)); err != nil {
			files := gitops.ConflictFiles(integrationWT)
			gitops.MergeAbort(integrationWT)
			return conflict(task, files, false), nil
		}
		_ = task.ApplyTransition(tasks.StatusIntegrated, "")
		_ = st.UpdateTask(task)
		_ = st.AppendEvent("task_integrated", repo.ID, task.ID, map[string]any{
			"branch": task.Branch, "into": integrationBranch})
		integrated = append(integrated, task.ID)
	}
	var pending []string
	for _, task := range ordered {
		if task.Status != tasks.StatusIntegrated {
			pending = append(pending, fmt.Sprintf("%s (%s)", task.ID, task.Status))
		}
	}
	for _, task := range list {
		if task.ReadOnly && task.Status == tasks.StatusApproved {
			_ = task.ApplyTransition(tasks.StatusIntegrated, "")
			_ = st.UpdateTask(task)
			_ = st.AppendEvent("task_integrated", repo.ID, task.ID, map[string]any{"note": "read-only task"})
		}
	}
	allWrite := filterWrite(list)
	if len(allWrite) > 0 {
		allIntegrated := true
		for _, t := range allWrite {
			if t.Status != tasks.StatusIntegrated {
				allIntegrated = false
			}
		}
		if allIntegrated {
			plan.Status = "integrated"
			_ = st.UpdatePlan(*plan)
			_ = st.AppendEvent("plan_integrated", repo.ID, "", map[string]any{
				"plan": plan.ID, "branch": integrationBranch})
		} else if plan.Status != "conflict" {
			plan.Status = "active"
			_ = st.UpdatePlan(*plan)
		}
	}
	return map[string]any{
		"status": "ok", "integration_branch": integrationBranch,
		"integration_worktree": integrationWT, "integrated": integrated,
		"pending": pending,
		"note":    "Nothing was pushed and the default branch was not modified.",
	}, nil
}

func filterWrite(list []*tasks.Task) []*tasks.Task {
	var out []*tasks.Task
	for _, t := range list {
		if !t.ReadOnly {
			out = append(out, t)
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func dirtyList(entries [][2]string) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e[1])
	}
	return out
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
