package cli

// Black-box tests: build the real guy binary once, then drive it as a
// subprocess against disposable git repositories with the built-in mock
// agent. State is isolated via GUY_DATA_DIR/GUY_PREFS_DIR; APPDATA/
// LOCALAPPDATA are redirected so the machine's real agent CLIs cannot leak
// into adapter results.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dwkaswala/the-guy/internal/config"
	"github.com/dwkaswala/the-guy/internal/process"
	"github.com/dwkaswala/the-guy/internal/store"
	"github.com/dwkaswala/the-guy/internal/tasks"
)

var guyBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "guy-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	bin := filepath.Join(dir, "guy")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.Command("go", "build", "-o", bin, "github.com/dwkaswala/the-guy/cmd/guy")
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building test binary: %v\n%s\n", err, out)
		os.Exit(1)
	}
	guyBin = bin
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// guyEnv is one isolated environment (per test).
type guyEnv struct {
	t    *testing.T
	root string
}

func newEnv(t *testing.T) *guyEnv {
	t.Helper()
	root := t.TempDir()
	e := &guyEnv{t: t, root: root}
	t.Setenv("GUY_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("GUY_PREFS_DIR", filepath.Join(root, "prefs"))
	t.Setenv("GUY_MOCK_AGENT_BIN", guyBin)
	t.Setenv("GUY_VSCODE_EXTENSIONS_DIR", filepath.Join(root, "extensions"))
	_ = os.MkdirAll(filepath.Join(root, "extensions"), 0o755)
	// Redirect machine-level discovery inputs for determinism: only the mock
	// adapter can be "ready" in these tests.
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	_ = os.MkdirAll(filepath.Join(root, "appdata"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "localappdata"), 0o755)
	keep := []string{}
	if git, err := exec.LookPath("git"); err == nil {
		keep = append(keep, filepath.Dir(git))
	}
	if runtime.GOOS == "windows" {
		sysRoot := os.Getenv("SystemRoot")
		if sysRoot == "" {
			sysRoot = `C:\Windows`
		}
		keep = append(keep, filepath.Join(sysRoot, "System32"))
	}
	empty := filepath.Join(root, "path-empty")
	_ = os.MkdirAll(empty, 0o755)
	t.Setenv("PATH", strings.Join(append([]string{empty}, keep...), string(os.PathListSeparator)))
	return e
}

// run executes the built guy binary; returns stdout and exit code.
func (e *guyEnv) run(args ...string) (string, int) {
	e.t.Helper()
	cmd := exec.Command(guyBin, args...)
	cmd.Env = os.Environ()
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stdout
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		e.t.Fatalf("running guy %v: %v", args, err)
	}
	return stdout.String(), code
}

func (e *guyEnv) runJSON(args ...string) (map[string]any, int) {
	e.t.Helper()
	out, code := e.run(append(args, "--json")...)
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		e.t.Fatalf("invalid JSON from %v:\n%s\n%v", args, out, err)
	}
	return payload, code
}

func (e *guyEnv) mustRun(args ...string) string {
	e.t.Helper()
	out, code := e.run(args...)
	if code != 0 {
		e.t.Fatalf("guy %v failed (%d):\n%s", args, code, out)
	}
	return out
}

// makeRepo creates a disposable git repository.
func makeRepo(t *testing.T, path, branch string, files map[string]string, dirty bool) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", path}, args...)...)
		var buf strings.Builder
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, buf.String())
		}
	}
	git("init", "-b", branch)
	git("config", "user.email", "test@the-guy.local")
	git("config", "user.name", "the-guy tests")
	_ = os.WriteFile(filepath.Join(path, "README.md"), []byte("# test repo\n"), 0o644)
	for name, content := range files {
		full := filepath.Join(path, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		_ = os.WriteFile(full, []byte(content), 0o644)
	}
	git("add", "-A")
	git("commit", "-m", "init")
	if dirty {
		_ = os.WriteFile(filepath.Join(path, "user-dirty.txt"), []byte("user's uncommitted work\n"), 0o644)
	}
	return path
}

