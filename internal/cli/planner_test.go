package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- unit tests (no binary) ----

func TestAgentOutputStripsHeaderEchoingThePrompt(t *testing.T) {
	log := "=== the-guy run ===\ntime: x\ncwd: /w\ncommand: claude -p \"... {\\\"tasks\\\": [{\\\"key\\\": \\\"t1\\\"}]} ...\"\ntimeout: 300s\n\n" +
		"thinking...\n{\"tasks\": [{\"key\": \"real\", \"title\": \"Real\"}]}\n\n[the-guy] agent exited 0\n[the-guy] duration: 5 ms\n"
	spec := extractPlanJSON(agentOutput(log))
	if spec == nil {
		t.Fatal("expected a plan")
	}
	first := spec["tasks"].([]any)[0].(map[string]any)
	if first["key"] != "real" {
		t.Fatalf("parsed the echoed prompt instead of the agent's answer: %v", first)
	}
}

func TestExtractPlanJSONTakesLastPlanAndHandlesFencesAndStrings(t *testing.T) {
	text := "Draft:\n{\"tasks\": [{\"key\": \"draft\"}]}\n" +
		"Note {not json} and {\"other\": 1}\n" +
		"```json\n{\"prompt\": \"has } brace and \\\" quote\", \"tasks\": [{\"key\": \"final\", \"title\": \"x {y}\"}]}\n```\n"
	spec := extractPlanJSON(text)
	if spec == nil {
		t.Fatal("expected a plan")
	}
	if key := spec["tasks"].([]any)[0].(map[string]any)["key"]; key != "final" {
		t.Fatalf("want last plan 'final', got %v", key)
	}
	if extractPlanJSON("no json here {\"tasks\": []}") != nil {
		t.Fatal("empty tasks list must not count as a plan")
	}
}

func TestSanitizePlannerSpec(t *testing.T) {
	spec := map[string]any{
		"prompt":      "planner rewrote the goal",
		"base_branch": "some-other-branch",
		"tasks": []any{
			map[string]any{"key": "a", "title": "A", "agent": "codex", "options": map[string]any{"x": 1},
				"allowed_paths": []any{"src/api/"}},
			map[string]any{"key": "b", "title": "B", "allowed_paths": []any{"src/api/handlers.go"}},
			map[string]any{"key": "c", "title": "C", "dependencies": []any{"a"}, "allowed_paths": []any{"src/api/x.go"}},
			map[string]any{"key": "d", "title": "D"},
			map[string]any{"key": "e", "title": "E", "read_only": true},
		},
	}
	out, notes, err := sanitizePlannerSpec(spec, "the user's goal", 8)
	if err != nil {
		t.Fatal(err)
	}
	if out["prompt"] != "the user's goal" {
		t.Fatalf("goal must come from the user, got %v", out["prompt"])
	}
	if _, ok := out["base_branch"]; ok {
		t.Fatal("planner must not choose the base branch")
	}
	first := out["tasks"].([]any)[0].(map[string]any)
	if _, ok := first["agent"]; ok {
		t.Fatal("planner agent choice must be stripped")
	}
	if _, ok := first["options"]; ok {
		t.Fatal("planner options must be stripped")
	}
	if _, ok := spec["tasks"].([]any)[0].(map[string]any)["agent"]; !ok {
		t.Fatal("sanitize must not mutate the input spec")
	}
	joined := strings.Join(notes, "\n")
	for _, want := range []string{
		"ignored the planner's agent choice on 1 task",
		"tasks 'a' and 'b' may change the same files",
		"task 'd' has no allowed_paths",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing note %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "'a' and 'c'") {
		t.Error("a->c are ordered by a dependency; overlap is expected, not a warning")
	}
	if strings.Contains(joined, "'e'") {
		t.Error("read-only tasks never conflict")
	}
	if _, _, err := sanitizePlannerSpec(spec, "g", 3); err == nil || !strings.Contains(err.Error(), "planner_max_tasks=3") {
		t.Fatalf("expected task-limit error, got %v", err)
	}
}

// ---- black-box tests (real binary, mock agent as planner) ----

func plannerEnv(t *testing.T, behaviour map[string]any) {
	t.Helper()
	data, _ := json.Marshal(behaviour)
	t.Setenv("GUY_MOCK_PLANNER_SPEC", string(data))
}

