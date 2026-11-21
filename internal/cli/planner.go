package cli

// `guy plan "goal" --planner <agent>`: ask an installed agent CLI to break a
// goal into a task DAG, then validate it like any hand-written plan file.
//
// Safety model (differs deliberately from the Python prototype, which ran the
// planner in the user's working copy and parsed the first "{" of the log —
// which is the prompt echoed in the log header):
//
//   - The planner runs in a throwaway *detached* worktree at the baseline
//     commit, never in the user's working copy. Anything it writes there is
//     reported and discarded with the worktree.
//   - The user's working copy (HEAD + porcelain status) is compared before
//     and after; any change is a hard error.
//   - Only the agent's output (after the log header) is parsed, and the LAST
//     JSON object with a non-empty "tasks" list wins.
//   - The planner proposes work, not policy: agent choices and per-task
//     options it emits are stripped; agent assignment follows the normal
//     rules (--agent > default_agent > first verified CLI).
//   - The result is a DRAFT plan like any other; nothing runs without
//     `guy approve` + `guy run --approve`.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dwarkeshkaswala/the-guy/internal/agents"
	"github.com/dwarkeshkaswala/the-guy/internal/config"
	"github.com/dwarkeshkaswala/the-guy/internal/gitops"
	"github.com/dwarkeshkaswala/the-guy/internal/process"
	"github.com/dwarkeshkaswala/the-guy/internal/scheduler"
	"github.com/dwarkeshkaswala/the-guy/internal/store"
	"github.com/dwarkeshkaswala/the-guy/internal/tasks"
)

const plannerInstructions = `You are the planning step of a multi-agent software engineering orchestrator.
Break the GOAL below into a small set of tasks that separate coding agents can
implement independently, each in its own git worktree.

You are running in a disposable, read-only snapshot of the repository. Read
whatever files you need. Do NOT modify, create or delete files, and do NOT run
commands that change state (installs, commits, network calls). Anything you
write is discarded.

GOAL:
%s

Repository facts (from read-only discovery):
%s

Reply with exactly one JSON object and nothing after it. Schema:
  prompt: string — the goal
  tasks: list (1..%d) of objects with
    key: short unique id such as "t1"
    title: short title
    objective: self-contained instructions for the coding agent, including
               what "done" means; the agent sees only this task
    dependencies: list of keys this task needs merged first ([] if none)
    read_only: true only for analysis/review tasks that change no files
    allowed_paths: repo-relative files, directories or globs this task may
                   change; keep them NON-OVERLAPPING between tasks that do
                   not depend on each other, so they can run in parallel
    acceptance_criteria: list of concrete, checkable statements
    required_tests: list of argv lists, e.g. [["go","test","./..."]]; only
                    deterministic local commands that exist in this repo

Rules: prefer fewer, larger tasks over many tiny ones; no dependency cycles;
do not choose which agent runs a task — the orchestrator decides that.`

// plannerResult is what a planner run produced, before persistence.
type plannerResult struct {
	Spec     map[string]any
	LogPath  string
	Warnings []string
	Planner  string
}