func headOf(t *testing.T, repo string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func gitOut(t *testing.T, repo string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// mockTask builds a plan task spec using the built-in mock agent.
func mockTask(key, title string, extra map[string]any) map[string]any {
	task := map[string]any{
		"key": key, "title": title, "objective": "Objective for " + title,
		"agent": "mock", "timeout_seconds": 60,
	}
	for k, v := range extra {
		task[k] = v
	}
	return task
}

func writeSpec(t *testing.T, path string, spec map[string]any) string {
	t.Helper()
	data, _ := json.MarshalIndent(spec, "", "  ")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(config.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func taskByID(t *testing.T, id string) map[string]any {
	t.Helper()
	st := openStore(t)
	task, err := st.GetTask(id)
	if err != nil || task == nil {
		t.Fatalf("task %s not found", id)
	}
	data, _ := json.Marshal(task)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	return out
}

// ---- CLI basics ----

func TestHelpVersionUnknown(t *testing.T) {
	e := newEnv(t)
	if out, code := e.run("--help"); code != 0 || !strings.Contains(out, "the-guy") {
		t.Fatalf("help: %d %q", code, out)
	}
	if out, code := e.run("--version"); code != 0 || !strings.Contains(out, "go") {
		t.Fatalf("version: %d %q", code, out)
	}
	if _, code := e.run("frobnicate"); code != 2 {
		t.Fatalf("unknown command must exit 2, got %d", code)
	}
	if out, code := e.run("init"); code != 0 || !strings.Contains(out, "initialized") {
		t.Fatalf("init: %d %q", code, out)
	}
}

func TestDoctorAndAgents(t *testing.T) {
	e := newEnv(t)
	out, code := e.run("doctor")
	if code != 0 || !strings.Contains(out, "agent:mock") {
		t.Fatalf("doctor: %d\n%s", code, out)
	}
	payload, _ := e.runJSON("agents")
	adapters, _ := payload["adapters"].([]any)
	if len(adapters) != 5 {
		t.Fatalf("expected 5 adapters, got %d", len(adapters))
	}
	out, _ = e.run("agents", "doctor", "claude")
	if !strings.Contains(out, "DISABLED") && !strings.Contains(out, "npm") {
		t.Fatalf("agents doctor claude:\n%s", out)
	}
	out, code = e.run("agents", "doctor", "nope")
	if code != 1 {
		t.Fatalf("unknown adapter in doctor must fail, got %d", code)
	}
}

// ---- registry ----

func TestRepoRegistryLifecycle(t *testing.T) {
	e := newEnv(t)
	pyRepo := makeRepo(t, filepath.Join(e.root, "py project"), "trunk",
		map[string]string{"pyproject.toml": "[project]\nname='x'\n"}, false)
	nodeRepo := makeRepo(t, filepath.Join(e.root, "node-repo"), "main",
		map[string]string{"package.json": `{"name":"x","scripts":{"test":"node test.js"}}`}, false)

	e.mustRun("repo", "add", pyRepo, "--alias", "py")
	e.mustRun("repo", "add", nodeRepo, "--alias", "node")
	_ = os.MkdirAll(filepath.Join(pyRepo, "sub"), 0o755)
	if _, code := e.run("repo", "add", filepath.Join(pyRepo, "sub"), "--alias", "py2"); code != 1 {
		t.Fatal("duplicate registration must fail")
	}
	if _, code := e.run("repo", "add", nodeRepo, "--alias", "py"); code != 1 {
		t.Fatal("alias conflict must fail")
	}
	out := e.mustRun("repo", "list")
	if !strings.Contains(out, "py") || !strings.Contains(out, "node") {
		t.Fatalf("list missing repos:\n%s", out)
	}
	e.mustRun("use", "py")
	out = e.mustRun("repo", "list")
	if !strings.Contains(out, "(default)") {
		t.Fatalf("default marker missing:\n%s", out)
	}
	if out := e.mustRun("use"); !strings.Contains(out, "default repo: py") {
		t.Fatalf("use show: %s", out)
	}
	info, _ := e.runJSON("inspect")
	if info["default_branch"] != "trunk" {
		t.Fatalf("default branch must be trunk, got %v", info["default_branch"])
	}
	if info["dirty"] != false {
		t.Fatal("repo should be clean")
	}
}

func TestDirtyDetectionAndPreservation(t *testing.T) {
	e := newEnv(t)
	weird := makeRepo(t, filepath.Join(e.root, "weird stuff"), "develop",
		map[string]string{"notes/ideas.txt": "hello"}, true)
	e.mustRun("repo", "add", weird, "--alias", "weird")
	info, _ := e.runJSON("inspect", "--repo", "weird")
	if info["dirty"] != true {
		t.Fatal("dirty not detected")
	}
	spec := map[string]any{"prompt": "p", "tasks": []any{mockTask("a", "Work", map[string]any{
		"options": map[string]any{"mock": map[string]any{
			"write": []any{map[string]any{"path": "out.txt", "content": "x"}}}}})}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--repo", "weird", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--repo", "weird", "--approve")
	data, err := os.ReadFile(filepath.Join(weird, "user-dirty.txt"))
	if err != nil || string(data) != "user's uncommitted work\n" {
		t.Fatal("user's uncommitted work was not preserved")
	}
}

func TestNoStateLeakageBetweenRepos(t *testing.T) {
	e := newEnv(t)
	repoA := makeRepo(t, filepath.Join(e.root, "a"), "main", nil, false)
	repoB := makeRepo(t, filepath.Join(e.root, "b"), "main", nil, false)
	e.mustRun("repo", "add", repoA, "--alias", "a")
	e.mustRun("repo", "add", repoB, "--alias", "b")
	e.mustRun("use", "a")
	spec := map[string]any{"prompt": "p", "tasks": []any{mockTask("t", "Only in A", nil)}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))

	payload, _ := e.runJSON("tasks", "--repo", "b")
	if tasks, _ := payload["tasks"].([]any); len(tasks) != 0 {
		t.Fatal("state leaked into repo b")
	}
	payload, _ = e.runJSON("tasks")
	if tasks, _ := payload["tasks"].([]any); len(tasks) != 1 {
		t.Fatal("task missing for default repo a")
	}
	e.mustRun("repo", "remove", "a")
	payload, _ = e.runJSON("tasks", "--repo", "b")
	if tasks, _ := payload["tasks"].([]any); len(tasks) != 0 {
		t.Fatal("state leaked after removal")
	}
}

// ---- planning ----

func TestPlanValidation(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")

	if out, code := e.run("plan", "Add a searchable catalog with tests"); code != 0 ||
		!strings.Contains(out, "DRAFT plan PLAN-001") || !strings.Contains(out, "TASK-001") {
		t.Fatalf("plan prompt: %d\n%s", code, out)
	}
	cycle := map[string]any{"tasks": []any{
		mockTask("a", "A", map[string]any{"dependencies": []string{"b"}}),
		mockTask("b", "B", map[string]any{"dependencies": []string{"a"}}),
	}}
	writeSpec(t, filepath.Join(e.root, "cycle.json"), cycle)
	if out, code := e.run("plan", "--file", filepath.Join(e.root, "cycle.json")); code != 1 || !strings.Contains(out, "cycle") {
		t.Fatalf("cycle must be rejected: %d %s", code, out)
	}
	ghost := map[string]any{"tasks": []any{
		mockTask("a", "A", map[string]any{"dependencies": []string{"ghost"}})}}
	writeSpec(t, filepath.Join(e.root, "ghost.json"), ghost)
	if out, code := e.run("plan", "--file", filepath.Join(e.root, "ghost.json")); code != 1 || !strings.Contains(out, "ghost") {
		t.Fatalf("missing dep must be rejected: %d %s", code, out)
	}
	badAgent := map[string]any{"tasks": []any{
		mockTask("a", "A", map[string]any{"agent": "hal9000"})}}
	writeSpec(t, filepath.Join(e.root, "badagent.json"), badAgent)
	if out, code := e.run("plan", "--file", filepath.Join(e.root, "badagent.json")); code != 1 || !strings.Contains(out, "hal9000") {
		t.Fatalf("unknown agent must be rejected: %d %s", code, out)
	}
	if out, code := e.run("approve", "PLAN-001"); code != 0 || !strings.Contains(out, "not any merge, commit, or push") {
		t.Fatalf("approve plan: %d %s", code, out)
	}
	if _, code := e.run("approve", "PLAN-001"); code != 1 {
		t.Fatal("double approve must fail")
	}
}

// ---- execution ----

func TestTwoConcurrentMockAgentsDistinctWorktrees(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "repo with space"), "trunk", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	baseline := headOf(t, repo)
	spec := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("a", "Write A", map[string]any{
			"allowed_paths": []string{"feature-a.txt"},
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "feature-a.txt", "content": "alpha"}}}}}),
		mockTask("b", "Write B", map[string]any{
			"allowed_paths": []string{"feature-b.txt"},
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "feature-b.txt", "content": "beta"}}}}}),
	}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")

	out := e.mustRun("run", "--dry-run")
	if !strings.Contains(out, "DRY RUN") {
		t.Fatalf("dry run marker missing:\n%s", out)
	}
	if wt := taskByID(t, "TASK-001")["worktree"]; wt != nil && wt != "" {
		t.Fatal("dry run must not assign worktrees")
	}
	if strings.Contains(gitOut(t, repo, "worktree", "list"), "TASK-") {
		t.Fatal("dry run must not create worktrees")
	}

	out, code := e.run("run", "--approve", "--max-workers", "2")
	if code != 0 {
		t.Fatalf("run failed:\n%s", out)
	}
	a := taskByID(t, "TASK-001")
	b := taskByID(t, "TASK-002")
	if a["status"] != "needs_review" || b["status"] != "needs_review" {
		t.Fatalf("statuses: %v %v", a["status"], b["status"])
	}
	if a["worktree"] == b["worktree"] || a["branch"] == b["branch"] {
		t.Fatal("worktrees/branches must be distinct")
	}
	if data, err := os.ReadFile(filepath.Join(fmt.Sprint(a["worktree"]), "feature-a.txt")); err != nil || string(data) != "alpha" {
		t.Fatal("feature-a.txt missing in worktree A")
	}
	if data, err := os.ReadFile(filepath.Join(fmt.Sprint(b["worktree"]), "feature-b.txt")); err != nil || string(data) != "beta" {
		t.Fatal("feature-b.txt missing in worktree B")
	}
	// no auto-commit: no new commits on task branches, default branch untouched
	for _, task := range []map[string]any{a, b} {
		if commits := gitOut(t, fmt.Sprint(task["worktree"]), "log", "--oneline",
			fmt.Sprint(task["baseline_commit"])+"..HEAD"); commits != "" {
			t.Fatalf("unexpected commits on task branch: %s", commits)
		}
	}
	if headOf(t, repo) != baseline {
		t.Fatal("default branch must be untouched")
	}
}

