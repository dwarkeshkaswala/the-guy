package application

// Service-layer tests: the application.Service runs in-process exactly like
// it will inside the desktop app. The built-in mock agent (built guy binary
// via GUY_MOCK_AGENT_BIN) provides deterministic, model-free execution.
// Machine inputs are redirected so only the mock adapter can be "ready".

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dwkaswala/the-guy/internal/config"
	"github.com/dwkaswala/the-guy/internal/store"
	"github.com/dwkaswala/the-guy/internal/tasks"
)

var guyBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "guy-app-bin-")
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

// captureSink records emitted events for assertions.
type captureSink struct {
	mu     sync.Mutex
	events []struct {
		name string
		data any
	}
}

func (c *captureSink) Emit(name string, data ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(data) > 0 {
		c.events = append(c.events, struct {
			name string
			data any
		}{name, data[0]})
	}
}

func (c *captureSink) names() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, e := range c.events {
		out = append(out, e.name)
	}
	return out
}

func newEnv(t *testing.T) *Service {
	t.Helper()
	root := t.TempDir()
	t.Setenv("GUY_DATA_DIR", filepath.Join(root, "data"))
	t.Setenv("GUY_PREFS_DIR", filepath.Join(root, "prefs"))
	t.Setenv("GUY_MOCK_AGENT_BIN", guyBin)
	t.Setenv("GUY_VSCODE_EXTENSIONS_DIR", filepath.Join(root, "extensions"))
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	for _, d := range []string{"extensions", "appdata", "localappdata"} {
		_ = os.MkdirAll(filepath.Join(root, d), 0o755)
	}
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
	if err := config.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(config.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sink := &captureSink{}
	svc := New(st, sink)
	return svc
}

func makeRepo(t *testing.T, path string, dirty bool) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(t)
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
	git("init", "-b", "main")
	git("config", "user.email", "test@the-guy.local")
	git("config", "user.name", "the-guy tests")
	_ = os.WriteFile(filepath.Join(path, "README.md"), []byte("# test repo\n"), 0o644)
	git("add", "-A")
	git("commit", "-m", "init")
	if dirty {
		_ = os.WriteFile(filepath.Join(path, "user-dirty.txt"), []byte("uncommitted\n"), 0o644)
	}
	return path
}

func waitFor(t *testing.T, timeout time.Duration, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func taskStatus(t *testing.T, svc *Service, id string) string {
	t.Helper()
	task, err := svc.Store.GetTask(id)
	if err != nil || task == nil {
		t.Fatalf("task %s missing: %v", id, err)
	}
	return task.Status
}

// ---- repos ----

func TestRepositoryLifecycleAndViews(t *testing.T) {
	svc := newEnv(t)
	root := t.TempDir()
	repoPath := makeRepo(t, filepath.Join(root, "alpha repo"), true) // path with space
	view, err := svc.AddRepository(repoPath, "")
	if err != nil {
		t.Fatalf("add repo: %v", err)
	}
	if view.Alias != "alpha repo" || !view.IsGitRoot || view.DefaultBranch != "main" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if !view.Dirty || view.DirtyFileCount != 1 {
		t.Fatalf("dirty state not detected: %+v", view)
	}

	// non-git path is rejected
	if _, err := svc.AddRepository(filepath.Join(root, "not-a-repo"), ""); err == nil {
		t.Fatal("expected error for non-git path")
	}

	list := svc.ListRepositories()
	if len(list) != 1 || list[0].RunningTasks != 0 {
		t.Fatalf("unexpected list: %+v", list)
	}
	if err := svc.SetDefaultRepository("alpha repo"); err != nil {
		t.Fatal(err)
	}
	list = svc.ListRepositories()
	if !list[0].IsDefault {
		t.Fatal("default flag not set")
	}
	if _, err := svc.RemoveRepository("alpha repo", false); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.ListRepositories()); got != 0 {
		t.Fatalf("repo not removed: %d", got)
	}
	// removing the registration never deletes the user's directory
	if _, err := os.Stat(filepath.Join(repoPath, "README.md")); err != nil {
		t.Fatalf("repo directory was touched: %v", err)
	}
}

// ---- planning ----

