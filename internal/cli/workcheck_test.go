package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dwarkeshkaswala/the-guy/internal/tasks"
)

// ---- scope matching (unit) ----

func TestInScopeMatching(t *testing.T) {
	cases := []struct {
		path    string
		allowed []string
		want    bool
	}{
		{"api/x.go", []string{"api/"}, true},
		{"api/x.go", []string{"api"}, true},
		{"api/sub/x.go", []string{"./api"}, true},
		{"apis/x.go", []string{"api"}, false}, // prefix of a name is not a directory
		{"README.md", []string{"README.md"}, true},
		{"docs/a/b.md", []string{"docs/*.md"}, true}, // lenient: '*' crosses '/'
		{"src/a.go", []string{"src/**/*.go"}, false},
		{"src/x/a.go", []string{"src/**/*.go"}, true},
		{"web/a.ts", []string{"api/", "web/"}, true},
		{"a1.txt", []string{"a?.txt"}, true},
		{"a/1.txt", []string{"a?.txt"}, false}, // '?' never matches '/'
		{`api\win.go`, []string{"api/"}, true},
		{"anything", []string{"."}, true},
	}
	for _, c := range cases {
		if got := tasks.InScope(c.path, c.allowed); got != c.want {
			t.Errorf("InScope(%q, %v) = %v, want %v", c.path, c.allowed, got, c.want)
		}
	}
	if out := tasks.OutOfScope([]string{"x", "y"}, nil); out != nil {
		t.Errorf("empty allowed_paths means whole repo; got %v", out)
	}
	if got := tasks.WorkFiles("mock", []string{"mock-prompt-received.txt", "a.txt"}); len(got) != 1 || got[0] != "a.txt" {
		t.Errorf("mock bookkeeping not dropped: %v", got)
	}
	if got := tasks.WorkFiles("claude", []string{"mock-prompt-received.txt"}); len(got) != 1 {
		t.Error("bookkeeping exclusion must be per-agent")
	}
	cs := tasks.ReplaceConcerns([]string{"keep", tasks.ConcernNoChanges + "old"}, tasks.ConcernNoChanges, "new")
	if len(cs) != 2 || cs[0] != "keep" || cs[1] != tasks.ConcernNoChanges+"new" {
		t.Errorf("ReplaceConcerns: %v", cs)
	}
}

// ---- run-end checks + integration gate (black-box) ----

func TestNoChangesRunIsFlagged(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	spec := map[string]any{"tasks": []any{mockTask("a", "Does nothing", map[string]any{
		"options": map[string]any{"mock": map[string]any{"stdout": "All done!"}}})}}
	e.mustRun("plan", "--file", writeSpec(t, filepath.Join(e.root, "p.json"), spec))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--approve")

	task := taskByID(t, "TASK-001")
	if task["status"] != "needs_review" {
		t.Fatalf("status: %v", task["status"])
	}
	if !strings.Contains(task["exit_reason"].(string), "NO files changed") {
		t.Errorf("exit_reason should say nothing changed: %v", task["exit_reason"])
	}
	concerns, _ := json.Marshal(task["concerns"])
	if !strings.Contains(string(concerns), "[no-changes]") || !strings.Contains(string(concerns), "agents smoke mock") {
		t.Errorf("missing no-changes concern: %s", concerns)
	}
	if out := e.mustRun("review", "TASK-001"); !strings.Contains(out, "without changing any file") {
		t.Errorf("review output should surface the concern:\n%s", out)
	}
	if !strings.Contains(e.mustRun("events", "--limit", "50"), "task_no_changes") {
		t.Error("missing task_no_changes event")
	}
}