func TestDependencyBlocksUntilIntegrated(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	spec := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("a", "First", map[string]any{
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "a.txt", "content": "a"}}}}}),
		mockTask("b", "Second", map[string]any{
			"dependencies": []string{"a"},
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "b.txt", "content": "b"}}}}}),
	}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--approve")
	if s := taskByID(t, "TASK-001")["status"]; s != "needs_review" {
		t.Fatalf("TASK-001: %v", s)
	}
	b := taskByID(t, "TASK-002")
	if b["status"] != "blocked" || !strings.Contains(fmt.Sprint(b["blocked_reason"]), "TASK-001") {
		t.Fatalf("TASK-002 should be blocked: %v", b)
	}
	// integrate A, then B unblocks on the next run
	e.mustRun("approve", "TASK-001")
	e.mustRun("integrate", "--approve", "--plan", "PLAN-001")
	e.mustRun("run", "--approve")
	if s := taskByID(t, "TASK-002")["status"]; s != "needs_review" {
		t.Fatalf("TASK-002 after integrate: %v", s)
	}
}

func TestPathCollisionBlocksConcurrency(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	spec := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("a", "A", map[string]any{"allowed_paths": []string{"shared/**"}}),
		mockTask("b", "B", map[string]any{"allowed_paths": []string{"shared/**"}}),
	}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--approve")
	a, b := taskByID(t, "TASK-001"), taskByID(t, "TASK-002")
	statuses := []string{fmt.Sprint(a["status"]), fmt.Sprint(b["status"])}
	sort.Strings(statuses)
	if statuses[0] != "blocked" || statuses[1] != "needs_review" {
		t.Fatalf("overlapping scopes must not run concurrently: %v", statuses)
	}
	blocked := a
	if fmt.Sprint(a["status"]) != "blocked" {
		blocked = b
	}
	if !strings.Contains(fmt.Sprint(blocked["blocked_reason"]), "path collision") {
		t.Fatalf("collision reason: %v", blocked["blocked_reason"])
	}
}