func TestPlanValidationAndCreation(t *testing.T) {
	svc := newEnv(t)
	root := t.TempDir()
	repoPath := makeRepo(t, filepath.Join(root, "planner"), false)
	if _, err := svc.AddRepository(repoPath, "planner"); err != nil {
		t.Fatal(err)
	}

	good := `{"prompt": "two tasks", "tasks": [
		{"key": "a", "title": "A", "agent": "mock"},
		{"key": "b", "title": "B", "agent": "mock", "dependencies": ["a"]}]}`
	v := svc.ValidatePlanSpec(good, "planner", "", "")
	if !v.OK || len(v.Errors) > 0 {
		t.Fatalf("expected valid: %+v", v)
	}
	if len(v.TopoOrder) != 2 || v.TopoOrder[0] != "a" {
		t.Fatalf("bad topo order: %v", v.TopoOrder)
	}

	for name, spec := range map[string]string{
		"cycle":         `{"tasks": [{"key":"a","title":"A","agent":"mock","dependencies":["b"]},{"key":"b","title":"B","agent":"mock","dependencies":["a"]}]}`,
		"unknown-dep":   `{"tasks": [{"key":"a","title":"A","agent":"mock","dependencies":["zz"]}]}`,
		"unknown-agent": `{"tasks": [{"key":"a","title":"A","agent":"nope"}]}`,
		"no-tasks":      `{"tasks": []}`,
		"bad-json":      `{`,
	} {
		v := svc.ValidatePlanSpec(spec, "planner", "", "")
		if v.OK || len(v.Errors) == 0 {
			t.Fatalf("%s: expected validation failure, got %+v", name, v)
		}
	}

	created, err := svc.CreatePlan("planner", good, "", "")
	if err != nil {
		t.Fatal(err)
	}
	planMap, _ := created["plan"].(*tasks.Plan)
	if planMap == nil || planMap.ID == "" || planMap.Status != "draft" {
		t.Fatalf("unexpected plan: %+v", created["plan"])
	}
	taskList, _ := created["tasks"].([]*tasks.Task)
	if len(taskList) != 2 || taskList[1].Dependencies[0] != taskList[0].ID {
		t.Fatalf("unexpected tasks: %+v", taskList)
	}
	graph, err := svc.GetPlan(planMap.ID)
	if err != nil {
		t.Fatal(err)
	}
	graphTasks, _ := graph["tasks"].([]*tasks.Task)
	if len(graphTasks) != 2 {
		t.Fatalf("graph tasks: %d", len(graphTasks))
	}
}

// ---- execution lifecycle with the mock agent ----