// runPlanner executes the planner adapter in a scratch worktree and returns a
// sanitized plan spec plus human-readable warnings. It never persists a plan.
func runPlanner(st *store.Store, repo *tasks.Repo, plannerName, goal, baseBranch string, settings map[string]any) (*plannerResult, error) {
	if strings.TrimSpace(goal) == "" {
		return nil, usage("--planner needs a goal: guy plan \"<goal>\" --planner <agent>")
	}
	adapter, err := agents.Get(plannerName)
	if err != nil {
		return nil, err
	}
	if _, err := adapter.VerifyForExecution(); err != nil {
		return nil, fmt.Errorf("planner '%s' is not usable: %w", plannerName, err)
	}

	base := baseBranch
	if base == "" {
		base = repo.DefaultBranch
	}
	commit, err := gitops.RevParse(repo.Path, base)
	if err != nil {
		return nil, fmt.Errorf("baseline branch '%s' not found: %w", base, err)
	}

	beforeHead, _ := gitops.HeadCommit(repo.Path)
	beforeStatus := statusFingerprint(repo.Path)

	stamp := time.Now().UTC().Format("20060102T150405.000000000")
	stamp = strings.ReplaceAll(stamp, ".", "")
	scratch := filepath.Join(config.WorktreesRoot(), repo.ID, "_planner-"+stamp)
	if err := os.MkdirAll(filepath.Dir(scratch), 0o755); err != nil {
		return nil, err
	}
	if _, err := gitops.Git(repo.Path, true, "worktree", "add", "--detach", scratch, commit); err != nil {
		return nil, fmt.Errorf("creating planner scratch worktree: %w", err)
	}
	defer func() {
		_ = gitops.WorktreeRemove(repo.Path, scratch, true)
		_ = os.RemoveAll(scratch)
		_, _ = gitops.Git(repo.Path, false, "worktree", "prune")
	}()

	facts := plannerFacts(st, repo)
	maxTasks := config.SettingsInt(settings, "planner_max_tasks", 8)
	prompt := fmt.Sprintf(plannerInstructions, goal, facts, maxTasks)

	options := map[string]any{}
	if plannerName == "mock" {
		// Test/demo hook: the mock agent's scripted behaviour for planning.
		if raw := os.Getenv("GUY_MOCK_PLANNER_SPEC"); raw != "" {
			_ = json.Unmarshal([]byte(raw), &options)
		}
	}
	argv, err := adapter.BuildCommand(prompt, scratch, options)
	if err != nil {
		return nil, err
	}
	env := adapter.ChildEnv(os.Environ(), options)
	logPath := filepath.Join(config.LogsRoot(), repo.ID, "planner",
		fmt.Sprintf("planner-%s-%s.log", plannerName, stamp))
	timeout := config.SettingsInt(settings, "planner_timeout_seconds", 300)
	_ = st.AppendEvent("planner_started", repo.ID, "", map[string]any{
		"planner": plannerName, "baseline": fmt.Sprintf("%s@%.10s", base, commit), "log": logPath})

	outcome := process.Execute(argv, scratch, env, timeout, logPath,
		int64(config.SettingsInt(settings, "log_max_bytes", 1_000_000)), nil, nil)

	var warnings []string

	// Hard check: the user's working copy must be exactly as it was.
	afterHead, _ := gitops.HeadCommit(repo.Path)
	if afterHead != beforeHead || statusFingerprint(repo.Path) != beforeStatus {
		_ = st.AppendEvent("planner_touched_working_copy", repo.ID, "", map[string]any{
			"planner": plannerName, "log": logPath})
		return nil, fmt.Errorf("your working copy changed while the planner ran (HEAD or `git status` differs) — "+
			"no plan was created; inspect %s and the planner log %s", repo.Path, logPath)
	}
	// Soft check: anything written in the scratch worktree is discarded.
	if changed := changedPaths(scratch); len(changed) > 0 {
		warnings = append(warnings, fmt.Sprintf(
			"planner wrote %d file(s) in its scratch worktree; discarded: %s",
			len(changed), summarizeList(changed, 5)))
	}

	if outcome.FailureClass != "" || outcome.ExitCode != 0 {
		reason := outcome.FailureClass
		if reason == "" || reason == "nonzero" {
			reason = fmt.Sprintf("exit %d", outcome.ExitCode)
		}
		if outcome.SpawnError != "" {
			reason += ": " + outcome.SpawnError
		}
		return nil, fmt.Errorf("planner '%s' failed (%s); log: %s", plannerName, reason, logPath)
	}

	raw, err := os.ReadFile(logPath)
	if err != nil {
		return nil, fmt.Errorf("reading planner log: %w", err)
	}
	spec := extractPlanJSON(agentOutput(string(raw)))
	if spec == nil {
		return nil, fmt.Errorf("planner output contained no JSON object with a non-empty \"tasks\" list; log: %s", logPath)
	}

	sanitized, notes, err := sanitizePlannerSpec(spec, goal, maxTasks)
	if err != nil {
		return nil, fmt.Errorf("%w; log: %s", err, logPath)
	}
	warnings = append(warnings, notes...)
	sanitized["base_branch"] = base
	return &plannerResult{Spec: sanitized, LogPath: logPath, Warnings: warnings, Planner: plannerName}, nil
}

// plannerFacts is the read-only discovery subset given to the planner.
func plannerFacts(st *store.Store, repo *tasks.Repo) string {
	info := inspectRepo(st, repo)
	subset := map[string]any{}
	for _, k := range []string{"default_branch", "current_branch", "project_files",
		"test_command_hints", "agent_config_files", "head_commit"} {
		if v, ok := info[k]; ok && v != nil {
			subset[k] = v
		}
	}
	data, err := json.MarshalIndent(subset, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}

func statusFingerprint(root string) string {
	res, err := gitops.Git(root, false, "status", "--porcelain=v1", "--untracked-files=all")
	if err != nil {
		return "error:" + err.Error()
	}
	return res.Stdout
}

func changedPaths(root string) []string {
	entries, err := gitops.StatusPorcelain(root)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		out = append(out, e[1])
	}
	sort.Strings(out)
	return out
}

func summarizeList(items []string, max int) string {
	if len(items) <= max {
		return strings.Join(items, ", ")
	}
	return strings.Join(items[:max], ", ") + fmt.Sprintf(", … (+%d more)", len(items)-max)
}