func TestPromptTransportUnicodeQuotesSpaces(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "repo with ünicode"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	fancy := `Résumé — «l'agent» said "hello" & <bye>; 100% × y(z) — unicode ✓ path C:\Some Path\file.txt`
	spec := map[string]any{"prompt": "p", "tasks": []any{
		map[string]any{"key": "a", "title": "Fancy", "objective": fancy, "agent": "mock",
			"timeout_seconds": 60}}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--approve")
	task := taskByID(t, "TASK-001")
	if task["status"] != "needs_review" {
		t.Fatalf("status: %v", task["status"])
	}
	received, err := os.ReadFile(filepath.Join(fmt.Sprint(task["worktree"]), "mock-prompt-received.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(received), fancy) {
		t.Fatalf("prompt not transported verbatim:\n%s", string(received))
	}
}

func TestFailureTimeoutAndLogs(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	e.mustRun("config", "set", "log_max_bytes", "30000")
	spec := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("fail", "Fails", map[string]any{
			"options": map[string]any{"mock": map[string]any{"exit": 7}}}),
		mockTask("slow", "Times out", map[string]any{
			"timeout_seconds": 1,
			"options":         map[string]any{"mock": map[string]any{"sleep": 30}}}),
		mockTask("leak", "Leaks secrets", map[string]any{
			"options": map[string]any{"mock": map[string]any{
				"leak_secret": true, "stdout": strings.Repeat("x", 40000)}}}),
	}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")
	// run each explicitly: whole-repo scopes would collide if batched.
	// TASK-001 exits 7 -> those run calls exit 1 (expected).
	if _, code := e.run("run", "--approve", "--task", "TASK-001"); code != 1 {
		t.Fatal("failing task run must exit 1")
	}
	if _, code := e.run("run", "--approve", "--task", "TASK-002"); code != 1 {
		t.Fatal("timeout task run must exit 1")
	}
	e.mustRun("run", "--approve", "--task", "TASK-003")
	out, code := e.run("status")
	if code != 0 {
		t.Fatalf("status after failures:\n%s", out)
	}
	fail := taskByID(t, "TASK-001")
	if fail["status"] != "failed" || fmt.Sprint(fail["exit_code"]) != "7" {
		t.Fatalf("failure task: %v", fail)
	}
	slow := taskByID(t, "TASK-002")
	if slow["status"] != "failed" || !strings.Contains(fmt.Sprint(slow["exit_reason"]), "timed out") {
		t.Fatalf("timeout task: %v", slow)
	}
	leak := taskByID(t, "TASK-003")
	if leak["status"] != "needs_review" {
		t.Fatalf("leak task: %v", leak["status"])
	}
	logs := e.mustRun("logs", "TASK-003")
	if strings.Contains(logs, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(logs, "SuperSecret123") {
		t.Fatalf("secrets leaked into logs:\n%s", logs)
	}
	if !strings.Contains(logs, "[REDACTED]") {
		st := openStore(t)
		runs, _ := st.RunsForTask("TASK-003")
		for _, r := range runs {
			if r.LogPath != "" {
				if data, err := os.ReadFile(r.LogPath); err == nil {
					tail := data[len(data)-min(len(data), 2000):]
					specDebug := ""
					if sd, err := os.ReadFile(filepath.Join(fmt.Sprint(leak["worktree"]), "mock-spec-debug.txt")); err == nil {
						specDebug = "\nspec(len=" + fmt.Sprint(len(sd)) + "): " + string(sd[:min(len(sd), 200)]) + "..." + string(sd[len(sd)-min(len(sd), 200):])
					}
					t.Fatalf("redaction marker missing; log tail:\n%s%s", string(tail), specDebug)
				}
			}
		}
		t.Fatalf("redaction marker missing and no log readable:\n%s", logs)
	}
	st := openStore(t)
	runs, _ := st.RunsForTask("TASK-003")
	if len(runs) == 0 || runs[len(runs)-1].LogPath == "" {
		t.Fatal("no log path recorded")
	}
	if info, err := os.Stat(runs[len(runs)-1].LogPath); err != nil || info.Size() > 60000 {
		t.Fatalf("log not size-capped: %v", err)
	}
}

func TestCancelKillsProcessTree(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	spec := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("sleep", "Sleeps with child", map[string]any{
			"timeout_seconds": 300,
			"options": map[string]any{"mock": map[string]any{
				"sleep": 60, "spawn_child": map[string]any{"seconds": 120}}}})}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")

	cmd := exec.Command(guyBin, "run", "--approve")
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	st := openStore(t)
	deadline := time.Now().Add(60 * time.Second)
	var worktree string
	for time.Now().Before(deadline) {
		tasks, _ := st.ListTasks("", "")
		if len(tasks) > 0 && tasks[0].Status == "running" {
			worktree = tasks[0].Worktree
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if worktree == "" {
		t.Fatal("task never started")
	}
	childPidFile := filepath.Join(worktree, "mock-child.pid")
	for time.Now().Before(deadline) {
		if _, err := os.Stat(childPidFile); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	data, err := os.ReadFile(childPidFile)
	if err != nil {
		t.Fatal("child pid file missing")
	}
	childPid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	if !process.PidAlive(childPid) {
		t.Fatal("child should be alive before cancel")
	}
	if out, code := e.run("cancel", "TASK-001"); code != 0 || !strings.Contains(out, "cancelled") {
		t.Fatalf("cancel: %d %s", code, out)
	}
	for time.Now().Before(deadline) {
		task, _ := st.GetTask("TASK-001")
		if task != nil && task.Status == "cancelled" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	_ = cmd.Wait()
	killDeadline := time.Now().Add(20 * time.Second)
	for process.PidAlive(childPid) && time.Now().Before(killDeadline) {
		time.Sleep(500 * time.Millisecond)
	}
	if process.PidAlive(childPid) {
		t.Fatal("orphaned child survived cancellation")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func TestRestartRecovery(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	spec := map[string]any{"prompt": "p", "tasks": []any{mockTask("a", "A", nil)}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")

	// fabricate a run whose owner process is dead, with the task stuck running
	dead := exec.Command(guyBin, "--version")
	_ = dead.Run()
	deadPid := 40000 // extremely unlikely to exist; verified below
	if process.PidAlive(deadPid) {
		t.Skip("could not find a dead pid for the fixture")
	}
	st := openStore(t)
	task, _ := st.GetTask("TASK-001")
	if task == nil {
		t.Fatal("task missing")
	}
	task.Status = "running"
	task.StartedAt = "2026-01-01T00:00:00Z"
	if err := st.UpdateTask(task); err != nil {
		t.Fatal(err)
	}
	runID, _ := st.NextID("RUN")
	if err := st.InsertRun(&tasks.RunRecord{
		ID: runID, TaskID: task.ID, Adapter: "mock", Argv: []string{"mock"},
		Cwd: repo, Pid: 399999, OwnerPid: deadPid, Status: "running",
		StartedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	e.mustRun("status") // reconcile runs here
	fresh := taskByID(t, "TASK-001")
	if fresh["status"] != "failed" {
		t.Fatalf("interrupted task must be failed, got %v", fresh["status"])
	}
	if !strings.Contains(fmt.Sprint(fresh["exit_reason"]), "unexpectedly") {
		t.Fatalf("exit reason: %v", fresh["exit_reason"])
	}
	run, _ := st.GetRun(runID)
	if run == nil || run.FailureClass != "interrupted" {
		t.Fatalf("run not reconciled: %+v", run)
	}
}

// ---- review & integration ----

func TestReviewTestsEvidenceAndIntegration(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	baseline := headOf(t, repo)
	check := []string{guyBin, "mock-agent", "--check-file", "feature.txt", "hello"}
	spec := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("a", "With tests", map[string]any{
			"required_tests": [][]string{check},
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "feature.txt", "content": "hello"}}}}})}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--approve")
	e.mustRun("approve", "TASK-001")

	// gate: tests not run yet -> integrate refuses
	if out, code := e.run("integrate", "--approve", "--plan", "PLAN-001"); code != 1 ||
		!strings.Contains(out, "required tests not passing") {
		t.Fatalf("tests gate: %d\n%s", code, out)
	}
	// record evidence
	payload, _ := e.runJSON("review", "TASK-001", "--run-tests")
	results, _ := payload["test_results"].([]any)
	if len(results) != 1 {
		t.Fatalf("test results: %v", payload["test_results"])
	}
	if !strings.Contains(fmt.Sprint(payload["handoff_md"]), "Handoff: TASK-001") {
		t.Fatal("handoff missing")
	}
	// dry run then approve
	out := e.mustRun("integrate", "--dry-run", "--plan", "PLAN-001")
	if !strings.Contains(out, "INTEGRATION DRY RUN") || !strings.Contains(out, "all gates pass") {
		t.Fatalf("integrate dry-run:\n%s", out)
	}
	out = e.mustRun("integrate", "--approve", "--plan", "PLAN-001")
	if !strings.Contains(out, "integration complete") || !strings.Contains(out, "Nothing was pushed") {
		t.Fatalf("integrate:\n%s", out)
	}
	if s := taskByID(t, "TASK-001")["status"]; s != "integrated" {
		t.Fatalf("status: %v", s)
	}
	if headOf(t, repo) != baseline {
		t.Fatal("default branch must be untouched by integration")
	}
	ib := "guy/plan-PLAN-001-integration"
	if out := gitOut(t, repo, "log", "--oneline", "main.."+ib); !strings.Contains(out, "guy: integrate TASK-001") {
		t.Fatalf("integration branch log:\n%s", out)
	}
	if out := gitOut(t, repo, "show", ib+":feature.txt"); out != "hello" {
		t.Fatalf("integration branch content: %q", out)
	}
}

func TestIntegrationConflictDetected(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	baseline := headOf(t, repo)
	spec := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("a", "Claims file", map[string]any{
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "shared.txt", "content": "version A"}}}}}),
		mockTask("b", "Competing claim", map[string]any{
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "shared.txt", "content": "version B"}}}}}),
	}}
	writeSpec(t, filepath.Join(e.root, "plan.json"), spec)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan.json"))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--approve", "--task", "TASK-001")
	e.mustRun("run", "--approve", "--task", "TASK-002")
	e.mustRun("approve", "TASK-001")
	e.mustRun("approve", "TASK-002")
	out, code := e.run("integrate", "--approve", "--plan", "PLAN-001")
	if code != 1 || !strings.Contains(strings.ToUpper(out), "CONFLICT") {
		t.Fatalf("conflict must stop integration: %d\n%s", code, out)
	}
	if s := taskByID(t, "TASK-001")["status"]; s != "integrated" {
		t.Fatalf("first task should be integrated: %v", s)
	}
	if s := taskByID(t, "TASK-002")["status"]; s != "approved" {
		t.Fatalf("conflicting task must remain approved (not forced): %v", s)
	}
	if headOf(t, repo) != baseline {
		t.Fatal("default branch must be untouched")
	}
	if out := gitOut(t, repo, "show", "guy/plan-PLAN-001-integration:shared.txt"); out != "version A" {
		t.Fatalf("integration branch kept first task's version: %q", out)
	}
	a, b := taskByID(t, "TASK-001"), taskByID(t, "TASK-002")
	if data, _ := os.ReadFile(filepath.Join(fmt.Sprint(b["worktree"]), "shared.txt")); string(data) != "version B" {
		t.Fatal("conflicting worktree content must be preserved")
	}
	_ = a
}