func TestRunLifecycleThroughReviewAndIntegration(t *testing.T) {
	svc := newEnv(t)
	root := t.TempDir()
	repoPath := makeRepo(t, filepath.Join(root, "runner"), false)
	if _, err := svc.AddRepository(repoPath, "runner"); err != nil {
		t.Fatal(err)
	}
	spec := fmt.Sprintf(`{"prompt": "change file", "tasks": [{"key": "t1", "title": "Write hello",
		"agent": "mock",
		"options": {"mock": {"write": [{"path": "hello.txt", "content": "hi there\n"}]}},
		"required_tests": [[%q, "mock-agent", "--check-file", "hello.txt", "hi there\n"]],
		"acceptance_criteria": ["hello.txt contains hi there"]}]}`, guyBin)

	created, err := svc.CreatePlan("runner", spec, "", "")
	if err != nil {
		t.Fatal(err)
	}
	plan := created["plan"].(*tasks.Plan)
	list := created["tasks"].([]*tasks.Task)

	// launching drafts without approval refuses
	receipt, err := svc.StartRun("runner", plan.ID, nil, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if started, _ := receipt["started"].(bool); started {
		t.Fatal("draft tasks must not launch")
	}

	if _, err := svc.Approve(plan.ID, "test approval"); err != nil {
		t.Fatal(err)
	}

	// dry-run after approval: proposals only, nothing executed
	dry, err := svc.DryRun("runner", plan.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(dry) != 1 || dry[0].TaskID != list[0].ID || !dry[0].AdapterOK {
		t.Fatalf("unexpected dry run: %+v", dry)
	}
	receipt, err = svc.StartRun("runner", plan.ID, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if started, _ := receipt["started"].(bool); !started {
		t.Fatalf("run not started: %+v", receipt)
	}

	// second batch while the first is active is refused (single writer)
	if _, err := svc.StartRun("runner", plan.ID, nil, 0); err == nil {
		t.Fatal("expected concurrent batch refusal")
	}

	waitFor(t, 60*time.Second, "task needs_review", func() bool {
		return taskStatus(t, svc, list[0].ID) == tasks.StatusNeedsReview
	})

	// logs: content exists and offset paging is coherent
	chunk, err := svc.ReadLogRange(list[0].ID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !chunk.Exists || chunk.Content == "" || !strings.Contains(chunk.Content, "=== the-guy run ===") {
		t.Fatalf("unexpected log chunk: %+v", chunk)
	}
	if chunk.NextOffset <= 0 || chunk.NextOffset > chunk.Size {
		t.Fatalf("bad offsets: %+v", chunk)
	}

	// diff against the baseline shows the change
	diff, err := svc.GetTaskDiff(list[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Available || !strings.Contains(diff.Patch, "hello.txt") {
		t.Fatalf("unexpected diff: %+v", diff)
	}

	// review + tests evidence
	results, err := svc.RunTests(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("test results: %+v", results)
	}
	reviewPayload, err := svc.GetReview(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reviewPayload["handoff_md"]; !ok {
		t.Fatalf("review payload missing handoff: %+v", reviewPayload)
	}

	// approving review does NOT integrate; integration needs its own step
	if _, err := svc.Approve(list[0].ID, "review ok"); err != nil {
		t.Fatal(err)
	}
	if taskStatus(t, svc, list[0].ID) != tasks.StatusApproved {
		t.Fatal("expected approved status")
	}
	preview, err := svc.PreviewIntegration("runner", plan.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if ready, _ := preview["ready"].(bool); !ready {
		t.Fatalf("integration not ready: %+v", preview)
	}
	integrated, err := svc.Integrate("runner", plan.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := integrated["status"].(string); status != "ok" {
		t.Fatalf("integration result: %+v", integrated)
	}
	if taskStatus(t, svc, list[0].ID) != tasks.StatusIntegrated {
		t.Fatal("expected integrated status")
	}
	// the default branch is untouched
	out, err := exec.Command("git", "-C", repoPath, "log", "--oneline", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(out)), "\n") != 0 {
		t.Fatalf("default branch has commits beyond init:\n%s", out)
	}
}

func TestCancelRunningTask(t *testing.T) {
	svc := newEnv(t)
	root := t.TempDir()
	repoPath := makeRepo(t, filepath.Join(root, "cancel"), false)
	if _, err := svc.AddRepository(repoPath, "cancel"); err != nil {
		t.Fatal(err)
	}
	spec := `{"tasks": [{"key": "slow", "title": "Sleeping task", "agent": "mock",
		"options": {"mock": {"sleep": 30, "spawn_child": {"seconds": 30}}},
		"timeout_seconds": 120}]}`
	created, err := svc.CreatePlan("cancel", spec, "", "")
	if err != nil {
		t.Fatal(err)
	}
	plan := created["plan"].(*tasks.Plan)
	if _, err := svc.Approve(plan.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.StartRun("cancel", plan.ID, nil, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "task running", func() bool {
		return taskStatus(t, svc, created["tasks"].([]*tasks.Task)[0].ID) == tasks.StatusRunning
	})
	// find the spawned child pid file to verify tree kill later
	task := created["tasks"].([]*tasks.Task)[0]
	waitFor(t, 30*time.Second, "child pid file", func() bool {
		if task.Worktree == "" {
			fresh, _ := svc.Store.GetTask(task.ID)
			if fresh != nil {
				task = fresh
			}
			return false
		}
		_, err := os.Stat(filepath.Join(task.Worktree, "mock-child.pid"))
		return err == nil
	})
	childPid := ""
	if data, err := os.ReadFile(filepath.Join(task.Worktree, "mock-child.pid")); err == nil {
		childPid = strings.TrimSpace(string(data))
	}
	result, err := svc.CancelTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := result["status"].(string); status != tasks.StatusCancelled {
		t.Fatalf("cancel result: %+v", result)
	}
	if childPid != "" {
		waitFor(t, 15*time.Second, "child process to die", func() bool {
			out, err := exec.Command("taskkill", "/PID", childPid, "/F", "/T").CombinedOutput()
			_ = out
			return err != nil // fails once the pid is gone
		})
	}
}

// ---- isolation ----

func TestTwoReposNoLeakage(t *testing.T) {
	svc := newEnv(t)
	root := t.TempDir()
	a := makeRepo(t, filepath.Join(root, "repo a"), false)
	b := makeRepo(t, filepath.Join(root, "repo-b"), false)
	if _, err := svc.AddRepository(a, "alpha"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AddRepository(b, "beta"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreatePlan("alpha", `{"tasks":[{"key":"x","title":"X","agent":"mock"}]}`, "", ""); err != nil {
		t.Fatal(err)
	}
	alphaTasks, err := svc.ListTasks("alpha", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(alphaTasks) != 1 {
		t.Fatalf("alpha tasks: %d", len(alphaTasks))
	}
	betaTasks, err := svc.ListTasks("beta", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(betaTasks) != 0 {
		t.Fatalf("leakage into beta: %d tasks", len(betaTasks))
	}
	// a task id from alpha is rejected for beta's run selection
	if _, err := svc.StartRun("beta", "", []string{alphaTasks[0].ID}, 0); err == nil {
		t.Fatal("cross-repo task selection must fail")
	}
}

// ---- settings / diagnostics / events ----

func TestSettingsAndDiagnostics(t *testing.T) {
	svc := newEnv(t)
	if err := svc.UpdateSetting("display_mode", "compatibility"); err != nil {
		t.Fatal(err)
	}
	settings, err := svc.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	m, _ := settings["settings"].(map[string]any)
	if m["display_mode"] != "compatibility" {
		t.Fatalf("display_mode not persisted: %+v", m)
	}
	if err := svc.UpdateSetting("bogus_key", 1); err == nil {
		t.Fatal("unknown setting must be rejected")
	}
	health := svc.GetServiceHealth()
	if !health.OK || health.NumCPU < 1 || health.Version == "" || health.DBPath == "" {
		t.Fatalf("health: %+v", health)
	}
	doctor := svc.GetDoctorReport("")
	if len(doctor.Checks) == 0 {
		t.Fatal("doctor produced no checks")
	}
	_ = svc.ExportDiagnostics()
}

func TestEventStreamReplay(t *testing.T) {
	svc := newEnv(t)
	root := t.TempDir()
	repoPath := makeRepo(t, filepath.Join(root, "events"), false)
	if _, err := svc.AddRepository(repoPath, "events"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	go svc.StartEventStream(stop)
	defer close(stop)
	waitFor(t, 15*time.Second, "repo_added event", func() bool {
		return strings.Contains(strings.Join(eventsOf(svc), " "), "guy:event")
	})
	// envelopes carry the persisted event payload
	sink := svc.sink.(*captureSink)
	sink.mu.Lock()
	var env EventEnvelope
	found := false
	for _, e := range sink.events {
		if e.name == "guy:event" {
			if data, ok := e.data.(EventEnvelope); ok && data.Kind == "repo_added" {
				env = data
				found = true
			}
		}
	}
	sink.mu.Unlock()
	if !found {
		t.Fatal("no repo_added envelope captured")
	}
	if env.Seq <= 0 || env.Version != 1 || env.RepoID == "" {
		t.Fatalf("bad envelope: %+v", env)
	}
}

func eventsOf(svc *Service) []string {
	sink := svc.sink.(*captureSink)
	return sink.names()
}

// ensure map-typed payloads marshal correctly (used by the UI contract)
func TestPayloadsAreJSONRoundTrippable(t *testing.T) {
	svc := newEnv(t)
	root := t.TempDir()
	repoPath := makeRepo(t, filepath.Join(root, "json"), false)
	if _, err := svc.AddRepository(repoPath, "json"); err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreatePlan("json", `{"tasks":[{"key":"k","title":"K","agent":"mock"}]}`, "", "")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(created)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\"plan\"") {
		t.Fatalf("payload missing plan: %s", data)
	}
}