func TestOutOfScopeChangesBlockIntegrationUntilFixed(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", map[string]string{"api/api.go": "package api\n"}, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	spec := map[string]any{"tasks": []any{mockTask("a", "API work", map[string]any{
		"allowed_paths": []any{"api/"},
		"options": map[string]any{"mock": map[string]any{"write": []any{
			map[string]any{"path": "api/new.go", "content": "package api\n"},
			map[string]any{"path": "web/stray/page.ts", "content": "oops\n"},
		}}}})}}
	e.mustRun("plan", "--file", writeSpec(t, filepath.Join(e.root, "p.json"), spec))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--approve")

	task := taskByID(t, "TASK-001")
	concerns, _ := json.Marshal(task["concerns"])
	if !strings.Contains(string(concerns), "[out-of-scope]") || !strings.Contains(string(concerns), "web/stray/page.ts") {
		t.Fatalf("missing out-of-scope concern naming the file (untracked dir must be expanded): %s", concerns)
	}
	if strings.Contains(string(concerns), "api/new.go") || strings.Contains(string(concerns), "mock-prompt") {
		t.Errorf("in-scope or bookkeeping files must not be flagged: %s", concerns)
	}
	e.mustRun("approve", "TASK-001")

	out, code := e.run("integrate", "--approve", "--plan", "PLAN-001")
	if code != 1 || !strings.Contains(out, "outside its allowed_paths") || !strings.Contains(out, "--allow-out-of-scope") {
		t.Fatalf("scope gate should block integration, got %d:\n%s", code, out)
	}
	if dry := e.mustRun("integrate", "--dry-run", "--plan", "PLAN-001", "--allow-out-of-scope"); !strings.Contains(dry, "all gates pass") {
		t.Fatalf("override should clear the gate in preview:\n%s", dry)
	}

	// Reverting the stray file in the worktree clears the gate (re-checked live).
	wt := task["worktree"].(string)
	if err := os.RemoveAll(filepath.Join(wt, "web")); err != nil {
		t.Fatal(err)
	}
	out = e.mustRun("integrate", "--approve", "--plan", "PLAN-001")
	if !strings.Contains(out, "integration complete") {
		t.Fatalf("integration after revert:\n%s", out)
	}
	if got := gitOut(t, repo, "ls-tree", "-r", "--name-only", "guy/plan-PLAN-001-integration"); strings.Contains(got, "web/") || !strings.Contains(got, "api/new.go") {
		t.Fatalf("integration branch content:\n%s", got)
	}
}

// ---- agents smoke (black-box, mock agent) ----

func TestAgentsSmoke(t *testing.T) {
	e := newEnv(t)

	// Dry run without --approve: nothing executes.
	out := e.mustRun("agents", "smoke", "mock")
	if !strings.Contains(out, "DRY RUN") || !strings.Contains(out, "REAL model calls") {
		t.Fatalf("dry run:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(e.root, "data", "smoke")); err == nil {
		t.Fatal("dry run must not create smoke repos")
	}

	cases := []struct {
		name, spec, verdict, detail string
		code                        int
	}{
		{"ok", `{"write": [{"path": "guy-smoke.txt", "content": "{{token}}\n"}]}`, "ok", "expected content", 0},
		{"no edit", `{"stdout": "Done! I created the file."}`, "no_edit", "did not create", 1},
		{"wrong content", `{"write": [{"path": "guy-smoke.txt", "content": "hello"}]}`, "wrong_content", "unexpected content", 1},
		{"failed", `{"stdout": "401 Unauthorized: invalid api key", "exit": 2}`, "failed", "exited 2", 1},
		{"extra files", `{"write": [{"path": "guy-smoke.txt", "content": "{{token}}"}, {"path": "notes.md", "content": "x"}]}`, "ok", "expected content", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("GUY_MOCK_SMOKE_SPEC", c.spec)
			payload, code := e.runJSON("agents", "smoke", "mock", "--approve")
			if code != c.code {
				t.Fatalf("exit %d, want %d: %v", code, c.code, payload)
			}
			r := payload["results"].([]any)[0].(map[string]any)
			if r["verdict"] != c.verdict || !strings.Contains(r["detail"].(string), c.detail) {
				t.Fatalf("result: %v", r)
			}
			if c.verdict == "no_edit" && !strings.Contains(r["hint"].(string), "permission") {
				t.Errorf("no_edit should hint at permissions: %v", r["hint"])
			}
			if c.name == "extra files" {
				if extra, _ := json.Marshal(r["extra_files"]); !strings.Contains(string(extra), "notes.md") || strings.Contains(string(extra), "mock-prompt") {
					t.Errorf("extra_files: %s", extra)
				}
			}
			kept, _ := r["kept_repo"].(string)
			if (c.verdict == "ok") == (kept != "") {
				t.Errorf("repo should be kept only on failure (kept=%q)", kept)
			}
		})
	}

	// `guy agents` reports the latest verdict for real adapters only; the
	// event trail has every run.
	if events := e.mustRun("events", "--limit", "20"); strings.Count(events, "agent_smoke") != len(cases) {
		t.Errorf("want %d agent_smoke events:\n%s", len(cases), events)
	}
	if out, code := e.run("agents", "smoke", "nope", "--approve"); code == 0 || !strings.Contains(out, "nope") {
		t.Errorf("unknown agent should fail: %d %s", code, out)
	}
	// With no verified real CLI discoverable, a bare smoke has nothing to
	// test. Redirect HOME/USERPROFILE too, or adapter discovery would find
	// real CLIs outside PATH on dev machines (e.g. droid in ~\bin) and the
	// test would make a REAL model call.
	home := filepath.Join(e.root, "isolated-home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if out, code := e.run("agents", "smoke", "--approve"); code != 1 || !strings.Contains(out, "no verified agent CLI") {
		t.Errorf("bare smoke without real agents: %d %s", code, out)
	}
}