func TestCleanRefusesDirtyAndWorksAfterIntegration(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")

	// plan 1: cancel leaves a dirty worktree that clean must refuse to delete
	spec1 := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("a", "A", map[string]any{
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "x.txt", "content": "x"}}}}})}}
	writeSpec(t, filepath.Join(e.root, "plan1.json"), spec1)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan1.json"))
	e.mustRun("approve", "PLAN-001")
	e.mustRun("run", "--approve")
	e.mustRun("cancel", "TASK-001")
	if out, code := e.run("clean", "--task", "TASK-001"); code != 1 || !strings.Contains(out, "refuses") {
		t.Fatalf("clean must refuse dirty worktree: %d %s", code, out)
	}

	// plan 2: full happy path, then clean removes worktree and merged branch
	spec2 := map[string]any{"prompt": "p", "tasks": []any{
		mockTask("b", "B", map[string]any{
			"options": map[string]any{"mock": map[string]any{
				"write": []any{map[string]any{"path": "y.txt", "content": "y"}}}}})}}
	writeSpec(t, filepath.Join(e.root, "plan2.json"), spec2)
	e.mustRun("plan", "--file", filepath.Join(e.root, "plan2.json"))
	e.mustRun("approve", "PLAN-002")
	e.mustRun("run", "--approve")
	e.mustRun("approve", "TASK-002")
	e.mustRun("integrate", "--approve", "--plan", "PLAN-002")
	out := e.mustRun("clean", "--task", "TASK-002", "--branch")
	if !strings.Contains(out, "deleted branch") {
		t.Fatalf("clean:\n%s", out)
	}
	task := taskByID(t, "TASK-002")
	if _, err := os.Stat(fmt.Sprint(task["worktree"])); !os.IsNotExist(err) {
		t.Fatal("worktree still exists after clean")
	}
	if out := gitOut(t, repo, "branch", "--list", fmt.Sprint(task["branch"])); out != "" {
		t.Fatalf("branch not deleted: %s", out)
	}
	if out := gitOut(t, repo, "show", "guy/plan-PLAN-002-integration:y.txt"); out != "y" {
		t.Fatalf("cleaned branch content must survive in integration branch: %q", out)
	}
}