func plannedOutput(t *testing.T, plan map[string]any) string {
	t.Helper()
	data, _ := json.MarshalIndent(plan, "", "  ")
	return "I looked at the repo. A first idea:\n{\"tasks\": [{\"key\": \"draft\", \"title\": \"Draft\"}]}\n" +
		"Final answer:\n```json\n" + string(data) + "\n```"
}

func plannerRepo(t *testing.T, e *guyEnv) string {
	t.Helper()
	repo := makeRepo(t, filepath.Join(e.root, "planned"), "trunk",
		map[string]string{"go.mod": "module example.com/p\n", "api/api.go": "package api\n"}, true)
	e.mustRun("repo", "add", repo, "--alias", "planned")
	return repo
}

func TestPlannerCreatesValidatedDraftPlan(t *testing.T) {
	e := newEnv(t)
	repo := plannerRepo(t, e)
	head := headOf(t, repo)
	statusBefore := gitOut(t, repo, "status", "--porcelain", "--untracked-files=all")

	plannerEnv(t, map[string]any{"stdout": plannedOutput(t, map[string]any{
		"prompt": "ignored",
		"tasks": []any{
			map[string]any{"key": "api", "title": "Add search endpoint", "agent": "codex",
				"objective": "Implement GET /search in api/.", "allowed_paths": []any{"api/"},
				"acceptance_criteria": []any{"GET /search returns 200"},
				"required_tests":      []any{[]any{"go", "test", "./api/..."}}},
			map[string]any{"key": "ui", "title": "Add search box", "dependencies": []any{"api"},
				"allowed_paths": []any{"web/"}},
			map[string]any{"key": "docs", "title": "Document search", "allowed_paths": []any{"api/README.md"}},
		},
	})})

	// --agent pins the task agent (default discovery can find real CLIs like
	// droid outside PATH, e.g. in ~\bin on the dev machine).
	out, code := e.runJSON("plan", "Add search", "--planner", "mock", "--agent", "mock", "--repo", "planned")
	if code != 0 {
		t.Fatalf("plan failed: %v", out)
	}
	created := out["tasks"].([]any)
	if len(created) != 3 {
		t.Fatalf("want 3 tasks (the final plan, not the draft), got %d", len(created))
	}
	byTitle := map[string]map[string]any{}
	for _, raw := range created {
		task := raw.(map[string]any)
		byTitle[task["title"].(string)] = task
		if task["status"] != "draft" {
			t.Errorf("planner output must be a draft, got %v", task["status"])
		}
		if task["agent"] != "mock" {
			t.Errorf("agent must follow the-guy's rules (mock here), got %v", task["agent"])
		}
	}
	api, ui := byTitle["Add search endpoint"], byTitle["Add search box"]
	if deps := ui["dependencies"].([]any); len(deps) != 1 || deps[0] != api["id"] {
		t.Errorf("dependency not mapped to task id: %v", ui["dependencies"])
	}
	plan := out["plan"].(map[string]any)
	if plan["prompt"] != "Add search" || plan["baseline_branch"] != "trunk" || plan["baseline_commit"] != head {
		t.Errorf("plan not pinned to the user's goal/baseline: %v", plan)
	}
	info := out["planner"].(map[string]any)
	notes, _ := json.Marshal(info["notes"])
	for _, want := range []string{"agent choice", "may change the same files", "discarded"} {
		if !strings.Contains(string(notes), want) {
			t.Errorf("missing planner note %q: %s", want, notes)
		}
	}
	logPath := info["log"].(string)
	if data, err := os.ReadFile(logPath); err != nil || !strings.Contains(string(data), "Add search") {
		t.Errorf("planner log missing or lacks the goal: %v", err)
	}
	if prompt, err := os.ReadFile(logPath); err == nil && !strings.Contains(string(prompt), "Do NOT modify") {
		t.Error("planner prompt should state the read-only contract")
	}

	// The user's repo is untouched and the scratch worktree is gone.
	if headOf(t, repo) != head {
		t.Error("HEAD moved")
	}
	if after := gitOut(t, repo, "status", "--porcelain", "--untracked-files=all"); after != statusBefore {
		t.Errorf("working copy changed:\nbefore:\n%s\nafter:\n%s", statusBefore, after)
	}
	if wt := gitOut(t, repo, "worktree", "list"); strings.Contains(wt, "_planner-") {
		t.Errorf("scratch worktree left behind:\n%s", wt)
	}
	if _, err := os.Stat(filepath.Join(repo, "mock-prompt-received.txt")); err == nil {
		t.Error("planner ran in the user's working copy")
	}

	// Text mode shows paths, tests and review guidance.
	plannerEnv(t, map[string]any{"stdout": plannedOutput(t, map[string]any{
		"tasks": []any{map[string]any{"key": "x", "title": "X", "allowed_paths": []any{"api/"},
			"required_tests": []any{[]any{"go", "test", "./..."}}}}})})
	text := e.mustRun("plan", "Another goal", "--planner", "mock", "--repo", "planned")
	for _, want := range []string{"created DRAFT plan", "planner mock", "paths: api/", "test:  go test ./...", "planner log:", "--run-tests"} {
		if !strings.Contains(text, want) {
			t.Errorf("text output missing %q:\n%s", want, text)
		}
	}
}