// agentOutput strips the-guy's log header (which echoes the command line,
// including the planner prompt and its schema) and trailer lines.
func agentOutput(log string) string {
	if i := strings.Index(log, "\n\n"); i >= 0 && strings.HasPrefix(log, "=== the-guy run ===") {
		log = log[i+2:]
	}
	var kept []string
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "[the-guy] ") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// extractPlanJSON returns the LAST top-level JSON object in text that parses
// and has a non-empty "tasks" list. Agents often think aloud, show drafts, or
// wrap the answer in code fences; the final object is the answer.
func extractPlanJSON(text string) map[string]any {
	var found map[string]any
	for start := 0; start < len(text); start++ {
		if text[start] != '{' {
			continue
		}
		end := matchBrace(text, start)
		if end < 0 {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(text[start:end+1]), &obj); err == nil {
			if list, ok := obj["tasks"].([]any); ok && len(list) > 0 {
				found = obj
				start = end // skip nested objects of an accepted plan
			}
		}
	}
	return found
}

// matchBrace returns the index of the '}' closing the '{' at start, honoring
// JSON strings and escapes, or -1.
func matchBrace(text string, start int) int {
	depth, inString, escaped := 0, false, false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// sanitizePlannerSpec removes fields the planner must not control and
// returns review notes. Structural validation (keys, DAG, tests format)
// happens afterwards in createPlanFromSpec, exactly as for plan files.
func sanitizePlannerSpec(spec map[string]any, goal string, maxTasks int) (map[string]any, []string, error) {
	rawTasks, _ := spec["tasks"].([]any)
	if maxTasks > 0 && len(rawTasks) > maxTasks {
		return nil, nil, fmt.Errorf("planner proposed %d tasks (limit planner_max_tasks=%d); "+
			"narrow the goal or raise the limit with `guy config set planner_max_tasks N`", len(rawTasks), maxTasks)
	}
	var notes []string
	droppedAgents, droppedOptions := 0, 0
	clean := make([]any, 0, len(rawTasks))
	type writeTask struct {
		key   string
		paths []string
		deps  []string
	}
	var writers []writeTask
	for _, raw := range rawTasks {
		task, ok := raw.(map[string]any)
		if !ok {
			clean = append(clean, raw) // createPlanFromSpec reports the error
			continue
		}
		copyTask := map[string]any{}
		for k, v := range task {
			copyTask[k] = v
		}
		if _, ok := copyTask["agent"]; ok {
			delete(copyTask, "agent")
			droppedAgents++
		}
		if _, ok := copyTask["options"]; ok {
			delete(copyTask, "options")
			droppedOptions++
		}
		clean = append(clean, copyTask)

		key, _ := copyTask["key"].(string)
		if ro, _ := copyTask["read_only"].(bool); !ro {
			var paths, deps []string
			for _, p := range listField(copyTask, "allowed_paths") {
				if s, ok := p.(string); ok {
					paths = append(paths, s)
				}
			}
			for _, d := range listField(copyTask, "dependencies") {
				if s, ok := d.(string); ok {
					deps = append(deps, s)
				}
			}
			if len(paths) == 0 {
				notes = append(notes, fmt.Sprintf(
					"task '%s' has no allowed_paths, so it may change anything and will never run in parallel with another writing task", key))
			}
			writers = append(writers, writeTask{key: key, paths: paths, deps: deps})
		}
	}
	if droppedAgents > 0 {
		notes = append(notes, fmt.Sprintf(
			"ignored the planner's agent choice on %d task(s); agents are assigned by the-guy's rules (--agent, default_agent, first verified CLI)", droppedAgents))
	}
	if droppedOptions > 0 {
		notes = append(notes, fmt.Sprintf("ignored planner-supplied 'options' on %d task(s)", droppedOptions))
	}

	// Overlapping write scopes between tasks that could otherwise run
	// together: not an error (the scheduler serializes them), but the plan
	// loses parallelism and may conflict at integration.
	dependsOn := func(a, b writeTask) bool {
		for _, d := range a.deps {
			if d == b.key {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(writers); i++ {
		for j := i + 1; j < len(writers); j++ {
			a, b := writers[i], writers[j]
			if len(a.paths) == 0 || len(b.paths) == 0 || dependsOn(a, b) || dependsOn(b, a) {
				continue
			}
			if msg := scheduler.PathsOverlap(a.paths, b.paths); msg != "" {
				notes = append(notes, fmt.Sprintf(
					"tasks '%s' and '%s' may change the same files (%s); they will run one at a time", a.key, b.key, msg))
			}
		}
	}

	// base_branch is deliberately not taken from the planner: the plan is
	// pinned to the baseline the planner actually analyzed (set by the caller).
	return map[string]any{"prompt": goal, "tasks": clean}, notes, nil
}