// ---- migration & demo ----

func TestMigrateFromPythonState(t *testing.T) {
	e := newEnv(t)
	// create "python" state in a separate dir with the same schema
	pyData := filepath.Join(e.root, "pydata")
	t.Setenv("GUY_DATA_DIR", pyData)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "frompython")
	e.mustRun("plan", "legacy objective")

	// switch to fresh Go state and migrate
	goData := filepath.Join(e.root, "godata")
	t.Setenv("GUY_DATA_DIR", goData)
	out := e.mustRun("migrate", "--check", "--from", pyData)
	if !strings.Contains(out, "repos: 1") || !strings.Contains(out, "compatible") {
		t.Fatalf("migrate --check:\n%s", out)
	}
	out = e.mustRun("migrate", "--from", pyData)
	if !strings.Contains(out, "migration complete") || !strings.Contains(out, "backup") {
		t.Fatalf("migrate:\n%s", out)
	}
	// backups written next to the source
	entries, _ := os.ReadDir(pyData)
	backups := 0
	for _, en := range entries {
		if strings.Contains(en.Name(), "pre-go-backup") {
			backups++
		}
	}
	if backups == 0 {
		t.Fatal("no backup file created")
	}
	// migrated state is usable: tasks visible, repo resolvable
	payload, _ := e.runJSON("tasks")
	if tasks, _ := payload["tasks"].([]any); len(tasks) != 1 {
		t.Fatalf("migrated tasks: %v", payload["tasks"])
	}
	if out := e.mustRun("inspect", "--repo", "frompython"); !strings.Contains(out, "frompython") {
		t.Fatalf("migrated repo unusable:\n%s", out)
	}
}

func TestDemoEndToEnd(t *testing.T) {
	e := newEnv(t)
	out, code := e.run("demo")
	if code != 0 {
		t.Fatalf("demo failed:\n%s", out)
	}
	for _, want := range []string{"demo complete", "unchanged: true", "blocked"} {
		if !strings.Contains(out, want) {
			t.Fatalf("demo output missing %q:\n%s", want, out)
		}
	}
	// demo is re-runnable
	if out, code := e.run("demo", "--keep"); code != 0 {
		t.Fatalf("demo --keep failed:\n%s", out)
	}
	if out, code := e.run("demo"); code != 0 {
		t.Fatalf("demo rerun failed (%d):\n%s", code, out)
	}
}

func TestEventsAuditTrail(t *testing.T) {
	e := newEnv(t)
	repo := makeRepo(t, filepath.Join(e.root, "r"), "main", nil, false)
	e.mustRun("repo", "add", repo, "--alias", "r")
	out := e.mustRun("events", "--limit", "10")
	if !strings.Contains(out, "repo_added") {
		t.Fatalf("events:\n%s", out)
	}
}