func TestPlannerFailuresCreateNoPlan(t *testing.T) {
	e := newEnv(t)
	plannerRepo(t, e)
	valid := plannedOutput(t, map[string]any{"tasks": []any{map[string]any{"key": "a", "title": "A"}}})
	cases := []struct {
		name      string
		behaviour map[string]any
		args      []string
		want      string
		code      int
	}{
		{"nonzero exit", map[string]any{"stdout": valid, "exit": 3}, nil, "failed (exit 3)", 1},
		{"no json", map[string]any{"stdout": "Sorry, I cannot help with that."}, nil, "no JSON object", 1},
		{"invalid DAG", map[string]any{"stdout": plannedOutput(t, map[string]any{"tasks": []any{
			map[string]any{"key": "a", "title": "A", "dependencies": []any{"b"}},
			map[string]any{"key": "b", "title": "B", "dependencies": []any{"a"}}}})}, nil, "failed validation", 1},
		{"too many tasks", map[string]any{"stdout": plannedOutput(t, map[string]any{"tasks": []any{
			map[string]any{"key": "a", "title": "A"}, map[string]any{"key": "b", "title": "B"},
			map[string]any{"key": "c", "title": "C"}}})}, nil, "planner_max_tasks=2", 1},
		{"missing goal", map[string]any{"stdout": valid}, []string{"plan", "--planner", "mock", "--repo", "planned"}, "needs a goal", 2},
		{"unknown planner", map[string]any{"stdout": valid}, []string{"plan", "x", "--planner", "nope", "--repo", "planned"}, "nope", 1},
	}
	e.mustRun("config", "set", "planner_max_tasks", "2")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plannerEnv(t, tc.behaviour)
			args := tc.args
			if args == nil {
				args = []string{"plan", "Goal", "--planner", "mock", "--repo", "planned"}
			}
			out, code := e.run(args...)
			if code != tc.code || !strings.Contains(out, tc.want) {
				t.Fatalf("want exit %d containing %q, got %d:\n%s", tc.code, tc.want, code, out)
			}
		})
	}
	tasksOut := e.mustRun("tasks", "--repo", "planned")
	if strings.Contains(tasksOut, "TASK-") {
		t.Fatalf("failed planner runs must not create tasks:\n%s", tasksOut)
	}
}

func TestPlannerThatTouchesWorkingCopyIsRejected(t *testing.T) {
	e := newEnv(t)
	repo := plannerRepo(t, e)
	escaped := filepath.Join(repo, "planner-escaped.txt")
	plannerEnv(t, map[string]any{
		"stdout": plannedOutput(t, map[string]any{"tasks": []any{map[string]any{"key": "a", "title": "A"}}}),
		"write":  []any{map[string]any{"path": escaped, "content": "oops"}},
	})
	out, code := e.run("plan", "Goal", "--planner", "mock", "--repo", "planned")
	if code != 1 || !strings.Contains(out, "working copy changed while the planner ran") {
		t.Fatalf("expected hard failure, got %d:\n%s", code, out)
	}
	if _, err := os.Stat(escaped); err != nil {
		t.Fatal("test setup: the escape write should exist (the-guy reports it, never deletes user files)")
	}
	if strings.Contains(e.mustRun("tasks", "--repo", "planned"), "TASK-") {
		t.Fatal("no plan may be created after the working copy changed")
	}
	events := e.mustRun("events", "--limit", "20")
	if !strings.Contains(events, "planner_touched_working_copy") {
		t.Errorf("audit trail missing planner_touched_working_copy:\n%s", events)
	}
}
